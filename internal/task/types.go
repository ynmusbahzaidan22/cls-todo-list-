package task

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidInput       = errors.New("invalid task input")
	ErrNotFound           = errors.New("task not found")
	ErrConflict           = errors.New("task version conflict")
	ErrTaskArchived       = errors.New("completed task is archived and read-only")
	ErrRecoveryOver       = errors.New("task recovery window expired")
	ErrStorage            = errors.New("task storage failure")
	ErrUndoUnsupported    = errors.New("activity event does not support undo")
	ErrUndoAlreadyApplied = errors.New("activity event was already undone")
	ErrUndoExpired        = errors.New("undo window expired")
)

const (
	StatusTODO       = "TODO"
	StatusInProgress = "IN_PROGRESS"
	StatusDone       = "DONE"
	PriorityLow      = "LOW"
	PriorityMedium   = "MEDIUM"
	PriorityHigh     = "HIGH"
	PriorityUrgent   = "URGENT"
	DefaultPageSize  = 20
	MaxPageSize      = 100
	MaxBulkTasks     = 100
)

type BulkTaskInput struct {
	ID      string
	Version int64
}

type BulkOperation struct {
	Action string
	Status string
	Tasks  []BulkTaskInput
}

type Task struct {
	ID          string
	Title       string
	Description string
	Status      string
	Priority    string
	DueDate     *string
	IsOverdue   bool
	CompletedAt *time.Time
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

type Cursor struct {
	CreatedAt  time.Time
	ID         string
	StatusRank int
	Cutoff     time.Time
	TimeZone   string
	LocalDate  string
	ExpiresAt  time.Time
}

type ListFilter struct {
	Query           string
	Status          string
	Priority        string
	Due             string
	TimeZone        string
	LocalDate       string
	Archived        bool
	CompletedPeriod string
	CompletedFrom   *time.Time
	CompletedBefore *time.Time
	Cutoff          time.Time
}

// DeletedCursor pins the recovery cutoff for all pages in one listing and
// keeps keyset ordering stable while the 24-hour window advances.
type DeletedCursor struct {
	DeletedAt time.Time
	ID        string
	Cutoff    time.Time
	OwnerID   string
}

type Page struct {
	Items      []Task
	NextCursor *string
	TotalCount int
}

type Activity struct {
	ID                    string          `json:"id"`
	TaskID                string          `json:"task_id"`
	ActorUserID           string          `json:"actor_user_id"`
	EventType             string          `json:"event_type"`
	PreviousValue         json.RawMessage `json:"previous_value"`
	NewValue              json.RawMessage `json:"new_value"`
	CreatedAt             time.Time       `json:"created_at"`
	UndoOfEventID         string          `json:"undo_of_event_id,omitempty"`
	UndoAvailable         bool            `json:"undo_available"`
	UndoUnavailableReason string          `json:"undo_unavailable_reason,omitempty"`
	UndoVersion           int64           `json:"undo_version,omitempty"`
}

type ActivityCursor struct {
	CreatedAt time.Time
	ID        string
}

type ActivityPage struct {
	Items      []Activity `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

type Repository interface {
	Create(context.Context, string, string, string, string, ...string) (Task, error)
	List(context.Context, string, ListFilter, *Cursor, int) ([]Task, error)
	ListDeleted(context.Context, string, *DeletedCursor, int) ([]Task, time.Time, error)
	CountDeleted(context.Context, string) (int, error)
	Get(context.Context, string, string) (Task, error)
	Update(context.Context, string, string, int64, string, string, string, ...string) (Task, error)
	Delete(context.Context, string, string, int64) (Task, error)
	Bulk(context.Context, string, BulkOperation) ([]Task, error)
	Restore(context.Context, string, string, int64) (Task, error)
	ListActivity(context.Context, string, string, *ActivityCursor, int) ([]Activity, error)
	Undo(context.Context, string, string, string, int64) (Task, Activity, error)
}
