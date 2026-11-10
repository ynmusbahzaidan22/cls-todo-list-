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

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
)

type fakeAuthService struct {
	user       auth.User
	registered bool
	loggedIn   bool
	loggedOut  bool
}

func (s *fakeAuthService) Register(_ context.Context, email, _, _ string) (auth.User, error) {
	s.registered = true
	s.user = auth.User{ID: "user-123", Email: strings.ToLower(email), CreatedAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
	return s.user, nil
}

func (s *fakeAuthService) Login(context.Context, string, string, string) (auth.User, string, time.Time, error) {
	s.loggedIn = true
	s.user = auth.User{ID: "user-123", Email: "person@example.test", CreatedAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
	return s.user, "opaque-session-token", time.Now().Add(time.Hour), nil
}

func (s *fakeAuthService) Authenticate(_ context.Context, token string) (auth.User, error) {
	if token != "opaque-session-token" || s.loggedOut {
		return auth.User{}, auth.ErrUnauthenticated
	}
	return s.user, nil
}

func (s *fakeAuthService) Logout(_ context.Context, token string) error {
	if token == "opaque-session-token" {
		s.loggedOut = true
	}
	return nil
}

func TestAuthenticationHTTPFlowAndCookiePolicy(t *testing.T) {
	service := &fakeAuthService{}
	options := RouterOptions{Auth: NewAuthHandlers(service, "todo_session", false), TrustedOrigin: "http://127.0.0.1:8080"}
	handler := NewRouter(nil, testLogger(), t.TempDir(), options)

	register := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"email":"Person@Example.Test","password":"a secure example password"}`))
	register.Header.Set("Content-Type", "application/json")
	register.Header.Set("Origin", options.TrustedOrigin)
	registerResult := httptest.NewRecorder()
	handler.ServeHTTP(registerResult, register)
	if registerResult.Code != http.StatusAccepted || !service.registered || strings.Contains(registerResult.Body.String(), "password_hash") || !strings.Contains(registerResult.Body.String(), "If an account can be created") {
		t.Fatalf("unexpected registration response: status=%d body=%s", registerResult.Code, registerResult.Body.String())
	}

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if unauthenticated.Code != http.StatusUnauthorized || !strings.Contains(unauthenticated.Body.String(), "Authentication is required") {
		t.Fatalf("unexpected unauthenticated response: status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}

	login := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"person@example.test","password":"a secure example password"}`))
	login.Header.Set("Content-Type", "application/json; charset=utf-8")
	login.Header.Set("Origin", options.TrustedOrigin)
	loginResult := httptest.NewRecorder()
	handler.ServeHTTP(loginResult, login)
	if loginResult.Code != http.StatusOK || !service.loggedIn {
		t.Fatalf("unexpected login response: status=%d body=%s", loginResult.Code, loginResult.Body.String())
	}
	cookies := loginResult.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/" || cookies[0].Value != "opaque-session-token" {
		t.Fatalf("session cookie has unexpected policy: %+v", cookies)
	}

	me := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	me.AddCookie(cookies[0])
	meResult := httptest.NewRecorder()
	handler.ServeHTTP(meResult, me)
	var body map[string]any
	if meResult.Code != http.StatusOK || json.Unmarshal(meResult.Body.Bytes(), &body) != nil || body["email"] != "person@example.test" {
		t.Fatalf("unexpected current-user response: status=%d body=%s", meResult.Code, meResult.Body.String())
	}

	logout := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logout.Header.Set("Origin", options.TrustedOrigin)
	logout.AddCookie(cookies[0])
	logoutResult := httptest.NewRecorder()
	handler.ServeHTTP(logoutResult, logout)
	if logoutResult.Code != http.StatusNoContent || !service.loggedOut {
		t.Fatalf("unexpected logout response: status=%d body=%s", logoutResult.Code, logoutResult.Body.String())
	}
	if cleared := logoutResult.Result().Cookies(); len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Fatalf("logout did not clear session cookie: %+v", cleared)
	}
}

func TestRegistrationDoesNotRevealExistingAccount(t *testing.T) {
	service := &fakeAuthService{}
	options := RouterOptions{Auth: NewAuthHandlers(service, "todo_session", false), TrustedOrigin: "http://127.0.0.1:8080"}
	handler := NewRouter(nil, testLogger(), t.TempDir(), options)
	makeRequest := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"email":"Person@Example.Test","password":"a secure example password"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", options.TrustedOrigin)
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, request)
		return result
	}
	first := makeRequest()
	serviceWithDuplicate := &authErrorService{fakeAuthService: service, err: auth.ErrEmailTaken}
	duplicateHandler := NewRouter(nil, testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(serviceWithDuplicate, "todo_session", false), TrustedOrigin: options.TrustedOrigin,
	})
	duplicateRequest := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"email":"Person@Example.Test","password":"a secure example password"}`))
	duplicateRequest.Header.Set("Content-Type", "application/json")
	duplicateRequest.Header.Set("Origin", options.TrustedOrigin)
	second := httptest.NewRecorder()
	duplicateHandler.ServeHTTP(second, duplicateRequest)
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted || first.Body.String() != second.Body.String() {
		t.Fatalf("registration responses reveal account state: first=%d %s second=%d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
}

func TestAuthHTTPRejectsUntrustedOriginInvalidJSONAndLargeBody(t *testing.T) {
	service := &fakeAuthService{}
	handler := NewRouter(nil, testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(service, "todo_session", false), TrustedOrigin: "http://127.0.0.1:8080",
	})
	tests := []struct {
		name        string
		body        string
		contentType string
		origin      string
		wantStatus  int
	}{
		{name: "untrusted origin", body: `{}`, contentType: "application/json", origin: "https://attacker.example", wantStatus: http.StatusForbidden},
		{name: "client-supplied identity", body: `{"email":"person@example.test","password":"a secure example password","user_id":"someone-else"}`, contentType: "application/json", origin: "http://127.0.0.1:8080", wantStatus: http.StatusUnprocessableEntity},
		{name: "false JSON media type", body: `{}`, contentType: "application/jsonx", origin: "http://127.0.0.1:8080", wantStatus: http.StatusUnsupportedMediaType},
		{name: "oversized body", body: `{"email":"` + strings.Repeat("x", maxAuthBodyBytes) + `"}`, contentType: "application/json", origin: "http://127.0.0.1:8080", wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("Origin", test.origin)
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", result.Code, test.wantStatus, result.Body.String())
			}
		})
	}
}

func TestAuthErrorsDoNotExposeStorageDetails(t *testing.T) {
	service := &authErrorService{fakeAuthService: &fakeAuthService{}, err: errors.New("password=database-secret")}
	handler := NewRouter(nil, testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(service, "todo_session", false), TrustedOrigin: "http://127.0.0.1:8080",
	})
	request := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"email":"person@example.test","password":"a secure example password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:8080")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusInternalServerError || strings.Contains(result.Body.String(), "database-secret") {
		t.Fatalf("storage details leaked: status=%d body=%s", result.Code, result.Body.String())
	}
}

type authErrorService struct {
	*fakeAuthService
	err error
}

func (s *authErrorService) Register(context.Context, string, string, string) (auth.User, error) {
	return auth.User{}, s.err
}
