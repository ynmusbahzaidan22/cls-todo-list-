package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/health"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/testdatabase"
)

func TestTaskUndoSafetyAndHistoryPostgreSQL(t *testing.T) {
	databaseURL, _ := testdatabase.TestURL(t)
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect test PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test PostgreSQL: %v", err)
	}

	var suffix [10]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "undo-" + hex.EncodeToString(suffix[:])
	emailA, emailB := prefix+"-a@example.test", prefix+"-b@example.test"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM app.users WHERE email = ANY($1::text[])`, []string{emailA, emailB}); cleanupErr != nil {
			t.Errorf("clean up only the Undo integration accounts: %v", cleanupErr)
		}
	})
	registerRateLimitTestCleanup(t, pool, []string{prefix + "-ip-a", prefix + "-ip-b", prefix + "-login-a"}, []string{emailA, emailB})

	manager, err := auth.NewManager(repository.NewAuthRepository(pool))
	if err != nil {
		t.Fatal(err)
	}
	userA, err := manager.Register(ctx, "Undo User A", emailA, "undo integration password A", prefix+"-ip-a")
	if err != nil {
		t.Fatalf("register user A: %v", err)
	}
	userB, err := manager.Register(ctx, "Undo User B", emailB, "undo integration password B", prefix+"-ip-b")
	if err != nil {
		t.Fatalf("register user B: %v", err)
	}
	_, tokenA, _, err := manager.Login(ctx, emailA, "undo integration password A", prefix+"-login-a")
	if err != nil {
		t.Fatal(err)
	}
	cookieA := &http.Cookie{Name: "todo_session", Value: tokenA}
	taskService := task.NewService(repository.NewTaskRepository(pool))
	server := httptest.NewServer(NewRouter(health.NewService(pool), testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(manager, "todo_session", false), Tasks: NewTaskHandlers(taskService), TrustedOrigin: "http://127.0.0.1:8080",
	}))
	defer server.Close()

	create := func(userID, title, description string) task.Task {
		t.Helper()
		created, createErr := taskService.Create(ctx, userID, title, description, task.StatusTODO)
		if createErr != nil {
			t.Fatalf("create task: %v", createErr)
		}
		return created
	}
	findEvent := func(userID, taskID, eventType string) task.Activity {
		t.Helper()
		page, activityErr := taskService.Activity(ctx, userID, taskID, 100, "")
		if activityErr != nil {
			t.Fatalf("read activity: %v", activityErr)
		}
		for _, item := range page.Items {
			if item.EventType == eventType {
				return item
			}
		}
		t.Fatalf("activity event %q was not found in %+v", eventType, page.Items)
		return task.Activity{}
	}

	// Priority Undo changes only priority, tolerates later changes to other fields,
	// and rejects a later change to priority itself.
	priorityTask := create(userA.ID, "Priority before", "description")
	if _, err := taskService.Update(ctx, userA.ID, priorityTask.ID, 1, priorityTask.Title, priorityTask.Description, priorityTask.Status, task.PriorityHigh); err != nil {
		t.Fatalf("set priority: %v", err)
	}
	priorityEvent := findEvent(userA.ID, priorityTask.ID, "TASK_PRIORITY_CHANGED")
	var previousPriority, newPriority map[string]string
	if err := json.Unmarshal(priorityEvent.PreviousValue, &previousPriority); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(priorityEvent.NewValue, &newPriority); err != nil {
		t.Fatal(err)
	}
	if previousPriority["priority"] != task.PriorityMedium || newPriority["priority"] != task.PriorityHigh || priorityEvent.ActorUserID != userA.ID {
		t.Fatalf("priority activity values/actor incorrect: %+v", priorityEvent)
	}
	if _, err := taskService.Update(ctx, userA.ID, priorityTask.ID, 2, "Title changed later", priorityTask.Description, priorityTask.Status); err != nil {
		t.Fatalf("unrelated title update: %v", err)
	}
	priorityHistory, err := taskService.Activity(ctx, userA.ID, priorityTask.ID, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	var eligiblePriority task.Activity
	for _, event := range priorityHistory.Items {
		if event.ID == priorityEvent.ID {
			eligiblePriority = event
		}
	}
	if !eligiblePriority.UndoAvailable || eligiblePriority.UndoVersion != 3 {
		t.Fatalf("priority Undo eligibility/version = %v/%d", eligiblePriority.UndoAvailable, eligiblePriority.UndoVersion)
	}
	priorityUndone, _, err := taskService.Undo(ctx, userA.ID, priorityTask.ID, priorityEvent.ID, 3)
	if err != nil || priorityUndone.Priority != task.PriorityMedium || priorityUndone.Title != "Title changed later" || priorityUndone.Version != 4 {
		t.Fatalf("priority Undo changed unrelated data or failed: task=%+v err=%v", priorityUndone, err)
	}

	reverseTask := create(userA.ID, "Original title", "description")
	if _, err := taskService.Update(ctx, userA.ID, reverseTask.ID, 1, "New title", reverseTask.Description, reverseTask.Status); err != nil {
		t.Fatal(err)
	}
	titleEventBeforePriority := findEvent(userA.ID, reverseTask.ID, "TASK_TITLE_UPDATED")
	if _, err := taskService.Update(ctx, userA.ID, reverseTask.ID, 2, "New title", reverseTask.Description, reverseTask.Status, task.PriorityUrgent); err != nil {
		t.Fatal(err)
	}
	titleUndone, _, err := taskService.Undo(ctx, userA.ID, reverseTask.ID, titleEventBeforePriority.ID, 3)
	if err != nil || titleUndone.Title != "Original title" || titleUndone.Priority != task.PriorityUrgent {
		t.Fatalf("title Undo overwrote a later priority: task=%+v err=%v", titleUndone, err)
	}
	priorityEventCount := func(taskID string) int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.task_activity WHERE user_id=$1 AND task_id=$2 AND event_type='TASK_PRIORITY_CHANGED'`, userA.ID, taskID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if _, err := taskService.Update(ctx, userA.ID, reverseTask.ID, 4, titleUndone.Title, reverseTask.Description, reverseTask.Status, task.PriorityUrgent); err != nil {
		t.Fatalf("same-value priority update: %v", err)
	}
	if count := priorityEventCount(reverseTask.ID); count != 1 {
		t.Fatalf("no-op priority update added an event: count=%d", count)
	}

	unsafeTask := create(userA.ID, "Unsafe priority", "")
	if _, err := taskService.Update(ctx, userA.ID, unsafeTask.ID, 1, unsafeTask.Title, unsafeTask.Description, unsafeTask.Status, task.PriorityHigh); err != nil {
		t.Fatal(err)
	}
	unsafeEvent := findEvent(userA.ID, unsafeTask.ID, "TASK_PRIORITY_CHANGED")
	if _, err := taskService.Update(ctx, userA.ID, unsafeTask.ID, 2, unsafeTask.Title, unsafeTask.Description, unsafeTask.Status, task.PriorityUrgent); err != nil {
		t.Fatal(err)
	}
	if _, _, err := taskService.Undo(ctx, userA.ID, unsafeTask.ID, unsafeEvent.ID, 3); !errors.Is(err, task.ErrConflict) {
		t.Fatalf("unsafe priority Undo error=%v want conflict", err)
	}
	unsafeCurrent, err := taskService.Get(ctx, userA.ID, unsafeTask.ID)
	if err != nil || unsafeCurrent.Priority != task.PriorityUrgent || unsafeCurrent.Version != 3 || priorityEventCount(unsafeTask.ID) != 2 {
		t.Fatalf("rejected priority Undo changed state/history: task=%+v events=%d err=%v", unsafeCurrent, priorityEventCount(unsafeTask.ID), err)
	}

	concurrentTask := create(userA.ID, "Concurrent priority", "")
	concurrentResults := make(chan error, 2)
	for _, priority := range []string{task.PriorityHigh, task.PriorityUrgent} {
		go func(value string) {
			_, updateErr := taskService.Update(ctx, userA.ID, concurrentTask.ID, 1, concurrentTask.Title, concurrentTask.Description, concurrentTask.Status, value)
			concurrentResults <- updateErr
		}(priority)
	}
	var succeeded, conflicted int
	for range 2 {
		updateErr := <-concurrentResults
		if updateErr == nil {
			succeeded++
		} else if errors.Is(updateErr, task.ErrConflict) {
			conflicted++
		} else {
			t.Fatalf("unexpected concurrent priority update error: %v", updateErr)
		}
	}
	if succeeded != 1 || conflicted != 1 || priorityEventCount(concurrentTask.ID) != 1 {
		t.Fatalf("concurrent priority updates success/conflict/events=%d/%d/%d", succeeded, conflicted, priorityEventCount(concurrentTask.ID))
	}

	rollbackPriorityTask := create(userA.ID, "Rollback priority", "")
	var randomName [6]byte
	if _, err := rand.Read(randomName[:]); err != nil {
		t.Fatal(err)
	}
	priorityTriggerName := "phase15_priority_failure_" + hex.EncodeToString(randomName[:])
	priorityFunctionName := priorityTriggerName + "_fn"
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION app.%s() RETURNS trigger LANGUAGE plpgsql AS $body$ BEGIN IF NEW.task_id = '%s'::uuid AND NEW.event_type = 'TASK_PRIORITY_CHANGED' THEN RAISE EXCEPTION 'phase15 scoped priority activity failure'; END IF; RETURN NEW; END $body$`, priorityFunctionName, rollbackPriorityTask.ID)); err != nil {
		t.Fatalf("create priority rollback function: %v", err)
	}
	registerScopedFunctionCleanup(t, pool, priorityFunctionName)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON app.task_activity FOR EACH ROW EXECUTE FUNCTION app.%s()`, priorityTriggerName, priorityFunctionName)); err != nil {
		t.Fatalf("create priority rollback trigger: %v", err)
	}
	registerScopedTriggerCleanup(t, pool, "task_activity", priorityTriggerName)
	if _, err := taskService.Update(ctx, userA.ID, rollbackPriorityTask.ID, 1, rollbackPriorityTask.Title, rollbackPriorityTask.Description, rollbackPriorityTask.Status, task.PriorityHigh); !errors.Is(err, task.ErrStorage) {
		t.Fatalf("priority event failure error=%v want storage failure", err)
	}
	rollbackPriorityCurrent, err := taskService.Get(ctx, userA.ID, rollbackPriorityTask.ID)
	if err != nil || rollbackPriorityCurrent.Priority != task.PriorityMedium || rollbackPriorityCurrent.Version != 1 || priorityEventCount(rollbackPriorityTask.ID) != 0 {
		t.Fatalf("failed priority event didn't roll back atomically: task=%+v event-count=%d err=%v", rollbackPriorityCurrent, priorityEventCount(rollbackPriorityTask.ID), err)
	}

	// A title Undo preserves later description and status changes on the same task.
	fieldTask := create(userA.ID, "Title A", "Description A")
	if _, err := taskService.Update(ctx, userA.ID, fieldTask.ID, 1, "Title B", "Description A", task.StatusTODO); err != nil {
		t.Fatal(err)
	}
	if _, err := taskService.Update(ctx, userA.ID, fieldTask.ID, 2, "Title B", "Description B", task.StatusInProgress); err != nil {
		t.Fatal(err)
	}
	titleEvent := findEvent(userA.ID, fieldTask.ID, "TASK_TITLE_UPDATED")
	if !titleEvent.UndoAvailable || titleEvent.UndoVersion != 3 {
		t.Fatalf("title event eligibility/version = %v/%d, want true/3", titleEvent.UndoAvailable, titleEvent.UndoVersion)
	}
	updated, _, err := taskService.Undo(ctx, userA.ID, fieldTask.ID, titleEvent.ID, 3)
	if err != nil || updated.Title != "Title A" || updated.Description != "Description B" || updated.Status != task.StatusInProgress || updated.Version != 4 {
		t.Fatalf("title Undo changed unrelated fields: task=%+v err=%v", updated, err)
	}
	descriptionEvent := findEvent(userA.ID, fieldTask.ID, "TASK_DESCRIPTION_UPDATED")
	updated, _, err = taskService.Undo(ctx, userA.ID, fieldTask.ID, descriptionEvent.ID, 4)
	if err != nil || updated.Description != "Description A" || updated.Title != "Title A" || updated.Status != task.StatusInProgress {
		t.Fatalf("description Undo changed unrelated fields: task=%+v err=%v", updated, err)
	}
	statusEvent := findEvent(userA.ID, fieldTask.ID, "TASK_STATUS_CHANGED")
	updated, _, err = taskService.Undo(ctx, userA.ID, fieldTask.ID, statusEvent.ID, 5)
	if err != nil || updated.Status != task.StatusTODO || updated.Title != "Title A" || updated.Description != "Description A" {
		t.Fatalf("status Undo failed: task=%+v err=%v", updated, err)
	}
	if _, _, err := taskService.Undo(ctx, userA.ID, fieldTask.ID, titleEvent.ID, 6); !errors.Is(err, task.ErrUndoAlreadyApplied) {
		t.Fatalf("second Undo error = %v, want already applied", err)
	}

	// A later change to the same field makes Undo ineligible and returns a conflict.
	conflictTask := create(userA.ID, "Before", "Keep")
	if _, err := taskService.Update(ctx, userA.ID, conflictTask.ID, 1, "First change", "Keep", task.StatusTODO); err != nil {
		t.Fatal(err)
	}
	conflictingEvent := findEvent(userA.ID, conflictTask.ID, "TASK_TITLE_UPDATED")
	if _, err := taskService.Update(ctx, userA.ID, conflictTask.ID, 2, "Newer title", "Keep", task.StatusTODO); err != nil {
		t.Fatal(err)
	}
	page, err := taskService.Activity(ctx, userA.ID, conflictTask.ID, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range page.Items {
		if event.ID == conflictingEvent.ID && (event.UndoAvailable || event.UndoUnavailableReason != "changed_since_event") {
			t.Fatalf("stale title event eligibility = %+v", event)
		}
	}
	if _, _, err := taskService.Undo(ctx, userA.ID, conflictTask.ID, conflictingEvent.ID, 3); !errors.Is(err, task.ErrConflict) {
		t.Fatalf("Undo after a newer same-field change = %v, want conflict", err)
	}
	// Simulate a stale browser tab: the task changed after the tab loaded its
	// activity, so the API must reject Undo without changing task state/history.
	staleUndoPath := "/api/tasks/" + conflictTask.ID + "/activity/" + conflictingEvent.ID + "/undo"
	staleUndoResponse := sendTaskRequest(t, server, cookieA, http.MethodPost, staleUndoPath, `{"version":2}`)
	var staleUndoError struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if staleUndoResponse.Code != http.StatusConflict || json.Unmarshal(staleUndoResponse.Body.Bytes(), &staleUndoError) != nil || staleUndoError.Error.Code != "version_conflict" {
		t.Fatalf("stale Undo API response = %d %s, want 409 version_conflict", staleUndoResponse.Code, staleUndoResponse.Body.String())
	}
	conflictTaskAfterUndo, err := taskService.Get(ctx, userA.ID, conflictTask.ID)
	if err != nil || conflictTaskAfterUndo.Title != "Newer title" || conflictTaskAfterUndo.Version != 3 {
		t.Fatalf("rejected stale Undo changed task state: task=%+v err=%v", conflictTaskAfterUndo, err)
	}
	conflictHistoryAfterUndo, err := taskService.Activity(ctx, userA.ID, conflictTask.ID, 100, "")
	if err != nil || len(conflictHistoryAfterUndo.Items) != len(page.Items) {
		t.Fatalf("rejected stale Undo changed activity history: before=%d after=%d err=%v", len(page.Items), len(conflictHistoryAfterUndo.Items), err)
	}
	for _, event := range conflictHistoryAfterUndo.Items {
		if event.EventType == "TASK_UNDONE" && event.UndoOfEventID == conflictingEvent.ID {
			t.Fatalf("rejected stale Undo created a false Undo event: %+v", event)
		}
	}

	// TASK_UPDATED reverses its title and description fields together.
	combinedTask := create(userA.ID, "Combined A", "Combined description A")
	if _, err := taskService.Update(ctx, userA.ID, combinedTask.ID, 1, "Combined B", "Combined description B", task.StatusTODO); err != nil {
		t.Fatal(err)
	}
	combinedEvent := findEvent(userA.ID, combinedTask.ID, "TASK_UPDATED")
	updated, _, err = taskService.Undo(ctx, userA.ID, combinedTask.ID, combinedEvent.ID, 2)
	if err != nil || updated.Title != "Combined A" || updated.Description != "Combined description A" {
		t.Fatalf("combined details Undo failed: task=%+v err=%v", updated, err)
	}

	dueTask := create(userA.ID, "Due date before", "")
	dueUpdated, err := taskService.Update(ctx, userA.ID, dueTask.ID, 1, dueTask.Title, dueTask.Description, dueTask.Status, dueTask.Priority, "2027-04-05")
	if err != nil || dueUpdated.DueDate == nil || *dueUpdated.DueDate != "2027-04-05" {
		t.Fatalf("set due date for Undo: task=%+v err=%v", dueUpdated, err)
	}
	dueEvent := findEvent(userA.ID, dueTask.ID, "TASK_DUE_DATE_CHANGED")
	dueUndone, _, err := taskService.Undo(ctx, userA.ID, dueTask.ID, dueEvent.ID, dueUpdated.Version)
	if err != nil || dueUndone.DueDate != nil || dueUndone.Version != dueUpdated.Version+1 {
		t.Fatalf("due-date Undo failed: task=%+v err=%v", dueUndone, err)
	}

	// Unsupported creation cannot be undone.
	creationEvent := findEvent(userA.ID, fieldTask.ID, "TASK_CREATED")
	if _, _, err := taskService.Undo(ctx, userA.ID, fieldTask.ID, creationEvent.ID, 6); !errors.Is(err, task.ErrUndoUnsupported) {
		t.Fatalf("creation Undo error = %v, want unsupported", err)
	}
	unsupportedRestoreTask := create(userA.ID, "Normal restore", "")
	deletedForRestore, err := taskService.Delete(ctx, userA.ID, unsupportedRestoreTask.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := taskService.Restore(ctx, userA.ID, unsupportedRestoreTask.ID, deletedForRestore.Version); err != nil {
		t.Fatal(err)
	}
	restoredEvent := findEvent(userA.ID, unsupportedRestoreTask.ID, "TASK_RESTORED")
	if _, _, err := taskService.Undo(ctx, userA.ID, unsupportedRestoreTask.ID, restoredEvent.ID, 3); !errors.Is(err, task.ErrUndoUnsupported) {
		t.Fatalf("restoration Undo error = %v, want unsupported", err)
	}

	// Undo deletion restores only the soft-deleted state and retains its history.
	deletedTask := create(userA.ID, "Deleted for Undo", "Retained")
	deletedTask, err = taskService.Delete(ctx, userA.ID, deletedTask.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	deletionEvent := findEvent(userA.ID, deletedTask.ID, "TASK_DELETED")
	if !deletionEvent.UndoAvailable {
		t.Fatalf("recent deletion should be undoable: %+v", deletionEvent)
	}
	updated, undoEvent, err := taskService.Undo(ctx, userA.ID, deletedTask.ID, deletionEvent.ID, deletedTask.Version)
	if err != nil || updated.DeletedAt != nil || updated.Version != deletedTask.Version+1 || undoEvent.EventType != "TASK_UNDONE" || undoEvent.UndoOfEventID != deletionEvent.ID {
		t.Fatalf("deletion Undo failed: task=%+v event=%+v err=%v", updated, undoEvent, err)
	}
	if undoEvent.ActorUserID != userA.ID {
		t.Fatalf("Undo actor was not derived from the authenticated owner: %+v", undoEvent)
	}
	if _, _, err := taskService.Undo(ctx, userA.ID, deletedTask.ID, deletionEvent.ID, updated.Version); !errors.Is(err, task.ErrUndoAlreadyApplied) {
		t.Fatalf("repeated deletion Undo error = %v, want already applied", err)
	}

	// Cross-user event IDs are indistinguishable from missing resources; forged actor fields are rejected.
	foreignTask := create(userB.ID, "Private event", "Private")
	foreignEvent := findEvent(userB.ID, foreignTask.ID, "TASK_CREATED")
	var foreignTaskState, foreignEventState, foreignActivityState string
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(t)::text FROM app.tasks t WHERE id=$1 AND user_id=$2`, foreignTask.ID, userB.ID).Scan(&foreignTaskState); err != nil {
		t.Fatalf("capture user B task before rejected Undo: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(a)::text FROM app.task_activity a WHERE id=$1 AND task_id=$2 AND user_id=$3`, foreignEvent.ID, foreignTask.ID, userB.ID).Scan(&foreignEventState); err != nil {
		t.Fatalf("capture user B source event before rejected Undo: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.created_at, a.id), '[]'::jsonb)::text FROM app.task_activity a WHERE task_id=$1 AND user_id=$2`, foreignTask.ID, userB.ID).Scan(&foreignActivityState); err != nil {
		t.Fatalf("capture user B history before rejected Undo: %v", err)
	}
	foreignPath := "/api/tasks/" + foreignTask.ID + "/activity/" + foreignEvent.ID + "/undo"
	if response := sendTaskRequest(t, server, cookieA, http.MethodPost, foreignPath, `{"version":1}`); response.Code != http.StatusNotFound {
		t.Fatalf("cross-user Undo response = %d, want 404", response.Code)
	}
	var gotForeignTaskState, gotForeignEventState, gotForeignActivityState string
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(t)::text FROM app.tasks t WHERE id=$1 AND user_id=$2`, foreignTask.ID, userB.ID).Scan(&gotForeignTaskState); err != nil {
		t.Fatalf("reload user B task after rejected Undo: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(a)::text FROM app.task_activity a WHERE id=$1 AND task_id=$2 AND user_id=$3`, foreignEvent.ID, foreignTask.ID, userB.ID).Scan(&gotForeignEventState); err != nil {
		t.Fatalf("reload user B source event after rejected Undo: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.created_at, a.id), '[]'::jsonb)::text FROM app.task_activity a WHERE task_id=$1 AND user_id=$2`, foreignTask.ID, userB.ID).Scan(&gotForeignActivityState); err != nil {
		t.Fatalf("reload user B history after rejected Undo: %v", err)
	}
	if gotForeignTaskState != foreignTaskState {
		t.Errorf("user B task changed after user A's rejected Undo: got %s want %s", gotForeignTaskState, foreignTaskState)
	}
	if gotForeignEventState != foreignEventState {
		t.Errorf("user B source activity changed after user A's rejected Undo: got %s want %s", gotForeignEventState, foreignEventState)
	}
	if gotForeignActivityState != foreignActivityState {
		t.Errorf("user B activity history changed after user A's rejected Undo: got %s want %s", gotForeignActivityState, foreignActivityState)
	}
	var undoRelationshipCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.task_activity WHERE task_id=$1 AND user_id=$2 AND undo_of_event_id=$3`, foreignTask.ID, userB.ID, foreignEvent.ID).Scan(&undoRelationshipCount); err != nil {
		t.Fatalf("check for foreign Undo relationship: %v", err)
	}
	if undoRelationshipCount != 0 {
		t.Errorf("rejected cross-user Undo created %d Undo relationship(s), want 0", undoRelationshipCount)
	}
	ownTask := create(userA.ID, "Actor protection", "")
	ownEvent := findEvent(userA.ID, ownTask.ID, "TASK_CREATED")
	ownPath := "/api/tasks/" + ownTask.ID + "/activity/" + ownEvent.ID + "/undo"
	if response := sendTaskRequest(t, server, cookieA, http.MethodPost, ownPath, fmt.Sprintf(`{"version":1,"actor_user_id":%q}`, userB.ID)); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("forged actor response = %d, want 422", response.Code)
	}
	apiTask := create(userA.ID, "API Undo A", "API description")
	if _, err := taskService.Update(ctx, userA.ID, apiTask.ID, 1, "API Undo B", "API description", task.StatusTODO); err != nil {
		t.Fatal(err)
	}
	apiEvent := findEvent(userA.ID, apiTask.ID, "TASK_TITLE_UPDATED")
	apiHistoryResponse := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/"+apiTask.ID+"/activity", "")
	var apiHistory struct {
		Items []struct {
			ID            string `json:"id"`
			UndoAvailable bool   `json:"undo_available"`
			UndoVersion   int64  `json:"undo_version"`
		} `json:"items"`
	}
	if apiHistoryResponse.Code != http.StatusOK || json.Unmarshal(apiHistoryResponse.Body.Bytes(), &apiHistory) != nil {
		t.Fatalf("activity API response = %d %s", apiHistoryResponse.Code, apiHistoryResponse.Body.String())
	}
	var apiUndoVersion int64
	for _, item := range apiHistory.Items {
		if item.ID == apiEvent.ID && item.UndoAvailable {
			apiUndoVersion = item.UndoVersion
		}
	}
	if apiUndoVersion != 2 {
		t.Fatalf("activity API did not expose eligible Undo version: %+v", apiHistory.Items)
	}
	apiUndoResponse := sendTaskRequest(t, server, cookieA, http.MethodPost, ownPathFor(apiTask.ID, apiEvent.ID), `{"version":2}`)
	var apiUndo struct {
		Task     taskWire `json:"task"`
		Activity struct {
			EventType     string `json:"event_type"`
			UndoOfEventID string `json:"undo_of_event_id"`
		} `json:"activity"`
	}
	if apiUndoResponse.Code != http.StatusOK || json.Unmarshal(apiUndoResponse.Body.Bytes(), &apiUndo) != nil || apiUndo.Task.Title != "API Undo A" || apiUndo.Activity.EventType != "TASK_UNDONE" || apiUndo.Activity.UndoOfEventID != apiEvent.ID {
		t.Fatalf("Undo API response = %d %s", apiUndoResponse.Code, apiUndoResponse.Body.String())
	}

	// Competing Undo attempts serialize: one succeeds and one conflicts.
	raceTask := create(userA.ID, "Race A", "Race description")
	if _, err := taskService.Update(ctx, userA.ID, raceTask.ID, 1, "Race B", "Race description", task.StatusTODO); err != nil {
		t.Fatal(err)
	}
	raceEvent := findEvent(userA.ID, raceTask.ID, "TASK_TITLE_UPDATED")
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, _, undoErr := taskService.Undo(ctx, userA.ID, raceTask.ID, raceEvent.ID, 2)
			results <- undoErr
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for result := range results {
		if result == nil {
			successes++
		} else if errors.Is(result, task.ErrConflict) || errors.Is(result, task.ErrUndoAlreadyApplied) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent Undo result: %v", result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent Undo results success=%d conflict=%d", successes, conflicts)
	}

	// An ordinary update racing with Undo also has exactly one version-checked winner.
	updateRaceTask := create(userA.ID, "Update race A", "Concurrent description")
	if _, err := taskService.Update(ctx, userA.ID, updateRaceTask.ID, 1, "Update race B", "Concurrent description", task.StatusTODO); err != nil {
		t.Fatal(err)
	}
	updateRaceEvent := findEvent(userA.ID, updateRaceTask.ID, "TASK_TITLE_UPDATED")
	start = make(chan struct{})
	results = make(chan error, 2)
	workers = sync.WaitGroup{}
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		_, _, undoErr := taskService.Undo(ctx, userA.ID, updateRaceTask.ID, updateRaceEvent.ID, 2)
		results <- undoErr
	}()
	go func() {
		defer workers.Done()
		<-start
		_, updateErr := taskService.Update(ctx, userA.ID, updateRaceTask.ID, 2, "Update race C", "Concurrent description", task.StatusTODO)
		results <- updateErr
	}()
	close(start)
	workers.Wait()
	close(results)
	twins, conflicts := 0, 0
	for result := range results {
		if result == nil {
			twins++
		} else if errors.Is(result, task.ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent update/Undo result: %v", result)
		}
	}
	if twins != 1 || conflicts != 1 {
		t.Fatalf("concurrent update/Undo results success=%d conflict=%d", twins, conflicts)
	}

	// The existing delete recovery window also bounds deletion Undo.
	expiredTask := create(userA.ID, "Expired Undo", "")
	expiredTask, err = taskService.Delete(ctx, userA.ID, expiredTask.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	expiredEvent := findEvent(userA.ID, expiredTask.ID, "TASK_DELETED")
	var oldDeletedAt time.Time
	if err := pool.QueryRow(ctx, `UPDATE app.tasks SET deleted_at = clock_timestamp() - interval '25 hours' WHERE id = $1 AND user_id = $2 RETURNING deleted_at`, expiredTask.ID, userA.ID).Scan(&oldDeletedAt); err != nil {
		t.Fatalf("expire only the test-owned deleted task: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.task_activity SET new_value = jsonb_set(new_value, '{deleted_at}', to_jsonb($2::timestamptz)) WHERE id = $1 AND user_id = $3`, expiredEvent.ID, oldDeletedAt, userA.ID); err != nil {
		t.Fatalf("align only the test-owned deletion event: %v", err)
	}
	if _, _, err := taskService.Undo(ctx, userA.ID, expiredTask.ID, expiredEvent.ID, expiredTask.Version); !errors.Is(err, task.ErrUndoExpired) {
		t.Fatalf("expired deletion Undo error = %v, want expired", err)
	}

	// A failed compensating UPDATE leaves both task state and history untouched.
	mutationFailureTask := create(userA.ID, "Mutation A", "")
	if _, err := taskService.Update(ctx, userA.ID, mutationFailureTask.ID, 1, "Mutation B", "", task.StatusTODO); err != nil {
		t.Fatal(err)
	}
	mutationFailureEvent := findEvent(userA.ID, mutationFailureTask.ID, "TASK_TITLE_UPDATED")
	var mutationTriggerSuffix [6]byte
	if _, err := rand.Read(mutationTriggerSuffix[:]); err != nil {
		t.Fatal(err)
	}
	mutationTrigger := "undo_update_fail_" + hex.EncodeToString(mutationTriggerSuffix[:])
	mutationFunction := mutationTrigger + "_fn"
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION app.%s() RETURNS trigger LANGUAGE plpgsql AS $body$ BEGIN IF OLD.id = '%s'::uuid AND NEW.title = 'Mutation A' THEN RAISE EXCEPTION 'forced Undo mutation failure'; END IF; RETURN NEW; END $body$`, mutationFunction, mutationFailureTask.ID)); err != nil {
		t.Fatalf("create scoped task mutation failure function: %v", err)
	}
	registerScopedFunctionCleanup(t, pool, mutationFunction)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF title ON app.tasks FOR EACH ROW EXECUTE FUNCTION app.%s()`, mutationTrigger, mutationFunction)); err != nil {
		t.Fatalf("create scoped task mutation failure trigger: %v", err)
	}
	registerScopedTriggerCleanup(t, pool, "tasks", mutationTrigger)
	if _, _, err := taskService.Undo(ctx, userA.ID, mutationFailureTask.ID, mutationFailureEvent.ID, 2); !errors.Is(err, task.ErrStorage) {
		t.Fatalf("Undo with failing task mutation = %v, want storage error", err)
	}
	var mutationTitle string
	var mutationVersion int64
	if err := pool.QueryRow(ctx, `SELECT title, version FROM app.tasks WHERE id = $1 AND user_id = $2`, mutationFailureTask.ID, userA.ID).Scan(&mutationTitle, &mutationVersion); err != nil || mutationTitle != "Mutation B" || mutationVersion != 2 {
		t.Fatalf("failed compensation changed task: title=%q version=%d err=%v", mutationTitle, mutationVersion, err)
	}
	var mutationUndoRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.task_activity WHERE undo_of_event_id = $1 AND user_id = $2`, mutationFailureEvent.ID, userA.ID).Scan(&mutationUndoRows); err != nil || mutationUndoRows != 0 {
		t.Fatalf("failed compensation created Undo history: count=%d err=%v", mutationUndoRows, err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER %s ON app.tasks`, mutationTrigger)); err != nil {
		t.Fatalf("drop task mutation failure trigger: %v", err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION app.%s()`, mutationFunction)); err != nil {
		t.Fatalf("drop task mutation failure function: %v", err)
	}

	// A failed history insert rolls back the task compensation and version increment.
	rollbackTask := create(userA.ID, "Rollback A", "")
	if _, err := taskService.Update(ctx, userA.ID, rollbackTask.ID, 1, "Rollback B", "", task.StatusTODO); err != nil {
		t.Fatal(err)
	}
	rollbackEvent := findEvent(userA.ID, rollbackTask.ID, "TASK_TITLE_UPDATED")
	var triggerSuffix [6]byte
	if _, err := rand.Read(triggerSuffix[:]); err != nil {
		t.Fatal(err)
	}
	triggerName := "undo_insert_fail_" + hex.EncodeToString(triggerSuffix[:])
	functionName := triggerName + "_fn"
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION app.%s() RETURNS trigger LANGUAGE plpgsql AS $body$ BEGIN IF NEW.undo_of_event_id IS NOT NULL THEN RAISE EXCEPTION 'forced Undo history failure'; END IF; RETURN NEW; END $body$`, functionName)); err != nil {
		t.Fatalf("create scoped Undo failure function: %v", err)
	}
	registerScopedFunctionCleanup(t, pool, functionName)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON app.task_activity FOR EACH ROW EXECUTE FUNCTION app.%s()`, triggerName, functionName)); err != nil {
		t.Fatalf("create scoped Undo failure trigger: %v", err)
	}
	registerScopedTriggerCleanup(t, pool, "task_activity", triggerName)
	if _, _, err := taskService.Undo(ctx, userA.ID, rollbackTask.ID, rollbackEvent.ID, 2); !errors.Is(err, task.ErrStorage) {
		t.Fatalf("Undo with failing event insert = %v, want storage error", err)
	}
	var rollbackTitle string
	var rollbackVersion int64
	if err := pool.QueryRow(ctx, `SELECT title, version FROM app.tasks WHERE id = $1 AND user_id = $2`, rollbackTask.ID, userA.ID).Scan(&rollbackTitle, &rollbackVersion); err != nil || rollbackTitle != "Rollback B" || rollbackVersion != 2 {
		t.Fatalf("failed Undo did not roll back task update: title=%q version=%d err=%v", rollbackTitle, rollbackVersion, err)
	}
	var undoRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.task_activity WHERE undo_of_event_id = $1 AND user_id = $2`, rollbackEvent.ID, userA.ID).Scan(&undoRows); err != nil || undoRows != 0 {
		t.Fatalf("failed Undo left a history event: count=%d err=%v", undoRows, err)
	}

	// History ordering includes the compensating event and retains the original.
	finalPage, err := taskService.Activity(ctx, userA.ID, fieldTask.ID, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	var finalUndo *task.Activity
	for index := range finalPage.Items {
		if finalPage.Items[index].UndoOfEventID == titleEvent.ID {
			finalUndo = &finalPage.Items[index]
			break
		}
	}
	if finalUndo == nil || finalUndo.EventType != "TASK_UNDONE" || !strings.Contains(string(finalUndo.NewValue), "TASK_TITLE_UPDATED") {
		t.Fatalf("compensating event does not identify its original action: %+v", finalUndo)
	}
}

func ownPathFor(taskID, eventID string) string {
	return "/api/tasks/" + taskID + "/activity/" + eventID + "/undo"
}
