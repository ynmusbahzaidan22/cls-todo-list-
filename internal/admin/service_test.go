package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

type fakeRepository struct {
	users      []User
	tasks      []Task
	userLimit  int
	taskLimit  int
	userCursor *Cursor
	taskCursor *Cursor
	status     string
	state      string
	userCalls  int
	taskCalls  int
	userErr    error
	taskErr    error
}

func (r *fakeRepository) ListAdminUsers(_ context.Context, _ string, cursor *Cursor, limit int) ([]User, error) {
	r.userCalls++
	r.userCursor, r.userLimit = cursor, limit
	if r.userErr != nil {
		return nil, r.userErr
	}
	items := append([]User(nil), r.users...)
	snapshotAt := time.Now().UTC().Add(time.Hour)
	if cursor != nil {
		snapshotAt = cursor.SnapshotAt
	}
	for i := range items {
		if items[i].SnapshotAt.IsZero() {
			items[i].SnapshotAt = snapshotAt
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return pageUsers(items, cursor, limit), nil
}

func (r *fakeRepository) ListAdminTasks(_ context.Context, status, state string, cursor *Cursor, limit int) ([]Task, error) {
	r.taskCalls++
	r.status, r.state, r.taskCursor, r.taskLimit = status, state, cursor, limit
	if r.taskErr != nil {
		return nil, r.taskErr
	}
	items := append([]Task(nil), r.tasks...)
	snapshotAt := time.Now().UTC().Add(time.Hour)
	if cursor != nil {
		snapshotAt = cursor.SnapshotAt
	}
	for i := range items {
		if items[i].SnapshotAt.IsZero() {
			items[i].SnapshotAt = snapshotAt
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return pageTasks(items, cursor, limit), nil
}

func (r *fakeRepository) ChangeUserRole(context.Context, string, string, string) (MutationResult, error) {
	return MutationResult{}, nil
}
func (r *fakeRepository) ChangeUserStatus(context.Context, string, string, string) (MutationResult, error) {
	return MutationResult{}, nil
}
func (r *fakeRepository) RevokeUserSessions(context.Context, string, string) (MutationResult, error) {
	return MutationResult{}, nil
}

func pageUsers(items []User, cursor *Cursor, limit int) []User {
	page := make([]User, 0, limit)
	for _, item := range items {
		if cursor != nil && !isAfterCursor(item.CreatedAt, item.ID, cursor) {
			continue
		}
		page = append(page, item)
		if len(page) == limit {
			break
		}
	}
	return page
}

func pageTasks(items []Task, cursor *Cursor, limit int) []Task {
	page := make([]Task, 0, limit)
	for _, item := range items {
		if cursor != nil && !isAfterCursor(item.CreatedAt, item.ID, cursor) {
			continue
		}
		page = append(page, item)
		if len(page) == limit {
			break
		}
	}
	return page
}

func isAfterCursor(createdAt time.Time, id string, cursor *Cursor) bool {
	return createdAt.Before(cursor.CreatedAt) || (createdAt.Equal(cursor.CreatedAt) && id < cursor.ID)
}

func TestListUsersUsesBoundedKeysetPagination(t *testing.T) {
	created := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	repo := &fakeRepository{users: []User{
		{ID: "00000000-0000-4000-8000-000000000001", CreatedAt: created},
		{ID: "00000000-0000-4000-8000-000000000002", CreatedAt: created},
		{ID: "00000000-0000-4000-8000-000000000003", CreatedAt: created},
	}}
	service := NewService(repo, make([]byte, 32))
	var got []string
	cursor := ""
	for pageNumber := 0; pageNumber < 4; pageNumber++ {
		page, err := service.ListUsers(context.Background(), 1, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if pageNumber == 0 && (repo.userLimit != 2 || repo.userCursor != nil) {
			t.Fatalf("first query limit=%d cursor=%+v", repo.userLimit, repo.userCursor)
		}
		for _, user := range page.Items {
			got = append(got, user.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	want := []string{
		"00000000-0000-4000-8000-000000000003",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000001",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paginated user IDs=%v, want stable tie-break order %v", got, want)
	}
}

func TestListTasksDefaultsAndBindsCursorToFilters(t *testing.T) {
	repo := &fakeRepository{tasks: []Task{{Task: task.Task{ID: "00000000-0000-4000-8000-000000000001", CreatedAt: time.Now().UTC()}}}}
	service := NewService(repo, make([]byte, 32))
	page, err := service.ListTasks(context.Background(), 0, "", "TODO", "")
	if err != nil {
		t.Fatal(err)
	}
	if repo.status != "TODO" || repo.state != "active" || repo.taskLimit != DefaultPageSize+1 {
		t.Fatalf("defaults or filters = status %q state %q limit %d", repo.status, repo.state, repo.taskLimit)
	}
	if page.NextCursor != nil {
		t.Fatal("one-row page unexpectedly had a next cursor")
	}

	created := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	repo.tasks = []Task{
		{Task: task.Task{ID: "00000000-0000-4000-8000-000000000001", CreatedAt: created}},
		{Task: task.Task{ID: "00000000-0000-4000-8000-000000000002", CreatedAt: created.Add(-time.Minute)}},
	}
	page, err = service.ListTasks(context.Background(), 1, "", "TODO", "active")
	if err != nil || page.NextCursor == nil {
		t.Fatalf("create task cursor: page=%+v err=%v", page, err)
	}
	queriesBefore := repo.taskCalls
	if _, err := service.ListTasks(context.Background(), 1, *page.NextCursor, "DONE", "active"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("filter-mismatched cursor error = %v", err)
	}
	if repo.taskCalls != queriesBefore {
		t.Fatal("filter-mismatched cursor reached repository")
	}
}

func TestListTasksKeepsStableOrderAcrossEqualTimestampPages(t *testing.T) {
	created := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	repo := &fakeRepository{tasks: []Task{
		{Task: task.Task{ID: "00000000-0000-4000-8000-000000000001", CreatedAt: created}},
		{Task: task.Task{ID: "00000000-0000-4000-8000-000000000002", CreatedAt: created}},
		{Task: task.Task{ID: "00000000-0000-4000-8000-000000000003", CreatedAt: created}},
	}}
	service := NewService(repo, make([]byte, 32))
	var got []string
	cursor := ""
	for pageNumber := 0; pageNumber < 4; pageNumber++ {
		page, err := service.ListTasks(context.Background(), 1, cursor, "", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			got = append(got, item.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	want := []string{
		"00000000-0000-4000-8000-000000000003",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000001",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paginated task IDs=%v, want stable tie-break order %v", got, want)
	}
}

func TestInvalidAdminCursorsDoNotQueryRepository(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	service := NewService(&fakeRepository{}, key)
	created := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	snapshotAt := created.Add(time.Minute)
	validUsers, err := service.encodeCursor("users", "", created, "00000000-0000-4000-8000-000000000001", snapshotAt)
	if err != nil {
		t.Fatal(err)
	}
	validTasks, err := service.encodeCursor("tasks", "TODO\x00active", created, "00000000-0000-4000-8000-000000000001", snapshotAt)
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(validUsers, ".")
	signature, _ := base64.RawURLEncoding.DecodeString(parts[1])
	signature[0] ^= 0xff
	modifiedSignature := parts[0] + "." + base64.RawURLEncoding.EncodeToString(signature)
	payload, _ := base64.RawURLEncoding.DecodeString(parts[0])
	payload[0] ^= 1
	modifiedPayload := base64.RawURLEncoding.EncodeToString(payload) + "." + parts[1]

	invalidUUID := signCursorPayload(t, key, cursorPayload{
		Resource: "users", CreatedAt: created, ID: "not-a-uuid",
	})

	cases := []struct {
		name  string
		call  func(*Service, string) error
		value string
	}{
		{"modified signature", func(s *Service, c string) error { _, err := s.ListUsers(context.Background(), 10, c); return err }, modifiedSignature},
		{"modified payload with original signature", func(s *Service, c string) error { _, err := s.ListUsers(context.Background(), 10, c); return err }, modifiedPayload},
		{"malformed base64", func(s *Service, c string) error { _, err := s.ListUsers(context.Background(), 10, c); return err }, "***.!!!"},
		{"truncated cursor", func(s *Service, c string) error { _, err := s.ListUsers(context.Background(), 10, c); return err }, validUsers[:len(validUsers)-1]},
		{"cursor reused across resources", func(s *Service, c string) error {
			_, err := s.ListTasks(context.Background(), 10, c, "TODO", "active")
			return err
		}, validUsers},
		{"cursor reused with different task filters", func(s *Service, c string) error {
			_, err := s.ListTasks(context.Background(), 10, c, "DONE", "active")
			return err
		}, validTasks},
		{"valid signature with invalid UUID", func(s *Service, c string) error { _, err := s.ListUsers(context.Background(), 10, c); return err }, invalidUUID},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeRepository{}
			testService := NewService(repo, key)
			if err := test.call(testService, test.value); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("invalid cursor error=%v, want ErrInvalidInput", err)
			}
			if repo.userCalls != 0 || repo.taskCalls != 0 {
				t.Fatalf("invalid cursor reached repository: users=%d tasks=%d", repo.userCalls, repo.taskCalls)
			}
		})
	}
}

func TestEmptyCursorStartsFirstPage(t *testing.T) {
	repo := &fakeRepository{}
	service := NewService(repo, make([]byte, 32))
	page, err := service.ListUsers(context.Background(), 0, "")
	if err != nil || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("empty first page=%+v err=%v", page, err)
	}
	if repo.userCalls != 1 || repo.userLimit != DefaultPageSize+1 {
		t.Fatalf("empty cursor calls=%d limit=%d", repo.userCalls, repo.userLimit)
	}
}

func TestAdminPaginationLimitBoundariesAndEmptyResults(t *testing.T) {
	for _, resource := range []string{"users", "tasks"} {
		t.Run(resource, func(t *testing.T) {
			repo := &fakeRepository{}
			service := NewService(repo, make([]byte, 32))
			var call func(int) (int, error)
			if resource == "users" {
				call = func(limit int) (int, error) {
					page, err := service.ListUsers(context.Background(), limit, "")
					if err != nil {
						return 0, err
					}
					if page.Items == nil {
						t.Fatal("empty user results must be a non-nil slice")
					}
					return repo.userLimit, nil
				}
			} else {
				call = func(limit int) (int, error) {
					page, err := service.ListTasks(context.Background(), limit, "", "", "")
					if err != nil {
						return 0, err
					}
					if page.Items == nil {
						t.Fatal("empty task results must be a non-nil slice")
					}
					return repo.taskLimit, nil
				}
			}
			if limit, err := call(0); err != nil || limit != DefaultPageSize+1 {
				t.Fatalf("default limit passed to repository=%d err=%v", limit, err)
			}
			if limit, err := call(MaxPageSize); err != nil || limit != MaxPageSize+1 {
				t.Fatalf("maximum limit passed to repository=%d err=%v", limit, err)
			}
			for _, invalid := range []int{-1, MaxPageSize + 1} {
				before := repo.userCalls + repo.taskCalls
				_, err := call(invalid)
				if !errors.Is(err, ErrInvalidInput) {
					t.Errorf("limit %d error=%v, want ErrInvalidInput", invalid, err)
				}
				if repo.userCalls+repo.taskCalls != before {
					t.Errorf("invalid limit %d reached repository", invalid)
				}
			}
		})
	}
}

func TestAdminRepositoryErrorsPreserveStorageAndCause(t *testing.T) {
	cause := errors.New("database query failed")
	for _, resource := range []string{"users", "tasks"} {
		t.Run(resource, func(t *testing.T) {
			repo := &fakeRepository{userErr: cause, taskErr: cause}
			service := NewService(repo, make([]byte, 32))
			var err error
			if resource == "users" {
				_, err = service.ListUsers(context.Background(), 10, "")
			} else {
				_, err = service.ListTasks(context.Background(), 10, "", "", "")
			}
			if !errors.Is(err, ErrStorage) || !errors.Is(err, cause) {
				t.Fatalf("storage error=%v, want both ErrStorage and underlying cause", err)
			}
		})
	}
}

func TestAccountManagementInputAndSelfProtection(t *testing.T) {
	service := NewService(&fakeRepository{}, make([]byte, 32))
	valid := "11111111-1111-4111-8111-111111111111"
	for name, call := range map[string]func() error{
		"invalid role": func() error {
			_, err := service.ChangeUserRole(context.Background(), valid, "22222222-2222-4222-8222-222222222222", "OWNER")
			return err
		},
		"invalid status": func() error {
			_, err := service.ChangeUserStatus(context.Background(), valid, "22222222-2222-4222-8222-222222222222", "DELETED")
			return err
		},
		"self role change": func() error {
			_, err := service.ChangeUserRole(context.Background(), valid, valid, RoleUser)
			return err
		},
		"self disable": func() error {
			_, err := service.ChangeUserStatus(context.Background(), valid, valid, StatusDisabled)
			return err
		},
		"self session revocation": func() error { _, err := service.RevokeUserSessions(context.Background(), valid, valid); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("invalid or self-management operation was accepted")
			}
		})
	}
	for _, role := range []string{RoleUser, RoleAdmin} {
		if _, err := service.ChangeUserRole(context.Background(), valid, "22222222-2222-4222-8222-222222222222", role); err != nil {
			t.Fatalf("valid role %s rejected: %v", role, err)
		}
	}
	for _, status := range []string{StatusActive, StatusDisabled} {
		if _, err := service.ChangeUserStatus(context.Background(), valid, "22222222-2222-4222-8222-222222222222", status); err != nil {
			t.Fatalf("valid status %s rejected: %v", status, err)
		}
	}
}

func signCursorPayload(t *testing.T, key []byte, payload cursorPayload) string {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	return base64.RawURLEncoding.EncodeToString(encoded) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
