package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultHTTPAddr = "127.0.0.1:8080"
const defaultDatabaseMaxConns = 10
const playwrightTestModeEnv = "PLAYWRIGHT_TEST_MODE"

var testSSLMode = regexp.MustCompile(`^sslmode=(disable|allow|prefer|require|verify-ca|verify-full)$`)

// Config contains only runtime settings that affect this server process.
type Config struct {
	AppEnv               string
	HTTPAddr             string
	DatabaseURL          string
	DatabaseMaxConns     int32
	DatabaseRuntimeRole  string
	TrustedOrigin        string
	TaskCursorSigningKey []byte
	SessionCookieSecure  bool
	TrustedProxyCIDRs    []string
	PlaywrightTestMode   bool
}

// Load reads and validates process environment configuration.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

func load(lookup func(string) (string, bool)) (Config, error) {
	appEnv := strings.ToLower(strings.TrimSpace(valueOr(lookup, "APP_ENV", "development")))
	if appEnv != "development" && appEnv != "production" {
		return Config{}, fmt.Errorf("APP_ENV must be development or production")
	}
	production := appEnv == "production"
	if production {
		for _, required := range []string{"HTTP_ADDR", "TRUSTED_ORIGIN", "SESSION_COOKIE_SECURE", "DATABASE_RUNTIME_ROLE", "TASK_CURSOR_SIGNING_KEY"} {
			if value, ok := lookup(required); !ok || strings.TrimSpace(value) == "" {
				return Config{}, fmt.Errorf("%s is required when APP_ENV=production", required)
			}
		}
	}
	addr := strings.TrimSpace(valueOr(lookup, "HTTP_ADDR", defaultHTTPAddr))
	if err := validateAddress(addr); err != nil {
		return Config{}, err
	}

	databaseURL := strings.TrimSpace(valueOr(lookup, "DATABASE_URL", ""))
	runtimeRole := strings.TrimSpace(valueOr(lookup, "DATABASE_RUNTIME_ROLE", ""))
	if production {
		if _, err := parseProductionDatabaseConfig(databaseURL, runtimeRole, lookup); err != nil {
			return Config{}, err
		}
	} else if err := validateDatabaseURL(databaseURL); err != nil {
		return Config{}, err
	}
	playwrightTestMode, err := validatePlaywrightTestMode(lookup, appEnv, databaseURL)
	if err != nil {
		return Config{}, err
	}

	maxConns := defaultDatabaseMaxConns
	if raw, ok := lookup("DATABASE_MAX_CONNS"); ok && strings.TrimSpace(raw) != "" {
		parsed, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || parsed < 1 || parsed > 100 {
			return Config{}, fmt.Errorf("DATABASE_MAX_CONNS must be an integer from 1 to 100")
		}
		maxConns = parsed
	}

	secureCookie := false
	if raw, ok := lookup("SESSION_COOKIE_SECURE"); ok && strings.TrimSpace(raw) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return Config{}, fmt.Errorf("SESSION_COOKIE_SECURE must be true or false")
		}
		secureCookie = parsed
	}
	if !secureCookie && !isLoopbackAddress(addr) {
		return Config{}, fmt.Errorf("SESSION_COOKIE_SECURE must be true when HTTP_ADDR is not loopback")
	}

	trustedOrigin := strings.TrimSpace(valueOr(lookup, "TRUSTED_ORIGIN", defaultTrustedOrigin(addr)))
	if err := validateOrigin(trustedOrigin); err != nil {
		return Config{}, err
	}
	if secureCookie && !strings.HasPrefix(strings.ToLower(trustedOrigin), "https://") {
		return Config{}, fmt.Errorf("TRUSTED_ORIGIN must use https when SESSION_COOKIE_SECURE is true")
	}
	if production {
		if !secureCookie {
			return Config{}, fmt.Errorf("SESSION_COOKIE_SECURE must be true when APP_ENV=production")
		}
		if !strings.HasPrefix(strings.ToLower(trustedOrigin), "https://") {
			return Config{}, fmt.Errorf("production TRUSTED_ORIGIN must use https")
		}
	}

	proxyCIDRs, err := parseProxyCIDRs(valueOr(lookup, "TRUSTED_PROXY_CIDRS", ""))
	if err != nil {
		return Config{}, err
	}
	if production && len(proxyCIDRs) == 0 {
		return Config{}, fmt.Errorf("TRUSTED_PROXY_CIDRS must identify the reverse proxy when APP_ENV=production")
	}
	cursorKey, keyErr := parseTaskCursorSigningKey(valueOr(lookup, "TASK_CURSOR_SIGNING_KEY", ""), production)
	if keyErr != nil {
		return Config{}, keyErr
	}

	return Config{
		AppEnv:               appEnv,
		HTTPAddr:             addr,
		DatabaseURL:          databaseURL,
		DatabaseMaxConns:     int32(maxConns),
		DatabaseRuntimeRole:  strings.TrimSpace(valueOr(lookup, "DATABASE_RUNTIME_ROLE", "")),
		TrustedOrigin:        trustedOrigin,
		TaskCursorSigningKey: cursorKey,
		SessionCookieSecure:  secureCookie,
		TrustedProxyCIDRs:    proxyCIDRs,
		PlaywrightTestMode:   playwrightTestMode,
	}, nil
}

// validatePlaywrightTestMode permits disabling background maintenance only for
// the explicitly marked local E2E process connected to the isolated test DB.
func validatePlaywrightTestMode(lookup func(string) (string, bool), appEnv, databaseURL string) (bool, error) {
	value, present := lookup(playwrightTestModeEnv)
	if !present || value == "" {
		return false, nil
	}
	if value != "1" {
		return false, fmt.Errorf("%s must be unset or exactly 1", playwrightTestModeEnv)
	}
	if appEnv != "development" {
		return false, fmt.Errorf("%s is allowed only with APP_ENV=development", playwrightTestModeEnv)
	}
	if err := validatePlaywrightDatabaseTarget(databaseURL); err != nil {
		return false, err
	}
	listenHost, _, err := net.SplitHostPort(valueOr(lookup, "HTTP_ADDR", defaultHTTPAddr))
	if err != nil || listenHost != "127.0.0.1" {
		return false, fmt.Errorf("HTTP_ADDR must bind to 127.0.0.1 when %s is enabled", playwrightTestModeEnv)
	}
	if valueOr(lookup, "TRUSTED_PROXY_CIDRS", "") != "127.0.0.1/32" {
		return false, fmt.Errorf("TRUSTED_PROXY_CIDRS must be exactly 127.0.0.1/32 in %s", playwrightTestModeEnv)
	}
	return true, nil
}

func validatePlaywrightDatabaseTarget(raw string) error {
	invalid := func() error {
		return fmt.Errorf("DATABASE_URL must target 127.0.0.1:25432/todo_test when %s is enabled", playwrightTestModeEnv)
	}
	if strings.ContainsAny(raw, "\\\\#\x00\r\n") || strings.IndexFunc(raw, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }) >= 0 {
		return invalid()
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.Opaque != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.User == nil || parsed.User.Username() == "" ||
		parsed.Hostname() != "127.0.0.1" || parsed.Port() != "25432" || parsed.Path != "/todo_test" || parsed.EscapedPath() != "/todo_test" {
		return invalid()
	}
	if parsed.RawQuery != "" && !testSSLMode.MatchString(parsed.RawQuery) {
		return invalid()
	}
	if parsed.ForceQuery {
		return invalid()
	}
	if _, err := pgxpool.ParseConfig(raw); err != nil {
		return invalid()
	}
	return nil
}

func parseTaskCursorSigningKey(raw string, production bool) ([]byte, error) {
	if strings.TrimSpace(raw) == "" {
		if production {
			return nil, fmt.Errorf("TASK_CURSOR_SIGNING_KEY is required when APP_ENV=production")
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate task cursor signing key: %w", err)
		}
		return key, nil
	}
	key, err := hex.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("TASK_CURSOR_SIGNING_KEY must be 64 hexadecimal characters")
	}
	return key, nil
}

func parseProxyCIDRs(raw string) ([]string, error) {
	var cidrs []string
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(value); err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS must contain comma-separated CIDRs")
		}
		cidrs = append(cidrs, value)
	}
	return cidrs, nil
}

func valueOr(lookup func(string) (string, bool), key, fallback string) string {
	if value, ok := lookup(key); ok {
		return value
	}
	return fallback
}

func validateAddress(addr string) error {
	if addr == "" {
		return fmt.Errorf("HTTP_ADDR must not be empty")
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("HTTP_ADDR must use host:port format")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("HTTP_ADDR must include a port from 1 to 65535")
	}
	return nil
}

func isLoopbackAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func validateOrigin(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("TRUSTED_ORIGIN must be an http or https origin without a path")
	}
	return nil
}

func defaultTrustedOrigin(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return "http://" + net.JoinHostPort(host, port)
}

func validateDatabaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" || strings.Trim(parsed.Path, "/") == "" {
		return fmt.Errorf("DATABASE_URL must be a valid PostgreSQL URL with a host and database name")
	}
	if _, err := pgxpool.ParseConfig(raw); err != nil {
		return fmt.Errorf("DATABASE_URL must be a valid PostgreSQL URL with a host and database name")
	}
	return nil
}

// parseProductionDatabaseConfig validates the canonical URL and the effective
// pgx configuration using pgx's parser. Production identity and TLS settings
// must come from one explicit URL authority/path and a single verified mode.
func parseProductionDatabaseConfig(raw, runtimeRole string, lookup func(string) (string, bool)) (*pgxpool.Config, error) {
	invalid := func(reason string) (*pgxpool.Config, error) {
		return nil, fmt.Errorf("production DATABASE_URL %s", reason)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Opaque != "" ||
		parsed.Fragment != "" || parsed.ForceQuery || parsed.RawPath != "" || parsed.User == nil {
		return invalid("is malformed or non-canonical")
	}

	password, hasPassword := parsed.User.Password()
	user := parsed.User.Username()
	host := parsed.Hostname()
	portText := parsed.Port()
	if !hasPassword || password == "" || user == "" {
		return invalid("must provide an explicit user and password")
	}
	if host == "" || strings.Contains(host, ",") || strings.Contains(host, "%") {
		return invalid("must provide one canonical host")
	}
	if portText == "" {
		return invalid("must provide an explicit port")
	}
	if parsed.Path == "/" || !strings.HasPrefix(parsed.Path, "/") || strings.Contains(strings.TrimPrefix(parsed.Path, "/"), "/") {
		return invalid("must provide one canonical database path")
	}
	if user != runtimeRole {
		return invalid("URL user does not match DATABASE_RUNTIME_ROLE")
	}
	databaseName := strings.TrimPrefix(parsed.Path, "/")
	portValue, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || portValue == 0 || strconv.FormatUint(portValue, 10) != portText {
		return invalid("must use a canonical numeric port")
	}

	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return invalid("contains a malformed query")
	}
	values := make(map[string]string, len(query))
	for key, occurrences := range query {
		canonicalKey := strings.ToLower(key)
		if key != canonicalKey || len(occurrences) != 1 {
			return invalid("contains a duplicate or non-canonical query parameter")
		}
		if _, duplicate := values[canonicalKey]; duplicate {
			return invalid("contains a query parameter that can override connection identity or TLS")
		}
		values[canonicalKey] = occurrences[0]
		switch canonicalKey {
		case "host", "hostaddr", "port", "user", "username", "database", "dbname",
			"password", "passfile", "service", "servicefile", "ssl", "options",
			"role", "session_authorization", "session_replication_role":
			return invalid("contains a query-level connection identity or TLS override")
		}
	}
	if values["sslmode"] != "verify-full" {
		return invalid("must contain exactly one sslmode=verify-full parameter")
	}

	// pgx can load a service file before applying URL values. Reject ambient
	// service configuration so it cannot add an unreviewed settings source.
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE"} {
		if value, present := lookup(name); present && strings.TrimSpace(value) != "" {
			return invalid("cannot use ambient PostgreSQL service configuration")
		}
	}

	config, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return invalid("could not be parsed by pgx")
	}
	connection := config.ConnConfig
	if connection.Host != host || connection.Port != uint16(portValue) || connection.Database != databaseName ||
		connection.User != runtimeRole || connection.Password != password || len(connection.Fallbacks) != 0 ||
		connection.TLSConfig == nil || connection.TLSConfig.InsecureSkipVerify || connection.TLSConfig.ServerName != host {
		return invalid("does not produce the expected effective pgx identity")
	}
	if _, hasOptions := connection.RuntimeParams["options"]; hasOptions {
		return invalid("cannot use startup options")
	}
	for _, name := range []string{"role", "session_authorization", "session_replication_role"} {
		if _, present := connection.RuntimeParams[name]; present {
			return invalid("cannot change the effective session role")
		}
	}
	return config, nil
}
