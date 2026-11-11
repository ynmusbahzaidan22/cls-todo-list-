package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

type TemplateService interface {
	Create(context.Context, string, string, string, []task.TemplateItem) (task.Template, error)
	List(context.Context, string) ([]task.TemplateSummary, error)
	Get(context.Context, string, string) (task.Template, error)
	Update(context.Context, string, string, int64, string, string, []task.TemplateItem) (task.Template, error)
	Delete(context.Context, string, string, int64) error
	Apply(context.Context, string, string, int64, bool, ...string) ([]task.Task, error)
}

type TemplateHandlers struct{ service TemplateService }

func NewTemplateHandlers(service TemplateService) *TemplateHandlers {
	return &TemplateHandlers{service: service}
}

type templateMutationRequest struct {
	Name        *string             `json:"name"`
	Description *string             `json:"description"`
	Items       []task.TemplateItem `json:"items"`
	Version     *int64              `json:"version"`
}

type templateVersionRequest struct {
	Version  *int64  `json:"version"`
	Mode     string  `json:"mode,omitempty"`
	TimeZone *string `json:"time_zone,omitempty"`
}
type templateListResponse struct {
	Items []task.TemplateSummary `json:"items"`
}
type applyTemplateResponse struct {
	Items []taskResponse `json:"items"`
}

func (h *TemplateHandlers) list(w http.ResponseWriter, r *http.Request) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	items, err := h.service.List(r.Context(), user.ID)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, templateListResponse{Items: items})
}

func (h *TemplateHandlers) create(w http.ResponseWriter, r *http.Request) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	var input templateMutationRequest
	if !decodeTaskJSON(w, r, &input) {
		return
	}
	if input.Name == nil || input.Items == nil || input.Version != nil {
		writeTemplateError(w, task.ErrInvalidInput)
		return
	}
	description := ""
	if input.Description != nil {
		description = *input.Description
	}
	created, err := h.service.Create(r.Context(), user.ID, *input.Name, description, input.Items)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	w.Header().Set("Location", "/api/task-templates/"+created.ID)
	writeJSONStatus(w, http.StatusCreated, created)
}

func (h *TemplateHandlers) get(w http.ResponseWriter, r *http.Request) {
	user, id, ok := templateRequestIdentity(w, r)
	if !ok {
		return
	}
	item, err := h.service.Get(r.Context(), user.ID, id)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, item)
}

func (h *TemplateHandlers) update(w http.ResponseWriter, r *http.Request) {
	user, id, ok := templateRequestIdentity(w, r)
	if !ok {
		return
	}
	var input templateMutationRequest
	if !decodeTaskJSON(w, r, &input) {
		return
	}
	if input.Name == nil || input.Items == nil || input.Version == nil {
		writeTemplateError(w, task.ErrInvalidInput)
		return
	}
	description := ""
	if input.Description != nil {
		description = *input.Description
	}
	updated, err := h.service.Update(r.Context(), user.ID, id, *input.Version, *input.Name, description, input.Items)
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, updated)
}

func (h *TemplateHandlers) delete(w http.ResponseWriter, r *http.Request) {
	user, id, ok := templateRequestIdentity(w, r)
	if !ok {
		return
	}
	var input templateVersionRequest
	if !decodeTaskJSON(w, r, &input) {
		return
	}
	if input.Version == nil {
		writeTemplateError(w, task.ErrInvalidInput)
		return
	}
	if err := h.service.Delete(r.Context(), user.ID, id, *input.Version); err != nil {
		writeTemplateError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *TemplateHandlers) apply(w http.ResponseWriter, r *http.Request) {
	user, id, ok := templateRequestIdentity(w, r)
	if !ok {
		return
	}
	var input templateVersionRequest
	if !decodeTaskJSON(w, r, &input) {
		return
	}
	if input.Version == nil || (input.Mode != "" && input.Mode != "incremental" && input.Mode != "new_set") || (input.TimeZone != nil && !task.ValidTimeZone(*input.TimeZone)) {
		writeTemplateError(w, task.ErrInvalidInput)
		return
	}
	var items []task.Task
	var err error
	if input.TimeZone == nil {
		items, err = h.service.Apply(r.Context(), user.ID, id, *input.Version, input.Mode == "new_set")
	} else {
		items, err = h.service.Apply(r.Context(), user.ID, id, *input.Version, input.Mode == "new_set", *input.TimeZone)
	}
	if err != nil {
		writeTemplateError(w, err)
		return
	}
	response := applyTemplateResponse{Items: make([]taskResponse, 0, len(items))}
	for _, item := range items {
		response.Items = append(response.Items, toTaskResponse(item))
	}
	if len(items) == 0 {
		writeJSONStatus(w, http.StatusOK, response)
		return
	}
	writeJSONStatus(w, http.StatusCreated, response)
}

func templateRequestIdentity(w http.ResponseWriter, r *http.Request) (auth.User, string, bool) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return auth.User{}, "", false
	}
	id := r.PathValue("templateID")
	if !task.ValidID(id) {
		writeTaskBadID(w)
		return auth.User{}, "", false
	}
	return user, id, true
}

func writeTemplateError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "The request could not be completed."
	switch {
	case errors.Is(err, task.ErrInvalidInput):
		status, code, message = http.StatusUnprocessableEntity, "invalid_template", "The template data is invalid."
	case errors.Is(err, task.ErrNotFound):
		status, code, message = http.StatusNotFound, "template_not_found", "The template was not found."
	case errors.Is(err, task.ErrConflict):
		status, code, message = http.StatusConflict, "version_conflict", "The template changed. Refresh it and try again."
	case errors.Is(err, task.ErrStorage):
		status, code, message = http.StatusInternalServerError, "internal_error", "The request could not be completed."
	}
	writeTaskMalformedBody(w, status, code, message)
}
