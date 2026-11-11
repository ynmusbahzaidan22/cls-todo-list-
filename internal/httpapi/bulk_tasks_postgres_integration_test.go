package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

type bulkTaskWire struct {
	Items []taskWire `json:"items"`
}

func TestBulkTaskOperationsPostgreSQL(t *testing.T) {
	databaseURL, _ := testdatabase.TestURL(t)
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect test PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test PostgreSQL: %v", err)
	}

	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate isolated account suffix: %v", err)
	}
	base := "phase9-bulk-" + hex.EncodeToString(suffix[:])
	emailA, emailB := base+"-a@example.test", base+"-b@example.test"
	userAIP, userBIP := base+"-user-a", base+"-user-b"
	loginAIP, loginBIP := base+"-login-a", base+"-login-b"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM app.users WHERE email = ANY($1::text[])`, []string{emailA, emailB}); cleanupErr != nil {
			t.Errorf("clean up the isolated Phase 9 accounts: %v", cleanupErr)
		}
		var remaining int
		if countErr := pool.QueryRow(cleanupCtx, `SELECT count(*) FROM app.users WHERE email LIKE $1`, base+"%").Scan(&remaining); countErr != nil || remaining != 0 {
			t.Errorf("Phase 9 account cleanup left %d accounts (query error: %v)", remaining, countErr)
		}
	})
	registerRateLimitTestCleanup(t, pool, []string{userAIP, userBIP, loginAIP, loginBIP}, []string{emailA, emailB})

	manager, err := auth.NewManager(repository.NewAuthRepository(pool))
	if err != nil {
		t.Fatalf("initialize authentication manager: %v", err)
	}
	password := "phase nine integration password"
	userA, err := manager.Register(ctx, "Bulk User A", emailA, password, userAIP)
	if err != nil {
		t.Fatalf("register account A: %v", err)
	}
	userB, err := manager.Register(ctx, "Bulk User B", emailB, password, userBIP)
	if err != nil {
		t.Fatalf("register account B: %v", err)
	}
	_, tokenA, _, err := manager.Login(ctx, emailA, password, loginAIP)
	if err != nil {
		t.Fatalf("create account A session: %v", err)
	}
	_, tokenB, _, err := manager.Login(ctx, emailB, password, loginBIP)
	if err != nil {
		t.Fatalf("create account B session: %v", err)
	}
	cookieA := &http.Cookie{Name: "todo_session", Value: tokenA}
	cookieB := &http.Cookie{Name: "todo_session", Value: tokenB}
	const origin = "http://127.0.0.1:8080"
	taskService := task.NewService(repository.NewTaskRepository(pool))
	server := httptest.NewServer(NewRouter(health.NewService(pool), testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(manager, "todo_session", false), Tasks: NewTaskHandlers(taskService), TrustedOrigin: origin,
	}))
	defer server.Close()

	create := func(cookie *http.Cookie, title string) taskWire {
		t.Helper()
		result := postTask(t, server, cookie, origin, fmt.Sprintf(`{"title":%q}`, title))
		if result.Code != http.StatusCreated {
			t.Fatalf("create task %q status=%d body=%s", title, result.Code, result.Body.String())
		}
		return decodeTaskResponse(t, result)
	}
	request := func(cookie *http.Cookie, operation any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, marshalErr := json.Marshal(operation)
		if marshalErr != nil {
			t.Fatalf("encode bulk operation: %v", marshalErr)
		}
		return sendTaskRequest(t, server, cookie, http.MethodPost, "/api/tasks/bulk", string(encoded))
	}
	input := func(action, status string, items ...taskWire) map[string]any {
		rows := make([]map[string]any, 0, len(items))
		for _, item := range items {
			rows = append(rows, map[string]any{"id": item.ID, "version": item.Version})
		}
		return map[string]any{"action": action, "status": status, "tasks": rows}
	}
	unauthenticated := sendTaskRequest(t, server, nil, http.MethodPost, "/api/tasks/bulk", `{"action":"delete","tasks":[]}`)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated bulk status=%d want=401 body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	read := func(cookie *http.Cookie, item taskWire) taskWire {
		t.Helper()
		result := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks/"+item.ID, "")
		if result.Code != http.StatusOK {
			t.Fatalf("read task status=%d body=%s", result.Code, result.Body.String())
		}
		return decodeTaskResponse(t, result)
	}
	activityCount := func(userID, taskID string) int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.task_activity WHERE user_id=$1 AND task_id=$2`, userID, taskID).Scan(&count); err != nil {
			t.Fatalf("count task activity: %v", err)
		}
		return count
	}

	a := create(cookieA, base+" task A")
	b := create(cookieA, base+" task B")
	foreign := create(cookieB, base+" private task")
	taskState := func(item taskWire, ownerID string) string {
		t.Helper()
		var state string
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(t)::text FROM app.tasks t WHERE id=$1 AND user_id=$2`, item.ID, ownerID).Scan(&state); err != nil {
			t.Fatalf("read task %s state: %v", item.ID, err)
		}
		return state
	}
	activityState := func(item taskWire, ownerID string) string {
		t.Helper()
		var state string
		if err := pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.created_at, a.id), '[]'::jsonb)::text FROM app.task_activity a WHERE task_id=$1 AND user_id=$2`, item.ID, ownerID).Scan(&state); err != nil {
			t.Fatalf("read task %s activity state: %v", item.ID, err)
		}
		return state
	}
	assertTaskAndActivityUnchanged := func(operation string, item taskWire, ownerID, wantTaskState, wantActivityState string) {
		t.Helper()
		if got := taskState(item, ownerID); got != wantTaskState {
			t.Errorf("task %s changed after rejected mixed-owner %s: got %s want %s", item.ID, operation, got, wantTaskState)
		}
		if got := activityState(item, ownerID); got != wantActivityState {
			t.Errorf("task %s activity changed after rejected mixed-owner %s: got %s want %s", item.ID, operation, got, wantActivityState)
		}
	}
	initialStates := map[string][2]string{
		a.ID:       {taskState(a, userA.ID), activityState(a, userA.ID)},
		b.ID:       {taskState(b, userA.ID), activityState(b, userA.ID)},
		foreign.ID: {taskState(foreign, userB.ID), activityState(foreign, userB.ID)},
	}

	for name, body := range map[string]any{
		"empty selection":   map[string]any{"action": "status", "status": task.StatusDone, "tasks": []any{}},
		"duplicate IDs":     input("delete", "", a, a),
		"invalid status":    input("status", "DONE-ish", a),
		"unknown operation": input("archive", "", a),
		"oversized selection": func() map[string]any {
			rows := make([]map[string]any, task.MaxBulkTasks+1)
			for i := range rows {
				rows[i] = map[string]any{"id": fmt.Sprintf("00000000-0000-0000-0000-%012x", i+1), "version": 1}
			}
			return map[string]any{"action": "delete", "tasks": rows}
		}(),
	} {
		result := request(cookieA, body)
		if result.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s status=%d want=422 body=%s", name, result.Code, result.Body.String())
		}
	}

	beforeA, beforeB := activityCount(userA.ID, a.ID), activityCount(userA.ID, b.ID)
	mixed := request(cookieA, input("status", task.StatusDone, a, foreign))
	if mixed.Code != http.StatusNotFound {
		t.Fatalf("mixed-owner bulk status=%d want=404 body=%s", mixed.Code, mixed.Body.String())
	}
	assertTaskAndActivityUnchanged("status update", a, userA.ID, initialStates[a.ID][0], initialStates[a.ID][1])
	assertTaskAndActivityUnchanged("status update", b, userA.ID, initialStates[b.ID][0], initialStates[b.ID][1])
	assertTaskAndActivityUnchanged("status update", foreign, userB.ID, initialStates[foreign.ID][0], initialStates[foreign.ID][1])
	if got := read(cookieA, a); got.Version != a.Version || got.Status != task.StatusTODO {
		t.Fatalf("mixed-owner request changed task A: %+v", got)
	}
	if activityCount(userA.ID, a.ID) != beforeA || activityCount(userA.ID, b.ID) != beforeB {
		t.Fatal("mixed-owner request inserted activity despite rejection")
	}
	mixedDelete := request(cookieA, input("delete", "", a, foreign))
	if mixedDelete.Code != http.StatusNotFound {
		t.Fatalf("mixed-owner bulk delete status=%d want=404 body=%s", mixedDelete.Code, mixedDelete.Body.String())
	}
	assertTaskAndActivityUnchanged("delete", a, userA.ID, initialStates[a.ID][0], initialStates[a.ID][1])
	assertTaskAndActivityUnchanged("delete", b, userA.ID, initialStates[b.ID][0], initialStates[b.ID][1])
	assertTaskAndActivityUnchanged("delete", foreign, userB.ID, initialStates[foreign.ID][0], initialStates[foreign.ID][1])
	if denied := sendTaskRequest(t, server, cookieB, http.MethodGet, "/api/tasks/"+a.ID, ""); denied.Code != http.StatusNotFound {
		t.Fatalf("foreign task access status=%d want=404", denied.Code)
	}
	if denied := sendTaskRequest(t, server, cookieB, http.MethodGet, "/api/tasks/"+a.ID+"/activity", ""); denied.Code != http.StatusNotFound {
		t.Fatalf("foreign activity access status=%d want=404", denied.Code)
	}

	// A stale version among otherwise valid IDs rejects the entire transaction.
	updatedB := sendTaskRequest(t, server, cookieA, http.MethodPut, "/api/tasks/"+b.ID,
		fmt.Sprintf(`{"title":%q,"description":"","status":"TODO","version":%d}`, b.Title+" revised", b.Version))
	if updatedB.Code != http.StatusOK {
		t.Fatalf("prepare stale second item status=%d body=%s", updatedB.Code, updatedB.Body.String())
	}
	latestB := decodeTaskResponse(t, updatedB)
	countA := activityCount(userA.ID, a.ID)
	stale := request(cookieA, input("status", task.StatusDone, a, b))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale mixed request status=%d want=409 body=%s", stale.Code, stale.Body.String())
	}
	if got := read(cookieA, a); got.Version != a.Version || got.Status != task.StatusTODO || activityCount(userA.ID, a.ID) != countA {
		t.Fatal("stale bulk rejection changed the otherwise-current task or its history")
	}
	if got := read(cookieA, b); got.Version != latestB.Version || got.Status != task.StatusTODO {
		t.Fatal("stale bulk rejection changed the newer task state")
	}

	valid := request(cookieA, input("status", task.StatusDone, a, latestB))
	if valid.Code != http.StatusOK {
		t.Fatalf("valid bulk status=%d body=%s", valid.Code, valid.Body.String())
	}
	var changed bulkTaskWire
	if err := json.Unmarshal(valid.Body.Bytes(), &changed); err != nil || len(changed.Items) != 2 {
		t.Fatalf("decode bulk status response: items=%d err=%v body=%s", len(changed.Items), err, valid.Body.String())
	}
	for _, item := range changed.Items {
		if item.Status != task.StatusDone || item.Version != a.Version+1 && item.Version != latestB.Version+1 {
			t.Errorf("unexpected bulk status task: %+v", item)
		}
		var eventCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.task_activity WHERE task_id=$1 AND user_id=$2 AND event_type='TASK_STATUS_CHANGED' AND actor_user_id=$2`, item.ID, userA.ID).Scan(&eventCount); err != nil || eventCount != 1 {
			t.Errorf("task %s status event count=%d err=%v", item.ID, eventCount, err)
		}
	}
	// Ordinary per-task activity events keep the Phase 8 Undo path compatible.
	undoTarget := changed.Items[0]
	var eventID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM app.task_activity WHERE task_id=$1 AND user_id=$2 AND event_type='TASK_STATUS_CHANGED'`, undoTarget.ID, userA.ID).Scan(&eventID); err != nil {
		t.Fatalf("find bulk status history event: %v", err)
	}
	undo := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks/"+undoTarget.ID+"/activity/"+eventID+"/undo", fmt.Sprintf(`{"version":%d}`, undoTarget.Version))
	if undo.Code != http.StatusOK {
		t.Fatalf("Undo bulk status event status=%d body=%s", undo.Code, undo.Body.String())
	}
	var undoResult undoTaskResponse
	if err := json.Unmarshal(undo.Body.Bytes(), &undoResult); err != nil {
		t.Fatalf("decode Undo result: %v", err)
	}
	for index := range changed.Items {
		if changed.Items[index].ID == undoResult.Task.ID {
			changed.Items[index] = taskWire{
				ID: undoResult.Task.ID, Title: undoResult.Task.Title, Description: undoResult.Task.Description,
				Status: undoResult.Task.Status, CompletedAt: undoResult.Task.CompletedAt,
				Version: undoResult.Task.Version, CreatedAt: undoResult.Task.CreatedAt,
				UpdatedAt: undoResult.Task.UpdatedAt, DeletedAt: undoResult.Task.DeletedAt,
			}
		}
	}

	deleted := request(cookieA, input("delete", "", changed.Items...))
	if deleted.Code != http.StatusOK {
		t.Fatalf("valid bulk delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	var deletedRows bulkTaskWire
	if err := json.Unmarshal(deleted.Body.Bytes(), &deletedRows); err != nil || len(deletedRows.Items) != 2 {
		t.Fatalf("decode bulk delete result: items=%d err=%v", len(deletedRows.Items), err)
	}
	for _, item := range deletedRows.Items {
		if item.DeletedAt == nil {
			t.Errorf("bulk delete did not soft-delete task %s", item.ID)
		}
	}

	// A failure inserting one activity event rolls back every row update and event.
	c, d := create(cookieA, base+" failure C"), create(cookieA, base+" failure D")
	var triggerSuffix [8]byte
	if _, err := rand.Read(triggerSuffix[:]); err != nil {
		t.Fatalf("generate scoped trigger name: %v", err)
	}
	triggerName := "phase9_bulk_fail_" + hex.EncodeToString(triggerSuffix[:])
	functionName := triggerName + "_fn"
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION app.%s() RETURNS trigger LANGUAGE plpgsql AS $body$ BEGIN IF NEW.task_id = '%s'::uuid AND NEW.event_type = 'TASK_STATUS_CHANGED' THEN RAISE EXCEPTION 'phase9 scoped activity failure'; END IF; RETURN NEW; END $body$`, functionName, d.ID)); err != nil {
		t.Fatalf("create scoped test function: %v", err)
	}
	registerScopedFunctionCleanup(t, pool, functionName)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON app.task_activity FOR EACH ROW EXECUTE FUNCTION app.%s()`, triggerName, functionName)); err != nil {
		t.Fatalf("create scoped test trigger: %v", err)
	}
	registerScopedTriggerCleanup(t, pool, "task_activity", triggerName)
	removeTrigger := func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON app.task_activity`, triggerName)); err != nil {
			t.Errorf("drop scoped Phase 9 trigger: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, fmt.Sprintf(`DROP FUNCTION IF EXISTS app.%s()`, functionName)); err != nil {
			t.Errorf("drop scoped Phase 9 function: %v", err)
		}
	}
	beforeC, beforeD := activityCount(userA.ID, c.ID), activityCount(userA.ID, d.ID)
	failed := request(cookieA, input("status", task.StatusDone, c, d))
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("forced activity failure status=%d want=500 body=%s", failed.Code, failed.Body.String())
	}
	removeTrigger()
	for _, item := range []taskWire{c, d} {
		got := read(cookieA, item)
		if got.Status != task.StatusTODO || got.Version != item.Version {
			t.Errorf("activity insert failure left task partially updated: %+v", got)
		}
	}
	if activityCount(userA.ID, c.ID) != beforeC || activityCount(userA.ID, d.ID) != beforeD {
		t.Fatal("activity insert failure left partial activity rows")
	}

	// A task-table write failure is also rolled back before any history is committed.
	f, g := create(cookieA, base+" database failure F"), create(cookieA, base+" database failure G")
	var taskTriggerSuffix [8]byte
	if _, err := rand.Read(taskTriggerSuffix[:]); err != nil {
		t.Fatalf("generate task trigger name: %v", err)
	}
	taskTrigger := "phase9_bulk_task_fail_" + hex.EncodeToString(taskTriggerSuffix[:])
	taskFunction := taskTrigger + "_fn"
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION app.%s() RETURNS trigger LANGUAGE plpgsql AS $body$ BEGIN IF NEW.id = '%s'::uuid AND NEW.status = 'DONE' THEN RAISE EXCEPTION 'phase9 scoped task failure'; END IF; RETURN NEW; END $body$`, taskFunction, g.ID)); err != nil {
		t.Fatalf("create scoped task failure function: %v", err)
	}
	registerScopedFunctionCleanup(t, pool, taskFunction)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF status ON app.tasks FOR EACH ROW EXECUTE FUNCTION app.%s()`, taskTrigger, taskFunction)); err != nil {
		t.Fatalf("create scoped task failure trigger: %v", err)
	}
	registerScopedTriggerCleanup(t, pool, "tasks", taskTrigger)
	removeTaskTrigger := func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON app.tasks`, taskTrigger)); err != nil {
			t.Errorf("drop scoped Phase 9 task trigger: %v", err)
		}
		if _, err := pool.Exec(cleanupCtx, fmt.Sprintf(`DROP FUNCTION IF EXISTS app.%s()`, taskFunction)); err != nil {
			t.Errorf("drop scoped Phase 9 task function: %v", err)
		}
	}
	beforeF, beforeG := activityCount(userA.ID, f.ID), activityCount(userA.ID, g.ID)
	failedTaskWrite := request(cookieA, input("status", task.StatusDone, f, g))
	if failedTaskWrite.Code != http.StatusInternalServerError {
		t.Fatalf("forced task write failure status=%d want=500 body=%s", failedTaskWrite.Code, failedTaskWrite.Body.String())
	}
	removeTaskTrigger()
	for _, item := range []taskWire{f, g} {
		got := read(cookieA, item)
		if got.Status != task.StatusTODO || got.Version != item.Version {
			t.Errorf("task write failure left a partial update: %+v", got)
		}
	}
	if activityCount(userA.ID, f.ID) != beforeF || activityCount(userA.ID, g.ID) != beforeG {
		t.Fatal("task write failure left partial activity rows")
	}

	// Simultaneous requests from the same captured versions cannot both commit.
	e := create(cookieA, base+" concurrent")
	responses := make([]*httptest.ResponseRecorder, 2)
	statuses := []string{task.StatusDone, task.StatusInProgress}
	var group sync.WaitGroup
	for i := range statuses {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			responses[index] = request(cookieA, input("status", statuses[index], e))
		}(i)
	}
	group.Wait()
	countOK, countConflict := 0, 0
	for _, response := range responses {
		switch response.Code {
		case http.StatusOK:
			countOK++
		case http.StatusConflict:
			countConflict++
		default:
			t.Errorf("concurrent bulk status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if countOK != 1 || countConflict != 1 {
		t.Fatalf("concurrent results success=%d conflict=%d, want 1/1", countOK, countConflict)
	}
	var concurrentEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.task_activity WHERE user_id=$1 AND task_id=$2 AND event_type='TASK_STATUS_CHANGED'`, userA.ID, e.ID).Scan(&concurrentEvents); err != nil || concurrentEvents != 1 {
		t.Fatalf("concurrent event count=%d err=%v want=1", concurrentEvents, err)
	}
}
