package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/testdatabase"
)

type rateLimitFixtureRow struct {
	keyHash         []byte
	windowStartedAt time.Time
	attemptCount    int
	expiresAt       time.Time
}

type rateLimitFixtureQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const (
	rateLimitFixtureLockClassID int32 = 731920243
	rateLimitFixtureLockObject  int32 = 18080808
)

type rateLimitFixtureCleanupStore interface {
	Exists(context.Context, []byte) (bool, error)
	Delete(context.Context, []byte) error
}

type postgresRateLimitFixtureCleanupStore struct{ conn *pgxpool.Conn }

func (s postgresRateLimitFixtureCleanupStore) Exists(ctx context.Context, hash []byte) (bool, error) {
	var exists bool
	err := s.conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.auth_rate_limits WHERE key_hash=$1)`, hash).Scan(&exists)
	return exists, err
}

func (s postgresRateLimitFixtureCleanupStore) Delete(ctx context.Context, hash []byte) error {
	_, err := s.conn.Exec(ctx, `DELETE FROM app.auth_rate_limits WHERE key_hash=$1`, hash)
	return err
}

func requireRateLimitFixtureKeysAbsent(ctx context.Context, store rateLimitFixtureCleanupStore, hashes [][]byte) error {
	for _, hash := range hashes {
		exists, err := store.Exists(ctx, hash)
		if err != nil {
			return errors.New("could not verify generated rate-limit fixture identity")
		}
		if exists {
			return errors.New("generated rate-limit fixture identity already exists")
		}
	}
	return nil
}

func cleanupRateLimitFixtureKeys(ctx context.Context, store rateLimitFixtureCleanupStore, hashes [][]byte) error {
	var failures []error
	for _, hash := range hashes {
		if err := store.Delete(ctx, hash); err != nil {
			failures = append(failures, errors.New("could not delete an owned rate-limit fixture"))
			continue
		}
		exists, err := store.Exists(ctx, hash)
		if err != nil {
			failures = append(failures, errors.New("could not verify rate-limit fixture cleanup"))
		} else if exists {
			failures = append(failures, errors.New("rate-limit fixture remained after cleanup"))
		}
	}
	return errors.Join(failures...)
}

func acquireRateLimitFixtureLock(t *testing.T, pool *pgxpool.Pool) *pgxpool.Conn {
	t.Helper()
	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), 8*time.Second)
	conn, err := pool.Acquire(acquireCtx)
	acquireCancel()
	if err != nil {
		t.Fatalf("acquire rate-limit fixture coordination connection: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	_, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1, $2)`, rateLimitFixtureLockClassID, rateLimitFixtureLockObject)
	cancel()
	if err != nil {
		conn.Release()
		t.Fatalf("acquire rate-limit fixture coordination lock: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cleanupCancel()
		var unlocked bool
		if err := conn.QueryRow(cleanupCtx, `SELECT pg_advisory_unlock($1, $2)`, rateLimitFixtureLockClassID, rateLimitFixtureLockObject).Scan(&unlocked); err != nil || !unlocked {
			t.Errorf("release rate-limit fixture coordination lock: %v", err)
			raw := conn.Hijack()
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			if closeErr := raw.Close(closeCtx); closeErr != nil {
				t.Errorf("close connection holding rate-limit fixture lock: %v", closeErr)
			}
			return
		}
		conn.Release()
	})
	return conn
}

func openRateLimitIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL, _ := testdatabase.TestURL(t)
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open isolated test PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newRateLimitTestKey(t *testing.T, prefix string) (string, []byte) {
	t.Helper()
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal("generate isolated rate-limit fixture key")
	}
	key := prefix + ":" + hex.EncodeToString(suffix[:])
	hash := sha256.Sum256([]byte(key))
	return key, append([]byte(nil), hash[:]...)
}

// Refuse to claim existing hashes and serialize cooperating test runs. Call
// before fixture writes; cleanup remains limited to these proven-absent hashes.
func registerRateLimitFixtureCleanup(t *testing.T, pool *pgxpool.Pool, hashes ...[]byte) {
	t.Helper()
	fixtureHashes := make([][]byte, len(hashes))
	for i := range hashes {
		fixtureHashes[i] = append([]byte(nil), hashes[i]...)
	}
	conn := acquireRateLimitFixtureLock(t, pool)
	store := postgresRateLimitFixtureCleanupStore{conn: conn}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	err := requireRateLimitFixtureKeysAbsent(ctx, store, fixtureHashes)
	cancel()
	if err != nil {
		t.Fatalf("refusing unsafe rate-limit fixture setup: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := cleanupRateLimitFixtureKeys(ctx, store, fixtureHashes); err != nil {
			t.Errorf("rate-limit fixture cleanup failed: %v", err)
		}
	})
}

func registerRateLimitTransactionRollback(t *testing.T, tx pgx.Tx, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rollback %s test transaction: %v", name, err)
		}
	})
}

func insertRateLimitFixture(ctx context.Context, db rateLimitExecer, hash []byte, expires time.Time, attempts int) error {
	_, err := db.Exec(ctx, `
		INSERT INTO app.auth_rate_limits (key_hash, window_started_at, attempt_count, expires_at)
		VALUES ($1, $2, $3, $4)`, hash, expires.Add(-time.Hour), attempts, expires)
	return err
}

func readRateLimitFixture(ctx context.Context, db rateLimitFixtureQuerier, hash []byte) (rateLimitFixtureRow, bool, error) {
	var row rateLimitFixtureRow
	row.keyHash = append([]byte(nil), hash...)
	err := db.QueryRow(ctx, `
		SELECT window_started_at, attempt_count, expires_at
		FROM app.auth_rate_limits WHERE key_hash=$1`, hash).
		Scan(&row.windowStartedAt, &row.attemptCount, &row.expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return rateLimitFixtureRow{}, false, nil
	}
	return row, err == nil, err
}

func assertRateLimitFixture(ctx context.Context, db rateLimitFixtureQuerier, hash []byte, expires time.Time, attempts int) error {
	row, found, err := readRateLimitFixture(ctx, db, hash)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("expected rate-limit fixture was absent")
	}
	if !row.expiresAt.Equal(expires) || row.attemptCount != attempts || !row.windowStartedAt.Equal(expires.Add(-time.Hour)) {
		return fmt.Errorf("rate-limit fixture fields changed unexpectedly")
	}
	return nil
}

func listRateLimitFixtures(ctx context.Context, pool *pgxpool.Pool) ([]rateLimitFixtureRow, error) {
	rows, err := pool.Query(ctx, `SELECT key_hash, window_started_at, attempt_count, expires_at FROM app.auth_rate_limits`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []rateLimitFixtureRow
	for rows.Next() {
		var row rateLimitFixtureRow
		if err := rows.Scan(&row.keyHash, &row.windowStartedAt, &row.attemptCount, &row.expiresAt); err != nil {
			return nil, err
		}
		row.keyHash = append([]byte(nil), row.keyHash...)
		result = append(result, row)
	}
	return result, rows.Err()
}

func assertExistingActiveRateLimitsUnchanged(ctx context.Context, db rateLimitFixtureQuerier, before []rateLimitFixtureRow, cutoff time.Time) error {
	for _, expected := range before {
		if !expected.expiresAt.After(cutoff) {
			continue
		}
		actual, found, err := readRateLimitFixture(ctx, db, expected.keyHash)
		if err != nil {
			return err
		}
		if !found || actual.attemptCount != expected.attemptCount ||
			!actual.windowStartedAt.Equal(expected.windowStartedAt) || !actual.expiresAt.Equal(expected.expiresAt) {
			return errors.New("a pre-existing active rate-limit entry was changed")
		}
	}
	return nil
}

func TestAllowRateLimitCleansExpiredEntriesAtCapacityPostgreSQL(t *testing.T) {
	pool := openRateLimitIntegrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	newKey, newHash := newRateLimitTestKey(t, "td08-capacity-new")
	_, expiredHash := newRateLimitTestKey(t, "td08-capacity-expired")
	_, activeHash := newRateLimitTestKey(t, "td08-capacity-active")
	registerRateLimitFixtureCleanup(t, pool, newHash, expiredHash, activeHash)

	baseline, err := listRateLimitFixtures(ctx, pool)
	if err != nil {
		t.Fatalf("snapshot rate-limit rows before capacity fixture: %v", err)
	}
	if len(baseline)+2 >= maxRateLimitEntries {
		t.Fatal("capacity fixture requires room below the production table cap")
	}
	capForTest := len(baseline) + 2
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rollback-protected capacity test: %v", err)
	}
	registerRateLimitTransactionRollback(t, tx, "capacity")
	now := time.Now().UTC().Truncate(time.Microsecond)
	expiredAt := time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC)
	activeAt := now.Add(24 * time.Hour)
	if err := insertRateLimitFixture(ctx, tx, expiredHash, expiredAt, 4); err != nil {
		t.Fatalf("insert test-owned expired fixture: %v", err)
	}
	if err := insertRateLimitFixture(ctx, tx, activeHash, activeAt, 7); err != nil {
		t.Fatalf("insert test-owned active fixture: %v", err)
	}

	allowed, err := NewAuthRepository(pool).allowRateLimitInTransaction(ctx, tx, newKey, 5, time.Hour, capForTest)
	if err != nil || !allowed {
		t.Fatalf("new key at capacity with an expired entry: allowed=%v err=%v", allowed, err)
	}
	if err := assertRateLimitFixture(ctx, tx, activeHash, activeAt, 7); err != nil {
		t.Fatalf("active fixture changed during capacity cleanup: %v", err)
	}
	if err := assertExistingActiveRateLimitsUnchanged(ctx, tx, baseline, now); err != nil {
		t.Fatalf("capacity cleanup changed pre-existing active entries: %v", err)
	}
	var rows int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.auth_rate_limits`).Scan(&rows); err != nil {
		t.Fatalf("read rate-limit count after capacity cleanup: %v", err)
	}
	if rows != capForTest {
		t.Fatalf("rate-limit table count after one-for-one expired cleanup and insert = %d, want cap %d", rows, capForTest)
	}
	newRow, found, err := readRateLimitFixture(ctx, tx, newHash)
	if err != nil || !found || newRow.attemptCount != 1 || !newRow.expiresAt.After(time.Now()) {
		t.Fatalf("new key was not stored within cap: found=%v err=%v", found, err)
	}
}

func TestRateLimitCleanupSkipsConcurrentlyRenewedEntryPostgreSQL(t *testing.T) {
	pool := openRateLimitIntegrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, hash := newRateLimitTestKey(t, "td08-concurrent-renewal")
	registerRateLimitFixtureCleanup(t, pool, hash)
	expiredAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := insertRateLimitFixture(ctx, pool, hash, expiredAt, 9); err != nil {
		t.Fatalf("insert expired renewal fixture: %v", err)
	}

	renewTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin renewal transaction: %v", err)
	}
	registerRateLimitTransactionRollback(t, renewTx, "renewal")
	allowed, found, err := updateRateLimitCounter(ctx, renewTx, hash, 10, time.Hour)
	if err != nil || !found || !allowed {
		t.Fatalf("renew expired entry while holding its row lock: found=%v allowed=%v err=%v", found, allowed, err)
	}

	cleanupTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin concurrent cleanup transaction: %v", err)
	}
	registerRateLimitTransactionRollback(t, cleanupTx, "concurrent cleanup")
	deleted, err := deleteExpiredRateLimitBatchForKeys(ctx, cleanupTx, rateLimitCleanupBatchSize, [][]byte{hash})
	if err != nil || deleted != 0 {
		t.Fatalf("cleanup should skip the row locked by renewal: deleted=%d err=%v", deleted, err)
	}
	if err := cleanupTx.Commit(ctx); err != nil {
		t.Fatalf("commit cleanup transaction: %v", err)
	}
	if err := renewTx.Commit(ctx); err != nil {
		t.Fatalf("commit renewal transaction: %v", err)
	}
	row, exists, err := readRateLimitFixture(ctx, pool, hash)
	if err != nil || !exists || row.attemptCount != 1 || !row.expiresAt.After(time.Now()) {
		t.Fatalf("renewed rate-limit entry did not survive concurrent cleanup: exists=%v err=%v", exists, err)
	}
}

func TestMaintenanceRateLimitCleanupIsBoundedAndPreservesActiveEntriesPostgreSQL(t *testing.T) {
	pool := openRateLimitIntegrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, expiredHashA := newRateLimitTestKey(t, "td08-maintenance-expired-a")
	_, expiredHashB := newRateLimitTestKey(t, "td08-maintenance-expired-b")
	_, activeHash := newRateLimitTestKey(t, "td08-maintenance-active")
	registerRateLimitFixtureCleanup(t, pool, expiredHashA, expiredHashB, activeHash)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rollback-protected maintenance test: %v", err)
	}
	registerRateLimitTransactionRollback(t, tx, "maintenance")
	now := time.Now().UTC().Truncate(time.Microsecond)
	expiredAt := time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC)
	activeAt := now.Add(24 * time.Hour)
	for _, hash := range [][]byte{expiredHashA, expiredHashB} {
		if err := insertRateLimitFixture(ctx, tx, hash, expiredAt, 2); err != nil {
			t.Fatalf("insert test-owned expired maintenance fixture: %v", err)
		}
	}
	if err := insertRateLimitFixture(ctx, tx, activeHash, activeAt, 5); err != nil {
		t.Fatalf("insert test-owned active maintenance fixture: %v", err)
	}

	if err := cleanupAuthenticationRecords(ctx, tx); err != nil {
		t.Fatalf("run one bounded maintenance cleanup: %v", err)
	}
	for _, hash := range [][]byte{expiredHashA, expiredHashB} {
		if _, found, err := readRateLimitFixture(ctx, tx, hash); err != nil || found {
			t.Fatalf("maintenance did not remove only expired fixture: found=%v err=%v", found, err)
		}
	}
	if err := assertRateLimitFixture(ctx, tx, activeHash, activeAt, 5); err != nil {
		t.Fatalf("maintenance changed active rate-limit fixture: %v", err)
	}
}

func TestConcurrentRateLimitCleanupWorkersDoNotOverlapOrDeleteActiveEntriesPostgreSQL(t *testing.T) {
	pool := openRateLimitIntegrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	expiredHashes := make([][]byte, 4)
	for i := range expiredHashes {
		_, expiredHashes[i] = newRateLimitTestKey(t, fmt.Sprintf("td08-worker-expired-%d", i))
	}
	_, activeHash := newRateLimitTestKey(t, "td08-worker-active")
	fixtureHashes := append(append([][]byte(nil), expiredHashes...), activeHash)
	registerRateLimitFixtureCleanup(t, pool, fixtureHashes...)
	now := time.Now().UTC().Truncate(time.Microsecond)
	expiredAt := now.Add(-time.Hour)
	activeAt := now.Add(24 * time.Hour)
	for _, hash := range expiredHashes {
		if err := insertRateLimitFixture(ctx, pool, hash, expiredAt, 2); err != nil {
			t.Fatalf("insert test-owned worker fixture: %v", err)
		}
	}
	if err := insertRateLimitFixture(ctx, pool, activeHash, activeAt, 3); err != nil {
		t.Fatalf("insert active worker fixture: %v", err)
	}

	workerA, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin first cleanup worker: %v", err)
	}
	registerRateLimitTransactionRollback(t, workerA, "cleanup worker A")
	deletedA, err := deleteExpiredRateLimitBatchForKeys(ctx, workerA, 2, expiredHashes)
	if err != nil || deletedA != 2 {
		t.Fatalf("first cleanup worker deleted=%d err=%v; want 2", deletedA, err)
	}
	workerB, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin second cleanup worker: %v", err)
	}
	registerRateLimitTransactionRollback(t, workerB, "cleanup worker B")
	deletedB, err := deleteExpiredRateLimitBatchForKeys(ctx, workerB, 2, expiredHashes)
	if err != nil || deletedB != 2 {
		t.Fatalf("second cleanup worker deleted=%d err=%v; want the other 2 unlocked rows", deletedB, err)
	}
	if err := workerA.Commit(ctx); err != nil {
		t.Fatalf("commit first cleanup worker: %v", err)
	}
	if err := workerB.Commit(ctx); err != nil {
		t.Fatalf("commit second cleanup worker: %v", err)
	}
	for _, hash := range expiredHashes {
		if _, found, err := readRateLimitFixture(ctx, pool, hash); err != nil || found {
			t.Fatalf("cleanup worker left or duplicated an expired fixture: found=%v err=%v", found, err)
		}
	}
	if err := assertRateLimitFixture(ctx, pool, activeHash, activeAt, 3); err != nil {
		t.Fatalf("cleanup workers changed active fixture: %v", err)
	}
}
