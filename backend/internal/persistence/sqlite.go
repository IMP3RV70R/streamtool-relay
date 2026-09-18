package persistence

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"modernc.org/sqlite"
)

// SQLite is the durable authority. One process owns the database, WAL permits
// readers, and IMMEDIATE transactions serialize admission/configuration/stop.
type Store struct {
	Pool      *DB
	ControlMu sync.Mutex
	lock      *os.File
}
type DB struct{ db *sql.DB }
type Tx struct{ tx *sql.Tx }
type Tag struct{ affected int64 }

func (t Tag) RowsAffected() int64 { return t.affected }

type Row interface{ Scan(...any) error }
type row struct{ r *sql.Row }
type Rows struct{ *sql.Rows }

func (r row) Scan(dest ...any) error   { return r.r.Scan(timeDestinations(dest)...) }
func (r *Rows) Scan(dest ...any) error { return r.Rows.Scan(timeDestinations(dest)...) }
func timeDestinations(dest []any) []any {
	result := append([]any(nil), dest...)
	for i, d := range result {
		switch v := d.(type) {
		case *time.Time:
			result[i] = timeValue{value: v}
		case **time.Time:
			result[i] = timeValue{optional: v}
		}
	}
	return result
}

type timeValue struct {
	value    *time.Time
	optional **time.Time
}

func (s timeValue) Scan(value any) error {
	if value == nil && s.optional != nil {
		*s.optional = nil
		return nil
	}
	var t time.Time
	switch v := value.(type) {
	case time.Time:
		t = v.UTC()
	case string:
		var err error
		t, err = time.Parse("2006-01-02T15:04:05.000Z", v)
		if err != nil {
			t, err = time.Parse(time.RFC3339Nano, v)
		}
		if err != nil {
			return fmt.Errorf("invalid stored timestamp: %w", err)
		}
	default:
		return errors.New("invalid stored timestamp type")
	}
	if s.optional != nil {
		*s.optional = &t
	} else {
		*s.value = t
	}
	return nil
}
func arguments(args []any) []any {
	out := append([]any(nil), args...)
	for i, v := range out {
		if t, ok := v.(time.Time); ok {
			out[i] = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
	return out
}
func init() {
	sqlite.MustRegisterScalarFunction("uuid", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		b[6] = (b[6] & 15) | 64
		b[8] = (b[8] & 63) | 128
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
	})
}
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" || path == ":memory:" || strings.Contains(path, "://") {
		return nil, errors.New("durable SQLite path required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(absolute+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("database already owned by another process")
	}
	fail := func() { syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); lock.Close() }
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		fail()
		return nil, err
	}
	file.Close()
	if err = os.Chmod(absolute, 0600); err != nil {
		fail()
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := u.Query()
	for _, pragma := range []string{"journal_mode(WAL)", "busy_timeout(5000)", "foreign_keys(1)", "synchronous(FULL)", "wal_autocheckpoint(256)", "journal_size_limit(16777216)"} {
		q.Add("_pragma", pragma)
	}
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		fail()
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		fail()
		return nil, err
	}
	return &Store{Pool: &DB{db: db}, lock: lock}, nil
}
func (s *Store) Close() {
	s.Pool.db.Close()
	syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	s.lock.Close()
}
func (d *DB) QueryRow(ctx context.Context, q string, args ...any) Row {
	return row{d.db.QueryRowContext(ctx, q, arguments(args)...)}
}
func (d *DB) Query(ctx context.Context, q string, args ...any) (*Rows, error) {
	r, e := d.db.QueryContext(ctx, q, arguments(args)...)
	if e != nil {
		return nil, e
	}
	return &Rows{r}, nil
}
func resultTag(r sql.Result, e error) (Tag, error) {
	if e != nil {
		return Tag{}, e
	}
	n, e := r.RowsAffected()
	return Tag{n}, e
}
func (d *DB) Exec(ctx context.Context, q string, args ...any) (Tag, error) {
	return resultTag(d.db.ExecContext(ctx, q, arguments(args)...))
}
func (d *DB) Begin(ctx context.Context) (*Tx, error) {
	t, e := d.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	return &Tx{t}, nil
}
func (t *Tx) QueryRow(ctx context.Context, q string, args ...any) Row {
	return row{t.tx.QueryRowContext(ctx, q, arguments(args)...)}
}
func (t *Tx) Query(ctx context.Context, q string, args ...any) (*Rows, error) {
	r, e := t.tx.QueryContext(ctx, q, arguments(args)...)
	if e != nil {
		return nil, e
	}
	return &Rows{r}, nil
}
func (t *Tx) Exec(ctx context.Context, q string, args ...any) (Tag, error) {
	return resultTag(t.tx.ExecContext(ctx, q, arguments(args)...))
}
func (t *Tx) Commit(_ context.Context) error   { return t.tx.Commit() }
func (t *Tx) Rollback(_ context.Context) error { return t.tx.Rollback() }
func IsConstraint(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code()&255 == 19
}
func (s *Store) NextFence(ctx context.Context) (uint64, error) {
	var n uint64
	err := s.Pool.QueryRow(ctx, `UPDATE controller_fencing SET value=value+1 WHERE singleton=1 RETURNING value`).Scan(&n)
	return n, err
}
func (s *Store) Migrate(ctx context.Context, dir string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil || len(paths) == 0 {
		return errors.New("SQLite migration inventory unavailable")
	}
	sort.Strings(paths)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(name TEXT PRIMARY KEY,digest TEXT NOT NULL,applied_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')))`); err != nil {
		return err
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		digest := hex.EncodeToString(sum[:])
		var old string
		err = tx.QueryRow(ctx, `SELECT digest FROM schema_migrations WHERE name=?1`, filepath.Base(path)).Scan(&old)
		if err == nil {
			if old != digest {
				return errors.New("applied SQLite migration changed")
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err = tx.Exec(ctx, string(content)); err != nil {
			return fmt.Errorf("SQLite migration %s: %w", filepath.Base(path), err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(name,digest) VALUES(?1,?2)`, filepath.Base(path), digest); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// AdvanceFence recovers the durable controller counter from the authenticated
// agent's high watermark after restoring an older database backup.
func (s *Store) AdvanceFence(ctx context.Context, minimum uint64) (uint64, error) {
	var value uint64
	err := s.Pool.QueryRow(ctx, `UPDATE controller_fencing SET value=max(value,?1)+1 WHERE singleton=1 RETURNING value`, minimum).Scan(&value)
	return value, err
}
