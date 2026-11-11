package admin

import (
	"context"
	"errors"
	"time"

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

var (
	ErrInvalidInput = errors.New("invalid admin list input")
	ErrStorage      = errors.New("admin storage failure")
	ErrNotFound     = errors.New("admin target not found")
	ErrConflict     = errors.New("admin operation conflicts with account policy")
	ErrForbidden    = errors.New("admin operation is not permitted")
)

const (
	RoleUser       = "USER"
	RoleAdmin      = "ADMIN"
	StatusActive   = "ACTIVE"
	StatusDisabled = "DISABLED"
)

const (
	DefaultPageSize  = task.DefaultPageSize
	MaxPageSize      = task.MaxPageSize
	DefaultTaskState = "active"
)

type User struct {
	ID                string
	Name              string
	Email             string
	Role              string
	Status            string
	CreatedAt         time.Time
	SnapshotAt        time.Time
	CanChangeRole     bool
	CanChangeStatus   bool
	CanRevokeSessions bool
	ProtectionReason  string
}

type Task struct {
	task.Task
	OwnerID    string
	OwnerName  string
	SnapshotAt time.Time
}

type Cursor struct {
	CreatedAt  time.Time
	ID         string
	SnapshotAt time.Time
}

type UserPage struct {
	Items      []User
	NextCursor *string
}

type TaskPage struct {
	Items      []Task
	NextCursor *string
}

type MutationResult struct {
	User            User
	RevokedSessions int64
}

type Repository interface {
	ListAdminUsers(context.Context, string, *Cursor, int) ([]User, error)
	ListAdminTasks(context.Context, string, string, *Cursor, int) ([]Task, error)
	ChangeUserRole(context.Context, string, string, string) (MutationResult, error)
	ChangeUserStatus(context.Context, string, string, string) (MutationResult, error)
	RevokeUserSessions(context.Context, string, string) (MutationResult, error)
}
