package application

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"streamtool-relay/internal/persistence"
	"strings"
	"testing"
)

func TestUserSecurityBoundaries(t *testing.T) {
	a := &API{WebDir: "../../../apps/web"}
	h := a.Handler()
	for path, want := range map[string]int{"/dashboard": 200, "/routing": 404, "/streams": 404} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatalf("web route %s: got %d, want %d", path, w.Code, want)
		}
	}
	for _, path := range []string{"/v1/auth/register", "/v1/auth/login", "/v1/me/source/stop"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
		if w.Code != 403 {
			t.Fatalf("CSRF %s: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	a.sessionCookie(w, "opaque", 600)
	c := w.Result().Cookies()[0]
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatal("insecure cookie")
	}
}
func TestUsersDatabase(t *testing.T) {
	database := filepath.Join(t.TempDir(), "control.sqlite")
	ctx := context.Background()
	s, err := persistence.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, "../../sqlite-migrations"); err != nil {
		t.Fatal(err)
	}
	a := &API{Store: s, AdminToken: "operator", SetupToken: "installation-secret-long-enough", WebDir: "../../../apps/web"}
	h := a.Handler()
	call := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Streamtool", "1")
		r.Header.Set("X-Setup-Token", a.SetupToken)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	credentials := func(password, code string) map[string]string {
		return map[string]string{"password": password, "recovery_code": code}
	}
	c1, enrollment := enrollTestOwner(t, a, h, "correct-password-123")
	c2 := &http.Cookie{Name: "streamtool_session", Value: "untrusted-other-account"}
	if w := call("POST", "/v1/auth/setup", credentials("second-password-123", ""), nil); w.Code != 409 {
		t.Fatal("second owner admitted", w.Code)
	}
	if w := call("POST", "/v1/auth/register", credentials("second-password-123", ""), nil); w.Code != 404 {
		t.Fatal("registration route active", w.Code)
	}
	if w := call("POST", "/v1/auth/login", credentials("incorrect-password", enrollment.Codes[0]), nil); w.Code != 401 {
		t.Fatal("wrong password accepted")
	}
	if w := call("POST", "/v1/auth/login", credentials("correct-password-123", enrollment.Codes[0]), nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("GET", "/v1/me", nil, c2); w.Code != 401 {
		t.Fatal("foreign session admitted")
	}
	for _, cookie := range []*http.Cookie{nil, c2} {
		if w := call("GET", "/v1/me/source", nil, cookie); w.Code != 401 {
			t.Fatal("source key exposed without owner session", w.Code)
		}
	}
	for _, path := range []string{"/v1/me/channels", "/v1/me/channels/unused", "/v1/me/tools", "/v1/me/source/title", "/v1/me/source/destinations"} {
		if w := call("GET", path, nil, c1); w.Code != 404 {
			t.Fatal("removed route active", path, w.Code)
		}
	}

	created := call("POST", "/v1/me/source", nil, c1)
	if created.Code != 200 {
		t.Fatal(created.Body.String())
	}
	var result struct {
		SourceID string `json:"source_id"`
		Key      string `json:"ingest_key"`
	}
	json.Unmarshal(created.Body.Bytes(), &result)
	if result.Key == "" {
		t.Fatal("no ingest key")
	}
	if result.SourceID == "" {
		t.Fatal("no source ID")
	}
	if w := call("GET", "/v1/me/source", nil, c1); w.Code != 200 || !strings.Contains(w.Body.String(), `"source_id":"`+result.SourceID+`"`) || !strings.Contains(w.Body.String(), result.Key) {
		t.Fatalf("source read route failed: %d %s", w.Code, w.Body.String())
	}
	var connectionID, sessionID string
	if err = s.Pool.QueryRow(ctx, `INSERT INTO ingest_connections(edge_id,connection_id,stream_id,protocol,status,connected_at,last_seen_at) VALUES('test-edge','routing-disable',?1,'srt','CONNECTED',strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now')) RETURNING id`, result.SourceID).Scan(&connectionID); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, `INSERT INTO stream_sessions(stream_id,ingest_connection_id,phase) VALUES(?1,?2,'LIVE') RETURNING id`, result.SourceID, connectionID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if w := call("DELETE", "/v1/me/source", nil, c1); w.Code != 405 {
		t.Fatalf("routing disable route is still active: %d %s", w.Code, w.Body.String())
	}
	var desired, phase string
	var operatorStopped bool
	if err = s.Pool.QueryRow(ctx, `SELECT desired,phase,operator_stopped FROM stream_sessions WHERE id=?1`, sessionID).Scan(&desired, &phase, &operatorStopped); err != nil || phase != "LIVE" || operatorStopped {
		t.Fatal("rejected routing disable affected active session", desired, phase, operatorStopped, err)
	}
	if w := call("POST", "/v1/me/source", nil, c1); w.Code != 200 || !strings.Contains(w.Body.String(), result.Key) || !strings.Contains(w.Body.String(), result.SourceID) {
		t.Fatalf("idempotent provisioning changed source identity: %d %s", w.Code, w.Body.String())
	}
	if w := call("GET", "/v1/me/source/slate", nil, c1); w.Code != 200 || !strings.Contains(w.Body.String(), `"on_source_loss":true`) || !strings.Contains(w.Body.String(), `"forced":false`) {
		t.Fatal("default slate settings unavailable", w.Code, w.Body.String())
	}
	if w := call("PUT", "/v1/me/source/slate", map[string]any{"on_source_loss": false, "forced": true, "generation": 1}, c1); w.Code != 200 || !strings.Contains(w.Body.String(), `"generation":2`) {
		t.Fatal("slate settings not saved", w.Code, w.Body.String())
	}
	if w := call("PUT", "/v1/me/source/slate", map[string]any{"on_source_loss": true, "forced": false, "generation": 1}, c1); w.Code != 409 {
		t.Fatal("stale slate settings accepted", w.Code)
	}
	if w := call("GET", "/v1/me/source/slate", nil, c2); w.Code != 401 {
		t.Fatal("slate settings leaked between accounts", w.Code)
	}
	if w := call("GET", "/v1/me/source/status", nil, c2); w.Code != 401 {
		t.Fatalf("account without source received status: %d", w.Code)
	}
	if w := call("GET", "/v1/me/source", nil, c2); w.Code != 401 {
		t.Fatalf("account without source received source: %d", w.Code)
	}
	if w := call("POST", "/v1/accounts", nil, c1); w.Code != 401 {
		t.Fatal("user gained operator access")
	}
	if w := call("POST", "/v1/me/source", nil, c1); w.Code != 200 || !strings.Contains(w.Body.String(), `"source_id":"`+result.SourceID+`"`) || !strings.Contains(w.Body.String(), result.Key) {
		t.Fatalf("source provisioning route failed: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/v1/me/camera", nil, c1); w.Code != 404 {
		t.Fatalf("removed camera route returned %d", w.Code)
	}
	if w := call("GET", "/v1/me/streams/"+result.SourceID, nil, c1); w.Code != 404 {
		t.Fatalf("removed owner stream route returned %d", w.Code)
	}
	if w := call("GET", "/", nil, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "личный кабинет") {
		t.Fatal("missing web client")
	}
	// A fresh API instance uses the persisted session.
	h = (&API{Store: s}).Handler()
	if w := call("GET", "/v1/me", nil, c1); w.Code != 200 {
		t.Fatal("session lost on restart")
	}
	if w := call("POST", "/v1/auth/logout", nil, c1); w.Code != 204 {
		t.Fatal("logout failed")
	}
	if w := call("GET", "/v1/me", nil, c1); w.Code != 401 {
		t.Fatal("logout did not revoke")
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE user_sessions SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 second')`); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/v1/me", nil, c2); w.Code != 401 {
		t.Fatal("expired session accepted")
	}
	for i := 0; i < 21; i++ {
		w := call("POST", "/v1/auth/login", credentials("wrong-password-123", "not-a-code"), nil)
		if i == 20 && w.Code != 429 {
			t.Fatalf("rate limit: %d", w.Code)
		}
	}
}
