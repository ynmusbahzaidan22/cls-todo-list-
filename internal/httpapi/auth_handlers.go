package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
)

const maxAuthBodyBytes = 16 * 1024

type AuthService interface {
	Register(context.Context, string, string, string) (auth.User, error)
	Login(context.Context, string, string, string) (auth.User, string, time.Time, error)
	Authenticate(context.Context, string) (auth.User, error)
	Logout(context.Context, string) error
}

type AuthHandlers struct {
	service      AuthService
	cookieName   string
	cookieSecure bool
}

func NewAuthHandlers(service AuthService, cookieName string, cookieSecure bool) *AuthHandlers {
	return &AuthHandlers{service: service, cookieName: cookieName, cookieSecure: cookieSecure}
}

type publicUser struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

type authInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type apiError struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type userContextKey struct{}

func (h *AuthHandlers) register(w http.ResponseWriter, r *http.Request) {
	var input authInput
	if !decodeAuthInput(w, r, &input) {
		return
	}
	_, err := h.service.Register(r.Context(), input.Email, input.Password, clientIP(r))
	if err != nil && !errors.Is(err, auth.ErrEmailTaken) {
		writeAuthError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]string{
		"status":  "accepted",
		"message": "If an account can be created, you can now sign in.",
	})
}

func (h *AuthHandlers) login(w http.ResponseWriter, r *http.Request) {
	var input authInput
	if !decodeAuthInput(w, r, &input) {
		return
	}
	user, token, expiresAt, err := h.service.Login(r.Context(), input.Email, input.Password, clientIP(r))
	if err != nil {
		writeAuthError(w, err)
		return
	}
	setSessionCookie(w, h.cookieName, token, expiresAt, h.cookieSecure)
	writeJSONStatus(w, http.StatusOK, publicUser{ID: user.ID, Email: user.Email, CreatedAt: user.CreatedAt})
}

func (h *AuthHandlers) me(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(userContextKey{}).(auth.User)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	writeJSONStatus(w, http.StatusOK, publicUser{ID: user.ID, Email: user.Email, CreatedAt: user.CreatedAt})
}

func (h *AuthHandlers) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(h.cookieName); err == nil {
		if err := h.service.Logout(r.Context(), cookie.Value); err != nil {
			clearSessionCookie(w, h.cookieName, h.cookieSecure)
			writeAuthError(w, err)
			return
		}
	}
	clearSessionCookie(w, h.cookieName, h.cookieSecure)
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandlers) requireAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(h.cookieName)
		if err != nil || cookie.Value == "" {
			writeAuthError(w, auth.ErrUnauthenticated)
			return
		}
		user, err := h.service.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			writeAuthError(w, err)
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey{}, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func decodeAuthInput(w http.ResponseWriter, r *http.Request, destination *authInput) bool {
	mediaType, _, mediaTypeErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaTypeErr != nil || !strings.EqualFold(mediaType, "application/json") {
		writeAuthError(w, errUnsupportedMediaType)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeAuthError(w, errRequestTooLarge)
		} else {
			writeAuthError(w, auth.ErrInvalidInput)
		}
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAuthError(w, auth.ErrInvalidInput)
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "unknown"
	}
	peerIP := net.ParseIP(host)
	if peerIP == nil {
		return "unknown"
	}
	trusted, _ := r.Context().Value(trustedProxyContextKey{}).([]*net.IPNet)
	if !containsIP(trusted, peerIP) {
		return peerIP.String()
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if len(forwarded) == 1 && strings.TrimSpace(forwarded[0]) == "" {
		return peerIP.String()
	}
	chain := make([]net.IP, 0, len(forwarded))
	for _, item := range forwarded {
		ip := net.ParseIP(strings.TrimSpace(item))
		if ip == nil {
			return peerIP.String()
		}
		chain = append(chain, ip)
	}
	for index := len(chain) - 1; index >= 0; index-- {
		if !containsIP(trusted, peerIP) {
			break
		}
		peerIP = chain[index]
	}
	return peerIP.String()
}

func containsIP(cidrs []*net.IPNet, ip net.IP) bool {
	for _, cidr := range cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func setSessionCookie(w http.ResponseWriter, name, token string, expiresAt time.Time, secure bool) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

var (
	errUnsupportedMediaType = errors.New("unsupported media type")
	errRequestTooLarge      = errors.New("request body too large")
)

func writeAuthError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code, message := "internal_error", "The request could not be completed."
	switch {
	case errors.Is(err, auth.ErrInvalidInput):
		status, code, message = http.StatusUnprocessableEntity, "invalid_request", "The request is invalid."
	case errors.Is(err, auth.ErrInvalidCredentials):
		status, code, message = http.StatusUnauthorized, "unauthorized", "Email or password is incorrect."
	case errors.Is(err, auth.ErrUnauthenticated):
		status, code, message = http.StatusUnauthorized, "unauthenticated", "Authentication is required."
	case errors.Is(err, auth.ErrRateLimited):
		status, code, message = http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again later."
	case errors.Is(err, errUnsupportedMediaType):
		status, code, message = http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json."
	case errors.Is(err, errRequestTooLarge):
		status, code, message = http.StatusRequestEntityTooLarge, "request_too_large", "The request body is too large."
	}
	writeJSONStatus(w, status, apiError{Error: errorBody{Code: code, Message: message}})
}

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
