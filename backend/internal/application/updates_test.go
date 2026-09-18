package application

import (
	"context"
	"encoding/base32"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"streamtool-relay/internal/auth"
	"streamtool-relay/internal/persistence"
	"streamtool-relay/internal/updaterclient"
	"strings"
	"testing"
	"time"
)

type testUpdater struct {
	release   *updaterclient.Release
	receipt   updaterclient.Status
	calls     int
	loseReply bool
}

func (u *testUpdater) Catalog(context.Context) (*updaterclient.Release, error) { return u.release, nil }
func (u *testUpdater) Status(context.Context, string) (updaterclient.Status, error) {
	if u.receipt.Phase != "" {
		return u.receipt, nil
	}
	return updaterclient.Status{Phase: "IDLE"}, nil
}
func (u *testUpdater) Start(_ context.Context, id, digest string) (updaterclient.Status, error) {
	u.calls++
	u.receipt = updaterclient.Status{ID: id, Digest: digest, Phase: "REQUESTED"}
	if u.loseReply {
		u.loseReply = false
		return updaterclient.Status{}, errors.New("lost host response")
	}
	return u.receipt, nil
}

func updateCall(a *API, cookie *http.Cookie, body string, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "https://relay.example/v1/me/updates", strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	r.Header.Set("X-Streamtool", "1")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func TestOwnerUpdateRouteAndDurableDispatch(t *testing.T) {
	a := limiterAPI(t)
	cookie, enrollment := enrollTestOwner(t, a, a.Handler(), "owner-password-long-enough")
	host := &testUpdater{release: &updaterclient.Release{Digest: strings.Repeat("a", 64), Version: "test"}, loseReply: true}
	a.Updater = host
	a.UpdatesEnabled = true
	a.EdgeID = "test-edge"
	ctx := context.Background()
	if _, err := a.Store.Pool.Exec(ctx, `INSERT INTO edge_observation_health VALUES(?1,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, a.EdgeID); err != nil {
		t.Fatal(err)
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enrollment.Secret)
	code := auth.TOTPCode(secret, time.Now().Unix()/30, 6)
	bodyBytes, _ := json.Marshal(map[string]string{"request_id": "11111111-1111-4111-8111-111111111111", "release_digest": host.release.Digest, "code": code})
	body := string(bodyBytes)
	if w := updateCall(a, nil, body, "https://relay.example"); w.Code != 401 {
		t.Fatal("unsigned-in", w.Code)
	}
	if w := updateCall(a, cookie, body, "https://foreign.example"); w.Code != 403 {
		t.Fatal("CSRF", w.Code)
	}
	a.UpdatesEnabled = false
	if w := updateCall(a, cookie, body, "https://relay.example"); w.Code != 404 {
		t.Fatal("feature default", w.Code)
	}
	a.UpdatesEnabled = true
	if w := updateCall(a, cookie, body+" {}", "https://relay.example"); w.Code != 400 {
		t.Fatal("trailing JSON", w.Code)
	}
	if w := updateCall(a, cookie, `{"password":"owner-password-long-enough"}`, "https://relay.example"); w.Code != 400 {
		t.Fatal("password in update contract", w.Code)
	}
	if _, err := a.Store.Pool.Exec(ctx, `UPDATE edge_observation_health SET last_seen_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if w := updateCall(a, cookie, body, "https://relay.example"); w.Code != 409 {
		t.Fatal("stale source", w.Code)
	}
	if _, err := a.Store.Pool.Exec(ctx, `UPDATE edge_observation_health SET last_seen_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Store.Pool.Exec(ctx, `UPDATE owner_mfa SET last_step=?1`, time.Now().Unix()/30-2); err != nil {
		t.Fatal(err)
	}
	clearTestAuthBudget(t, a)
	if w := updateCall(a, cookie, body, "https://relay.example"); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if host.calls != 0 {
		t.Fatal("host start before durable background dispatch")
	}
	if err := a.dispatchUpdate(ctx); err == nil {
		t.Fatal("lost response treated as delivered")
	}
	var state string
	if err := a.Store.Pool.QueryRow(ctx, `SELECT state FROM update_authorizations`).Scan(&state); err != nil || state != "PENDING" {
		t.Fatal(state, err)
	}
	var number int
	var databaseName, databasePath string
	if err := a.Store.Pool.QueryRow(ctx, "PRAGMA database_list").Scan(&number, &databaseName, &databasePath); err != nil {
		t.Fatal(err)
	}
	a.Store.Close()
	reopened, err := persistence.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	a.Store = reopened
	// Retry keeps the consumed step and exact request, even if the feed moved away.
	host.release = nil
	if w := updateCall(a, cookie, body, "https://relay.example"); w.Code != 202 {
		t.Fatal("receipt retry", w.Code, w.Body.String())
	}
	if err := a.dispatchUpdate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.Pool.QueryRow(ctx, `SELECT state FROM update_authorizations`).Scan(&state); err != nil || state != "DISPATCHED" {
		t.Fatal(state, err)
	}
	if host.calls != 2 {
		t.Fatal("wrong delivery retry count", host.calls)
	}
}

func TestUpdateAuthorizationSharesPersistentAuthBudget(t *testing.T) {
	a := limiterAPI(t)
	cookie, _ := enrollTestOwner(t, a, a.Handler(), "owner-password-long-enough")
	host := &testUpdater{release: &updaterclient.Release{Digest: strings.Repeat("a", 64)}}
	a.Updater = host
	a.UpdatesEnabled = true
	a.EdgeID = "test-edge"
	if _, err := a.Store.Pool.Exec(context.Background(), `INSERT INTO edge_observation_health VALUES(?1,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, a.EdgeID); err != nil {
		t.Fatal(err)
	}
	clearTestAuthBudget(t, a)
	// Non-numeric input is unconditionally invalid and cannot consume a TOTP.
	body := `{"request_id":"11111111-1111-4111-8111-111111111111","release_digest":"` + host.release.Digest + `","code":"abcdef"}`
	for i := 0; i < 10; i++ {
		if w := updateCall(a, cookie, body, "https://relay.example"); w.Code != 401 {
			t.Fatal(i, w.Code, w.Body.String())
		}
	}
	if w := updateCall(a, cookie, body, "https://relay.example"); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("brute force", w.Code)
	}
	if host.calls != 0 {
		t.Fatal("invalid factor reached host")
	}
}
