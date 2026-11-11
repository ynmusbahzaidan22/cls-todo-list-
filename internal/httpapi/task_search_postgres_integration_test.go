package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/health"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/testdatabase"
)

func TestTaskSearchAndFilteringPostgreSQL(t *testing.T) {
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
		t.Fatalf("generate isolated test account suffix: %v", err)
	}
	baseEmail := "phase14-search-" + hex.EncodeToString(suffix[:])
	emailA, emailB := baseEmail+"-a@example.test", baseEmail+"-b@example.test"
	userAIP, userBIP := baseEmail+"-user-a", baseEmail+"-user-b"
	loginAIP, loginBIP := baseEmail+"-login-a", baseEmail+"-login-b"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM app.users WHERE email = ANY($1::text[])`, []string{emailA, emailB}); cleanupErr != nil {
			t.Errorf("clean up only the Phase 14 test accounts: %v", cleanupErr)
		}
	})
	registerRateLimitTestCleanup(t, pool, []string{userAIP, userBIP, loginAIP, loginBIP}, []string{emailA, emailB})

	manager, err := auth.NewManager(repository.NewAuthRepository(pool))
	if err != nil {
		t.Fatalf("initialize authentication manager: %v", err)
	}
	password := "phase fourteen search integration password"
	userA, err := manager.Register(ctx, "Search User A", emailA, password, userAIP)
	if err != nil {
		t.Fatalf("register account A: %v", err)
	}
	userB, err := manager.Register(ctx, "Search User B", emailB, password, userBIP)
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
	service := task.NewService(repository.NewTaskRepository(pool), []byte(strings.Repeat("s", 32)))
	server := httptest.NewServer(NewRouter(health.NewService(pool), testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(manager, "todo_session", false), Tasks: NewTaskHandlers(service), TrustedOrigin: "http://127.0.0.1:8080",
	}))
	defer server.Close()

	created := map[string]taskWire{}
	for _, input := range []struct{ key, title, description, status string }{
		{"title", "Needle lantern", "warm light", task.StatusTODO},
		{"description", "A quiet note", "Needle inside the description", task.StatusInProgress},
		{"case", "NEEDLE case insensitive", "", task.StatusTODO},
		{"arabic", "مهمة عربية", "تفاصيل للبحث بالعربية", task.StatusDone},
		{"percent", "100% ready", "literal percent", task.StatusDone},
		{"underscore", "under_score task", "literal underscore", task.StatusTODO},
		{"backslash", `path\folder task`, "literal backslash", task.StatusInProgress},
		{"deleted", "Needle deleted task", "must stay out of active search", task.StatusTODO},
	} {
		body, _ := json.Marshal(map[string]string{"title": input.title, "description": input.description, "status": input.status})
		response := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks", string(body))
		if response.Code != http.StatusCreated {
			t.Fatalf("create %s task status=%d body=%s", input.key, response.Code, response.Body.String())
		}
		created[input.key] = decodeTaskResponse(t, response)
	}
	userBTaskResponse := sendTaskRequest(t, server, cookieB, http.MethodPost, "/api/tasks", `{"title":"Needle private","description":"account B only","status":"TODO"}`)
	if userBTaskResponse.Code != http.StatusCreated {
		t.Fatalf("create account B task status=%d body=%s", userBTaskResponse.Code, userBTaskResponse.Body.String())
	}
	userBTask := decodeTaskResponse(t, userBTaskResponse)
	deleted := sendTaskRequest(t, server, cookieA, http.MethodDelete, "/api/tasks/"+created["deleted"].ID, `{"version":1}`)
	if deleted.Code != http.StatusOK {
		t.Fatalf("soft delete task status=%d body=%s", deleted.Code, deleted.Body.String())
	}

	// Equal timestamps make the UUID tie-breaker deterministic across pages.
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET created_at = '2026-10-01T12:00:00Z' WHERE user_id = $1 AND id = ANY($2::uuid[])`, userA.ID, []string{created["title"].ID, created["description"].ID, created["case"].ID}); err != nil {
		t.Fatalf("set tied timestamps for stable pagination: %v", err)
	}

	assertSearch := func(name, query string, wantIDs ...string) []taskWire {
		t.Helper()
		values := url.Values{"q": []string{query}}
		response := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?"+values.Encode(), "")
		if response.Code != http.StatusOK {
			t.Fatalf("%s search status=%d body=%s", name, response.Code, response.Body.String())
		}
		page := decodeTaskListResponse(t, response)
		if len(page.Items) != len(wantIDs) {
			t.Fatalf("%s search returned %d tasks, want %d: %+v", name, len(page.Items), len(wantIDs), page.Items)
		}
		got := make(map[string]bool, len(page.Items))
		for _, item := range page.Items {
			got[item.ID] = true
		}
		for _, id := range wantIDs {
			if !got[id] {
				t.Fatalf("%s search did not include %s: %+v", name, id, page.Items)
			}
		}
		return page.Items
	}

	assertSearch("title, description, partial, and case-insensitive", "needle", created["title"].ID, created["description"].ID, created["case"].ID)
	assertSearch("Arabic Unicode", "بحث بالعربية", created["arabic"].ID)
	assertSearch("literal percent", "%", created["percent"].ID)
	assertSearch("literal underscore", "_", created["underscore"].ID)
	assertSearch("literal backslash", `\`, created["backslash"].ID)

	for status, expected := range map[string][]string{
		task.StatusTODO:       {created["title"].ID, created["case"].ID, created["underscore"].ID},
		task.StatusInProgress: {created["description"].ID, created["backslash"].ID},
		task.StatusDone:       {created["arabic"].ID, created["percent"].ID},
	} {
		t.Run("status "+status, func(t *testing.T) {
			response := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?status="+url.QueryEscape(status), "")
			if response.Code != http.StatusOK {
				t.Fatalf("status filter %s returned %d: %s", status, response.Code, response.Body.String())
			}
			page := decodeTaskListResponse(t, response)
			got := make(map[string]bool, len(page.Items))
			for _, item := range page.Items {
				got[item.ID] = true
				if item.Status != status || item.DeletedAt != nil {
					t.Fatalf("status filter returned wrong task: %+v", item)
				}
			}
			if len(page.Items) != len(expected) {
				t.Fatalf("status filter %s returned %d tasks, want %d", status, len(page.Items), len(expected))
			}
			for _, id := range expected {
				if !got[id] {
					t.Fatalf("status filter %s omitted task %s", status, id)
				}
			}
		})
	}

	combined := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?q=needle&status=IN_PROGRESS", "")
	combinedPage := decodeTaskListResponse(t, combined)
	if combined.Code != http.StatusOK || len(combinedPage.Items) != 1 || combinedPage.Items[0].ID != created["description"].ID {
		t.Fatalf("combined search/status returned %d %s: %+v", combined.Code, combined.Body.String(), combinedPage.Items)
	}

	for _, badQuery := range []string{
		"q=TODO&q=DONE", "status=TODO&status=DONE", "status=INVALID",
		"q=" + url.QueryEscape(strings.Repeat("界", 201)),
	} {
		response := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?"+badQuery, "")
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid query %q status=%d want=400 body=%s", badQuery, response.Code, response.Body.String())
		}
	}
	trimmed := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?q="+url.QueryEscape("   "), "")
	if trimmed.Code != http.StatusOK || len(decodeTaskListResponse(t, trimmed).Items) != 7 {
		t.Errorf("whitespace-only search should behave as no query: status=%d body=%s", trimmed.Code, trimmed.Body.String())
	}
	spoofed := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?user_id="+url.QueryEscape(userB.ID)+"&q=Needle", "")
	for _, item := range decodeTaskListResponse(t, spoofed).Items {
		if spoofed.Code != http.StatusOK || item.ID == userBTask.ID || item.ID == created["deleted"].ID {
			t.Fatalf("forged owner/deleted task appeared in account A results: status=%d item=%+v", spoofed.Code, item)
		}
	}

	first := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?q=needle&status=TODO&limit=1", "")
	firstPage := decodeTaskListResponse(t, first)
	if first.Code != http.StatusOK || len(firstPage.Items) != 1 || firstPage.NextCursor == nil {
		t.Fatalf("filtered page one invalid: status=%d body=%s", first.Code, first.Body.String())
	}
	second := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?q=needle&status=TODO&limit=1&cursor="+url.QueryEscape(*firstPage.NextCursor), "")
	secondPage := decodeTaskListResponse(t, second)
	if second.Code != http.StatusOK || len(secondPage.Items) != 1 || secondPage.Items[0].Status != task.StatusTODO || secondPage.Items[0].ID == firstPage.Items[0].ID {
		t.Fatalf("filtered page two lost filters or repeated a row: status=%d page=%+v body=%s", second.Code, secondPage, second.Body.String())
	}
	for _, changedFilter := range []string{"q=other&status=TODO", "q=needle&status=DONE"} {
		response := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?"+changedFilter+"&limit=1&cursor="+url.QueryEscape(*firstPage.NextCursor), "")
		if response.Code != http.StatusBadRequest {
			t.Errorf("filter-mismatched cursor query %q status=%d want=400", changedFilter, response.Code)
		}
	}
	tamperedCursor := *firstPage.NextCursor
	last := tamperedCursor[len(tamperedCursor)-1]
	if last == 'A' {
		tamperedCursor = tamperedCursor[:len(tamperedCursor)-1] + "B"
	} else {
		tamperedCursor = tamperedCursor[:len(tamperedCursor)-1] + "A"
	}
	tampered := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?q=needle&status=TODO&limit=1&cursor="+url.QueryEscape(tamperedCursor), "")
	if tampered.Code != http.StatusBadRequest {
		t.Errorf("tampered cursor status=%d want=400", tampered.Code)
	}

	foreignSearch := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?q=private", "")
	if foreignSearch.Code != http.StatusOK || len(decodeTaskListResponse(t, foreignSearch).Items) != 0 {
		t.Fatalf("account A search exposed account B task: status=%d body=%s", foreignSearch.Code, foreignSearch.Body.String())
	}

	priorityIDs := make(map[string][]string)
	for _, priority := range []string{task.PriorityLow, task.PriorityMedium, task.PriorityHigh, task.PriorityUrgent} {
		count := 1
		if priority == task.PriorityUrgent {
			count = 2
		}
		for index := 0; index < count; index++ {
			body, _ := json.Marshal(map[string]string{
				"title":  "Phase15 priority marker " + priority + " " + string(rune('A'+index)),
				"status": task.StatusTODO, "priority": priority,
			})
			response := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks", string(body))
			if response.Code != http.StatusCreated {
				t.Fatalf("create priority %s task status=%d body=%s", priority, response.Code, response.Body.String())
			}
			createdTask := decodeTaskResponse(t, response)
			if createdTask.Priority != priority {
				t.Fatalf("created priority=%q want=%q", createdTask.Priority, priority)
			}
			priorityIDs[priority] = append(priorityIDs[priority], createdTask.ID)
		}
	}
	defaultTaskResponse := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks", `{"title":"Phase15 default marker"}`)
	if defaultTaskResponse.Code != http.StatusCreated || decodeTaskResponse(t, defaultTaskResponse).Priority != task.PriorityMedium {
		t.Fatalf("omitted priority should default to MEDIUM: status=%d body=%s", defaultTaskResponse.Code, defaultTaskResponse.Body.String())
	}
	invalidPriority := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks", `{"title":"Invalid priority","priority":"ASAP"}`)
	if invalidPriority.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid priority status=%d want=422 body=%s", invalidPriority.Code, invalidPriority.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET priority='ASAP' WHERE id=$1 AND user_id=$2`, created["title"].ID, userA.ID); err == nil {
		t.Fatal("database accepted unsupported task priority")
	}
	for _, priority := range []string{task.PriorityLow, task.PriorityMedium, task.PriorityHigh, task.PriorityUrgent} {
		response := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?q=Phase15%20priority%20marker&status=TODO&priority="+priority, "")
		page := decodeTaskListResponse(t, response)
		if response.Code != http.StatusOK || len(page.Items) != len(priorityIDs[priority]) {
			t.Fatalf("combined priority filter %s status=%d got=%d want=%d body=%s", priority, response.Code, len(page.Items), len(priorityIDs[priority]), response.Body.String())
		}
		for _, item := range page.Items {
			if item.Priority != priority || item.Status != task.StatusTODO {
				t.Fatalf("combined filter returned mismatched task: %+v", item)
			}
		}
	}
	priorityFirst := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?q=Phase15%20priority%20marker&priority=URGENT&limit=1", "")
	priorityFirstPage := decodeTaskListResponse(t, priorityFirst)
	if priorityFirst.Code != http.StatusOK || len(priorityFirstPage.Items) != 1 || priorityFirstPage.NextCursor == nil {
		t.Fatalf("priority cursor first page invalid: %d %s", priorityFirst.Code, priorityFirst.Body.String())
	}
	prioritySecond := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?q=Phase15%20priority%20marker&priority=URGENT&limit=1&cursor="+url.QueryEscape(*priorityFirstPage.NextCursor), "")
	prioritySecondPage := decodeTaskListResponse(t, prioritySecond)
	if prioritySecond.Code != http.StatusOK || len(prioritySecondPage.Items) != 1 || prioritySecondPage.Items[0].ID == priorityFirstPage.Items[0].ID {
		t.Fatalf("priority cursor second page invalid: %d %+v", prioritySecond.Code, prioritySecondPage)
	}
	priorityMismatch := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks?q=Phase15%20priority%20marker&priority=HIGH&limit=1&cursor="+url.QueryEscape(*priorityFirstPage.NextCursor), "")
	if priorityMismatch.Code != http.StatusBadRequest {
		t.Fatalf("priority-mismatched cursor status=%d want=400", priorityMismatch.Code)
	}
	legacyUpdate := sendTaskRequest(t, server, cookieA, http.MethodPut, "/api/tasks/"+priorityIDs[task.PriorityHigh][0], `{"title":"Legacy update","description":"","status":"TODO","version":1}`)
	if legacyUpdate.Code != http.StatusOK || decodeTaskResponse(t, legacyUpdate).Priority != task.PriorityHigh {
		t.Fatalf("legacy update should preserve priority: status=%d body=%s", legacyUpdate.Code, legacyUpdate.Body.String())
	}
}

func decodeTaskListResponse(t *testing.T, response *httptest.ResponseRecorder) taskListWire {
	t.Helper()
	var page taskListWire
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode task list: %v; body=%s", err, response.Body.String())
	}
	return page
}
