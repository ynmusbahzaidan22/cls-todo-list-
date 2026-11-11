package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/testdatabase"
)

func TestVerifyRuntimeRolePostgreSQL(t *testing.T) {
	adminURL, adminTarget := testdatabase.TestURL(t)
	connectionString, runtimeTarget := testdatabase.URL(t, "RUNTIME_DATABASE_URL", "")
	if !testdatabase.SameTarget(adminTarget, runtimeTarget) {
		t.Fatal("RUNTIME_DATABASE_URL must identify the same dedicated test database as TEST_DATABASE_URL")
	}
	parsed, err := pgx.ParseConfig(connectionString)
	if err != nil {
		t.Fatal("RUNTIME_DATABASE_URL must be a valid dedicated test-database URL")
	}
	pool, err := Open(context.Background(), connectionString, 2)
	if err != nil {
		t.Fatalf("open runtime database pool: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := VerifyRuntimeRole(ctx, pool, parsed.User); err != nil {
		t.Fatalf("runtime role privilege verification failed: %v", err)
	}
	var canLogin, superuser, canCreateDB, canCreateRole, inherit, replication, bypassRLS bool
	if err := pool.QueryRow(ctx, `
		SELECT rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolinherit, rolreplication, rolbypassrls
		FROM pg_roles WHERE rolname = current_user
	`).Scan(&canLogin, &superuser, &canCreateDB, &canCreateRole, &inherit, &replication, &bypassRLS); err != nil {
		t.Fatalf("inspect runtime role attributes: %v", err)
	}
	if !canLogin || superuser || canCreateDB || canCreateRole || inherit || replication || bypassRLS {
		t.Fatalf("runtime role attributes violate policy: LOGIN=%t SUPERUSER=%t CREATEDB=%t CREATEROLE=%t INHERIT=%t REPLICATION=%t BYPASSRLS=%t", canLogin, superuser, canCreateDB, canCreateRole, inherit, replication, bypassRLS)
	}

	var canSelectUsers, canInsertUsers, canUpdateUsersTable, canUpdateTimeZone bool
	var canUpdateRole, canUpdateStatus, canUpdateTimestamp bool
	var canDeleteUsers, canTruncateUsers, canReferenceUsers, canMaintainUsers bool
	var canUpdateOtherUsersColumns bool
	if err := pool.QueryRow(ctx, `
		SELECT has_table_privilege(current_user, 'app.users', 'SELECT'),
		       has_table_privilege(current_user, 'app.users', 'INSERT'),
		       has_table_privilege(current_user, 'app.users', 'UPDATE'),
		       has_column_privilege(current_user, 'app.users', 'time_zone', 'UPDATE'),
		       has_column_privilege(current_user, 'app.users', 'role', 'UPDATE'),
		       has_column_privilege(current_user, 'app.users', 'status', 'UPDATE'),
		       has_column_privilege(current_user, 'app.users', 'updated_at', 'UPDATE'),
		       EXISTS (
		           SELECT 1 FROM pg_attribute att
		           WHERE att.attrelid = 'app.users'::regclass
		             AND att.attnum > 0 AND NOT att.attisdropped
	             AND att.attname NOT IN ('time_zone', 'role', 'status', 'updated_at')
		             AND has_column_privilege(current_user, att.attrelid, att.attname, 'UPDATE')
		       ),
		       has_table_privilege(current_user, 'app.users', 'DELETE'),
		       has_table_privilege(current_user, 'app.users', 'TRUNCATE'),
		       has_table_privilege(current_user, 'app.users', 'REFERENCES') OR has_any_column_privilege(current_user, 'app.users', 'REFERENCES'),
		       has_table_privilege(current_user, 'app.users', 'MAINTAIN')
	`).Scan(&canSelectUsers, &canInsertUsers, &canUpdateUsersTable, &canUpdateTimeZone, &canUpdateRole, &canUpdateStatus, &canUpdateTimestamp, &canUpdateOtherUsersColumns,
		&canDeleteUsers, &canTruncateUsers, &canReferenceUsers, &canMaintainUsers); err != nil {
		t.Fatalf("inspect effective app.users privileges: %v", err)
	}
	if !canSelectUsers || !canInsertUsers || canUpdateUsersTable || !canUpdateTimeZone || !canUpdateRole || !canUpdateStatus || !canUpdateTimestamp || canUpdateOtherUsersColumns ||
		canDeleteUsers || canTruncateUsers || canReferenceUsers || canMaintainUsers {
		t.Fatalf("unexpected app.users privileges: SELECT=%t INSERT=%t table UPDATE=%t time_zone UPDATE=%t other-column UPDATE=%t DELETE=%t TRUNCATE=%t REFERENCES=%t MAINTAIN=%t",
			canSelectUsers, canInsertUsers, canUpdateUsersTable, canUpdateTimeZone, canUpdateOtherUsersColumns,
			canDeleteUsers, canTruncateUsers, canReferenceUsers, canMaintainUsers)
	}

	var canSelect, canInsert, canUpdate, canDelete, canTruncate bool
	if err := pool.QueryRow(ctx, `
		SELECT has_table_privilege(current_user, 'app.task_activity', 'SELECT'),
		       has_table_privilege(current_user, 'app.task_activity', 'INSERT'),
		       (has_table_privilege(current_user, 'app.task_activity', 'UPDATE') OR has_any_column_privilege(current_user, 'app.task_activity', 'UPDATE')),
		       has_table_privilege(current_user, 'app.task_activity', 'DELETE'),
		       has_table_privilege(current_user, 'app.task_activity', 'TRUNCATE')
	`).Scan(&canSelect, &canInsert, &canUpdate, &canDelete, &canTruncate); err != nil {
		t.Fatalf("inspect activity privileges: %v", err)
	}
	if !canSelect || !canInsert || canUpdate || canDelete || canTruncate {
		t.Fatalf("unexpected activity privileges: SELECT=%t INSERT=%t UPDATE=%t DELETE=%t TRUNCATE=%t", canSelect, canInsert, canUpdate, canDelete, canTruncate)
	}
	t.Logf("runtime role %q effective app.task_activity privileges: SELECT=%t INSERT=%t UPDATE=%t DELETE=%t TRUNCATE=%t", parsed.User, canSelect, canInsert, canUpdate, canDelete, canTruncate)
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
	if _, err := tx.Exec(ctx, `UPDATE app.users SET time_zone='America/New_York' WHERE id=$1`, userID); err != nil {
		t.Fatalf("runtime role could not update the permitted app.users.time_zone column: %v", err)
	}
	expectPermissionDeniedInSavepoint(t, ctx, tx, `UPDATE app.users SET email=email WHERE id='00000000-0000-0000-0000-000000000000'`)
	expectPermissionDeniedInSavepoint(t, ctx, tx, `DELETE FROM app.users WHERE id='00000000-0000-0000-0000-000000000000'`)
	expectPermissionDeniedInSavepoint(t, ctx, tx, `TRUNCATE app.users`)
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
	for _, operation := range []struct {
		name string
		sql  string
	}{
		{"activity UPDATE", `UPDATE app.task_activity SET event_type='TASK_UPDATED' WHERE id='00000000-0000-0000-0000-000000000000'`},
		{"activity DELETE", `DELETE FROM app.task_activity WHERE id='00000000-0000-0000-0000-000000000000'`},
		{"activity TRUNCATE", `TRUNCATE app.task_activity`},
	} {
		t.Run(operation.name, func(t *testing.T) { expectPermissionDeniedInSavepoint(t, ctx, tx, operation.sql) })
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback runtime privilege fixtures: %v", err)
	}
	var residual int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE email=$1`, email).Scan(&residual); err != nil || residual != 0 {
		t.Fatalf("rollback left runtime privilege fixture rows: count=%d err=%v", residual, err)
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

	quotedRuntimeRole := pgx.Identifier{parsed.User}.Sanitize()
	verifyUnexpectedGrant := func(label, grantSQL, revokeSQL string) {
		t.Helper()
		cleanup := func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			if _, err := adminPool.Exec(cleanupCtx, revokeSQL); err != nil {
				t.Errorf("revoke temporary %s privilege: %v", label, err)
			}
		}
		defer cleanup()
		if _, err := adminPool.Exec(ctx, grantSQL); err != nil {
			t.Fatalf("grant temporary %s privilege in isolated test database: %v", label, err)
		}
		if err := VerifyRuntimeRole(ctx, pool, parsed.User); err == nil {
			t.Errorf("startup verifier accepted unexpected effective %s privilege", label)
		}
		cleanup()
		if err := VerifyRuntimeRole(ctx, pool, parsed.User); err != nil {
			t.Errorf("startup verifier rejected approved state after revoking temporary %s privilege: %v", label, err)
		}
	}
	verifyUnexpectedGrant("UPDATE on activity",
		"GRANT UPDATE ON app.task_activity TO "+quotedRuntimeRole,
		"REVOKE UPDATE ON app.task_activity FROM "+quotedRuntimeRole)
	verifyUnexpectedGrant("column UPDATE on template tracking",
		"GRANT UPDATE (template_id) ON app.task_template_applications, app.task_template_application_items TO "+quotedRuntimeRole,
		"REVOKE UPDATE (template_id) ON app.task_template_applications, app.task_template_application_items FROM "+quotedRuntimeRole)
	verifyUnexpectedGrant("DELETE on tasks",
		"GRANT DELETE ON app.tasks TO "+quotedRuntimeRole,
		"REVOKE DELETE ON app.tasks FROM "+quotedRuntimeRole)
	verifyUnexpectedGrant("TRUNCATE on activity",
		"GRANT TRUNCATE ON app.task_activity TO "+quotedRuntimeRole,
		"REVOKE TRUNCATE ON app.task_activity FROM "+quotedRuntimeRole)
	verifyUnexpectedGrant("MAINTAIN on tasks",
		"GRANT MAINTAIN ON app.tasks TO "+quotedRuntimeRole,
		"REVOKE MAINTAIN ON app.tasks FROM "+quotedRuntimeRole)
	verifyUnexpectedGrant("EXECUTE on application function",
		"GRANT EXECUTE ON FUNCTION app.enforce_task_template_item_count() TO "+quotedRuntimeRole,
		"REVOKE EXECUTE ON FUNCTION app.enforce_task_template_item_count() FROM "+quotedRuntimeRole)
}

func TestVerifyRuntimeRoleRejectsPublicUsersUpdatePostgreSQL(t *testing.T) {
	adminURL, adminTarget := testdatabase.TestURL(t)
	runtimeURL, runtimeTarget := testdatabase.URL(t, "RUNTIME_DATABASE_URL", "")
	if !testdatabase.SameTarget(adminTarget, runtimeTarget) {
		t.Fatal("RUNTIME_DATABASE_URL must identify the same dedicated test database as TEST_DATABASE_URL")
	}
	runtimeConfig, err := pgx.ParseConfig(runtimeURL)
	if err != nil {
		t.Fatal("RUNTIME_DATABASE_URL must be a valid dedicated test-database URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtimePool, err := Open(ctx, runtimeURL, 2)
	if err != nil {
		t.Fatalf("open runtime database pool: %v", err)
	}
	defer runtimePool.Close()
	if err := VerifyRuntimeRole(ctx, runtimePool, runtimeConfig.User); err != nil {
		t.Fatalf("runtime role did not satisfy the baseline privilege policy: %v", err)
	}

	adminPool, err := Open(ctx, adminURL, 1)
	if err != nil {
		t.Fatalf("open isolated test administrator pool: %v", err)
	}
	defer adminPool.Close()
	var adminIsSuperuser bool
	if err := adminPool.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&adminIsSuperuser); err != nil {
		t.Fatalf("verify isolated test administrator: %v", err)
	}
	if !adminIsSuperuser {
		t.Fatal("TEST_DATABASE_URL must use the isolated test administrator for this PUBLIC ACL test")
	}

	var publicAlreadyHasUpdate bool
	if err := adminPool.QueryRow(ctx, `
		SELECT has_table_privilege('public', 'app.users', 'UPDATE')
		    OR has_any_column_privilege('public', 'app.users', 'UPDATE')
	`).Scan(&publicAlreadyHasUpdate); err != nil {
		t.Fatalf("inspect pre-existing PUBLIC users UPDATE privileges: %v", err)
	}
	if publicAlreadyHasUpdate {
		t.Fatal("isolated test database already grants PUBLIC UPDATE on app.users; refusing to alter pre-existing ACL state")
	}

	// Register cleanup before changing the ACL. The dedicated test database is
	// required because this temporary grant affects every database role.
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, `REVOKE UPDATE ON TABLE app.users FROM PUBLIC`); err != nil {
			t.Errorf("revoke temporary PUBLIC users UPDATE privilege during cleanup: %v", err)
		}
	}()

	if _, err := adminPool.Exec(ctx, `GRANT UPDATE ON TABLE app.users TO PUBLIC`); err != nil {
		t.Fatalf("grant temporary PUBLIC users UPDATE privilege in isolated test database: %v", err)
	}
	if err := VerifyRuntimeRole(ctx, runtimePool, runtimeConfig.User); err == nil {
		t.Fatal("runtime role verifier accepted effective app.users UPDATE inherited through PUBLIC")
	}

	if _, err := adminPool.Exec(ctx, `REVOKE UPDATE ON TABLE app.users FROM PUBLIC`); err != nil {
		t.Fatalf("revoke temporary PUBLIC users UPDATE privilege: %v", err)
	}
	if err := VerifyRuntimeRole(ctx, runtimePool, runtimeConfig.User); err != nil {
		t.Fatalf("runtime role verifier rejected the approved privilege state after PUBLIC UPDATE was revoked: %v", err)
	}
}

func expectPermissionDeniedInSavepoint(t *testing.T, ctx context.Context, tx pgx.Tx, statement string) {
	t.Helper()
	const savepoint = "runtime_privilege_check"
	if _, err := tx.Exec(ctx, "SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("create privilege-check savepoint: %v", err)
	}
	_, operationErr := tx.Exec(ctx, statement)
	var pgError *pgconn.PgError
	denied := errors.As(operationErr, &pgError) && pgError.Code == "42501"
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("roll back privilege-check statement: %v", err)
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("release privilege-check savepoint: %v", err)
	}
	if !denied {
		t.Fatalf("statement error = %v, want PostgreSQL permission denied (42501)", operationErr)
	}
}
