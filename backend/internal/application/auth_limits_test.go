package application

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"streamtool-relay/internal/persistence"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func limiterAPI(t *testing.T) *API {
	t.Helper()
	s, err := persistence.Open(context.Background(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err = s.Migrate(context.Background(), "../../sqlite-migrations"); err != nil {
		t.Fatal(err)
	}
	return &API{Store: s, SetupToken: "unique-test-setup-token-32-bytes-long"}
}
func authRequest(handler http.Handler, path, ip, code string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(`{"password":"wrong-password-long-enough","code":"000000"}`))
	r.RemoteAddr = ip
	r.Header.Set("X-Streamtool", "1")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Setup-Token", code)
	r.Header.Set("X-Forwarded-For", "198.51.100.99") // Ignored from untrusted peers.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func TestSetupGuessesSharePersistentIPv6Budget(t *testing.T) {
	a := limiterAPI(t)
	h := a.Handler()
	for i := 1; i <= 10; i++ {
		w := authRequest(h, "/v1/auth/setup", fmt.Sprintf("[2001:db8:1::%x]:1234", i), "wrong")
		if w.Code != 403 {
			t.Fatal(i, w.Code)
		}
	}
	// Restarting HTTP handlers, rotating addresses in a /64,
	// moving from setup to login and forging forwarded headers cannot reset it.
	var sequence int
	var name, database string
	if err := a.Store.Pool.QueryRow(context.Background(), "PRAGMA database_list").Scan(&sequence, &name, &database); err != nil {
		t.Fatal(err)
	}
	a.Store.Close()
	reopened, err := persistence.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	a.Store = reopened
	w := authRequest(a.Handler(), "/v1/auth/login", "[2001:db8:1::ffff]:1234", "")
	if w.Code != 429 || w.Header().Get("Retry-After") != "900" {
		t.Fatal("budget bypass", w.Code)
	}
	var count int
	a.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM installation`).Scan(&count)
	if count != 0 {
		t.Fatal("guessed setup claimed installation")
	}
}
func TestDistributedAuthGlobalBudgetAndExpiry(t *testing.T) {
	a := limiterAPI(t)
	h := a.Handler()
	for i := 1; i <= 60; i++ {
		want := 403
		if i > 10 {
			want = 429
		}
		if w := authRequest(h, "/v1/auth/setup", fmt.Sprintf("198.51.100.%d:1234", i), "wrong"); w.Code != want {
			t.Fatal(i, w.Code)
		}
	}
	if w := authRequest(h, "/v1/auth/setup", "203.0.113.1:1234", "wrong"); w.Code != 429 || w.Header().Get("Retry-After") != "60" {
		t.Fatal("distributed bypass", w.Code)
	}
	_, err := a.Store.Pool.Exec(context.Background(), `UPDATE auth_attempts SET expires_at='2000-01-01T00:00:00.000Z'`)
	if err != nil {
		t.Fatal(err)
	}
	if w := authRequest(h, "/v1/auth/setup", "203.0.113.2:1234", "wrong"); w.Code != 403 {
		t.Fatal("expiry did not release budget", w.Code)
	}
	var stale int
	a.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM auth_attempts WHERE expires_at='2000-01-01T00:00:00.000Z'`).Scan(&stale)
	if stale != 0 {
		t.Fatal("expired keys retained", stale)
	}
}
func TestDistributedOwnerBudget(t *testing.T) {
	a := limiterAPI(t)
	h := a.Handler()
	for i := 1; i <= 10; i++ {
		if w := authRequest(h, "/v1/auth/login", fmt.Sprintf("198.51.100.%d:1234", i), ""); w.Code != 401 || !strings.Contains(w.Body.String(), "invalid password or code") {
			t.Fatal(i, w.Code)
		}
	}
	if w := authRequest(h, "/v1/auth/login", "203.0.113.1:1234", ""); w.Code != 429 {
		t.Fatal("owner budget bypass", w.Code)
	}
}
func TestPersistentLimiterAtomicConcurrentAdmission(t *testing.T) {
	a := limiterAPI(t)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := a.allow(context.Background(), "concurrent-test", 10, 900)
			if err != nil {
				t.Error(err)
			}
			if ok {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 10 {
		t.Fatal("atomic budget bypass", accepted.Load())
	}
}
func TestAuthenticationOriginAndBodyBoundary(t *testing.T) {
	a := limiterAPI(t)
	h := a.Handler()
	for _, origin := range []string{"https://foreign.invalid", "null", "https://example.com/path"} {
		r := httptest.NewRequest("POST", "https://example.com/v1/auth/login", strings.NewReader(`{}`))
		r.Header.Set("Origin", origin)
		r.Header.Set("X-Streamtool", "1")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("foreign origin accepted or sensitive response cacheable", origin, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/v1/auth/login", strings.NewReader(`{"password":"`+strings.Repeat("a", 5000)+`"}`))
	r.Header.Set("X-Streamtool", "1")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("oversized credentials accepted", w.Code)
	}
	if authNetwork("::ffff:198.51.100.1") != authNetwork("198.51.100.1") || authNetwork("2001:db8:1::1") != netip.MustParsePrefix("2001:db8:1::/64").String() {
		t.Fatal("network normalization")
	}
}
