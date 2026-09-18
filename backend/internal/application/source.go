package application

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"streamtool-relay/internal/auth"
	"strings"
)

func (a *API) sourceRoutes(w http.ResponseWriter, r *http.Request, account string) {
	if r.URL.Path == "/v1/me/source" || r.URL.Path == "/v1/me/source/credential" {
		a.source(w, r, account)
		return
	}
	var sourceID string
	if err := a.Store.Pool.QueryRow(r.Context(), `SELECT stream_id FROM account_sources WHERE account_id=?1`, account).Scan(&sourceID); err != nil {
		problem(w, 404, "source not found")
		return
	}
	r.SetPathValue("id", sourceID)
	action := strings.TrimPrefix(r.URL.Path, "/v1/me/source/")
	if action == "fallback" {
		a.userFallback(w, r, sourceID)
		return
	}
	if action == "media" {
		a.userMedia(w, r, sourceID)
		return
	}
	if action == "slate" {
		a.userSlate(w, r, account, sourceID)
		return
	}
	if action == "outputs" || strings.HasPrefix(action, "outputs/") {
		a.userOutput(w, r, account, sourceID, action)
		return
	}
	switch r.Method + " " + action {
	case "GET status":
		a.status(w, r)
	case "POST stop":
		var sessionID string
		err := a.Store.Pool.QueryRow(r.Context(), `SELECT id FROM stream_sessions WHERE stream_id=?1 AND phase NOT IN ('ENDED','FAILED') ORDER BY started_at DESC LIMIT 1`, sourceID).Scan(&sessionID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				w.WriteHeader(204)
			} else {
				problem(w, 503, "broadcast state unavailable")
			}
			return
		}
		r.SetPathValue("id", sessionID)
		a.stopSession(w, r)
	default:
		problem(w, 404, "not found")
	}
}

// A source is provisioned once per account. Reopening the page never rotates its key.
func (a *API) source(w http.ResponseWriter, r *http.Request, account string) {
	if r.URL.Path == "/v1/me/source/credential" && r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	if a.Store == nil {
		problem(w, 503, "source unavailable")
		return
	}
	ctx := r.Context()
	if r.Method == "GET" {
		var id string
		if err := a.Store.Pool.QueryRow(ctx, `SELECT s.id FROM account_sources a JOIN streams s ON s.id=a.stream_id WHERE a.account_id=?1`, account).Scan(&id); errors.Is(err, sql.ErrNoRows) {
			problem(w, 404, "source not found")
			return
		} else if err != nil {
			problem(w, 503, "source unavailable")
			return
		}
		var delivery bool
		if err := a.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM destinations WHERE stream_id=?1 AND enabled AND NOT archived)`, id).Scan(&delivery); err != nil {
			problem(w, 503, "source unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"source_id": id, "srt_url": a.PublicSRTURL, "rtmp_url": a.PublicRTMPURL, "media_configured": a.MediaConfigured, "delivery_configured": delivery})
		return
	}
	rotate := r.URL.Path == "/v1/me/source/credential"
	if rotate {
		var in struct {
			Password string `json:"password"`
		}
		if decode(w, r, &in) != nil || len(in.Password) < 12 || len(in.Password) > 256 {
			problem(w, 400, "invalid credentials")
			return
		}
		var attempts int
		if err := a.Store.Pool.QueryRow(ctx, `INSERT INTO auth_attempts(key,count,expires_at) VALUES(?1,1,strftime('%Y-%m-%dT%H:%M:%fZ','now','+15 minutes')) ON CONFLICT(key) DO UPDATE SET count=CASE WHEN auth_attempts.expires_at<strftime('%Y-%m-%dT%H:%M:%fZ','now') THEN 1 ELSE auth_attempts.count+1 END,expires_at=CASE WHEN auth_attempts.expires_at<strftime('%Y-%m-%dT%H:%M:%fZ','now') THEN strftime('%Y-%m-%dT%H:%M:%fZ','now','+15 minutes') ELSE auth_attempts.expires_at END RETURNING count`, "source-rotation:"+account).Scan(&attempts); err != nil {
			problem(w, 503, "source unavailable")
			return
		}
		if attempts > 10 {
			w.Header().Set("Retry-After", "900")
			problem(w, 429, "too many attempts")
			return
		}
		var hash, salt []byte
		if err := a.Store.Pool.QueryRow(ctx, `SELECT password_hash,password_salt FROM users WHERE account_id=?1`, account).Scan(&hash, &salt); err != nil || subtle.ConstantTimeCompare(passwordHash(in.Password, salt), hash) != 1 {
			problem(w, 403, "invalid email or password")
			return
		}
	}
	tx, err := a.Store.Pool.Begin(ctx)
	if err != nil {
		problem(w, 503, "source unavailable")
		return
	}
	defer tx.Rollback(ctx)
	var id string
	key := ""
	err = tx.QueryRow(ctx, `SELECT stream_id FROM account_sources WHERE account_id=?1`, account).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if rotate {
			problem(w, 404, "source not found")
			return
		}
		raw := make([]byte, 24)
		if _, err = rand.Read(raw); err != nil {
			problem(w, 503, "source unavailable")
			return
		}
		key = base64.RawURLEncoding.EncodeToString(raw)
		hash, e := auth.HashKey(key)
		if e != nil {
			problem(w, 503, "source unavailable")
			return
		}
		region := a.SourceRegion
		if region == "" {
			region = "local"
		}
		err = tx.QueryRow(ctx, `INSERT INTO streams(account_id,name,region,ingest_key_hash) VALUES(?1,'Источник',?3,?2) RETURNING id`, account, hash, region).Scan(&id)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO account_sources(account_id,stream_id) VALUES(?1,?2)`, account, id)
		}
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO source_slates(source_id) VALUES(?1)`, id)
		}
	}
	if err != nil {
		problem(w, 503, "source unavailable")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO source_media_profiles(source_id) VALUES(?1) ON CONFLICT DO NOTHING`, id); err != nil {
		problem(w, 503, "source unavailable")
		return
	}
	if !rotate {
		if _, err = tx.Exec(ctx, `UPDATE streams SET enabled=true,generation=generation+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1 AND NOT enabled`, id); err != nil {
			problem(w, 503, "source unavailable")
			return
		}
	}

	if rotate {
		if _, err = tx.Exec(ctx, `SELECT id FROM streams WHERE id=?1`, id); err != nil {
			problem(w, 503, "source unavailable")
			return
		}
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stream_sessions WHERE stream_id=?1 AND phase NOT IN ('ENDED','FAILED')) OR EXISTS(SELECT 1 FROM ingest_connections WHERE stream_id=?1 AND status='CONNECTED')`, id).Scan(&active); err != nil {
			problem(w, 503, "source unavailable")
			return
		}
		if active {
			problem(w, 409, "stop and disconnect source before key rotation")
			return
		}
		raw := make([]byte, 24)
		if _, err = rand.Read(raw); err != nil {
			problem(w, 503, "source unavailable")
			return
		}
		key = base64.RawURLEncoding.EncodeToString(raw)
		hash, err := auth.HashKey(key)
		if err != nil {
			problem(w, 503, "source unavailable")
			return
		}
		if _, err = tx.Exec(ctx, `UPDATE streams SET ingest_key_hash=?2,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, id, hash); err != nil {
			problem(w, 503, "source unavailable")
			return
		}
		if _, err = tx.Exec(ctx, `INSERT INTO audit_records(account_id,action,resource_type,resource_id) VALUES(?1,'ROTATE_INGEST_KEY','stream',?2)`, account, id); err != nil {
			problem(w, 503, "source unavailable")
			return
		}
	}

	if tx.Commit(ctx) != nil {
		problem(w, 503, "source unavailable")
		return
	}

	var delivery bool
	if err = a.Store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM destinations WHERE stream_id=?1 AND enabled AND NOT archived)`, id).Scan(&delivery); err != nil {
		problem(w, 503, "source unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"source_id": id, "ingest_key": key, "srt_url": a.PublicSRTURL, "rtmp_url": a.PublicRTMPURL, "media_configured": a.MediaConfigured, "delivery_configured": delivery})
}
