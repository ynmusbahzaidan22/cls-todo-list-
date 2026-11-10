package task

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDueDateValidationAndExpiredCursor(t *testing.T) {
	if !ValidTimeZone("Asia/Amman") || !ValidTimeZone("America/Los_Angeles") || ValidTimeZone("Local") || ValidTimeZone("Not/AZone") {
		t.Fatal("IANA timezone validation did not accept supported zones or reject invalid zones")
	}
	for _, value := range []string{"2028-02-29", "2000-01-01", "0001-01-01"} {
		if !ValidDueDate(value) {
			t.Errorf("valid date rejected: %s", value)
		}
	}
	for _, value := range []string{"2027-02-29", "2027-02-30", "2027-2-03", "2027-01-01T00:00:00Z", "infinity", "0000-01-01"} {
		if ValidDueDate(value) {
			t.Errorf("invalid date accepted: %s", value)
		}
	}
	key := bytes.Repeat([]byte("k"), 32)
	owner := "00000000-0000-4000-8000-000000000001"
	filter := ListFilter{Query: "due", Status: StatusTODO, Priority: PriorityHigh, Due: "overdue", TimeZone: "Asia/Amman", LocalDate: "2026-11-07"}
	payload, err := json.Marshal(cursorPayload{CreatedAt: time.Now().UTC(), ID: "00000000-0000-4000-8000-000000000002", FilterHash: taskListFilterHash(owner, filter), TimeZone: filter.TimeZone, LocalDate: filter.LocalDate, ExpiresAt: time.Now().UTC().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(body))
	raw := body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, err := decodeTaskListCursor(raw, key, owner, filter); err != ErrInvalidInput {
		t.Fatalf("expired cursor error=%v want invalid input", err)
	}
}

type stubRepository struct {
	createdTask   Task
	listed        []Task
	deleted       []Task
	deletedCount  int
	listLimit     int
	listCursor    *Cursor
	listFilter    ListFilter
	deletedLimit  int
	deletedCursor *DeletedCursor
	deletedCutoff time.Time
	bulkCalls     int
}

func (r *stubRepository) Create(_ context.Context, _, title, description, status string, priorities ...string) (Task, error) {
	priority := PriorityMedium
	if len(priorities) > 0 {
		priority = priorities[0]
	}
	return Task{ID: "00000000-0000-0000-0000-000000000001", Title: title, Description: description, Status: status, Priority: priority, Version: 1}, nil
}

func (r *stubRepository) List(_ context.Context, _ string, filter ListFilter, cursor *Cursor, limit int) ([]Task, error) {
	r.listFilter = filter
	r.listCursor = cursor
	r.listLimit = limit
	return r.listed, nil
}

func (r *stubRepository) ListDeleted(_ context.Context, _ string, cursor *DeletedCursor, limit int) ([]Task, time.Time, error) {
	r.deletedCursor = cursor
	r.deletedLimit = limit
	return r.deleted, r.deletedCutoff, nil
}

func (r *stubRepository) CountDeleted(context.Context, string) (int, error) {
	return r.deletedCount, nil
}

func (*stubRepository) Get(context.Context, string, string) (Task, error) { return Task{}, nil }
func (*stubRepository) Update(context.Context, string, string, int64, string, string, string, ...string) (Task, error) {
	return Task{}, nil
}
func (*stubRepository) Delete(context.Context, string, string, int64) (Task, error) {
	return Task{}, nil
}
func (r *stubRepository) Bulk(context.Context, string, BulkOperation) ([]Task, error) {
	r.bulkCalls++
	return []Task{}, nil
}
func (*stubRepository) Restore(context.Context, string, string, int64) (Task, error) {
	return Task{}, nil
}
func (*stubRepository) ListActivity(context.Context, string, string, *ActivityCursor, int) ([]Activity, error) {
	return []Activity{}, nil
}
func (*stubRepository) Undo(context.Context, string, string, string, int64) (Task, Activity, error) {
	return Task{}, Activity{}, nil
}

func TestTaskValidationMatchesDatabaseLimits(t *testing.T) {
	repository := &stubRepository{}
	service := NewService(repository)
	validTitle := strings.Repeat("é", 200)
	created, err := service.Create(context.Background(), "owner", "  "+validTitle+"  ", strings.Repeat("界", 10000), StatusTODO)
	if err != nil {
		t.Fatalf("Create rejected valid Unicode boundaries: %v", err)
	}
	if created.Title != validTitle {
		t.Fatalf("title was not trimmed: %q", created.Title)
	}
	if created.Priority != PriorityMedium {
		t.Fatalf("omitted priority default=%q want %q", created.Priority, PriorityMedium)
	}
	for _, test := range []struct {
		name        string
		title       string
		description string
		status      string
	}{
		{name: "blank title", title: " \t ", status: StatusTODO},
		{name: "title too long", title: strings.Repeat("x", 201), status: StatusTODO},
		{name: "description too long", title: "valid", description: strings.Repeat("x", 10001), status: StatusTODO},
		{name: "description NUL", title: "valid", description: "bad\x00text", status: StatusTODO},
		{name: "invalid status", title: "valid", status: "DONE-ish"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Create(context.Background(), "owner", test.title, test.description, test.status); err != ErrInvalidInput {
				t.Fatalf("Create error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestPriorityValidationAndFilterCursorBinding(t *testing.T) {
	owner := "00000000-0000-4000-8000-000000000001"
	key := bytes.Repeat([]byte{0x4a}, 32)
	cursor := Cursor{CreatedAt: time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC), ID: "00000000-0000-0000-0000-000000000001"}
	filter := ListFilter{Query: "report", Status: StatusTODO, Priority: PriorityUrgent}
	encoded, err := encodeTaskListCursor(cursor, key, owner, filter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTaskListCursor(encoded, key, owner, filter); err != nil {
		t.Fatalf("valid filter-bound cursor rejected: %v", err)
	}
	if _, err := decodeTaskListCursor(encoded, key, owner, ListFilter{Query: "report", Status: StatusTODO, Priority: PriorityHigh}); err != ErrInvalidInput {
		t.Fatalf("priority-mismatched cursor error=%v want ErrInvalidInput", err)
	}
	for _, value := range []string{PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent} {
		if !validPriority(value) {
			t.Errorf("valid priority %q rejected", value)
		}
	}
	for _, value := range []string{"", "urgent", "ASAP", "TODO"} {
		if validPriority(value) {
			t.Errorf("invalid priority %q accepted", value)
		}
	}
	repository := &stubRepository{}
	service := NewService(repository)
	if _, err := service.Create(context.Background(), owner, "priority", "", StatusTODO, "ASAP"); err != ErrInvalidInput {
		t.Fatalf("invalid create priority error=%v want ErrInvalidInput", err)
	}
	if _, err := service.Update(context.Background(), owner, cursor.ID, 1, "priority", "", StatusTODO, "ASAP"); err != ErrInvalidInput {
		t.Fatalf("invalid update priority error=%v want ErrInvalidInput", err)
	}
	if _, err := service.List(context.Background(), owner, 10, "", "", "", "ASAP"); err != ErrInvalidInput {
		t.Fatalf("invalid list priority error=%v want ErrInvalidInput", err)
	}
	if _, err := service.List(context.Background(), owner, 10, "", "", "", "", "", "Not/AZone"); err != ErrInvalidInput {
		t.Fatalf("invalid device timezone error=%v want ErrInvalidInput", err)
	}
	if _, err := service.List(context.Background(), owner, 10, "", "", "", "", "", "Asia/Amman"); err != nil {
		t.Fatalf("valid device timezone was rejected: %v", err)
	}
	if repository.listFilter.TimeZone != "Asia/Amman" {
		t.Fatalf("device timezone not carried into list filter: %+v", repository.listFilter)
	}
}

func TestListDeletedUsesOwnerBoundCursorAndPageLimit(t *testing.T) {
	owner := "11111111-1111-4111-8111-111111111111"
	cutoff := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	deletedAt := cutoff.Add(time.Second)
	repository := &stubRepository{
		deleted: []Task{
			{ID: "00000000-0000-4000-8000-000000000001", DeletedAt: &deletedAt},
			{ID: "00000000-0000-4000-8000-000000000002", DeletedAt: &deletedAt},
		},
		deletedCutoff: cutoff,
	}
	page, err := NewService(repository).ListDeleted(context.Background(), owner, 1, "")
	if err != nil {
		t.Fatalf("ListDeleted returned error: %v", err)
	}
	if len(page.Items) != 1 || page.NextCursor == nil || repository.deletedLimit != 2 {
		t.Fatalf("ListDeleted page/limit = %d/%v/%d, want one item, cursor, limit 2", len(page.Items), page.NextCursor, repository.deletedLimit)
	}
	cursor, err := decodeDeletedCursor(*page.NextCursor)
	if err != nil {
		t.Fatalf("decode generated deleted cursor: %v", err)
	}
	if cursor.OwnerID != owner || cursor.Cutoff != cutoff || cursor.ID != page.Items[0].ID {
		t.Fatalf("cursor does not pin owner/cutoff/last item: %+v", cursor)
	}
	if _, err := NewService(repository).ListDeleted(context.Background(), "22222222-2222-4222-8222-222222222222", 1, *page.NextCursor); err != ErrInvalidInput {
		t.Fatalf("cross-owner cursor error = %v, want ErrInvalidInput", err)
	}
	if _, err := NewService(repository).ListDeleted(context.Background(), owner, 1, "not-a-cursor"); err != ErrInvalidInput {
		t.Fatalf("malformed cursor error = %v, want ErrInvalidInput", err)
	}
}

func TestListCursorRoundTripAndPaginationLookahead(t *testing.T) {
	createdAt := time.Date(2026, time.November, 5, 14, 0, 0, 123456000, time.FixedZone("test", 3*60*60))
	id := "00000000-0000-0000-0000-00000000000a"
	owner := "00000000-0000-4000-8000-000000000001"
	filter := ListFilter{Query: "notes", Status: StatusTODO}
	key := bytes.Repeat([]byte{0x5a}, 32)
	cursor := Cursor{CreatedAt: createdAt, ID: id}
	encoded, err := encodeTaskListCursor(cursor, key, owner, filter)
	if err != nil {
		t.Fatalf("encodeTaskListCursor returned error: %v", err)
	}
	decoded, err := decodeTaskListCursor(encoded, key, owner, filter)
	if err != nil {
		t.Fatalf("decodeTaskListCursor returned error: %v", err)
	}
	if !decoded.CreatedAt.Equal(createdAt) || decoded.ID != id {
		t.Fatalf("cursor round-trip mismatch: %+v", decoded)
	}
	if _, err := decodeTaskListCursor(encoded, key, owner, ListFilter{Query: "different", Status: StatusTODO}); err != ErrInvalidInput {
		t.Fatalf("mismatched filter cursor error = %v, want ErrInvalidInput", err)
	}
	tampered := encoded[:len(encoded)-1] + map[bool]string{true: "A", false: "B"}[encoded[len(encoded)-1] == 'A']
	if _, err := decodeTaskListCursor(tampered, key, owner, filter); err != ErrInvalidInput {
		t.Fatalf("tampered cursor error = %v, want ErrInvalidInput", err)
	}

	repository := &stubRepository{listed: []Task{
		{ID: "00000000-0000-0000-0000-000000000001", CreatedAt: createdAt},
		{ID: "00000000-0000-0000-0000-000000000002", CreatedAt: createdAt.Add(-time.Second)},
	}}
	page, err := NewService(repository).List(context.Background(), "00000000-0000-4000-8000-000000000001", 1, "  مرحبا  ", StatusTODO, "")
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(page.Items) != 1 || page.NextCursor == nil || repository.listLimit != 2 {
		t.Fatalf("unexpected page/lookahead: page=%+v query-limit=%d", page, repository.listLimit)
	}
	if repository.listFilter.Query != "مرحبا" || repository.listFilter.Status != StatusTODO {
		t.Fatalf("unexpected list filters: %+v", repository.listFilter)
	}
}

func TestInvalidCursorAndPageSizeAreRejected(t *testing.T) {
	service := NewService(&stubRepository{})
	owner := "00000000-0000-4000-8000-000000000001"
	if _, err := service.List(context.Background(), owner, 1, "", "", "not-a-cursor"); err != ErrInvalidInput {
		t.Fatalf("invalid cursor error = %v, want ErrInvalidInput", err)
	}
	for _, size := range []int{-1, MaxPageSize + 1} {
		if _, err := service.List(context.Background(), owner, size, "", "", ""); err != ErrInvalidInput {
			t.Fatalf("page size %d error = %v, want ErrInvalidInput", size, err)
		}
	}
}

func TestTaskSearchValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		in   string
		want string
		bad  bool
	}{
		{name: "trims whitespace", in: "  مرحبا  ", want: "مرحبا"},
		{name: "empty after trimming", in: " \t\n ", want: ""},
		{name: "accepts 200 Unicode characters", in: strings.Repeat("界", 200), want: strings.Repeat("界", 200)},
		{name: "rejects 201 Unicode characters", in: strings.Repeat("界", 201), bad: true},
		{name: "rejects null byte", in: "task\x00name", bad: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeTaskSearch(test.in)
			if test.bad {
				if err != ErrInvalidInput {
					t.Fatalf("normalizeTaskSearch error = %v, want ErrInvalidInput", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("normalizeTaskSearch = %q, %v; want %q", got, err, test.want)
			}
		})
	}
	for _, status := range []string{"", StatusTODO, StatusInProgress, StatusDone} {
		if !validOptionalStatus(status) {
			t.Fatalf("valid status %q was rejected", status)
		}
	}
	for _, status := range []string{"todo", "DONE-ish", "TODO,DONE"} {
		if validOptionalStatus(status) {
			t.Fatalf("invalid status %q was accepted", status)
		}
	}
}

func TestBulkValidationRejectsUnsafeInputsBeforeRepository(t *testing.T) {
	valid := BulkTaskInput{ID: "00000000-0000-0000-0000-000000000001", Version: 1}
	for _, test := range []struct {
		name      string
		operation BulkOperation
	}{
		{name: "empty", operation: BulkOperation{Action: "delete"}},
		{name: "too many", operation: BulkOperation{Action: "delete", Tasks: make([]BulkTaskInput, MaxBulkTasks+1)}},
		{name: "invalid action", operation: BulkOperation{Action: "restore", Tasks: []BulkTaskInput{valid}}},
		{name: "invalid status", operation: BulkOperation{Action: "status", Status: "DONE-ish", Tasks: []BulkTaskInput{valid}}},
		{name: "delete with irrelevant status", operation: BulkOperation{Action: "delete", Status: StatusDone, Tasks: []BulkTaskInput{valid}}},
		{name: "invalid id", operation: BulkOperation{Action: "delete", Tasks: []BulkTaskInput{{ID: "not-a-uuid", Version: 1}}}},
		{name: "invalid version", operation: BulkOperation{Action: "delete", Tasks: []BulkTaskInput{{ID: valid.ID, Version: 0}}}},
		{name: "duplicate id", operation: BulkOperation{Action: "delete", Tasks: []BulkTaskInput{valid, valid}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &stubRepository{}
			if _, err := NewService(repository).Bulk(context.Background(), "owner", test.operation); err != ErrInvalidInput {
				t.Fatalf("Bulk error = %v, want ErrInvalidInput", err)
			}
			if repository.bulkCalls != 0 {
				t.Fatalf("invalid operation reached repository %d times", repository.bulkCalls)
			}
		})
	}
	for _, status := range []string{StatusTODO, StatusInProgress, StatusDone} {
		repository := &stubRepository{}
		_, err := NewService(repository).Bulk(context.Background(), "owner", BulkOperation{Action: "status", Status: status, Tasks: []BulkTaskInput{valid}})
		if err != nil || repository.bulkCalls != 1 {
			t.Errorf("valid status %q result err=%v repository calls=%d", status, err, repository.bulkCalls)
		}
	}
}
