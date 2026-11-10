package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PreferenceRepository struct{ pool *pgxpool.Pool }

func NewPreferenceRepository(pool *pgxpool.Pool) *PreferenceRepository {
	return &PreferenceRepository{pool: pool}
}

func (r *PreferenceRepository) TimeZone(ctx context.Context, userID string) (string, error) {
	var zone string
	err := r.pool.QueryRow(ctx, `SELECT time_zone FROM app.users WHERE id = $1`, userID).Scan(&zone)
	return zone, err
}

func (r *PreferenceRepository) SetTimeZone(ctx context.Context, userID, zone string) error {
	_, err := r.pool.Exec(ctx, `UPDATE app.users SET time_zone = $2 WHERE id = $1`, userID, zone)
	return err
}
