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
	"net/url"
	"reflect"
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

type taskWire struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	Priority    string     `json:"priority"`
	DueDate     *string    `json:"due_date"`
	IsOverdue   bool       `json:"is_overdue"`
	CompletedAt *time.Time `json:"completed_at"`
	Version     int64      `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at"`
}

type taskListWire struct {
	Items      []taskWire `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

type activityWire struct {
	ID            string          `json:"id"`
	TaskID        string          `json:"task_id"`
	ActorUserID   string          `json:"actor_user_id"`
	EventType     string          `json:"event_type"`
	PreviousValue json.RawMessage `json:"previous_value"`
	NewValue      json.RawMessage `json:"new_value"`
	CreatedAt     time.Time       `json:"created_at"`
}

type activityListWire struct {
	Items      []activityWire `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

func TestTaskManagementOwnershipAndLifecyclePostgreSQL(t *testing.T) {
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

	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate test account suffix: %v", err)
	}
	baseEmail := "phase5-" + hex.EncodeToString(suffix[:])
	emailA, emailB := baseEmail+"-a@example.test", baseEmail+"-b@example.test"
	userAIP, userBIP := baseEmail+"-user-a", baseEmail+"-user-b"
	loginAIP, loginBIP := baseEmail+"-login-a", baseEmail+"-login-b"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM app.users WHERE email = ANY($1::text[])`, []string{emailA, emailB}); cleanupErr != nil {
			t.Errorf("clean up only the two task integration accounts: %v", cleanupErr)
		}
	})
	registerRateLimitTestCleanup(t, pool, []string{userAIP, userBIP, loginAIP, loginBIP}, []string{emailA, emailB})

	manager, err := auth.NewManager(repository.NewAuthRepository(pool))
	if err != nil {
		t.Fatalf("initialize authentication manager: %v", err)
	}
	password := "phase five integration password"
	userA, err := manager.Register(ctx, "Task User A", emailA, password, userAIP)
	if err != nil {
		t.Fatalf("register integration user A: %v", err)
	}
	userB, err := manager.Register(ctx, "Task User B", emailB, password, userBIP)
	if err != nil {
		t.Fatalf("register integration user B: %v", err)
	}
	_, tokenA, _, err := manager.Login(ctx, emailA, password, loginAIP)
	if err != nil {
		t.Fatalf("create user A session: %v", err)
	}
	_, tokenB, _, err := manager.Login(ctx, emailB, password, loginBIP)
	if err != nil {
		t.Fatalf("create user B session: %v", err)
	}
	cookieA := &http.Cookie{Name: "todo_session", Value: tokenA}
	cookieB := &http.Cookie{Name: "todo_session", Value: tokenB}
	trustedOrigin := "http://127.0.0.1:8080"
	taskService := task.NewService(repository.NewTaskRepository(pool))
	options := RouterOptions{
		Auth:          NewAuthHandlers(manager, "todo_session", false),
		Tasks:         NewTaskHandlers(taskService),
		TrustedOrigin: trustedOrigin,
	}
	server := httptest.NewServer(NewRouter(health.NewService(pool), testLogger(), t.TempDir(), options))
	defer server.Close()

	unauthenticated := sendTaskRequest(t, server, nil, http.MethodPost, "/api/tasks", `{}`)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create status = %d, want 401", unauthenticated.Code)
	}
	unauthenticatedActivity := sendTaskRequest(t, server, nil, http.MethodGet, "/api/tasks/00000000-0000-0000-0000-000000000001/activity", "")
	if unauthenticatedActivity.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated activity status = %d, want 401", unauthenticatedActivity.Code)
	}

	for _, invalid := range []string{
		`{}`,
		`{"title":"Missing status is valid, but this status is not","status":"COMPLETE"}`,
		`{"title":"spoof","user_id":"` + userB.ID + `"}`,
		`{"title":"spoof actor","actor_user_id":"` + userB.ID + `"}`,
		fmt.Sprintf(`{"title":%q}`, strings.Repeat("x", 201)),
		fmt.Sprintf(`{"title":"description limit","description":%q}`, strings.Repeat("x", 10001)),
	} {
		result := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks", invalid)
		if result.Code != http.StatusUnprocessableEntity {
			t.Errorf("invalid create body status = %d, want 422; body=%s", result.Code, result.Body.String())
		}
	}

	paginatedHistoryResult := postTask(t, server, cookieA, trustedOrigin, `{"title":"Activity pagination"}`)
	paginatedHistoryTask := decodeTaskResponse(t, paginatedHistoryResult)
	for index := 1; index <= 20; index++ {
		body := fmt.Sprintf(`{"title":"Activity pagination %02d","description":"","status":"TODO","version":%d}`, index, index)
		result := sendTaskRequest(t, server, cookieA, http.MethodPut, "/api/tasks/"+paginatedHistoryTask.ID, body)
		if result.Code != http.StatusOK {
			t.Fatalf("prepare activity pagination event %d: status=%d body=%s", index, result.Code, result.Body.String())
		}
	}
	collectedEvents := make([]activityWire, 0, 21)
	cursor := ""
	for {
		page := getActivityPage(t, server, cookieA, paginatedHistoryTask.ID, cursor)
		collectedEvents = append(collectedEvents, page.Items...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(collectedEvents) != 21 {
		t.Fatalf("activity pagination returned %d events, want 21", len(collectedEvents))
	}
	seenActivityIDs := make(map[string]bool, len(collectedEvents))
	for index, event := range collectedEvents {
		if seenActivityIDs[event.ID] {
			t.Fatalf("activity pagination duplicated event %s", event.ID)
		}
		seenActivityIDs[event.ID] = true
		if index > 0 && collectedEvents[index-1].CreatedAt.Before(event.CreatedAt) {
			t.Fatal("activity pagination ordering is not stable newest first")
		}
	}

	alpha := postTask(t, server, cookieA, trustedOrigin, `{"title":"  Alpha  "}`)
	if alpha.Code != http.StatusCreated {
		t.Fatalf("create Alpha status = %d body=%s", alpha.Code, alpha.Body.String())
	}
	alphaTask := decodeTaskResponse(t, alpha)
	if alphaTask.Title != "Alpha" || alphaTask.Status != task.StatusTODO || alphaTask.Version != 1 || alphaTask.Description != "" || alphaTask.CompletedAt != nil {
		t.Fatalf("unexpected default task fields: %+v", alphaTask)
	}
	var ownerID string
	if err := pool.QueryRow(ctx, `SELECT user_id::text FROM app.tasks WHERE id = $1`, alphaTask.ID).Scan(&ownerID); err != nil || ownerID != userA.ID {
		t.Fatalf("task owner was not taken from the session: owner=%s err=%v", ownerID, err)
	}
	alphaHistory := getActivityPage(t, server, cookieA, alphaTask.ID, "")
	if len(alphaHistory.Items) != 1 || alphaHistory.Items[0].EventType != "TASK_CREATED" || alphaHistory.Items[0].ActorUserID != userA.ID || alphaHistory.Items[0].TaskID != alphaTask.ID {
		t.Fatalf("task creation history is incorrect: %+v", alphaHistory)
	}
	assertActivityInsertFailureRollsBack(t, ctx, pool, server, cookieA, userA.ID, trustedOrigin)

	beta := postTask(t, server, cookieA, trustedOrigin, `{"title":"Beta","description":"details","status":"IN_PROGRESS"}`)
	if beta.Code != http.StatusCreated {
		t.Fatalf("create Beta status = %d body=%s", beta.Code, beta.Body.String())
	}
	betaTask := decodeTaskResponse(t, beta)

	userBTaskResult := postTask(t, server, cookieB, trustedOrigin, `{"title":"Private to user B"}`)
	if userBTaskResult.Code != http.StatusCreated {
		t.Fatalf("create user B task status = %d body=%s", userBTaskResult.Code, userBTaskResult.Body.String())
	}
	userBTask := decodeTaskResponse(t, userBTaskResult)
	foreignTaskState := func() string {
		t.Helper()
		var state string
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(t)::text FROM app.tasks t WHERE id=$1 AND user_id=$2`, userBTask.ID, userB.ID).Scan(&state); err != nil {
			t.Fatalf("read user B task state: %v", err)
		}
		return state
	}
	foreignActivityState := func() string {
		t.Helper()
		var state string
		if err := pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.created_at, a.id), '[]'::jsonb)::text FROM app.task_activity a WHERE task_id=$1 AND user_id=$2`, userBTask.ID, userB.ID).Scan(&state); err != nil {
			t.Fatalf("read user B task activity state: %v", err)
		}
		return state
	}
	assertUserBTaskUnchanged := func(operation string, wantTaskState, wantActivityState string) {
		t.Helper()
		if got := foreignTaskState(); got != wantTaskState {
			t.Errorf("user B task changed after user A's rejected %s: got %s want %s", operation, got, wantTaskState)
		}
		if got := foreignActivityState(); got != wantActivityState {
			t.Errorf("user B activity changed after user A's rejected %s: got %s want %s", operation, got, wantActivityState)
		}
	}
	userBTaskState, userBActivityState := foreignTaskState(), foreignActivityState()
	if result := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/"+userBTask.ID+"/activity", ""); result.Code != http.StatusNotFound {
		t.Fatalf("cross-user activity access status = %d, want 404", result.Code)
	}
	if result := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/00000000-0000-0000-0000-000000000000/activity", ""); result.Code != http.StatusNotFound {
		t.Fatalf("nonexistent task activity status = %d, want 404", result.Code)
	}
	if result := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/not-a-uuid/activity", ""); result.Code != http.StatusBadRequest {
		t.Fatalf("invalid activity task ID status = %d, want 400", result.Code)
	}
	if result := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/"+alphaTask.ID+"/activity?limit=101", ""); result.Code != http.StatusBadRequest {
		t.Fatalf("invalid activity limit status = %d, want 400", result.Code)
	}
	if result := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/"+alphaTask.ID+"/activity?cursor=bad", ""); result.Code != http.StatusBadRequest {
		t.Fatalf("invalid activity cursor status = %d, want 400", result.Code)
	}
	if _, err := pool.Exec(ctx, `
		WITH shared_time AS (SELECT clock_timestamp() AS created_at)
		UPDATE app.tasks
		SET created_at = (SELECT created_at FROM shared_time)
		WHERE id = ANY($1::uuid[]) AND user_id = $2`, []string{alphaTask.ID, betaTask.ID}, userA.ID); err != nil {
		t.Fatalf("set equal creation times for pagination tie-break test: %v", err)
	}

	firstPage := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?limit=1", "")
	if firstPage.Code != http.StatusOK {
		t.Fatalf("list first page status = %d body=%s", firstPage.Code, firstPage.Body.String())
	}
	var firstPageData taskListWire
	if err := json.Unmarshal(firstPage.Body.Bytes(), &firstPageData); err != nil || len(firstPageData.Items) != 1 || firstPageData.NextCursor == nil {
		t.Fatalf("unexpected first page: %+v err=%v", firstPageData, err)
	}
	secondPage := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?limit=1&cursor="+url.QueryEscape(*firstPageData.NextCursor), "")
	if secondPage.Code != http.StatusOK {
		t.Fatalf("list second page status = %d body=%s", secondPage.Code, secondPage.Body.String())
	}
	var secondPageData taskListWire
	if err := json.Unmarshal(secondPage.Body.Bytes(), &secondPageData); err != nil || len(secondPageData.Items) != 1 || secondPageData.Items[0].ID == firstPageData.Items[0].ID {
		t.Fatalf("pagination repeated or omitted the next item: page=%+v err=%v", secondPageData, err)
	}
	statusRank := func(status string) int {
		switch status {
		case task.StatusInProgress:
			return 0
		case task.StatusDone:
			return 2
		default:
			return 1
		}
	}
	firstRank, secondRank := statusRank(firstPageData.Items[0].Status), statusRank(secondPageData.Items[0].Status)
	if firstRank > secondRank {
		t.Fatalf("task pagination violated status-rank ordering: first=%s second=%s", firstPageData.Items[0].Status, secondPageData.Items[0].Status)
	}
	if firstRank == secondRank {
		if firstPageData.Items[0].CreatedAt.Before(secondPageData.Items[0].CreatedAt) {
			t.Fatal("tasks within the same status rank were not ordered newest first")
		}
		if firstPageData.Items[0].CreatedAt.Equal(secondPageData.Items[0].CreatedAt) && firstPageData.Items[0].ID <= secondPageData.Items[0].ID {
			t.Fatal("tasks within the same status rank and creation time were not ordered by descending task ID")
		}
	}
	if badLimit := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?limit=101", ""); badLimit.Code != http.StatusBadRequest {
		t.Fatalf("oversized page limit status = %d, want 400", badLimit.Code)
	}
	if badCursor := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?cursor=bad", ""); badCursor.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d, want 400", badCursor.Code)
	}

	if result := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/not-a-uuid", ""); result.Code != http.StatusBadRequest {
		t.Fatalf("invalid task ID status = %d, want 400", result.Code)
	}
	if result := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/"+userBTask.ID, ""); result.Code != http.StatusNotFound {
		t.Fatalf("cross-user get status = %d, want 404", result.Code)
	}
	if result := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/00000000-0000-0000-0000-000000000000", ""); result.Code != http.StatusNotFound {
		t.Fatalf("nonexistent owned task status = %d, want 404", result.Code)
	}
	spoofedList := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?limit=100&user_id="+url.QueryEscape(userB.ID), "")
	var spoofedListData taskListWire
	if spoofedList.Code != http.StatusOK || json.Unmarshal(spoofedList.Body.Bytes(), &spoofedListData) != nil {
		t.Fatalf("user_id query list failed: status=%d body=%s", spoofedList.Code, spoofedList.Body.String())
	}
	for _, item := range spoofedListData.Items {
		if item.ID == userBTask.ID {
			t.Fatal("user_id query parameter changed the authenticated owner's task list")
		}
	}
	if result := sendTaskRequest(t, server, cookieA, http.MethodPut, "/api/tasks/"+userBTask.ID, `{"title":"takeover","description":"","status":"DONE","version":1}`); result.Code != http.StatusNotFound {
		t.Fatalf("cross-user update status = %d, want 404", result.Code)
	}
	assertUserBTaskUnchanged("update", userBTaskState, userBActivityState)
	if result := sendTaskRequest(t, server, cookieA, http.MethodDelete, "/api/tasks/"+userBTask.ID, `{"version":1}`); result.Code != http.StatusNotFound {
		t.Fatalf("cross-user delete status = %d, want 404", result.Code)
	}
	assertUserBTaskUnchanged("delete", userBTaskState, userBActivityState)
	userBDelete := sendTaskRequest(t, server, cookieB, http.MethodDelete, "/api/tasks/"+userBTask.ID, `{"version":1}`)
	if userBDelete.Code != http.StatusOK || decodeTaskResponse(t, userBDelete).DeletedAt == nil {
		t.Fatalf("user B soft delete failed: %d %s", userBDelete.Code, userBDelete.Body.String())
	}
	deletedTaskState, deletedActivityState := foreignTaskState(), foreignActivityState()
	if result := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks/"+userBTask.ID+"/restore", `{"version":2}`); result.Code != http.StatusNotFound {
		t.Fatalf("cross-user restore status = %d, want 404", result.Code)
	}
	assertUserBTaskUnchanged("restore", deletedTaskState, deletedActivityState)
	if result := sendTaskRequest(t, server, cookieB, http.MethodGet, "/api/tasks", ""); result.Code != http.StatusOK {
		t.Fatalf("list empty user B tasks status = %d", result.Code)
	} else {
		var list taskListWire
		if err := json.Unmarshal(result.Body.Bytes(), &list); err != nil || len(list.Items) != 0 {
			t.Fatalf("deleted user B task remained in normal list: %+v err=%v", list, err)
		}
	}

	doneUpdate := sendTaskRequest(t, server, cookieA, http.MethodPut, "/api/tasks/"+alphaTask.ID, `{"title":"Alpha done","description":"finished","status":"DONE","version":1}`)
	if doneUpdate.Code != http.StatusOK {
		t.Fatalf("mark task done status = %d body=%s", doneUpdate.Code, doneUpdate.Body.String())
	}
	doneTask := decodeTaskResponse(t, doneUpdate)
	if doneTask.Version != 2 || doneTask.CompletedAt == nil {
		t.Fatalf("DONE transition did not set completion time and version: %+v", doneTask)
	}
	completedAt := *doneTask.CompletedAt
	stillDone := sendTaskRequest(t, server, cookieA, http.MethodPut, "/api/tasks/"+alphaTask.ID, `{"title":"Alpha revised","description":"finished","status":"DONE","version":2}`)
	stillDoneTask := decodeTaskResponse(t, stillDone)
	if stillDone.Code != http.StatusOK || stillDoneTask.CompletedAt == nil || !stillDoneTask.CompletedAt.Equal(completedAt) || stillDoneTask.Version != 3 {
		t.Fatalf("remaining DONE did not preserve completed_at: status=%d task=%+v", stillDone.Code, stillDoneTask)
	}
	leaveDone := sendTaskRequest(t, server, cookieA, http.MethodPut, "/api/tasks/"+alphaTask.ID, `{"title":"Alpha in progress","description":"","status":"IN_PROGRESS","version":3}`)
	leaveDoneTask := decodeTaskResponse(t, leaveDone)
	if leaveDone.Code != http.StatusOK || leaveDoneTask.CompletedAt != nil || leaveDoneTask.Version != 4 {
		t.Fatalf("leaving DONE did not clear completed_at: status=%d task=%+v", leaveDone.Code, leaveDoneTask)
	}
	alphaHistory = getActivityPage(t, server, cookieA, alphaTask.ID, "")
	gotEventTypes := make([]string, len(alphaHistory.Items))
	for index, event := range alphaHistory.Items {
		gotEventTypes[index] = event.EventType
		if event.ActorUserID != userA.ID || event.TaskID != alphaTask.ID {
			t.Fatalf("activity has incorrect task or actor ownership: %+v", event)
		}
		if index > 0 && alphaHistory.Items[index-1].CreatedAt.Before(event.CreatedAt) {
			t.Fatal("activity history is not newest first")
		}
	}
	wantEventTypes := []string{"TASK_STATUS_CHANGED", "TASK_UPDATED", "TASK_TITLE_UPDATED", "TASK_STATUS_CHANGED", "TASK_UPDATED", "TASK_CREATED"}
	if !reflect.DeepEqual(gotEventTypes, wantEventTypes) {
		t.Fatalf("activity event strategy mismatch: got=%v want=%v", gotEventTypes, wantEventTypes)
	}
	activityCountBeforeStale := len(getActivityPage(t, server, cookieA, alphaTask.ID, "").Items)
	staleUpdate := sendTaskRequest(t, server, cookieA, http.MethodPut, "/api/tasks/"+alphaTask.ID, `{"title":"stale","description":"","status":"TODO","version":3}`)
	if staleUpdate.Code != http.StatusConflict {
		t.Fatalf("stale update status = %d, want 409", staleUpdate.Code)
	}
	if activityCountAfterStale := len(getActivityPage(t, server, cookieA, alphaTask.ID, "").Items); activityCountAfterStale != activityCountBeforeStale {
		t.Fatalf("stale update created activity: before=%d after=%d", activityCountBeforeStale, activityCountAfterStale)
	}
	staleDelete := sendTaskRequest(t, server, cookieA, http.MethodDelete, "/api/tasks/"+alphaTask.ID, `{"version":3}`)
	if staleDelete.Code != http.StatusConflict {
		t.Fatalf("stale delete status = %d, want 409", staleDelete.Code)
	}

	concurrentResult := postTask(t, server, cookieA, trustedOrigin, `{"title":"Concurrent update"}`)
	concurrentTask := decodeTaskResponse(t, concurrentResult)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, title := range []string{"winner A", "winner B"} {
		workers.Add(1)
		go func(title string) {
			defer workers.Done()
			<-start
			_, updateErr := taskService.Update(ctx, userA.ID, concurrentTask.ID, 1, title, "", task.StatusTODO)
			results <- updateErr
		}(title)
	}
	close(start)
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for updateErr := range results {
		if updateErr == nil {
			successes++
		} else if errors.Is(updateErr, task.ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent update error: %v", updateErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("optimistic concurrency results: successes=%d conflicts=%d", successes, conflicts)
	}
	concurrentHistory := getActivityPage(t, server, cookieA, concurrentTask.ID, "")
	if len(concurrentHistory.Items) != 2 || concurrentHistory.Items[0].EventType != "TASK_TITLE_UPDATED" || concurrentHistory.Items[1].EventType != "TASK_CREATED" {
		t.Fatalf("concurrent update produced misleading history: %+v", concurrentHistory)
	}

	deleteRaceResult := postTask(t, server, cookieA, trustedOrigin, `{"title":"Concurrent delete and restore"}`)
	deleteRaceTask := decodeTaskResponse(t, deleteRaceResult)
	deleteResults := runTwoTaskOperations(func() error {
		_, deleteErr := taskService.Delete(ctx, userA.ID, deleteRaceTask.ID, 1)
		return deleteErr
	})
	if countOperationResults(deleteResults) != (operationResultCounts{success: 1, conflict: 1}) {
		t.Fatalf("concurrent deletes were not serialized: %+v", deleteResults)
	}
	restoreResults := runTwoTaskOperations(func() error {
		_, restoreErr := taskService.Restore(ctx, userA.ID, deleteRaceTask.ID, 2)
		return restoreErr
	})
	if countOperationResults(restoreResults) != (operationResultCounts{success: 1, conflict: 1}) {
		t.Fatalf("concurrent restores were not serialized: %+v", restoreResults)
	}

	lifecycleResult := postTask(t, server, cookieA, trustedOrigin, `{"title":"Restore lifecycle"}`)
	lifecycleTask := decodeTaskResponse(t, lifecycleResult)
	deletedResult := sendTaskRequest(t, server, cookieA, http.MethodDelete, "/api/tasks/"+lifecycleTask.ID, `{"version":1}`)
	deletedTask := decodeTaskResponse(t, deletedResult)
	if deletedResult.Code != http.StatusOK || deletedTask.Version != 2 || deletedTask.DeletedAt == nil {
		t.Fatalf("soft deletion failed: status=%d task=%+v", deletedResult.Code, deletedTask)
	}
	deletedHistory := getActivityPage(t, server, cookieA, lifecycleTask.ID, "")
	if len(deletedHistory.Items) != 2 || deletedHistory.Items[0].EventType != "TASK_DELETED" {
		t.Fatalf("soft-deleted task history was not preserved: %+v", deletedHistory)
	}
	staleRestore := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks/"+lifecycleTask.ID+"/restore", `{"version":1}`)
	if staleRestore.Code != http.StatusConflict {
		t.Fatalf("stale restore status = %d, want 409", staleRestore.Code)
	}
	if result := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/"+lifecycleTask.ID, ""); result.Code != http.StatusNotFound {
		t.Fatalf("deleted task retrieval status = %d, want 404", result.Code)
	}
	if result := sendTaskRequest(t, server, cookieA, http.MethodPut, "/api/tasks/"+lifecycleTask.ID, `{"title":"update deleted","description":"","status":"TODO","version":2}`); result.Code != http.StatusConflict {
		t.Fatalf("updating a deleted task status = %d, want 409", result.Code)
	}
	restoredResult := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks/"+lifecycleTask.ID+"/restore", `{"version":2}`)
	restoredTask := decodeTaskResponse(t, restoredResult)
	if restoredResult.Code != http.StatusOK || restoredTask.Version != 3 || restoredTask.DeletedAt != nil {
		t.Fatalf("eligible restore failed: status=%d task=%+v", restoredResult.Code, restoredTask)
	}
	lifecycleHistory := getActivityPage(t, server, cookieA, lifecycleTask.ID, "")
	if len(lifecycleHistory.Items) != 3 || lifecycleHistory.Items[0].EventType != "TASK_RESTORED" || lifecycleHistory.Items[1].EventType != "TASK_DELETED" || lifecycleHistory.Items[2].EventType != "TASK_CREATED" {
		t.Fatalf("delete and restore history is incorrect: %+v", lifecycleHistory)
	}
	if result := sendTaskRequest(t, server, cookieA, http.MethodDelete, "/api/tasks/"+lifecycleTask.ID, `{"version":2}`); result.Code != http.StatusConflict {
		t.Fatalf("stale delete after restore status = %d, want 409", result.Code)
	}
	if result := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks/"+lifecycleTask.ID+"/restore", `{"version":3}`); result.Code != http.StatusConflict {
		t.Fatalf("restoring an active task status = %d, want 409", result.Code)
	}

	expiredResult := postTask(t, server, cookieA, trustedOrigin, `{"title":"Expired recovery"}`)
	expiredTask := decodeTaskResponse(t, expiredResult)
	expiredDelete := sendTaskRequest(t, server, cookieA, http.MethodDelete, "/api/tasks/"+expiredTask.ID, `{"version":1}`)
	expiredDeletedTask := decodeTaskResponse(t, expiredDelete)
	if expiredDelete.Code != http.StatusOK {
		t.Fatalf("prepare expired restore test: %d %s", expiredDelete.Code, expiredDelete.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET deleted_at = clock_timestamp() - interval '25 hours' WHERE id = $1 AND user_id = $2`, expiredTask.ID, userA.ID); err != nil {
		t.Fatalf("expire only the test-owned task: %v", err)
	}
	expiredRestore := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks/"+expiredTask.ID+"/restore", fmt.Sprintf(`{"version":%d}`, expiredDeletedTask.Version))
	if expiredRestore.Code != http.StatusConflict || !strings.Contains(expiredRestore.Body.String(), "recovery_expired") {
		t.Fatalf("expired restore response = %d %s", expiredRestore.Code, expiredRestore.Body.String())
	}
	var retainedRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.tasks WHERE id = $1 AND user_id = $2 AND deleted_at IS NOT NULL`, expiredTask.ID, userA.ID).Scan(&retainedRows); err != nil || retainedRows != 1 {
		t.Fatalf("soft-deleted task was physically removed: rows=%d err=%v", retainedRows, err)
	}

	fullList := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?limit=100", "")
	if fullList.Code != http.StatusOK {
		t.Fatalf("list all user A tasks status = %d body=%s", fullList.Code, fullList.Body.String())
	}
	var fullListData taskListWire
	if err := json.Unmarshal(fullList.Body.Bytes(), &fullListData); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	seen := make(map[string]bool, len(fullListData.Items))
	for _, item := range fullListData.Items {
		seen[item.ID] = true
		if item.ID == userBTask.ID || item.DeletedAt != nil {
			t.Fatalf("list exposed a foreign or deleted task: %+v", item)
		}
	}
	if !seen[alphaTask.ID] || !seen[betaTask.ID] || !seen[concurrentTask.ID] || !seen[lifecycleTask.ID] || seen[expiredTask.ID] {
		t.Fatalf("unexpected active task set: %+v", seen)
	}
}

type operationResultCounts struct {
	success  int
	conflict int
}

func countOperationResults(results []error) operationResultCounts {
	counts := operationResultCounts{}
	for _, err := range results {
		if err == nil {
			counts.success++
		} else if errors.Is(err, task.ErrConflict) {
			counts.conflict++
		}
	}
	return counts
}

func runTwoTaskOperations(operation func() error) []error {
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- operation()
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	collected := make([]error, 0, 2)
	for err := range results {
		collected = append(collected, err)
	}
	return collected
}

func postTask(t *testing.T, server *httptest.Server, cookie *http.Cookie, origin, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	return performHTTP(t, server, request)
}

func sendTaskRequest(t *testing.T, server *httptest.Server, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodDelete {
		request.Header.Set("Origin", "http://127.0.0.1:8080")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	return performHTTP(t, server, request)
}

func decodeTaskResponse(t *testing.T, response *httptest.ResponseRecorder) taskWire {
	t.Helper()
	var result taskWire
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode task response (status %d): %v; body=%s", response.Code, err, response.Body.String())
	}
	return result
}

func getActivityPage(t *testing.T, server *httptest.Server, cookie *http.Cookie, taskID, cursor string) activityListWire {
	t.Helper()
	path := "/api/tasks/" + taskID + "/activity?limit=7"
	if cursor != "" {
		path += "&cursor=" + url.QueryEscape(cursor)
	}
	response := sendTaskRequest(t, server, cookie, http.MethodGet, path, "")
	if response.Code != http.StatusOK {
		t.Fatalf("get task activity status = %d body=%s", response.Code, response.Body.String())
	}
	var page activityListWire
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode activity response: %v body=%s", err, response.Body.String())
	}
	return page
}

func registerScopedFunctionCleanup(t *testing.T, pool *pgxpool.Pool, functionName string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, fmt.Sprintf(`DROP FUNCTION IF EXISTS app.%s()`, functionName)); err != nil {
			t.Errorf("drop scoped test function %s(): %v", functionName, err)
		}
	})
}

func registerScopedTriggerCleanup(t *testing.T, pool *pgxpool.Pool, tableName, triggerName string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON app.%s`, triggerName, tableName)); err != nil {
			t.Errorf("drop scoped test trigger %s on app.%s: %v", triggerName, tableName, err)
		}
	})
}

func assertActivityInsertFailureRollsBack(t *testing.T, ctx context.Context, pool *pgxpool.Pool, server *httptest.Server, cookie *http.Cookie, userID, origin string) {
	t.Helper()
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate isolated activity trigger name: %v", err)
	}
	name := hex.EncodeToString(suffix[:])
	functionName := "phase7_fail_activity_" + name
	triggerName := "phase7_fail_activity_" + name
	_, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION app.%s() RETURNS trigger
		LANGUAGE plpgsql AS $function$
		BEGIN
		  IF NEW.new_value->>'title' = 'phase7-force-activity-failure' THEN
		    RAISE EXCEPTION 'forced activity insertion failure';
		  END IF;
		  RETURN NEW;
		END
		$function$`, functionName))
	if err != nil {
		t.Fatalf("create isolated failure trigger function: %v", err)
	}
	registerScopedFunctionCleanup(t, pool, functionName)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON app.task_activity FOR EACH ROW EXECUTE FUNCTION app.%s()`, triggerName, functionName)); err != nil {
		t.Fatalf("create isolated activity failure trigger: %v", err)
	}
	registerScopedTriggerCleanup(t, pool, "task_activity", triggerName)

	createFailure := postTask(t, server, cookie, origin, `{"title":"phase7-force-activity-failure"}`)
	if createFailure.Code != http.StatusInternalServerError || strings.Contains(createFailure.Body.String(), "forced activity") {
		t.Fatalf("activity insertion failure response must be safe 500: status=%d body=%s", createFailure.Code, createFailure.Body.String())
	}
	var failedCreateTaskCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.tasks WHERE user_id = $1 AND title = 'phase7-force-activity-failure'`, userID).Scan(&failedCreateTaskCount); err != nil || failedCreateTaskCount != 0 {
		t.Fatalf("task creation was not rolled back after activity failure: count=%d err=%v", failedCreateTaskCount, err)
	}

	created := postTask(t, server, cookie, origin, `{"title":"Rollback update baseline"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create rollback test task: status=%d body=%s", created.Code, created.Body.String())
	}
	baseline := decodeTaskResponse(t, created)
	updateFailure := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+baseline.ID,
		fmt.Sprintf(`{"title":"phase7-force-activity-failure","description":"","status":"TODO","version":%d}`, baseline.Version))
	if updateFailure.Code != http.StatusInternalServerError || strings.Contains(updateFailure.Body.String(), "forced activity") {
		t.Fatalf("activity insertion failure update response must be safe 500: status=%d body=%s", updateFailure.Code, updateFailure.Body.String())
	}
	var title string
	var version int64
	if err := pool.QueryRow(ctx, `SELECT title, version FROM app.tasks WHERE id = $1 AND user_id = $2`, baseline.ID, userID).Scan(&title, &version); err != nil || title != baseline.Title || version != baseline.Version {
		t.Fatalf("task update was not rolled back after activity failure: title=%q version=%d err=%v", title, version, err)
	}
}
