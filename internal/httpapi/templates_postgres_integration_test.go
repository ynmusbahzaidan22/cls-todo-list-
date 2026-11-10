package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/health"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

func TestTaskTemplatesPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL integration test skipped")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		t.Fatal("TEST_DATABASE_URL must target loopback PostgreSQL")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test database: %v", err)
	}
	var suffix [10]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	marker := "phase10-template-" + hex.EncodeToString(suffix[:])
	emails := []string{marker + "-a@example.test", marker + "-b@example.test"}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 8*time.Second)
		defer done()
		if _, err := pool.Exec(cleanup, `DELETE FROM app.users WHERE email = ANY($1::text[])`, emails); err != nil {
			t.Errorf("remove isolated template test users: %v", err)
		}
	})
	manager, err := auth.NewManager(repository.NewAuthRepository(pool))
	if err != nil {
		t.Fatal(err)
	}
	password := "Phase ten integration password!"
	userA, err := manager.Register(ctx, emails[0], password, marker+"-a")
	if err != nil {
		t.Fatalf("register owner: %v", err)
	}
	userB, err := manager.Register(ctx, emails[1], password, marker+"-b")
	if err != nil {
		t.Fatalf("register second owner: %v", err)
	}
	_, tokenA, _, err := manager.Login(ctx, emails[0], password, marker+"-login-a")
	if err != nil {
		t.Fatal(err)
	}
	_, tokenB, _, err := manager.Login(ctx, emails[1], password, marker+"-login-b")
	if err != nil {
		t.Fatal(err)
	}
	cookieA, cookieB := &http.Cookie{Name: "todo_session", Value: tokenA}, &http.Cookie{Name: "todo_session", Value: tokenB}
	service := task.NewService(repository.NewTaskRepository(pool))
	templates := task.NewTemplateService(repository.NewTemplateRepository(pool))
	server := httptest.NewServer(NewRouter(health.NewService(pool), testLogger(), t.TempDir(), RouterOptions{Auth: NewAuthHandlers(manager, "todo_session", false), Tasks: NewTaskHandlers(service), Templates: NewTemplateHandlers(templates), TrustedOrigin: "http://127.0.0.1:8080"}))
	defer server.Close()
	request := func(cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
		return sendTaskRequest(t, server, cookie, method, path, body)
	}

	unauthenticated := request(nil, http.MethodGet, "/api/task-templates", "")
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated template list status=%d", unauthenticated.Code)
	}
	created := request(cookieA, http.MethodPost, "/api/task-templates", fmt.Sprintf(`{"name":%q,"description":"prep","items":[{"title":%q,"description":"first","priority":"URGENT"},{"title":%q}]}`, marker, marker+" item one", marker+" item two"))
	if created.Code != http.StatusCreated {
		t.Fatalf("create template status=%d body=%s", created.Code, created.Body.String())
	}
	var template task.Template
	if err := json.Unmarshal(created.Body.Bytes(), &template); err != nil {
		t.Fatal(err)
	}
	if len(template.Items) != 2 || template.Items[0].Priority != task.PriorityUrgent || template.Items[1].Priority != task.PriorityMedium {
		t.Fatalf("template item priorities were not stored/defaulted: %+v", template.Items)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.task_template_items SET priority='ASAP' WHERE template_id=$1 AND user_id=$2 AND position=0`, template.ID, userA.ID); err == nil {
		t.Fatal("database accepted unsupported template-item priority")
	}
	path := "/api/task-templates/" + template.ID
	for _, invalidBody := range []string{`{"name":"empty","items":[]}`, `{"name":"duplicate","items":[{"title":"same"},{"title":" SAME "}]}`, fmt.Sprintf(`{"name":"too many","items":%s}`, repeatedItemsJSON(task.MaxTemplateItems+1))} {
		result := request(cookieA, http.MethodPost, "/api/task-templates", invalidBody)
		if result.Code != http.StatusUnprocessableEntity {
			t.Errorf("invalid template status=%d body=%s", result.Code, result.Body.String())
		}
	}
	if got := request(cookieB, http.MethodGet, path, "").Code; got != http.StatusNotFound {
		t.Fatalf("foreign template detail status=%d", got)
	}
	if got := request(cookieB, http.MethodPut, path, fmt.Sprintf(`{"name":"forged","version":1,"items":[{"title":"x"}]}`)).Code; got != http.StatusNotFound {
		t.Fatalf("foreign template update status=%d", got)
	}
	if got := request(cookieB, http.MethodDelete, path, `{"version":1}`).Code; got != http.StatusNotFound {
		t.Fatalf("foreign template delete status=%d", got)
	}
	if got := request(cookieB, http.MethodPost, path+"/apply", `{"version":1}`).Code; got != http.StatusNotFound {
		t.Fatalf("foreign template apply status=%d", got)
	}
	list := request(cookieA, http.MethodGet, "/api/task-templates", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), template.ID) {
		t.Fatalf("owner template listing status=%d body=%s", list.Code, list.Body.String())
	}
	if got := request(cookieB, http.MethodGet, "/api/task-templates", "").Body.String(); strings.Contains(got, template.ID) {
		t.Fatalf("foreign template leaked in listing: %s", got)
	}

	// Concurrent incremental applies serialize: one creates the items and the other skips them.
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	bodies := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, server.URL+path+"/apply", strings.NewReader(`{"version":1}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://127.0.0.1:8080")
			req.AddCookie(cookieA)
			res, e := server.Client().Do(req)
			if e != nil {
				statuses <- 0
				bodies <- e.Error()
				return
			}
			defer res.Body.Close()
			statuses <- res.StatusCode
			data, _ := io.ReadAll(res.Body)
			bodies <- string(data)
		}()
	}
	wg.Wait()
	close(statuses)
	close(bodies)
	statusCounts := map[int]int{}
	for status := range statuses {
		statusCounts[status]++
	}
	if statusCounts[http.StatusCreated] != 1 || statusCounts[http.StatusOK] != 1 {
		t.Errorf("concurrent incremental apply statuses=%v, want one 201 and one 200", statusCounts)
	}
	responseCounts := []int{}
	for body := range bodies {
		var result applyTemplateResponse
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			t.Errorf("decode apply response: %v", err)
			continue
		}
		responseCounts = append(responseCounts, len(result.Items))
	}
	if len(responseCounts) != 2 || !((responseCounts[0] == 2 && responseCounts[1] == 0) || (responseCounts[0] == 0 && responseCounts[1] == 2)) {
		t.Errorf("concurrent incremental apply result sizes=%v, want one 2 and one 0", responseCounts)
	}
	var taskCount, eventCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.tasks WHERE user_id=$1 AND title LIKE $2`, userA.ID, marker+" item%").Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.task_activity a JOIN app.tasks t ON t.id=a.task_id WHERE t.user_id=$1 AND t.title LIKE $2 AND a.event_type='TASK_CREATED'`, userA.ID, marker+" item%").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 2 || eventCount != 2 {
		t.Fatalf("concurrent incremental apply created tasks/events %d/%d, want 2/2", taskCount, eventCount)
	}
	var urgentApplied, mediumApplied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE priority='URGENT'), count(*) FILTER (WHERE priority='MEDIUM') FROM app.tasks WHERE user_id=$1 AND title LIKE $2`, userA.ID, marker+" item%").Scan(&urgentApplied, &mediumApplied); err != nil {
		t.Fatal(err)
	}
	if urgentApplied != 1 || mediumApplied != 1 {
		t.Fatalf("template application priorities urgent/medium=%d/%d, want 1/1", urgentApplied, mediumApplied)
	}
	repeatApply := request(cookieA, http.MethodPost, path+"/apply", `{"version":1}`)
	if repeatApply.Code != http.StatusOK || !strings.Contains(repeatApply.Body.String(), `"items":[]`) {
		t.Fatalf("repeat incremental apply status=%d body=%s; duplicate items must be skipped", repeatApply.Code, repeatApply.Body.String())
	}

	// Editing advances the version and never mutates already generated tasks. A stale write conflicts.
	update := request(cookieA, http.MethodPut, path, fmt.Sprintf(`{"name":%q,"description":"edited","version":1,"items":[{"id":%q,"title":%q},{"title":%q}]}`, marker+" edited", template.Items[0].ID, marker+" item one", marker+" new item"))
	if update.Code != http.StatusOK {
		t.Fatalf("update template status=%d body=%s", update.Code, update.Body.String())
	}
	if stale := request(cookieA, http.MethodPut, path, fmt.Sprintf(`{"name":"stale","version":1,"items":[{"title":"bad"}]}`)); stale.Code != http.StatusConflict {
		t.Fatalf("stale update status=%d body=%s", stale.Code, stale.Body.String())
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.tasks WHERE user_id=$1 AND title LIKE $2`, userA.ID, marker+" item%").Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 2 {
		t.Fatalf("template edit modified generated task count: %d", taskCount)
	}
	added := request(cookieA, http.MethodPost, path+"/apply", `{"version":2}`)
	if added.Code != http.StatusCreated || !strings.Contains(added.Body.String(), marker+" new item") || strings.Contains(added.Body.String(), marker+" item one") {
		t.Fatalf("incremental apply did not add only the new template item: %d %s", added.Code, added.Body.String())
	}
	newSet := request(cookieA, http.MethodPost, path+"/apply", `{"version":2,"mode":"new_set"}`)
	if newSet.Code != http.StatusCreated || strings.Count(newSet.Body.String(), `"title"`) < 2 {
		t.Fatalf("new-set apply did not include the full template: %d %s", newSet.Code, newSet.Body.String())
	}
	updated := request(cookieA, http.MethodGet, path, "")
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), "edited") {
		t.Fatalf("get updated template status=%d body=%s", updated.Code, updated.Body.String())
	}

	// Two edits based on the same version cannot overwrite each other.
	var editWG sync.WaitGroup
	editStatuses := make(chan int, 2)
	for _, name := range []string{marker + " concurrent edit a", marker + " concurrent edit b"} {
		editWG.Add(1)
		go func(name string) {
			defer editWG.Done()
			body := fmt.Sprintf(`{"name":%q,"version":2,"items":[{"title":%q}]}`, name, name+" task")
			req, _ := http.NewRequest(http.MethodPut, server.URL+path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://127.0.0.1:8080")
			req.AddCookie(cookieA)
			res, requestErr := server.Client().Do(req)
			if requestErr != nil {
				editStatuses <- 0
				return
			}
			defer res.Body.Close()
			editStatuses <- res.StatusCode
		}(name)
	}
	editWG.Wait()
	close(editStatuses)
	twins := map[int]int{}
	for status := range editStatuses {
		twins[status]++
	}
	if twins[http.StatusOK] != 1 || twins[http.StatusConflict] != 1 {
		t.Fatalf("concurrent template edits returned %#v, want one 200 and one 409", twins)
	}

	// Deletion racing against itself is one successful delete and one consistent not-found.
	var deleteWG sync.WaitGroup
	deleteStatuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		deleteWG.Add(1)
		go func() {
			defer deleteWG.Done()
			req, _ := http.NewRequest(http.MethodDelete, server.URL+path, strings.NewReader(`{"version":3}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://127.0.0.1:8080")
			req.AddCookie(cookieA)
			res, requestErr := server.Client().Do(req)
			if requestErr != nil {
				deleteStatuses <- 0
				return
			}
			defer res.Body.Close()
			deleteStatuses <- res.StatusCode
		}()
	}
	deleteWG.Wait()
	close(deleteStatuses)
	deleteResults := map[int]int{}
	for status := range deleteStatuses {
		deleteResults[status]++
	}
	if deleteResults[http.StatusNoContent] != 1 || deleteResults[http.StatusNotFound] != 1 {
		t.Fatalf("concurrent template deletes returned %#v, want one 204 and one 404", deleteResults)
	}
	if deleted := request(cookieA, http.MethodGet, path, ""); deleted.Code != http.StatusNotFound {
		t.Fatalf("deleted template remained readable: status=%d", deleted.Code)
	}
	if deleted := request(cookieA, http.MethodPost, path+"/apply", `{"version":3}`); deleted.Code != http.StatusNotFound {
		t.Fatalf("deleted template remained applicable: status=%d", deleted.Code)
	}
	if retained := request(cookieA, http.MethodGet, "/api/tasks?limit=100", ""); retained.Code != http.StatusOK || !strings.Contains(retained.Body.String(), marker+" item one") {
		t.Fatalf("generated task did not survive template deletion: %d %s", retained.Code, retained.Body.String())
	}

	// Any failure in a generated task or its activity event must roll back the complete apply.
	for _, failOn := range []string{marker + " task fail", marker + " activity fail"} {
		failureTemplate := request(cookieA, http.MethodPost, "/api/task-templates", fmt.Sprintf(`{"name":%q,"items":[{"title":%q},{"title":%q}]}`, marker+" failure", failOn, marker+" companion"))
		if failureTemplate.Code != http.StatusCreated {
			t.Fatalf("create failure template: %d %s", failureTemplate.Code, failureTemplate.Body.String())
		}
		var candidate task.Template
		if err := json.Unmarshal(failureTemplate.Body.Bytes(), &candidate); err != nil {
			t.Fatal(err)
		}
		triggerSuffix := strings.ReplaceAll(strings.ToLower(candidate.ID), "-", "")
		trigger, function := "phase10_fail_task_"+triggerSuffix, "phase10_fail_activity_"+triggerSuffix
		if strings.Contains(failOn, "task fail") {
			_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION app.%s() RETURNS trigger LANGUAGE plpgsql AS $body$ BEGIN IF NEW.title = %q THEN RAISE EXCEPTION 'injected template task failure'; END IF; RETURN NEW; END $body$`, function, failOn))
			if err == nil {
				_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON app.tasks FOR EACH ROW EXECUTE FUNCTION app.%s()`, trigger, function))
			}
		} else {
			_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION app.%s() RETURNS trigger LANGUAGE plpgsql AS $body$ BEGIN IF NEW.event_type = 'TASK_CREATED' AND NEW.new_value->>'title' = %q THEN RAISE EXCEPTION 'injected template activity failure'; END IF; RETURN NEW; END $body$`, function, failOn))
			if err == nil {
				_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON app.task_activity FOR EACH ROW EXECUTE FUNCTION app.%s()`, trigger, function))
			}
		}
		if err != nil {
			t.Fatalf("create scoped rollback trigger: %v", err)
		}
		result := request(cookieA, http.MethodPost, "/api/task-templates/"+candidate.ID+"/apply", `{"version":1}`)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, dropErr := pool.Exec(cleanupCtx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON app.%s`, trigger, map[bool]string{true: "tasks", false: "task_activity"}[strings.Contains(failOn, "task fail")]))
		if dropErr == nil {
			_, dropErr = pool.Exec(cleanupCtx, fmt.Sprintf(`DROP FUNCTION IF EXISTS app.%s()`, function))
		}
		cleanupCancel()
		if dropErr != nil {
			t.Fatalf("remove scoped rollback trigger: %v", dropErr)
		}
		if result.Code == http.StatusCreated {
			t.Fatalf("expected injected apply failure for %q", failOn)
		}
		var partial int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.tasks WHERE user_id=$1 AND title LIKE $2`, userA.ID, marker+" companion").Scan(&partial); err != nil {
			t.Fatal(err)
		}
		if partial != 0 {
			t.Fatalf("failed application left %d companion tasks", partial)
		}
	}

	_ = userB
}

func repeatedItemsJSON(count int) string {
	items := make([]string, count)
	for i := range items {
		items[i] = fmt.Sprintf(`{"title":"item %d"}`, i)
	}
	return "[" + strings.Join(items, ",") + "]"
}
