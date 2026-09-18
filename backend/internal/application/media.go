package application

import (
	"net/http"
	"streamtool-relay/internal/config"
)

func (a *API) userMedia(w http.ResponseWriter, r *http.Request, source string) {
	ctx := r.Context()
	if r.Method == "GET" {
		p := config.DefaultMediaProfile()
		var generation int64
		err := a.Store.Pool.QueryRow(ctx, `SELECT width,height,fps_num,fps_den,video_kbps,audio_kbps,generation FROM source_media_profiles WHERE source_id=?1`, source).Scan(&p.Width, &p.Height, &p.FPSNum, &p.FPSDen, &p.VideoKbps, &p.AudioKbps, &generation)
		if err != nil {
			problem(w, 503, "media profile unavailable")
			return
		}
		writeJSON(w, 200, struct {
			config.MediaProfile
			Generation int64 `json:"generation"`
		}{p, generation})
		return
	}
	if r.Method != "PUT" {
		problem(w, 405, "method not allowed")
		return
	}
	var input struct {
		config.MediaProfile
		Generation int64 `json:"generation"`
	}
	if decode(w, r, &input) != nil || input.MediaProfile.Validate() != nil {
		problem(w, 422, "invalid media profile")
		return
	}
	tx, err := a.Store.Pool.Begin(ctx)
	if err != nil {
		problem(w, 503, "media profile unavailable")
		return
	}
	defer tx.Rollback(ctx)
	// Same source lock as ingest admission: a concurrent publisher cannot start
	// between checking inactivity and committing a different media contract.
	if _, err = tx.Exec(ctx, `SELECT id FROM streams WHERE id=?1`, source); err != nil {
		problem(w, 503, "media profile unavailable")
		return
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stream_sessions WHERE stream_id=?1 AND phase NOT IN ('ENDED','FAILED')) OR EXISTS(SELECT 1 FROM ingest_connections WHERE stream_id=?1 AND status='CONNECTED')`, source).Scan(&active); err != nil {
		problem(w, 503, "media profile unavailable")
		return
	}
	if active {
		problem(w, 409, "stop and disconnect source before changing media profile")
		return
	}
	tag, err := tx.Exec(ctx, `UPDATE source_media_profiles SET width=?2,height=?3,fps_num=?4,fps_den=?5,video_kbps=?6,audio_kbps=?7,generation=generation+1 WHERE source_id=?1 AND generation=?8`, source, input.Width, input.Height, input.FPSNum, input.FPSDen, input.VideoKbps, input.AudioKbps, input.Generation)
	if err != nil {
		problem(w, 503, "media profile unavailable")
		return
	}
	if tag.RowsAffected() != 1 {
		problem(w, 409, "media profile changed; reload")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_records(account_id,action,resource_type,resource_id) SELECT account_id,'UPDATE_MEDIA_PROFILE','stream',id FROM streams WHERE id=?1`, source); err != nil || tx.Commit(ctx) != nil {
		problem(w, 503, "media profile unavailable")
		return
	}
	input.Generation++
	writeJSON(w, 200, input)
}
