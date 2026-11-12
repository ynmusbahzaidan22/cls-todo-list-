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

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/admin"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
)

type AdminService interface {
	ListUsers(context.Context, int, string) (admin.UserPage, error)
	ListUsersForActor(context.Context, string, string, int, string) (admin.UserPage, error)
	ListTasks(context.Context, int, string, string, string) (admin.TaskPage, error)
	ChangeUserRole(context.Context, string, string, string) (admin.MutationResult, error)
	ChangeUserStatus(context.Context, string, string, string) (admin.MutationResult, error)
	RevokeUserSessions(context.Context, string, string) (admin.MutationResult, error)
}

type AdminHandlers struct {
	service AdminService
}

func NewAdminHandlers(service AdminService) *AdminHandlers {
	return &AdminHandlers{service: service}
}

type adminUserResponse struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Email             string `json:"email"`
	Role              string `json:"role"`
	Status            string `json:"status"`
	CreatedAt         string `json:"created_at"`
	CanChangeRole     bool   `json:"can_change_role"`
	CanChangeStatus   bool   `json:"can_change_status"`
	CanRevokeSessions bool   `json:"can_revoke_sessions"`
	ProtectionReason  string `json:"protection_reason,omitempty"`
}

type adminMutationResponse struct {
	User            *adminManagedUser `json:"user,omitempty"`
	RevokedSessions *int64            `json:"revoked_sessions,omitempty"`
}
type adminManagedUser struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

type adminTaskResponse struct {
	ID          string  `json:"id"`
	OwnerID     string  `json:"owner_id"`
	OwnerName   string  `json:"owner_name"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	DueDate     *string `json:"due_date"`
	IsOverdue   bool    `json:"is_overdue"`
	CompletedAt any     `json:"completed_at"`
	Version     int64   `json:"version"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	DeletedAt   any     `json:"deleted_at"`
}

type adminUserPageResponse struct {
	Items      []adminUserResponse `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

type adminTaskPageResponse struct {
	Items      []adminTaskResponse `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

func (h *AdminHandlers) listUsers(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if !onlyQueryKeys(query, "limit", "cursor", "q") || len(query["limit"]) > 1 || len(query["cursor"]) > 1 || len(query["q"]) > 1 {
		writeTaskQueryError(w)
		return
	}
	limit, ok := adminPageLimit(query.Get("limit"))
	if !ok {
		writeTaskQueryError(w)
		return
	}
	actor, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	page, err := h.service.ListUsersForActor(r.Context(), actor.ID, query.Get("q"), limit, query.Get("cursor"))
	if err != nil {
		writeAdminError(w, err)
		return
	}
	response := adminUserPageResponse{Items: make([]adminUserResponse, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, user := range page.Items {
		response.Items = append(response.Items, adminUserResponse{
			ID: user.ID, Name: user.Name, Email: user.Email, Role: user.Role, Status: user.Status, CreatedAt: user.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
			CanChangeRole: user.CanChangeRole, CanChangeStatus: user.CanChangeStatus, CanRevokeSessions: user.CanRevokeSessions, ProtectionReason: user.ProtectionReason,
		})
	}
	writeJSONStatus(w, http.StatusOK, response)
}

func (h *AdminHandlers) changeUserRole(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Role string `json:"role"`
	}
	if !decodeAdminMutation(w, r, &input) {
		return
	}
	actor, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	result, err := h.service.ChangeUserRole(r.Context(), actor.ID, r.PathValue("userID"), input.Role)
	if err != nil {
		writeAdminMutationError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, adminMutationResponse{User: &adminManagedUser{ID: result.User.ID, Role: result.User.Role, Status: result.User.Status}})
}

func (h *AdminHandlers) changeUserStatus(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Status string `json:"status"`
	}
	if !decodeAdminMutation(w, r, &input) {
		return
	}
	actor, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	result, err := h.service.ChangeUserStatus(r.Context(), actor.ID, r.PathValue("userID"), input.Status)
	if err != nil {
		writeAdminMutationError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, adminMutationResponse{User: &adminManagedUser{ID: result.User.ID, Role: result.User.Role, Status: result.User.Status}})
}

func (h *AdminHandlers) revokeUserSessions(w http.ResponseWriter, r *http.Request) {
	actor, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	result, err := h.service.RevokeUserSessions(r.Context(), actor.ID, r.PathValue("userID"))
	if err != nil {
		writeAdminMutationError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, adminMutationResponse{RevokedSessions: &result.RevokedSessions})
}

func decodeAdminMutation(w http.ResponseWriter, r *http.Request, destination any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeTaskMalformedBody(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeAdminInputError(w)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAdminInputError(w)
		return false
	}
	return true
}

func writeAdminInputError(w http.ResponseWriter) {
	writeTaskMalformedBody(w, http.StatusUnprocessableEntity, "invalid_request", "The request is invalid.")
}

func writeAdminMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, admin.ErrInvalidInput):
		writeAdminInputError(w)
	case errors.Is(err, admin.ErrForbidden):
		writeTaskMalformedBody(w, http.StatusForbidden, "forbidden", "This account-management action is not permitted.")
	case errors.Is(err, admin.ErrNotFound):
		writeTaskMalformedBody(w, http.StatusNotFound, "not_found", "The account was not found.")
	case errors.Is(err, admin.ErrConflict):
		writeTaskMalformedBody(w, http.StatusConflict, "last_active_admin", "The final active administrator cannot be disabled or demoted.")
	default:
		writeTaskMalformedBody(w, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
	}
}

func (h *AdminHandlers) listTasks(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if !onlyQueryKeys(query, "limit", "cursor", "status", "state") || len(query["limit"]) > 1 || len(query["cursor"]) > 1 || len(query["status"]) > 1 || len(query["state"]) > 1 {
		writeTaskQueryError(w)
		return
	}
	limit, ok := adminPageLimit(query.Get("limit"))
	if !ok {
		writeTaskQueryError(w)
		return
	}
	status, state := query.Get("status"), query.Get("state")
	if (status != "" && status != "TODO" && status != "IN_PROGRESS" && status != "DONE") ||
		(state != "" && state != "active" && state != "deleted" && state != "all") {
		writeTaskQueryError(w)
		return
	}
	page, err := h.service.ListTasks(r.Context(), limit, query.Get("cursor"), status, state)
	if err != nil {
		writeAdminError(w, err)
		return
	}
	response := adminTaskPageResponse{Items: make([]adminTaskResponse, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, item := range page.Items {
		response.Items = append(response.Items, adminTaskResponse{
			ID: item.ID, OwnerID: item.OwnerID, OwnerName: item.OwnerName,
			Title: item.Title, Description: item.Description, Status: item.Status, Priority: item.Priority,
			DueDate: item.DueDate, IsOverdue: item.IsOverdue, CompletedAt: item.CompletedAt,
			Version: item.Version, CreatedAt: item.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
			UpdatedAt: item.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"), DeletedAt: item.DeletedAt,
		})
	}
	writeJSONStatus(w, http.StatusOK, response)
}

func adminPageLimit(raw string) (int, bool) {
	if raw == "" {
		return admin.DefaultPageSize, true
	}
	limit, err := strconv.Atoi(raw)
	return limit, err == nil && limit >= 1 && limit <= admin.MaxPageSize
}

func onlyQueryKeys(query map[string][]string, allowed ...string) bool {
	allowlist := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowlist[key] = struct{}{}
	}
	for key := range query {
		if _, ok := allowlist[key]; !ok {
			return false
		}
	}
	return true
}

func writeAdminError(w http.ResponseWriter, err error) {
	if errors.Is(err, admin.ErrInvalidInput) {
		writeTaskQueryError(w)
		return
	}
	writeTaskMalformedBody(w, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
}
