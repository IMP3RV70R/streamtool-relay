package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	appcrypto "streamtool-relay/internal/crypto"
	"streamtool-relay/internal/netpolicy"
	"streamtool-relay/internal/persistence"
	"strings"
	"testing"
)

func TestOwnerOutputs(t *testing.T) {

	ctx := context.Background()
	s, err := persistence.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, "../../sqlite-migrations"); err != nil {
		t.Fatal(err)
	}
	a := &API{Store: s, Keys: appcrypto.LocalProvider{ID: "test", Keys: map[string][]byte{"test": []byte("01234567890123456789012345678901")}}, DestinationPolicy: netpolicy.RTMPPolicy{Resolver: publicTestResolver{}}}
	h := a.Handler()
	createUser := func() (string, string) {
		account, e := s.CreateAccount(ctx, "outputs")
		if e != nil {
			t.Fatal(e)
		}
		digest := sha256.Sum256([]byte(account))
		token := base64.RawURLEncoding.EncodeToString(digest[:])
		var uid string
		e = s.Pool.QueryRow(ctx, `INSERT INTO users(account_id,email,password_hash,password_salt) VALUES(?1,?2,'x','y') RETURNING id`, account, account+"@example.com").Scan(&uid)
		if e != nil {
			t.Fatal(e)
		}
		_, e = s.Pool.Exec(ctx, `INSERT INTO user_sessions(user_id,token_hash,expires_at) VALUES(?1,?2,strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour'))`, uid, sessionHash(token))
		if e != nil {
			t.Fatal(e)
		}

		if _, e := s.Pool.Exec(ctx, `INSERT INTO owner_mfa(user_id,secret) VALUES(?1,'fixture')`, uid); e != nil {
			t.Fatal(e)
		}
		if _, e := s.Pool.Exec(ctx, `INSERT OR IGNORE INTO installation(singleton,owner_user) VALUES(1,?1)`, uid); e != nil {
			t.Fatal(e)
		}
		return account, token
	}
	account, token := createUser()
	_, other := createUser()
	stream, err := s.CreateStream(ctx, account, "Source", "local", []byte("hash"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO account_sources(account_id,stream_id) VALUES(?1,?2)`, account, stream.ID); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, session string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.AddCookie(&http.Cookie{Name: "streamtool_session", Value: session})
		r.Header.Set("X-Streamtool", "1")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	base := "/v1/me/source/outputs"
	input := map[string]any{"name": "YouTube", "endpoint": "rtmps://output.example/live", "secret": "private-output-key", "enabled": true, "generation": 0}
	w := call("POST", base, token, input)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created map[string]string
	json.Unmarshal(w.Body.Bytes(), &created)
	ids := []string{created["id"]}
	for i := 1; i < 8; i++ {
		input["name"] = fmt.Sprintf("output-%d", i)
		if w = call("POST", base, token, input); w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	input["name"] = "ninth"
	if w = call("POST", base, token, input); w.Code != 409 {
		t.Fatal("quota bypass", w.Code)
	}
	w = call("GET", base, token, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "private-output-key") {
		t.Fatal("secret leak", w.Code)
	}
	var outputs []map[string]any
	json.Unmarshal(w.Body.Bytes(), &outputs)
	if len(outputs) != 8 {
		t.Fatal("outputs missing")
	}
	for _, route := range []struct{ method, path string }{{"GET", base}, {"PUT", base + "/" + ids[0]}, {"DELETE", base + "/" + ids[0]}, {"POST", base + "/" + ids[0] + "/retry"}} {
		if w = call(route.method, route.path, other, input); w.Code != 401 {
			t.Fatal("cross-account access", route, w.Code)
		}
	}

	item := base + "/" + ids[0]
	input["name"] = "Renamed"
	input["secret"] = ""
	input["generation"] = 1
	if w = call("PUT", item, token, input); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	var env appcrypto.Envelope
	if err = s.Pool.QueryRow(ctx, `SELECT key_id,secret_ciphertext,secret_nonce FROM destinations WHERE id=?1`, ids[0]).Scan(&env.KeyID, &env.Ciphertext, &env.Nonce); err != nil {
		t.Fatal(err)
	}
	plain, err := appcrypto.Decrypt(ctx, a.Keys, env, []byte(stream.ID+":Renamed"))
	if err != nil || string(plain) != "private-output-key" {
		t.Fatal("rename lost secret", err)
	}
	if w = call("PUT", item, token, input); w.Code != 409 {
		t.Fatal("stale write accepted")
	}
	if w = call("POST", item+"/retry", token, map[string]int{"generation": 2}); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	input["generation"] = 3
	input["enabled"] = false
	if w = call("PUT", item, token, input); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("POST", item+"/retry", token, map[string]int{"generation": 4}); w.Code != 409 {
		t.Fatal("disabled retry accepted")
	}
	if w = call("DELETE", item, token, map[string]int{"generation": 4}); w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	var archived, enabled bool
	var secretSize int
	if err = s.Pool.QueryRow(ctx, `SELECT archived,enabled,octet_length(secret_ciphertext) FROM destinations WHERE id=?1`, ids[0]).Scan(&archived, &enabled, &secretSize); err != nil || !archived || enabled || secretSize != 0 {
		t.Fatal("archive must preserve record and erase secret", err)
	}
	if w = call("GET", base, token, nil); w.Code != 200 || strings.Contains(w.Body.String(), ids[0]) {
		t.Fatal("archive still listed")
	}
	if w = call("PUT", item, token, input); w.Code != 404 {
		t.Fatal("archived output mutable", w.Code)
	}

	input["endpoint"] = "rtmp://127.0.0.1/live"
	input["secret"] = "x"
	if w = call("POST", base, token, input); w.Code != 422 {
		t.Fatal("private endpoint accepted", w.Code)
	}
	if w = call(http.MethodPost, base, token, map[string]any{"name": "bad", "endpoint": "rtmps://output.example/live?key=secret", "secret": "x"}); w.Code != 400 {
		t.Fatal("secret in query accepted")
	}
}

type publicTestResolver struct{}

func (publicTestResolver) LookupIP(_ context.Context, _ string, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	return []net.IP{net.ParseIP("93.184.216.34")}, nil
}
