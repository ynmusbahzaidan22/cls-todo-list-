package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/adminbootstrap"
)

// AdminBootstrapRepository is intended for the guarded local operational CLI,
// which connects with the database administrator account, never the runtime role.
type AdminBootstrapRepository struct{ pool *pgxpool.Pool }

func NewAdminBootstrapRepository(pool *pgxpool.Pool) *AdminBootstrapRepository {
	return &AdminBootstrapRepository{pool: pool}
}

func (r *AdminBootstrapRepository) Status(ctx context.Context, email string) (adminbootstrap.Status, error) {
	var status adminbootstrap.Status
	var targetName string
	err := r.pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM app.users WHERE role='ADMIN' AND status='ACTIVE'),
		  g.active_count,
		  (u.id IS NOT NULL),
		  COALESCE(u.status='ACTIVE', false),
	  COALESCE(u.role,''),
		  COALESCE(u.name,''),
		  (SELECT count(*) FROM app.sessions s WHERE s.user_id=u.id AND s.revoked_at IS NULL)
		FROM app.active_admin_guard AS g
		LEFT JOIN app.users AS u ON ($1::text <> '' AND u.email=$1)
		WHERE g.singleton=true`, email).Scan(&status.ActiveAdmins, &status.GuardAdmins, &status.TargetExists, &status.TargetActive, &status.TargetRole, &targetName, &status.TargetActiveSessions)
	if err != nil {
		return adminbootstrap.Status{}, fmt.Errorf("read bootstrap status")
	}
	status.Eligible = status.TargetExists && status.TargetActive && status.TargetRole == "USER" && strings.TrimSpace(targetName) != "" && len([]rune(targetName)) <= 100
	return status, nil
}

func (r *AdminBootstrapRepository) Bootstrap(ctx context.Context, email string) (retErr error) {
	return r.bootstrap(ctx, email, true)
}

func (r *AdminBootstrapRepository) BootstrapPreservingSessions(ctx context.Context, email string) error {
	return r.bootstrap(ctx, email, false)
}

func (r *AdminBootstrapRepository) bootstrap(ctx context.Context, email string, revokeSessions bool) (retErr error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return errors.New("begin bootstrap transaction")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
		return errors.New("configure bootstrap lock timeout")
	}
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='10s'`); err != nil {
		return errors.New("configure bootstrap transaction")
	}
	// Share the same advisory guard used by Admin account mutations. The table
	// lock serializes any direct user-table writer during this one-time action.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(804201014)`); err != nil {
		return errors.New("lock bootstrap operation")
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE app.users IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return errors.New("lock account table for bootstrap")
	}
	var guardCount int64
	if err = tx.QueryRow(ctx, `SELECT active_count FROM app.active_admin_guard WHERE singleton=true FOR UPDATE`).Scan(&guardCount); err != nil {
		return errors.New("lock active administrator guard")
	}
	var activeAdmins int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE role='ADMIN' AND status='ACTIVE'`).Scan(&activeAdmins); err != nil {
		return errors.New("count active administrators")
	}
	if guardCount != 0 || activeAdmins != 0 {
		return adminbootstrap.ErrAdminExists
	}
	var id, name, role, status string
	err = tx.QueryRow(ctx, `SELECT id::text, name, role, status FROM app.users WHERE email=$1 FOR UPDATE`, email).Scan(&id, &name, &role, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return adminbootstrap.ErrNotFound
	}
	if err != nil {
		return errors.New("lock selected account")
	}
	if status != "ACTIVE" || role != "USER" || strings.TrimSpace(name) == "" || len([]rune(name)) > 100 {
		return adminbootstrap.ErrIneligible
	}
	var sessionsBefore int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM app.sessions WHERE user_id=$1::uuid AND revoked_at IS NULL`, id).Scan(&sessionsBefore); err != nil {
		return errors.New("count target sessions")
	}
	if _, err = tx.Exec(ctx, `UPDATE app.users SET role='ADMIN', updated_at=clock_timestamp() WHERE id=$1::uuid`, id); err != nil {
		return errors.New("promote selected account")
	}
	var sessionAuditKey string
	var sessionsAfter int64
	if revokeSessions {
		err = tx.QueryRow(ctx, `WITH changed AS (
			UPDATE app.sessions SET revoked_at=clock_timestamp()
			WHERE user_id=$1::uuid AND revoked_at IS NULL RETURNING 1
		) SELECT count(*) FROM changed`, id).Scan(&sessionsAfter)
		if err != nil {
			return errors.New("revoke target sessions")
		}
		sessionAuditKey = "revoked_sessions"
	} else {
		sessionsAfter = sessionsBefore
		sessionAuditKey = "preserved_sessions"
	}
	before, _ := json.Marshal(map[string]any{"role": role, "status": status, "active_sessions": sessionsBefore})
	after, _ := json.Marshal(map[string]any{"role": "ADMIN", "status": status, sessionAuditKey: sessionsAfter})
	if _, err = tx.Exec(ctx, `INSERT INTO app.admin_audit_events(actor_user_id,target_user_id,action,before_state,after_state)
		VALUES (NULL,$1::uuid,'INITIAL_ADMIN_BOOTSTRAPPED',$2::jsonb,$3::jsonb)`, id, string(before), string(after)); err != nil {
		return errors.New("write bootstrap audit event")
	}
	if err = tx.Commit(ctx); err != nil {
		return errors.New("commit bootstrap transaction")
	}
	return nil
}

var _ adminbootstrap.Repository = (*AdminBootstrapRepository)(nil)
