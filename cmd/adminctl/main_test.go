package main

import "testing"

func TestParseInvocationRequiresExplicitTargetAndConfirmation(t *testing.T) {
	cases := [][]string{
		{"bootstrap", "--email", "owner@example.test"},
		{"bootstrap", "--database", "other", "--email", "owner@example.test", "--confirm-database", "other"},
		{"bootstrap", "--database", "todo_dev", "--email", "Owner@example.test", "--confirm-database", "todo_dev"},
		{"status"},
		{"bootstrap", "--database", "neondb", "--production", "--email", "owner@example.test", "--confirm-database", "neondb"},
		{"bootstrap", "--database", "todo_dev", "--authorize-production-bootstrap"},
	}
	for _, args := range cases {
		if _, err := parseInvocation(args); err == nil {
			t.Errorf("parseInvocation(%q) unexpectedly succeeded", args)
		}
	}
	if _, err := parseInvocation([]string{"bootstrap", "--database", "todo_dev", "--email", "owner@example.test", "--confirm-database", "todo_dev"}); err != nil {
		t.Fatalf("valid guarded invocation rejected: %v", err)
	}
	if _, err := parseInvocation([]string{"bootstrap", "--database", "neondb", "--production", "--email", "owner@example.test", "--confirm-database", "neondb", "--authorize-production-bootstrap"}); err != nil {
		t.Fatalf("explicit production bootstrap invocation rejected: %v", err)
	}
	if _, err := parseInvocation([]string{"status", "--database", "neondb", "--production", "--email", "owner@example.test"}); err != nil {
		t.Fatalf("read-only production status invocation rejected: %v", err)
	}
}

func TestProductionTargetRequiresPinnedDirectOwnerTLSURL(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD", "TEST_DATABASE_URL", "RUNTIME_DATABASE_URL", "TEST_POSTGRES_DB", "TEST_POSTGRES_USER", "TEST_POSTGRES_PASSWORD", "ADMINCTL_AUTOMATED_TEST", "PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGSSLMODE", "PGSERVICE", "PGSERVICEFILE", "PGPASSFILE"} {
		t.Setenv(name, "")
	}
	valid := "postgres://neondb_owner:fixture-secret@" + productionDatabaseHost + ":5432/neondb?sslmode=verify-full"
	t.Setenv("PRODUCTION_DATABASE_URL", valid)
	target, connection, err := targetAndConnection("neondb", true)
	if err != nil || target.Host != productionDatabaseHost || target.Port != "5432" || target.Database != "neondb" || target.User != "neondb_owner" || connection != valid {
		t.Fatalf("pinned production URL rejected: target=%+v err=%v", target, err)
	}
	for _, raw := range []string{
		"postgres://neondb_owner:fixture-secret@other.c-7.eu-central-1.aws.neon.tech:5432/neondb?sslmode=verify-full",
		"postgres://neondb_owner:fixture-secret@" + productionDatabaseHost + ":5432/todo_dev?sslmode=verify-full",
		"postgres://neondb_owner:fixture-secret@" + productionDatabaseHost + ":5432/neondb?sslmode=require",
		"postgres://neondb_owner:fixture-secret@" + productionDatabaseHost + ":5432/neondb?sslmode=verify-full&host=other",
	} {
		t.Setenv("PRODUCTION_DATABASE_URL", raw)
		if _, _, err := targetAndConnection("neondb", true); err == nil {
			t.Error("production target accepted an unexpected endpoint or URL option")
		}
	}
	t.Setenv("PRODUCTION_DATABASE_URL", valid)
	t.Setenv("DATABASE_URL", "postgres://dev:fixture@127.0.0.1:15432/todo_dev")
	if _, _, err := targetAndConnection("neondb", true); err == nil {
		t.Error("production mode accepted an inherited development URL")
	}
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PGSSLROOTCERT", "unexpected-root")
	if _, _, err := targetAndConnection("neondb", true); err == nil {
		t.Error("production mode accepted an inherited PostgreSQL TLS override")
	}
}
