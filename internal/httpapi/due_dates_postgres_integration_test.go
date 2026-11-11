package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/health"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/testdatabase"
)

func TestDueDatesAndTimeZonesPostgreSQL(t *testing.T) {
	databaseURL, _ := testdatabase.TestURL(t)
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	identity := hex.EncodeToString(suffix[:])
	email := "phase16-due-" + identity + "@example.test"
	registerIP, loginIP := "phase16-due-register-"+identity, "phase16-due-login-"+identity
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 8*time.Second)
		defer stop()
		if _, err := pool.Exec(cleanup, `DELETE FROM app.users WHERE email=$1`, email); err != nil {
			t.Errorf("cleanup Phase 16 test account: %v", err)
		}
	})
	registerRateLimitTestCleanup(t, pool, []string{registerIP, loginIP}, []string{email})
	manager, err := auth.NewManager(repository.NewAuthRepository(pool))
	if err != nil {
		t.Fatal(err)
	}
	user, err := manager.Register(ctx, "Due Date User", email, "phase sixteen due integration password", registerIP)
	if err != nil {
		t.Fatal(err)
	}
	_, token, _, err := manager.Login(ctx, email, "phase sixteen due integration password", loginIP)
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "todo_session", Value: token}
	taskRepo := repository.NewTaskRepository(pool)
	taskService := task.NewService(taskRepo, []byte("0123456789abcdef0123456789abcdef"))
	templateService := task.NewTemplateService(repository.NewTemplateRepository(pool))
	preferences := repository.NewPreferenceRepository(pool)
	server := httptest.NewServer(NewRouter(health.NewService(pool), testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(manager, "todo_session", false), Tasks: NewTaskHandlers(taskService),
		Templates: NewTemplateHandlers(templateService), Preferences: NewPreferenceHandlers(preferences), TrustedOrigin: "http://127.0.0.1:8080",
	}))
	defer server.Close()
	var zone pgtype.Text
	if err := pool.QueryRow(ctx, `SELECT time_zone FROM app.users WHERE id=$1`, user.ID).Scan(&zone); err != nil || zone.Valid {
		t.Fatalf("new account timezone valid=%t err=%v; want device default NULL", zone.Valid, err)
	}
	preferencesResponse := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/preferences", "")
	if preferencesResponse.Code != http.StatusOK || !strings.Contains(preferencesResponse.Body.String(), `"time_zone":null`) {
		t.Fatalf("new account preference response=%d %s; want device default", preferencesResponse.Code, preferencesResponse.Body.String())
	}
	if result := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/preferences", `{"time_zone":"America/Los_Angeles"}`); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"time_zone":"America/Los_Angeles"`) {
		t.Fatalf("persist manual timezone: %d %s", result.Code, result.Body.String())
	}
	if result := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/preferences", `{"time_zone":null}`); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"time_zone":null`) {
		t.Fatalf("reset to device timezone: %d %s", result.Code, result.Body.String())
	}
	if err := pool.QueryRow(ctx, `SELECT time_zone FROM app.users WHERE id=$1`, user.ID).Scan(&zone); err != nil || zone.Valid {
		t.Fatalf("device timezone reset valid=%t err=%v; want NULL", zone.Valid, err)
	}
	if result := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/preferences", `{"time_zone":"Asia/Amman"}`); result.Code != http.StatusOK {
		t.Fatalf("set timezone: %d %s", result.Code, result.Body.String())
	}
	var dbToday string
	if err := pool.QueryRow(ctx, `SELECT to_char((clock_timestamp() AT TIME ZONE time_zone)::date,'YYYY-MM-DD') FROM app.users WHERE id=$1`, user.ID).Scan(&dbToday); err != nil {
		t.Fatal(err)
	}
	today, err := time.Parse("2006-01-02", dbToday)
	if err != nil {
		t.Fatal(err)
	}
	yesterday, tomorrow := today.AddDate(0, 0, -1).Format("2006-01-02"), today.AddDate(0, 0, 1).Format("2006-01-02")
	for _, zone := range []string{"", "Local", "Not/AZone", "UTC+3"} {
		body, _ := json.Marshal(map[string]string{"time_zone": zone})
		result := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/preferences", string(body))
		if result.Code != http.StatusUnprocessableEntity {
			t.Errorf("invalid zone %q status=%d", zone, result.Code)
		}
	}
	if result := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/preferences", `{"time_zone":"Asia/Amman"}`); result.Code != http.StatusOK {
		t.Fatalf("set timezone: %d %s", result.Code, result.Body.String())
	}
	create := func(title, status string, due any) taskWire {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"title": title, "status": status, "priority": "LOW", "due_date": due})
		result := sendTaskRequest(t, server, cookie, http.MethodPost, "/api/tasks", string(raw))
		if result.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", title, result.Code, result.Body.String())
		}
		return decodeTaskResponse(t, result)
	}
	old := create("Phase16 overdue", task.StatusTODO, yesterday)
	current := create("Phase16 today", task.StatusTODO, dbToday)
	future := create("Phase16 upcoming", task.StatusTODO, tomorrow)
	none := create("Phase16 no date", task.StatusTODO, nil)
	done := create("Phase16 done", task.StatusDone, yesterday)
	for _, date := range []string{"2027-02-30", "2027-2-03", "2027-01-01T00:00:00Z", "infinity", "0000-01-01"} {
		result := sendTaskRequest(t, server, cookie, http.MethodPost, "/api/tasks", fmt.Sprintf(`{"title":"invalid","due_date":%q}`, date))
		if result.Code != http.StatusUnprocessableEntity {
			t.Errorf("invalid date %q status=%d", date, result.Code)
		}
	}
	if old.DueDate == nil || *old.DueDate != yesterday || !old.IsOverdue || current.IsOverdue || future.IsOverdue || none.DueDate != nil || done.IsOverdue {
		t.Fatalf("incorrect overdue semantics: %+v %+v %+v %+v %+v", old, current, future, none, done)
	}
	for filter, want := range map[string]string{"overdue": old.ID, "today": current.ID, "upcoming": future.ID, "none": none.ID} {
		result := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks?due="+filter, "")
		page := decodeTaskListResponse(t, result)
		if result.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].ID != want {
			t.Errorf("due filter %s result %d %+v %s", filter, result.Code, page.Items, result.Body.String())
		}
	}
	first := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks?limit=1&q=Phase16&tz=Asia%2FAmman", "")
	firstPage := decodeTaskListResponse(t, first)
	if first.Code != http.StatusOK || firstPage.NextCursor == nil {
		t.Fatalf("due cursor was not issued: %d %s", first.Code, first.Body.String())
	}
	changedDue := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks?limit=1&q=Phase16&due=none&tz=Asia%2FAmman&cursor="+url.QueryEscape(*firstPage.NextCursor), "")
	if changedDue.Code != http.StatusBadRequest {
		t.Fatalf("filter mismatch cursor status=%d", changedDue.Code)
	}
	tampered := *firstPage.NextCursor
	if tampered[len(tampered)-1] == 'A' {
		tampered = tampered[:len(tampered)-1] + "B"
	} else {
		tampered = tampered[:len(tampered)-1] + "A"
	}
	if response := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks?limit=1&q=Phase16&tz=Asia%2FAmman&cursor="+url.QueryEscape(tampered), ""); response.Code != http.StatusBadRequest {
		t.Fatalf("tampered cursor status=%d", response.Code)
	}
	combined := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks?due=overdue&priority=LOW&status=TODO&q=Phase16", "")
	if page := decodeTaskListResponse(t, combined); combined.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].ID != old.ID {
		t.Fatalf("combined due filters: %d %+v %s", combined.Code, page.Items, combined.Body.String())
	}
	for _, bad := range []string{"invalid", "OVERDUE"} {
		if result := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks?due="+bad, ""); result.Code != http.StatusBadRequest {
			t.Errorf("invalid due filter %q status=%d", bad, result.Code)
		}
	}
	legacy := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+old.ID, httpBody(old.Title, 1))
	if legacy.Code != http.StatusOK || decodeTaskResponse(t, legacy).DueDate == nil {
		t.Fatalf("omitted due_date did not preserve: %d %s", legacy.Code, legacy.Body.String())
	}
	cleared := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+old.ID, httpBodyWithDue(old.Title, 2, "null"))
	if cleared.Code != http.StatusOK || decodeTaskResponse(t, cleared).DueDate != nil {
		t.Fatalf("null due_date did not clear: %d %s", cleared.Code, cleared.Body.String())
	}
	history := getActivityPage(t, server, cookie, old.ID, "")
	var dueEventID string
	for _, event := range history.Items {
		if event.EventType == "TASK_DUE_DATE_CHANGED" {
			dueEventID = event.ID
			break
		}
	}
	if dueEventID == "" {
		t.Fatal("due-date change event missing")
	}
	undo := sendTaskRequest(t, server, cookie, http.MethodPost, "/api/tasks/"+old.ID+"/activity/"+dueEventID+"/undo", `{"version":3}`)
	var undone struct {
		Task taskWire `json:"task"`
	}
	if undo.Code != http.StatusOK || json.Unmarshal(undo.Body.Bytes(), &undone) != nil || undone.Task.DueDate == nil || *undone.Task.DueDate != yesterday {
		t.Fatalf("due-date Undo: %d %+v %s", undo.Code, undone, undo.Body.String())
	}
	repeated := sendTaskRequest(t, server, cookie, http.MethodPost, "/api/tasks/"+old.ID+"/activity/"+dueEventID+"/undo", `{"version":4}`)
	if repeated.Code != http.StatusConflict {
		t.Fatalf("repeated due Undo status=%d: %s", repeated.Code, repeated.Body.String())
	}
	firstChange := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+old.ID, httpBodyWithDue(old.Title, 4, fmt.Sprintf("%q", dbToday)))
	if firstChange.Code != http.StatusOK {
		t.Fatalf("first due change: %d %s", firstChange.Code, firstChange.Body.String())
	}
	history = getActivityPage(t, server, cookie, old.ID, "")
	var staleEvent string
	for _, event := range history.Items {
		if event.EventType == "TASK_DUE_DATE_CHANGED" {
			staleEvent = event.ID
			break
		}
	}
	secondChange := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+old.ID, httpBodyWithDue(old.Title, 5, fmt.Sprintf("%q", tomorrow)))
	if secondChange.Code != http.StatusOK {
		t.Fatalf("second due change: %d %s", secondChange.Code, secondChange.Body.String())
	}
	beforeReject := getActivityPage(t, server, cookie, old.ID, "")
	staleUndo := sendTaskRequest(t, server, cookie, http.MethodPost, "/api/tasks/"+old.ID+"/activity/"+staleEvent+"/undo", `{"version":6}`)
	if staleUndo.Code != http.StatusConflict {
		t.Fatalf("unsafe stale due Undo status=%d want=409: %s", staleUndo.Code, staleUndo.Body.String())
	}
	latest := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks/"+old.ID, "")
	if latest.Code != http.StatusOK || decodeTaskResponse(t, latest).DueDate == nil || *decodeTaskResponse(t, latest).DueDate != tomorrow {
		t.Fatalf("stale Undo changed task date: %d %s", latest.Code, latest.Body.String())
	}
	afterReject := getActivityPage(t, server, cookie, old.ID, "")
	if len(afterReject.Items) != len(beforeReject.Items) {
		t.Fatalf("rejected Undo changed history count from %d to %d", len(beforeReject.Items), len(afterReject.Items))
	}
	template := sendTaskRequest(t, server, cookie, http.MethodPost, "/api/task-templates", "{\"name\":\"Phase16 offsets\",\"items\":[{\"title\":\"Offset zero\",\"due_in_days\":0},{\"title\":\"Offset one\",\"due_in_days\":1}]}")
	if template.Code != http.StatusCreated {
		t.Fatalf("create due template: %d %s", template.Code, template.Body.String())
	}
	var templateResult struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	if err := json.Unmarshal(template.Body.Bytes(), &templateResult); err != nil {
		t.Fatal(err)
	}
	var deviceToday string
	if err := pool.QueryRow(ctx, `SELECT to_char((clock_timestamp() AT TIME ZONE 'America/Los_Angeles')::date,'YYYY-MM-DD')`).Scan(&deviceToday); err != nil {
		t.Fatal(err)
	}
	deviceTomorrowDate, err := time.Parse("2006-01-02", deviceToday)
	if err != nil {
		t.Fatal(err)
	}
	deviceTomorrow := deviceTomorrowDate.AddDate(0, 0, 1).Format("2006-01-02")
	apply := sendTaskRequest(t, server, cookie, http.MethodPost, "/api/task-templates/"+templateResult.ID+"/apply", fmt.Sprintf(`{"version":%d,"time_zone":"America/Los_Angeles"}`, templateResult.Version))
	var applied struct {
		Items []taskWire `json:"items"`
	}
	if apply.Code != http.StatusCreated || json.Unmarshal(apply.Body.Bytes(), &applied) != nil || len(applied.Items) != 2 || applied.Items[0].DueDate == nil || *applied.Items[0].DueDate != deviceToday || applied.Items[1].DueDate == nil || *applied.Items[1].DueDate != deviceTomorrow {
		t.Fatalf("template offsets: %d %+v %s", apply.Code, applied, apply.Body.String())
	}
	compat := create("Phase16 operation compatibility", task.StatusTODO, tomorrow)
	deleted := sendTaskRequest(t, server, cookie, http.MethodDelete, "/api/tasks/"+compat.ID, `{"version":1}`)
	if deleted.Code != http.StatusOK || decodeTaskResponse(t, deleted).DueDate == nil {
		t.Fatalf("delete lost due date: %d %s", deleted.Code, deleted.Body.String())
	}
	restored := sendTaskRequest(t, server, cookie, http.MethodPost, "/api/tasks/"+compat.ID+"/restore", `{"version":2}`)
	if restored.Code != http.StatusOK || decodeTaskResponse(t, restored).DueDate == nil || *decodeTaskResponse(t, restored).DueDate != tomorrow {
		t.Fatalf("restore lost due date: %d %s", restored.Code, restored.Body.String())
	}
	bulkStatus := sendTaskRequest(t, server, cookie, http.MethodPost, "/api/tasks/bulk", fmt.Sprintf(`{"action":"status","status":"DONE","tasks":[{"id":%q,"version":3}]}`, compat.ID))
	var bulkResult struct {
		Items []taskWire `json:"items"`
	}
	if bulkStatus.Code != http.StatusOK || json.Unmarshal(bulkStatus.Body.Bytes(), &bulkResult) != nil || len(bulkResult.Items) != 1 || bulkResult.Items[0].DueDate == nil || *bulkResult.Items[0].DueDate != tomorrow {
		t.Fatalf("bulk status changed due date: %d %s", bulkStatus.Code, bulkStatus.Body.String())
	}
	bulkDelete := sendTaskRequest(t, server, cookie, http.MethodPost, "/api/tasks/bulk", fmt.Sprintf(`{"action":"delete","tasks":[{"id":%q,"version":4}]}`, compat.ID))
	if bulkDelete.Code != http.StatusOK {
		t.Fatalf("bulk delete: %d %s", bulkDelete.Code, bulkDelete.Body.String())
	}
	deletedPage := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks/deleted", "")
	var deletedResult struct {
		Items []taskWire `json:"items"`
	}
	if deletedPage.Code != http.StatusOK || json.Unmarshal(deletedPage.Body.Bytes(), &deletedResult) != nil {
		t.Fatalf("list deleted compatibility task: %d %s", deletedPage.Code, deletedPage.Body.String())
	}
	keptDueDate := false
	for _, item := range deletedResult.Items {
		if item.ID == compat.ID && item.DueDate != nil && *item.DueDate == tomorrow {
			keptDueDate = true
		}
	}
	if !keptDueDate {
		t.Fatalf("bulk delete did not preserve due date in recently deleted list: %+v", deletedResult.Items)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET due_date='infinity'::date WHERE id=$1`, future.ID); err == nil {
		t.Fatal("database accepted infinite due date")
	}
	if _, err := pool.Exec(ctx, `UPDATE app.task_template_items SET due_in_days=3651 WHERE template_id=$1`, templateResult.ID); err == nil {
		t.Fatal("database accepted template offset above 3650")
	}
	atomic := create("Phase16 atomic event failure", task.StatusTODO, nil)
	triggerName := "phase16_activity_failure_" + hex.EncodeToString(suffix[:])
	functionName := triggerName + "_fn"
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION app.%s() RETURNS trigger LANGUAGE plpgsql AS $body$ BEGIN IF NEW.user_id='%s'::uuid AND (NEW.event_type='TASK_DUE_DATE_CHANGED' OR NEW.event_type='TASK_CREATED') THEN RAISE EXCEPTION 'Phase 16 scoped activity insert failure'; END IF; RETURN NEW; END $body$`, functionName, user.ID)); err != nil {
		t.Fatal("create scoped Phase 16 trigger function:", err)
	}
	registerScopedFunctionCleanup(t, pool, functionName)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON app.task_activity FOR EACH ROW EXECUTE FUNCTION app.%s()`, triggerName, functionName)); err != nil {
		t.Fatal("create scoped Phase 16 trigger:", err)
	}
	registerScopedTriggerCleanup(t, pool, "task_activity", triggerName)
	failure := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+atomic.ID, httpBodyWithDue(atomic.Title, 1, fmt.Sprintf("%q", tomorrow)))
	if failure.Code != http.StatusInternalServerError {
		t.Fatalf("due history failure status=%d want=500: %s", failure.Code, failure.Body.String())
	}
	atomicCurrent := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks/"+atomic.ID, "")
	if atomicCurrent.Code != http.StatusOK || decodeTaskResponse(t, atomicCurrent).Version != 1 || decodeTaskResponse(t, atomicCurrent).DueDate != nil {
		t.Fatalf("failed due event did not roll back task: %d %s", atomicCurrent.Code, atomicCurrent.Body.String())
	}
	var templateTasksBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.tasks WHERE user_id=$1 AND title IN ('Offset zero','Offset one')`, user.ID).Scan(&templateTasksBefore); err != nil {
		t.Fatal(err)
	}
	failedApply := sendTaskRequest(t, server, cookie, http.MethodPost, "/api/task-templates/"+templateResult.ID+"/apply", fmt.Sprintf(`{"version":%d,"mode":"new_set"}`, templateResult.Version))
	if failedApply.Code != http.StatusInternalServerError {
		t.Fatalf("template event failure status=%d want=500: %s", failedApply.Code, failedApply.Body.String())
	}
	var templateTasksAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.tasks WHERE user_id=$1 AND title IN ('Offset zero','Offset one')`, user.ID).Scan(&templateTasksAfter); err != nil || templateTasksAfter != templateTasksBefore {
		t.Fatalf("failed template application partially committed tasks: before=%d after=%d err=%v", templateTasksBefore, templateTasksAfter, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app.tasks (user_id,title,status,priority,due_date,completed_at)
		SELECT $1,'Phase16 perf '||g::text, CASE WHEN g%10=0 THEN 'DONE' ELSE 'TODO' END,'MEDIUM',
		CASE WHEN g%7=0 THEN NULL ELSE (clock_timestamp() AT TIME ZONE 'Asia/Amman')::date-(g%45) END,
		CASE WHEN g%10=0 THEN clock_timestamp() ELSE NULL END FROM generate_series(1,4000) g`, user.ID); err != nil {
		t.Fatal("prepare scoped EXPLAIN rows:", err)
	}
	planRows, err := pool.Query(ctx, `EXPLAIN (ANALYZE,BUFFERS,FORMAT TEXT) SELECT id FROM app.tasks WHERE user_id=$1 AND deleted_at IS NULL AND status<>'DONE' AND due_date<$2::date ORDER BY created_at DESC,id DESC LIMIT 20`, user.ID, dbToday)
	if err != nil {
		t.Fatal("EXPLAIN due-date filtering:", err)
	}
	var plan []string
	for planRows.Next() {
		var line string
		if err := planRows.Scan(&line); err != nil {
			planRows.Close()
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	if err := planRows.Err(); err != nil {
		planRows.Close()
		t.Fatal(err)
	}
	planRows.Close()
	if len(plan) == 0 {
		t.Fatal("EXPLAIN produced no plan")
	}
	t.Logf("Representative due-date EXPLAIN ANALYZE (4,000 scoped tasks):\n%s", strings.Join(plan, "\n"))
	if _, err := pool.Exec(ctx, `UPDATE app.users SET time_zone='America/Los_Angeles' WHERE id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	deviceZoneCursor := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks?limit=1&q=Phase16&tz=Asia%2FAmman&cursor="+url.QueryEscape(*firstPage.NextCursor), "")
	if deviceZoneCursor.Code != http.StatusOK {
		t.Fatalf("device timezone cursor was affected by account timezone: status=%d", deviceZoneCursor.Code)
	}
	wrongZoneCursor := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks?limit=1&q=Phase16&tz=America%2FLos_Angeles&cursor="+url.QueryEscape(*firstPage.NextCursor), "")
	if wrongZoneCursor.Code != http.StatusBadRequest {
		t.Fatalf("timezone-mismatched cursor status=%d", wrongZoneCursor.Code)
	}
	if result := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks?tz=Not%2FAZone", ""); result.Code != http.StatusBadRequest {
		t.Fatalf("invalid requested timezone status=%d", result.Code)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT due_date::text FROM app.tasks WHERE id=$1`, future.ID).Scan(&stored); err != nil || stored != tomorrow {
		t.Fatalf("timezone changed stored date=%s err=%v", stored, err)
	}
	for _, test := range []struct{ instant, want string }{{"2026-03-08 07:59:00+00", "2026-03-07"}, {"2026-03-08 08:00:00+00", "2026-03-08"}, {"2026-11-01 08:30:00+00", "2026-11-01"}, {"2026-11-01 09:30:00+00", "2026-11-01"}} {
		var local string
		err := pool.QueryRow(ctx, `SELECT to_char(($1::timestamptz AT TIME ZONE 'America/Los_Angeles')::date,'YYYY-MM-DD')`, test.instant).Scan(&local)
		if err != nil || local != test.want {
			t.Errorf("DST local date at %s=%s err=%v want=%s", test.instant, local, err, test.want)
		}
	}
}

func httpBody(title string, version int64) string {
	return fmt.Sprintf(`{"title":%q,"description":"","status":"TODO","version":%d}`, title, version)
}
func httpBodyWithDue(title string, version int64, due string) string {
	return fmt.Sprintf(`{"title":%q,"description":"","status":"TODO","version":%d,"due_date":%s}`, title, version, due)
}
