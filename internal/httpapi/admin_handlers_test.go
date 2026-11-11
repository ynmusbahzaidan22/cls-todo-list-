package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/admin"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

type fakeAdminService struct {
	users          admin.UserPage
	tasks          admin.TaskPage
	userCalls      int
	taskCalls      int
	gotLimit       int
	gotCursor      string
	gotStatus      string
	gotState       string
	userErr        error
	taskErr        error
	mutationActor  string
	mutationTarget string
	mutationValue  string
	mutationCalls  int
}

func (s *fakeAdminService) ListUsers(_ context.Context, limit int, cursor string) (admin.UserPage, error) {
	s.userCalls++
	s.gotLimit, s.gotCursor = limit, cursor
	return s.users, s.userErr
}

func (s *fakeAdminService) ListUsersForActor(ctx context.Context, _ string, limit int, cursor string) (admin.UserPage, error) {
	return s.ListUsers(ctx, limit, cursor)
}

func (s *fakeAdminService) ListTasks(_ context.Context, limit int, cursor, status, state string) (admin.TaskPage, error) {
	s.taskCalls++
	s.gotLimit, s.gotCursor, s.gotStatus, s.gotState = limit, cursor, status, state
	return s.tasks, s.taskErr
}

func (s *fakeAdminService) ChangeUserRole(_ context.Context, actor, target, role string) (admin.MutationResult, error) {
	s.mutationActor, s.mutationTarget, s.mutationValue = actor, target, role
	s.mutationCalls++
	return admin.MutationResult{User: admin.User{ID: target, Role: role, Status: admin.StatusActive}}, nil
}
func (s *fakeAdminService) ChangeUserStatus(_ context.Context, actor, target, status string) (admin.MutationResult, error) {
	s.mutationActor, s.mutationTarget, s.mutationValue = actor, target, status
	s.mutationCalls++
	return admin.MutationResult{User: admin.User{ID: target, Role: admin.RoleUser, Status: status}}, nil
}
func (s *fakeAdminService) RevokeUserSessions(_ context.Context, actor, target string) (admin.MutationResult, error) {
	s.mutationActor, s.mutationTarget, s.mutationCalls = actor, target, s.mutationCalls+1
	return admin.MutationResult{RevokedSessions: 2}, nil
}

type countingAdminRepository struct {
	userCalls int
	taskCalls int
}

func (r *countingAdminRepository) ChangeUserRole(context.Context, string, string, string) (admin.MutationResult, error) {
	return admin.MutationResult{}, nil
}
func (r *countingAdminRepository) ChangeUserStatus(context.Context, string, string, string) (admin.MutationResult, error) {
	return admin.MutationResult{}, nil
}
func (r *countingAdminRepository) RevokeUserSessions(context.Context, string, string) (admin.MutationResult, error) {
	return admin.MutationResult{}, nil
}

func (r *countingAdminRepository) ListAdminUsers(context.Context, string, *admin.Cursor, int) ([]admin.User, error) {
	r.userCalls++
	return nil, nil
}

func (r *countingAdminRepository) ListAdminTasks(context.Context, string, string, *admin.Cursor, int) ([]admin.Task, error) {
	r.taskCalls++
	return nil, nil
}

func TestAdminRoutesRequireAuthenticationAndAdmin(t *testing.T) {
	adminService := &fakeAdminService{}
	options := RouterOptions{
		Auth:  NewAuthHandlers(&fakeAuthService{}, "todo_session", false),
		Admin: NewAdminHandlers(adminService), TrustedOrigin: "http://127.0.0.1:8080",
	}
	handler := NewRouter(nil, testLogger(), t.TempDir(), options)
	for _, path := range []string{"/api/admin/users", "/api/admin/tasks"} {
		t.Run("unauthenticated "+path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
			}
		})
	}
	for _, role := range []string{"USER", "", "UNKNOWN", "admin"} {
		t.Run("forbidden role "+role, func(t *testing.T) {
			service := &fakeAuthService{user: auth.User{ID: "user-1", Role: role}}
			local := NewRouter(nil, testLogger(), t.TempDir(), RouterOptions{
				Auth: NewAuthHandlers(service, "todo_session", false), Admin: NewAdminHandlers(adminService),
			})
			for _, path := range []string{"/api/admin/users", "/api/admin/tasks"} {
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.AddCookie(&http.Cookie{Name: "todo_session", Value: "opaque-session-token"})
				result := httptest.NewRecorder()
				local.ServeHTTP(result, request)
				if result.Code != http.StatusForbidden {
					t.Fatalf("role %q path %s status=%d body=%s", role, path, result.Code, result.Body.String())
				}
			}
		})
	}
}

func TestAdminEndpointsReturnSafeDataAndOwnerIdentity(t *testing.T) {
	createdAt := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	adminService := &fakeAdminService{
		users: admin.UserPage{Items: []admin.User{{ID: "user-2", Name: "A User", Email: "a@example.test", Role: "USER", Status: "ACTIVE", CreatedAt: createdAt}}},
		tasks: admin.TaskPage{Items: []admin.Task{{Task: task.Task{ID: "task-1", Title: "Task", Status: task.StatusTODO, Priority: task.PriorityMedium, CreatedAt: createdAt, UpdatedAt: createdAt}, OwnerID: "user-2", OwnerName: "A User"}}},
	}
	authService := &fakeAuthService{user: auth.User{ID: "admin-1", Role: "ADMIN"}}
	handler := NewRouter(nil, testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(authService, "todo_session", false), Admin: NewAdminHandlers(adminService),
	})
	for _, path := range []string{"/api/admin/users?limit=1", "/api/admin/tasks?limit=1&status=TODO&state=active"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: "todo_session", Value: "opaque-session-token"})
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		if result.Code != http.StatusOK {
			t.Fatalf("path %s status=%d body=%s", path, result.Code, result.Body.String())
		}
		if strings.Contains(result.Body.String(), "password_hash") || strings.Contains(result.Body.String(), "session_token") || strings.Contains(result.Body.String(), "token_hash") {
			t.Fatalf("sensitive authentication field in response: %s", result.Body.String())
		}
	}
	var users struct {
		Items []map[string]any `json:"items"`
	}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	request.AddCookie(&http.Cookie{Name: "todo_session", Value: "opaque-session-token"})
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if err := json.Unmarshal(result.Body.Bytes(), &users); err != nil || len(users.Items) != 1 {
		t.Fatalf("decode users response: items=%v err=%v", users.Items, err)
	}
	for _, forbidden := range []string{"password_hash", "password", "token_hash", "session_token"} {
		if _, ok := users.Items[0][forbidden]; ok {
			t.Fatalf("user response contains %q", forbidden)
		}
	}
	var tasks struct {
		Items []map[string]any `json:"items"`
	}
	request = httptest.NewRequest(http.MethodGet, "/api/admin/tasks", nil)
	request.AddCookie(&http.Cookie{Name: "todo_session", Value: "opaque-session-token"})
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if err := json.Unmarshal(result.Body.Bytes(), &tasks); err != nil || len(tasks.Items) != 1 || tasks.Items[0]["owner_id"] != "user-2" || tasks.Items[0]["owner_name"] != "A User" {
		t.Fatalf("admin task owner fields = %v err=%v", tasks.Items, err)
	}
	if _, ok := tasks.Items[0]["email"]; ok {
		t.Fatal("admin task response exposed owner email")
	}
}

func TestAdminEndpointsRejectClientRoleSpoofing(t *testing.T) {
	service := &fakeAdminService{}
	handler := NewRouter(nil, testLogger(), t.TempDir(), RouterOptions{
		Auth:  NewAuthHandlers(&fakeAuthService{user: auth.User{ID: "user-1", Role: "USER"}}, "todo_session", false),
		Admin: NewAdminHandlers(service),
	})
	requests := []struct {
		name   string
		query  string
		header string
	}{
		{name: "query", query: "?role=ADMIN"},
		{name: "header", header: "ADMIN"},
		{name: "query and header", query: "?role=ADMIN", header: "ADMIN"},
	}
	for _, route := range []string{"/api/admin/users", "/api/admin/tasks"} {
		for _, spoof := range requests {
			t.Run(route+"/"+spoof.name, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodGet, route+spoof.query, nil)
				request.AddCookie(&http.Cookie{Name: "todo_session", Value: "opaque-session-token"})
				if spoof.header != "" {
					request.Header.Set("X-Role", spoof.header)
				}
				result := httptest.NewRecorder()
				handler.ServeHTTP(result, request)
				if result.Code != http.StatusForbidden {
					t.Fatalf("spoofed role returned status=%d body=%s", result.Code, result.Body.String())
				}
			})
		}
	}
	if service.userCalls != 0 || service.taskCalls != 0 {
		t.Fatalf("spoofed role reached admin service: users=%d tasks=%d", service.userCalls, service.taskCalls)
	}
}

func TestAdminMutationRoutesUseSessionActorAndOriginProtection(t *testing.T) {
	service := &fakeAdminService{}
	actorID := "11111111-1111-4111-8111-111111111111"
	targetID := "22222222-2222-4222-8222-222222222222"
	handler := NewRouter(nil, testLogger(), t.TempDir(), RouterOptions{
		Auth:  NewAuthHandlers(&fakeAuthService{user: auth.User{ID: actorID, Role: "ADMIN"}}, "todo_session", false),
		Admin: NewAdminHandlers(service), TrustedOrigin: "http://127.0.0.1:8080",
	})
	request := httptest.NewRequest(http.MethodPatch, "/api/admin/users/"+targetID+"/role", strings.NewReader(`{"role":"ADMIN","actor_id":"forged"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:8080")
	request.AddCookie(&http.Cookie{Name: "todo_session", Value: "opaque-session-token"})
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusUnprocessableEntity || service.mutationCalls != 0 {
		t.Fatalf("unknown actor field status=%d calls=%d", result.Code, service.mutationCalls)
	}
	request = httptest.NewRequest(http.MethodPatch, "/api/admin/users/"+targetID+"/role", strings.NewReader(`{"role":"ADMIN"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:8080")
	request.AddCookie(&http.Cookie{Name: "todo_session", Value: "opaque-session-token"})
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || service.mutationActor != actorID || service.mutationTarget != targetID || service.mutationValue != "ADMIN" {
		t.Fatalf("mutation did not use session actor and path target: status=%d actor=%s target=%s value=%s", result.Code, service.mutationActor, service.mutationTarget, service.mutationValue)
	}
	request = httptest.NewRequest(http.MethodPatch, "/api/admin/users/"+targetID+"/role", strings.NewReader(`{"role":"USER"}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "todo_session", Value: "opaque-session-token"})
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusForbidden || service.mutationCalls != 1 {
		t.Fatalf("missing Origin status=%d mutation calls=%d", result.Code, service.mutationCalls)
	}
}

func TestAdminListQueryValidation(t *testing.T) {
	service := &fakeAdminService{}
	handler := NewRouter(nil, testLogger(), t.TempDir(), RouterOptions{
		Auth:  NewAuthHandlers(&fakeAuthService{user: auth.User{ID: "admin-1", Role: "ADMIN"}}, "todo_session", false),
		Admin: NewAdminHandlers(service),
	})
	for _, path := range []string{
		"/api/admin/users?limit=0", "/api/admin/users?limit=-1", "/api/admin/users?limit=101", "/api/admin/users?limit=1&limit=2", "/api/admin/users?unexpected=x",
		"/api/admin/tasks?status=INVALID", "/api/admin/tasks?state=hidden", "/api/admin/tasks?cursor=x&cursor=y",
		"/api/admin/tasks?limit=0", "/api/admin/tasks?limit=-1", "/api/admin/tasks?limit=101",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: "todo_session", Value: "opaque-session-token"})
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		if result.Code != http.StatusBadRequest {
			t.Errorf("path %s status=%d body=%s", path, result.Code, result.Body.String())
		}
	}
	if service.userCalls != 0 || service.taskCalls != 0 {
		t.Fatalf("invalid queries reached service: users=%d tasks=%d", service.userCalls, service.taskCalls)
	}
	for _, test := range []struct {
		path string
		want int
	}{
		{path: "/api/admin/users", want: admin.DefaultPageSize},
		{path: "/api/admin/users?limit=100", want: 100},
		{path: "/api/admin/tasks", want: admin.DefaultPageSize},
		{path: "/api/admin/tasks?limit=100", want: 100},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.AddCookie(&http.Cookie{Name: "todo_session", Value: "opaque-session-token"})
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		if result.Code != http.StatusOK || service.gotLimit != test.want {
			t.Errorf("path %s status=%d limit=%d, want status 200 limit %d", test.path, result.Code, service.gotLimit, test.want)
		}
		if !strings.Contains(result.Body.String(), `"items":[]`) {
			t.Errorf("path %s empty results were not encoded as an empty array: %s", test.path, result.Body.String())
		}
	}
}

func TestAdminInvalidCursorReturnsGenericClientError(t *testing.T) {
	repository := &countingAdminRepository{}
	handler := NewRouter(nil, testLogger(), t.TempDir(), RouterOptions{
		Auth:  NewAuthHandlers(&fakeAuthService{user: auth.User{ID: "admin-1", Role: "ADMIN"}}, "todo_session", false),
		Admin: NewAdminHandlers(admin.NewService(repository, make([]byte, 32))),
	})
	for _, path := range []string{"/api/admin/users?cursor=broken", "/api/admin/tasks?cursor=broken"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: "todo_session", Value: "opaque-session-token"})
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "invalid_pagination") {
			t.Fatalf("path %s status=%d body=%s, want generic invalid_pagination", path, result.Code, result.Body.String())
		}
		if strings.Contains(result.Body.String(), "broken") || strings.Contains(result.Body.String(), "database") {
			t.Fatalf("path %s disclosed internal details: %s", path, result.Body.String())
		}
	}
	if repository.userCalls != 0 || repository.taskCalls != 0 {
		t.Fatalf("invalid cursor reached repository: users=%d tasks=%d", repository.userCalls, repository.taskCalls)
	}
}

func TestAdminStorageFailuresRemainGenericHTTPResponses(t *testing.T) {
	internalCause := errors.New("internal SQL query detail")
	service := &fakeAdminService{userErr: internalCause, taskErr: internalCause}
	handler := NewRouter(nil, testLogger(), t.TempDir(), RouterOptions{
		Auth:  NewAuthHandlers(&fakeAuthService{user: auth.User{ID: "admin-1", Role: "ADMIN"}}, "todo_session", false),
		Admin: NewAdminHandlers(service),
	})
	for _, path := range []string{"/api/admin/users", "/api/admin/tasks"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: "todo_session", Value: "opaque-session-token"})
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		if result.Code != http.StatusInternalServerError || !strings.Contains(result.Body.String(), "internal_error") {
			t.Fatalf("path %s status=%d body=%s, want generic 500", path, result.Code, result.Body.String())
		}
		if strings.Contains(result.Body.String(), internalCause.Error()) || strings.Contains(result.Body.String(), "SQL") {
			t.Fatalf("path %s disclosed storage error: %s", path, result.Body.String())
		}
	}
}
