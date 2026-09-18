package application

import (
	"errors"
	"net/http"

	"database/sql"
)

// userSlate manages the source's slate independently from ingest and outputs.
func (a *API) userSlate(w http.ResponseWriter, r *http.Request, account, sourceID string) {
	ctx := r.Context()
	if r.Method == http.MethodGet {
		var onSourceLoss, forced bool
		var generation int64
		err := a.Store.Pool.QueryRow(ctx, `SELECT on_source_loss,forced,generation FROM source_slates WHERE source_id=?1`, sourceID).Scan(&onSourceLoss, &forced, &generation)
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, 404, "slate not found")
			return
		}
		if err != nil {
			problem(w, 503, "slate unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"on_source_loss": onSourceLoss, "forced": forced, "generation": generation})
		return
	}
	if r.Method != http.MethodPut {
		problem(w, 405, "method not allowed")
		return
	}
	var in struct {
		OnSourceLoss bool  `json:"on_source_loss"`
		Forced       bool  `json:"forced"`
		Generation   int64 `json:"generation"`
	}
	if decode(w, r, &in) != nil || in.Generation < 1 {
		problem(w, 400, "invalid slate settings")
		return
	}
	tx, err := a.Store.Pool.Begin(ctx)
	if err != nil {
		problem(w, 503, "slate unavailable")
		return
	}
	defer tx.Rollback(ctx)
	var generation int64
	err = tx.QueryRow(ctx, `UPDATE source_slates SET on_source_loss=?2,forced=?3,generation=generation+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE source_id=?1 AND generation=?4 RETURNING generation`, sourceID, in.OnSourceLoss, in.Forced, in.Generation).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 409, "slate settings changed; reload")
		return
	}
	if err != nil {
		problem(w, 503, "slate unavailable")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_records(account_id,action,resource_type,resource_id) VALUES(?1,'UPDATE_SLATE','source_slate',?2)`, account, sourceID); err != nil {
		problem(w, 503, "slate unavailable")
		return
	}
	if tx.Commit(ctx) != nil {
		problem(w, 503, "slate unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"on_source_loss": in.OnSourceLoss, "forced": in.Forced, "generation": generation})
}
