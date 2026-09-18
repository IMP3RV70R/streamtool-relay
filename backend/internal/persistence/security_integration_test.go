package persistence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSQLiteExclusiveOwnerAndConcurrentQuota(t *testing.T) {
	database := filepath.Join(t.TempDir(), "control.sqlite")
	ctx := context.Background()
	owner, err := Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err = owner.Migrate(ctx, "../../sqlite-migrations"); err != nil {
		t.Fatal(err)
	}
	account, err := owner.CreateAccount(ctx, "security")
	if err != nil {
		t.Fatal(err)
	}
	runtime := owner
	if info, err := os.Stat(database); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("database permissions", err)
	}
	if other, err := Open(ctx, database); err == nil {
		other.Close()
		t.Fatal("second control process admitted")
	}
	stream, err := runtime.CreateStream(ctx, account, "quota", "local", []byte("hash"))
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := runtime.Pool.Exec(ctx, `INSERT INTO destinations(stream_id,name,endpoint,secret_ciphertext,secret_nonce,key_id) VALUES(?1,?2,'rtmp://example.com/live','cipher','nonce','test')`, stream.ID, fmt.Sprintf("output-%d", i))
			if err == nil {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 8 {
		t.Fatal("quota not atomic", accepted.Load())
	}
	var archived string
	if err = runtime.Pool.QueryRow(ctx, `UPDATE destinations SET archived=true,enabled=false WHERE id=(SELECT id FROM destinations WHERE stream_id=?1 LIMIT 1) RETURNING id`, stream.ID).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Pool.Exec(ctx, `INSERT INTO destinations(stream_id,name,endpoint,secret_ciphertext,secret_nonce,key_id) VALUES(?1,'replacement','rtmp://example.com/live','cipher','nonce','test')`, stream.ID); err != nil {
		t.Fatal("archived output counted against quota", err)
	}
	if _, err = runtime.Pool.Exec(ctx, `UPDATE destinations SET archived=false WHERE id=?1`, archived); err == nil {
		t.Fatal("unarchive bypassed quota")
	}
}
