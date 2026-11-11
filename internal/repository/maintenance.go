package repository

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunMaintenance removes only expired authentication records in bounded batches.
func RunMaintenance(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := cleanupAuthenticationRecords(cleanupCtx, pool); err != nil && logger != nil && cleanupCtx.Err() == nil {
			logger.Warn("bounded authentication maintenance failed", "error_type", "database")
		}
	}
	cleanup()
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

func cleanupAuthenticationRecords(ctx context.Context, db rateLimitExecer) error {
	if _, err := db.Exec(ctx, `WITH expired AS (
		SELECT token_hash FROM app.sessions
		WHERE (idle_expires_at <= clock_timestamp() OR absolute_expires_at <= clock_timestamp() OR revoked_at < clock_timestamp() - interval '30 days')
		ORDER BY COALESCE(revoked_at, LEAST(idle_expires_at, absolute_expires_at))
		LIMIT 500 FOR UPDATE SKIP LOCKED
	) DELETE FROM app.sessions s USING expired e WHERE s.token_hash = e.token_hash`); err != nil {
		return err
	}
	_, err := deleteExpiredRateLimitBatch(ctx, db, rateLimitCleanupBatchSize)
	return err
}
