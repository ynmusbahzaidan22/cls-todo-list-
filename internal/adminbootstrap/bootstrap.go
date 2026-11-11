// Package adminbootstrap implements the one-time, explicitly targeted initial
// administrator bootstrap workflow. It is intentionally separate from HTTP.
package adminbootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
)

var (
	ErrInvalidInput = errors.New("invalid bootstrap input")
	ErrNotFound     = errors.New("target account not found")
	ErrIneligible   = errors.New("target account is not eligible")
	ErrAdminExists  = errors.New("an active administrator already exists")
)

type Target struct {
	Host, Port, Database, User string
}

// ValidateEmail requires the exact normalized address stored by registration.
func ValidateEmail(raw string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || raw != strings.ToLower(raw) {
		return "", ErrInvalidInput
	}
	parsed, err := mail.ParseAddress(raw)
	if err != nil || parsed.Address != raw || len(raw) > 254 || strings.ContainsAny(raw, "\r\n\x00") {
		return "", ErrInvalidInput
	}
	return raw, nil
}

// ValidateTarget enforces the only supported operational and automated-test
// endpoints. It deliberately rejects unknown targets instead of accepting an
// arbitrary caller-provided URL.
func ValidateTarget(target Target, testMode bool) error {
	if testMode {
		if target.Host != "127.0.0.1" || target.Port != "25432" || target.Database != "todo_test" || target.User == "" {
			return fmt.Errorf("automated test target must be the isolated todo_test endpoint")
		}
		return nil
	}
	if target.Host != "127.0.0.1" || target.Port != "15432" || target.Database != "todo_dev" || target.User == "" {
		return fmt.Errorf("operational target must be the verified todo_dev endpoint")
	}
	return nil
}

// URLTarget parses only identity components; callers must never print the URL.
func URLTarget(raw string) (Target, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil || u.User.Username() == "" || u.Fragment != "" || u.Opaque != "" || u.ForceQuery || u.Path == "/" || strings.Count(u.EscapedPath(), "/") != 1 || strings.Contains(u.EscapedPath(), "%") {
		return Target{}, ErrInvalidInput
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	if u.RawQuery != "" {
		query, queryErr := url.ParseQuery(u.RawQuery)
		if queryErr != nil || len(query) > 1 || (len(query) == 1 && (len(query["sslmode"]) != 1 || !oneOf(query.Get("sslmode"), "disable", "allow", "prefer", "require", "verify-ca", "verify-full"))) {
			return Target{}, ErrInvalidInput
		}
	}
	db, err := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), "/"))
	if err != nil || db == "" || strings.ContainsAny(db, "/\\\x00") {
		return Target{}, ErrInvalidInput
	}
	return Target{Host: u.Hostname(), Port: port, Database: db, User: u.User.Username()}, nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

type Status struct {
	ActiveAdmins int64
	GuardAdmins  int64
	TargetExists bool
	TargetActive bool
	TargetRole   string
	Eligible     bool
}

type Repository interface {
	Status(context.Context, string) (Status, error)
	Bootstrap(context.Context, string) error
}

type Service struct{ repository Repository }

func New(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) Status(ctx context.Context, rawEmail string) (Status, error) {
	email := ""
	if rawEmail != "" {
		validated, err := ValidateEmail(rawEmail)
		if err != nil {
			return Status{}, err
		}
		email = validated
	}
	return s.repository.Status(ctx, email)
}

func (s *Service) Bootstrap(ctx context.Context, rawEmail string) error {
	email, err := ValidateEmail(rawEmail)
	if err != nil {
		return err
	}
	return s.repository.Bootstrap(ctx, email)
}
