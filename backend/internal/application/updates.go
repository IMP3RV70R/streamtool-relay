package application

import (
	"context"
	"errors"
	"io"
	"net/http"
	"streamtool-relay/internal/buildinfo"
	"streamtool-relay/internal/maintenance"
	"streamtool-relay/internal/release"
	"streamtool-relay/internal/updaterclient"
	"time"
)

func (a *API) updateRoutes(w http.ResponseWriter, r *http.Request, token string) {
	if !a.UpdatesEnabled || a.Updater == nil {
		problem(w, 404, "not found")
		return
	}
	if r.Method == "GET" {
		release, err := a.Updater.Catalog(r.Context())
		if err != nil {
			problem(w, 503, "update check unavailable")
			return
		}
		status, err := a.Updater.Status(r.Context(), "")
		if err != nil {
			problem(w, 503, "update status unavailable")
			return
		}
		var pending string
		if err = a.Store.Pool.QueryRow(r.Context(), `SELECT COALESCE((SELECT request_id FROM update_authorizations WHERE state='PENDING'),'')`).Scan(&pending); err != nil {
			problem(w, 503, "update status unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"installed_version": buildinfo.Version, "release": release, "host": status, "pending_request_id": pending})
		return
	}
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	var input struct {
		RequestID string `json:"request_id"`
		Digest    string `json:"release_digest"`
		Code      string `json:"code"`
	}
	defer r.Body.Close()
	body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if readErr != nil || release.Decode(body, &input) != nil {
		problem(w, 400, "invalid update request")
		return
	}
	var exists bool
	if err := a.Store.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM update_authorizations WHERE request_id=?1)`, input.RequestID).Scan(&exists); err != nil {
		problem(w, 503, "update unavailable")
		return
	}
	if !exists {
		if !a.authAdmission(w, r) {
			return
		}
		release, err := a.Updater.Catalog(r.Context())
		if err != nil {
			problem(w, 503, "update check unavailable")
			return
		}
		if release == nil || release.Digest != input.Digest {
			problem(w, 409, "update target unavailable")
			return
		}
		host, err := a.Updater.Status(r.Context(), "")
		if err != nil {
			problem(w, 503, "update status unavailable")
			return
		}
		switch host.Phase {
		case "IDLE", "SUCCEEDED", "ROLLED_BACK", "FAILED":
		default:
			problem(w, 409, "host update unavailable")
			return
		}
		var idle bool
		err = a.Store.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM edge_observation_health WHERE edge_id=?1 AND last_seen_at>strftime('%Y-%m-%dT%H:%M:%fZ','now','-6 seconds')) AND NOT EXISTS(SELECT 1 FROM ingest_connections WHERE status='CONNECTED') AND NOT EXISTS(SELECT 1 FROM stream_sessions WHERE phase NOT IN ('ENDED','FAILED')) AND NOT EXISTS(SELECT 1 FROM worker_allocations WHERE state NOT IN ('STOPPED','FAILED'))`, a.EdgeID).Scan(&idle)
		if err != nil {
			problem(w, 503, "source state unavailable")
			return
		}
		if !idle {
			problem(w, 409, "finish broadcast and confirm source disconnection before update")
			return
		}
	}
	_, err := a.authorizeUpdate(r.Context(), token, input.RequestID, input.Digest, input.Code)
	if errors.Is(err, errUpdateProof) {
		problem(w, 401, "invalid update authorization")
		return
	}
	if errors.Is(err, errUpdateConflict) {
		problem(w, 409, "update request conflict")
		return
	}
	if err != nil {
		problem(w, 503, "update authorization unavailable")
		return
	}
	var state string
	if err = a.Store.Pool.QueryRow(r.Context(), `SELECT state FROM update_authorizations WHERE request_id=?1`, input.RequestID).Scan(&state); err != nil {
		problem(w, 503, "update status unavailable")
		return
	}
	// This is the application's durable receipt, not a claim that installation started.
	writeJSON(w, 202, map[string]string{"request_id": input.RequestID, "state": state})
}

// RunUpdateDispatch drains the durable outbox on startup and thereafter. It never
// needs to retain a plaintext session token or factor. The host inbox survives API
// crashes; its identical ID/digest receipt closes the delivery/commit crash window.
func (a *API) RunUpdateDispatch(ctx context.Context) {
	if !a.UpdatesEnabled || a.Updater == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		a.dispatchUpdate(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *API) dispatchUpdate(ctx context.Context) error {
	leave, err := maintenance.Enter(a.MaintenanceDirectory)
	if err != nil {
		return err
	}
	defer leave()
	tx, err := a.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id, digest string
	var live bool
	err = tx.QueryRow(ctx, `SELECT request_id,release_digest,expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now') AND EXISTS(SELECT 1 FROM user_sessions s JOIN installation i ON i.owner_user=s.user_id JOIN owner_mfa m ON m.user_id=s.user_id WHERE s.token_hash=u.session_hash AND s.user_id=u.user_id AND s.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')) FROM update_authorizations u WHERE state='PENDING'`).Scan(&id, &digest, &live)
	if err != nil {
		return err
	}
	if !live {
		if _, err = tx.Exec(ctx, `UPDATE update_authorizations SET state='CANCELLED' WHERE request_id=?1 AND state='PENDING'`, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	status, err := a.Updater.Start(callCtx, id, digest)
	if err != nil {
		var remote *updaterclient.RemoteError
		if errors.As(err, &remote) && (remote.Code == "target_unavailable" || remote.Code == "conflict") {
			if _, err = tx.Exec(ctx, `UPDATE update_authorizations SET state='CANCELLED' WHERE request_id=?1 AND state='PENDING'`, id); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		return err // Uncertain delivery/busy: leave pending, retry with the exact same ID.
	}
	if status.ID != id || status.Digest != digest || status.Phase == "" {
		return errors.New("invalid host receipt")
	}
	if _, err = tx.Exec(ctx, `UPDATE update_authorizations SET state='DISPATCHED' WHERE request_id=?1 AND state='PENDING'`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
