package httpapi

import (
	"context"
	"net/http"

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

type preferenceService interface {
	TimeZone(context.Context, string) (string, error)
	SetTimeZone(context.Context, string, string) error
}

type PreferenceHandlers struct{ service preferenceService }

func NewPreferenceHandlers(service preferenceService) *PreferenceHandlers {
	return &PreferenceHandlers{service: service}
}

func (h *PreferenceHandlers) get(w http.ResponseWriter, r *http.Request) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	zone, err := h.service.TimeZone(r.Context(), user.ID)
	if err != nil {
		writeTaskError(w, task.ErrStorage)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]string{"time_zone": zone})
}

func (h *PreferenceHandlers) update(w http.ResponseWriter, r *http.Request) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	var input struct {
		TimeZone *string `json:"time_zone"`
	}
	if !decodeTaskJSON(w, r, &input) {
		return
	}
	if input.TimeZone == nil || !task.ValidTimeZone(*input.TimeZone) {
		writeTaskError(w, task.ErrInvalidInput)
		return
	}
	if err := h.service.SetTimeZone(r.Context(), user.ID, *input.TimeZone); err != nil {
		writeTaskError(w, task.ErrStorage)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]string{"time_zone": *input.TimeZone})
}
