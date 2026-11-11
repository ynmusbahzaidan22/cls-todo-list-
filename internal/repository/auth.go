package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
)

type AuthRepository struct {
	pool *pgxpool.Pool
}

const (
	maxRateLimitEntries        = 100_000
	rateLimitCleanupBatchSize  = 1_000
	maxRateLimitCleanupBatches = 100
)

type rateLimitExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func NewAuthRepository(pool *pgxpool.Pool) *AuthRepository {
	return &AuthRepository{pool: pool}
}

func (r *AuthRepository) CreateAccount(ctx context.Context, name, email, passwordHash string) (auth.User, error) {
	var user auth.User
	err := r.pool.QueryRow(ctx, `
		INSERT INTO app.users (name, email, password_hash, status, role)
		VALUES ($1, $2, $3, 'ACTIVE', 'USER')
		ON CONFLICT (email) DO NOTHING
		RETURNING id::text, name, email, role, created_at`, name, email, passwordHash,
	).Scan(&user.ID, &user.Name, &user.Email, &user.Role, &user.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrEmailTaken
	}
	return user, err
}

// AllowRateLimit stores fixed-window counters in PostgreSQL so separate API
// instances enforce the same limit. New key creation is capped and fails closed.
func (r *AuthRepository) AllowRateLimit(ctx context.Context, key string, maxAttempts int, window time.Duration) (bool, error) {
	return r.allowRateLimitWithLimit(ctx, key, maxAttempts, window, maxRateLimitEntries)
}

// allowRateLimitWithLimit keeps the production path and capacity policy in one
// implementation while allowing integration tests to exercise the cap with a
// small, isolated fixture set.
func (r *AuthRepository) allowRateLimitWithLimit(ctx context.Context, key string, maxAttempts int, window time.Duration, maxEntries int) (bool, error) {
	if maxEntries < 1 {
		return false, errors.New("rate-limit entry limit must be positive")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	allowed, err := r.allowRateLimitInTransaction(ctx, tx, key, maxAttempts, window, maxEntries)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return allowed, nil
}

func (r *AuthRepository) allowRateLimitInTransaction(ctx context.Context, tx pgx.Tx, key string, maxAttempts int, window time.Duration, maxEntries int) (bool, error) {
	if maxEntries < 1 {
		return false, errors.New("rate-limit entry limit must be positive")
	}
	keyHash := sha256.Sum256([]byte(key))
	allowed, found, err := updateRateLimitCounter(ctx, tx, keyHash[:], maxAttempts, window)
	if err != nil || found {
		return allowed, err
	}

	// Serialize only the creation of new keys. Existing keys update independently.
	var lockResult any
	if err := tx.QueryRow(ctx, `SELECT pg_advisory_xact_lock(731920241)`).Scan(&lockResult); err != nil {
		return false, err
	}
	allowed, found, err = updateRateLimitCounter(ctx, tx, keyHash[:], maxAttempts, window)
	if err != nil {
		return false, err
	}
	if found {
		return allowed, nil
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.auth_rate_limits`).Scan(&count); err != nil {
		return false, err
	}
	for batches := 0; count >= maxEntries; batches++ {
		if batches == maxRateLimitCleanupBatches {
			return false, nil
		}
		limit := rateLimitCleanupLimit(count, maxEntries)
		deleted, err := deleteExpiredRateLimitBatch(ctx, tx, limit)
		if err != nil {
			return false, err
		}
		if deleted == 0 {
			return false, nil
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.auth_rate_limits`).Scan(&count); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO app.auth_rate_limits (key_hash, window_started_at, attempt_count, expires_at)
		VALUES ($1, clock_timestamp(), 1, clock_timestamp() + $2 * interval '1 second')
	`, keyHash[:], window.Seconds()); err != nil {
		return false, err
	}
	return true, nil
}

func rateLimitCleanupLimit(currentCount, maxEntries int) int {
	if currentCount < maxEntries {
		return 0
	}
	needed := currentCount - maxEntries + 1
	if needed > rateLimitCleanupBatchSize {
		return rateLimitCleanupBatchSize
	}
	return needed
}

// deleteExpiredRateLimitBatch removes only expired rows that it can lock. The
// row locks prevent a concurrent renewal from being deleted using a stale
// candidate list; the outer expiry predicate is a second guard at deletion.
func deleteExpiredRateLimitBatch(ctx context.Context, db rateLimitExecer, limit int) (int64, error) {
	return deleteExpiredRateLimitBatchForKeys(ctx, db, limit, nil)
}

// deleteExpiredRateLimitBatchForKeys is used by integration tests to constrain
// cleanup to their own generated fixtures. Production maintenance and capacity
// handling use deleteExpiredRateLimitBatch with no key filter.
func deleteExpiredRateLimitBatchForKeys(ctx context.Context, db rateLimitExecer, limit int, keyHashes [][]byte) (int64, error) {
	if limit < 1 || limit > rateLimitCleanupBatchSize {
		return 0, errors.New("rate-limit cleanup batch limit is out of range")
	}
	args := []any{limit}
	filter := ""
	if keyHashes != nil {
		if len(keyHashes) == 0 {
			return 0, errors.New("rate-limit cleanup key filter must not be empty")
		}
		placeholders := make([]string, len(keyHashes))
		for i, keyHash := range keyHashes {
			args = append(args, keyHash)
			placeholders[i] = fmt.Sprintf("$%d", i+2)
		}
		filter = " AND key_hash IN (" + strings.Join(placeholders, ",") + ")"
	}
	query := fmt.Sprintf(`
		WITH expired AS (
			SELECT key_hash
			FROM app.auth_rate_limits
			WHERE expires_at <= clock_timestamp()%s
			ORDER BY expires_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM app.auth_rate_limits AS r
		USING expired
		WHERE r.key_hash = expired.key_hash
		  AND r.expires_at <= clock_timestamp()`, filter)
	tag, err := db.Exec(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func updateRateLimitCounter(ctx context.Context, tx pgx.Tx, keyHash []byte, maxAttempts int, window time.Duration) (allowed, found bool, err error) {
	err = tx.QueryRow(ctx, `
		UPDATE app.auth_rate_limits
		SET window_started_at = CASE WHEN expires_at <= clock_timestamp() THEN clock_timestamp() ELSE window_started_at END,
		    attempt_count = CASE WHEN expires_at <= clock_timestamp() THEN 1 ELSE LEAST(attempt_count + 1, $2 + 1) END,
	    expires_at = CASE WHEN expires_at <= clock_timestamp() THEN clock_timestamp() + $3 * interval '1 second' ELSE expires_at END
		WHERE key_hash = $1
		RETURNING attempt_count <= $2
	`, keyHash, maxAttempts, window.Seconds()).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	return allowed, err == nil, err
}

func (r *AuthRepository) FindAccountByEmail(ctx context.Context, email string) (auth.Account, error) {
	var account auth.Account
	err := r.pool.QueryRow(ctx, `
		SELECT id::text, name, email, role, password_hash, status, created_at
		FROM app.users
		WHERE email = $1`, email,
	).Scan(&account.ID, &account.Name, &account.Email, &account.Role, &account.PasswordHash, &account.Status, &account.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Account{}, auth.ErrAccountNotFound
	}
	return account, err
}

func (r *AuthRepository) CreateSession(ctx context.Context, tokenHash []byte, userID string, createdAt, idleExpiry, absoluteExpiry time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO app.sessions (token_hash, user_id, created_at, last_activity_at, idle_expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, $3, $4, $5)`, tokenHash, userID, createdAt, idleExpiry, absoluteExpiry,
	)
	return err
}

func (r *AuthRepository) AuthenticateAndTouchSession(ctx context.Context, tokenHash []byte, now, idleExpiry time.Time) (auth.User, error) {
	var user auth.User
	err := r.pool.QueryRow(ctx, `
		UPDATE app.sessions AS s
		SET last_activity_at = $2,
		    idle_expires_at = LEAST($3, s.absolute_expires_at)
		FROM app.users AS u
		WHERE s.token_hash = $1
		  AND s.user_id = u.id
		  AND u.status = 'ACTIVE'
		  AND s.revoked_at IS NULL
		  AND s.idle_expires_at > $2
		  AND s.absolute_expires_at > $2
		RETURNING u.id::text, u.name, u.email, u.role, u.created_at`, tokenHash, now, idleExpiry,
	).Scan(&user.ID, &user.Name, &user.Email, &user.Role, &user.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrUnauthenticated
	}
	return user, err
}

func (r *AuthRepository) RevokeSession(ctx context.Context, tokenHash []byte, now time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE app.sessions
		SET revoked_at = COALESCE(revoked_at, $2)
		WHERE token_hash = $1`, tokenHash, now,
	)
	return err
}
