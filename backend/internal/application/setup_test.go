package application

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"streamtool-relay/internal/auth"
	appcrypto "streamtool-relay/internal/crypto"
	"sync"
	"testing"
	"time"
)

type testEnrollment struct {
	Token  string   `json:"enrollment_token"`
	Secret string   `json:"secret"`
	Codes  []string `json:"recovery_codes"`
	QR     string   `json:"qr"`
}

func mfaCall(h http.Handler, path string, input any, token string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(input)
	r := httptest.NewRequest("POST", path, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Streamtool", "1")
	r.Header.Set("X-Setup-Token", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func testMFAKeys(a *API) {
	a.Keys = appcrypto.LocalProvider{ID: "test", Keys: map[string][]byte{"test": []byte("01234567890123456789012345678901")}}
}
func beginTestEnrollment(t *testing.T, a *API, h http.Handler, path string, input any) testEnrollment {
	t.Helper()
	w := mfaCall(h, path, input, a.SetupToken)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("session before MFA confirmation")
	}
	var e testEnrollment
	if json.Unmarshal(w.Body.Bytes(), &e) != nil || len(e.Codes) != 10 || e.Token == "" || e.Secret == "" || e.QR == "" {
		t.Fatal("missing enrollment")
	}
	return e
}
func enrollmentCode(t *testing.T, e testEnrollment) string {
	t.Helper()
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(e.Secret)
	if err != nil {
		t.Fatal(err)
	}
	return auth.TOTPCode(secret, time.Now().Unix()/30, 6)
}
func enrollTestOwner(t *testing.T, a *API, h http.Handler, password string) (*http.Cookie, testEnrollment) {
	t.Helper()
	testMFAKeys(a)
	e := beginTestEnrollment(t, a, h, "/v1/auth/setup", ownerCredentials{Password: password})
	w := mfaCall(h, "/v1/auth/setup/confirm", ownerCredentials{EnrollmentToken: e.Token, Code: enrollmentCode(t, e)}, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	return w.Result().Cookies()[0], e
}
func TestFirstOwnerSetupCannotBeClaimedOrRepeated(t *testing.T) {
	a := limiterAPI(t)
	testMFAKeys(a)
	h := a.Handler()
	for _, token := range []string{"", "wrong-installation-code"} {
		if w := mfaCall(h, "/v1/auth/setup", ownerCredentials{Password: "owner-password-long-enough"}, token); w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	e := beginTestEnrollment(t, a, h, "/v1/auth/setup", ownerCredentials{Password: "owner-password-long-enough"})
	var owners int
	if err := a.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM installation`).Scan(&owners); err != nil || owners != 0 {
		t.Fatal("owner created before confirmation", owners, err)
	}
	if w := mfaCall(h, "/v1/auth/setup/confirm", ownerCredentials{EnrollmentToken: e.Token, Code: "abcdef"}, ""); w.Code != 401 {
		t.Fatal("invalid code accepted", w.Code)
	}
	var wg sync.WaitGroup
	responses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- mfaCall(h, "/v1/auth/setup/confirm", ownerCredentials{EnrollmentToken: e.Token, Code: enrollmentCode(t, e)}, "").Code
		}()
	}
	wg.Wait()
	close(responses)
	successes, rejections := 0, 0
	for code := range responses {
		if code == 200 {
			successes++
		} else if code == 401 {
			rejections++
		} else {
			t.Fatal(code)
		}
	}
	if successes != 1 || rejections != 1 {
		t.Fatal(successes, rejections)
	}
	var accounts, users, factors, codes int
	if err := a.Store.Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM installation),(SELECT count(*) FROM accounts),(SELECT count(*) FROM users),(SELECT count(*) FROM owner_mfa),(SELECT count(*) FROM owner_recovery)`).Scan(&owners, &accounts, &users, &factors, &codes); err != nil || owners != 1 || accounts != 1 || users != 1 || factors != 1 || codes != 10 {
		t.Fatal("partial setup", owners, accounts, users, factors, codes, err)
	}
	if w := mfaCall(h, "/v1/auth/setup", ownerCredentials{Password: "owner-password-long-enough"}, a.SetupToken); w.Code != 409 {
		t.Fatal("setup bypass", w.Code)
	}
	state := httptest.NewRecorder()
	h.ServeHTTP(state, httptest.NewRequest("GET", "/v1/auth/setup", nil))
	if state.Code != 200 || !bytes.Contains(state.Body.Bytes(), []byte(`"required":false`)) {
		t.Fatal(state.Body.String())
	}
}
