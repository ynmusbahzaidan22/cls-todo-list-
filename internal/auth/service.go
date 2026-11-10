package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

const (
	sessionIdleLifetime     = 7 * 24 * time.Hour
	sessionAbsoluteLifetime = 30 * 24 * time.Hour
	loginWindow             = 15 * time.Minute
	registrationWindow      = time.Hour
)

type Manager struct {
	repository        Repository
	dummyPasswordHash string
	kdfSlots          chan struct{}
}

func NewManager(repository Repository) (*Manager, error) {
	dummyHash, err := hashPassword("not-a-real-user-password")
	if err != nil {
		return nil, fmt.Errorf("initialize password verifier: %w", err)
	}
	return &Manager{
		repository:        repository,
		dummyPasswordHash: dummyHash,
		kdfSlots:          make(chan struct{}, 4),
	}, nil
}

func (m *Manager) Register(ctx context.Context, rawEmail, password, remoteIP string) (User, error) {
	allowed, err := m.repository.AllowRateLimit(ctx, "register:ip:"+remoteIP, 50, registrationWindow)
	if err != nil {
		return User{}, fmt.Errorf("check registration rate limit: %w", ErrStorage)
	}
	if !allowed {
		return User{}, ErrRateLimited
	}
	email, err := normalizeEmail(rawEmail)
	if err != nil || validatePassword(password) != nil {
		return User{}, ErrInvalidInput
	}
	var encoded string
	err = m.withKDF(ctx, func() error {
		var hashErr error
		encoded, hashErr = hashPassword(password)
		return hashErr
	})
	if err != nil {
		return User{}, err
	}
	user, err := m.repository.CreateAccount(ctx, email, encoded)
	if err != nil {
		if errors.Is(err, ErrEmailTaken) {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("create account: %w", ErrStorage)
	}
	return user, nil
}

func (m *Manager) Login(ctx context.Context, rawEmail, password, remoteIP string) (User, string, time.Time, error) {
	allowed, err := m.repository.AllowRateLimit(ctx, "login:ip:"+remoteIP, 100, loginWindow)
	if err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("check login rate limit: %w", ErrStorage)
	}
	if !allowed {
		return User{}, "", time.Time{}, ErrRateLimited
	}
	email, emailErr := normalizeEmail(rawEmail)
	if emailErr == nil {
		digest := sha256.Sum256([]byte(email))
		allowed, err = m.repository.AllowRateLimit(ctx, "login:email:"+base64.RawURLEncoding.EncodeToString(digest[:]), 5, loginWindow)
		if err != nil {
			return User{}, "", time.Time{}, fmt.Errorf("check account rate limit: %w", ErrStorage)
		}
		if !allowed {
			return User{}, "", time.Time{}, ErrRateLimited
		}
	}

	account, accountErr := Account{}, ErrInvalidInput
	if emailErr == nil {
		account, accountErr = m.repository.FindAccountByEmail(ctx, email)
	}
	storedHash := m.dummyPasswordHash
	if accountErr == nil {
		storedHash = account.PasswordHash
	} else if !errors.Is(accountErr, ErrAccountNotFound) && !errors.Is(accountErr, ErrInvalidInput) {
		return User{}, "", time.Time{}, fmt.Errorf("find login account: %w", ErrStorage)
	}

	verified := false
	err = m.withKDF(ctx, func() error {
		verified = verifyPassword(storedHash, password)
		return nil
	})
	if err != nil {
		return User{}, "", time.Time{}, err
	}
	if accountErr != nil || !verified || account.Status != "ACTIVE" {
		return User{}, "", time.Time{}, ErrInvalidCredentials
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	absoluteExpiry := now.Add(sessionAbsoluteLifetime)
	idleExpiry := now.Add(sessionIdleLifetime)
	if idleExpiry.After(absoluteExpiry) {
		idleExpiry = absoluteExpiry
	}
	if err := m.repository.CreateSession(ctx, tokenHash[:], account.ID, now, idleExpiry, absoluteExpiry); err != nil {
		return User{}, "", time.Time{}, fmt.Errorf("create login session: %w", ErrStorage)
	}
	return account.User, token, absoluteExpiry, nil
}

func (m *Manager) Authenticate(ctx context.Context, token string) (User, error) {
	tokenBytes, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(tokenBytes) != 32 {
		return User{}, ErrUnauthenticated
	}
	tokenHash := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	user, err := m.repository.AuthenticateAndTouchSession(ctx, tokenHash[:], now, now.Add(sessionIdleLifetime))
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			return User{}, ErrUnauthenticated
		}
		return User{}, fmt.Errorf("validate login session: %w", ErrStorage)
	}
	return user, nil
}

func (m *Manager) Logout(ctx context.Context, token string) error {
	tokenBytes, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(tokenBytes) != 32 {
		return nil
	}
	tokenHash := sha256.Sum256([]byte(token))
	if err := m.repository.RevokeSession(ctx, tokenHash[:], time.Now().UTC()); err != nil {
		return fmt.Errorf("revoke login session: %w", ErrStorage)
	}
	return nil
}

func normalizeEmail(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 3 || len(trimmed) > 254 {
		return "", ErrInvalidInput
	}
	parsed, err := mail.ParseAddress(trimmed)
	if err != nil || parsed.Address != trimmed || strings.Count(trimmed, "@") != 1 {
		return "", ErrInvalidInput
	}
	return strings.ToLower(trimmed), nil
}

func (m *Manager) withKDF(ctx context.Context, operation func() error) error {
	select {
	case m.kdfSlots <- struct{}{}:
		defer func() { <-m.kdfSlots }()
		return operation()
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrRateLimited
	}
}
