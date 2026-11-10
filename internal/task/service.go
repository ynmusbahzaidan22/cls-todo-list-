package task

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const maxCursorLength = 512
const maxTaskSearchLength = 200

type Service struct {
	repository    Repository
	cursorSignKey []byte
}

type cursorPayload struct {
	CreatedAt  time.Time `json:"created_at"`
	ID         string    `json:"id"`
	StatusRank int       `json:"status_rank"`
	Cutoff     time.Time `json:"cutoff"`
	FilterHash string    `json:"filter_hash"`
	TimeZone   string    `json:"time_zone"`
	LocalDate  string    `json:"local_date"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type listContextProvider interface {
	ListContext(context.Context, string, string) (string, string, time.Time, error)
}

type deletedCursorPayload struct {
	DeletedAt time.Time `json:"deleted_at"`
	ID        string    `json:"id"`
	Cutoff    time.Time `json:"cutoff"`
	OwnerID   string    `json:"owner_id"`
}

type activityCursorPayload struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func NewService(repository Repository, cursorSigningKey ...[]byte) *Service {
	key := make([]byte, 32)
	if len(cursorSigningKey) > 0 {
		if len(cursorSigningKey[0]) != len(key) {
			panic("task cursor signing key must be 32 bytes")
		}
		copy(key, cursorSigningKey[0])
	} else if _, err := rand.Read(key); err != nil {
		panic("secure randomness is unavailable for task cursor signing")
	}
	return &Service{repository: repository, cursorSignKey: key}
}

func (s *Service) Create(ctx context.Context, userID, title, description, status string, priorities ...string) (Task, error) {
	cleanTitle, err := validateTitle(title)
	priority := PriorityMedium
	if len(priorities) > 2 {
		return Task{}, ErrInvalidInput
	}
	if len(priorities) >= 1 {
		priority = priorities[0]
	}
	if err != nil || validateDescription(description) != nil || !validStatus(status) || !validPriority(priority) {
		return Task{}, ErrInvalidInput
	}
	args := []string{priority}
	if len(priorities) == 2 {
		if priorities[1] != "__DUE_NONE__" && !ValidDueDate(priorities[1]) {
			return Task{}, ErrInvalidInput
		}
		args = append(args, priorities[1])
	}
	created, err := s.repository.Create(ctx, userID, cleanTitle, description, status, args...)
	if err != nil {
		return Task{}, fmt.Errorf("create task: %w", ErrStorage)
	}
	return created, nil
}

func (s *Service) List(ctx context.Context, userID string, limit int, query, status, rawCursor string, priorities ...string) (Page, error) {
	return s.list(ctx, userID, limit, query, status, rawCursor, false, "", priorities...)
}

func (s *Service) ListCompleted(ctx context.Context, userID string, limit int, rawCursor, period, zone string) (Page, error) {
	return s.list(ctx, userID, limit, "", "", rawCursor, true, period, "", "", zone)
}

func (s *Service) list(ctx context.Context, userID string, limit int, query, status, rawCursor string, archived bool, completedPeriod string, priorities ...string) (Page, error) {
	if limit == 0 {
		limit = DefaultPageSize
	}
	query, err := normalizeTaskSearch(query)
	priority := ""
	if len(priorities) > 3 {
		return Page{}, ErrInvalidInput
	}
	if len(priorities) >= 1 {
		priority = priorities[0]
	}
	due := ""
	if len(priorities) >= 2 {
		due = priorities[1]
	}
	zone := ""
	if len(priorities) == 3 {
		zone = priorities[2]
	}
	if err != nil || !validOptionalStatus(status) || !validOptionalPriority(priority) || !validDueFilter(due) || (zone != "" && !ValidTimeZone(zone)) || limit < 1 || limit > MaxPageSize || !ValidID(userID) || (archived && !validCompletedPeriod(completedPeriod)) || (!archived && completedPeriod != "") {
		return Page{}, ErrInvalidInput
	}
	requestedZone := zone
	localDate := ""
	cutoff := time.Now().UTC()
	if provider, ok := s.repository.(listContextProvider); ok {
		var contextErr error
		zone, localDate, cutoff, contextErr = provider.ListContext(ctx, userID, requestedZone)
		if contextErr != nil {
			return Page{}, fmt.Errorf("load task date context: %w", ErrStorage)
		}
	} else {
		if zone == "" {
			zone = "UTC"
		}
		localDate = time.Now().UTC().Format("2006-01-02")
		if location, locationErr := time.LoadLocation(zone); locationErr == nil {
			localDate = time.Now().In(location).Format("2006-01-02")
		}
	}
	filter := ListFilter{Query: query, Status: status, Priority: priority, Due: due, TimeZone: zone, LocalDate: localDate, Archived: archived, CompletedPeriod: completedPeriod, Cutoff: cutoff}
	var cursor *Cursor
	if rawCursor != "" {
		decoded, err := decodeTaskListCursor(rawCursor, s.cursorSignKey, userID, filter)
		if err != nil {
			return Page{}, ErrInvalidInput
		}
		filter.LocalDate = decoded.LocalDate
		filter.Cutoff = decoded.Cutoff
		localDate = decoded.LocalDate
		cursor = &decoded
	}
	if archived {
		from, before, rangeErr := completedPeriodBounds(completedPeriod, zone, localDate)
		if rangeErr != nil {
			return Page{}, ErrInvalidInput
		}
		filter.CompletedFrom, filter.CompletedBefore = from, before
	}
	items, err := s.repository.List(ctx, userID, filter, cursor, limit+1)
	if err != nil {
		return Page{}, fmt.Errorf("list tasks: %w", ErrStorage)
	}
	page := Page{Items: items}
	if archived {
		if counter, ok := s.repository.(interface {
			CountCompleted(context.Context, string, *time.Time, *time.Time, time.Time) (int, error)
		}); ok {
			page.TotalCount, err = counter.CountCompleted(ctx, userID, filter.CompletedFrom, filter.CompletedBefore, filter.Cutoff)
			if err != nil {
				return Page{}, fmt.Errorf("count completed tasks: %w", ErrStorage)
			}
		}
	}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		next, err := encodeTaskListCursor(Cursor{CreatedAt: last.CreatedAt, ID: last.ID, StatusRank: taskStatusRank(last.Status), Cutoff: filter.Cutoff}, s.cursorSignKey, userID, filter)
		if err != nil {
			return Page{}, fmt.Errorf("encode task cursor: %w", ErrStorage)
		}
		page.NextCursor = &next
	}
	if page.Items == nil {
		page.Items = []Task{}
	}
	return page, nil
}

func (s *Service) ListDeleted(ctx context.Context, userID string, limit int, rawCursor string) (Page, error) {
	if limit == 0 {
		limit = DefaultPageSize
	}
	if limit < 1 || limit > MaxPageSize || !ValidID(userID) {
		return Page{}, ErrInvalidInput
	}
	var cursor *DeletedCursor
	if rawCursor != "" {
		decoded, err := decodeDeletedCursor(rawCursor)
		if err != nil || !strings.EqualFold(decoded.OwnerID, userID) {
			return Page{}, ErrInvalidInput
		}
		cursor = &decoded
	}
	items, cutoff, err := s.repository.ListDeleted(ctx, userID, cursor, limit+1)
	if err != nil {
		return Page{}, fmt.Errorf("list deleted tasks: %w", ErrStorage)
	}
	page := Page{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		if last.DeletedAt == nil || cutoff.IsZero() {
			return Page{}, fmt.Errorf("deleted task listing returned invalid cursor fields: %w", ErrStorage)
		}
		next, err := encodeDeletedCursor(DeletedCursor{
			DeletedAt: *last.DeletedAt, ID: last.ID, Cutoff: cutoff, OwnerID: userID,
		})
		if err != nil {
			return Page{}, fmt.Errorf("encode deleted task cursor: %w", ErrStorage)
		}
		page.NextCursor = &next
	}
	if page.Items == nil {
		page.Items = []Task{}
	}
	return page, nil
}

func (s *Service) CountDeleted(ctx context.Context, userID string) (int, error) {
	if !ValidID(userID) {
		return 0, ErrInvalidInput
	}
	count, err := s.repository.CountDeleted(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("count recently deleted tasks: %w", ErrStorage)
	}
	return count, nil
}

func (s *Service) Get(ctx context.Context, userID, taskID string) (Task, error) {
	item, err := s.repository.Get(ctx, userID, taskID)
	if err != nil {
		return Task{}, mapRepositoryError("get task", err)
	}
	return item, nil
}

func (s *Service) Update(ctx context.Context, userID, taskID string, version int64, title, description, status string, priorities ...string) (Task, error) {
	cleanTitle, err := validateTitle(title)
	if len(priorities) > 2 || err != nil || validateDescription(description) != nil || !validStatus(status) || version < 1 {
		return Task{}, ErrInvalidInput
	}
	if len(priorities) >= 1 && priorities[0] != "__PRIORITY_PRESERVE__" && !validPriority(priorities[0]) {
		return Task{}, ErrInvalidInput
	}
	if len(priorities) == 2 && priorities[1] != "__DUE_NONE__" && !ValidDueDate(priorities[1]) {
		return Task{}, ErrInvalidInput
	}
	item, err := s.repository.Update(ctx, userID, taskID, version, cleanTitle, description, status, priorities...)
	if err != nil {
		return Task{}, mapRepositoryError("update task", err)
	}
	return item, nil
}

func (s *Service) Delete(ctx context.Context, userID, taskID string, version int64) (Task, error) {
	if version < 1 {
		return Task{}, ErrInvalidInput
	}
	item, err := s.repository.Delete(ctx, userID, taskID, version)
	if err != nil {
		return Task{}, mapRepositoryError("delete task", err)
	}
	return item, nil
}

// Bulk applies one validated operation to every task in a single repository transaction.
func (s *Service) Bulk(ctx context.Context, userID string, operation BulkOperation) ([]Task, error) {
	if len(operation.Tasks) == 0 || len(operation.Tasks) > MaxBulkTasks {
		return nil, ErrInvalidInput
	}
	switch operation.Action {
	case "status":
		if !validStatus(operation.Status) {
			return nil, ErrInvalidInput
		}
	case "delete":
		if operation.Status != "" {
			return nil, ErrInvalidInput
		}
	default:
		return nil, ErrInvalidInput
	}
	seen := make(map[string]struct{}, len(operation.Tasks))
	for index := range operation.Tasks {
		item := &operation.Tasks[index]
		if !ValidID(item.ID) || item.Version < 1 {
			return nil, ErrInvalidInput
		}
		item.ID = strings.ToLower(item.ID)
		if _, exists := seen[item.ID]; exists {
			return nil, ErrInvalidInput
		}
		seen[item.ID] = struct{}{}
	}
	items, err := s.repository.Bulk(ctx, userID, operation)
	if err != nil {
		return nil, mapRepositoryError("bulk update tasks", err)
	}
	return items, nil
}

func (s *Service) Restore(ctx context.Context, userID, taskID string, version int64) (Task, error) {
	if version < 1 {
		return Task{}, ErrInvalidInput
	}
	item, err := s.repository.Restore(ctx, userID, taskID, version)
	if err != nil {
		return Task{}, mapRepositoryError("restore task", err)
	}
	return item, nil
}

func (s *Service) Activity(ctx context.Context, userID, taskID string, limit int, rawCursor string) (ActivityPage, error) {
	if !ValidID(taskID) {
		return ActivityPage{}, ErrInvalidInput
	}
	if limit == 0 {
		limit = DefaultPageSize
	}
	if limit < 1 || limit > MaxPageSize {
		return ActivityPage{}, ErrInvalidInput
	}
	var cursor *ActivityCursor
	if rawCursor != "" {
		decoded, err := decodeActivityCursor(rawCursor)
		if err != nil {
			return ActivityPage{}, ErrInvalidInput
		}
		cursor = &decoded
	}
	items, err := s.repository.ListActivity(ctx, userID, taskID, cursor, limit+1)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ActivityPage{}, ErrNotFound
		}
		return ActivityPage{}, fmt.Errorf("list task activity: %w", ErrStorage)
	}
	page := ActivityPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		payload, err := json.Marshal(activityCursorPayload{CreatedAt: last.CreatedAt.UTC(), ID: last.ID})
		if err != nil {
			return ActivityPage{}, fmt.Errorf("encode activity cursor: %w", ErrStorage)
		}
		encoded := base64.RawURLEncoding.EncodeToString(payload)
		page.NextCursor = &encoded
	}
	if page.Items == nil {
		page.Items = []Activity{}
	}
	return page, nil
}

// Undo applies a one-time compensating operation to an eligible activity event.
func (s *Service) Undo(ctx context.Context, userID, taskID, eventID string, version int64) (Task, Activity, error) {
	if !ValidID(taskID) || !ValidID(eventID) || version < 1 {
		return Task{}, Activity{}, ErrInvalidInput
	}
	updated, event, err := s.repository.Undo(ctx, userID, strings.ToLower(taskID), strings.ToLower(eventID), version)
	if err != nil {
		return Task{}, Activity{}, mapRepositoryError("undo task activity", err)
	}
	return updated, event, nil
}

func decodeActivityCursor(raw string) (ActivityCursor, error) {
	if len(raw) == 0 || len(raw) > maxCursorLength {
		return ActivityCursor{}, ErrInvalidInput
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return ActivityCursor{}, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var payload activityCursorPayload
	if err := decoder.Decode(&payload); err != nil {
		return ActivityCursor{}, ErrInvalidInput
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ActivityCursor{}, ErrInvalidInput
	}
	if payload.CreatedAt.IsZero() || !ValidID(payload.ID) {
		return ActivityCursor{}, ErrInvalidInput
	}
	return ActivityCursor{CreatedAt: payload.CreatedAt.UTC(), ID: strings.ToLower(payload.ID)}, nil
}

func mapRepositoryError(operation string, err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrTaskArchived) || errors.Is(err, ErrRecoveryOver) ||
		errors.Is(err, ErrUndoUnsupported) || errors.Is(err, ErrUndoAlreadyApplied) || errors.Is(err, ErrUndoExpired) {
		return err
	}
	return fmt.Errorf("%s: %w", operation, ErrStorage)
}

func validateTitle(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", ErrInvalidInput
	}
	clean := strings.TrimSpace(value)
	length := utf8.RuneCountInString(clean)
	if length < 1 || length > 200 || strings.IndexByte(clean, 0) >= 0 {
		return "", ErrInvalidInput
	}
	return clean, nil
}

func validateDescription(value string) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 10000 || strings.IndexByte(value, 0) >= 0 {
		return ErrInvalidInput
	}
	return nil
}

func validStatus(status string) bool {
	switch status {
	case StatusTODO, StatusInProgress, StatusDone:
		return true
	default:
		return false
	}
}

func validPriority(priority string) bool {
	switch priority {
	case PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent:
		return true
	default:
		return false
	}
}

func validOptionalPriority(priority string) bool {
	return priority == "" || validPriority(priority)
}

func validDueFilter(value string) bool {
	return value == "" || value == "overdue" || value == "today" || value == "upcoming" || value == "none"
}

func ValidID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, char := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func normalizeTaskSearch(query string) (string, error) {
	if !utf8.ValidString(query) || strings.IndexByte(query, 0) >= 0 {
		return "", ErrInvalidInput
	}
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) > maxTaskSearchLength {
		return "", ErrInvalidInput
	}
	return query, nil
}

func validOptionalStatus(status string) bool {
	return status == "" || validStatus(status)
}

func encodeTaskListCursor(cursor Cursor, signingKey []byte, ownerID string, filter ListFilter) (string, error) {
	if len(signingKey) != 32 || !ValidID(ownerID) || cursor.CreatedAt.IsZero() || !ValidID(cursor.ID) {
		return "", ErrInvalidInput
	}
	if filter.TimeZone == "" {
		filter.TimeZone = "UTC"
	}
	if filter.LocalDate == "" {
		filter.LocalDate = time.Now().UTC().Format("2006-01-02")
	}
	if cursor.Cutoff.IsZero() {
		cursor.Cutoff = filter.Cutoff
	}
	if cursor.Cutoff.IsZero() {
		cursor.Cutoff = time.Now().UTC()
	}
	encoded, err := json.Marshal(cursorPayload{
		CreatedAt: cursor.CreatedAt.UTC(), ID: strings.ToLower(cursor.ID), StatusRank: cursor.StatusRank, Cutoff: cursor.Cutoff.UTC(),
		FilterHash: taskListFilterHash(ownerID, filter),
		TimeZone:   filter.TimeZone, LocalDate: filter.LocalDate,
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	})
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(encoded)
	mac := hmac.New(sha256.New, signingKey)
	_, _ = mac.Write([]byte(body))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + signature, nil
}

func decodeTaskListCursor(raw string, signingKey []byte, ownerID string, filter ListFilter) (Cursor, error) {
	if len(raw) == 0 || len(raw) > maxCursorLength || len(signingKey) != 32 || !ValidID(ownerID) {
		return Cursor{}, ErrInvalidInput
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return Cursor{}, ErrInvalidInput
	}
	providedSignature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(providedSignature) != parts[1] {
		return Cursor{}, ErrInvalidInput
	}
	mac := hmac.New(sha256.New, signingKey)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(providedSignature, mac.Sum(nil)) {
		return Cursor{}, ErrInvalidInput
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != parts[0] {
		return Cursor{}, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var payload cursorPayload
	if err := decoder.Decode(&payload); err != nil {
		return Cursor{}, ErrInvalidInput
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Cursor{}, ErrInvalidInput
	}
	if filter.TimeZone == "" {
		filter.TimeZone = "UTC"
	}
	if payload.CreatedAt.IsZero() || !ValidID(payload.ID) || payload.StatusRank < 0 || payload.StatusRank > 2 || payload.Cutoff.IsZero() || payload.ExpiresAt.IsZero() || time.Now().UTC().After(payload.ExpiresAt) || payload.TimeZone != filter.TimeZone || !ValidDueDate(payload.LocalDate) {
		return Cursor{}, ErrInvalidInput
	}
	filter.LocalDate = payload.LocalDate
	if payload.FilterHash != taskListFilterHash(ownerID, filter) {
		return Cursor{}, ErrInvalidInput
	}
	return Cursor{CreatedAt: payload.CreatedAt.UTC(), ID: strings.ToLower(payload.ID), StatusRank: payload.StatusRank, Cutoff: payload.Cutoff.UTC(), TimeZone: payload.TimeZone, LocalDate: payload.LocalDate, ExpiresAt: payload.ExpiresAt}, nil
}

func taskListFilterHash(ownerID string, filter ListFilter) string {
	data := strings.ToLower(ownerID) + "\x00" + filter.Query + "\x00" + filter.Status + "\x00" + filter.Priority + "\x00" + filter.Due + "\x00" + filter.TimeZone + "\x00" + filter.LocalDate + "\x00" + fmt.Sprint(filter.Archived) + "\x00" + filter.CompletedPeriod
	hash := sha256.Sum256([]byte(data))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func validCompletedPeriod(period string) bool {
	return period == "all" || period == "week" || period == "month" || period == "year"
}

func completedPeriodBounds(period, zone, localDate string) (*time.Time, *time.Time, error) {
	if period == "all" {
		return nil, nil, nil
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return nil, nil, err
	}
	date, err := time.ParseInLocation("2006-01-02", localDate, location)
	if err != nil {
		return nil, nil, err
	}
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, location)
	switch period {
	case "week":
		start = start.AddDate(0, 0, -((int(start.Weekday()) + 6) % 7))
	case "month":
		start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, location)
	case "year":
		start = time.Date(start.Year(), time.January, 1, 0, 0, 0, 0, location)
	default:
		return nil, nil, ErrInvalidInput
	}
	var end time.Time
	switch period {
	case "week":
		end = start.AddDate(0, 0, 7)
	case "month":
		end = start.AddDate(0, 1, 0)
	case "year":
		end = start.AddDate(1, 0, 0)
	}
	startUTC, endUTC := start.UTC(), end.UTC()
	return &startUTC, &endUTC, nil
}

func taskStatusRank(status string) int {
	switch status {
	case StatusInProgress:
		return 0
	case StatusDone:
		return 2
	default:
		return 1
	}
}

func encodeDeletedCursor(cursor DeletedCursor) (string, error) {
	encoded, err := json.Marshal(deletedCursorPayload{
		DeletedAt: cursor.DeletedAt.UTC(), ID: cursor.ID, Cutoff: cursor.Cutoff.UTC(), OwnerID: cursor.OwnerID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeDeletedCursor(raw string) (DeletedCursor, error) {
	if len(raw) == 0 || len(raw) > maxCursorLength {
		return DeletedCursor{}, ErrInvalidInput
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return DeletedCursor{}, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var payload deletedCursorPayload
	if err := decoder.Decode(&payload); err != nil {
		return DeletedCursor{}, ErrInvalidInput
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return DeletedCursor{}, ErrInvalidInput
	}
	if payload.DeletedAt.IsZero() || payload.Cutoff.IsZero() || !ValidID(payload.ID) || !ValidID(payload.OwnerID) || payload.Cutoff.After(payload.DeletedAt) {
		return DeletedCursor{}, ErrInvalidInput
	}
	return DeletedCursor{
		DeletedAt: payload.DeletedAt.UTC(), ID: strings.ToLower(payload.ID),
		Cutoff: payload.Cutoff.UTC(), OwnerID: strings.ToLower(payload.OwnerID),
	}, nil
}
