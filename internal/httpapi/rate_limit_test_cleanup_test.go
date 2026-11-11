package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	rateLimitTestLockClassID int32 = 731920243
	rateLimitTestLockObject  int32 = 18080808
)

type rateLimitCleanupStore interface {
	Exists(context.Context, []byte) (bool, error)
	Delete(context.Context, []byte) error
}

type postgresRateLimitCleanupStore struct{ conn *pgxpool.Conn }

func (s postgresRateLimitCleanupStore) Exists(ctx context.Context, hash []byte) (bool, error) {
	var exists bool
	err := s.conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.auth_rate_limits WHERE key_hash=$1)`, hash).Scan(&exists)
	return exists, err
}

func (s postgresRateLimitCleanupStore) Delete(ctx context.Context, hash []byte) error {
	_, err := s.conn.Exec(ctx, `DELETE FROM app.auth_rate_limits WHERE key_hash=$1`, hash)
	return err
}

func rateLimitCleanupHashes(remoteIPs, emails []string) [][]byte {
	hashes := make(map[[sha256.Size]byte]struct{}, len(remoteIPs)*2+len(emails))
	add := func(key string) { hashes[sha256.Sum256([]byte(key))] = struct{}{} }
	for _, remoteIP := range remoteIPs {
		add("register:ip:" + remoteIP)
		add("login:ip:" + remoteIP)
	}
	for _, email := range emails {
		normalized := strings.ToLower(strings.TrimSpace(email))
		digest := sha256.Sum256([]byte(normalized))
		add("login:email:" + base64.RawURLEncoding.EncodeToString(digest[:]))
	}
	result := make([][]byte, 0, len(hashes))
	for hash := range hashes {
		result = append(result, append([]byte(nil), hash[:]...))
	}
	return result
}

func requireRateLimitKeysAbsent(ctx context.Context, store rateLimitCleanupStore, hashes [][]byte) error {
	for _, hash := range hashes {
		exists, err := store.Exists(ctx, hash)
		if err != nil {
			return errors.New("could not verify the test rate-limit identity")
		}
		if exists {
			return errors.New("test rate-limit identity already exists; refusing to claim it")
		}
	}
	return nil
}

func cleanupOwnedRateLimitKeys(ctx context.Context, store rateLimitCleanupStore, hashes [][]byte) error {
	var failures []error
	for _, hash := range hashes {
		if err := store.Delete(ctx, hash); err != nil {
			failures = append(failures, errors.New("could not delete an owned test rate-limit key"))
			continue
		}
		exists, err := store.Exists(ctx, hash)
		if err != nil {
			failures = append(failures, errors.New("could not verify test rate-limit cleanup"))
		} else if exists {
			failures = append(failures, errors.New("test rate-limit key remained after cleanup"))
		}
	}
	return errors.Join(failures...)
}

func acquireRateLimitTestLock(t *testing.T, pool *pgxpool.Pool) *pgxpool.Conn {
	t.Helper()
	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), 8*time.Second)
	conn, err := pool.Acquire(acquireCtx)
	acquireCancel()
	if err != nil {
		t.Fatalf("acquire rate-limit test coordination connection: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	_, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1, $2)`, rateLimitTestLockClassID, rateLimitTestLockObject)
	cancel()
	if err != nil {
		conn.Release()
		t.Fatalf("acquire rate-limit test coordination lock: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cleanupCancel()
		var unlocked bool
		if err := conn.QueryRow(cleanupCtx, `SELECT pg_advisory_unlock($1, $2)`, rateLimitTestLockClassID, rateLimitTestLockObject).Scan(&unlocked); err != nil || !unlocked {
			t.Errorf("release rate-limit test coordination lock: %v", err)
			raw := conn.Hijack()
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			if closeErr := raw.Close(closeCtx); closeErr != nil {
				t.Errorf("close connection holding rate-limit test lock: %v", closeErr)
			}
			return
		}
		conn.Release()
	})
	return conn
}

// registerRateLimitTestCleanup serializes cooperating tests and refuses to
// claim any key present before setup. Call it before authentication requests.
func registerRateLimitTestCleanup(t *testing.T, pool *pgxpool.Pool, remoteIPs, emails []string) {
	t.Helper()
	conn := acquireRateLimitTestLock(t, pool)
	store := postgresRateLimitCleanupStore{conn: conn}
	hashes := rateLimitCleanupHashes(remoteIPs, emails)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	err := requireRateLimitKeysAbsent(ctx, store, hashes)
	cancel()
	if err != nil {
		t.Fatalf("refusing unsafe rate-limit test setup: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := cleanupOwnedRateLimitKeys(ctx, store, hashes); err != nil {
			t.Errorf("rate-limit test cleanup failed: %v", err)
		}
	})
}

type fakeRateLimitCleanupStore struct {
	mu        sync.Mutex
	keys      map[string]bool
	deleteErr error
	existsErr error
}

func (s *fakeRateLimitCleanupStore) Exists(_ context.Context, hash []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.existsErr != nil {
		return false, s.existsErr
	}
	return s.keys[string(hash)], nil
}

func (s *fakeRateLimitCleanupStore) Delete(_ context.Context, hash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.keys, string(hash))
	return nil
}

func TestRateLimitCleanupRefusesPreexistingKey(t *testing.T) {
	preexisting := []byte("existing-key-hash")
	store := &fakeRateLimitCleanupStore{keys: map[string]bool{string(preexisting): true}}
	if err := requireRateLimitKeysAbsent(context.Background(), store, [][]byte{preexisting}); err == nil {
		t.Fatal("pre-existing key was accepted as test-owned")
	}
	if exists, _ := store.Exists(context.Background(), preexisting); !exists {
		t.Fatal("pre-existing key was changed")
	}
}

func TestRateLimitCleanupDeletesOnlyCurrentRunKeys(t *testing.T) {
	owned := []byte("current-run-key")
	otherRun := []byte("other-run-key")
	store := &fakeRateLimitCleanupStore{keys: map[string]bool{string(owned): true, string(otherRun): true}}
	if err := cleanupOwnedRateLimitKeys(context.Background(), store, [][]byte{owned}); err != nil {
		t.Fatalf("clean current-run key: %v", err)
	}
	if exists, _ := store.Exists(context.Background(), owned); exists {
		t.Fatal("current-run key remained")
	}
	if exists, _ := store.Exists(context.Background(), otherRun); !exists {
		t.Fatal("cleanup removed another run's key")
	}
}

func TestRateLimitCleanupReportsStorageFailures(t *testing.T) {
	hash := []byte("current-run-key")
	store := &fakeRateLimitCleanupStore{keys: map[string]bool{string(hash): true}, deleteErr: errors.New("storage failure")}
	if err := cleanupOwnedRateLimitKeys(context.Background(), store, [][]byte{hash}); err == nil {
		t.Fatal("cleanup failure was hidden")
	}
	if exists, _ := store.Exists(context.Background(), hash); !exists {
		t.Fatal("failed deletion unexpectedly changed the key")
	}
	store.deleteErr = nil
	store.existsErr = errors.New("verification failure")
	if err := requireRateLimitKeysAbsent(context.Background(), store, [][]byte{hash}); err == nil {
		t.Fatal("preflight verification failure was hidden")
	}
}

func TestRateLimitCleanupConcurrentRunsRemainIsolated(t *testing.T) {
	keysA := [][]byte{[]byte("run-a-key")}
	keysB := [][]byte{[]byte("run-b-key")}
	store := &fakeRateLimitCleanupStore{keys: map[string]bool{string(keysA[0]): true, string(keysB[0]): true}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, owned := range [][][]byte{keysA, keysB} {
		wg.Add(1)
		go func(hashes [][]byte) {
			defer wg.Done()
			errs <- cleanupOwnedRateLimitKeys(context.Background(), store, hashes)
		}(owned)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent exact-key cleanup: %v", err)
		}
	}
	if len(store.keys) != 0 {
		t.Fatalf("owned cleanup keys remain: %d", len(store.keys))
	}
}
