package application

import (
	"context"
	"encoding/base32"
	"net/http"
	"net/http/httptest"
	"streamtool-relay/internal/auth"
	"streamtool-relay/internal/persistence"
	"strings"
	"sync"
	"testing"
	"time"
)

func clearTestAuthBudget(t *testing.T, a *API) {
	t.Helper()
	if _, err := a.Store.Pool.Exec(context.Background(), `DELETE FROM auth_attempts`); err != nil {
		t.Fatal(err)
	}
}
func TestMFAReplayRecoveryAndRotation(t *testing.T) {
	a := limiterAPI(t)
	h := a.Handler()
	cookie, e := enrollTestOwner(t, a, h, "owner-password-long-enough")
	getMe := func(c *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v1/me", nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := getMe(cookie); w.Code != 200 || strings.Contains(w.Body.String(), "email") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, input := range []ownerCredentials{{Password: "owner-password-long-enough"}, {Password: "wrong-password-long-enough", RecoveryCode: e.Codes[0]}, {Password: "owner-password-long-enough", Code: enrollmentCode(t, e)}, {Password: "owner-password-long-enough", Code: "000000", RecoveryCode: e.Codes[0]}} {
		if w := mfaCall(h, "/v1/auth/login", input, ""); w.Code != 401 {
			t.Fatal("MFA bypass or replay", w.Code, w.Body.String())
		}
	}
	clearTestAuthBudget(t, a)
	// Two concurrent uses of one recovery code must yield exactly one session.
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- mfaCall(h, "/v1/auth/login", ownerCredentials{Password: "owner-password-long-enough", RecoveryCode: e.Codes[0]}, "")
		}()
	}
	wg.Wait()
	close(responses)
	successes := 0
	for w := range responses {
		if w.Code == 200 {
			successes++
		} else if w.Code != 401 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if successes != 1 {
		t.Fatal("recovery replay", successes)
	}
	var stored []byte
	if err := a.Store.Pool.QueryRow(context.Background(), `SELECT secret FROM owner_mfa`).Scan(&stored); err != nil || strings.Contains(string(stored), e.Secret) {
		t.Fatal("plaintext TOTP stored", err)
	}
	if w := mfaCall(h, "/v1/auth/totp/replace", ownerCredentials{Password: "owner-password-long-enough"}, ""); w.Code != 401 {
		t.Fatal("rotation without second factor", w.Code)
	}
	next := beginTestEnrollment(t, a, h, "/v1/auth/totp/replace", ownerCredentials{Password: "owner-password-long-enough", RecoveryCode: e.Codes[1]})
	// Current authenticator/session remain in force until new code is confirmed.
	if w := getMe(cookie); w.Code != 200 {
		t.Fatal("rotation revoked before confirmation", w.Code)
	}
	clearTestAuthBudget(t, a)
	w := mfaCall(h, "/v1/auth/setup/confirm", ownerCredentials{EnrollmentToken: next.Token, Code: enrollmentCode(t, next)}, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if getMe(cookie).Code != 401 {
		t.Fatal("old session survived rotation")
	}
	if w := mfaCall(h, "/v1/auth/login", ownerCredentials{Password: "owner-password-long-enough", RecoveryCode: e.Codes[2]}, ""); w.Code != 401 {
		t.Fatal("old recovery code survived", w.Code)
	}
	if w := mfaCall(h, "/v1/auth/login", ownerCredentials{Password: "owner-password-long-enough", RecoveryCode: next.Codes[0]}, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestTOTPConcurrentReplayAndDurableStep(t *testing.T) {
	a := limiterAPI(t)
	h := a.Handler()
	_, e := enrollTestOwner(t, a, h, "owner-password-long-enough")
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(e.Secret)
	// Move only test storage's consumed step back so a current TOTP is fresh.
	if _, err := a.Store.Pool.Exec(context.Background(), `UPDATE owner_mfa SET last_step=?1`, time.Now().Unix()/30-2); err != nil {
		t.Fatal(err)
	}
	code := auth.TOTPCode(secret, time.Now().Unix()/30, 6)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- mfaCall(h, "/v1/auth/login", ownerCredentials{Password: "owner-password-long-enough", Code: code}, "").Code
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for result := range results {
		if result == 200 {
			ok++
		} else if result != 401 {
			t.Fatal(result)
		}
	}
	if ok != 1 {
		t.Fatal("TOTP replay", ok)
	}
	var sequence int
	var name, path string
	a.Store.Pool.QueryRow(context.Background(), "PRAGMA database_list").Scan(&sequence, &name, &path)
	a.Store.Close()
	reopened, err := persistence.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	a.Store = reopened
	if w := mfaCall(a.Handler(), "/v1/auth/login", ownerCredentials{Password: "owner-password-long-enough", Code: code}, ""); w.Code != 401 {
		t.Fatal("replay after restart", w.Code)
	}
}
func TestEnrollmentExpiryAndOfflineRecovery(t *testing.T) {
	a := limiterAPI(t)
	h := a.Handler()
	testMFAKeys(a)
	e := beginTestEnrollment(t, a, h, "/v1/auth/setup", ownerCredentials{Password: "owner-password-long-enough"})
	a.Store.Pool.Exec(context.Background(), `UPDATE auth_enrollment SET expires_at='2000-01-01T00:00:00.000Z'`)
	if w := mfaCall(h, "/v1/auth/setup/confirm", ownerCredentials{EnrollmentToken: e.Token, Code: enrollmentCode(t, e)}, ""); w.Code != 401 {
		t.Fatal("expired enrollment", w.Code)
	}
	cookie, _ := enrollTestOwner(t, a, h, "owner-password-long-enough")
	var account, email string
	a.Store.Pool.QueryRow(context.Background(), `SELECT account_id,email FROM users`).Scan(&account, &email)
	source, err := a.Store.CreateStream(context.Background(), account, "source", "local", []byte("preserve-ingest-hash"))
	if err != nil {
		t.Fatal(err)
	}
	if err = RecoverOwner(context.Background(), a.Store, "replacement-password-long-enough"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/v1/me", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("recovery kept session")
	}
	var hash []byte
	if err = a.Store.Pool.QueryRow(context.Background(), `SELECT ingest_key_hash FROM streams WHERE id=?1`, source.ID).Scan(&hash); err != nil || string(hash) != "preserve-ingest-hash" {
		t.Fatal("recovery altered source", err)
	}
	if w := mfaCall(h, "/v1/auth/login", ownerCredentials{Password: "replacement-password-long-enough", Code: "000000"}, ""); w.Code != 401 {
		t.Fatal("password-only recovery", w.Code)
	}
	clearTestAuthBudget(t, a)
	if w := mfaCall(h, "/v1/auth/setup", ownerCredentials{Password: "owner-password-long-enough"}, a.SetupToken); w.Code != 401 {
		t.Fatal("old password recovered MFA", w.Code)
	}
	cookie, _ = enrollTestOwner(t, a, h, "replacement-password-long-enough")
	if cookie == nil {
		t.Fatal("re-enrollment unavailable")
	}
	var kept string
	a.Store.Pool.QueryRow(context.Background(), `SELECT email FROM users`).Scan(&kept)
	if kept != email {
		t.Fatal("stored historical metadata altered")
	}
}
func TestMFAKeyFailureDoesNotConsumeRecovery(t *testing.T) {
	a := limiterAPI(t)
	h := a.Handler()
	_, e := enrollTestOwner(t, a, h, "owner-password-long-enough")
	a.Keys = nil
	if w := mfaCall(h, "/v1/auth/totp/replace", ownerCredentials{Password: "owner-password-long-enough", RecoveryCode: e.Codes[0]}, ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
	var codes int
	a.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM owner_recovery`).Scan(&codes)
	if codes != 10 {
		t.Fatal("failed transaction consumed recovery", codes)
	}
	testMFAKeys(a)
	if w := mfaCall(h, "/v1/auth/login", ownerCredentials{Password: "owner-password-long-enough", RecoveryCode: e.Codes[0]}, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
