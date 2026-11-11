package testdatabase

import (
	"strings"
	"testing"
)

func TestResolveAcceptsDedicatedLoopbackDatabase(t *testing.T) {
	for _, raw := range []string{
		"postgres://tester:secret@127.0.0.1:25432/todo_test",
		"postgresql://tester:secret@localhost:25432/todo_test?sslmode=disable",
		"postgres://tester:secret@[::1]:25432/todo_test",
	} {
		t.Run(raw, func(t *testing.T) {
			value, target, skip, err := Resolve(raw, "", true)
			if err != nil || skip || value != raw {
				t.Fatalf("Resolve() = (%q, %#v, %t, %v), want accepted explicit URL", value, target, skip, err)
			}
			if target.Database == "" || target.Port == 0 {
				t.Fatalf("incomplete normalized target: %#v", target)
			}
		})
	}
}

func TestResolveRejectsUnsafeURLForms(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "missing scheme", url: "//127.0.0.1/todo_test"},
		{name: "missing credentials", url: "postgres://127.0.0.1/todo_test"},
		{name: "noncanonical loopback address", url: "postgres://tester:secret@127.0.0.2/todo_test"},
		{name: "leading-zero port", url: "postgres://tester:secret@127.0.0.1:025432/todo_test"},
		{name: "development port", url: "postgres://tester:secret@127.0.0.1:15432/todo_test"},
		{name: "missing host", url: "postgres:///todo_test"},
		{name: "missing database", url: "postgres://tester:secret@127.0.0.1"},
		{name: "non-loopback host", url: "postgres://tester:secret@db.example.test/todo_test"},
		{name: "comma-separated hosts", url: "postgres://tester:secret@127.0.0.1,localhost/todo_test"},
		{name: "encoded host", url: "postgres://tester:secret@%31%32%37.0.0.1/todo_test"},
		{name: "trailing dot host", url: "postgres://tester:secret@localhost./todo_test"},
		{name: "development database name", url: "postgres://tester:secret@127.0.0.1:25432/todo_dev"},
		{name: "other test-like database name", url: "postgres://tester:secret@127.0.0.1:25432/todo_test_suffix"},
		{name: "uppercase test database name", url: "postgres://tester:secret@127.0.0.1:25432/TODO_TEST"},
		{name: "encoded database path", url: "postgres://tester:secret@127.0.0.1:25432/todo%5ftest"},
		{name: "multiple path segments", url: "postgres://tester:secret@127.0.0.1:25432/todo_test/other"},
		{name: "fragment", url: "postgres://tester:secret@127.0.0.1:25432/todo_test#fragment"},
		{name: "backslash", url: "postgres://tester:secret@127.0.0.1:25432/todo_test\\@other"},
		{name: "target override host", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?host=other"},
		{name: "target override port", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?port=5433"},
		{name: "target override database", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?dbname=todo_dev"},
		{name: "hostaddr", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?hostaddr=192.0.2.1"},
		{name: "service", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?service=other"},
		{name: "servicefile", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?servicefile=/tmp/pg_service.conf"},
		{name: "options", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?options=-csearch_path%3Dother"},
		{name: "encoded parameter name", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?%68ost=other"},
		{name: "duplicate sslmode", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?sslmode=disable&sslmode=require"},
		{name: "duplicate query separator", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?sslmode=disable?sslmode=require"},
		{name: "invalid sslmode", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?sslmode=verify-ca&x=1"},
		{name: "empty sslmode", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?sslmode="},
		{name: "surrounding whitespace", url: " postgres://tester:secret@127.0.0.1:25432/todo_test"},
		{name: "empty query marker", url: "postgres://tester:secret@127.0.0.1:25432/todo_test?"},
		{name: "raw whitespace in userinfo", url: "postgres://tester:pass word@127.0.0.1/todo_test"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, skip, err := Resolve(tc.url, "", true)
			if err == nil || skip {
				t.Fatalf("Resolve(%q) = err %v, skip %t; want validation error", tc.url, err, skip)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("validation error exposed URL credentials: %v", err)
			}
		})
	}
}

func TestResolveRejectsDevelopmentTarget(t *testing.T) {
	for _, developmentURL := range []string{
		"postgres://dev:secret@localhost:25432/todo_test",
		"postgres://dev:secret@[::1]:25432/todo_test",
		"postgres://dev:secret@127.0.0.2:25432/todo_test",
	} {
		_, _, _, err := Resolve("postgres://tester:secret@127.0.0.1:25432/todo_test", developmentURL, true)
		if err == nil || !strings.Contains(err.Error(), "different database") {
			t.Fatalf("Resolve() error = %v, want same-target rejection", err)
		}
	}
}

func TestResolveAllowsDifferentDatabaseOnLoopbackServer(t *testing.T) {
	_, target, skip, err := Resolve(
		"postgres://tester:secret@127.0.0.1:25432/todo_test",
		"postgres://dev:secret@localhost:15432/todo_dev",
		true,
	)
	if err != nil || skip || target.Database != "todo_test" {
		t.Fatalf("Resolve() = (%#v, %t, %v), want distinct test database", target, skip, err)
	}
}

func TestResolveMissingURLSkipsOnlyWhenNotRequired(t *testing.T) {
	_, _, skip, err := Resolve("", "", false)
	if err != nil || !skip {
		t.Fatalf("optional missing URL = skip %t, err %v; want skip", skip, err)
	}
	_, _, skip, err = Resolve("", "", true)
	if err == nil || skip || !strings.Contains(err.Error(), "REQUIRE_POSTGRES_INTEGRATION") {
		t.Fatalf("required missing URL = skip %t, err %v; want explicit-mode error", skip, err)
	}
}

func TestIntegrationRequiredValues(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{value: "", want: false},
		{value: "0", want: false},
		{value: "false", want: false},
		{value: "1", want: true},
		{value: "true", want: true},
		{value: "TRUE", want: true},
	} {
		got, err := integrationRequired(tc.value)
		if err != nil || got != tc.want {
			t.Errorf("integrationRequired(%q) = %t, %v; want %t", tc.value, got, err, tc.want)
		}
	}
	for _, value := range []string{"yes", "2", "maybe"} {
		if _, err := integrationRequired(value); err == nil {
			t.Errorf("integrationRequired(%q) accepted unsupported value", value)
		}
	}
}
