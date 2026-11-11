package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type memoryRepository struct {
	accounts       map[string]Account
	sessions       map[string]User
	lastHash       []byte
	rateLimits     map[string]int
	rateLimitError error
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{accounts: make(map[string]Account), sessions: make(map[string]User), rateLimits: make(map[string]int)}
}

func (r *memoryRepository) AllowRateLimit(_ context.Context, key string, max int, _ time.Duration) (bool, error) {
	if r.rateLimitError != nil {
		return false, r.rateLimitError
	}
	r.rateLimits[key]++
	return r.rateLimits[key] <= max, nil
}

func TestManagerFailsClosedWhenSharedRateLimitStorageIsUnavailable(t *testing.T) {
	repository := newMemoryRepository()
	repository.rateLimitError = errors.New("database unavailable")
	manager, err := NewManager(repository)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	if _, err := manager.Register(context.Background(), "Person", "person@example.test", "a secure example password", "127.0.0.1"); !errors.Is(err, ErrStorage) {
		t.Fatalf("registration when limiter storage is unavailable = %v, want ErrStorage", err)
	}
	if len(repository.accounts) != 0 {
		t.Fatal("registration continued despite unavailable shared rate limiter")
	}
	if _, _, _, err := manager.Login(context.Background(), "person@example.test", "a secure example password", "127.0.0.1"); !errors.Is(err, ErrStorage) {
		t.Fatalf("login when limiter storage is unavailable = %v, want ErrStorage", err)
	}
}

func (r *memoryRepository) CreateAccount(_ context.Context, name, email, passwordHash string) (User, error) {
	if _, exists := r.accounts[email]; exists {
		return User{}, ErrEmailTaken
	}
	user := User{ID: "user-1", Name: name, Email: email, Role: "USER", CreatedAt: time.Now().UTC()}
	r.accounts[email] = Account{User: user, PasswordHash: passwordHash, Status: "ACTIVE"}
	return user, nil
}

func (r *memoryRepository) FindAccountByEmail(_ context.Context, email string) (Account, error) {
	account, exists := r.accounts[email]
	if !exists {
		return Account{}, ErrAccountNotFound
	}
	return account, nil
}

func (r *memoryRepository) CreateSession(_ context.Context, tokenHash []byte, userID string, _, _, _ time.Time) error {
	r.lastHash = append([]byte(nil), tokenHash...)
	r.sessions[string(tokenHash)] = User{ID: userID, Name: "Test User", Email: "person@example.test", Role: "USER", CreatedAt: time.Now().UTC()}
	return nil
}

func (r *memoryRepository) AuthenticateAndTouchSession(_ context.Context, tokenHash []byte, _, _ time.Time) (User, error) {
	user, exists := r.sessions[string(tokenHash)]
	if !exists {
		return User{}, ErrUnauthenticated
	}
	return user, nil
}

func (r *memoryRepository) RevokeSession(_ context.Context, tokenHash []byte, _ time.Time) error {
	delete(r.sessions, string(tokenHash))
	return nil
}

func TestRegisterLoginAuthenticateAndLogout(t *testing.T) {
	repository := newMemoryRepository()
	manager, err := NewManager(repository)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	ctx := context.Background()
	user, err := manager.Register(ctx, "  Display Name  ", "  Person@Example.Test ", "a secure example password", "127.0.0.1")
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if user.Email != "person@example.test" {
		t.Fatalf("email was not normalized: %q", user.Email)
	}
	if user.Name != "Display Name" || user.Role != "USER" {
		t.Fatalf("registration did not normalize name and assign USER role: %+v", user)
	}
	if _, err := manager.Register(ctx, "Duplicate Name", "person@example.test", "a secure example password", "127.0.0.1"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate registration error = %v, want ErrEmailTaken", err)
	}
	loggedIn, token, expiry, err := manager.Login(ctx, "PERSON@example.test", "a secure example password", "127.0.0.1")
	if err != nil {
		t.Fatalf("Login returned error: %v", err)
	}
	if token == "" || !expiry.After(time.Now()) || loggedIn.Email != user.Email || loggedIn.Name != user.Name || loggedIn.Role != "USER" {
		t.Fatalf("unexpected login result: user=%+v expiry=%v token-empty=%v", loggedIn, expiry, token == "")
	}
	if len(repository.lastHash) != 32 || string(repository.lastHash) == token {
		t.Fatal("session repository did not receive a 32-byte token hash")
	}
	if _, err := manager.Authenticate(ctx, token); err != nil {
		t.Fatalf("Authenticate returned error: %v", err)
	}
	if err := manager.Logout(ctx, token); err != nil {
		t.Fatalf("Logout returned error: %v", err)
	}
	if _, err := manager.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("authentication after logout error = %v, want ErrUnauthenticated", err)
	}
}

func TestLoginDoesNotRevealUnknownEmail(t *testing.T) {
	manager, err := NewManager(newMemoryRepository())
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	_, _, _, err = manager.Login(context.Background(), "missing@example.test", "a secure example password", "127.0.0.1")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown account login error = %v, want ErrInvalidCredentials", err)
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	manager, err := NewManager(newMemoryRepository())
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	if _, err := manager.Register(context.Background(), "Person", "not-an-email", "a secure example password", "127.0.0.1"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid email error = %v, want ErrInvalidInput", err)
	}
	if _, err := manager.Register(context.Background(), "Person", "valid@example.test", "short", "127.0.0.1"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("short password error = %v, want ErrInvalidInput", err)
	}
}

func TestRegisterValidatesAndTrimsName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "minimum one character", input: "A"},
		{name: "maximum one hundred characters", input: strings.Repeat("a", 100)},
		{name: "empty", input: "", wantErr: true},
		{name: "whitespace only", input: " \t\n ", wantErr: true},
		{name: "over one hundred characters", input: strings.Repeat("a", 101), wantErr: true},
		{name: "unicode counts as characters", input: strings.Repeat("界", 100)},
		{name: "unicode over character limit", input: strings.Repeat("界", 101), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, err := NewManager(newMemoryRepository())
			if err != nil {
				t.Fatalf("NewManager returned error: %v", err)
			}
			_, registerErr := manager.Register(context.Background(), test.input, "valid@example.test", "a secure example password", "127.0.0.1")
			if test.wantErr && !errors.Is(registerErr, ErrInvalidInput) {
				t.Fatalf("Register error = %v, want ErrInvalidInput", registerErr)
			}
			if !test.wantErr && registerErr != nil {
				t.Fatalf("Register returned error: %v", registerErr)
			}
		})
	}
}
