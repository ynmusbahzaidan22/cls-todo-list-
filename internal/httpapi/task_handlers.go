package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

const maxTaskBodyBytes = 64 * 1024

type TaskService interface {
	Create(context.Context, string, string, string, string, ...string) (task.Task, error)
	List(context.Context, string, int, string, string, string, ...string) (task.Page, error)
	ListCompleted(context.Context, string, int, string, string, string) (task.Page, error)
	ListDeleted(context.Context, string, int, string) (task.Page, error)
	CountDeleted(context.Context, string) (int, error)
	Get(context.Context, string, string) (task.Task, error)
	Update(context.Context, string, string, int64, string, string, string, ...string) (task.Task, error)
	Delete(context.Context, string, string, int64) (task.Task, error)
	Restore(context.Context, string, string, int64) (task.Task, error)
	Activity(context.Context, string, string, int, string) (task.ActivityPage, error)
	Undo(context.Context, string, string, string, int64) (task.Task, task.Activity, error)
	Bulk(context.Context, string, task.BulkOperation) ([]task.Task, error)
}

type TaskHandlers struct {
	service TaskService
}

func NewTaskHandlers(service TaskService) *TaskHandlers {
	return &TaskHandlers{service: service}
}

type taskResponse struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	Priority    string     `json:"priority"`
	DueDate     *string    `json:"due_date"`
	IsOverdue   bool       `json:"is_overdue"`
	CompletedAt *time.Time `json:"completed_at"`
	Version     int64      `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at"`
}

type taskPageResponse struct {
	Items      []taskResponse `json:"items"`
	NextCursor *string        `json:"next_cursor"`
	TotalCount *int           `json:"total_count,omitempty"`
}

type createTaskRequest struct {
	Title       *string         `json:"title"`
	Description *string         `json:"description"`
	Status      *string         `json:"status"`
	Priority    *string         `json:"priority"`
	DueDate     json.RawMessage `json:"due_date"`
}

type updateTaskRequest struct {
	Title       *string         `json:"title"`
	Description *string         `json:"description"`
	Status      *string         `json:"status"`
	Priority    *string         `json:"priority"`
	Version     *int64          `json:"version"`
	DueDate     json.RawMessage `json:"due_date"`
}

type taskVersionRequest struct {
	Version *int64 `json:"version"`
}

type bulkTaskInput struct {
	ID      string `json:"id"`
	Version *int64 `json:"version"`
}

type bulkTaskRequest struct {
	Action string          `json:"action"`
	Status string          `json:"status"`
	Tasks  []bulkTaskInput `json:"tasks"`
}

type bulkTaskResponse struct {
	Items []taskResponse `json:"items"`
}

type taskAPIError struct {
	Error taskAPIErrorDetails `json:"error"`
}

type taskAPIErrorDetails struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (h *TaskHandlers) create(w http.ResponseWriter, r *http.Request) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	var input createTaskRequest
	if !decodeTaskJSON(w, r, &input) {
		return
	}
	if input.Title == nil {
		writeTaskError(w, task.ErrInvalidInput)
		return
	}
	description := ""
	if input.Description != nil {
		description = *input.Description
	}
	status := task.StatusTODO
	if input.Status != nil {
		status = *input.Status
	}
	priority := task.PriorityMedium
	if input.Priority != nil {
		priority = *input.Priority
	}
	dueOption, dueErr := dueDateOption(input.DueDate, true)
	if dueErr != nil {
		writeTaskError(w, task.ErrInvalidInput)
		return
	}
	created, err := h.service.Create(r.Context(), user.ID, *input.Title, description, status, priority, dueOption)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	path := "/api/tasks/" + created.ID
	w.Header().Set("Location", path)
	writeJSONStatus(w, http.StatusCreated, toTaskResponse(created))
}

func (h *TaskHandlers) bulk(w http.ResponseWriter, r *http.Request) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	var input bulkTaskRequest
	if !decodeTaskJSON(w, r, &input) {
		return
	}
	operation := task.BulkOperation{Action: input.Action, Status: input.Status, Tasks: make([]task.BulkTaskInput, 0, len(input.Tasks))}
	for _, item := range input.Tasks {
		if item.Version == nil {
			writeTaskError(w, task.ErrInvalidInput)
			return
		}
		operation.Tasks = append(operation.Tasks, task.BulkTaskInput{ID: item.ID, Version: *item.Version})
	}
	updated, err := h.service.Bulk(r.Context(), user.ID, operation)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	response := bulkTaskResponse{Items: make([]taskResponse, 0, len(updated))}
	for _, item := range updated {
		response.Items = append(response.Items, toTaskResponse(item))
	}
	writeJSONStatus(w, http.StatusOK, response)
}

func (h *TaskHandlers) list(w http.ResponseWriter, r *http.Request) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	limit := task.DefaultPageSize
	query := r.URL.Query()
	if query.Has("limit") {
		parsed, err := strconv.Atoi(query.Get("limit"))
		if err != nil || parsed < 1 || parsed > task.MaxPageSize {
			writeTaskQueryError(w)
			return
		}
		limit = parsed
	}
	if len(query["cursor"]) > 1 || len(query["limit"]) > 1 || len(query["q"]) > 1 || len(query["status"]) > 1 || len(query["priority"]) > 1 || len(query["due"]) > 1 || len(query["tz"]) > 1 {
		writeTaskQueryError(w)
		return
	}
	page, err := h.service.List(r.Context(), user.ID, limit, query.Get("q"), query.Get("status"), query.Get("cursor"), query.Get("priority"), query.Get("due"), query.Get("tz"))
	if err != nil {
		if errors.Is(err, task.ErrInvalidInput) {
			writeTaskQueryError(w)
			return
		}
		writeTaskError(w, err)
		return
	}
	response := taskPageResponse{Items: make([]taskResponse, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, item := range page.Items {
		response.Items = append(response.Items, toTaskResponse(item))
	}
	writeJSONStatus(w, http.StatusOK, response)
}

func (h *TaskHandlers) listDeleted(w http.ResponseWriter, r *http.Request) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	limit := task.DefaultPageSize
	query := r.URL.Query()
	if query.Has("limit") {
		parsed, err := strconv.Atoi(query.Get("limit"))
		if err != nil || parsed < 1 || parsed > task.MaxPageSize {
			writeTaskQueryError(w)
			return
		}
		limit = parsed
	}
	if len(query["cursor"]) > 1 || len(query["limit"]) > 1 {
		writeTaskQueryError(w)
		return
	}
	page, err := h.service.ListDeleted(r.Context(), user.ID, limit, query.Get("cursor"))
	if err != nil {
		if errors.Is(err, task.ErrInvalidInput) {
			writeTaskQueryError(w)
			return
		}
		writeTaskError(w, err)
		return
	}
	response := taskPageResponse{Items: make([]taskResponse, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, item := range page.Items {
		response.Items = append(response.Items, toTaskResponse(item))
	}
	writeJSONStatus(w, http.StatusOK, response)
}

func (h *TaskHandlers) listCompleted(w http.ResponseWriter, r *http.Request) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	limit := task.DefaultPageSize
	query := r.URL.Query()
	if query.Has("limit") {
		parsed, err := strconv.Atoi(query.Get("limit"))
		if err != nil || parsed < 1 || parsed > task.MaxPageSize {
			writeTaskQueryError(w)
			return
		}
		limit = parsed
	}
	period := query.Get("period")
	if period == "" {
		period = "all"
	}
	zone := query.Get("tz")
	if len(query["cursor"]) > 1 || len(query["limit"]) > 1 || len(query["period"]) > 1 || len(query["tz"]) > 1 {
		writeTaskQueryError(w)
		return
	}
	page, err := h.service.ListCompleted(r.Context(), user.ID, limit, query.Get("cursor"), period, zone)
	if err != nil {
		if errors.Is(err, task.ErrInvalidInput) {
			writeTaskQueryError(w)
			return
		}
		writeTaskError(w, err)
		return
	}
	totalCount := page.TotalCount
	response := taskPageResponse{Items: make([]taskResponse, 0, len(page.Items)), NextCursor: page.NextCursor, TotalCount: &totalCount}
	for _, item := range page.Items {
		response.Items = append(response.Items, toTaskResponse(item))
	}
	writeJSONStatus(w, http.StatusOK, response)
}

func (h *TaskHandlers) deletedCount(w http.ResponseWriter, r *http.Request) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	count, err := h.service.CountDeleted(r.Context(), user.ID)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, struct {
		Count int `json:"count"`
	}{Count: count})
}

func (h *TaskHandlers) get(w http.ResponseWriter, r *http.Request) {
	user, taskID, ok := taskRequestIdentity(w, r)
	if !ok {
		return
	}
	item, err := h.service.Get(r.Context(), user.ID, taskID)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, toTaskResponse(item))
}

func (h *TaskHandlers) activity(w http.ResponseWriter, r *http.Request) {
	user, taskID, ok := taskRequestIdentity(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	if len(query["limit"]) > 1 || len(query["cursor"]) > 1 {
		writeTaskQueryError(w)
		return
	}
	limit := task.DefaultPageSize
	if query.Has("limit") {
		parsed, err := strconv.Atoi(query.Get("limit"))
		if err != nil || parsed < 1 || parsed > task.MaxPageSize {
			writeTaskQueryError(w)
			return
		}
		limit = parsed
	}
	page, err := h.service.Activity(r.Context(), user.ID, taskID, limit, query.Get("cursor"))
	if err != nil {
		if errors.Is(err, task.ErrInvalidInput) {
			writeTaskQueryError(w)
			return
		}
		writeTaskError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, page)
}

type undoTaskResponse struct {
	Task     taskResponse  `json:"task"`
	Activity task.Activity `json:"activity"`
}

func (h *TaskHandlers) undoActivity(w http.ResponseWriter, r *http.Request) {
	user, taskID, ok := taskRequestIdentity(w, r)
	if !ok {
		return
	}
	eventID := r.PathValue("eventID")
	if !task.ValidID(eventID) {
		writeTaskBadID(w)
		return
	}
	var input taskVersionRequest
	if !decodeTaskJSON(w, r, &input) {
		return
	}
	if input.Version == nil {
		writeTaskError(w, task.ErrInvalidInput)
		return
	}
	updated, activity, err := h.service.Undo(r.Context(), user.ID, taskID, strings.ToLower(eventID), *input.Version)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, undoTaskResponse{Task: toTaskResponse(updated), Activity: activity})
}

func (h *TaskHandlers) update(w http.ResponseWriter, r *http.Request) {
	user, taskID, ok := taskRequestIdentity(w, r)
	if !ok {
		return
	}
	var input updateTaskRequest
	if !decodeTaskJSON(w, r, &input) {
		return
	}
	if input.Title == nil || input.Description == nil || input.Status == nil || input.Version == nil {
		writeTaskError(w, task.ErrInvalidInput)
		return
	}
	var updated task.Task
	var err error
	options := []string{}
	if input.Priority != nil {
		options = append(options, *input.Priority)
	}
	if len(input.DueDate) > 0 {
		dueOption, dueErr := dueDateOption(input.DueDate, false)
		if dueErr != nil {
			writeTaskError(w, task.ErrInvalidInput)
			return
		}
		if len(options) == 0 {
			options = append(options, "__PRIORITY_PRESERVE__")
		}
		options = append(options, dueOption)
	}
	updated, err = h.service.Update(r.Context(), user.ID, taskID, *input.Version, *input.Title, *input.Description, *input.Status, options...)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, toTaskResponse(updated))
}

func (h *TaskHandlers) delete(w http.ResponseWriter, r *http.Request) {
	user, taskID, ok := taskRequestIdentity(w, r)
	if !ok {
		return
	}
	var input taskVersionRequest
	if !decodeTaskJSON(w, r, &input) {
		return
	}
	if input.Version == nil {
		writeTaskError(w, task.ErrInvalidInput)
		return
	}
	deleted, err := h.service.Delete(r.Context(), user.ID, taskID, *input.Version)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, toTaskResponse(deleted))
}

func (h *TaskHandlers) restore(w http.ResponseWriter, r *http.Request) {
	user, taskID, ok := taskRequestIdentity(w, r)
	if !ok {
		return
	}
	var input taskVersionRequest
	if !decodeTaskJSON(w, r, &input) {
		return
	}
	if input.Version == nil {
		writeTaskError(w, task.ErrInvalidInput)
		return
	}
	restored, err := h.service.Restore(r.Context(), user.ID, taskID, *input.Version)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, toTaskResponse(restored))
}

func taskRequestIdentity(w http.ResponseWriter, r *http.Request) (auth.User, string, bool) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return auth.User{}, "", false
	}
	taskID := r.PathValue("taskID")
	if !task.ValidID(taskID) {
		writeTaskBadID(w)
		return auth.User{}, "", false
	}
	return user, strings.ToLower(taskID), true
}

func authenticatedUser(r *http.Request) (auth.User, bool) {
	user, ok := r.Context().Value(userContextKey{}).(auth.User)
	return user, ok && user.ID != ""
}

func decodeTaskJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	mediaType, _, mediaTypeErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaTypeErr != nil || !strings.EqualFold(mediaType, "application/json") {
		writeTaskMalformedBody(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeTaskMalformedBody(w, http.StatusRequestEntityTooLarge, "request_too_large", "The request body is too large.")
		} else {
			writeTaskError(w, task.ErrInvalidInput)
		}
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeTaskError(w, task.ErrInvalidInput)
		return false
	}
	return true
}

func toTaskResponse(item task.Task) taskResponse {
	return taskResponse{
		ID: item.ID, Title: item.Title, Description: item.Description, Status: item.Status, Priority: item.Priority,
		DueDate: item.DueDate, IsOverdue: item.IsOverdue,
		CompletedAt: item.CompletedAt, Version: item.Version, CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt, DeletedAt: item.DeletedAt,
	}
}

func dueDateOption(raw json.RawMessage, create bool) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		if create {
			return "__DUE_NONE__", nil
		}
		return "__DUE_NONE__", nil
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !task.ValidDueDate(value) {
		return "", task.ErrInvalidInput
	}
	return value, nil
}

func writeTaskError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code, message := "internal_error", "The request could not be completed."
	switch {
	case errors.Is(err, task.ErrInvalidInput):
		status, code, message = http.StatusUnprocessableEntity, "invalid_task", "The task data is invalid."
	case errors.Is(err, task.ErrNotFound):
		status, code, message = http.StatusNotFound, "task_not_found", "The task was not found."
	case errors.Is(err, task.ErrConflict):
		status, code, message = http.StatusConflict, "version_conflict", "The task changed. Refresh it and try again."
	case errors.Is(err, task.ErrTaskArchived):
		status, code, message = http.StatusConflict, "task_archived", "This task has been completed for more than 12 hours and is read-only."
	case errors.Is(err, task.ErrRecoveryOver):
		status, code, message = http.StatusConflict, "recovery_expired", "The task can no longer be restored."
	case errors.Is(err, task.ErrUndoUnsupported):
		status, code, message = http.StatusUnprocessableEntity, "undo_unsupported", "This activity cannot be undone."
	case errors.Is(err, task.ErrUndoAlreadyApplied):
		status, code, message = http.StatusConflict, "undo_already_applied", "This activity has already been undone."
	case errors.Is(err, task.ErrUndoExpired):
		status, code, message = http.StatusConflict, "undo_expired", "The deletion can no longer be undone."
	case errors.Is(err, task.ErrStorage):
		status, code, message = http.StatusInternalServerError, "internal_error", "The request could not be completed."
	}
	writeTaskMalformedBody(w, status, code, message)
}

func writeTaskBadID(w http.ResponseWriter) {
	writeTaskMalformedBody(w, http.StatusBadRequest, "invalid_task_id", "The task ID is invalid.")
}

func writeTaskQueryError(w http.ResponseWriter) {
	writeTaskMalformedBody(w, http.StatusBadRequest, "invalid_pagination", "The task list query parameters are invalid.")
}

func writeTaskMalformedBody(w http.ResponseWriter, status int, code, message string) {
	writeJSONStatus(w, status, taskAPIError{Error: taskAPIErrorDetails{Code: code, Message: message}})
}
