package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type fakeRateLimitFixtureCleanupStore struct {
	mu        sync.Mutex
	keys      map[string]bool
	deleteErr error
	existsErr error
}

func (s *fakeRateLimitFixtureCleanupStore) Exists(_ context.Context, hash []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.existsErr != nil {
		return false, s.existsErr
	}
	return s.keys[string(hash)], nil
}

func (s *fakeRateLimitFixtureCleanupStore) Delete(_ context.Context, hash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.keys, string(hash))
	return nil
}

func TestRateLimitFixtureCleanupRefusesPreexistingKey(t *testing.T) {
	preexisting := []byte("pre-existing-fixture")
	store := &fakeRateLimitFixtureCleanupStore{keys: map[string]bool{string(preexisting): true}}
	if err := requireRateLimitFixtureKeysAbsent(context.Background(), store, [][]byte{preexisting}); err == nil {
		t.Fatal("pre-existing fixture was accepted as test-owned")
	}
	if exists, _ := store.Exists(context.Background(), preexisting); !exists {
		t.Fatal("pre-existing fixture was changed")
	}
}

func TestRateLimitFixtureCleanupRemovesOnlyOwnedKey(t *testing.T) {
	owned := []byte("owned-fixture")
	otherRun := []byte("other-run-fixture")
	store := &fakeRateLimitFixtureCleanupStore{keys: map[string]bool{string(owned): true, string(otherRun): true}}
	if err := cleanupRateLimitFixtureKeys(context.Background(), store, [][]byte{owned}); err != nil {
		t.Fatalf("remove test-owned fixture: %v", err)
	}
	if exists, _ := store.Exists(context.Background(), owned); exists {
		t.Fatal("owned fixture remained")
	}
	if exists, _ := store.Exists(context.Background(), otherRun); !exists {
		t.Fatal("cleanup removed another run's fixture")
	}
}

func TestRateLimitFixtureCleanupReportsFailure(t *testing.T) {
	hash := []byte("owned-fixture")
	store := &fakeRateLimitFixtureCleanupStore{keys: map[string]bool{string(hash): true}, deleteErr: errors.New("storage failure")}
	if err := cleanupRateLimitFixtureKeys(context.Background(), store, [][]byte{hash}); err == nil {
		t.Fatal("cleanup failure was hidden")
	}
	if exists, _ := store.Exists(context.Background(), hash); !exists {
		t.Fatal("failed deletion unexpectedly changed the fixture")
	}
}

func TestRateLimitFixtureCleanupConcurrentRunsRemainIsolated(t *testing.T) {
	runA := [][]byte{[]byte("run-a-fixture")}
	runB := [][]byte{[]byte("run-b-fixture")}
	store := &fakeRateLimitFixtureCleanupStore{keys: map[string]bool{string(runA[0]): true, string(runB[0]): true}}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, hashes := range [][][]byte{runA, runB} {
		wg.Add(1)
		go func(owned [][]byte) {
			defer wg.Done()
			errs <- cleanupRateLimitFixtureKeys(context.Background(), store, owned)
		}(hashes)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent fixture cleanup: %v", err)
		}
	}
	if len(store.keys) != 0 {
		t.Fatalf("owned fixture keys remain: %d", len(store.keys))
	}
}

func TestRateLimitCleanupLimitIsBoundedAndFreesRequiredCapacity(t *testing.T) {
	tests := []struct {
		name        string
		current     int
		maximum     int
		wantCleanup int
	}{
		{name: "below capacity does not clean", current: 9, maximum: 10, wantCleanup: 0},
		{name: "at capacity frees one slot", current: 10, maximum: 10, wantCleanup: 1},
		{name: "over capacity frees only the required rows", current: 14, maximum: 10, wantCleanup: 5},
		{name: "one batch maximum", current: 5_000, maximum: 1, wantCleanup: rateLimitCleanupBatchSize},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := rateLimitCleanupLimit(test.current, test.maximum); got != test.wantCleanup {
				t.Fatalf("rateLimitCleanupLimit(%d, %d) = %d, want %d", test.current, test.maximum, got, test.wantCleanup)
			}
		})
	}
}
