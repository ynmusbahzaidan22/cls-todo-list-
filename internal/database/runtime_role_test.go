package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestVerifyRuntimeRolePostgreSQL(t *testing.T) {
	connectionString := os.Getenv("RUNTIME_DATABASE_URL")
	if connectionString == "" {
		t.Skip("RUNTIME_DATABASE_URL is not set; least-privilege PostgreSQL check skipped")
	}
	parsed, err := url.Parse(connectionString)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		t.Fatal("RUNTIME_DATABASE_URL must target loopback PostgreSQL")
	}
	pool, err := Open(context.Background(), connectionString, 2)
	if err != nil {
		t.Fatalf("open runtime database pool: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := VerifyRuntimeRole(ctx, pool, parsed.User.Username()); err != nil {
		t.Fatalf("runtime role privilege verification failed: %v", err)
	}

	var canSelect, canInsert, canUpdate, canDelete, canTruncate bool
	if err := pool.QueryRow(ctx, `
		SELECT has_table_privilege(current_user, 'app.task_activity', 'SELECT'),
		       has_table_privilege(current_user, 'app.task_activity', 'INSERT'),
		       has_table_privilege(current_user, 'app.task_activity', 'UPDATE'),
		       has_table_privilege(current_user, 'app.task_activity', 'DELETE'),
		       has_table_privilege(current_user, 'app.task_activity', 'TRUNCATE')
	`).Scan(&canSelect, &canInsert, &canUpdate, &canDelete, &canTruncate); err != nil {
		t.Fatalf("inspect activity privileges: %v", err)
	}
	if !canSelect || !canInsert || canUpdate || canDelete || canTruncate {
		t.Fatalf("unexpected activity privileges: SELECT=%t INSERT=%t UPDATE=%t DELETE=%t TRUNCATE=%t", canSelect, canInsert, canUpdate, canDelete, canTruncate)
	}
	t.Logf("runtime role %q effective app.task_activity privileges: SELECT=%t INSERT=%t UPDATE=%t DELETE=%t TRUNCATE=%t", parsed.User.Username(), canSelect, canInsert, canUpdate, canDelete, canTruncate)
	if _, err := pool.Exec(ctx, `SELECT id FROM app.task_activity LIMIT 0`); err != nil {
		t.Fatalf("activity SELECT failed: %v", err)
	}

	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate runtime privilege test identity: %v", err)
	}
	email := "runtime-privilege-" + hex.EncodeToString(suffix[:]) + "@example.test"
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin runtime privilege transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	var userID, taskID, activityID string
	if err := tx.QueryRow(ctx, `INSERT INTO app.users (email, password_hash) VALUES ($1, 'runtime-check-hash') RETURNING id::text`, email).Scan(&userID); err != nil {
		t.Fatalf("runtime role could not insert a test user inside rollback-only transaction: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO app.tasks (user_id, title) VALUES ($1, 'runtime privilege verification') RETURNING id::text`, userID).Scan(&taskID); err != nil {
		t.Fatalf("runtime role could not insert a test task inside rollback-only transaction: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO app.task_activity (task_id, user_id, actor_user_id, event_type) VALUES ($1, $2, $2, 'TASK_CREATED') RETURNING id::text`, taskID, userID).Scan(&activityID); err != nil {
		t.Fatalf("runtime role could not insert activity: %v", err)
	}
	var selected string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM app.task_activity WHERE id=$1 AND user_id=$2`, activityID, userID).Scan(&selected); err != nil || selected != activityID {
		t.Fatalf("runtime role could not read inserted activity: id=%q err=%v", selected, err)
	}
	_, updateErr := tx.Exec(ctx, `UPDATE app.task_activity SET event_type='TASK_UPDATED' WHERE id=$1`, activityID)
	var pgError *pgconn.PgError
	if !errors.As(updateErr, &pgError) || pgError.Code != "42501" {
		t.Fatalf("activity UPDATE error = %v, want PostgreSQL permission denied (42501)", updateErr)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback runtime privilege fixtures: %v", err)
	}
	var residual int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE email=$1`, email).Scan(&residual); err != nil || residual != 0 {
		t.Fatalf("rollback left runtime privilege fixture rows: count=%d err=%v", residual, err)
	}

	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Fatal("TEST_DATABASE_URL is required to confirm administrative privileges remain unchanged")
	}
	adminParsed, err := url.Parse(adminURL)
	if err != nil || (adminParsed.Hostname() != "127.0.0.1" && adminParsed.Hostname() != "localhost" && adminParsed.Hostname() != "::1") {
		t.Fatal("TEST_DATABASE_URL must target a loopback PostgreSQL server")
	}
	adminPool, err := Open(context.Background(), adminURL, 1)
	if err != nil {
		t.Fatalf("open administrative verification connection: %v", err)
	}
	defer adminPool.Close()
	var adminName string
	var adminSuperuser, adminCanUpdate bool
	if err := adminPool.QueryRow(ctx, `SELECT current_user, rolsuper, has_table_privilege(current_user, 'app.task_activity', 'UPDATE') FROM pg_roles WHERE rolname=current_user`).Scan(&adminName, &adminSuperuser, &adminCanUpdate); err != nil {
		t.Fatalf("inspect administrative role: %v", err)
	}
	if !adminSuperuser || !adminCanUpdate {
		t.Fatalf("administrative role %q unexpectedly changed: superuser=%t activity_update=%t", adminName, adminSuperuser, adminCanUpdate)
	}
	t.Logf("administrative role %q remains superuser=%t with activity UPDATE=%t", adminName, adminSuperuser, adminCanUpdate)

	var probeSuffix [8]byte
	if _, err := rand.Read(probeSuffix[:]); err != nil {
		t.Fatalf("generate role-verification probe name: %v", err)
	}
	probeRole := "runtime_verify_" + hex.EncodeToString(probeSuffix[:])
	quotedProbeRole := pgx.Identifier{probeRole}.Sanitize()
	if _, err := adminPool.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS", quotedProbeRole)); err != nil {
		t.Fatalf("create isolated role-verification probe: %v", err)
	}
	probeRoleExists := true
	t.Cleanup(func() {
		if probeRoleExists {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_, _ = adminPool.Exec(cleanupCtx, "REVOKE UPDATE ON app.task_activity FROM "+quotedProbeRole)
			_, _ = adminPool.Exec(cleanupCtx, "DROP ROLE "+quotedProbeRole)
		}
	})
	if _, err := adminPool.Exec(ctx, "GRANT UPDATE ON app.task_activity TO "+quotedProbeRole); err != nil {
		t.Fatalf("grant unsafe activity privilege to isolated probe: %v", err)
	}

	probeConfig, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("parse administrative URL for isolated role probe: %v", err)
	}
	probeConfig.MaxConns = 1
	probeConfig.AfterConnect = func(connectCtx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(connectCtx, "SET ROLE "+quotedProbeRole)
		return err
	}
	probePool, err := pgxpool.NewWithConfig(ctx, probeConfig)
	if err != nil {
		t.Fatalf("open isolated role-verification probe: %v", err)
	}
	probeErr := VerifyRuntimeRole(ctx, probePool, probeRole)
	probePool.Close()
	if probeErr == nil {
		t.Fatal("startup role verification accepted effective UPDATE on app.task_activity")
	}
	if _, err := adminPool.Exec(ctx, "REVOKE UPDATE ON app.task_activity FROM "+quotedProbeRole); err != nil {
		t.Fatalf("revoke unsafe activity privilege from isolated probe: %v", err)
	}
	if _, err := adminPool.Exec(ctx, "DROP ROLE "+quotedProbeRole); err != nil {
		t.Fatalf("drop isolated role-verification probe: %v", err)
	}
	probeRoleExists = false
}
