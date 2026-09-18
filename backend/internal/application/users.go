package application

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
)

func passwordHash(password string, salt []byte) []byte {
	h, _ := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	return h
}
func sessionHash(token string) []byte { h := sha256.Sum256([]byte(token)); return h[:] }
func (a *API) sessionCookie(w http.ResponseWriter, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: "streamtool_session", Value: value, Path: "/", MaxAge: age, HttpOnly: true, Secure: !a.InsecureCookies, SameSite: http.SameSiteStrictMode})
}
func (a *API) userRoutes(w http.ResponseWriter, r *http.Request, passwords chan struct{}) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "GET" {
		origin := r.Header.Get("Origin")
		if origin != "" {
			u, err := url.Parse(origin)
			scheme := "https"
			if a.InsecureCookies {
				scheme = "http"
			}
			if err != nil || u.Scheme != scheme || !strings.EqualFold(u.Host, r.Host) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
				problem(w, 403, "same-origin request required")
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			problem(w, 403, "same-origin request required")
			return
		}
	}
	// A custom header requires a CORS preflight from foreign origins. No CORS is enabled.
	binaryFallback := r.Method == http.MethodPut && r.URL.Path == "/v1/me/source/fallback"
	if r.Method != "GET" && (r.Header.Get("X-Streamtool") != "1" || (!binaryFallback && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json"))) {
		problem(w, 403, "same-origin request required")
		return
	}
	if r.URL.Path == "/v1/auth/setup" && r.Method == "GET" {
		var required bool
		if err := a.Store.Pool.QueryRow(r.Context(), `SELECT NOT EXISTS(SELECT 1 FROM owner_mfa)`).Scan(&required); err != nil {
			problem(w, 503, "setup unavailable")
			return
		}
		writeJSON(w, 200, map[string]bool{"required": required})
		return
	}
	if r.URL.Path == "/v1/auth/setup" || r.URL.Path == "/v1/auth/setup/confirm" || r.URL.Path == "/v1/auth/totp/replace" || r.URL.Path == "/v1/auth/login" {
		if r.Method != "POST" {
			problem(w, 405, "method not allowed")
			return
		}
		a.signIn(w, r, passwords)
		return
	}
	if r.URL.Path == "/v1/auth/register" {
		problem(w, 404, "not found")
		return
	}
	token := ""
	if cookie, err := r.Cookie("streamtool_session"); err == nil {
		token = cookie.Value
	}
	decodedToken, tokenErr := base64.RawURLEncoding.DecodeString(token)
	if len(token) != 43 || tokenErr != nil || len(decodedToken) != 32 || a.Store == nil {
		problem(w, 401, "unauthorized")
		return
	}
	var account string
	err := a.Store.Pool.QueryRow(r.Context(), `SELECT u.account_id FROM user_sessions s JOIN users u ON u.id=s.user_id JOIN installation i ON i.owner_user=u.id JOIN owner_mfa m ON m.user_id=u.id WHERE s.token_hash=?1 AND s.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')`, sessionHash(token)).Scan(&account)
	if err != nil {
		problem(w, 401, "unauthorized")
		return
	}
	if r.Method != "GET" {
		allowed, err := a.allow(r.Context(), "mutation:"+account, 60, 60)
		if err != nil {
			problem(w, 503, "request limiter unavailable")
			return
		}
		if !allowed {
			w.Header().Set("Retry-After", "60")
			problem(w, 429, "too many changes")
			return
		}
	}
	if r.URL.Path == "/v1/auth/logout" && r.Method == "POST" {
		if _, err = a.Store.Pool.Exec(r.Context(), `DELETE FROM user_sessions WHERE token_hash=?1`, sessionHash(token)); err != nil {
			problem(w, 503, "logout unavailable")
			return
		}
		a.sessionCookie(w, "", -1)
		w.WriteHeader(204)
		return
	}
	if r.URL.Path == "/v1/me" && r.Method == "GET" {
		writeJSON(w, 200, map[string]string{"account_id": account})
		return
	}
	if r.URL.Path == "/v1/me/updates" {
		a.updateRoutes(w, r, token)
		return
	}
	if r.URL.Path == "/v1/me/source" || strings.HasPrefix(r.URL.Path, "/v1/me/source/") {
		a.sourceRoutes(w, r, account)
		return
	}
	problem(w, 404, "not found")
}
