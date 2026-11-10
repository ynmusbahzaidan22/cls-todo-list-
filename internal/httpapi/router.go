package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

type readinessChecker interface {
	DatabaseReady(context.Context) bool
}

type response struct {
	Status   string `json:"status"`
	Database string `json:"database,omitempty"`
}

type RouterOptions struct {
	Auth              *AuthHandlers
	Tasks             *TaskHandlers
	Templates         *TemplateHandlers
	Preferences       *PreferenceHandlers
	TrustedOrigin     string
	TrustedProxyCIDRs []string
	Production        bool
}

// NewRouter registers health, local authentication, and the explicitly allowed frontend files.
func NewRouter(checker readinessChecker, logger *slog.Logger, frontendDir string, options ...RouterOptions) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", serveFrontendFile(frontendDir, "index.html"))
	mux.HandleFunc("GET /index.html", serveFrontendFile(frontendDir, "index.html"))
	mux.HandleFunc("GET /style.css", serveFrontendFile(frontendDir, "style.css"))
	mux.HandleFunc("GET /script.js", serveFrontendFile(frontendDir, "script.js"))
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, response{Status: "ok"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if checker == nil || !checker.DatabaseReady(ctx) {
			writeJSON(w, http.StatusServiceUnavailable, response{Status: "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, response{Status: "ok", Database: "ready"})
	})
	if len(options) > 0 && options[0].Auth != nil {
		handlers := options[0].Auth
		mux.HandleFunc("POST /api/auth/register", handlers.register)
		mux.HandleFunc("POST /api/auth/login", handlers.login)
		mux.Handle("GET /api/auth/me", handlers.requireAuthentication(http.HandlerFunc(handlers.me)))
		mux.HandleFunc("POST /api/auth/logout", handlers.logout)
		if options[0].Preferences != nil {
			preferences := options[0].Preferences
			mux.Handle("GET /api/preferences", handlers.requireAuthentication(http.HandlerFunc(preferences.get)))
			mux.Handle("PUT /api/preferences", handlers.requireAuthentication(http.HandlerFunc(preferences.update)))
		}
		if options[0].Tasks != nil {
			tasks := options[0].Tasks
			mux.Handle("POST /api/tasks/bulk", handlers.requireAuthentication(http.HandlerFunc(tasks.bulk)))
			mux.Handle("POST /api/tasks", handlers.requireAuthentication(http.HandlerFunc(tasks.create)))
			mux.Handle("GET /api/tasks", handlers.requireAuthentication(http.HandlerFunc(tasks.list)))
			mux.Handle("GET /api/tasks/deleted/count", handlers.requireAuthentication(http.HandlerFunc(tasks.deletedCount)))
			mux.Handle("GET /api/tasks/deleted", handlers.requireAuthentication(http.HandlerFunc(tasks.listDeleted)))
			mux.Handle("GET /api/tasks/completed", handlers.requireAuthentication(http.HandlerFunc(tasks.listCompleted)))
			mux.Handle("GET /api/tasks/{taskID}", handlers.requireAuthentication(http.HandlerFunc(tasks.get)))
			mux.Handle("GET /api/tasks/{taskID}/activity", handlers.requireAuthentication(http.HandlerFunc(tasks.activity)))
			mux.Handle("POST /api/tasks/{taskID}/activity/{eventID}/undo", handlers.requireAuthentication(http.HandlerFunc(tasks.undoActivity)))
			mux.Handle("PUT /api/tasks/{taskID}", handlers.requireAuthentication(http.HandlerFunc(tasks.update)))
			mux.Handle("DELETE /api/tasks/{taskID}", handlers.requireAuthentication(http.HandlerFunc(tasks.delete)))
			mux.Handle("POST /api/tasks/{taskID}/restore", handlers.requireAuthentication(http.HandlerFunc(tasks.restore)))
		}
		if options[0].Templates != nil {
			templates := options[0].Templates
			mux.Handle("GET /api/task-templates", handlers.requireAuthentication(http.HandlerFunc(templates.list)))
			mux.Handle("POST /api/task-templates", handlers.requireAuthentication(http.HandlerFunc(templates.create)))
			mux.Handle("GET /api/task-templates/{templateID}", handlers.requireAuthentication(http.HandlerFunc(templates.get)))
			mux.Handle("PUT /api/task-templates/{templateID}", handlers.requireAuthentication(http.HandlerFunc(templates.update)))
			mux.Handle("DELETE /api/task-templates/{templateID}", handlers.requireAuthentication(http.HandlerFunc(templates.delete)))
			mux.Handle("POST /api/task-templates/{templateID}/apply", handlers.requireAuthentication(http.HandlerFunc(templates.apply)))
		}
	}

	var handler http.Handler = mux
	if len(options) > 0 {
		handler = trustedProxyContext(options[0].TrustedProxyCIDRs, handler)
		if options[0].Production {
			handler = enforceHost(options[0].TrustedOrigin, handler)
		}
		if options[0].Production {
			handler = securityHeaders(true, handler)
		} else {
			handler = securityHeaders(false, handler)
		}
	}
	if len(options) > 0 && options[0].TrustedOrigin != "" {
		handler = enforceTrustedOrigin(options[0].TrustedOrigin, handler)
	}
	handler = recoverPanics(handler, logger)
	return requestLogger(handler, logger)
}

type trustedProxyContextKey struct{}

func trustedProxyContext(cidrs []string, next http.Handler) http.Handler {
	var trusted []*net.IPNet
	for _, raw := range cidrs {
		_, network, err := net.ParseCIDR(raw)
		if err == nil {
			trusted = append(trusted, network)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), trustedProxyContextKey{}, trusted)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func enforceHost(origin string, next http.Handler) http.Handler {
	expected, _ := url.Parse(origin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/live" && r.URL.Path != "/health/ready" && !strings.EqualFold(r.Host, expected.Host) {
			writeJSONStatus(w, http.StatusBadRequest, apiError{Error: errorBody{Code: "invalid_host", Message: "Request host is not allowed."}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(production bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if production {
			header.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func enforceTrustedOrigin(expected string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if !sameOrigin(r.Header.Get("Origin"), expected) {
				writeJSONStatus(w, http.StatusForbidden, apiError{Error: errorBody{Code: "origin_rejected", Message: "Request origin is not allowed."}})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(actual, expected string) bool {
	actualURL, actualErr := url.Parse(actual)
	expectedURL, expectedErr := url.Parse(expected)
	if actualErr != nil || expectedErr != nil || actualURL.Scheme == "" || actualURL.Host == "" {
		return false
	}
	if actualURL.Path != "" || actualURL.RawQuery != "" || actualURL.Fragment != "" || actualURL.User != nil {
		return false
	}
	return strings.EqualFold(actualURL.Scheme, expectedURL.Scheme) && strings.EqualFold(actualURL.Host, expectedURL.Host)
}

func serveFrontendFile(frontendDir, filename string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The app is served locally and frontend assets change during development.
		// Prevent a browser from keeping an old CSS or JavaScript response after reload.
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, filepath.Join(frontendDir, filename))
	}
}

func writeJSON(w http.ResponseWriter, status int, value response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func requestLogger(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var requestID [16]byte
		if _, err := rand.Read(requestID[:]); err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, apiError{Error: errorBody{Code: "internal_error", Message: "The request could not be completed."}})
			return
		}
		id := hex.EncodeToString(requestID[:])
		w.Header().Set("X-Request-ID", id)
		started := time.Now()
		wrapped := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(wrapped, r)
		status := wrapped.status
		if status == 0 {
			status = http.StatusOK
		}
		logger.Info("HTTP request", "request_id", id, "method", r.Method, "path", r.URL.Path, "status", status, "duration", time.Since(started))
	})
}

func recoverPanics(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("HTTP handler panic recovered", "panic_type", fmt.Sprintf("%T", recovered))
				writeJSON(w, http.StatusInternalServerError, response{Status: "internal error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
