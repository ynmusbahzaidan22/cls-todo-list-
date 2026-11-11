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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/adminbootstrap"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
)

type invocation struct {
	operation, database, email, confirmation string
}

func parseInvocation(args []string) (invocation, error) {
	if len(args) == 0 || (args[0] != "status" && args[0] != "bootstrap") {
		return invocation{}, errors.New("usage: adminctl status|bootstrap --database todo_dev [--email address] [--confirm-database todo_dev]")
	}
	fs := flag.NewFlagSet("adminctl", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	database := fs.String("database", "", "expected database identity")
	email := fs.String("email", "", "exact normalized account email")
	confirm := fs.String("confirm-database", "", "explicit execution confirmation")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return invocation{}, errors.New("invalid command arguments")
	}
	if *database == "" || (*database != "todo_dev" && *database != "todo_test") {
		return invocation{}, errors.New("database must be explicitly todo_dev or todo_test")
	}
	if args[0] == "bootstrap" && (*email == "" || *confirm != *database) {
		return invocation{}, errors.New("bootstrap requires --email and matching --confirm-database")
	}
	if *email != "" {
		if _, err := adminbootstrap.ValidateEmail(*email); err != nil {
			return invocation{}, errors.New("email must be the exact normalized account email")
		}
	}
	return invocation{operation: args[0], database: *database, email: *email, confirmation: *confirm}, nil
}

func targetAndConnection(database string) (adminbootstrap.Target, string, error) {
	for _, key := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGSSLMODE", "PGSERVICE", "PGSERVICEFILE", "PGPASSFILE"} {
		if os.Getenv(key) != "" {
			return adminbootstrap.Target{}, "", errors.New("conflicting PostgreSQL environment override is set")
		}
	}
	testMode := os.Getenv("ADMINCTL_AUTOMATED_TEST") == "1"
	var host, port, db, user, password, sslmode string
	if testMode {
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
	if err := adminbootstrap.ValidateTarget(target, testMode); err != nil {
		return adminbootstrap.Target{}, "", err
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
	target, connection, err := targetAndConnection(call.database)
	if err != nil {
		return err
	}
	config, err := pgx.ParseConfig(connection)
	if err != nil || config.Host != target.Host || fmt.Sprint(config.Port) != target.Port || config.Database != target.Database || config.User != target.User {
		return errors.New("effective database target failed validation")
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
	service := adminbootstrap.New(repository.NewAdminBootstrapRepository(pool))
	if call.operation == "status" {
		status, err := service.Status(ctx, call.email)
		if err != nil {
			return errors.New("read bootstrap status failed")
		}
		fmt.Printf("database=%s active_admins=%d guard_admins=%d\n", target.Database, status.ActiveAdmins, status.GuardAdmins)
		if call.email != "" {
			fmt.Printf("target_exists=%t target_active=%t target_role=%s eligible=%t\n", status.TargetExists, status.TargetActive, status.TargetRole, status.Eligible)
		}
		if status.ActiveAdmins != status.GuardAdmins {
			return errors.New("active administrator guard and account state disagree")
		}
		return nil
	}
	if call.confirmation != call.database {
		return errors.New("bootstrap requires an explicit matching --confirm-database")
	}
	if err := service.Bootstrap(ctx, call.email); err != nil {
		return fmt.Errorf("bootstrap refused: %w", err)
	}
	fmt.Println("initial administrator bootstrap committed; affected account sessions were revoked")
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
