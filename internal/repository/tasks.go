package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

type TaskRepository struct {
	pool *pgxpool.Pool
}

func NewTaskRepository(pool *pgxpool.Pool) *TaskRepository {
	return &TaskRepository{pool: pool}
}

// ListContext takes one database timestamp so date filters and lifecycle cutoffs
// share a consistent snapshot for the whole cursor traversal.
func (r *TaskRepository) ListContext(ctx context.Context, userID, requestedZone string) (string, string, time.Time, error) {
	var zone, localDate string
	var cutoff time.Time
	var err error
	if requestedZone == "" {
		err = r.pool.QueryRow(ctx, `WITH db_clock AS (SELECT statement_timestamp() AS as_of)
			SELECT COALESCE(u.time_zone, 'UTC'), to_char((db_clock.as_of AT TIME ZONE COALESCE(u.time_zone, 'UTC'))::date, 'YYYY-MM-DD'), db_clock.as_of
			FROM app.users AS u CROSS JOIN db_clock WHERE u.id = $1`, userID).Scan(&zone, &localDate, &cutoff)
	} else {
		err = r.pool.QueryRow(ctx, `WITH db_clock AS (SELECT statement_timestamp() AS as_of)
			SELECT $2::text, to_char((db_clock.as_of AT TIME ZONE $2::text)::date, 'YYYY-MM-DD'), db_clock.as_of
			FROM app.users AS u CROSS JOIN db_clock WHERE u.id = $1`, userID, requestedZone).Scan(&zone, &localDate, &cutoff)
	}
	return zone, localDate, cutoff, err
}

const taskListColumns = `
	id::text, title, COALESCE(description, ''), status, priority, due_date, completed_at,
	version, created_at, updated_at, deleted_at,
	COALESCE(due_date < $9::date
	 AND status <> 'DONE' AND deleted_at IS NULL, FALSE) AS is_overdue`

const taskColumns = `
	id::text, title, COALESCE(description, ''), status, priority, due_date, completed_at,
	version, created_at, updated_at, deleted_at,
	COALESCE(due_date < ((clock_timestamp() AT TIME ZONE (SELECT COALESCE(time_zone, 'UTC') FROM app.users WHERE id = user_id))::date)
	 AND status <> 'DONE' AND deleted_at IS NULL, FALSE) AS is_overdue`

func (r *TaskRepository) Create(ctx context.Context, userID, title, description, status string, priorities ...string) (task.Task, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return task.Task{}, err
	}
	defer tx.Rollback(ctx)
	priority := task.PriorityMedium
	var dueDate any
	if len(priorities) >= 1 && priorities[0] != "__PRIORITY_PRESERVE__" {
		priority = priorities[0]
	}
	if len(priorities) > 1 && priorities[1] != "__DUE_NONE__" {
		dueDate = priorities[1]
	}
	created, err := insertTaskTx(ctx, tx, userID, title, description, status, priority, dueDate)
	if err != nil {
		return task.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return task.Task{}, err
	}
	return created, nil
}

func insertTaskTx(ctx context.Context, tx pgx.Tx, userID, title, description, status, priority string, due ...any) (task.Task, error) {
	var dueDate any
	if len(due) > 0 {
		dueDate = due[0]
	}
	created, err := scanTask(tx.QueryRow(ctx, `
		INSERT INTO app.tasks (user_id, title, description, status, priority, due_date, completed_at)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6::date,
			CASE WHEN $4 = 'DONE' THEN clock_timestamp() ELSE NULL END)
		RETURNING `+taskColumns, userID, title, description, status, priority, dueDate))
	if err != nil {
		return task.Task{}, err
	}
	if err := insertActivity(ctx, tx, userID, created.ID, "TASK_CREATED", nil, map[string]any{
		"title": created.Title, "description": created.Description, "status": created.Status, "priority": created.Priority, "due_date": created.DueDate,
	}); err != nil {
		return task.Task{}, err
	}
	return created, nil
}

func (r *TaskRepository) List(ctx context.Context, userID string, filter task.ListFilter, cursor *task.Cursor, maxRows int) ([]task.Task, error) {
	var cursorTime any
	var cursorID any
	var cursorStatusRank any
	if cursor != nil {
		cursorTime = cursor.CreatedAt
		cursorID = cursor.ID
		cursorStatusRank = cursor.StatusRank
	}
	searchPattern := ""
	if filter.Query != "" {
		literal := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(filter.Query)
		searchPattern = "%" + literal + "%"
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+taskListColumns+`
		FROM app.tasks
		WHERE user_id = $1
		  AND deleted_at IS NULL
		  AND created_at <= $14::timestamptz
		  AND updated_at <= $14::timestamptz
		  AND ($2 = '' OR (title ILIKE $2 ESCAPE E'\\' OR COALESCE(description, '') ILIKE $2 ESCAPE E'\\'))
		  AND ($3 = '' OR status = $3)
		  AND ($4 = '' OR priority = $4)
		  AND ($7 = '' OR ($7 = 'none' AND due_date IS NULL)
	       OR ($7 = 'overdue' AND status <> 'DONE' AND due_date < $9::date)
	       OR ($7 = 'today' AND status <> 'DONE' AND due_date = $9::date)
	       OR ($7 = 'upcoming' AND status <> 'DONE' AND due_date > $9::date))
		  AND (($5::timestamptz IS NULL)
		       OR (CASE WHEN status = 'IN_PROGRESS' THEN 0 WHEN status = 'DONE' THEN 2 ELSE 1 END) > $10::integer
		       OR ((CASE WHEN status = 'IN_PROGRESS' THEN 0 WHEN status = 'DONE' THEN 2 ELSE 1 END) = $10::integer AND (created_at, id) < ($5::timestamptz, $6::uuid)))
		  AND (($11::boolean AND status = 'DONE' AND completed_at <= $14::timestamptz - interval '12 hours')
		       OR (NOT $11::boolean AND (status <> 'DONE' OR completed_at > $14::timestamptz - interval '12 hours')))
		  AND (NOT $11::boolean OR $12::timestamptz IS NULL OR completed_at >= $12::timestamptz)
		  AND (NOT $11::boolean OR $13::timestamptz IS NULL OR completed_at < $13::timestamptz)
		ORDER BY CASE WHEN status = 'IN_PROGRESS' THEN 0 WHEN status = 'DONE' THEN 2 ELSE 1 END ASC, created_at DESC, id DESC
		LIMIT $8`, userID, searchPattern, filter.Status, filter.Priority, cursorTime, cursorID, filter.Due, maxRows, filter.LocalDate, cursorStatusRank, filter.Archived, filter.CompletedFrom, filter.CompletedBefore, filter.Cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]task.Task, 0, maxRows)
	for rows.Next() {
		item, err := scanTaskRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *TaskRepository) CountCompleted(ctx context.Context, userID string, from, before *time.Time, cutoff time.Time) (int, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM app.tasks
		WHERE user_id = $1
		  AND deleted_at IS NULL
		  AND created_at <= $4::timestamptz
		  AND updated_at <= $4::timestamptz
		  AND status = 'DONE'
		  AND completed_at <= $4::timestamptz - interval '12 hours'
		  AND ($2::timestamptz IS NULL OR completed_at >= $2::timestamptz)
		  AND ($3::timestamptz IS NULL OR completed_at < $3::timestamptz)`, userID, from, before, cutoff).Scan(&count)
	if err != nil {
		return 0, err
	}
	return int(count), nil
}

func (r *TaskRepository) ListDeleted(ctx context.Context, userID string, cursor *task.DeletedCursor, maxRows int) ([]task.Task, time.Time, error) {
	var cutoff any
	var cursorTime any
	var cursorID any
	var snapshotAt any
	if cursor != nil {
		cutoff = cursor.Cutoff
		cursorTime = cursor.DeletedAt
		cursorID = cursor.ID
		snapshotAt = cursor.SnapshotAt
	}
	rows, err := r.pool.Query(ctx, `
		WITH snapshot AS (
			SELECT COALESCE($6::timestamptz, statement_timestamp()) AS at
		), recovery_window AS (
			SELECT COALESCE($2::timestamptz, snapshot.at - interval '24 hours') AS cutoff, snapshot.at
			FROM snapshot
		)
		SELECT `+taskColumns+`, recovery_window.cutoff
		FROM app.tasks
		CROSS JOIN recovery_window
		WHERE user_id = $1
		  AND deleted_at IS NOT NULL
		  AND created_at <= recovery_window.at
		  AND updated_at <= recovery_window.at
		  AND deleted_at >= COALESCE($2::timestamptz, recovery_window.cutoff)
		  AND ($3::timestamptz IS NULL OR (deleted_at, id) < ($3::timestamptz, $4::uuid))
		ORDER BY deleted_at DESC, id DESC
		LIMIT $5`, userID, cutoff, cursorTime, cursorID, maxRows, snapshotAt)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()

	items := make([]task.Task, 0, maxRows)
	var pageCutoff time.Time
	for rows.Next() {
		var item task.Task
		var dueDate pgtype.Date
		var completedAt pgtype.Timestamptz
		var deletedAt pgtype.Timestamptz
		if err := rows.Scan(
			&item.ID, &item.Title, &item.Description, &item.Status, &item.Priority, &dueDate, &completedAt,
			&item.Version, &item.CreatedAt, &item.UpdatedAt, &deletedAt, &item.IsOverdue, &pageCutoff,
		); err != nil {
			return nil, time.Time{}, err
		}
		if completedAt.Valid {
			value := completedAt.Time
			item.CompletedAt = &value
		}
		if dueDate.Valid && dueDate.InfinityModifier == pgtype.Finite {
			value := dueDate.Time.Format("2006-01-02")
			item.DueDate = &value
		}
		if deletedAt.Valid {
			value := deletedAt.Time
			item.DeletedAt = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return items, pageCutoff, nil
}

func snapshotTime(cursor *task.DeletedCursor) any {
	if cursor == nil {
		return nil
	}
	return cursor.SnapshotAt
}

func (r *TaskRepository) CountDeleted(ctx context.Context, userID string) (int, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM app.tasks
		WHERE user_id = $1
		  AND deleted_at IS NOT NULL
		  AND deleted_at >= statement_timestamp() - interval '24 hours'`, userID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return int(count), nil
}

func (r *TaskRepository) Get(ctx context.Context, userID, taskID string) (task.Task, error) {
	item, err := scanTask(r.pool.QueryRow(ctx, `
		SELECT `+taskColumns+`
		FROM app.tasks
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, taskID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Task{}, task.ErrNotFound
	}
	return item, err
}

func (r *TaskRepository) Update(ctx context.Context, userID, taskID string, version int64, title, description, status string, priorities ...string) (task.Task, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return task.Task{}, err
	}
	defer tx.Rollback(ctx)
	previous, err := scanTask(tx.QueryRow(ctx, `
		SELECT `+taskColumns+`
		FROM app.tasks
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		FOR UPDATE`, taskID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Task{}, r.ownedConflictOrNotFound(ctx, userID, taskID)
	}
	if err != nil {
		return task.Task{}, err
	}
	if previous.Version != version {
		return task.Task{}, task.ErrConflict
	}
	archived, err := completedTaskArchived(ctx, tx, previous)
	if err != nil {
		return task.Task{}, err
	}
	if archived {
		return task.Task{}, task.ErrTaskArchived
	}
	priority := previous.Priority
	dueDate := any(previous.DueDate)
	dueProvided := false
	if len(priorities) >= 1 && priorities[0] != "__PRIORITY_PRESERVE__" {
		priority = priorities[0]
	}
	if len(priorities) > 1 {
		dueProvided = true
		if priorities[1] == "__DUE_NONE__" {
			dueDate = nil
		} else {
			dueDate = priorities[1]
		}
	}
	item, err := scanTask(tx.QueryRow(ctx, `
		UPDATE app.tasks
		SET title = $4,
		    description = NULLIF($5, ''),
		    status = $6,
		    priority = $7,
		    due_date = CASE WHEN $9 THEN $8::date ELSE due_date END,
		    completed_at = CASE
		        WHEN $6 = 'DONE' THEN COALESCE(completed_at, clock_timestamp())
		        ELSE NULL
		    END,
		    version = version + 1,
		    updated_at = clock_timestamp()
		WHERE id = $1 AND user_id = $2 AND version = $3 AND deleted_at IS NULL
		RETURNING `+taskColumns, taskID, userID, version, title, description, status, priority, dueDate, dueProvided))
	if err != nil {
		return task.Task{}, err
	}
	if previous.Title != item.Title && previous.Description != item.Description {
		if err := insertActivity(ctx, tx, userID, taskID, "TASK_UPDATED",
			map[string]any{"title": previous.Title, "description": previous.Description},
			map[string]any{"title": item.Title, "description": item.Description}); err != nil {
			return task.Task{}, err
		}
	} else if previous.Title != item.Title {
		if err := insertActivity(ctx, tx, userID, taskID, "TASK_TITLE_UPDATED", map[string]any{"title": previous.Title}, map[string]any{"title": item.Title}); err != nil {
			return task.Task{}, err
		}
	} else if previous.Description != item.Description {
		if err := insertActivity(ctx, tx, userID, taskID, "TASK_DESCRIPTION_UPDATED", map[string]any{"description": previous.Description}, map[string]any{"description": item.Description}); err != nil {
			return task.Task{}, err
		}
	}
	if previous.Status != item.Status {
		if err := insertActivity(ctx, tx, userID, taskID, "TASK_STATUS_CHANGED", map[string]any{"status": previous.Status}, map[string]any{"status": item.Status}); err != nil {
			return task.Task{}, err
		}
	}
	if previous.Priority != item.Priority {
		if err := insertActivity(ctx, tx, userID, taskID, "TASK_PRIORITY_CHANGED", map[string]any{"priority": previous.Priority}, map[string]any{"priority": item.Priority}); err != nil {
			return task.Task{}, err
		}
	}
	if !sameDueDate(previous.DueDate, item.DueDate) {
		if err := insertActivity(ctx, tx, userID, taskID, "TASK_DUE_DATE_CHANGED", map[string]any{"due_date": previous.DueDate}, map[string]any{"due_date": item.DueDate}); err != nil {
			return task.Task{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return task.Task{}, err
	}
	return item, nil
}

func (r *TaskRepository) Delete(ctx context.Context, userID, taskID string, version int64) (task.Task, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return task.Task{}, err
	}
	defer tx.Rollback(ctx)
	previous, err := scanTask(tx.QueryRow(ctx, `
		SELECT `+taskColumns+`
		FROM app.tasks
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		FOR UPDATE`, taskID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Task{}, r.ownedConflictOrNotFound(ctx, userID, taskID)
	}
	if err != nil {
		return task.Task{}, err
	}
	if previous.Version != version {
		return task.Task{}, task.ErrConflict
	}
	archived, err := completedTaskArchived(ctx, tx, previous)
	if err != nil {
		return task.Task{}, err
	}
	if archived {
		return task.Task{}, task.ErrTaskArchived
	}
	item, err := scanTask(tx.QueryRow(ctx, `
		UPDATE app.tasks
		SET deleted_at = clock_timestamp(),
		    version = version + 1,
		    updated_at = clock_timestamp()
		WHERE id = $1 AND user_id = $2 AND version = $3 AND deleted_at IS NULL
		RETURNING `+taskColumns, taskID, userID, version))
	if err != nil {
		return task.Task{}, err
	}
	if err := insertActivity(ctx, tx, userID, taskID, "TASK_DELETED",
		map[string]any{"deleted_at": nil}, map[string]any{"deleted_at": item.DeletedAt}); err != nil {
		return task.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return task.Task{}, err
	}
	return item, nil
}

func (r *TaskRepository) Bulk(ctx context.Context, userID string, operation task.BulkOperation) ([]task.Task, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Lock in a stable order so overlapping bulk operations do not deadlock.
	ordered := append([]task.BulkTaskInput(nil), operation.Tasks...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	locked := make(map[string]task.Task, len(ordered))
	for _, requested := range ordered {
		previous, scanErr := scanTask(tx.QueryRow(ctx, `
			SELECT `+taskColumns+`
			FROM app.tasks
			WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
			FOR UPDATE`, requested.ID, userID))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, r.ownedConflictOrNotFound(ctx, userID, requested.ID)
		}
		if scanErr != nil {
			return nil, scanErr
		}
		if previous.Version != requested.Version {
			return nil, task.ErrConflict
		}
		archived, archiveErr := completedTaskArchived(ctx, tx, previous)
		if archiveErr != nil {
			return nil, archiveErr
		}
		if archived {
			return nil, task.ErrTaskArchived
		}
		locked[requested.ID] = previous
	}

	updated := make(map[string]task.Task, len(ordered))
	for _, requested := range ordered {
		previous := locked[requested.ID]
		var item task.Task
		switch operation.Action {
		case "status":
			item, err = scanTask(tx.QueryRow(ctx, `
				UPDATE app.tasks
				SET status = $4,
				    completed_at = CASE WHEN $4 = 'DONE' THEN COALESCE(completed_at, clock_timestamp()) ELSE NULL END,
				    version = version + 1,
				    updated_at = clock_timestamp()
				WHERE id = $1 AND user_id = $2 AND version = $3 AND deleted_at IS NULL
				RETURNING `+taskColumns, requested.ID, userID, requested.Version, operation.Status))
			if err == nil && previous.Status != item.Status {
				err = insertActivity(ctx, tx, userID, item.ID, "TASK_STATUS_CHANGED",
					map[string]any{"status": previous.Status}, map[string]any{"status": item.Status})
			}
		case "delete":
			item, err = scanTask(tx.QueryRow(ctx, `
				UPDATE app.tasks
				SET deleted_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
				WHERE id = $1 AND user_id = $2 AND version = $3 AND deleted_at IS NULL
				RETURNING `+taskColumns, requested.ID, userID, requested.Version))
			if err == nil {
				err = insertActivity(ctx, tx, userID, item.ID, "TASK_DELETED",
					map[string]any{"deleted_at": nil}, map[string]any{"deleted_at": item.DeletedAt})
			}
		default:
			return nil, task.ErrInvalidInput
		}
		if err != nil {
			return nil, err
		}
		updated[item.ID] = item
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	result := make([]task.Task, 0, len(operation.Tasks))
	for _, requested := range operation.Tasks {
		result = append(result, updated[requested.ID])
	}
	return result, nil
}

func (r *TaskRepository) Restore(ctx context.Context, userID, taskID string, version int64) (task.Task, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return task.Task{}, err
	}
	defer tx.Rollback(ctx)
	previous, err := scanTask(tx.QueryRow(ctx, `
		SELECT `+taskColumns+`
		FROM app.tasks
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NOT NULL
		FOR UPDATE`, taskID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Task{}, r.restoreConflictOrNotFound(ctx, userID, taskID, version)
	}
	if err != nil {
		return task.Task{}, err
	}
	if previous.Version != version {
		return task.Task{}, task.ErrConflict
	}
	var recoveryAllowed bool
	if err := tx.QueryRow(ctx, `SELECT $1::timestamptz >= statement_timestamp() - interval '24 hours'`, previous.DeletedAt).Scan(&recoveryAllowed); err != nil {
		return task.Task{}, err
	}
	if !recoveryAllowed {
		return task.Task{}, task.ErrRecoveryOver
	}
	item, err := scanTask(tx.QueryRow(ctx, `
		UPDATE app.tasks
		SET deleted_at = NULL,
		    version = version + 1,
		    updated_at = clock_timestamp()
		WHERE id = $1 AND user_id = $2 AND version = $3
		  AND deleted_at IS NOT NULL
		  AND deleted_at >= statement_timestamp() - interval '24 hours'
		RETURNING `+taskColumns, taskID, userID, version))
	if err != nil {
		return task.Task{}, err
	}
	if err := insertActivity(ctx, tx, userID, taskID, "TASK_RESTORED",
		map[string]any{"deleted_at": previous.DeletedAt}, map[string]any{"deleted_at": nil}); err != nil {
		return task.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return task.Task{}, err
	}
	return item, nil
}

func (r *TaskRepository) restoreConflictOrNotFound(ctx context.Context, userID, taskID string, version int64) error {
	var currentVersion int64
	var deletedAt pgtype.Timestamptz
	var recoveryExpired bool
	checkErr := r.pool.QueryRow(ctx, `
		SELECT version, deleted_at,
		       deleted_at IS NOT NULL AND deleted_at < statement_timestamp() - interval '24 hours'
		FROM app.tasks
		WHERE id = $1 AND user_id = $2`, taskID, userID,
	).Scan(&currentVersion, &deletedAt, &recoveryExpired)
	if errors.Is(checkErr, pgx.ErrNoRows) {
		return task.ErrNotFound
	}
	if checkErr != nil {
		return checkErr
	}
	if currentVersion != version {
		return task.ErrConflict
	}
	if !deletedAt.Valid {
		return task.ErrConflict
	}
	if recoveryExpired {
		return task.ErrRecoveryOver
	}
	return task.ErrConflict
}

func (r *TaskRepository) ListActivity(ctx context.Context, userID, taskID string, cursor *task.ActivityCursor, maxRows int) ([]task.Activity, error) {
	var owned bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.tasks WHERE id = $1 AND user_id = $2)`, taskID, userID).Scan(&owned); err != nil {
		return nil, err
	}
	if !owned {
		return nil, task.ErrNotFound
	}
	var cursorTime any
	var cursorID any
	var cursorSnapshot any
	if cursor != nil {
		cursorTime, cursorID = cursor.CreatedAt, cursor.ID
		cursorSnapshot = cursor.SnapshotAt
	}
	rows, err := r.pool.Query(ctx, `
		WITH snapshot AS (SELECT COALESCE($5::timestamptz, statement_timestamp()) AS at)
		SELECT a.id::text, a.task_id::text, a.actor_user_id::text, a.event_type,
		       a.previous_value, a.new_value, a.created_at,
		       COALESCE(a.undo_of_event_id::text, ''), t.version, t.title,
		       COALESCE(t.description, ''), t.status, t.priority, t.due_date, t.deleted_at,
		       EXISTS (SELECT 1 FROM app.task_activity undone WHERE undone.undo_of_event_id = a.id),
		       EXISTS (
		         SELECT 1 FROM app.task_activity later
		         WHERE later.task_id = a.task_id AND later.user_id = a.user_id
		           AND (later.created_at, later.id) > (a.created_at, a.id)
		           AND (later.event_type IN ('TASK_TITLE_UPDATED', 'TASK_UPDATED')
		             OR (later.event_type = 'TASK_UNDONE' AND later.new_value->'restored_fields' ? 'title'))
		       ),
		       EXISTS (
		         SELECT 1 FROM app.task_activity later
		         WHERE later.task_id = a.task_id AND later.user_id = a.user_id
		           AND (later.created_at, later.id) > (a.created_at, a.id)
		           AND (later.event_type IN ('TASK_DESCRIPTION_UPDATED', 'TASK_UPDATED')
		             OR (later.event_type = 'TASK_UNDONE' AND later.new_value->'restored_fields' ? 'description'))
		       ),
		       EXISTS (
		         SELECT 1 FROM app.task_activity later
		         WHERE later.task_id = a.task_id AND later.user_id = a.user_id
		           AND (later.created_at, later.id) > (a.created_at, a.id)
		           AND (later.event_type = 'TASK_STATUS_CHANGED'
		             OR (later.event_type = 'TASK_UNDONE' AND later.new_value->'restored_fields' ? 'status'))
		       ),
		       EXISTS (
		         SELECT 1 FROM app.task_activity later
		         WHERE later.task_id = a.task_id AND later.user_id = a.user_id
		           AND (later.created_at, later.id) > (a.created_at, a.id)
		           AND (later.event_type = 'TASK_PRIORITY_CHANGED'
		             OR (later.event_type = 'TASK_UNDONE' AND later.new_value->'restored_fields' ? 'priority'))
		       ),
		       EXISTS (
		         SELECT 1 FROM app.task_activity later
		         WHERE later.task_id = a.task_id AND later.user_id = a.user_id
		           AND (later.created_at, later.id) > (a.created_at, a.id)
		           AND (later.event_type IN ('TASK_DELETED', 'TASK_RESTORED')
		             OR (later.event_type = 'TASK_UNDONE' AND later.new_value->>'original_event_type' = 'TASK_DELETED'))
		       ),
		       EXISTS (
		         SELECT 1 FROM app.task_activity later
		         WHERE later.task_id = a.task_id AND later.user_id = a.user_id
		           AND (later.created_at, later.id) > (a.created_at, a.id)
		           AND (later.event_type = 'TASK_DUE_DATE_CHANGED'
		             OR (later.event_type = 'TASK_UNDONE' AND later.new_value->'restored_fields' ? 'due_date'))
		       ),
		       CASE WHEN a.event_type = 'TASK_DELETED'
	         THEN (a.new_value->>'deleted_at')::timestamptz < clock_timestamp() - interval '24 hours'
	         ELSE FALSE END,
		       snapshot.at
		FROM app.task_activity a
		JOIN app.tasks t ON t.id = a.task_id AND t.user_id = a.user_id
		CROSS JOIN snapshot
		WHERE a.user_id = $1 AND a.task_id = $2
		  AND a.created_at <= snapshot.at
		  AND ($3::timestamptz IS NULL OR (a.created_at, a.id) < ($3::timestamptz, $4::uuid))
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT $6`, userID, taskID, cursorTime, cursorID, cursorSnapshot, maxRows)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]task.Activity, 0, maxRows)
	for rows.Next() {
		var item task.Activity
		var previous, current []byte
		var currentVersion int64
		var currentTitle, currentDescription, currentStatus, currentPriority string
		var taskDeletedAt pgtype.Timestamptz
		var dueDate pgtype.Date
		var alreadyUndone, laterTitle, laterDescription, laterStatus, laterPriority, laterDeletion, laterDueDate, deletionExpired bool
		if err := rows.Scan(
			&item.ID, &item.TaskID, &item.ActorUserID, &item.EventType, &previous, &current, &item.CreatedAt,
			&item.UndoOfEventID, &currentVersion, &currentTitle, &currentDescription, &currentStatus, &currentPriority, &dueDate, &taskDeletedAt,
			&alreadyUndone, &laterTitle, &laterDescription, &laterStatus, &laterPriority, &laterDeletion, &laterDueDate, &deletionExpired, &item.SnapshotAt,
		); err != nil {
			return nil, err
		}
		if previous != nil {
			item.PreviousValue = json.RawMessage(previous)
		}
		if current != nil {
			item.NewValue = json.RawMessage(current)
		}
		item.UndoVersion = currentVersion
		var currentDueDate *string
		if dueDate.Valid && dueDate.InfinityModifier == pgtype.Finite {
			value := dueDate.Time.Format("2006-01-02")
			currentDueDate = &value
		}
		item.UndoUnavailableReason = undoUnavailableReason(item, currentTitle, currentDescription, currentStatus, currentPriority, currentDueDate, taskDeletedAt, alreadyUndone, laterTitle, laterDescription, laterStatus, laterPriority, laterDeletion, laterDueDate, deletionExpired)
		item.UndoAvailable = item.UndoUnavailableReason == ""
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// Undo locks the task to serialize all task mutations, validates the inverse against
// current values, and commits the compensation and linked history event atomically.
func (r *TaskRepository) Undo(ctx context.Context, userID, taskID, eventID string, expectedVersion int64) (task.Task, task.Activity, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return task.Task{}, task.Activity{}, err
	}
	defer tx.Rollback(ctx)

	// Lock the task first. Every application task mutation takes this row lock while
	// updating the task, so it serializes Undo without requiring UPDATE on history.
	current, err := scanTask(tx.QueryRow(ctx, `
		SELECT `+taskColumns+`
		FROM app.tasks
		WHERE id = $1 AND user_id = $2
		FOR UPDATE`, taskID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Task{}, task.Activity{}, task.ErrNotFound
	}
	if err != nil {
		return task.Task{}, task.Activity{}, err
	}

	var original task.Activity
	var previousJSON, newJSON []byte
	err = tx.QueryRow(ctx, `
		SELECT id::text, task_id::text, actor_user_id::text, event_type,
		       previous_value, new_value, created_at, COALESCE(undo_of_event_id::text, '')
		FROM app.task_activity
		WHERE id = $1 AND task_id = $2 AND user_id = $3
		`, eventID, taskID, userID,
	).Scan(&original.ID, &original.TaskID, &original.ActorUserID, &original.EventType, &previousJSON, &newJSON, &original.CreatedAt, &original.UndoOfEventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Task{}, task.Activity{}, task.ErrNotFound
	}
	if err != nil {
		return task.Task{}, task.Activity{}, err
	}
	if previousJSON != nil {
		original.PreviousValue = json.RawMessage(previousJSON)
	}
	if newJSON != nil {
		original.NewValue = json.RawMessage(newJSON)
	}

	var alreadyUndone, laterTitle, laterDescription, laterStatus, laterPriority, laterDeletion, laterDueDate bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM app.task_activity WHERE undo_of_event_id = $1),
		       EXISTS (
		         SELECT 1 FROM app.task_activity later
		         WHERE later.task_id = $2 AND later.user_id = $3
		           AND (later.created_at, later.id) > ($4::timestamptz, $1::uuid)
		           AND (later.event_type IN ('TASK_TITLE_UPDATED', 'TASK_UPDATED')
		             OR (later.event_type = 'TASK_UNDONE' AND later.new_value->'restored_fields' ? 'title'))
		       ),
		       EXISTS (
		         SELECT 1 FROM app.task_activity later
		         WHERE later.task_id = $2 AND later.user_id = $3
		           AND (later.created_at, later.id) > ($4::timestamptz, $1::uuid)
		           AND (later.event_type IN ('TASK_DESCRIPTION_UPDATED', 'TASK_UPDATED')
		             OR (later.event_type = 'TASK_UNDONE' AND later.new_value->'restored_fields' ? 'description'))
		       ),
		       EXISTS (
		         SELECT 1 FROM app.task_activity later
		         WHERE later.task_id = $2 AND later.user_id = $3
		           AND (later.created_at, later.id) > ($4::timestamptz, $1::uuid)
		           AND (later.event_type = 'TASK_STATUS_CHANGED'
		             OR (later.event_type = 'TASK_UNDONE' AND later.new_value->'restored_fields' ? 'status'))
		       ),
		       EXISTS (
		         SELECT 1 FROM app.task_activity later
		         WHERE later.task_id = $2 AND later.user_id = $3
		           AND (later.created_at, later.id) > ($4::timestamptz, $1::uuid)
		           AND (later.event_type = 'TASK_PRIORITY_CHANGED'
		             OR (later.event_type = 'TASK_UNDONE' AND later.new_value->'restored_fields' ? 'priority'))
		       ),
		       EXISTS (
		         SELECT 1 FROM app.task_activity later
		         WHERE later.task_id = $2 AND later.user_id = $3
		           AND (later.created_at, later.id) > ($4::timestamptz, $1::uuid)
		           AND (later.event_type IN ('TASK_DELETED', 'TASK_RESTORED')
		             OR (later.event_type = 'TASK_UNDONE' AND later.new_value->>'original_event_type' = 'TASK_DELETED'))
		       ),
		       EXISTS (
		         SELECT 1 FROM app.task_activity later
		         WHERE later.task_id = $2 AND later.user_id = $3
		           AND (later.created_at, later.id) > ($4::timestamptz, $1::uuid)
		           AND (later.event_type = 'TASK_DUE_DATE_CHANGED'
		             OR (later.event_type = 'TASK_UNDONE' AND later.new_value->'restored_fields' ? 'due_date'))
		       )`, original.ID, taskID, userID, original.CreatedAt,
	).Scan(&alreadyUndone, &laterTitle, &laterDescription, &laterStatus, &laterPriority, &laterDeletion, &laterDueDate); err != nil {
		return task.Task{}, task.Activity{}, err
	}
	if alreadyUndone {
		return task.Task{}, task.Activity{}, task.ErrUndoAlreadyApplied
	}
	var deletionExpired bool
	if original.EventType == "TASK_DELETED" {
		deletedValue, ok := jsonTime(decodeActivityObject(original.NewValue)["deleted_at"])
		if !ok {
			return task.Task{}, task.Activity{}, task.ErrConflict
		}
		if err := tx.QueryRow(ctx, `SELECT $1::timestamptz < clock_timestamp() - interval '24 hours'`, deletedValue).Scan(&deletionExpired); err != nil {
			return task.Task{}, task.Activity{}, err
		}
	}
	if current.Version != expectedVersion {
		return task.Task{}, task.Activity{}, task.ErrConflict
	}
	archived, err := completedTaskArchived(ctx, tx, current)
	if err != nil {
		return task.Task{}, task.Activity{}, err
	}
	if archived {
		return task.Task{}, task.Activity{}, task.ErrTaskArchived
	}
	if reason := undoUnavailableReason(original, current.Title, current.Description, current.Status, current.Priority, current.DueDate, nullableTime(current.DeletedAt), alreadyUndone, laterTitle, laterDescription, laterStatus, laterPriority, laterDeletion, laterDueDate, deletionExpired); reason != "" {
		switch reason {
		case "unsupported":
			return task.Task{}, task.Activity{}, task.ErrUndoUnsupported
		case "already_undone":
			return task.Task{}, task.Activity{}, task.ErrUndoAlreadyApplied
		case "recovery_expired":
			return task.Task{}, task.Activity{}, task.ErrUndoExpired
		default:
			return task.Task{}, task.Activity{}, task.ErrConflict
		}
	}

	before, restored := undoValues(original)
	var updated task.Task
	switch original.EventType {
	case "TASK_TITLE_UPDATED":
		updated, err = scanTask(tx.QueryRow(ctx, `UPDATE app.tasks SET title = $4, version = version + 1, updated_at = clock_timestamp() WHERE id = $1 AND user_id = $2 AND version = $3 AND deleted_at IS NULL RETURNING `+taskColumns,
			taskID, userID, expectedVersion, restored["title"]))
	case "TASK_DESCRIPTION_UPDATED":
		updated, err = scanTask(tx.QueryRow(ctx, `UPDATE app.tasks SET description = NULLIF($4, ''), version = version + 1, updated_at = clock_timestamp() WHERE id = $1 AND user_id = $2 AND version = $3 AND deleted_at IS NULL RETURNING `+taskColumns,
			taskID, userID, expectedVersion, restored["description"]))
	case "TASK_UPDATED":
		updated, err = scanTask(tx.QueryRow(ctx, `UPDATE app.tasks SET title = $4, description = NULLIF($5, ''), version = version + 1, updated_at = clock_timestamp() WHERE id = $1 AND user_id = $2 AND version = $3 AND deleted_at IS NULL RETURNING `+taskColumns,
			taskID, userID, expectedVersion, restored["title"], restored["description"]))
	case "TASK_STATUS_CHANGED":
		updated, err = scanTask(tx.QueryRow(ctx, `UPDATE app.tasks SET status = $4, completed_at = CASE WHEN $4 = 'DONE' THEN COALESCE(completed_at, clock_timestamp()) ELSE NULL END, version = version + 1, updated_at = clock_timestamp() WHERE id = $1 AND user_id = $2 AND version = $3 AND deleted_at IS NULL RETURNING `+taskColumns,
			taskID, userID, expectedVersion, restored["status"]))
	case "TASK_PRIORITY_CHANGED":
		updated, err = scanTask(tx.QueryRow(ctx, `UPDATE app.tasks SET priority = $4, version = version + 1, updated_at = clock_timestamp() WHERE id = $1 AND user_id = $2 AND version = $3 AND deleted_at IS NULL RETURNING `+taskColumns,
			taskID, userID, expectedVersion, restored["priority"]))
	case "TASK_DUE_DATE_CHANGED":
		updated, err = scanTask(tx.QueryRow(ctx, `UPDATE app.tasks SET due_date = $4::date, version = version + 1, updated_at = clock_timestamp() WHERE id = $1 AND user_id = $2 AND version = $3 AND deleted_at IS NULL RETURNING `+taskColumns,
			taskID, userID, expectedVersion, restored["due_date"]))
	case "TASK_DELETED":
		updated, err = scanTask(tx.QueryRow(ctx, `UPDATE app.tasks SET deleted_at = NULL, version = version + 1, updated_at = clock_timestamp() WHERE id = $1 AND user_id = $2 AND version = $3 AND deleted_at = $4 AND deleted_at >= statement_timestamp() - interval '24 hours' RETURNING `+taskColumns,
			taskID, userID, expectedVersion, current.DeletedAt))
	default:
		return task.Task{}, task.Activity{}, task.ErrUndoUnsupported
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return task.Task{}, task.Activity{}, task.ErrConflict
	}
	if err != nil {
		return task.Task{}, task.Activity{}, err
	}

	undoEvent, err := insertUndoActivity(ctx, tx, userID, taskID, original, before, restored)
	if err != nil {
		return task.Task{}, task.Activity{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return task.Task{}, task.Activity{}, err
	}
	return updated, undoEvent, nil
}

func completedTaskArchived(ctx context.Context, tx pgx.Tx, item task.Task) (bool, error) {
	if item.Status != task.StatusDone || item.CompletedAt == nil {
		return false, nil
	}
	var archived bool
	err := tx.QueryRow(ctx, `SELECT $1::timestamptz <= statement_timestamp() - interval '12 hours'`, *item.CompletedAt).Scan(&archived)
	return archived, err
}

func nullableTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func undoUnavailableReason(event task.Activity, title, description, status, priority string, dueDate *string, deletedAt pgtype.Timestamptz, alreadyUndone, laterTitle, laterDescription, laterStatus, laterPriority, laterDeletion, laterDueDate, deletionExpired bool) string {
	switch event.EventType {
	case "TASK_TITLE_UPDATED", "TASK_DESCRIPTION_UPDATED", "TASK_UPDATED", "TASK_STATUS_CHANGED", "TASK_PRIORITY_CHANGED", "TASK_DUE_DATE_CHANGED", "TASK_DELETED":
	default:
		return "unsupported"
	}
	if alreadyUndone || event.UndoOfEventID != "" {
		return "already_undone"
	}
	values := decodeActivityObject(event.NewValue)
	switch event.EventType {
	case "TASK_TITLE_UPDATED":
		if deletedAt.Valid {
			return "task_deleted"
		}
		if laterTitle || jsonString(values["title"]) != title {
			return "changed_since_event"
		}
	case "TASK_DESCRIPTION_UPDATED":
		if deletedAt.Valid {
			return "task_deleted"
		}
		if laterDescription || jsonString(values["description"]) != description {
			return "changed_since_event"
		}
	case "TASK_UPDATED":
		if deletedAt.Valid {
			return "task_deleted"
		}
		if laterTitle || laterDescription || jsonString(values["title"]) != title || jsonString(values["description"]) != description {
			return "changed_since_event"
		}
	case "TASK_STATUS_CHANGED":
		if deletedAt.Valid {
			return "task_deleted"
		}
		if laterStatus || jsonString(values["status"]) != status {
			return "changed_since_event"
		}
	case "TASK_PRIORITY_CHANGED":
		if deletedAt.Valid {
			return "task_deleted"
		}
		if laterPriority || jsonString(values["priority"]) != priority {
			return "changed_since_event"
		}
	case "TASK_DUE_DATE_CHANGED":
		if deletedAt.Valid || laterDueDate || !sameDueDate(dueDate, jsonNullableString(values["due_date"])) {
			return "changed_since_event"
		}
	case "TASK_DELETED":
		if !deletedAt.Valid {
			return "task_restored"
		}
		deletedValue, ok := jsonTime(values["deleted_at"])
		if !ok || !deletedAt.Time.Equal(deletedValue) || laterDeletion {
			return "changed_since_event"
		}
		if deletionExpired {
			return "recovery_expired"
		}
	}
	return ""
}

func decodeActivityObject(raw json.RawMessage) map[string]json.RawMessage {
	values := make(map[string]json.RawMessage)
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil {
		return values
	}
	return values
}

func jsonString(value json.RawMessage) string {
	var result string
	_ = json.Unmarshal(value, &result)
	return result
}

func jsonTime(value json.RawMessage) (time.Time, bool) {
	var result time.Time
	if len(value) == 0 || json.Unmarshal(value, &result) != nil || result.IsZero() {
		return time.Time{}, false
	}
	return result, true
}

func undoValues(event task.Activity) (map[string]any, map[string]any) {
	previous := decodeActivityObject(event.NewValue)
	restored := decodeActivityObject(event.PreviousValue)
	before := make(map[string]any)
	values := make(map[string]any)
	fields := []string{}
	switch event.EventType {
	case "TASK_TITLE_UPDATED":
		fields = []string{"title"}
	case "TASK_DESCRIPTION_UPDATED":
		fields = []string{"description"}
	case "TASK_UPDATED":
		fields = []string{"title", "description"}
	case "TASK_STATUS_CHANGED":
		fields = []string{"status"}
	case "TASK_PRIORITY_CHANGED":
		fields = []string{"priority"}
	case "TASK_DUE_DATE_CHANGED":
		fields = []string{"due_date"}
	case "TASK_DELETED":
		fields = []string{"deleted_at"}
	}
	for _, field := range fields {
		var oldValue, newValue any
		_ = json.Unmarshal(previous[field], &oldValue)
		_ = json.Unmarshal(restored[field], &newValue)
		before[field] = oldValue
		values[field] = newValue
	}
	return before, values
}

func insertUndoActivity(ctx context.Context, tx pgx.Tx, userID, taskID string, original task.Activity, before, restored map[string]any) (task.Activity, error) {
	previousJSON, err := json.Marshal(before)
	if err != nil {
		return task.Activity{}, err
	}
	newJSON, err := json.Marshal(map[string]any{
		"original_event_id":   original.ID,
		"original_event_type": original.EventType,
		"restored_fields":     restored,
	})
	if err != nil {
		return task.Activity{}, err
	}
	var event task.Activity
	var previous, current []byte
	err = tx.QueryRow(ctx, `
		INSERT INTO app.task_activity (task_id, user_id, actor_user_id, event_type, previous_value, new_value, undo_of_event_id)
		VALUES ($1, $2, $2, 'TASK_UNDONE', $3::jsonb, $4::jsonb, $5)
		RETURNING id::text, task_id::text, actor_user_id::text, event_type, previous_value, new_value, created_at, undo_of_event_id::text`,
		taskID, userID, previousJSON, newJSON, original.ID,
	).Scan(&event.ID, &event.TaskID, &event.ActorUserID, &event.EventType, &previous, &current, &event.CreatedAt, &event.UndoOfEventID)
	if err != nil {
		return task.Activity{}, err
	}
	event.PreviousValue = json.RawMessage(previous)
	event.NewValue = json.RawMessage(current)
	return event, nil
}

func insertActivity(ctx context.Context, tx pgx.Tx, userID, taskID, eventType string, previous, current any) error {
	var previousJSON, currentJSON []byte
	var err error
	if previous != nil {
		previousJSON, err = json.Marshal(previous)
		if err != nil {
			return err
		}
	}
	if current != nil {
		currentJSON, err = json.Marshal(current)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO app.task_activity (task_id, user_id, actor_user_id, event_type, previous_value, new_value)
		VALUES ($1, $2, $2, $3, $4::jsonb, $5::jsonb)`, taskID, userID, eventType, previousJSON, currentJSON)
	return err
}

func sameDueDate(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func jsonNullableString(raw json.RawMessage) *string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return &value
}

func (r *TaskRepository) ownedConflictOrNotFound(ctx context.Context, userID, taskID string) error {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM app.tasks WHERE id = $1 AND user_id = $2
		)`, taskID, userID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return task.ErrNotFound
	}
	return task.ErrConflict
}

type rowScanner interface {
	Scan(...any) error
}

func scanTask(row rowScanner) (task.Task, error) {
	var item task.Task
	var dueDate pgtype.Date
	var completedAt pgtype.Timestamptz
	var deletedAt pgtype.Timestamptz
	err := row.Scan(
		&item.ID, &item.Title, &item.Description, &item.Status, &item.Priority, &dueDate, &completedAt,
		&item.Version, &item.CreatedAt, &item.UpdatedAt, &deletedAt, &item.IsOverdue,
	)
	if err != nil {
		return task.Task{}, err
	}
	if completedAt.Valid {
		value := completedAt.Time
		item.CompletedAt = &value
	}
	if dueDate.Valid && dueDate.InfinityModifier == pgtype.Finite {
		value := dueDate.Time.Format("2006-01-02")
		item.DueDate = &value
	}
	if deletedAt.Valid {
		value := deletedAt.Time
		item.DeletedAt = &value
	}
	return item, nil
}

func scanTaskRow(rows pgx.Rows) (task.Task, error) {
	return scanTask(rows)
}
