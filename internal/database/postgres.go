package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const startupPingTimeout = 5 * time.Second

// Open creates a bounded pgx pool and verifies PostgreSQL is reachable before returning.
func Open(ctx context.Context, connectionString string, maxConnections int32) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL is invalid")
	}
	if maxConnections < 1 {
		return nil, fmt.Errorf("database maximum connections must be positive")
	}
	poolConfig.MaxConns = maxConnections
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}

	pingContext, cancel := context.WithTimeout(ctx, startupPingTimeout)
	defer cancel()
	if err := pool.Ping(pingContext); err != nil {
		pool.Close()
		return nil, fmt.Errorf("verify database connectivity: %w", err)
	}
	return pool, nil
}

// VerifyRuntimeRole refuses production startup when the connected role has
// database/schema ownership, broad administrative rights, or can mutate
// append-only task activity history.
func VerifyRuntimeRole(ctx context.Context, pool *pgxpool.Pool, expectedRole string) error {
	var current string
	var privileges runtimeRolePrivileges
	err := pool.QueryRow(ctx, `
		SELECT current_user,
		       r.rolsuper, r.rolcreatedb, r.rolcreaterole, r.rolinherit,
		       d.datdba = r.oid, has_database_privilege(r.oid, d.oid, 'CREATE'),
		       n.nspowner = r.oid, has_schema_privilege(r.oid, n.oid, 'CREATE'),
		       has_table_privilege(r.oid, 'app.tasks', 'TRUNCATE'),
		       has_table_privilege(r.oid, 'app.task_activity', 'UPDATE'),
		       has_table_privilege(r.oid, 'app.task_activity', 'DELETE'),
		       has_table_privilege(r.oid, 'app.task_activity', 'TRUNCATE'),
	       EXISTS (SELECT 1 FROM pg_class c WHERE c.relnamespace=n.oid AND c.relowner=r.oid),
	       EXISTS (SELECT 1 FROM pg_auth_members m WHERE m.member=r.oid)
		FROM pg_roles r
		JOIN pg_database d ON d.datname = current_database()
		JOIN pg_namespace n ON n.nspname = 'app'
		WHERE r.rolname = current_user
	`).Scan(&current,
		&privileges.superuser, &privileges.createdb, &privileges.createrole, &privileges.inherit,
		&privileges.ownsDatabase, &privileges.canCreateDatabase,
		&privileges.ownsSchema, &privileges.canCreateSchema, &privileges.canTruncateTasks,
		&privileges.canUpdateActivity, &privileges.canDeleteActivity, &privileges.canTruncateActivity,
		&privileges.ownsApplicationTables, &privileges.hasRoleMembership)
	if err != nil {
		return fmt.Errorf("verify production database role privileges: query failed")
	}
	if err := validateRuntimeRolePrivileges(current, expectedRole, privileges); err != nil {
		return err
	}
	return nil
}

type runtimeRolePrivileges struct {
	superuser, createdb, createrole, inherit bool
	ownsDatabase, canCreateDatabase          bool
	ownsSchema, canCreateSchema              bool
	canTruncateTasks                         bool
	canUpdateActivity, canDeleteActivity     bool
	canTruncateActivity                      bool
	ownsApplicationTables, hasRoleMembership bool
}

func validateRuntimeRolePrivileges(current, expected string, p runtimeRolePrivileges) error {
	if current != expected || p.superuser || p.createdb || p.createrole || p.inherit ||
		p.ownsDatabase || p.canCreateDatabase || p.ownsSchema || p.canCreateSchema ||
		p.canTruncateTasks || p.ownsApplicationTables || p.hasRoleMembership ||
		p.canUpdateActivity || p.canDeleteActivity || p.canTruncateActivity {
		return fmt.Errorf("production database role does not satisfy least-privilege requirements")
	}
	return nil
}
