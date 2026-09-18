package application

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"streamtool-relay/internal/persistence"
	"testing"
)

func TestMFAMigrationPreservesOwnerAndRequiresEnrollment(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	migrations := filepath.Join(dir, "migrations")
	if err := os.Mkdir(migrations, 0700); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob("../../sqlite-migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if filepath.Base(path) >= "000005_owner_mfa.up.sql" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(migrations, filepath.Base(path)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := persistence.Open(ctx, filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, migrations); err != nil {
		t.Fatal(err)
	}
	password := "existing-owner-password"
	salt := []byte("0123456789012345")
	raw := "0123456789012345678901234567890123456789012"
	statements := []string{`INSERT INTO accounts(id,name) VALUES('account','Existing')`, `INSERT INTO users(id,account_id,email,password_hash,password_salt) VALUES('owner','account','historical@example.invalid',?1,?2)`, `INSERT INTO installation VALUES(1,'owner')`, `INSERT INTO streams(id,account_id,name,ingest_key_hash) VALUES('source','account','Source',x'0102')`, `INSERT INTO user_sessions(token_hash,user_id,expires_at) VALUES(?1,'owner','2099-01-01T00:00:00Z')`}
	for i, statement := range statements {
		var args []any
		if i == 1 {
			args = []any{passwordHash(password, salt), salt}
		}
		if i == 4 {
			args = []any{sessionHash(raw)}
		}
		if _, err = s.Pool.Exec(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Migrate(ctx, "../../sqlite-migrations"); err != nil {
		t.Fatal(err)
	}
	var email string
	var ingest []byte
	var sessions int
	if err = s.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id='owner'`).Scan(&email); err != nil || email != "historical@example.invalid" {
		t.Fatal(email, err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT ingest_key_hash FROM streams WHERE id='source'`).Scan(&ingest); err != nil || string(ingest) != string([]byte{1, 2}) {
		t.Fatal("source changed", err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM user_sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("old sessions retained", err)
	}
	a := &API{Store: s, SetupToken: "existing-installation-token"}
	testMFAKeys(a)
	h := a.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/auth/setup", nil))
	if w.Code != 200 || w.Body.String() != "{\"required\":true}\n" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = mfaCall(h, "/v1/auth/setup", ownerCredentials{Password: "wrong-owner-password"}, a.SetupToken); w.Code != 401 {
		t.Fatal("old owner bypass", w.Code)
	}
	if w = mfaCall(h, "/v1/auth/setup", ownerCredentials{Password: password}, ""); w.Code != 403 {
		t.Fatal("installation token missing", w.Code)
	}
	cookie, e := enrollTestOwner(t, a, h, password)
	if cookie == nil {
		t.Fatal("missing session")
	}
	if w = mfaCall(h, "/v1/auth/login", ownerCredentials{Password: password, RecoveryCode: e.Codes[0]}, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var owners int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&owners); err != nil || owners != 1 {
		t.Fatal("owner replaced", owners, err)
	}
}
