package application

import (
	"database/sql"
	"errors"
	"net/http"
	appcrypto "streamtool-relay/internal/crypto"
	"streamtool-relay/internal/persistence"
	"strings"
	"unicode/utf8"
)

// Ownership is checked by sourceRoutes. The source lock serializes configuration
// writes; generations guard each independent output against stale mutations.
func (a *API) userOutput(w http.ResponseWriter, r *http.Request, account, stream, actionName string) {
	ctx := r.Context()
	parts := strings.Split(actionName, "/")
	collection := actionName == "outputs"
	idPath := ""
	if len(parts) >= 2 {
		idPath = parts[1]
	}
	retry := len(parts) == 3 && parts[2] == "retry"
	if collection && r.Method == "GET" {
		rows, err := a.Store.Pool.Query(ctx, `SELECT id,name,endpoint,enabled,generation FROM destinations WHERE stream_id=?1 AND NOT archived ORDER BY created_at,id`, stream)
		if err != nil {
			problem(w, 503, "output unavailable")
			return
		}
		defer rows.Close()
		list := make([]map[string]any, 0)
		for rows.Next() {
			var id, name, endpoint string
			var enabled bool
			var generation int64
			if err := rows.Scan(&id, &name, &endpoint, &enabled, &generation); err != nil {
				problem(w, 503, "output unavailable")
				return
			}
			list = append(list, map[string]any{"id": id, "name": name, "endpoint": endpoint, "enabled": enabled, "generation": generation})
		}
		if rows.Err() != nil {
			problem(w, 503, "output unavailable")
			return
		}
		writeJSON(w, 200, list)
		return
	}
	creating := collection && r.Method == "POST"
	if !(creating || len(parts) == 2 && idPath != "" && (r.Method == "PUT" || r.Method == "DELETE") || retry && idPath != "" && r.Method == "POST") {
		problem(w, 405, "method not allowed")
		return
	}

	tx, err := a.Store.Pool.Begin(ctx)
	if err != nil {
		problem(w, 503, "output unavailable")
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM streams WHERE id=?1`, stream); err != nil {
		problem(w, 503, "output unavailable")
		return
	}
	var id, name, endpoint, keyID string
	var ciphertext, nonce []byte
	var generation int64
	var enabled bool
	if !creating {
		err = tx.QueryRow(ctx, `SELECT id,name,endpoint,key_id,secret_ciphertext,secret_nonce,generation,enabled FROM destinations WHERE stream_id=?1 AND id=?2 AND NOT archived`, stream, idPath).Scan(&id, &name, &endpoint, &keyID, &ciphertext, &nonce, &generation, &enabled)
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, 404, "output not found")
			return
		}
		if err != nil {
			problem(w, 503, "output unavailable")
			return
		}
	}

	var in struct {
		Name       string `json:"name"`
		Endpoint   string `json:"endpoint"`
		Secret     string `json:"secret"`
		Enabled    bool   `json:"enabled"`
		Generation int64  `json:"generation"`
	}
	if decode(w, r, &in) != nil {
		problem(w, 400, "invalid output")
		return
	}
	if creating || r.Method == "PUT" {
		in.Name = strings.TrimSpace(in.Name)
		in.Endpoint = strings.TrimSpace(in.Endpoint)
		if in.Name == "" || utf8.RuneCountInString(in.Name) > 140 || len(in.Endpoint) > 2048 || len(in.Secret) > 4096 || strings.ContainsAny(in.Secret, "\r\n") || strings.Contains(in.Endpoint, "?") || (creating && in.Secret == "") {
			problem(w, 400, "invalid output")
			return
		}
		endpoint, err := a.DestinationPolicy.Validate(ctx, in.Endpoint)
		if err != nil {
			problem(w, 422, "invalid output endpoint")
			return
		}
		in.Endpoint = endpoint
	}
	if in.Generation != generation {
		problem(w, 409, "output changed; reload")
		return
	}

	action := ""
	switch {
	case creating || r.Method == "PUT":
		if in.Secret != "" || !creating && in.Name != name {
			if a.Keys == nil {
				problem(w, 503, "output secret unavailable")
				return
			}
			secret := []byte(in.Secret)
			if len(secret) == 0 {
				secret, err = appcrypto.Decrypt(ctx, a.Keys, appcrypto.Envelope{Ciphertext: ciphertext, Nonce: nonce, KeyID: keyID}, []byte(stream+":"+name))
				if err != nil {
					problem(w, 503, "output secret unavailable")
					return
				}
			}
			envelope, e := appcrypto.Encrypt(ctx, a.Keys, secret, []byte(stream+":"+in.Name))
			if e != nil {
				problem(w, 503, "output secret unavailable")
				return
			}
			ciphertext, nonce, keyID = envelope.Ciphertext, envelope.Nonce, envelope.KeyID
		}
		if creating {
			err = tx.QueryRow(ctx, `INSERT INTO destinations(stream_id,name,endpoint,enabled,secret_ciphertext,secret_nonce,key_id) VALUES(?1,?2,?3,?4,?5,?6,?7) RETURNING id`, stream, in.Name, in.Endpoint, in.Enabled, ciphertext, nonce, keyID).Scan(&id)
			action = "CREATE_OUTPUT"
		} else {
			_, err = tx.Exec(ctx, `UPDATE destinations SET name=?2,endpoint=?3,enabled=?4,secret_ciphertext=?5,secret_nonce=?6,key_id=?7,generation=generation+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, id, in.Name, in.Endpoint, in.Enabled, ciphertext, nonce, keyID)
			action = "UPDATE_OUTPUT"
		}
	case r.Method == "DELETE":
		_, err = tx.Exec(ctx, `UPDATE destinations SET archived=true,enabled=false,secret_ciphertext=X'',secret_nonce=X'',generation=generation+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, id)
		action = "DELETE_OUTPUT"
	case r.Method == "POST":
		if !enabled {
			problem(w, 409, "output disabled")
			return
		}
		_, err = tx.Exec(ctx, `UPDATE destinations SET generation=generation+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?1`, id)
		action = "RETRY_OUTPUT"
	default:
		problem(w, 405, "method not allowed")
		return
	}
	if err != nil {
		if persistence.IsConstraint(err) {
			problem(w, 409, "output limit reached or name already used")
			return
		}
		problem(w, 503, "output not saved")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_records(account_id,action,resource_type,resource_id) VALUES(?1,?2,'destination',?3)`, account, action, id); err != nil {
		problem(w, 503, "output not saved")
		return
	}
	if tx.Commit(ctx) != nil {
		problem(w, 503, "output not saved")
		return
	}
	if creating {
		writeJSON(w, 201, map[string]string{"id": id})
	} else {
		w.WriteHeader(204)
	}
}
