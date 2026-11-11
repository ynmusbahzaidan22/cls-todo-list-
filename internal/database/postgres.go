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

type tablePrivilegePolicy struct {
	selectRows, insertRows, updateRows, deleteRows bool
}

// Every application table is listed explicitly. Adding a table requires an
// intentional privilege-policy decision before production startup can accept it.
var runtimeTablePolicies = map[string]tablePrivilegePolicy{
	"users":                           {selectRows: true, insertRows: true},
	"sessions":                        {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"tasks":                           {selectRows: true, insertRows: true, updateRows: true},
	"task_activity":                   {selectRows: true, insertRows: true},
	"task_templates":                  {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"task_template_items":             {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"auth_rate_limits":                {selectRows: true, insertRows: true, updateRows: true, deleteRows: true},
	"admin_audit_events":              {insertRows: true},
	"active_admin_guard":              {},
	"task_template_applications":      {selectRows: true, insertRows: true},
	"task_template_application_items": {selectRows: true, insertRows: true},
}

type runtimeRoleBase struct {
	canLogin, superuser, createdb, createrole bool
	inherit, replication, bypassRLS           bool
	ownsDatabase, canCreateDatabase           bool
	canUseTemporaryTable                      bool
	canConnectDatabase                        bool
	ownsSchema, canCreateSchema, canUseSchema bool
	ownsPublicSchema, canCreatePublicSchema   bool
	ownsOtherSchema, canCreateOtherSchema     bool
	hasRoleMembership                         bool
	ownsApplicationRelations                  bool
	ownsApplicationFunctions                  bool
	ownsApplicationTypes                      bool
	canGrantPrivileges, hasPublicAppGrants    bool
	hasUnexpectedDefaultPrivileges            bool
}

type effectiveTablePrivileges struct {
	selectRows, insertRows, updateRows, deleteRows bool
	truncateRows, referencesRows, triggerRows      bool
	maintainRows                                   bool
}

type effectiveColumnPrivileges struct {
	name                               string
	selectRows, insertRows, updateRows bool
	referencesRows                     bool
}

func validateRuntimeRoleBase(current, expected string, base runtimeRoleBase) error {
	if current != expected || !base.canLogin || base.superuser || base.createdb || base.createrole ||
		base.inherit || base.replication || base.bypassRLS || base.ownsDatabase || base.canCreateDatabase ||
		base.canUseTemporaryTable || !base.canConnectDatabase || base.ownsSchema || base.canCreateSchema || !base.canUseSchema ||
		base.ownsPublicSchema || base.canCreatePublicSchema || base.ownsOtherSchema || base.canCreateOtherSchema ||
		base.hasRoleMembership || base.ownsApplicationRelations || base.ownsApplicationFunctions || base.ownsApplicationTypes ||
		base.canGrantPrivileges || base.hasPublicAppGrants || base.hasUnexpectedDefaultPrivileges {
		return fmt.Errorf("production database role does not satisfy the least-privilege role, ownership, schema, database, PUBLIC, or grant-option policy")
	}
	return nil
}

func validateRuntimeTablePrivileges(name string, actual effectiveTablePrivileges) error {
	policy, known := runtimeTablePolicies[name]
	if !known {
		return fmt.Errorf("runtime privilege policy is missing app.%s; refusing startup", name)
	}
	want := effectiveTablePrivileges{
		selectRows: policy.selectRows, insertRows: policy.insertRows,
		updateRows: policy.updateRows, deleteRows: policy.deleteRows,
	}
	if actual != want {
		return fmt.Errorf("runtime role has an unexpected effective table privilege on app.%s", name)
	}
	return nil
}

func validateRuntimeColumnPrivileges(table string, policy tablePrivilegePolicy, actual effectiveColumnPrivileges) error {
	wantUpdate := policy.updateRows || (table == "users" && (actual.name == "time_zone" || actual.name == "role" || actual.name == "status" || actual.name == "updated_at"))
	if actual.selectRows != policy.selectRows || actual.insertRows != policy.insertRows ||
		actual.updateRows != wantUpdate || actual.referencesRows {
		return fmt.Errorf("runtime role has an unexpected effective column privilege on app.%s.%s", table, actual.name)
	}
	return nil
}

// VerifyRuntimeRole compares effective PostgreSQL privileges with the complete
// application allowlist. has_*_privilege checks include grants to PUBLIC and
// inherited grants; memberships are separately prohibited as defense in depth.
func VerifyRuntimeRole(ctx context.Context, pool *pgxpool.Pool, expectedRole string) error {
	var current string
	var base runtimeRoleBase
	err := pool.QueryRow(ctx, `
		SELECT current_user,
		       r.rolcanlogin, r.rolsuper, r.rolcreatedb, r.rolcreaterole,
		       r.rolinherit, r.rolreplication, r.rolbypassrls,
		       d.datdba = r.oid,
		       has_database_privilege(r.oid, d.oid, 'CREATE'),
		       has_database_privilege(r.oid, d.oid, 'TEMPORARY'),
		       has_database_privilege(r.oid, d.oid, 'CONNECT'),
		       n.nspowner = r.oid,
		       has_schema_privilege(r.oid, n.oid, 'CREATE'),
		       has_schema_privilege(r.oid, n.oid, 'USAGE'),
		       EXISTS (SELECT 1 FROM pg_namespace s WHERE s.nspname='public' AND s.nspowner=r.oid),
	       COALESCE(has_schema_privilege(r.oid, 'public', 'CREATE'), false),
	       EXISTS (SELECT 1 FROM pg_namespace s WHERE s.nspname NOT IN ('pg_catalog','information_schema','public','app') AND left(s.nspname,3) <> 'pg_' AND s.nspowner=r.oid),
	       EXISTS (SELECT 1 FROM pg_namespace s WHERE s.nspname NOT IN ('pg_catalog','information_schema','public','app') AND left(s.nspname,3) <> 'pg_' AND has_schema_privilege(r.oid, s.oid, 'CREATE')),
		       EXISTS (SELECT 1 FROM pg_auth_members m WHERE m.member = r.oid),
		       EXISTS (SELECT 1 FROM pg_class c WHERE c.relnamespace=n.oid AND c.relowner=r.oid),
		       EXISTS (SELECT 1 FROM pg_proc p WHERE p.pronamespace=n.oid AND p.proowner=r.oid),
		       EXISTS (SELECT 1 FROM pg_type t WHERE t.typnamespace=n.oid AND t.typowner=r.oid),
		       EXISTS (
		           SELECT 1 FROM pg_class c
		           CROSS JOIN LATERAL aclexplode(COALESCE(c.relacl, acldefault(CASE WHEN c.relkind = 'S' THEN 'S'::"char" ELSE 'r'::"char" END, c.relowner))) a
		           WHERE c.relnamespace=n.oid AND a.grantee IN (0, r.oid) AND a.is_grantable
		           UNION ALL
		           SELECT 1 FROM pg_proc p
		           CROSS JOIN LATERAL aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) a
		           WHERE p.pronamespace=n.oid AND a.grantee IN (0, r.oid) AND a.is_grantable
		           UNION ALL
		           SELECT 1 FROM pg_attribute att
		           JOIN pg_class c ON c.oid=att.attrelid
		           CROSS JOIN LATERAL aclexplode(att.attacl) a
		           WHERE c.relnamespace=n.oid AND att.attnum > 0 AND NOT att.attisdropped
		             AND att.attacl IS NOT NULL AND a.grantee IN (0, r.oid) AND a.is_grantable
		           UNION ALL
		           SELECT 1 FROM pg_namespace s
		           CROSS JOIN LATERAL aclexplode(COALESCE(s.nspacl, acldefault('n', s.nspowner))) a
		           WHERE s.oid=n.oid AND a.grantee IN (0, r.oid) AND a.is_grantable
		           UNION ALL
		           SELECT 1 FROM pg_database db
		           CROSS JOIN LATERAL aclexplode(COALESCE(db.datacl, acldefault('d', db.datdba))) a
		           WHERE db.oid=d.oid AND a.grantee IN (0, r.oid) AND a.is_grantable
		           UNION ALL
		           SELECT 1 FROM pg_default_acl da
		           CROSS JOIN LATERAL aclexplode(da.defaclacl) a
		           WHERE (da.defaclnamespace = 0 OR da.defaclnamespace=n.oid)
		             AND a.grantee IN (0, r.oid) AND a.is_grantable
		       ),
		       EXISTS (
		           SELECT 1 FROM pg_default_acl da
		           CROSS JOIN LATERAL aclexplode(da.defaclacl) a
		           WHERE (da.defaclnamespace = 0 OR da.defaclnamespace=n.oid)
		             AND a.grantee IN (0, r.oid)
		       ),
		       EXISTS (
		           SELECT 1 FROM pg_class c
		           CROSS JOIN LATERAL aclexplode(c.relacl) a
		           WHERE c.relnamespace=n.oid AND a.grantee=0
		           UNION ALL
		           SELECT 1 FROM pg_attribute att
		           JOIN pg_class c ON c.oid=att.attrelid
		           CROSS JOIN LATERAL aclexplode(att.attacl) a
		           WHERE c.relnamespace=n.oid AND att.attnum > 0 AND NOT att.attisdropped
		             AND a.grantee=0
		           UNION ALL
		           SELECT 1 FROM pg_proc p
		           CROSS JOIN LATERAL aclexplode(COALESCE(p.proacl, acldefault('f', p.proowner))) a
		           WHERE p.pronamespace=n.oid AND a.grantee=0
		           UNION ALL
		           SELECT 1 FROM pg_namespace s
		           CROSS JOIN LATERAL aclexplode(COALESCE(s.nspacl, acldefault('n', s.nspowner))) a
		           WHERE s.oid=n.oid AND a.grantee=0
		           UNION ALL
		           SELECT 1 FROM pg_default_acl da
		           CROSS JOIN LATERAL aclexplode(da.defaclacl) a
		           WHERE da.defaclnamespace=n.oid AND a.grantee=0
		       )
		FROM pg_roles r
		JOIN pg_database d ON d.datname=current_database()
		JOIN pg_namespace n ON n.nspname='app'
		WHERE r.rolname=current_user
	`).Scan(&current,
		&base.canLogin, &base.superuser, &base.createdb, &base.createrole,
		&base.inherit, &base.replication, &base.bypassRLS,
		&base.ownsDatabase, &base.canCreateDatabase, &base.canUseTemporaryTable, &base.canConnectDatabase,
		&base.ownsSchema, &base.canCreateSchema, &base.canUseSchema,
		&base.ownsPublicSchema, &base.canCreatePublicSchema, &base.ownsOtherSchema, &base.canCreateOtherSchema,
		&base.hasRoleMembership, &base.ownsApplicationRelations, &base.ownsApplicationFunctions, &base.ownsApplicationTypes,
		&base.canGrantPrivileges, &base.hasUnexpectedDefaultPrivileges, &base.hasPublicAppGrants)
	if err != nil {
		return fmt.Errorf("verify production database role metadata/ownership/ACL query: %w", err)
	}
	if err := validateRuntimeRoleBase(current, expectedRole, base); err != nil {
		return err
	}

	if err := verifyApplicationTablePrivileges(ctx, pool); err != nil {
		return err
	}
	if err := verifyApplicationSequencePrivileges(ctx, pool); err != nil {
		return err
	}
	if err := verifyApplicationFunctionPrivileges(ctx, pool); err != nil {
		return err
	}
	return nil
}

func verifyApplicationTablePrivileges(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `
		SELECT c.relname,
		       has_table_privilege(current_user, c.oid, 'SELECT'),
		       has_table_privilege(current_user, c.oid, 'INSERT'),
		       has_table_privilege(current_user, c.oid, 'UPDATE'),
		       has_table_privilege(current_user, c.oid, 'DELETE'),
		       has_table_privilege(current_user, c.oid, 'TRUNCATE'),
		       has_table_privilege(current_user, c.oid, 'REFERENCES'),
		       has_table_privilege(current_user, c.oid, 'TRIGGER'),
		       has_table_privilege(current_user, c.oid, 'MAINTAIN')
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='app' AND c.relkind IN ('r','p','v','m','f')
		ORDER BY c.relname
	`)
	if err != nil {
		return fmt.Errorf("verify production database role table-privilege query: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]bool, len(runtimeTablePolicies))
	for rows.Next() {
		var name string
		var actual effectiveTablePrivileges
		if err := rows.Scan(&name, &actual.selectRows, &actual.insertRows, &actual.updateRows, &actual.deleteRows,
			&actual.truncateRows, &actual.referencesRows, &actual.triggerRows, &actual.maintainRows); err != nil {
			return fmt.Errorf("verify production database role table-privilege scan: %w", err)
		}
		seen[name] = true
		if err := validateRuntimeTablePrivileges(name, actual); err != nil {
			return err
		}
		policy := runtimeTablePolicies[name]
		if err := verifyApplicationColumnPrivileges(ctx, pool, name, policy); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("verify production database role table-privilege iteration: %w", err)
	}
	for name := range runtimeTablePolicies {
		if !seen[name] {
			return fmt.Errorf("runtime privilege policy expects missing app.%s", name)
		}
	}
	return nil
}

func verifyApplicationColumnPrivileges(ctx context.Context, pool *pgxpool.Pool, table string, policy tablePrivilegePolicy) error {
	rows, err := pool.Query(ctx, `
		SELECT att.attname,
		       has_column_privilege(current_user, c.oid, att.attname, 'SELECT'),
		       has_column_privilege(current_user, c.oid, att.attname, 'INSERT'),
		       has_column_privilege(current_user, c.oid, att.attname, 'UPDATE'),
		       has_column_privilege(current_user, c.oid, att.attname, 'REFERENCES')
		FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_attribute att ON att.attrelid=c.oid
		WHERE n.nspname='app' AND c.relname=$1
		  AND att.attnum > 0 AND NOT att.attisdropped
		ORDER BY att.attnum
	`, table)
	if err != nil {
		return fmt.Errorf("verify production database role column-privilege query for app.%s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var actual effectiveColumnPrivileges
		if err := rows.Scan(&actual.name, &actual.selectRows, &actual.insertRows, &actual.updateRows, &actual.referencesRows); err != nil {
			return fmt.Errorf("verify production database role column-privilege scan for app.%s: %w", table, err)
		}
		if err := validateRuntimeColumnPrivileges(table, policy, actual); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("verify production database role column-privilege iteration for app.%s: %w", table, err)
	}
	return nil
}

func verifyApplicationSequencePrivileges(ctx context.Context, pool *pgxpool.Pool) error {
	var hasSequencePrivilege bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		    WHERE n.nspname='app' AND c.relkind='S'
		      AND (has_sequence_privilege(current_user,c.oid,'USAGE')
		        OR has_sequence_privilege(current_user,c.oid,'SELECT')
		        OR has_sequence_privilege(current_user,c.oid,'UPDATE'))
		)
	`).Scan(&hasSequencePrivilege)
	if err != nil {
		return fmt.Errorf("verify production database role sequence-privilege query: %w", err)
	}
	if hasSequencePrivilege {
		return fmt.Errorf("runtime role has an unexpected effective privilege on an app sequence")
	}
	return nil
}

func verifyApplicationFunctionPrivileges(ctx context.Context, pool *pgxpool.Pool) error {
	var hasExecutableFunction bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		    WHERE n.nspname='app' AND has_function_privilege(current_user,p.oid,'EXECUTE')
	        )
	`).Scan(&hasExecutableFunction)
	if err != nil {
		return fmt.Errorf("verify production database role function-privilege query: %w", err)
	}
	if hasExecutableFunction {
		return fmt.Errorf("runtime role has an unexpected effective EXECUTE privilege on an app function")
	}
	return nil
}
