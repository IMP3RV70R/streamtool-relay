package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"streamtool-relay/internal/persistence"
	"strings"
	"testing"
)

func TestRecoveryCommandProcess(t *testing.T) {
	if os.Getenv("STREAMTOOL_TEST_RECOVERY_PROCESS") != "1" {
		return
	}
	os.Args = []string{"api", "recover-owner"}
	if run() != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestOfflineRecoveryCommandRequiresExclusiveStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	migrations, err := filepath.Abs("../../sqlite-migrations")
	if err != nil {
		t.Fatal(err)
	}
	s, err := persistence.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx, migrations); err != nil {
		s.Close()
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO accounts(id,name) VALUES('account','Owner')`,
		`INSERT INTO users(id,account_id,email,password_hash,password_salt) VALUES('owner','account','historical@example.invalid',x'01',x'02')`,
		`INSERT INTO installation VALUES(1,'owner')`,
		`INSERT INTO owner_mfa VALUES('owner',x'0304',100)`,
		`INSERT INTO owner_recovery VALUES(x'05','owner')`,
		`INSERT INTO user_sessions(token_hash,user_id,expires_at) VALUES(x'06','owner','2099-01-01T00:00:00Z')`,
		`INSERT INTO update_authorizations(request_id,release_digest,user_id,session_hash,expires_at) VALUES('11111111-1111-4111-8111-111111111111','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','owner',zeroblob(32),'2099-01-01T00:00:00Z')`,
		`INSERT INTO streams(id,account_id,name,ingest_key_hash) VALUES('source','account','Source',x'0708')`,
	} {
		if _, err = s.Pool.Exec(ctx, statement); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	invoke := func(password string) error {
		command := exec.Command(executable, "-test.run=^TestRecoveryCommandProcess$")
		command.Env = append(os.Environ(), "STREAMTOOL_TEST_RECOVERY_PROCESS=1", "SQLITE_PATH="+path, "SQLITE_MIGRATIONS_DIR="+migrations)
		command.Stdin = strings.NewReader(password)
		output, err := command.CombinedOutput()
		if bytes.Contains(output, []byte(password)) {
			t.Fatal("password leaked")
		}
		return err
	}
	if err = invoke("new-owner-password\n"); err == nil {
		s.Close()
		t.Fatal("recovery ignored active API lock")
	}
	s.Close()
	if err = invoke(strings.Repeat("x", 256) + "\r\nextra"); err == nil {
		t.Fatal("oversized stdin was silently truncated")
	}
	if err = invoke("new-owner-password\n"); err != nil {
		t.Fatal("offline recovery failed", err)
	}
	s, err = persistence.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var hash, salt, ingest []byte
	var updateState string
	if err = s.Pool.QueryRow(ctx, `SELECT state FROM update_authorizations`).Scan(&updateState); err != nil || updateState != "CANCELLED" {
		t.Fatal("recovery retained pending update", err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT password_hash,password_salt FROM users WHERE id='owner'`).Scan(&hash, &salt); err != nil || len(hash) != 32 || len(salt) != 16 {
		t.Fatal("password not replaced", err)
	}
	for _, table := range []string{"owner_mfa", "owner_recovery", "user_sessions", "auth_enrollment"} {
		var count int
		if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatal("authentication remained", table, err)
		}
	}
	if err = s.Pool.QueryRow(ctx, `SELECT ingest_key_hash FROM streams WHERE id='source'`).Scan(&ingest); err != nil || !bytes.Equal(ingest, []byte{7, 8}) {
		t.Fatal("source key changed", err)
	}
}
