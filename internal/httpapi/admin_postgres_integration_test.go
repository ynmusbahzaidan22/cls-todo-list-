package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/admin"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/database"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/health"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/testdatabase"
)

const adminHTTPTestOrigin = "http://127.0.0.1:8080"

type adminHTTPFixture struct {
	server               *httptest.Server
	adminPool            *pgxpool.Pool
	runtimePool          *pgxpool.Pool
	ip                   string
	adminEmail           string
	userEmail            string
	adminPass            string
	userPass             string
	adminCookie          *http.Cookie
	userCookie           *http.Cookie
	userID               string
	adminID              string
	taskID               string
	baselineActiveAdmins int64
}

func TestAdminHTTPAuthorizationAndResponsesPostgreSQL(t *testing.T) {
	f := newAdminHTTPFixture(t)

	for _, path := range []string{"/api/admin/users", "/api/admin/tasks"} {
		if got := f.get(t, path, nil).Code; got != http.StatusUnauthorized {
			t.Errorf("missing session %s returned %d; want 401", path, got)
		}
		if got := f.get(t, path, &http.Cookie{Name: "todo_session", Value: "invalid-session-token"}).Code; got != http.StatusUnauthorized {
			t.Errorf("invalid session %s returned %d; want 401", path, got)
		}
		userSpoof := *f.userCookie
		if got := f.get(t, path+"?role=ADMIN", &userSpoof).Code; got != http.StatusForbidden {
			t.Errorf("USER session with role spoof on %s returned %d; want 403", path, got)
		}
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(f.userCookie)
		request.Header.Set("X-Role", "ADMIN")
		result := performHTTP(t, f.server, request)
		if result.Code != http.StatusForbidden {
			t.Errorf("USER session with X-Role spoof on %s returned %d; want 403", path, result.Code)
		}
	}

	// Exercise both real session rejection paths using unique sessions owned by
	// this fixture. The administrative pool can change only the one session hash.
	adminExpired := f.login(t, f.adminEmail, f.adminPass)
	expiredHash := sha256.Sum256([]byte(adminExpired.Value))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	_, err := f.adminPool.Exec(ctx, `UPDATE app.sessions SET created_at = clock_timestamp() - interval '8 days', idle_expires_at = clock_timestamp() - interval '1 second' WHERE token_hash = $1`, expiredHash[:])
	cancel()
	if err != nil {
		t.Fatalf("expire the fixture-owned Admin session: %v", err)
	}
	if got := f.get(t, "/api/admin/users", adminExpired).Code; got != http.StatusUnauthorized {
		t.Errorf("expired Admin session returned %d; want 401", got)
	}
	adminRevoked := f.login(t, f.adminEmail, f.adminPass)
	logout := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logout.Header.Set("Origin", adminHTTPTestOrigin)
	logout.AddCookie(adminRevoked)
	if result := performHTTP(t, f.server, logout); result.Code != http.StatusNoContent {
		t.Fatalf("logout for fixture-owned Admin session returned %d", result.Code)
	}
	if got := f.get(t, "/api/admin/tasks", adminRevoked).Code; got != http.StatusUnauthorized {
		t.Errorf("revoked Admin session returned %d; want 401", got)
	}

	users := f.get(t, "/api/admin/users?limit=100", f.adminCookie)
	if users.Code != http.StatusOK {
		t.Fatalf("Admin user list status=%d body=%s", users.Code, users.Body.String())
	}
	var userPage struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(users.Body.Bytes(), &userPage); err != nil {
		t.Fatalf("decode Admin user list: %v", err)
	}
	foundAdmin, foundUser := false, false
	for _, item := range userPage.Items {
		switch item["email"] {
		case f.adminEmail:
			foundAdmin = item["role"] == "ADMIN" && item["name"] == "Admin HTTP fixture administrator"
		case f.userEmail:
			foundUser = item["role"] == "USER" && item["name"] == "Admin HTTP fixture user"
		default:
			continue
		}
		for key := range item {
			if key != "id" && key != "name" && key != "email" && key != "role" && key != "status" && key != "created_at" && key != "can_change_role" && key != "can_change_status" && key != "can_revoke_sessions" && key != "protection_reason" {
				t.Errorf("Admin user DTO included undocumented field %q", key)
			}
		}
		for _, key := range []string{"password_hash", "password", "token_hash", "session_token"} {
			if _, exists := item[key]; exists {
				t.Errorf("Admin user DTO exposed sensitive field %q", key)
			}
		}
	}
	if !foundAdmin || !foundUser {
		t.Errorf("Admin user list did not return both fixture accounts with their expected trusted roles")
	}

	tasks := f.get(t, "/api/admin/tasks?state=all&limit=100", f.adminCookie)
	if tasks.Code != http.StatusOK {
		t.Fatalf("Admin task list status=%d body=%s", tasks.Code, tasks.Body.String())
	}
	var taskPage struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(tasks.Body.Bytes(), &taskPage); err != nil {
		t.Fatalf("decode Admin task list: %v", err)
	}
	foundTask := false
	for _, item := range taskPage.Items {
		if item["id"] != f.taskID {
			continue
		}
		foundTask = true
		if item["owner_id"] != f.userID || item["owner_name"] != "Admin HTTP fixture user" {
			t.Errorf("Admin task owner mapping missing or incorrect: %v", item)
		}
		for _, key := range []string{"password_hash", "password", "email", "token_hash", "session_token"} {
			if _, exists := item[key]; exists {
				t.Errorf("Admin task DTO exposed sensitive/unapproved field %q", key)
			}
		}
	}
	if !foundTask {
		t.Error("Admin task list did not include the fixture task owned by another account")
	}

	// Activity is deliberately an owner-scoped task API in the current contract;
	// an Admin session must not bypass that ownership boundary.
	userActivity := f.get(t, "/api/tasks/"+f.taskID+"/activity", f.userCookie)
	if userActivity.Code != http.StatusOK {
		t.Errorf("owner activity read returned %d; want 200", userActivity.Code)
	}
	adminActivity := f.get(t, "/api/tasks/"+f.taskID+"/activity", f.adminCookie)
	if adminActivity.Code != http.StatusNotFound {
		t.Errorf("Admin read of another user's activity returned %d; want owner-scoped 404", adminActivity.Code)
	}
}

func TestAdminHTTPConcurrentAuthorizationPostgreSQL(t *testing.T) {
	f := newAdminHTTPFixture(t)
	const requests = 24
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan string, requests)
	for i := 0; i < requests; i++ {
		cookie, want := f.adminCookie, http.StatusOK
		path := "/api/admin/users"
		if i%2 == 1 {
			cookie, want = f.userCookie, http.StatusForbidden
			path = "/api/admin/tasks"
		}
		wg.Add(1)
		go func(cookie *http.Cookie, want int, path string) {
			defer wg.Done()
			<-start
			request, err := http.NewRequest(http.MethodGet, f.server.URL+path, nil)
			if err != nil {
				errCh <- "could not construct concurrent request"
				return
			}
			request.AddCookie(cookie)
			response, err := f.server.Client().Do(request)
			if err != nil {
				errCh <- "concurrent request failed"
				return
			}
			defer response.Body.Close()
			if response.StatusCode != want {
				errCh <- fmt.Sprintf("concurrent authorization returned %d; want %d", response.StatusCode, want)
			}
		}(cookie, want, path)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestAdminAccountManagementPostgreSQL(t *testing.T) {
	f := newAdminHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	roleChange := performHTTP(t, f.server, makeJSONRequest(http.MethodPatch, "/api/admin/users/"+f.userID+"/role", `{"role":"ADMIN"}`, adminHTTPTestOrigin, f.adminCookie))
	if roleChange.Code != http.StatusOK {
		t.Fatalf("promote fixture user status=%d body=%s", roleChange.Code, roleChange.Body.String())
	}
	if got := f.get(t, "/api/admin/users", f.userCookie).Code; got != http.StatusUnauthorized {
		t.Fatalf("role change did not revoke previous user session: status=%d", got)
	}
	userAdminCookie := createAdminHTTPFixtureSession(t, f, f.userID)
	if got := f.get(t, "/api/admin/users", userAdminCookie).Code; got != http.StatusOK {
		t.Fatalf("role change was not reflected in fresh session: status=%d", got)
	}
	demote := performHTTP(t, f.server, makeJSONRequest(http.MethodPatch, "/api/admin/users/"+f.userID+"/role", `{"role":"USER"}`, adminHTTPTestOrigin, f.adminCookie))
	if demote.Code != http.StatusOK {
		t.Fatalf("demote fixture user status=%d body=%s", demote.Code, demote.Body.String())
	}
	if got := f.get(t, "/api/admin/users", userAdminCookie).Code; got != http.StatusUnauthorized {
		t.Fatalf("role demotion did not revoke Admin session: status=%d", got)
	}
	selfRole := performHTTP(t, f.server, makeJSONRequest(http.MethodPatch, "/api/admin/users/"+f.adminID+"/role", `{"role":"USER"}`, adminHTTPTestOrigin, f.adminCookie))
	selfStatus := performHTTP(t, f.server, makeJSONRequest(http.MethodPatch, "/api/admin/users/"+f.adminID+"/status", `{"status":"DISABLED"}`, adminHTTPTestOrigin, f.adminCookie))
	if selfRole.Code != http.StatusForbidden || selfStatus.Code != http.StatusForbidden {
		t.Fatalf("self-management statuses role=%d status=%d; want 403", selfRole.Code, selfStatus.Code)
	}
	if _, err := f.adminPool.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("test database became unavailable: %v", err)
	}

	userCookie := createAdminHTTPFixtureSession(t, f, f.userID)
	if got := f.get(t, "/api/admin/users", userCookie).Code; got != http.StatusForbidden {
		t.Fatalf("demoted user session role=%d; want 403", got)
	}
	disable := performHTTP(t, f.server, makeJSONRequest(http.MethodPatch, "/api/admin/users/"+f.userID+"/status", `{"status":"DISABLED"}`, adminHTTPTestOrigin, f.adminCookie))
	if disable.Code != http.StatusOK {
		t.Fatalf("disable user status=%d body=%s", disable.Code, disable.Body.String())
	}
	if got := f.get(t, "/api/admin/users", userCookie).Code; got != http.StatusUnauthorized {
		t.Fatalf("disabled account session remained valid: %d", got)
	}
	deniedLogin := performHTTP(t, f.server, makeJSONRequest(http.MethodPost, "/api/auth/login", fmt.Sprintf(`{"email":%q,"password":%q}`, f.userEmail, f.userPass), adminHTTPTestOrigin, nil))
	if deniedLogin.Code != http.StatusUnauthorized {
		t.Fatalf("disabled account login status=%d; want 401", deniedLogin.Code)
	}
	enable := performHTTP(t, f.server, makeJSONRequest(http.MethodPatch, "/api/admin/users/"+f.userID+"/status", `{"status":"ACTIVE"}`, adminHTTPTestOrigin, f.adminCookie))
	if enable.Code != http.StatusOK {
		t.Fatalf("enable user status=%d body=%s", enable.Code, enable.Body.String())
	}
	fresh := createAdminHTTPFixtureSession(t, f, f.userID)
	if f.get(t, "/api/admin/users", fresh).Code != http.StatusForbidden {
		t.Fatal("re-enabled USER did not authenticate with normal USER role")
	}

	_ = createAdminHTTPFixtureSession(t, f, f.userID)
	_ = createAdminHTTPFixtureSession(t, f, f.userID)
	revoke := performHTTP(t, f.server, makeJSONRequest(http.MethodPost, "/api/admin/users/"+f.userID+"/sessions/revoke", `{}`, adminHTTPTestOrigin, f.adminCookie))
	if revoke.Code != http.StatusOK {
		t.Fatalf("session revocation status=%d body=%s", revoke.Code, revoke.Body.String())
	}
	var revocation struct {
		Revoked int64 `json:"revoked_sessions"`
	}
	if json.Unmarshal(revoke.Body.Bytes(), &revocation) != nil || revocation.Revoked < 2 {
		t.Fatalf("revocation response did not report active sessions: %s", revoke.Body.String())
	}
	if got := f.get(t, "/api/admin/users", fresh).Code; got != http.StatusUnauthorized {
		t.Fatalf("session revocation left old session valid: %d", got)
	}
	var auditCount int
	if err := f.adminPool.QueryRow(ctx, `SELECT count(*) FROM app.admin_audit_events WHERE actor_user_id=$1 OR target_user_id=$1`, f.adminID).Scan(&auditCount); err != nil || auditCount < 4 {
		t.Fatalf("audit events missing for successful mutations: count=%d err=%v", auditCount, err)
	}
	var sensitiveState bool
	if err := f.adminPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.admin_audit_events WHERE actor_user_id=$1 AND (before_state ?| ARRAY['password','password_hash','session_token','token_hash','email']))`, f.adminID).Scan(&sensitiveState); err != nil || sensitiveState {
		t.Fatalf("audit event contains a sensitive state key: exists=%t err=%v", sensitiveState, err)
	}
}

func TestAdminLastActiveAdministratorGuardPostgreSQL(t *testing.T) {
	f := newAdminHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.adminPool.Exec(ctx, `UPDATE app.users SET role='ADMIN' WHERE id=$1::uuid`, f.userID); err != nil {
		t.Fatalf("create second isolated guard fixture: %v", err)
	}
	tx, err := f.adminPool.Begin(ctx)
	if err != nil {
		t.Fatal("begin guard rollback verification")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `UPDATE app.active_admin_guard SET active_count=2 WHERE singleton`); err != nil {
		t.Fatalf("prepare rollback-only two-admin counter fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE app.users SET role='USER' WHERE id=$1::uuid`, f.adminID); err != nil {
		t.Fatalf("remove one of two active administrators in rollback-only transaction: %v", err)
	}
	_, err = tx.Exec(ctx, `UPDATE app.users SET status='DISABLED' WHERE id=$1::uuid`, f.userID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "users_last_active_admin" {
		t.Fatalf("final active administrator update error=%v, want users_last_active_admin constraint", err)
	}
	if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
		t.Fatalf("rollback guard verification: %v", rollbackErr)
	}
	var secondStatus string
	if err := f.adminPool.QueryRow(ctx, `SELECT status FROM app.users WHERE id=$1::uuid`, f.userID).Scan(&secondStatus); err != nil || secondStatus != "ACTIVE" {
		t.Fatalf("rollback-only final-admin check changed fixture: status=%s err=%v", secondStatus, err)
	}
}

func TestAdminConcurrentFinalActiveAdministratorProtectionPostgreSQL(t *testing.T) {
	f := newAdminHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.adminPool.Exec(ctx, `UPDATE app.users SET role='ADMIN' WHERE id=$1::uuid`, f.userID); err != nil {
		t.Fatalf("promote second fixture administrator: %v", err)
	}
	userAdmin := f.login(t, f.userEmail, f.userPass)
	start := make(chan struct{})
	results := make(chan int, 2)
	requests := []struct {
		cookie *http.Cookie
		target string
	}{{f.adminCookie, f.userID}, {userAdmin, f.adminID}}
	for _, item := range requests {
		go func(cookie *http.Cookie, target string) {
			<-start
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodPatch, f.server.URL+"/api/admin/users/"+target+"/role", strings.NewReader(`{"role":"USER"}`))
			if requestErr != nil {
				results <- 0
				return
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", adminHTTPTestOrigin)
			request.AddCookie(cookie)
			response, requestErr := f.server.Client().Do(request)
			if requestErr != nil {
				results <- 0
				return
			}
			defer response.Body.Close()
			results <- response.StatusCode
		}(item.cookie, item.target)
	}
	close(start)
	first, second := <-results, <-results
	if !((first == http.StatusOK && second == http.StatusForbidden) || (second == http.StatusOK && first == http.StatusForbidden)) {
		t.Fatalf("concurrent demotions returned %d and %d; want one success and one fresh-authorization denial", first, second)
	}
	var activeAdmins int
	if err := f.adminPool.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE role='ADMIN' AND status='ACTIVE'`).Scan(&activeAdmins); err != nil || int64(activeAdmins) != f.baselineActiveAdmins+1 {
		t.Fatalf("active Admin count after race=%d err=%v; want baseline plus one fixture administrator", activeAdmins, err)
	}
}

func newAdminHTTPFixture(t *testing.T) *adminHTTPFixture {
	t.Helper()
	adminURL, adminTarget := testdatabase.TestURL(t)
	runtimeURL, runtimeTarget := testdatabase.URL(t, "RUNTIME_DATABASE_URL", "")
	adminConfig, adminErr := pgx.ParseConfig(adminURL)
	runtimeConfig, runtimeErr := pgx.ParseConfig(runtimeURL)
	if adminErr != nil || runtimeErr != nil || adminTarget.Host != "<loopback>" || adminTarget.Port != 25432 || adminTarget.Database != "todo_test" || !testdatabase.SameTarget(adminTarget, runtimeTarget) || adminConfig.Host != "127.0.0.1" || adminConfig.Port != 25432 || adminConfig.Database != "todo_test" || adminConfig.User != "todo_test_admin" || runtimeConfig.Host != "127.0.0.1" || runtimeConfig.Port != 25432 || runtimeConfig.Database != "todo_test" || runtimeConfig.User != "todo_test_runtime" {
		t.Fatal("Admin HTTP PostgreSQL tests require the approved test admin/runtime roles at 127.0.0.1:25432/todo_test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runtimePool, err := database.Open(ctx, runtimeURL, 8)
	if err != nil {
		t.Fatalf("open restricted test runtime pool: %v", err)
	}
	if err := database.VerifyRuntimeRole(ctx, runtimePool, "todo_test_runtime"); err != nil {
		runtimePool.Close()
		t.Fatalf("test runtime role failed VerifyRuntimeRole: %v", err)
	}
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		runtimePool.Close()
		t.Fatalf("open test fixture administrator pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		runtimePool.Close()
		t.Fatalf("connect test fixture administrator pool: %v", err)
	}
	var dbName, adminRole, serverVersion, runtimeDB, runtimeRole string
	if err := adminPool.QueryRow(ctx, `SELECT current_database(), current_user, current_setting('server_version')`).Scan(&dbName, &adminRole, &serverVersion); err != nil {
		adminPool.Close()
		runtimePool.Close()
		t.Fatalf("verify PostgreSQL target identity: %v", err)
	}
	if err := runtimePool.QueryRow(ctx, `SELECT current_database(), current_user`).Scan(&runtimeDB, &runtimeRole); err != nil || dbName != "todo_test" || runtimeDB != "todo_test" || adminRole != "todo_test_admin" || runtimeRole != "todo_test_runtime" || !strings.HasPrefix(serverVersion, "18.6") {
		adminPool.Close()
		runtimePool.Close()
		t.Fatal("connected PostgreSQL identity did not match isolated todo_test PostgreSQL 18.6")
	}
	t.Cleanup(adminPool.Close)
	t.Cleanup(runtimePool.Close)

	adminEmail, userEmail := adminHTTPTestEmail(t), adminHTTPTestEmail(t)
	var baselineActiveAdmins int64
	if err := adminPool.QueryRow(ctx, `SELECT active_count FROM app.active_admin_guard WHERE singleton`).Scan(&baselineActiveAdmins); err != nil {
		t.Fatal("read baseline active administrator count")
	}
	password := "admin HTTP fixture password for tests"
	var ipBytes [16]byte
	if _, err := rand.Read(ipBytes[:]); err != nil {
		t.Fatal("generate isolated HTTP test client identity")
	}
	ip := net.IP(ipBytes[:]).String()
	var existing int
	if err := adminPool.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE email = ANY($1::text[])`, []string{adminEmail, userEmail}).Scan(&existing); err != nil || existing != 0 {
		t.Fatal("refusing to use a pre-existing Admin HTTP test identity")
	}
	f := &adminHTTPFixture{adminPool: adminPool, runtimePool: runtimePool, ip: ip, adminEmail: adminEmail, userEmail: userEmail, adminPass: password, userPass: password, baselineActiveAdmins: baselineActiveAdmins}
	t.Cleanup(func() { f.cleanup(t) })
	registerRateLimitTestCleanup(t, adminPool, []string{ip}, []string{adminEmail, userEmail})

	manager, err := auth.NewManager(repository.NewAuthRepository(runtimePool))
	if err != nil {
		t.Fatal("initialize test authentication manager")
	}
	adminAPI := NewAdminHandlers(admin.NewService(repository.NewAdminRepository(runtimePool), []byte("0123456789abcdef0123456789abcdef")))
	taskAPI := NewTaskHandlers(task.NewService(repository.NewTaskRepository(runtimePool), []byte("abcdef0123456789abcdef0123456789")))
	handler := NewRouter(health.NewService(runtimePool), testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(manager, "todo_session", false), Tasks: taskAPI, Admin: adminAPI,
		TrustedOrigin: adminHTTPTestOrigin,
	})
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = net.JoinHostPort(ip, "12345")
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)

	f.register(t, adminEmail, "Admin HTTP fixture administrator")
	f.register(t, userEmail, "Admin HTTP fixture user")
	var adminID string
	if err := adminPool.QueryRow(ctx, `SELECT id::text FROM app.users WHERE email=$1`, adminEmail).Scan(&adminID); err != nil {
		t.Fatal("read unique Admin fixture identity")
	}
	f.adminID = adminID
	if err := adminPool.QueryRow(ctx, `SELECT id::text FROM app.users WHERE email=$1`, userEmail).Scan(&f.userID); err != nil {
		t.Fatal("read unique USER fixture identity")
	}
	if _, err := adminPool.Exec(ctx, `UPDATE app.users SET role='ADMIN' WHERE id=$1::uuid`, adminID); err != nil {
		t.Fatal("promote only the generated Admin integration fixture")
	}
	f.adminCookie = f.login(t, adminEmail, password)
	f.userCookie = f.login(t, userEmail, password)
	f.createOwnedTask(t)
	return f
}

func (f *adminHTTPFixture) register(t *testing.T, email, name string) {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"email":%q,"password":%q}`, name, email, f.adminPass)
	result := performHTTP(t, f.server, makeJSONRequest(http.MethodPost, "/api/auth/register", body, adminHTTPTestOrigin, nil))
	if result.Code != http.StatusAccepted {
		t.Fatalf("fixture registration returned %d", result.Code)
	}
}

func (f *adminHTTPFixture) login(t *testing.T, email, password string) *http.Cookie {
	t.Helper()
	request := makeJSONRequest(http.MethodPost, "/api/auth/login", fmt.Sprintf(`{"email":%q,"password":%q}`, email, password), adminHTTPTestOrigin, nil)
	result := performHTTP(t, f.server, request)
	if result.Code != http.StatusOK {
		t.Fatalf("fixture login returned %d", result.Code)
	}
	cookies := result.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "todo_session" || cookies[0].Value == "" {
		t.Fatal("fixture login did not issue the expected session cookie")
	}
	return cookies[0]
}

func createAdminHTTPFixtureSession(t *testing.T, f *adminHTTPFixture, userID string) *http.Cookie {
	t.Helper()
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal("generate isolated fixture session")
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := f.adminPool.Exec(ctx, `INSERT INTO app.sessions(token_hash,user_id,created_at,last_activity_at,idle_expires_at,absolute_expires_at) VALUES($1,$2::uuid,$3,$3,$4,$5)`, hash[:], userID, now, now.Add(7*24*time.Hour), now.Add(30*24*time.Hour)); err != nil {
		t.Fatalf("create exact isolated fixture session: %v", err)
	}
	return &http.Cookie{Name: "todo_session", Value: token}
}

func (f *adminHTTPFixture) createOwnedTask(t *testing.T) {
	t.Helper()
	body := `{"title":"Admin HTTP fixture activity task","description":"fixture","priority":"MEDIUM"}`
	request := makeJSONRequest(http.MethodPost, "/api/tasks", body, adminHTTPTestOrigin, f.userCookie)
	result := performHTTP(t, f.server, request)
	if result.Code != http.StatusCreated {
		t.Fatalf("fixture task creation returned %d", result.Code)
	}
	var created struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(result.Body.Bytes(), &created) != nil || created.ID == "" {
		t.Fatal("fixture task response did not include an ID")
	}
	f.taskID = created.ID
}

func (f *adminHTTPFixture) get(t *testing.T, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	return performHTTP(t, f.server, request)
}

func (f *adminHTTPFixture) cleanup(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	emails := []string{f.adminEmail, f.userEmail}
	tx, err := f.adminPool.Begin(ctx)
	if err != nil {
		t.Errorf("begin Admin HTTP fixture cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var activeFixtureAdmins int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE email=ANY($1::text[]) AND role='ADMIN' AND status='ACTIVE'`, emails).Scan(&activeFixtureAdmins); err != nil {
		t.Errorf("count exact fixture administrators: %v", err)
		return
	}
	if activeFixtureAdmins > 0 {
		if _, err := tx.Exec(ctx, `UPDATE app.active_admin_guard SET active_count=$1 WHERE singleton`, f.baselineActiveAdmins+activeFixtureAdmins+1); err != nil {
			t.Errorf("prepare isolated fixture administrator cleanup: %v", err)
			return
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app.admin_audit_events WHERE actor_user_id IN (SELECT id FROM app.users WHERE email=ANY($1::text[])) OR target_user_id IN (SELECT id FROM app.users WHERE email=ANY($1::text[]))`, emails); err != nil {
		t.Errorf("clean exact Admin HTTP fixture audit events: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app.tasks WHERE user_id IN (SELECT id FROM app.users WHERE email=ANY($1::text[]))`, emails); err != nil {
		t.Errorf("clean exact Admin HTTP fixture tasks: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app.sessions WHERE user_id IN (SELECT id FROM app.users WHERE email=ANY($1::text[]))`, emails); err != nil {
		t.Errorf("clean exact Admin HTTP fixture sessions: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app.users WHERE email=ANY($1::text[])`, emails); err != nil {
		t.Errorf("clean exact Admin HTTP fixture users: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE app.active_admin_guard SET active_count=$1 WHERE singleton`, f.baselineActiveAdmins); err != nil {
		t.Errorf("restore active administrator count after fixture cleanup: %v", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit exact Admin HTTP fixture cleanup: %v", err)
		return
	}
	var users, tasks, sessions int
	if err := f.adminPool.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE email=ANY($1::text[])`, emails).Scan(&users); err != nil || users != 0 {
		t.Errorf("Admin HTTP fixture user cleanup verification failed")
	}
	if err := f.adminPool.QueryRow(ctx, `SELECT count(*) FROM app.tasks WHERE user_id IN (SELECT id FROM app.users WHERE email=ANY($1::text[]))`, emails).Scan(&tasks); err != nil || tasks != 0 {
		t.Errorf("Admin HTTP fixture task cleanup verification failed")
	}
	if err := f.adminPool.QueryRow(ctx, `SELECT count(*) FROM app.sessions s JOIN app.users u ON u.id=s.user_id WHERE u.email=ANY($1::text[])`, emails).Scan(&sessions); err != nil || sessions != 0 {
		t.Errorf("Admin HTTP fixture session cleanup verification failed")
	}
}

func adminHTTPTestEmail(t *testing.T) string {
	t.Helper()
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal("generate unique Admin HTTP test email")
	}
	return "admin-http-" + hex.EncodeToString(suffix[:]) + "@example.test"
}
