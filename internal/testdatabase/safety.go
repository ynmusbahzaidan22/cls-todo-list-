// Package testdatabase contains fail-closed configuration checks shared by
// PostgreSQL integration tests. It is only imported by test code.
package testdatabase

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/jackc/pgx/v5"
)

var (
	testDatabaseName  = "todo_test"
	testDatabasePort  = uint16(25432)
	validDatabaseName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	validPort         = regexp.MustCompile(`^[1-9][0-9]{0,4}$`)
	validSSLMode      = regexp.MustCompile(`^sslmode=(disable|allow|prefer|require|verify-ca|verify-full)$`)
)

// Target is the normalized database endpoint used for safe target comparison.
type Target struct {
	Host     string
	Port     uint16
	Database string
}

// Resolve validates an explicit disposable test URL. skip is true only for a
// normal local run with no URL and no explicit integration-test requirement.
func Resolve(testURL, developmentURL string, required bool) (value string, target Target, skip bool, err error) {
	if testURL == "" {
		if required {
			return "", Target{}, false, fmt.Errorf("TEST_DATABASE_URL is required when REQUIRE_POSTGRES_INTEGRATION is enabled")
		}
		return "", Target{}, true, nil
	}
	if testURL != strings.TrimSpace(testURL) {
		return "", Target{}, false, fmt.Errorf("TEST_DATABASE_URL must not contain surrounding whitespace")
	}
	target, err = parseTarget(testURL, true, "TEST_DATABASE_URL")
	if err != nil {
		return "", Target{}, false, err
	}
	if strings.TrimSpace(developmentURL) != "" {
		devTarget, parseErr := parseTarget(developmentURL, false, "DATABASE_URL")
		if parseErr != nil {
			return "", Target{}, false, parseErr
		}
		if SameTarget(target, devTarget) {
			return "", Target{}, false, fmt.Errorf("TEST_DATABASE_URL must identify a different database from DATABASE_URL")
		}
	}
	return testURL, target, false, nil
}

// URL reads and validates a configured dedicated test-database URL. Missing
// configuration skips by default, but fails when REQUIRE_POSTGRES_INTEGRATION
// is set to 1 or true.
func URL(t testing.TB, variableName, developmentURL string) (string, Target) {
	t.Helper()
	required, err := integrationRequired(os.Getenv("REQUIRE_POSTGRES_INTEGRATION"))
	if err != nil {
		t.Fatal(err)
	}
	value, target, skip, err := Resolve(os.Getenv(variableName), developmentURL, required)
	if err != nil {
		t.Fatal(err)
	}
	if skip {
		t.Skipf("%s is not set; set REQUIRE_POSTGRES_INTEGRATION=1 to require PostgreSQL integration coverage", variableName)
	}
	return value, target
}

// TestURL is the standard administrative test connection used by integration
// tests. Its target is checked against DATABASE_URL when that is configured.
func TestURL(t testing.TB) (string, Target) {
	t.Helper()
	return URL(t, "TEST_DATABASE_URL", os.Getenv("DATABASE_URL"))
}

// SameTarget compares normalized host, port, and database name. All literal
// loopback spellings intentionally share one canonical host to fail closed.
func SameTarget(left, right Target) bool {
	return canonicalHost(left.Host) == canonicalHost(right.Host) &&
		left.Port == right.Port && left.Database == right.Database
}

func integrationRequired(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, fmt.Errorf("REQUIRE_POSTGRES_INTEGRATION must be 1, true, 0, or false")
	}
}

func parseTarget(raw string, requireTestName bool, variableName string) (Target, error) {
	invalid := func() (Target, error) {
		return Target{}, fmt.Errorf("%s must be an unambiguous PostgreSQL URL with an explicit host and database", variableName)
	}
	if raw == "" || (!strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://")) || strings.ContainsAny(raw, "\x00\r\n\\#") || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return invalid()
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Opaque != "" || u.Fragment != "" || u.Host == "" || u.User == nil || u.User.Username() == "" {
		return invalid()
	}
	if strings.Contains(u.Host, ",") || strings.Contains(u.Host, "%") || strings.ContainsAny(u.Hostname(), " \t\r\n") {
		return invalid()
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost." {
		return invalid()
	}
	if requireTestName {
		if host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return Target{}, fmt.Errorf("TEST_DATABASE_URL must use a loopback PostgreSQL host")
		}
	}
	if u.RawPath != "" || u.EscapedPath() != u.Path || !strings.HasPrefix(u.Path, "/") || strings.Count(u.Path, "/") != 1 {
		return invalid()
	}
	databaseName := strings.TrimPrefix(u.Path, "/")
	if databaseName == "" || !validDatabaseName.MatchString(databaseName) {
		return invalid()
	}
	if requireTestName && databaseName != testDatabaseName {
		return Target{}, fmt.Errorf("TEST_DATABASE_URL must use the dedicated todo_test database")
	}
	if u.ForceQuery && u.RawQuery == "" || u.RawQuery != "" && !validSSLMode.MatchString(u.RawQuery) {
		return invalid()
	}
	if u.Port() != "" {
		if !validPort.MatchString(u.Port()) {
			return invalid()
		}
		port, parseErr := strconv.ParseUint(u.Port(), 10, 16)
		if parseErr != nil || port == 0 {
			return invalid()
		}
	}

	// pgx parses the effective target. Requiring all target fields in the URL
	// and allowing only sslmode prevents PG* environment defaults or query
	// parameters from redirecting this connection.
	config, err := pgx.ParseConfig(raw)
	if err != nil || config == nil || config.Host == "" || config.Database == "" || config.User == "" {
		return invalid()
	}
	if strings.ToLower(config.Host) != host || config.Port == 0 || config.Database != databaseName {
		return invalid()
	}
	if requireTestName && config.Port != testDatabasePort {
		return Target{}, fmt.Errorf("TEST_DATABASE_URL must use the isolated test PostgreSQL port 25432")
	}
	return Target{Host: canonicalHost(host), Port: config.Port, Database: config.Database}, nil
}

func canonicalHost(host string) string {
	host = strings.ToLower(host)
	if host == "localhost" {
		return "<loopback>"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return "<loopback>"
	}
	return host
}
