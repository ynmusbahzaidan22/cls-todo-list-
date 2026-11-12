package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/admin"
)

// AdminRepository contains explicitly scoped read-only queries for administrator views.
type AdminRepository struct {
	pool *pgxpool.Pool
}

func NewAdminRepository(pool *pgxpool.Pool) *AdminRepository {
	return &AdminRepository{pool: pool}
}

func (r *AdminRepository) ListAdminUsers(ctx context.Context, actorID, search string, cursor *admin.Cursor, maxRows int) ([]admin.User, error) {
	var cursorTime, cursorID, snapshotAt any
	if cursor != nil {
		cursorTime, cursorID = cursor.CreatedAt, cursor.ID
		snapshotAt = cursor.SnapshotAt
	}
	rows, err := r.pool.Query(ctx, `
		WITH snapshot AS (SELECT COALESCE($1::timestamptz, statement_timestamp()) AS at)
		SELECT u.id::text, u.name, u.email, u.role, u.status, u.created_at, snapshot.at,
		       u.id <> $5::uuid,
	       u.id <> $5::uuid AND NOT (u.role='ADMIN' AND u.status='ACTIVE' AND (SELECT count(*) FROM app.users WHERE role='ADMIN' AND status='ACTIVE') <= 1),
	       u.id <> $5::uuid,
	       CASE WHEN u.id=$5::uuid THEN 'self' WHEN u.role='ADMIN' AND u.status='ACTIVE' AND (SELECT count(*) FROM app.users WHERE role='ADMIN' AND status='ACTIVE') <= 1 THEN 'last_active_admin' ELSE '' END
		FROM app.users AS u CROSS JOIN snapshot
		WHERE u.created_at <= snapshot.at AND u.updated_at <= snapshot.at
		  AND ($6 = '' OR strpos(lower(u.name), $6) > 0)
		  AND ($2::timestamptz IS NULL OR (u.created_at, u.id) < ($2::timestamptz, $3::uuid))
		ORDER BY u.created_at DESC, u.id DESC
		LIMIT $4`, snapshotAt, cursorTime, cursorID, maxRows, actorID, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]admin.User, 0, maxRows)
	for rows.Next() {
		var item admin.User
		if err := rows.Scan(&item.ID, &item.Name, &item.Email, &item.Role, &item.Status, &item.CreatedAt, &item.SnapshotAt, &item.CanChangeRole, &item.CanChangeStatus, &item.CanRevokeSessions, &item.ProtectionReason); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *AdminRepository) ListAdminTasks(ctx context.Context, status, state string, cursor *admin.Cursor, maxRows int) ([]admin.Task, error) {
	var cursorTime, cursorID, snapshotAt any
	if cursor != nil {
		cursorTime, cursorID = cursor.CreatedAt, cursor.ID
		snapshotAt = cursor.SnapshotAt
	}
	rows, err := r.pool.Query(ctx, `
		WITH snapshot AS (SELECT COALESCE($3::timestamptz, statement_timestamp()) AS at)
		SELECT t.id::text, t.user_id::text, u.name,
		       t.title, COALESCE(t.description, ''), t.status, t.priority,
	       t.due_date, t.completed_at, t.version, t.created_at, t.updated_at, t.deleted_at,
	       COALESCE(t.due_date < ((statement_timestamp() AT TIME ZONE COALESCE(u.time_zone, 'UTC'))::date)
		         AND t.status <> 'DONE' AND t.deleted_at IS NULL, FALSE) AS is_overdue,
		       snapshot.at
		FROM app.tasks AS t CROSS JOIN snapshot
		JOIN app.users AS u ON u.id = t.user_id
		WHERE ($1 = '' OR t.status = $1)
		  AND (($2 = 'active' AND t.deleted_at IS NULL)
		       OR ($2 = 'deleted' AND t.deleted_at IS NOT NULL)
		       OR $2 = 'all')
		  AND t.created_at <= snapshot.at AND t.updated_at <= snapshot.at
		  AND ($4::timestamptz IS NULL OR (t.created_at, t.id) < ($4::timestamptz, $5::uuid))
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT $6`, status, state, snapshotAt, cursorTime, cursorID, maxRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]admin.Task, 0, maxRows)
	for rows.Next() {
		var item admin.Task
		var dueDate pgtype.Date
		var completedAt, deletedAt pgtype.Timestamptz
		if err := rows.Scan(
			&item.ID, &item.OwnerID, &item.OwnerName,
			&item.Title, &item.Description, &item.Status, &item.Priority,
			&dueDate, &completedAt, &item.Version, &item.CreatedAt, &item.UpdatedAt, &deletedAt, &item.IsOverdue, &item.SnapshotAt,
		); err != nil {
			return nil, err
		}
		if dueDate.Valid && dueDate.InfinityModifier == pgtype.Finite {
			value := dueDate.Time.Format("2006-01-02")
			item.DueDate = &value
		}
		if completedAt.Valid {
			value := completedAt.Time
			item.CompletedAt = &value
		}
		if deletedAt.Valid {
			value := deletedAt.Time
			item.DeletedAt = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

var _ admin.Repository = (*AdminRepository)(nil)

func (r *AdminRepository) ChangeUserRole(ctx context.Context, actorID, targetID, role string) (admin.MutationResult, error) {
	return r.mutateAccount(ctx, actorID, targetID, "USER_ROLE_CHANGED", role, "")
}

func (r *AdminRepository) ChangeUserStatus(ctx context.Context, actorID, targetID, status string) (admin.MutationResult, error) {
	return r.mutateAccount(ctx, actorID, targetID, "USER_STATUS_CHANGED", "", status)
}

func (r *AdminRepository) RevokeUserSessions(ctx context.Context, actorID, targetID string) (admin.MutationResult, error) {
	return r.mutateAccount(ctx, actorID, targetID, "USER_SESSIONS_REVOKED", "", "")
}

func (r *AdminRepository) mutateAccount(ctx context.Context, actorID, targetID, action, role, status string) (result admin.MutationResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout = '3s'`); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout = '7s'`); err != nil {
		return result, err
	}
	// Serialize all account-management changes before reloading the actor from
	// PostgreSQL. This prevents stale session role state and lock-order races.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(804201014)`); err != nil {
		return result, err
	}
	var actorRole, actorStatus string
	err = tx.QueryRow(ctx, `SELECT role, status FROM app.users WHERE id=$1 FOR UPDATE`, actorID).Scan(&actorRole, &actorStatus)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (actorRole != admin.RoleAdmin || actorStatus != admin.StatusActive)) {
		return result, admin.ErrForbidden
	}
	if err != nil {
		return result, err
	}
	var beforeRole, beforeStatus string
	err = tx.QueryRow(ctx, `SELECT role, status FROM app.users WHERE id=$1 FOR UPDATE`, targetID).Scan(&beforeRole, &beforeStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, admin.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	var activeBefore int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM app.sessions WHERE user_id=$1 AND revoked_at IS NULL`, targetID).Scan(&activeBefore); err != nil {
		return result, err
	}
	var afterRole, afterStatus string
	switch action {
	case "USER_ROLE_CHANGED":
		afterRole, afterStatus = role, beforeStatus
		_, err = tx.Exec(ctx, `UPDATE app.users SET role=$2, updated_at=clock_timestamp() WHERE id=$1`, targetID, role)
	case "USER_STATUS_CHANGED":
		afterRole, afterStatus = beforeRole, status
		_, err = tx.Exec(ctx, `UPDATE app.users SET status=$2, updated_at=clock_timestamp() WHERE id=$1`, targetID, status)
	case "USER_SESSIONS_REVOKED":
		afterRole, afterStatus = beforeRole, beforeStatus
		err = tx.QueryRow(ctx, `WITH revoked AS (UPDATE app.sessions SET revoked_at=COALESCE(revoked_at, clock_timestamp()) WHERE user_id=$1 AND revoked_at IS NULL RETURNING 1) SELECT count(*) FROM revoked`, targetID).Scan(&result.RevokedSessions)
	default:
		return result, fmt.Errorf("unsupported account audit action")
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "users_last_active_admin" {
			return result, admin.ErrConflict
		}
		return result, err
	}
	if action != "USER_SESSIONS_REVOKED" {
		if err = tx.QueryRow(ctx, `WITH revoked AS (UPDATE app.sessions SET revoked_at=COALESCE(revoked_at, clock_timestamp()) WHERE user_id=$1 AND revoked_at IS NULL RETURNING 1) SELECT count(*) FROM revoked`, targetID).Scan(&result.RevokedSessions); err != nil {
			return result, err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM app.users WHERE id=$1`, targetID).Scan(&result.User.ID); err != nil {
		return result, err
	}
	result.User.Role, result.User.Status = afterRole, afterStatus
	var beforeJSON, afterJSON string
	if action == "USER_SESSIONS_REVOKED" {
		beforeJSON = fmt.Sprintf(`{"active_sessions":%d}`, result.RevokedSessions)
		afterJSON = `{"active_sessions":0}`
	} else {
		beforeJSON = fmt.Sprintf(`{"role":%q,"status":%q,"active_sessions":%d}`, beforeRole, beforeStatus, activeBefore)
		afterJSON = fmt.Sprintf(`{"role":%q,"status":%q,"active_sessions":0}`, afterRole, afterStatus)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO app.admin_audit_events(actor_user_id,target_user_id,action,before_state,after_state) VALUES($1,$2,$3,$4::jsonb,$5::jsonb)`, actorID, targetID, action, beforeJSON, afterJSON); err != nil {
		return result, err
	}
	if err = tx.Commit(ctx); err != nil {
		return result, err
	}
	return result, nil
}
