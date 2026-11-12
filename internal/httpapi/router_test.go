package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type checkerFunc func(context.Context) bool

func (f checkerFunc) DatabaseReady(ctx context.Context) bool { return f(ctx) }

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestHealthEndpoints(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		checker    readinessChecker
		wantStatus int
		wantBody   string
	}{
		{name: "liveness independent from database", path: "/health/live", checker: checkerFunc(func(context.Context) bool { return false }), wantStatus: http.StatusOK, wantBody: `{"status":"ok"}`},
		{name: "database ready", path: "/health/ready", checker: checkerFunc(func(context.Context) bool { return true }), wantStatus: http.StatusOK, wantBody: `{"status":"ok","database":"ready"}`},
		{name: "database unavailable", path: "/health/ready", checker: checkerFunc(func(context.Context) bool { return false }), wantStatus: http.StatusServiceUnavailable, wantBody: `{"status":"unavailable"}`},
		{name: "missing readiness dependency", path: "/health/ready", wantStatus: http.StatusServiceUnavailable, wantBody: `{"status":"unavailable"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			result := httptest.NewRecorder()
			NewRouter(test.checker, testLogger(), t.TempDir()).ServeHTTP(result, request)
			if result.Code != test.wantStatus || strings.TrimSpace(result.Body.String()) != test.wantBody {
				t.Fatalf("got status %d body %q, want status %d body %q", result.Code, result.Body.String(), test.wantStatus, test.wantBody)
			}
		})
	}
}

func TestTrustedProxyHeadersAndSecurityResponseHeaders(t *testing.T) {
	proxyNetwork, _, _ := net.ParseCIDR("10.0.0.0/8")
	trusted := trustedProxyContext([]string{"10.0.0.0/8"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := clientIP(r); got != "198.51.100.7" {
			t.Errorf("trusted proxy client IP = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.0.0.2:443"
	request.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	result := httptest.NewRecorder()
	trusted.ServeHTTP(result, request)
	if result.Code != http.StatusNoContent || proxyNetwork == nil {
		t.Fatalf("trusted proxy request status=%d", result.Code)
	}

	requestIDHandler := requestLogger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), testLogger())
	requestIDResult := httptest.NewRecorder()
	requestIDHandler.ServeHTTP(requestIDResult, httptest.NewRequest(http.MethodGet, "/", nil))
	if len(requestIDResult.Header().Get("X-Request-ID")) != 32 {
		t.Fatalf("request correlation ID header missing or malformed: %q", requestIDResult.Header().Get("X-Request-ID"))
	}

	untrusted := httptest.NewRequest(http.MethodGet, "/", nil)
	untrusted.RemoteAddr = "192.0.2.10:1234"
	untrusted.Header.Set("X-Forwarded-For", "203.0.113.99")
	if got := clientIP(untrusted); got != "192.0.2.10" {
		t.Fatalf("untrusted forwarded header changed client IP: %q", got)
	}

	secured := securityHeaders(false, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	securedResult := httptest.NewRecorder()
	secured.ServeHTTP(securedResult, httptest.NewRequest(http.MethodGet, "/", nil))
	for name, want := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "strict-origin-when-cross-origin"} {
		if got := securedResult.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if securedResult.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("development response unexpectedly enables HSTS")
	}
}

func TestRenderProxyClientIPUsesSingleVerifiedProviderHeader(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.0.0.2:443"
	request.Header.Set("CF-Connecting-IP", "198.51.100.7")
	request.Header.Set("X-Forwarded-For", "203.0.113.99")
	wrapped := trustedProxyContextWithProvider("render", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := clientIP(r); got != "198.51.100.7" {
			t.Errorf("Render client IP = %q, want provider client address", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	result := httptest.NewRecorder()
	wrapped.ServeHTTP(result, request)
	if result.Code != http.StatusNoContent {
		t.Fatalf("Render proxy request status = %d", result.Code)
	}
}

func TestRenderProxyMalformedForwardedHeadersFallBackToPeer(t *testing.T) {
	tests := []struct {
		name   string
		values []string
	}{
		{name: "missing"},
		{name: "invalid address", values: []string{"not-an-ip"}},
		{name: "comma-separated chain", values: []string{"198.51.100.7, 10.0.0.1"}},
		{name: "duplicate header values", values: []string{"198.51.100.7", "203.0.113.9"}},
		{name: "empty address", values: []string{" "}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.RemoteAddr = "10.0.0.2:443"
			for _, value := range tt.values {
				request.Header.Add("CF-Connecting-IP", value)
			}
			// X-Forwarded-For is never used as a fallback in Render mode.
			request.Header.Set("X-Forwarded-For", "192.0.2.99")
			if got := clientIP(request.WithContext(context.WithValue(request.Context(), trustedProxyContextKey{}, trustedProxyContextValue{provider: "render"}))); got != "10.0.0.2" {
				t.Fatalf("malformed Render forwarded IP resolved to %q, want socket peer", got)
			}
		})
	}
}

func TestProductionRejectsUnexpectedHostAndEnablesHSTS(t *testing.T) {
	handler := NewRouter(nil, testLogger(), t.TempDir(), RouterOptions{TrustedOrigin: "https://todo.example.test", Production: true})
	request := httptest.NewRequest(http.MethodGet, "https://attacker.example/", nil)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusBadRequest {
		t.Fatalf("unexpected Host status = %d", result.Code)
	}
	if result.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("production response did not include HSTS")
	}
}

func TestFrontendRoutesServeOnlyAllowlistedFiles(t *testing.T) {
	frontendDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(frontendDir, "index.html"), []byte("<main>todo</main>"), 0o600); err != nil {
		t.Fatalf("write test frontend: %v", err)
	}
	if err := os.WriteFile(filepath.Join(frontendDir, ".env"), []byte("must stay private"), 0o600); err != nil {
		t.Fatalf("write private test file: %v", err)
	}
	handler := NewRouter(nil, testLogger(), frontendDir)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || strings.TrimSpace(page.Body.String()) != "<main>todo</main>" {
		t.Fatalf("unexpected frontend response: status %d body %q", page.Code, page.Body.String())
	}

	privateFile := httptest.NewRecorder()
	handler.ServeHTTP(privateFile, httptest.NewRequest(http.MethodGet, "/.env", nil))
	if privateFile.Code != http.StatusNotFound || strings.Contains(privateFile.Body.String(), "must stay private") {
		t.Fatalf("private file was exposed: status %d body %q", privateFile.Code, privateFile.Body.String())
	}
}

func TestLoggerOmitsQueryParameters(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	request := httptest.NewRequest(http.MethodGet, "/health/live?token=secret-value", nil)
	NewRouter(nil, logger, t.TempDir()).ServeHTTP(httptest.NewRecorder(), request)
	if strings.Contains(logs.String(), "secret-value") || !strings.Contains(logs.String(), "/health/live") {
		t.Fatalf("unexpected request log: %s", logs.String())
	}
}
