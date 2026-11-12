package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

const maxCursorLength = 512

type Service struct {
	repository Repository
	signingKey []byte
}

type cursorPayload struct {
	Resource   string    `json:"resource"`
	Filter     string    `json:"filter"`
	CreatedAt  time.Time `json:"created_at"`
	ID         string    `json:"id"`
	SnapshotAt time.Time `json:"snapshot_at"`
}

func NewService(repository Repository, signingKey []byte) *Service {
	if len(signingKey) != 32 {
		panic("admin cursor signing key must be 32 bytes")
	}
	key := append([]byte(nil), signingKey...)
	return &Service{repository: repository, signingKey: key}
}

func (s *Service) ChangeUserRole(ctx context.Context, actorID, targetID, role string) (MutationResult, error) {
	if !task.ValidID(actorID) || !task.ValidID(targetID) || (role != RoleUser && role != RoleAdmin) {
		return MutationResult{}, ErrInvalidInput
	}
	if actorID == targetID {
		return MutationResult{}, ErrForbidden
	}
	result, err := s.repository.ChangeUserRole(ctx, actorID, targetID, role)
	return result, mapMutationError("change user role", err)
}

func (s *Service) ChangeUserStatus(ctx context.Context, actorID, targetID, status string) (MutationResult, error) {
	if !task.ValidID(actorID) || !task.ValidID(targetID) || (status != StatusActive && status != StatusDisabled) {
		return MutationResult{}, ErrInvalidInput
	}
	if actorID == targetID {
		return MutationResult{}, ErrForbidden
	}
	result, err := s.repository.ChangeUserStatus(ctx, actorID, targetID, status)
	return result, mapMutationError("change user status", err)
}

func (s *Service) RevokeUserSessions(ctx context.Context, actorID, targetID string) (MutationResult, error) {
	if !task.ValidID(actorID) || !task.ValidID(targetID) {
		return MutationResult{}, ErrInvalidInput
	}
	if actorID == targetID {
		return MutationResult{}, ErrForbidden
	}
	result, err := s.repository.RevokeUserSessions(ctx, actorID, targetID)
	return result, mapMutationError("revoke user sessions", err)
}

func mapMutationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrForbidden) {
		return err
	}
	return fmt.Errorf("%s: %w: %w", operation, ErrStorage, err)
}

func (s *Service) ListUsers(ctx context.Context, limit int, rawCursor string) (UserPage, error) {
	return s.listUsers(ctx, "00000000-0000-0000-0000-000000000000", "", limit, rawCursor)
}

func (s *Service) ListUsersForActor(ctx context.Context, actorID, search string, limit int, rawCursor string) (UserPage, error) {
	return s.listUsers(ctx, actorID, search, limit, rawCursor)
}

func normalizeUserSearch(search string) (string, error) {
	search = strings.TrimSpace(search)
	if len([]rune(search)) > 100 || strings.IndexFunc(search, unicode.IsControl) >= 0 {
		return "", ErrInvalidInput
	}
	return strings.ToLower(search), nil
}

func (s *Service) listUsers(ctx context.Context, actorID, rawSearch string, limit int, rawCursor string) (UserPage, error) {
	if limit == 0 {
		limit = DefaultPageSize
	}
	if limit < 1 || limit > MaxPageSize {
		return UserPage{}, ErrInvalidInput
	}
	search, err := normalizeUserSearch(rawSearch)
	if err != nil {
		return UserPage{}, err
	}
	cursor, err := s.decodeCursor(rawCursor, "users", search)
	if err != nil {
		return UserPage{}, ErrInvalidInput
	}
	items, err := s.repository.ListAdminUsers(ctx, actorID, search, cursor, limit+1)
	if err != nil {
		return UserPage{}, fmt.Errorf("list admin users: %w: %w", ErrStorage, err)
	}
	page := UserPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		next, err := s.encodeCursor("users", search, last.CreatedAt, last.ID, last.SnapshotAt)
		if err != nil {
			return UserPage{}, fmt.Errorf("encode admin user cursor: %w", ErrStorage)
		}
		page.NextCursor = &next
	}
	if page.Items == nil {
		page.Items = []User{}
	}
	return page, nil
}

// ListTasks lists all non-deleted tasks by default. State may be active,
// deleted, or all; status may be empty or one of the task workflow states.
func (s *Service) ListTasks(ctx context.Context, limit int, rawCursor, status, state string) (TaskPage, error) {
	if limit == 0 {
		limit = DefaultPageSize
	}
	if state == "" {
		state = DefaultTaskState
	}
	if limit < 1 || limit > MaxPageSize || (status != "" && status != task.StatusTODO && status != task.StatusInProgress && status != task.StatusDone) ||
		(state != "active" && state != "deleted" && state != "all") {
		return TaskPage{}, ErrInvalidInput
	}
	filter := status + "\x00" + state
	cursor, err := s.decodeCursor(rawCursor, "tasks", filter)
	if err != nil {
		return TaskPage{}, ErrInvalidInput
	}
	items, err := s.repository.ListAdminTasks(ctx, status, state, cursor, limit+1)
	if err != nil {
		return TaskPage{}, fmt.Errorf("list admin tasks: %w: %w", ErrStorage, err)
	}
	page := TaskPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		next, err := s.encodeCursor("tasks", filter, last.CreatedAt, last.ID, last.SnapshotAt)
		if err != nil {
			return TaskPage{}, fmt.Errorf("encode admin task cursor: %w", ErrStorage)
		}
		page.NextCursor = &next
	}
	if page.Items == nil {
		page.Items = []Task{}
	}
	return page, nil
}

func (s *Service) decodeCursor(raw, resource, filter string) (*Cursor, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxCursorLength {
		return nil, ErrInvalidInput
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return nil, ErrInvalidInput
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidInput
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidInput
	}
	mac := hmac.New(sha256.New, s.signingKey)
	_, _ = mac.Write(payloadBytes)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, ErrInvalidInput
	}
	decoder := json.NewDecoder(strings.NewReader(string(payloadBytes)))
	decoder.DisallowUnknownFields()
	var payload cursorPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, ErrInvalidInput
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || payload.Resource != resource || payload.Filter != filter ||
		payload.CreatedAt.IsZero() || payload.SnapshotAt.IsZero() || payload.CreatedAt.After(payload.SnapshotAt) || !task.ValidID(payload.ID) {
		return nil, ErrInvalidInput
	}
	return &Cursor{CreatedAt: payload.CreatedAt.UTC(), ID: strings.ToLower(payload.ID), SnapshotAt: payload.SnapshotAt.UTC()}, nil
}

func (s *Service) encodeCursor(resource, filter string, createdAt time.Time, id string, snapshotAt time.Time) (string, error) {
	if createdAt.IsZero() || snapshotAt.IsZero() || createdAt.After(snapshotAt) || !task.ValidID(id) {
		return "", ErrInvalidInput
	}
	payload, err := json.Marshal(cursorPayload{Resource: resource, Filter: filter, CreatedAt: createdAt.UTC(), ID: strings.ToLower(id), SnapshotAt: snapshotAt.UTC()})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.signingKey)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
