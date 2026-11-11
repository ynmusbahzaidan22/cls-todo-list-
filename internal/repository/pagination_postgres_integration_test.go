package repository

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

func TestTaskPaginationSnapshotBoundariesPostgreSQL(t *testing.T) {
	runtimePool, adminPool := openAdminRepositoryTestPools(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ownerID := testUUID(t)
	activeIDs := []string{testUUID(t), testUUID(t), testUUID(t), testUUID(t), testUUID(t)}
	deletedIDs := []string{testUUID(t), testUUID(t), testUUID(t), testUUID(t), testUUID(t)}
	activityTaskID := testUUID(t)
	activityIDs := []string{testUUID(t), testUUID(t), testUUID(t), testUUID(t), testUUID(t)}
	registerAdminFixtureCleanup(t, adminPool, []string{ownerID}, append(append(append([]string{}, activeIDs...), deletedIDs...), activityTaskID))
	if _, err := adminPool.Exec(ctx, `
		INSERT INTO app.users (id, name, email, password_hash, role, status, time_zone)
		VALUES ($1::uuid, 'Pagination snapshot owner', $2, 'integration-only-hash', 'USER', 'ACTIVE', 'UTC')
	`, ownerID, testEmail(t, "pagination-snapshot-owner")); err != nil {
		t.Fatalf("insert pagination owner: %v", err)
	}
	createdAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	for i := 0; i < 4; i++ {
		if _, err := adminPool.Exec(ctx, `
			INSERT INTO app.tasks (id, user_id, title, description, status, priority, created_at, updated_at)
			VALUES ($1::uuid, $2::uuid, $3, '', 'TODO', 'MEDIUM', $4, $4)
		`, activeIDs[i], ownerID, fmt.Sprintf("pagination-active-%d", i), createdAt); err != nil {
			t.Fatalf("insert active task fixture: %v", err)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(activeIDs[:4])))
	service := task.NewService(NewTaskRepository(runtimePool), []byte("0123456789abcdef0123456789abcdef"))
	first, err := service.List(ctx, ownerID, 1, "pagination-active-", "", "")
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != activeIDs[0] || first.NextCursor == nil {
		t.Fatalf("first active snapshot page=%+v err=%v", first, err)
	}
	if _, err := adminPool.Exec(ctx, `UPDATE app.tasks SET title='updated after snapshot', updated_at=clock_timestamp() WHERE id=$1::uuid`, activeIDs[1]); err != nil {
		t.Fatalf("update active row after snapshot: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `DELETE FROM app.tasks WHERE id=$1::uuid`, activeIDs[2]); err != nil {
		t.Fatalf("delete active row after snapshot: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `
		INSERT INTO app.tasks (id, user_id, title, description, status, priority)
		VALUES ($1::uuid, $2::uuid, 'pagination-active-late', '', 'TODO', 'MEDIUM')
	`, activeIDs[4], ownerID); err != nil {
		t.Fatalf("insert active row after snapshot: %v", err)
	}
	var activeGot []string
	cursor := *first.NextCursor
	for pages := 0; pages < 10; pages++ {
		page, listErr := service.List(ctx, ownerID, 1, "pagination-active-", "", cursor)
		if listErr != nil {
			t.Fatalf("continue active snapshot: %v", listErr)
		}
		for _, item := range page.Items {
			activeGot = append(activeGot, item.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if fmt.Sprint(activeGot) != fmt.Sprint([]string{activeIDs[3]}) {
		t.Fatalf("active continuation=%v; want only unchanged row %s; no duplicate, updated, deleted, or late row", activeGot, activeIDs[3])
	}

	deletedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	for i := 0; i < 4; i++ {
		if _, err := adminPool.Exec(ctx, `
			INSERT INTO app.tasks (id, user_id, title, description, status, priority, created_at, updated_at, deleted_at)
			VALUES ($1::uuid, $2::uuid, $3, '', 'TODO', 'MEDIUM', $4, $5, $5)
		`, deletedIDs[i], ownerID, fmt.Sprintf("pagination-deleted-%d", i), deletedAt.Add(-time.Hour), deletedAt); err != nil {
			t.Fatalf("insert deleted task fixture: %v", err)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(deletedIDs[:4])))
	deletedFirst, err := service.ListDeleted(ctx, ownerID, 1, "")
	if err != nil || len(deletedFirst.Items) != 1 || deletedFirst.Items[0].ID != deletedIDs[0] || deletedFirst.NextCursor == nil {
		t.Fatalf("first deleted snapshot page=%+v err=%v", deletedFirst, err)
	}
	if _, err := adminPool.Exec(ctx, `UPDATE app.tasks SET title='updated deleted fixture', updated_at=clock_timestamp() WHERE id=$1::uuid`, deletedIDs[1]); err != nil {
		t.Fatalf("update deleted row after snapshot: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `DELETE FROM app.tasks WHERE id=$1::uuid`, deletedIDs[2]); err != nil {
		t.Fatalf("delete deleted row after snapshot: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `
		INSERT INTO app.tasks (id, user_id, title, description, status, priority, deleted_at)
		VALUES ($1::uuid, $2::uuid, 'pagination-deleted-late', '', 'TODO', 'MEDIUM', clock_timestamp())
	`, deletedIDs[4], ownerID); err != nil {
		t.Fatalf("insert deleted row after snapshot: %v", err)
	}
	deletedNext, err := service.ListDeleted(ctx, ownerID, 1, *deletedFirst.NextCursor)
	if err != nil || len(deletedNext.Items) != 1 || deletedNext.Items[0].ID != deletedIDs[3] || deletedNext.NextCursor != nil {
		t.Fatalf("deleted snapshot continuation=%+v err=%v; want only unchanged row %s", deletedNext, err, deletedIDs[3])
	}

	if _, err := adminPool.Exec(ctx, `
		INSERT INTO app.tasks (id, user_id, title, description, status, priority, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, 'pagination-activity-task', '', 'TODO', 'MEDIUM', $3, $3)
	`, activityTaskID, ownerID, createdAt); err != nil {
		t.Fatalf("insert activity task fixture: %v", err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(activityIDs[:4])))
	for i := 0; i < 4; i++ {
		if _, err := adminPool.Exec(ctx, `
			INSERT INTO app.task_activity (id, task_id, user_id, actor_user_id, event_type, previous_value, new_value, created_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $3::uuid, 'TASK_TITLE_UPDATED', '{}'::jsonb, '{}'::jsonb, $4)
		`, activityIDs[i], activityTaskID, ownerID, createdAt); err != nil {
			t.Fatalf("insert activity fixture: %v", err)
		}
	}
	activityFirst, err := service.Activity(ctx, ownerID, activityTaskID, 1, "")
	if err != nil || len(activityFirst.Items) != 1 || activityFirst.Items[0].ID != activityIDs[0] || activityFirst.NextCursor == nil {
		t.Fatalf("first activity snapshot page=%+v err=%v", activityFirst, err)
	}
	if _, err := adminPool.Exec(ctx, `UPDATE app.task_activity SET created_at=clock_timestamp() WHERE id=$1::uuid`, activityIDs[1]); err != nil {
		t.Fatalf("update activity row after snapshot: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `DELETE FROM app.task_activity WHERE id=$1::uuid`, activityIDs[2]); err != nil {
		t.Fatalf("delete activity row after snapshot: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `
		INSERT INTO app.task_activity (id, task_id, user_id, actor_user_id, event_type, previous_value, new_value)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $3::uuid, 'TASK_TITLE_UPDATED', '{}'::jsonb, '{}'::jsonb)
	`, activityIDs[4], activityTaskID, ownerID); err != nil {
		t.Fatalf("insert activity row after snapshot: %v", err)
	}
	activityNext, err := service.Activity(ctx, ownerID, activityTaskID, 1, *activityFirst.NextCursor)
	if err != nil || len(activityNext.Items) != 1 || activityNext.Items[0].ID != activityIDs[3] || activityNext.NextCursor != nil {
		t.Fatalf("activity snapshot continuation=%+v err=%v; want only unchanged row %s", activityNext, err, activityIDs[3])
	}
}
