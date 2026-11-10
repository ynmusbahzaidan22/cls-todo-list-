package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/health"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
)

func TestAuthenticationAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL integration test skipped")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		t.Fatal("TEST_DATABASE_URL must target a loopback PostgreSQL server")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect test PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test PostgreSQL: %v", err)
	}

	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate test account suffix: %v", err)
	}
	email := "phase3-" + hex.EncodeToString(suffix[:]) + "@example.test"
	testIP := net.IPv4(198, 18, suffix[0], suffix[1]).String()
	password := "phase three integration password"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(cleanupCtx, "DELETE FROM app.users WHERE email = $1", email); cleanupErr != nil {
			t.Errorf("clean up only this test account: %v", cleanupErr)
		}
		for _, key := range []string{"register:ip:" + testIP, "login:ip:" + testIP} {
			keyHash := sha256.Sum256([]byte(key))
			if _, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM app.auth_rate_limits WHERE key_hash = $1`, keyHash[:]); cleanupErr != nil {
				t.Errorf("clean up only this test rate-limit key: %v", cleanupErr)
			}
		}
	})

	manager, err := auth.NewManager(repository.NewAuthRepository(pool))
	if err != nil {
		t.Fatalf("initialize auth manager: %v", err)
	}
	trustedOrigin := "http://127.0.0.1:8080"
	handler := NewRouter(health.NewService(pool), testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(manager, "todo_session", false), TrustedOrigin: trustedOrigin,
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = net.JoinHostPort(testIP, "12345")
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	live := performHTTP(t, server, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), `"status":"ok"`) {
		t.Fatalf("live health response = %d, %s", live.Code, live.Body.String())
	}
	ready := performHTTP(t, server, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"database":"ready"`) {
		t.Fatalf("database readiness response = %d, %s", ready.Code, ready.Body.String())
	}

	register := makeJSONRequest(http.MethodPost, "/api/auth/register", fmt.Sprintf(`{"email":%q,"password":%q}`, strings.ToUpper(email), password), trustedOrigin, nil)
	registerResult := performHTTP(t, server, register)
	if registerResult.Code != http.StatusAccepted {
		t.Fatalf("register status = %d, body=%s", registerResult.Code, registerResult.Body.String())
	}
	var registrationResponse map[string]string
	if err := json.Unmarshal(registerResult.Body.Bytes(), &registrationResponse); err != nil || registrationResponse["status"] != "accepted" {
		t.Fatalf("unexpected registration response: %+v, err=%v", registrationResponse, err)
	}
	duplicate := makeJSONRequest(http.MethodPost, "/api/auth/register", fmt.Sprintf(`{"email":%q,"password":%q}`, email, password), trustedOrigin, nil)
	duplicateResult := performHTTP(t, server, duplicate)
	if duplicateResult.Code != http.StatusAccepted || duplicateResult.Body.String() != registerResult.Body.String() {
		t.Fatalf("duplicate registration status = %d, body=%s", duplicateResult.Code, duplicateResult.Body.String())
	}
	var registered struct {
		ID string
	}
	if err := pool.QueryRow(ctx, `SELECT id::text FROM app.users WHERE email = $1`, email).Scan(&registered.ID); err != nil || registered.ID == "" {
		t.Fatalf("read created account identifier: id=%q err=%v", registered.ID, err)
	}

	wrongLogin := makeJSONRequest(http.MethodPost, "/api/auth/login", fmt.Sprintf(`{"email":%q,"password":%q}`, email, "wrong but long enough password"), trustedOrigin, nil)
	wrongLoginResult := performHTTP(t, server, wrongLogin)
	if wrongLoginResult.Code != http.StatusUnauthorized || !strings.Contains(wrongLoginResult.Body.String(), "Email or password is incorrect") {
		t.Fatalf("invalid login response = %d, %s", wrongLoginResult.Code, wrongLoginResult.Body.String())
	}
	injectionLogin := makeJSONRequest(http.MethodPost, "/api/auth/login", fmt.Sprintf(`{"email":%q,"password":%q}`, "x' OR 1=1 --", password), trustedOrigin, nil)
	injectionLoginResult := performHTTP(t, server, injectionLogin)
	if injectionLoginResult.Code != http.StatusUnauthorized {
		t.Fatalf("SQL injection-shaped login did not fail generically: %d, %s", injectionLoginResult.Code, injectionLoginResult.Body.String())
	}

	login := makeJSONRequest(http.MethodPost, "/api/auth/login", fmt.Sprintf(`{"email":%q,"password":%q}`, email, password), trustedOrigin, nil)
	loginResult := performHTTP(t, server, login)
	if loginResult.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", loginResult.Code, loginResult.Body.String())
	}
	cookies := loginResult.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Value == "" {
		t.Fatalf("unexpected localhost session cookie policy: %+v", cookies)
	}
	tokenHash := sha256.Sum256([]byte(cookies[0].Value))
	var storedHash []byte
	var idleExpiry, absoluteExpiry time.Time
	if err := pool.QueryRow(ctx, `SELECT token_hash, idle_expires_at, absolute_expires_at FROM app.sessions WHERE user_id = $1`, registered.ID).Scan(&storedHash, &idleExpiry, &absoluteExpiry); err != nil {
		t.Fatalf("read session metadata: %v", err)
	}
	if !bytes.Equal(storedHash, tokenHash[:]) || len(storedHash) != sha256.Size {
		t.Fatal("database session token is not stored as its SHA-256 digest")
	}
	if absoluteExpiry.Sub(time.Now()) < 29*24*time.Hour || absoluteExpiry.Sub(time.Now()) > 30*24*time.Hour || idleExpiry.After(absoluteExpiry) {
		t.Fatalf("unexpected session expiry: idle=%s absolute=%s", idleExpiry, absoluteExpiry)
	}

	me := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	me.AddCookie(cookies[0])
	meResult := performHTTP(t, server, me)
	if meResult.Code != http.StatusOK || !strings.Contains(meResult.Body.String(), email) || strings.Contains(meResult.Body.String(), "password_hash") {
		t.Fatalf("current-user response = %d, %s", meResult.Code, meResult.Body.String())
	}

	var storedPasswordHash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM app.users WHERE id = $1`, registered.ID).Scan(&storedPasswordHash); err != nil {
		t.Fatalf("read password hash metadata: %v", err)
	}
	if !strings.HasPrefix(storedPasswordHash, "$argon2id$") || storedPasswordHash == password {
		t.Fatal("database did not store an Argon2id password hash")
	}

	expiredLogin := makeJSONRequest(http.MethodPost, "/api/auth/login", fmt.Sprintf(`{"email":%q,"password":%q}`, email, password), trustedOrigin, nil)
	expiredLoginResult := performHTTP(t, server, expiredLogin)
	if expiredLoginResult.Code != http.StatusOK {
		t.Fatalf("second login status = %d, body=%s", expiredLoginResult.Code, expiredLoginResult.Body.String())
	}
	expiredCookie := expiredLoginResult.Result().Cookies()[0]
	expiredHash := sha256.Sum256([]byte(expiredCookie.Value))
	var forcedIdleExpiry time.Time
	if err := pool.QueryRow(ctx, `UPDATE app.sessions SET idle_expires_at = created_at + interval '1 second' WHERE token_hash = $1 RETURNING idle_expires_at`, expiredHash[:]).Scan(&forcedIdleExpiry); err != nil {
		t.Fatalf("expire only the second test session: %v", err)
	}
	time.Sleep(time.Until(forcedIdleExpiry) + 15*time.Millisecond)
	expiredMe := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	expiredMe.AddCookie(expiredCookie)
	expiredMeResult := performHTTP(t, server, expiredMe)
	if expiredMeResult.Code != http.StatusUnauthorized {
		t.Fatalf("expired session remained valid: status=%d body=%s", expiredMeResult.Code, expiredMeResult.Body.String())
	}

	logout := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logout.Header.Set("Origin", trustedOrigin)
	logout.AddCookie(cookies[0])
	logoutResult := performHTTP(t, server, logout)
	if logoutResult.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, body=%s", logoutResult.Code, logoutResult.Body.String())
	}
	meAgain := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	meAgain.AddCookie(cookies[0])
	meAgainResult := performHTTP(t, server, meAgain)
	if meAgainResult.Code != http.StatusUnauthorized {
		t.Fatalf("session remained valid after logout: status=%d body=%s", meAgainResult.Code, meAgainResult.Body.String())
	}

	assertTaskConstraint := func(name string, query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err == nil {
			t.Errorf("database accepted invalid task data: %s", name)
		}
	}
	assertTaskConstraint("invalid status", `INSERT INTO app.tasks (user_id, title, status) VALUES ($1, 'valid title', 'INVALID')`, registered.ID)
	assertTaskConstraint("completed timestamp mismatch", `INSERT INTO app.tasks (user_id, title, status, completed_at) VALUES ($1, 'valid title', 'DONE', NULL)`, registered.ID)
	assertTaskConstraint("blank title", `INSERT INTO app.tasks (user_id, title) VALUES ($1, '   ')`, registered.ID)
	assertTaskConstraint("missing owner", `INSERT INTO app.tasks (user_id, title) VALUES ('00000000-0000-0000-0000-000000000000', 'valid title')`)

	var sessionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.sessions WHERE user_id = $1`, registered.ID).Scan(&sessionCount); err != nil {
		t.Fatalf("count test sessions: %v", err)
	}
	if sessionCount != 2 {
		t.Fatalf("expected two test sessions, got %d", sessionCount)
	}
}

func makeJSONRequest(method, path, body, origin string, cookie *http.Cookie) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	return request
}

func performHTTP(t *testing.T, server *httptest.Server, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	target, err := url.Parse(server.URL + request.URL.RequestURI())
	if err != nil {
		t.Fatalf("create live HTTP test URL: %v", err)
	}
	request.URL = target
	request.RequestURI = ""
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("send live HTTP test request: %v", err)
	}
	defer response.Body.Close()

	result := httptest.NewRecorder()
	for name, values := range response.Header {
		for _, value := range values {
			result.Header().Add(name, value)
		}
	}
	result.WriteHeader(response.StatusCode)
	if _, err := io.Copy(result, response.Body); err != nil {
		t.Fatalf("read live HTTP test response: %v", err)
	}
	return result
}
