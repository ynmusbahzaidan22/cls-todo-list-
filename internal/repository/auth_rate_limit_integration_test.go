package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/testdatabase"
)

func TestSharedRateLimitAcrossRepositoriesPostgreSQL(t *testing.T) {
	databaseURL, _ := testdatabase.TestURL(t)
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	secondPool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open second API instance pool: %v", err)
	}
	t.Cleanup(secondPool.Close)

	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal("generate isolated rate-limit key")
	}
	key := "phase11-shared-limit:" + hex.EncodeToString(suffix[:])
	keyHash := sha256.Sum256([]byte(key))
	expiredKey := "phase11-shared-expired:" + hex.EncodeToString(suffix[:])
	expiredHash := sha256.Sum256([]byte(expiredKey))
	activeKey := "phase11-shared-active:" + hex.EncodeToString(suffix[:])
	activeHash := sha256.Sum256([]byte(activeKey))
	registerRateLimitFixtureCleanup(t, pool, keyHash[:], expiredHash[:], activeHash[:])

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var existingRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.auth_rate_limits`).Scan(&existingRows); err != nil {
		t.Fatalf("read rate-limit table size: %v", err)
	}
	if existingRows+2 >= maxRateLimitEntries {
		t.Fatal("shared-window fixture requires capacity below the global rate-limit cap")
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := insertRateLimitFixture(ctx, pool, expiredHash[:], now.Add(-2*time.Hour), 8); err != nil {
		t.Fatalf("insert expired unrelated rate-limit fixture: %v", err)
	}
	if err := insertRateLimitFixture(ctx, pool, activeHash[:], now.Add(24*time.Hour), 3); err != nil {
		t.Fatalf("insert active unrelated rate-limit fixture: %v", err)
	}

	instances := []*AuthRepository{NewAuthRepository(pool), NewAuthRepository(secondPool)}
	const limit, requests = 20, 40
	var allowed atomic.Int32
	var rejected atomic.Int32
	var failures atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < requests; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ok, err := instances[index%len(instances)].AllowRateLimit(ctx, key, limit, time.Hour)
			if err != nil {
				failures.Add(1)
			} else if ok {
				allowed.Add(1)
			} else {
				rejected.Add(1)
			}
		}(i)
	}
	group.Wait()
	if failures.Load() != 0 || allowed.Load() != limit || rejected.Load() != requests-limit {
		t.Fatalf("two-instance shared window: allowed=%d rejected=%d errors=%d; want %d/%d/0", allowed.Load(), rejected.Load(), failures.Load(), limit, requests-limit)
	}
	if err := assertRateLimitFixture(ctx, pool, expiredHash[:], now.Add(-2*time.Hour), 8); err != nil {
		t.Fatalf("ordinary new-key requests changed an unrelated expired row: %v", err)
	}
	if err := assertRateLimitFixture(ctx, pool, activeHash[:], now.Add(24*time.Hour), 3); err != nil {
		t.Fatalf("ordinary new-key requests changed an unrelated active row: %v", err)
	}
}
