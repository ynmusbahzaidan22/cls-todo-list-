package auth

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidInput       = errors.New("invalid input")
	ErrEmailTaken         = errors.New("email already registered")
	ErrAccountNotFound    = errors.New("account not found")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrUnauthenticated    = errors.New("unauthenticated")
	ErrRateLimited        = errors.New("rate limited")
	ErrStorage            = errors.New("authentication storage failure")
)

type User struct {
	ID        string
	Name      string
	Email     string
	Role      string
	CreatedAt time.Time
}

type Account struct {
	User
	PasswordHash string
	Status       string
}

type Repository interface {
	CreateAccount(context.Context, string, string, string) (User, error)
	AllowRateLimit(context.Context, string, int, time.Duration) (bool, error)
	FindAccountByEmail(context.Context, string) (Account, error)
	CreateSession(context.Context, []byte, string, time.Time, time.Time, time.Time) error
	AuthenticateAndTouchSession(context.Context, []byte, time.Time, time.Time) (User, error)
	RevokeSession(context.Context, []byte, time.Time) error
}

type Service interface {
	Register(context.Context, string, string, string, string) (User, error)
	Login(context.Context, string, string, string) (User, string, time.Time, error)
	Authenticate(context.Context, string) (User, error)
	Logout(context.Context, string) error
}
