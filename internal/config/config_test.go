package config

import (
	"os"
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
		"DATABASE_URL":          "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full",
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

func TestLoadValidRenderProductionConfiguration(t *testing.T) {
	values := renderProductionValues()
	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatalf("valid Render configuration was rejected: %v", err)
	}
	if cfg.TrustedProxyProvider != "render" || len(cfg.TrustedProxyCIDRs) != 0 {
		t.Fatalf("Render proxy mode was not selected exclusively: provider=%q CIDRs=%v", cfg.TrustedProxyProvider, cfg.TrustedProxyCIDRs)
	}
}

func TestLoadRenderProxyModeFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		change func(map[string]string)
	}{
		{name: "missing Render marker", change: func(v map[string]string) { delete(v, "RENDER") }},
		{name: "forged false Render marker", change: func(v map[string]string) { v["RENDER"] = "false" }},
		{name: "wrong service type", change: func(v map[string]string) { v["RENDER_SERVICE_TYPE"] = "private" }},
		{name: "missing service ID", change: func(v map[string]string) { delete(v, "RENDER_SERVICE_ID") }},
		{name: "blank service ID", change: func(v map[string]string) { v["RENDER_SERVICE_ID"] = "  " }},
		{name: "missing external URL", change: func(v map[string]string) { delete(v, "RENDER_EXTERNAL_URL") }},
		{name: "external URL mismatch", change: func(v map[string]string) { v["RENDER_EXTERNAL_URL"] = "https://attacker.onrender.com" }},
		{name: "missing external hostname", change: func(v map[string]string) { delete(v, "RENDER_EXTERNAL_HOSTNAME") }},
		{name: "external hostname mismatch", change: func(v map[string]string) { v["RENDER_EXTERNAL_HOSTNAME"] = "attacker.onrender.com" }},
		{name: "conflicting CIDR mode", change: func(v map[string]string) { v["TRUSTED_PROXY_CIDRS"] = "127.0.0.1/32" }},
		{name: "Render mode outside production", change: func(v map[string]string) { v["APP_ENV"] = "development" }},
		{name: "unknown provider", change: func(v map[string]string) { v["TRUSTED_PROXY_PROVIDER"] = "other" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := renderProductionValues()
			tt.change(values)
			if _, err := load(mapLookup(values)); err == nil {
				t.Fatal("unsafe Render proxy configuration was accepted")
			}
		})
	}
}

func TestProductionCIDRModeStillRequiresExplicitPeers(t *testing.T) {
	values := renderProductionValues()
	delete(values, "TRUSTED_PROXY_PROVIDER")
	delete(values, "RENDER")
	delete(values, "RENDER_SERVICE_TYPE")
	delete(values, "RENDER_SERVICE_ID")
	delete(values, "RENDER_EXTERNAL_URL")
	delete(values, "RENDER_EXTERNAL_HOSTNAME")
	if _, err := load(mapLookup(values)); err == nil {
		t.Fatal("production CIDR mode accepted an empty trusted proxy list")
	}
	values["TRUSTED_PROXY_CIDRS"] = "192.0.2.4/32"
	cfg, err := load(mapLookup(values))
	if err != nil {
		t.Fatalf("production CIDR mode rejected an explicit peer: %v", err)
	}
	if cfg.TrustedProxyProvider != "" || len(cfg.TrustedProxyCIDRs) != 1 {
		t.Fatalf("unexpected CIDR mode selection: provider=%q CIDRs=%v", cfg.TrustedProxyProvider, cfg.TrustedProxyCIDRs)
	}
}

func renderProductionValues() map[string]string {
	return map[string]string{
		"APP_ENV":                  "production",
		"HTTP_ADDR":                "0.0.0.0:10000",
		"DATABASE_URL":             "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full",
		"DATABASE_RUNTIME_ROLE":    "todo_runtime",
		"TRUSTED_ORIGIN":           "https://cls-todo-list.onrender.com",
		"SESSION_COOKIE_SECURE":    "true",
		"TRUSTED_PROXY_PROVIDER":   "render",
		"TASK_CURSOR_SIGNING_KEY":  strings.Repeat("ab", 32),
		"RENDER":                   "true",
		"RENDER_SERVICE_TYPE":      "web",
		"RENDER_SERVICE_ID":        "srv-test",
		"RENDER_EXTERNAL_URL":      "https://cls-todo-list.onrender.com",
		"RENDER_EXTERNAL_HOSTNAME": "cls-todo-list.onrender.com",
	}
}

func TestProductionDatabaseURLRejectsAmbiguousOrOverriddenSettings(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSERVICEFILE", "")
	tests := []struct {
		name string
		dsn  string
		role string
	}{
		{name: "missing TLS mode", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo", role: "todo_runtime"},
		{name: "TLS disabled", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=disable", role: "todo_runtime"},
		{name: "TLS allow is not verified", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=allow", role: "todo_runtime"},
		{name: "TLS prefer is not verified", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=prefer", role: "todo_runtime"},
		{name: "TLS require does not verify identity", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=require", role: "todo_runtime"},
		{name: "TLS verify ca does not verify hostname", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-ca", role: "todo_runtime"},
		{name: "unknown TLS mode", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=custom", role: "todo_runtime"},
		{name: "verify full then disable", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&sslmode=disable", role: "todo_runtime"},
		{name: "disable then verify full", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=disable&sslmode=verify-full", role: "todo_runtime"},
		{name: "duplicate identical TLS mode", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&sslmode=verify-full", role: "todo_runtime"},
		{name: "encoded duplicate TLS parameter", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&%73slmode=disable", role: "todo_runtime"},
		{name: "case-variant TLS parameter", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&SSLMode=disable", role: "todo_runtime"},
		{name: "empty TLS mode", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=", role: "todo_runtime"},
		{name: "TLS alias", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&ssl=true", role: "todo_runtime"},
		{name: "query user override", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&user=other", role: "todo_runtime"},
		{name: "query host override", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&host=other.example.test", role: "todo_runtime"},
		{name: "query host address override", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&hostaddr=192.0.2.4", role: "todo_runtime"},
		{name: "query port override", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&port=5433", role: "todo_runtime"},
		{name: "query database override", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&database=other", role: "todo_runtime"},
		{name: "query database alias override", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&dbname=other", role: "todo_runtime"},
		{name: "query password override", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&password=other", role: "todo_runtime"},
		{name: "query passfile override", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&passfile=/tmp/credentials", role: "todo_runtime"},
		{name: "service file override", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&servicefile=/tmp/service.conf", role: "todo_runtime"},
		{name: "service override", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&service=production", role: "todo_runtime"},
		{name: "startup options override", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full&options=-c%20role%3Dother", role: "todo_runtime"},
		{name: "runtime role mismatch", dsn: "postgres://other:secret@db.example.test:5432/todo?sslmode=verify-full", role: "todo_runtime"},
		{name: "missing explicit port", dsn: "postgres://todo_runtime:secret@db.example.test/todo?sslmode=verify-full", role: "todo_runtime"},
		{name: "missing explicit password", dsn: "postgres://todo_runtime@db.example.test:5432/todo?sslmode=verify-full", role: "todo_runtime"},
		{name: "encoded database path", dsn: "postgres://todo_runtime:secret@db.example.test:5432/%74odo?sslmode=verify-full", role: "todo_runtime"},
		{name: "malformed query", dsn: "postgres://todo_runtime:secret@db.example.test:5432/todo?sslmode=verify-full;user=other", role: "todo_runtime"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseProductionDatabaseConfig(test.dsn, test.role, os.LookupEnv); err == nil {
				t.Fatal("ambiguous or unsafe production database URL was accepted")
			} else if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "db.example.test") {
				t.Fatal("database URL validation error exposed connection details")
			}
		})
	}
}

func TestProductionDatabaseConfigRejectsAmbientPGServiceSettings(t *testing.T) {
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE"} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseProductionDatabaseConfig(
				"postgres://todo_runtime:url-secret@db.example.test:5432/todo?sslmode=verify-full",
				"todo_runtime",
				mapLookup(map[string]string{name: "configured"}),
			); err == nil {
				t.Fatal("production database configuration accepted ambient service settings")
			} else if strings.Contains(err.Error(), "url-secret") || strings.Contains(err.Error(), "db.example.test") {
				t.Fatal("database URL validation error exposed connection details")
			}
		})
	}
}

func TestProductionDatabaseConfigUsesVerifiedEffectivePGXConfiguration(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSERVICEFILE", "")
	t.Setenv("PGHOST", "environment-host.example.test")
	t.Setenv("PGPORT", "6432")
	t.Setenv("PGUSER", "environment-user")
	t.Setenv("PGPASSWORD", "environment-password")
	t.Setenv("PGDATABASE", "environment-database")
	t.Setenv("PGSSLMODE", "disable")
	t.Setenv("PGOPTIONS", "")

	config, err := parseProductionDatabaseConfig(
		"postgres://todo_runtime:url-secret@db.example.test:5432/todo?sslmode=verify-full&sslrootcert=system&application_name=todo-api",
		"todo_runtime",
		os.LookupEnv,
	)
	if err != nil {
		t.Fatalf("valid production database configuration was rejected: %v", err)
	}
	connection := config.ConnConfig
	if connection.Host != "db.example.test" || connection.Port != 5432 || connection.Database != "todo" ||
		connection.User != "todo_runtime" || connection.Password != "url-secret" || len(connection.Fallbacks) != 0 ||
		connection.TLSConfig == nil || connection.TLSConfig.InsecureSkipVerify || connection.TLSConfig.ServerName != "db.example.test" {
		t.Fatal("effective pgx identity or TLS configuration does not match the approved URL")
	}
}

func TestProductionDatabaseConfigRejectsAmbientPGOptions(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSERVICEFILE", "")
	t.Setenv("PGOPTIONS", "-c role=other")
	if _, err := parseProductionDatabaseConfig(
		"postgres://todo_runtime:url-secret@db.example.test:5432/todo?sslmode=verify-full",
		"todo_runtime",
		os.LookupEnv,
	); err == nil {
		t.Fatal("production database configuration accepted ambient PGOPTIONS")
	}
}

func TestPlaywrightTestModeRequiresExactIsolatedDatabase(t *testing.T) {
	valid := map[string]string{
		"APP_ENV":             "development",
		"DATABASE_URL":        "postgres://test:secret@127.0.0.1:25432/todo_test",
		"HTTP_ADDR":           "127.0.0.1:18080",
		"TRUSTED_PROXY_CIDRS": "127.0.0.1/32",
		playwrightTestModeEnv: "1",
	}
	cfg, err := load(mapLookup(valid))
	if err != nil || !cfg.PlaywrightTestMode {
		t.Fatalf("isolated Playwright test mode config = enabled %v, err %v", cfg.PlaywrightTestMode, err)
	}

	for name, databaseURL := range map[string]string{
		"development database": "postgres://test:secret@127.0.0.1:15432/todo_dev",
		"wrong database":       "postgres://test:secret@127.0.0.1:25432/todo_dev",
		"remote host":          "postgres://test:secret@db.example.test:25432/todo_test",
		"alternate loopback":   "postgres://test:secret@localhost:25432/todo_test",
		"wrong port":           "postgres://test:secret@127.0.0.1:25433/todo_test",
		"target query":         "postgres://test:secret@127.0.0.1:25432/todo_test?host=localhost",
	} {
		t.Run(name, func(t *testing.T) {
			values := cloneConfigValues(valid)
			values["DATABASE_URL"] = databaseURL
			if _, err := load(mapLookup(values)); err == nil {
				t.Fatal("unsafe E2E test database target was accepted")
			}
		})
	}
}

func TestPlaywrightTestModeCannotDisableProductionMaintenance(t *testing.T) {
	values := map[string]string{
		"APP_ENV":                 "production",
		"HTTP_ADDR":               "0.0.0.0:8080",
		"DATABASE_URL":            "postgres://test:secret@127.0.0.1:25432/todo_test?sslmode=verify-full",
		"DATABASE_RUNTIME_ROLE":   "todo_test_runtime",
		"TRUSTED_ORIGIN":          "https://todo.example.test",
		"SESSION_COOKIE_SECURE":   "true",
		"TRUSTED_PROXY_CIDRS":     "127.0.0.1/32",
		"TASK_CURSOR_SIGNING_KEY": strings.Repeat("ab", 32),
		playwrightTestModeEnv:     "1",
	}
	if _, err := load(mapLookup(values)); err == nil {
		t.Fatal("production configuration accepted Playwright maintenance override")
	}

	values["APP_ENV"] = "development"
	values[playwrightTestModeEnv] = "true"
	if _, err := load(mapLookup(values)); err == nil {
		t.Fatal("non-canonical Playwright mode value was accepted")
	}
	values[playwrightTestModeEnv] = "1"
	values["TRUSTED_PROXY_CIDRS"] = "127.0.0.0/8"
	if _, err := load(mapLookup(values)); err == nil {
		t.Fatal("broad proxy trust was accepted in Playwright test mode")
	}
	values["TRUSTED_PROXY_CIDRS"] = "127.0.0.1/32"
	values["HTTP_ADDR"] = "localhost:18080"
	if _, err := load(mapLookup(values)); err == nil {
		t.Fatal("non-literal API bind host was accepted in Playwright test mode")
	}
}

func cloneConfigValues(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
