package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

func TestTaskResponseNormalizesInstantsToUTC(t *testing.T) {
	zone := time.FixedZone("offset", 5*60*60+30*60)
	instant := time.Date(2027, time.April, 5, 12, 0, 0, 0, zone)
	dueDate := "2027-04-05"
	response := toTaskResponse(task.Task{
		DueDate:     &dueDate,
		CreatedAt:   instant,
		UpdatedAt:   instant,
		CompletedAt: &instant,
		DeletedAt:   &instant,
	})

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal task response: %v", err)
	}
	for _, field := range []string{"created_at", "updated_at", "completed_at", "deleted_at"} {
		if !strings.Contains(string(encoded), `"`+field+`":"2027-04-05T06:30:00Z"`) {
			t.Errorf("%s is not serialized as the equivalent UTC instant: %s", field, encoded)
		}
	}
	if response.DueDate == nil || *response.DueDate != dueDate {
		t.Fatalf("civil due date changed during response serialization: %v", response.DueDate)
	}
}
