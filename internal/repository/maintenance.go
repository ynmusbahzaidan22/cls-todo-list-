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
		for _, statement := range []string{
			`WITH expired AS (
				SELECT token_hash FROM app.sessions
				WHERE (idle_expires_at <= clock_timestamp() OR absolute_expires_at <= clock_timestamp() OR revoked_at < clock_timestamp() - interval '30 days')
				ORDER BY COALESCE(revoked_at, LEAST(idle_expires_at, absolute_expires_at))
				LIMIT 500 FOR UPDATE SKIP LOCKED
			) DELETE FROM app.sessions s USING expired e WHERE s.token_hash = e.token_hash`,
			`WITH expired AS (
				SELECT key_hash FROM app.auth_rate_limits
				WHERE expires_at <= clock_timestamp()
				ORDER BY expires_at LIMIT 1000 FOR UPDATE SKIP LOCKED
			) DELETE FROM app.auth_rate_limits r USING expired e WHERE r.key_hash = e.key_hash`,
		} {
			if _, err := pool.Exec(cleanupCtx, statement); err != nil {
				if logger != nil && cleanupCtx.Err() == nil {
					logger.Warn("bounded authentication maintenance failed", "error_type", "database")
				}
				return
			}
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
