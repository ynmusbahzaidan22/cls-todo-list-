package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSharedRateLimitAcrossRepositoriesPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL integration test skipped")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		t.Fatal("TEST_DATABASE_URL must target loopback PostgreSQL")
	}
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
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, `DELETE FROM app.auth_rate_limits WHERE key_hash = $1`, keyHash[:]); err != nil {
			t.Errorf("remove only this test's rate-limit key: %v", err)
		}
	})

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
}
