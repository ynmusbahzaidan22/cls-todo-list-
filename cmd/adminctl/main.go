// adminctl provides the guarded, non-HTTP initial administrator bootstrap.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/adminbootstrap"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
)

type invocation struct {
	operation, database, email, confirmation string
	production, authorizeProduction          bool
}

const productionDatabaseHost = "ep-raspy-fog-b9fxieuo.c-7.eu-central-1.aws.neon.tech"

func parseInvocation(args []string) (invocation, error) {
	if len(args) == 0 || (args[0] != "status" && args[0] != "bootstrap") {
		return invocation{}, errors.New("usage: adminctl status|bootstrap --database todo_dev|todo_test|neondb [--production] [--email address] [--confirm-database database] [--authorize-production-bootstrap]")
	}
	fs := flag.NewFlagSet("adminctl", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	database := fs.String("database", "", "expected database identity")
	email := fs.String("email", "", "exact normalized account email")
	confirm := fs.String("confirm-database", "", "explicit execution confirmation")
	production := fs.Bool("production", false, "select the explicitly guarded Neon production target")
	authorizeProduction := fs.Bool("authorize-production-bootstrap", false, "authorize the one-time production bootstrap write")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return invocation{}, errors.New("invalid command arguments")
	}
	if *production {
		if *database != "neondb" {
			return invocation{}, errors.New("production mode requires --database neondb")
		}
		if args[0] == "bootstrap" && (!*authorizeProduction || *email == "" || *confirm != "neondb") {
			return invocation{}, errors.New("production bootstrap requires --email, --confirm-database neondb, and --authorize-production-bootstrap")
		}
		if args[0] == "status" && (*authorizeProduction || *confirm != "") {
			return invocation{}, errors.New("production status does not accept write authorization")
		}
	} else {
		if *database == "" || (*database != "todo_dev" && *database != "todo_test") {
			return invocation{}, errors.New("database must be explicitly todo_dev or todo_test")
		}
		if *authorizeProduction {
			return invocation{}, errors.New("production authorization requires --production")
		}
		if args[0] == "bootstrap" && (*email == "" || *confirm != *database) {
			return invocation{}, errors.New("bootstrap requires --email and matching --confirm-database")
		}
	}
	if *email != "" {
		if _, err := adminbootstrap.ValidateEmail(*email); err != nil {
			return invocation{}, errors.New("email must be the exact normalized account email")
		}
	}
	return invocation{operation: args[0], database: *database, email: *email, confirmation: *confirm, production: *production, authorizeProduction: *authorizeProduction}, nil
}

func targetAndConnection(database string, production bool) (adminbootstrap.Target, string, error) {
	for _, key := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGSSLMODE", "PGSERVICE", "PGSERVICEFILE", "PGPASSFILE"} {
		if os.Getenv(key) != "" {
			return adminbootstrap.Target{}, "", errors.New("conflicting PostgreSQL environment override is set")
		}
	}
	testMode := os.Getenv("ADMINCTL_AUTOMATED_TEST") == "1"
	if production && testMode {
		return adminbootstrap.Target{}, "", errors.New("production mode cannot use automated test mode")
	}
	var host, port, db, user, password, sslmode string
	if production {
		for _, entry := range os.Environ() {
			name, value, found := strings.Cut(entry, "=")
			if found && strings.HasPrefix(strings.ToUpper(name), "PG") && value != "" {
				return adminbootstrap.Target{}, "", errors.New("PostgreSQL environment overrides must be absent in production mode")
			}
		}
		for _, key := range []string{"DATABASE_URL", "POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD", "TEST_DATABASE_URL", "RUNTIME_DATABASE_URL", "TEST_POSTGRES_DB", "TEST_POSTGRES_USER", "TEST_POSTGRES_PASSWORD", "ADMINCTL_AUTOMATED_TEST"} {
			if os.Getenv(key) != "" {
				return adminbootstrap.Target{}, "", errors.New("development, test, or automated-test settings must be absent in production mode")
			}
		}
		raw := os.Getenv("PRODUCTION_DATABASE_URL")
		target, err := adminbootstrap.URLTarget(raw)
		if err != nil {
			return adminbootstrap.Target{}, "", errors.New("PRODUCTION_DATABASE_URL is missing or malformed")
		}
		u, err := url.Parse(raw)
		if err != nil || u.User == nil {
			return adminbootstrap.Target{}, "", errors.New("production owner URL is malformed")
		}
		parsedPassword, hasPassword := u.User.Password()
		password = parsedPassword
		sslmode = querySSLMode(raw)
		if target.Host != productionDatabaseHost || target.Port != "5432" || target.Database != "neondb" || target.User != "neondb_owner" || !hasPassword || parsedPassword == "" || sslmode != "verify-full" || len(u.Query()) != 1 || len(u.Query()["sslmode"]) != 1 {
			return adminbootstrap.Target{}, "", errors.New("production owner URL does not match the pinned TLS-verified Neon target")
		}
		host, port, db, user = target.Host, target.Port, target.Database, target.User
	} else if testMode {
		if os.Getenv("REQUIRE_POSTGRES_INTEGRATION") != "1" || database != "todo_test" {
			return adminbootstrap.Target{}, "", errors.New("automated mode requires explicit isolated todo_test authorization")
		}
		if os.Getenv("DATABASE_URL") != "" || os.Getenv("POSTGRES_DB") != "" || os.Getenv("POSTGRES_USER") != "" || os.Getenv("POSTGRES_PASSWORD") != "" {
			return adminbootstrap.Target{}, "", errors.New("development database settings must be absent in automated mode")
		}
		host, port = "127.0.0.1", "25432"
		db, user, password = os.Getenv("TEST_POSTGRES_DB"), os.Getenv("TEST_POSTGRES_USER"), os.Getenv("TEST_POSTGRES_PASSWORD")
		testURL, err := adminbootstrap.URLTarget(os.Getenv("TEST_DATABASE_URL"))
		if err != nil || testURL.Host != host || testURL.Port != port || testURL.Database != db || testURL.User != user {
			return adminbootstrap.Target{}, "", errors.New("test administrator URL does not match isolated environment")
		}
		testURLParsed, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
		testURLPassword, hasTestURLPassword := "", false
		if err == nil && testURLParsed.User != nil {
			testURLPassword, hasTestURLPassword = testURLParsed.User.Password()
		}
		if err != nil || !hasTestURLPassword || testURLPassword != password {
			return adminbootstrap.Target{}, "", errors.New("test administrator credentials do not match the dedicated environment")
		}
		runtimeURL, err := adminbootstrap.URLTarget(os.Getenv("RUNTIME_DATABASE_URL"))
		if err != nil || runtimeURL.Host != host || runtimeURL.Port != port || runtimeURL.Database != db || runtimeURL.User != "todo_test_runtime" {
			return adminbootstrap.Target{}, "", errors.New("runtime URL does not match isolated test target")
		}
		sslmode = querySSLMode(os.Getenv("TEST_DATABASE_URL"))
	} else {
		if database != "todo_dev" {
			return adminbootstrap.Target{}, "", errors.New("todo_test is allowed only in dedicated automated test mode")
		}
		if os.Getenv("TEST_DATABASE_URL") != "" || os.Getenv("RUNTIME_DATABASE_URL") != "" || os.Getenv("TEST_POSTGRES_DB") != "" || os.Getenv("TEST_POSTGRES_USER") != "" || os.Getenv("TEST_POSTGRES_PASSWORD") != "" {
			return adminbootstrap.Target{}, "", errors.New("test database settings must be absent in operational mode")
		}
		host, port = "127.0.0.1", "15432"
		db, user, password = os.Getenv("POSTGRES_DB"), os.Getenv("POSTGRES_USER"), os.Getenv("POSTGRES_PASSWORD")
		devURL, err := adminbootstrap.URLTarget(os.Getenv("DATABASE_URL"))
		if err != nil || devURL.Host != host || devURL.Port != port || devURL.Database != db {
			return adminbootstrap.Target{}, "", errors.New("development runtime URL does not confirm the expected local target")
		}
		sslmode = querySSLMode(os.Getenv("DATABASE_URL"))
	}
	if db != database || user == "" || password == "" {
		return adminbootstrap.Target{}, "", errors.New("required administrator connection settings are missing or mismatched")
	}
	target := adminbootstrap.Target{Host: host, Port: port, Database: db, User: user}
	if production {
		if target.Host != productionDatabaseHost || target.Port != "5432" || target.Database != "neondb" || target.User != "neondb_owner" {
			return adminbootstrap.Target{}, "", errors.New("production target did not match the pinned Neon owner identity")
		}
	} else if err := adminbootstrap.ValidateTarget(target, testMode); err != nil {
		return adminbootstrap.Target{}, "", err
	}
	if production {
		return target, os.Getenv("PRODUCTION_DATABASE_URL"), nil
	}
	query := url.Values{"sslmode": []string{sslmode}}
	connection := (&url.URL{Scheme: "postgres", User: url.UserPassword(user, password), Host: net.JoinHostPort(host, port), Path: "/" + db, RawQuery: query.Encode()}).String()
	return target, connection, nil
}

func querySSLMode(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	mode := u.Query().Get("sslmode")
	if mode == "" {
		return "prefer"
	}
	return mode
}

func run(ctx context.Context, args []string) error {
	call, err := parseInvocation(args)
	if err != nil {
		return err
	}
	if call.operation == "bootstrap" && call.confirmation != call.database {
		return errors.New("explicit database confirmation did not match")
	}
	target, connection, err := targetAndConnection(call.database, call.production)
	if err != nil {
		return err
	}
	config, err := pgx.ParseConfig(connection)
	if err != nil || config.Host != target.Host || fmt.Sprint(config.Port) != target.Port || config.Database != target.Database || config.User != target.User {
		return errors.New("effective database target failed validation")
	}
	if call.production && (config.TLSConfig == nil || config.TLSConfig.InsecureSkipVerify || config.TLSConfig.ServerName != productionDatabaseHost) {
		return errors.New("production owner connection does not enforce hostname-verified TLS")
	}
	poolConfig, err := pgxpool.ParseConfig(connection)
	if err != nil {
		return errors.New("cannot configure guarded database connection")
	}
	poolConfig.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("cannot connect to guarded database target")
	}
	defer pool.Close()
	var actualDB, actualUser, databaseOwner string
	if err := pool.QueryRow(ctx, `SELECT current_database(), current_user, (SELECT datdba::regrole::text FROM pg_database WHERE datname=current_database())`).Scan(&actualDB, &actualUser, &databaseOwner); err != nil || actualDB != target.Database || actualUser != target.User || databaseOwner != target.User {
		return errors.New("connected database identity did not match the explicit target")
	}
	if call.production {
		var serverVersion string
		var migrationVersion int64
		if err := pool.QueryRow(ctx, `SELECT current_setting('server_version'), (SELECT version FROM public.schema_version LIMIT 1)`).Scan(&serverVersion, &migrationVersion); err != nil || !strings.HasPrefix(serverVersion, "18.6") || migrationVersion != 15 {
			return errors.New("production bootstrap requires PostgreSQL 18.6 and Tern version 15")
		}
	}
	service := adminbootstrap.New(repository.NewAdminBootstrapRepository(pool))
	if call.operation == "status" {
		status, err := service.Status(ctx, call.email)
		if err != nil {
			return errors.New("read bootstrap status failed")
		}
		fmt.Printf("database=%s active_admins=%d guard_admins=%d\n", target.Database, status.ActiveAdmins, status.GuardAdmins)
		if call.email != "" {
			fmt.Printf("target_exists=%t target_active=%t target_role=%s eligible=%t active_sessions=%d\n", status.TargetExists, status.TargetActive, status.TargetRole, status.Eligible, status.TargetActiveSessions)
		}
		if status.ActiveAdmins != status.GuardAdmins {
			return errors.New("active administrator guard and account state disagree")
		}
		return nil
	}
	if call.confirmation != call.database {
		return errors.New("bootstrap requires an explicit matching --confirm-database")
	}
	if call.production {
		status, err := service.Status(ctx, call.email)
		if err != nil {
			return errors.New("read production bootstrap eligibility failed")
		}
		if status.ActiveAdmins != 0 || status.GuardAdmins != 0 {
			return errors.New("production bootstrap requires zero active administrators and a zero guard")
		}
		if !status.TargetExists || !status.TargetActive || status.TargetRole != "USER" || !status.Eligible {
			return errors.New("production bootstrap target is missing, inactive, or ineligible")
		}
		var otherUsersBefore string
		if err := pool.QueryRow(ctx, `SELECT md5(COALESCE(string_agg(id::text||':'||role||':'||status||':'||COALESCE(updated_at::text,''), ',' ORDER BY id),'')) FROM app.users WHERE email<>$1`, call.email).Scan(&otherUsersBefore); err != nil {
			return errors.New("capture production non-target account state failed")
		}
		if err := service.BootstrapPreservingSessions(ctx, call.email); err != nil {
			return fmt.Errorf("production bootstrap refused: %w", err)
		}
		after, err := service.Status(ctx, call.email)
		if err != nil || after.ActiveAdmins != 1 || after.GuardAdmins != 1 || !after.TargetExists || !after.TargetActive || after.TargetRole != "ADMIN" || after.TargetActiveSessions != status.TargetActiveSessions {
			return errors.New("production bootstrap postcondition verification failed")
		}
		var otherUsersAfter string
		if err := pool.QueryRow(ctx, `SELECT md5(COALESCE(string_agg(id::text||':'||role||':'||status||':'||COALESCE(updated_at::text,''), ',' ORDER BY id),'')) FROM app.users WHERE email<>$1`, call.email).Scan(&otherUsersAfter); err != nil || otherUsersAfter != otherUsersBefore {
			return errors.New("production bootstrap changed a non-target account")
		}
		var auditCount int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.admin_audit_events e JOIN app.users u ON u.id=e.target_user_id WHERE u.email=$1 AND e.actor_user_id IS NULL AND e.action='INITIAL_ADMIN_BOOTSTRAPPED' AND e.before_state->>'role'='USER' AND e.after_state->>'role'='ADMIN' AND e.after_state->>'preserved_sessions'=$2`, call.email, fmt.Sprint(status.TargetActiveSessions)).Scan(&auditCount); err != nil || auditCount != 1 {
			return errors.New("production bootstrap audit verification failed")
		}
	} else if err := service.Bootstrap(ctx, call.email); err != nil {
		return fmt.Errorf("bootstrap refused: %w", err)
	}
	if call.production {
		fmt.Println("production initial administrator bootstrap committed; existing sessions were preserved")
	} else {
		fmt.Println("initial administrator bootstrap committed; affected account sessions were revoked")
	}
	return nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
