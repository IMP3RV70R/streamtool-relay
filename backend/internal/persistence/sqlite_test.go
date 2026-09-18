package persistence

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteDurabilityFencingAndMigrationIntegrity(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	migrationDir := filepath.Join(directory, "migrations")
	if err := os.Mkdir(migrationDir, 0700); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile("../../sqlite-migrations/000001_selfhost.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration := filepath.Join(migrationDir, "000001_selfhost.up.sql")
	if err = os.WriteFile(migration, content, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "control.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(ctx, migrationDir); err != nil {
		store.Close()
		t.Fatal(err)
	}
	account, err := store.CreateAccount(ctx, "owner")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	fence, err := store.NextFence(ctx)
	if err != nil || fence != 1 {
		store.Close()
		t.Fatal("initial fence", fence, err)
	}
	store.Close()
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var persisted string
	if err = store.Pool.QueryRow(ctx, `SELECT name FROM accounts WHERE id=?1`, account).Scan(&persisted); err != nil || persisted != "owner" {
		t.Fatal("lost durable configuration", err)
	}
	fence, err = store.AdvanceFence(ctx, 100)
	if err != nil || fence != 101 {
		t.Fatal("backup fence recovery", fence, err)
	}
	fence, err = store.NextFence(ctx)
	if err != nil || fence != 102 {
		t.Fatal("fence regressed", fence, err)
	}
	if err = store.Migrate(ctx, migrationDir); err != nil {
		t.Fatal("idempotent migration", err)
	}
	if err = os.WriteFile(migration, append(content, []byte("\n-- changed\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(ctx, migrationDir); err == nil {
		t.Fatal("modified applied migration accepted")
	}
	var mode, sync string
	if err = store.Pool.QueryRow(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatal("WAL disabled", mode, err)
	}
	if err = store.Pool.QueryRow(ctx, `PRAGMA synchronous`).Scan(&sync); err != nil || sync != "2" {
		t.Fatal("FULL sync disabled", sync, err)
	}
	// Parameter numbering must not depend on occurrence order in SQL.
	var second, first string
	if err = store.Pool.QueryRow(ctx, `SELECT ?2,?1`, "first", "second").Scan(&second, &first); err != nil || first != "first" || second != "second" {
		t.Fatal("incorrect parameter binding", first, second, err)
	}
}

func TestSQLiteObservationOrderingAtStoredPrecision(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx, "../../sqlite-migrations"); err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateAccount(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := store.CreateStream(ctx, account, "source", "local", []byte("hash"))
	if err != nil {
		t.Fatal(err)
	}
	seen := time.Now().UTC().Truncate(time.Millisecond)
	if _, err = store.ObserveIngest(ctx, "edge", "connection", stream.ID, "srt", true, seen, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ObserveIngest(ctx, "edge", "connection", stream.ID, "srt", false, seen.Add(time.Millisecond), time.Second); err != nil {
		t.Fatal(err)
	}
	// Nanosecond noise cannot override a disconnection at the same stored instant.
	if _, err = store.ObserveIngest(ctx, "edge", "connection", stream.ID, "srt", true, seen.Add(time.Millisecond+500*time.Nanosecond), time.Second); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = store.Pool.QueryRow(ctx, `SELECT status FROM ingest_connections WHERE edge_id='edge' AND connection_id='connection'`).Scan(&status); err != nil || status != "DISCONNECTED" {
		t.Fatal("stale connect resurrected publisher", status, err)
	}
}

func TestAlwaysOnSourceMigrationPreservesKeyAndIsIdempotent(t *testing.T) {
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
		if filepath.Base(path) >= "000008" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(migrations, filepath.Base(path)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(ctx, filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx, migrations); err != nil {
		t.Fatal(err)
	}
	account, err := s.CreateAccount(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := s.Pool.QueryRow(ctx, `INSERT INTO streams(account_id,name,enabled,ingest_key_hash,generation) VALUES(?1,'Источник',false,?2,12) RETURNING id`, account, []byte("preserved-hash")).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO account_sources(account_id,stream_id) VALUES(?1,?2)`, account, id); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../sqlite-migrations/000008_always_on_source.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migrations, "000008_always_on_source.up.sql"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Migrate(ctx, migrations); err != nil {
			t.Fatal(err)
		}
		var enabled bool
		var hash []byte
		var generation int
		if err := s.Pool.QueryRow(ctx, `SELECT enabled,ingest_key_hash,generation FROM streams WHERE id=?1`, id).Scan(&enabled, &hash, &generation); err != nil || !enabled || string(hash) != "preserved-hash" || generation != 13 {
			t.Fatal("migration changed identity or was not idempotent", enabled, generation, err)
		}
	}
}
