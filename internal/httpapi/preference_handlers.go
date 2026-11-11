package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

type preferenceService interface {
	TimeZone(context.Context, string) (*string, error)
	SetTimeZone(context.Context, string, *string) error
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
	writeJSONStatus(w, http.StatusOK, map[string]*string{"time_zone": zone})
}

func (h *PreferenceHandlers) update(w http.ResponseWriter, r *http.Request) {
	user, ok := authenticatedUser(r)
	if !ok {
		writeAuthError(w, auth.ErrUnauthenticated)
		return
	}
	var input struct {
		TimeZone json.RawMessage `json:"time_zone"`
	}
	if !decodeTaskJSON(w, r, &input) {
		return
	}
	if len(input.TimeZone) == 0 {
		writeTaskError(w, task.ErrInvalidInput)
		return
	}
	var zone *string
	if string(input.TimeZone) != "null" {
		var value string
		if err := json.Unmarshal(input.TimeZone, &value); err != nil || !task.ValidTimeZone(value) {
			writeTaskError(w, task.ErrInvalidInput)
			return
		}
		zone = &value
	}
	if err := h.service.SetTimeZone(r.Context(), user.ID, zone); err != nil {
		writeTaskError(w, task.ErrStorage)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]*string{"time_zone": zone})
}
