package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/admin"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/database"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/testdatabase"
)

func TestAdminUsersRepositoryListsAndPaginatesAsRuntimePostgreSQL(t *testing.T) {
	runtimePool, adminPool := openAdminRepositoryTestPools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	ids := make([]string, 2)
	for i := range ids {
		ids[i] = testUUID(t)
	}
	registerAdminFixtureCleanup(t, adminPool, ids, nil)
	createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	users := []admin.User{
		{ID: ids[0], Name: "Admin integration Alpha", Email: testEmail(t, "admin-alpha"), Role: "USER", Status: "ACTIVE", CreatedAt: createdAt},
		{ID: ids[1], Name: "Admin integration Beta", Email: testEmail(t, "admin-beta"), Role: "ADMIN", Status: "DISABLED", CreatedAt: createdAt},
	}
	for _, user := range users {
		_, err := adminPool.Exec(ctx, `
			INSERT INTO app.users (id, name, email, password_hash, role, status, time_zone, created_at, updated_at)
			VALUES ($1::uuid, $2, $3, 'integration-only-hash', $4, $5, 'UTC', $6, $6)
		`, user.ID, user.Name, user.Email, user.Role, user.Status, createdAt)
		if err != nil {
			t.Fatalf("insert admin user fixture: %v", err)
		}
	}

	repo := NewAdminRepository(runtimePool)
	pageIDs := make([]string, 0, 2)
	seen := make(map[string]bool)
	var previous *admin.User
	for cursor, pageCount := (*admin.Cursor)(nil), 0; len(pageIDs) < len(users); pageCount++ {
		if pageCount > 1000 {
			t.Fatal("admin user pagination did not reach both fixtures")
		}
		items, err := repo.ListAdminUsers(ctx, ids[0], "", cursor, 1)
		if err != nil {
			t.Fatalf("list admin users through runtime role: %v", err)
		}
		if len(items) == 0 {
			break
		}
		item := items[0]
		if seen[item.ID] {
			t.Fatalf("admin user pagination returned duplicate ID %s", item.ID)
		}
		seen[item.ID] = true
		if previous != nil && !adminUserBefore(*previous, item) {
			t.Fatalf("admin users are not in stable descending (created_at, id) order: previous=%+v current=%+v", *previous, item)
		}
		copy := item
		previous = &copy
		for _, expected := range users {
			if item.ID == expected.ID {
				if item.ID != expected.ID || item.Name != expected.Name || item.Email != expected.Email || item.Role != expected.Role || item.Status != expected.Status || !item.CreatedAt.Equal(expected.CreatedAt) {
					t.Fatalf("admin user fields mismatch: got=%+v want=%+v", item, expected)
				}
				pageIDs = append(pageIDs, item.ID)
			}
		}
		cursor = &admin.Cursor{CreatedAt: item.CreatedAt, ID: item.ID, SnapshotAt: item.SnapshotAt}
	}
	if len(pageIDs) != len(users) {
		t.Fatalf("admin user pagination returned %d of %d fixtures", len(pageIDs), len(users))
	}
	searchItems, err := repo.ListAdminUsers(ctx, ids[0], "alpha", nil, 10)
	if err != nil || len(searchItems) != 1 || searchItems[0].ID != users[0].ID {
		t.Fatalf("case-insensitive name search returned %+v err=%v; want only Alpha fixture", searchItems, err)
	}
	literalItems, err := repo.ListAdminUsers(ctx, ids[0], "%_", nil, 10)
	if err != nil || len(literalItems) != 0 {
		t.Fatalf("search wildcard characters were not treated literally: items=%+v err=%v", literalItems, err)
	}
	orderedExpected := []string{users[0].ID, users[1].ID}
	sort.Sort(sort.Reverse(sort.StringSlice(orderedExpected)))
	if fmt.Sprint(pageIDs) != fmt.Sprint(orderedExpected) {
		t.Fatalf("equal-created_at users were not ordered by descending ID: got=%v want=%v", pageIDs, orderedExpected)
	}
	empty, err := repo.ListAdminUsers(ctx, ids[0], "", &admin.Cursor{CreatedAt: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), ID: "00000000-0000-0000-0000-000000000000", SnapshotAt: time.Now().UTC().Add(time.Minute)}, 2)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty admin user result = %d rows, err=%v; want zero rows", len(empty), err)
	}
	t.Log("admin user repository query executed under todo_test_runtime; allowlisted fields, tie ordering, non-overlapping pagination, and empty result verified")
}

func TestAdminUserPaginationPinsCreationAndUpdateSnapshotPostgreSQL(t *testing.T) {
	runtimePool, adminPool := openAdminRepositoryTestPools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	ids := []string{testUUID(t), testUUID(t), testUUID(t), testUUID(t), testUUID(t)}
	registerAdminFixtureCleanup(t, adminPool, ids, nil)
	createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	for i := 0; i < 4; i++ {
		if _, err := adminPool.Exec(ctx, `
			INSERT INTO app.users (id, name, email, password_hash, role, status, time_zone, created_at, updated_at)
			VALUES ($1::uuid, $2, $3, 'integration-only-hash', 'USER', 'ACTIVE', 'UTC', $4, $4)
		`, ids[i], fmt.Sprintf("Snapshot fixture %d", i), testEmail(t, fmt.Sprintf("snapshot-user-%d", i)), createdAt); err != nil {
			t.Fatalf("insert snapshot user fixture: %v", err)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids[:4])))
	repo := NewAdminRepository(runtimePool)
	first, err := repo.ListAdminUsers(ctx, ids[0], "", nil, 1)
	if err != nil || len(first) != 1 || first[0].ID != ids[0] || first[0].SnapshotAt.IsZero() {
		t.Fatalf("first snapshot page=%+v err=%v", first, err)
	}
	if _, err := adminPool.Exec(ctx, `UPDATE app.users SET updated_at = clock_timestamp() WHERE id = $1::uuid`, ids[1]); err != nil {
		t.Fatalf("update fixture after snapshot: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `DELETE FROM app.users WHERE id = $1::uuid`, ids[2]); err != nil {
		t.Fatalf("delete fixture after snapshot: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `
		INSERT INTO app.users (id, name, email, password_hash, role, status, time_zone)
		VALUES ($1::uuid, 'Inserted after snapshot', $2, 'integration-only-hash', 'USER', 'ACTIVE', 'UTC')
	`, ids[4], testEmail(t, "snapshot-user-late")); err != nil {
		t.Fatalf("insert user after snapshot: %v", err)
	}

	var got []string
	cursor := &admin.Cursor{CreatedAt: first[0].CreatedAt, ID: first[0].ID, SnapshotAt: first[0].SnapshotAt}
	for pages := 0; pages < 10; pages++ {
		items, listErr := repo.ListAdminUsers(ctx, ids[0], "", cursor, 1)
		if listErr != nil {
			t.Fatalf("continue snapshot pagination: %v", listErr)
		}
		if len(items) == 0 {
			break
		}
		got = append(got, items[0].ID)
		cursor = &admin.Cursor{CreatedAt: items[0].CreatedAt, ID: items[0].ID, SnapshotAt: items[0].SnapshotAt}
	}
	if fmt.Sprint(got) != fmt.Sprint([]string{ids[3]}) {
		t.Fatalf("continuation returned %v, want only unchanged snapshot member %s (updated/deleted/late rows omitted)", got, ids[3])
	}
}

func TestAdminTasksRepositoryListsAcrossOwnersAndPaginatesAsRuntimePostgreSQL(t *testing.T) {
	runtimePool, adminPool := openAdminRepositoryTestPools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	ownerIDs := []string{testUUID(t), testUUID(t)}
	taskIDs := []string{testUUID(t), testUUID(t), testUUID(t), testUUID(t), testUUID(t), testUUID(t)}
	registerAdminFixtureCleanup(t, adminPool, ownerIDs, taskIDs)
	createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	ownerNames := []string{"Admin task owner Alpha", "Admin task owner Beta"}
	for i, id := range ownerIDs {
		_, err := adminPool.Exec(ctx, `
			INSERT INTO app.users (id, name, email, password_hash, role, status, time_zone, created_at, updated_at)
			VALUES ($1::uuid, $2, $3, 'integration-only-hash', 'USER', 'ACTIVE', 'UTC', $4, $4)
		`, id, ownerNames[i], testEmail(t, fmt.Sprintf("task-owner-%d", i)), createdAt)
		if err != nil {
			t.Fatalf("insert admin task owner fixture: %v", err)
		}
	}

	type taskFixture struct {
		id, ownerID, title, status, priority string
		deleted                              bool
		dueDate                              *string
	}
	dueDate := "2100-01-01"
	fixtures := []taskFixture{
		{taskIDs[0], ownerIDs[0], "Admin fixture active TODO", task.StatusTODO, task.PriorityLow, false, &dueDate},
		{taskIDs[1], ownerIDs[1], "Admin fixture active IN_PROGRESS", task.StatusInProgress, task.PriorityMedium, false, nil},
		{taskIDs[2], ownerIDs[0], "Admin fixture completed", task.StatusDone, task.PriorityHigh, false, nil},
		{taskIDs[3], ownerIDs[1], "Admin fixture deleted", task.StatusTODO, task.PriorityUrgent, true, nil},
		{taskIDs[4], ownerIDs[0], "Admin fixture shared timestamp A", task.StatusTODO, task.PriorityMedium, false, nil},
		{taskIDs[5], ownerIDs[1], "Admin fixture shared timestamp B", task.StatusTODO, task.PriorityMedium, false, nil},
	}
	for _, fixture := range fixtures {
		var deletedAt any
		if fixture.deleted {
			deletedAt = createdAt.Add(time.Hour)
		}
		var completedAt any
		if fixture.status == task.StatusDone {
			completedAt = createdAt.Add(time.Hour)
		}
		_, err := adminPool.Exec(ctx, `
			INSERT INTO app.tasks (id, user_id, title, description, status, priority, version, created_at, updated_at, due_date, completed_at, deleted_at)
			VALUES ($1::uuid, $2::uuid, $3, 'admin integration task description', $4, $5, 7, $6, $6, $7::date, $8, $9)
		`, fixture.id, fixture.ownerID, fixture.title, fixture.status, fixture.priority, createdAt, fixture.dueDate, completedAt, deletedAt)
		if err != nil {
			t.Fatalf("insert admin task fixture %s: %v", fixture.title, err)
		}
	}

	repo := NewAdminRepository(runtimePool)
	collected := make(map[string]admin.Task)
	seen := make(map[string]bool)
	var previous *admin.Task
	var cursor *admin.Cursor
	for pageCount := 0; len(collected) < len(fixtures); pageCount++ {
		if pageCount > 1000 {
			t.Fatal("admin task pagination did not reach all fixtures")
		}
		items, err := repo.ListAdminTasks(ctx, "", "all", cursor, 2)
		if err != nil {
			t.Fatalf("list admin tasks through runtime role: %v", err)
		}
		if len(items) == 0 {
			break
		}
		for i := range items {
			item := items[i]
			if seen[item.ID] {
				t.Fatalf("admin task pagination returned duplicate ID %s", item.ID)
			}
			seen[item.ID] = true
			if previous != nil && !adminTaskBefore(*previous, item) {
				t.Fatalf("admin tasks are not in stable descending (created_at, id) order: previous=%+v current=%+v", *previous, item)
			}
			copy := item
			previous = &copy
			for _, fixture := range fixtures {
				if item.ID != fixture.id {
					continue
				}
				ownerIndex := 0
				if fixture.ownerID == ownerIDs[1] {
					ownerIndex = 1
				}
				if item.OwnerID != fixture.ownerID || item.OwnerName != ownerNames[ownerIndex] || item.Title != fixture.title || item.Description != "admin integration task description" || item.Status != fixture.status || item.Priority != fixture.priority || item.Version != 7 || !item.CreatedAt.Equal(createdAt) || !item.UpdatedAt.Equal(createdAt) {
					t.Fatalf("admin task fixture fields mismatch: got=%+v fixture=%+v", item, fixture)
				}
				if fixture.dueDate == nil && item.DueDate != nil || fixture.dueDate != nil && (item.DueDate == nil || *item.DueDate != *fixture.dueDate) {
					t.Fatalf("admin task due date mismatch for %s: got=%v want=%v", fixture.id, item.DueDate, fixture.dueDate)
				}
				if (item.CompletedAt != nil) != (fixture.status == task.StatusDone) {
					t.Fatalf("admin task completed_at mapping mismatch for %s: got=%v status=%s", fixture.id, item.CompletedAt, fixture.status)
				}
				if (item.DeletedAt != nil) != fixture.deleted {
					t.Fatalf("admin task deleted state mismatch for %s: deleted_at=%v", fixture.id, item.DeletedAt)
				}
				if fixture.deleted && item.IsOverdue {
					t.Fatalf("deleted task %s must not be reported overdue", fixture.id)
				}
				collected[item.ID] = item
			}
		}
		last := items[len(items)-1]
		cursor = &admin.Cursor{CreatedAt: last.CreatedAt, ID: last.ID, SnapshotAt: last.SnapshotAt}
	}
	if len(collected) != len(fixtures) {
		t.Fatalf("admin task pagination returned %d of %d fixtures", len(collected), len(fixtures))
	}

	active, err := repo.ListAdminTasks(ctx, "", "active", nil, 100)
	if err != nil {
		t.Fatalf("list active admin task state: %v", err)
	}
	deleted, err := repo.ListAdminTasks(ctx, "", "deleted", nil, 100)
	if err != nil {
		t.Fatalf("list deleted admin task state: %v", err)
	}
	done, err := repo.ListAdminTasks(ctx, task.StatusDone, "all", nil, 100)
	if err != nil {
		t.Fatalf("list admin tasks by DONE status: %v", err)
	}
	activeIDs, deletedIDs, doneIDs := taskIDSet(active), taskIDSet(deleted), taskIDSet(done)
	for _, fixture := range fixtures {
		if fixture.deleted && (!deletedIDs[fixture.id] || activeIDs[fixture.id]) {
			t.Fatalf("deleted state filter mismatch for fixture %s", fixture.id)
		}
		if !fixture.deleted && (!activeIDs[fixture.id] || deletedIDs[fixture.id]) {
			t.Fatalf("active state filter mismatch for fixture %s", fixture.id)
		}
		if (fixture.status == task.StatusDone) != doneIDs[fixture.id] {
			t.Fatalf("status filter mismatch for fixture %s status=%s", fixture.id, fixture.status)
		}
	}
	empty, err := repo.ListAdminTasks(ctx, "", "all", &admin.Cursor{CreatedAt: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), ID: "00000000-0000-0000-0000-000000000000"}, 2)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty admin task result = %d rows, err=%v; want zero rows", len(empty), err)
	}
	sharedOrder := make([]string, 0, 2)
	for _, fixture := range fixtures {
		if fixture.title == "Admin fixture shared timestamp A" || fixture.title == "Admin fixture shared timestamp B" {
			sharedOrder = append(sharedOrder, fixture.id)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(sharedOrder)))
	if !seen[sharedOrder[0]] || !seen[sharedOrder[1]] {
		t.Fatalf("same-created_at task fixtures were not included in pagination: %v", sharedOrder)
	}
	t.Log("admin task query executed under todo_test_runtime across two owners; owner mapping, active/completed/deleted filters, stable pagination, tie ordering, and empty result verified")
}

func TestAdminTaskPaginationPinsCreationAndUpdateSnapshotPostgreSQL(t *testing.T) {
	runtimePool, adminPool := openAdminRepositoryTestPools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	ownerIDs := []string{testUUID(t), testUUID(t)}
	taskIDs := []string{testUUID(t), testUUID(t), testUUID(t), testUUID(t), testUUID(t)}
	registerAdminFixtureCleanup(t, adminPool, ownerIDs, taskIDs)
	createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	for i, id := range ownerIDs {
		if _, err := adminPool.Exec(ctx, `
			INSERT INTO app.users (id, name, email, password_hash, role, status, time_zone, created_at, updated_at)
			VALUES ($1::uuid, $2, $3, 'integration-only-hash', 'USER', 'ACTIVE', 'UTC', $4, $4)
		`, id, fmt.Sprintf("Admin snapshot owner %d", i), testEmail(t, fmt.Sprintf("admin-snapshot-owner-%d", i)), createdAt); err != nil {
			t.Fatalf("insert task snapshot owner: %v", err)
		}
	}
	for i := 0; i < 4; i++ {
		if _, err := adminPool.Exec(ctx, `
			INSERT INTO app.tasks (id, user_id, title, description, status, priority, created_at, updated_at)
			VALUES ($1::uuid, $2::uuid, $3, '', 'TODO', 'MEDIUM', $4, $4)
		`, taskIDs[i], ownerIDs[i%2], fmt.Sprintf("Admin snapshot task %d", i), createdAt); err != nil {
			t.Fatalf("insert task snapshot fixture: %v", err)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(taskIDs[:4])))
	repo := NewAdminRepository(runtimePool)
	first, err := repo.ListAdminTasks(ctx, "", "all", nil, 1)
	if err != nil || len(first) != 1 || first[0].ID != taskIDs[0] || first[0].SnapshotAt.IsZero() {
		t.Fatalf("first Admin task snapshot page=%+v err=%v", first, err)
	}
	if _, err := adminPool.Exec(ctx, `UPDATE app.tasks SET title='changed after snapshot', updated_at=clock_timestamp() WHERE id=$1::uuid`, taskIDs[1]); err != nil {
		t.Fatalf("update task after snapshot: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `DELETE FROM app.tasks WHERE id=$1::uuid`, taskIDs[2]); err != nil {
		t.Fatalf("delete task after snapshot: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `
		INSERT INTO app.tasks (id, user_id, title, description, status, priority)
		VALUES ($1::uuid, $2::uuid, 'inserted after snapshot', '', 'TODO', 'MEDIUM')
	`, taskIDs[4], ownerIDs[0]); err != nil {
		t.Fatalf("insert task after snapshot: %v", err)
	}
	cursor := &admin.Cursor{CreatedAt: first[0].CreatedAt, ID: first[0].ID, SnapshotAt: first[0].SnapshotAt}
	items, err := repo.ListAdminTasks(ctx, "", "all", cursor, 10)
	if err != nil || len(items) != 1 || items[0].ID != taskIDs[3] {
		t.Fatalf("Admin task snapshot continuation=%+v err=%v; want only unchanged row %s", items, err, taskIDs[3])
	}
}

func openAdminRepositoryTestPools(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	adminURL, adminTarget := testdatabase.TestURL(t)
	runtimeURL, runtimeTarget := testdatabase.URL(t, "RUNTIME_DATABASE_URL", "")
	if adminTarget.Host != "<loopback>" || adminTarget.Port != 25432 || adminTarget.Database != "todo_test" || !testdatabase.SameTarget(adminTarget, runtimeTarget) {
		t.Fatal("admin repository integration tests require TEST_DATABASE_URL and RUNTIME_DATABASE_URL on 127.0.0.1:25432/todo_test")
	}
	adminConfig, err := pgx.ParseConfig(adminURL)
	if err != nil || adminConfig.Host != "127.0.0.1" || adminConfig.Port != 25432 || adminConfig.Database != "todo_test" {
		t.Fatal("TEST_DATABASE_URL must target 127.0.0.1:25432/todo_test")
	}
	runtimeConfig, err := pgx.ParseConfig(runtimeURL)
	if err != nil || runtimeConfig.Host != "127.0.0.1" || runtimeConfig.Port != 25432 || runtimeConfig.Database != "todo_test" || runtimeConfig.User != "todo_test_runtime" {
		t.Fatal("RUNTIME_DATABASE_URL must use todo_test_runtime on 127.0.0.1:25432/todo_test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtimePool, err := database.Open(ctx, runtimeURL, 2)
	if err != nil {
		t.Fatalf("open restricted runtime pool: %v", err)
	}
	if err := database.VerifyRuntimeRole(ctx, runtimePool, "todo_test_runtime"); err != nil {
		runtimePool.Close()
		t.Fatalf("restricted runtime role verification failed: %v", err)
	}
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		runtimePool.Close()
		t.Fatalf("open isolated test fixture pool: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		runtimePool.Close()
		t.Fatalf("connect isolated admin fixture pool: %v", err)
	}
	// Register pool shutdown before fixture cleanup so Go's LIFO cleanup order
	// removes only test fixtures while the administrative connection is open.
	t.Cleanup(runtimePool.Close)
	t.Cleanup(adminPool.Close)
	return runtimePool, adminPool
}

func registerAdminFixtureCleanup(t *testing.T, pool *pgxpool.Pool, userIDs, taskIDs []string) {
	t.Helper()
	userIDs = append([]string(nil), userIDs...)
	taskIDs = append([]string(nil), taskIDs...)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if len(taskIDs) > 0 {
			if _, err := pool.Exec(cleanupCtx, `DELETE FROM app.tasks WHERE id = ANY($1::uuid[])`, taskIDs); err != nil {
				t.Errorf("clean up admin task fixtures: %v", err)
			}
			var remaining int
			if err := pool.QueryRow(cleanupCtx, `SELECT count(*) FROM app.tasks WHERE id = ANY($1::uuid[])`, taskIDs).Scan(&remaining); err != nil {
				t.Errorf("verify admin task fixture cleanup: %v", err)
			} else if remaining != 0 {
				t.Errorf("admin task fixture cleanup left %d rows", remaining)
			}
		}
		if len(userIDs) > 0 {
			if _, err := pool.Exec(cleanupCtx, `DELETE FROM app.users WHERE id = ANY($1::uuid[])`, userIDs); err != nil {
				t.Errorf("clean up admin user fixtures: %v", err)
			}
			var remaining int
			if err := pool.QueryRow(cleanupCtx, `SELECT count(*) FROM app.users WHERE id = ANY($1::uuid[])`, userIDs).Scan(&remaining); err != nil {
				t.Errorf("verify admin user fixture cleanup: %v", err)
			} else if remaining != 0 {
				t.Errorf("admin user fixture cleanup left %d rows", remaining)
			}
		}
		t.Log("admin repository test fixtures removed and absence verified")
	})
}

func testUUID(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("generate unique fixture UUID: %v", err)
	}
	encoded := hex.EncodeToString(raw[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:])
}

func testEmail(t *testing.T, label string) string {
	t.Helper()
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate unique fixture email: %v", err)
	}
	return fmt.Sprintf("%s-%s@example.test", label, hex.EncodeToString(suffix[:]))
}

func adminUserBefore(left, right admin.User) bool {
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.After(right.CreatedAt)
	}
	return left.ID > right.ID
}

func adminTaskBefore(left, right admin.Task) bool {
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.After(right.CreatedAt)
	}
	return left.ID > right.ID
}

func taskIDSet(items []admin.Task) map[string]bool {
	ids := make(map[string]bool, len(items))
	for _, item := range items {
		ids[item.ID] = true
	}
	return ids
}
