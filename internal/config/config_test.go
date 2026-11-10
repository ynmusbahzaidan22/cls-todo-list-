package config

import (
	"strings"
	"testing"
)

func TestLoadValidConfigurationAndDefaults(t *testing.T) {
	values := map[string]string{"DATABASE_URL": "postgres://dev:secret@localhost:15432/todo?sslmode=disable"}
	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:8080" || cfg.DatabaseMaxConns != 10 || cfg.TrustedOrigin != "http://127.0.0.1:8080" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadAcceptsExplicitConfiguration(t *testing.T) {
	values := map[string]string{
		"HTTP_ADDR":          "127.0.0.1:9090",
		"DATABASE_URL":       "postgresql://dev:secret@localhost/todo",
		"DATABASE_MAX_CONNS": "24",
	}
	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" || cfg.DatabaseMaxConns != 24 || cfg.TrustedOrigin != "http://127.0.0.1:9090" {
		t.Fatalf("unexpected configuration: %+v", cfg)
	}
}

func TestLoadAcceptsSecureCookieWithHTTPSOrigin(t *testing.T) {
	values := map[string]string{
		"DATABASE_URL":          "postgres://dev:secret@localhost/todo",
		"TRUSTED_ORIGIN":        "https://todo.example.test",
		"SESSION_COOKIE_SECURE": "true",
	}
	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if !cfg.SessionCookieSecure || cfg.TrustedOrigin != "https://todo.example.test" {
		t.Fatalf("unexpected secure-cookie configuration: %+v", cfg)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "missing database URL", values: map[string]string{}},
		{name: "invalid database URL", values: map[string]string{"DATABASE_URL": "not-a-url"}},
		{name: "invalid listen address", values: map[string]string{"DATABASE_URL": "postgres://localhost/todo", "HTTP_ADDR": "not-an-address"}},
		{name: "out-of-range pool size", values: map[string]string{"DATABASE_URL": "postgres://localhost/todo", "DATABASE_MAX_CONNS": "101"}},
		{name: "insecure wildcard bind", values: map[string]string{"DATABASE_URL": "postgres://localhost/todo", "HTTP_ADDR": ":8080"}},
		{name: "secure cookie requires https origin", values: map[string]string{"DATABASE_URL": "postgres://localhost/todo", "SESSION_COOKIE_SECURE": "true"}},
		{name: "unknown app environment", values: map[string]string{"DATABASE_URL": "postgres://localhost/todo", "APP_ENV": "prod"}},
		{name: "production secure defaults are not implicit", values: map[string]string{"DATABASE_URL": "postgres://todo_runtime:secret@db.example/todo?sslmode=verify-full", "APP_ENV": "production"}},
		{name: "production requires TLS verification", values: map[string]string{"DATABASE_URL": "postgres://todo_runtime:secret@db.example/todo?sslmode=disable", "APP_ENV": "production", "DATABASE_RUNTIME_ROLE": "todo_runtime", "HTTP_ADDR": "0.0.0.0:8080", "TRUSTED_ORIGIN": "https://todo.example.test", "SESSION_COOKIE_SECURE": "true", "TRUSTED_PROXY_CIDRS": "10.0.0.0/8"}},
		{name: "production URL role matches dedicated role", values: map[string]string{"DATABASE_URL": "postgres://todo_dev:secret@db.example/todo?sslmode=verify-full", "APP_ENV": "production", "DATABASE_RUNTIME_ROLE": "todo_runtime", "HTTP_ADDR": "0.0.0.0:8080", "TRUSTED_ORIGIN": "https://todo.example.test", "SESSION_COOKIE_SECURE": "true", "TRUSTED_PROXY_CIDRS": "10.0.0.0/8"}},
		{name: "invalid proxy CIDR", values: map[string]string{"DATABASE_URL": "postgres://localhost/todo", "TRUSTED_PROXY_CIDRS": "not-a-cidr"}},
		{name: "invalid cursor signing key", values: map[string]string{"DATABASE_URL": "postgres://localhost/todo", "TASK_CURSOR_SIGNING_KEY": "short"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := load(mapLookup(test.values)); err == nil {
				t.Fatal("expected invalid configuration to fail")
			}
		})
	}
}

func TestLoadUsesConfiguredTaskCursorSigningKey(t *testing.T) {
	want := strings.Repeat("ab", 32)
	cfg, err := load(mapLookup(map[string]string{
		"DATABASE_URL": "postgres://dev:secret@localhost/todo", "TASK_CURSOR_SIGNING_KEY": want,
	}))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if len(cfg.TaskCursorSigningKey) != 32 || cfg.TaskCursorSigningKey[0] != 0xab {
		t.Fatalf("configured task cursor key was not decoded: %x", cfg.TaskCursorSigningKey)
	}
}

func TestLoadValidProductionConfiguration(t *testing.T) {
	values := map[string]string{
		"APP_ENV": "production", "HTTP_ADDR": "0.0.0.0:8080",
		"DATABASE_URL":          "postgres://todo_runtime:secret@db.example.test/todo?sslmode=verify-full",
		"DATABASE_RUNTIME_ROLE": "todo_runtime",
		"TRUSTED_ORIGIN":        "https://todo.example.test", "SESSION_COOKIE_SECURE": "true",
		"TRUSTED_PROXY_CIDRS":     "127.0.0.1/32, 10.0.0.0/8",
		"TASK_CURSOR_SIGNING_KEY": strings.Repeat("ab", 32),
	}
	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatalf("load returned error: %v", err)
	}
	if cfg.AppEnv != "production" || !cfg.SessionCookieSecure || len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatalf("unexpected production configuration: %+v", cfg)
	}
	if len(cfg.TaskCursorSigningKey) != 32 {
		t.Fatalf("task cursor signing key length = %d, want 32", len(cfg.TaskCursorSigningKey))
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
