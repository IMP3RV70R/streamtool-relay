package application

import (
	"context"
	"encoding/base32"
	"errors"
	"streamtool-relay/internal/auth"
	"streamtool-relay/internal/persistence"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpdateAuthorizationAtomicRetryAndRevocation(t *testing.T) {
	a := limiterAPI(t)
	cookie, e := enrollTestOwner(t, a, a.Handler(), "owner-password-long-enough")
	ctx := context.Background()
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(e.Secret)
	code := auth.TOTPCode(secret, time.Now().Unix()/30, 6)
	id := "11111111-1111-4111-8111-111111111111"
	digest := strings.Repeat("a", 64)
	// The setup/login step has already been consumed; no update may reuse it.
	if _, err := a.authorizeUpdate(ctx, cookie.Value, id, digest, enrollmentCode(t, e)); !errors.Is(err, errUpdateProof) {
		t.Fatal("setup replay", err)
	}
	if _, err := a.authorizeUpdate(ctx, cookie.Value, id, digest, e.Codes[0]); !errors.Is(err, errUpdateProof) {
		t.Fatal("recovery used for update", err)
	}
	if _, err := a.Store.Pool.Exec(ctx, `UPDATE owner_mfa SET last_step=?1`, time.Now().Unix()/30-2); err != nil {
		t.Fatal(err)
	}
	// An insertion failure must roll back factor consumption too.
	if _, err := a.Store.Pool.Exec(ctx, `CREATE TRIGGER fail_update BEFORE INSERT ON update_authorizations BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.authorizeUpdate(ctx, cookie.Value, id, digest, code); err == nil {
		t.Fatal("accepted failed durable insert")
	}
	if _, err := a.Store.Pool.Exec(ctx, `DROP TRIGGER fail_update`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 2)
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			retry, err := a.authorizeUpdate(ctx, cookie.Value, id, digest, code)
			results <- retry
			failures <- err
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	retries := 0
	for retry := range results {
		if retry {
			retries++
		}
	}
	if retries != 1 {
		t.Fatal("two new authorizations", retries)
	}
	if _, err := a.authorizeUpdate(ctx, cookie.Value, id, strings.Repeat("b", 64), code); !errors.Is(err, errUpdateConflict) {
		t.Fatal("target changed", err)
	}
	if _, err := a.Store.Pool.Exec(ctx, `UPDATE update_authorizations SET expires_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.authorizeUpdate(ctx, cookie.Value, id, digest, ""); !errors.Is(err, errUpdateConflict) {
		t.Fatal("expired request revived", err)
	}
	if _, err := a.Store.Pool.Exec(ctx, `UPDATE update_authorizations SET state='CANCELLED'`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.authorizeUpdate(ctx, cookie.Value, id, digest, ""); !errors.Is(err, errUpdateConflict) {
		t.Fatal("cancelled request revived", err)
	}
	if _, err := a.Store.Pool.Exec(ctx, `UPDATE update_authorizations SET state='DISPATCHED'`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.authorizeUpdate(ctx, cookie.Value, "22222222-2222-4222-8222-222222222222", digest, code); !errors.Is(err, errUpdateProof) {
		t.Fatal("consumed update step reused", err)
	}
	// Exact retry doesn't need a new code, but does require its original live session.
	if retry, err := a.authorizeUpdate(ctx, cookie.Value, id, digest, ""); err != nil || !retry {
		t.Fatal(retry, err)
	}
	var sequence int
	var name, path string
	if err := a.Store.Pool.QueryRow(ctx, "PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	a.Store.Close()
	reopened, err := persistence.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	a.Store = reopened
	if retry, err := a.authorizeUpdate(ctx, cookie.Value, id, digest, ""); err != nil || !retry {
		t.Fatal("receipt lost on restart", retry, err)
	}
	if _, err := a.Store.Pool.Exec(ctx, `DELETE FROM user_sessions`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.authorizeUpdate(ctx, cookie.Value, id, digest, ""); !errors.Is(err, errUpdateProof) {
		t.Fatal("revoked receipt access", err)
	}
}
