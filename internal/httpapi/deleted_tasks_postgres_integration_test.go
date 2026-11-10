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
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/health"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

func TestDeletedTaskListingPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL integration test skipped")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		t.Fatal("TEST_DATABASE_URL must target a loopback PostgreSQL server")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("connect test PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test PostgreSQL: %v", err)
	}

	var suffix [10]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "deleted-list-" + hex.EncodeToString(suffix[:])
	emailA, emailB := prefix+"-a@example.test", prefix+"-b@example.test"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM app.users WHERE email = ANY($1::text[])`, []string{emailA, emailB}); cleanupErr != nil {
			t.Errorf("clean up only the deleted-list integration accounts: %v", cleanupErr)
		}
	})

	manager, err := auth.NewManager(repository.NewAuthRepository(pool))
	if err != nil {
		t.Fatal(err)
	}
	password := "deleted-list integration password"
	userA, err := manager.Register(ctx, emailA, password, prefix+"-ip-a")
	if err != nil {
		t.Fatalf("register user A: %v", err)
	}
	if _, err := manager.Register(ctx, emailB, password, prefix+"-ip-b"); err != nil {
		t.Fatalf("register user B: %v", err)
	}
	_, tokenA, _, err := manager.Login(ctx, emailA, password, prefix+"-login-a")
	if err != nil {
		t.Fatal(err)
	}
	_, tokenB, _, err := manager.Login(ctx, emailB, password, prefix+"-login-b")
	if err != nil {
		t.Fatal(err)
	}
	cookieA := &http.Cookie{Name: "todo_session", Value: tokenA}
	cookieB := &http.Cookie{Name: "todo_session", Value: tokenB}
	service := task.NewService(repository.NewTaskRepository(pool))
	server := httptest.NewServer(NewRouter(health.NewService(pool), testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(manager, "todo_session", false), Tasks: NewTaskHandlers(service), TrustedOrigin: "http://127.0.0.1:8080",
	}))
	defer server.Close()

	createDeleted := func(title string) task.Task {
		t.Helper()
		created, createErr := service.Create(ctx, userA.ID, title, "private description", task.StatusTODO)
		if createErr != nil {
			t.Fatalf("create test task: %v", createErr)
		}
		deleted, deleteErr := service.Delete(ctx, userA.ID, created.ID, created.Version)
		if deleteErr != nil {
			t.Fatalf("delete test task: %v", deleteErr)
		}
		return deleted
	}

	unauthenticated := sendTaskRequest(t, server, nil, http.MethodGet, "/api/tasks/deleted", "")
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated listing status=%d, want 401", unauthenticated.Code)
	}
	failedTaskPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("create task-pool failure fixture: %v", err)
	}
	failedTaskService := task.NewService(repository.NewTaskRepository(failedTaskPool))
	failedTaskPool.Close()
	failedStorageServer := httptest.NewServer(NewRouter(health.NewService(pool), testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(manager, "todo_session", false), Tasks: NewTaskHandlers(failedTaskService), TrustedOrigin: "http://127.0.0.1:8080",
	}))
	failedStorageResponse := sendTaskRequest(t, failedStorageServer, cookieA, http.MethodGet, "/api/tasks/deleted", "")
	failedStorageServer.Close()
	if failedStorageResponse.Code != http.StatusInternalServerError || strings.Contains(failedStorageResponse.Body.String(), "pool is closed") {
		t.Fatalf("storage failure response=%d %s; want generic 500 without storage details", failedStorageResponse.Code, failedStorageResponse.Body.String())
	}

	// All rows share a deletion timestamp so page boundaries exercise the ID tie-breaker.
	wantIDs := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		deleted := createDeleted(fmt.Sprintf("same-time-%d", index))
		wantIDs = append(wantIDs, deleted.ID)
	}
	if _, err := pool.Exec(ctx, `WITH stamp AS (SELECT clock_timestamp() AS value) UPDATE app.tasks SET deleted_at = stamp.value FROM stamp WHERE user_id = $1`, userA.ID); err != nil {
		t.Fatalf("set equal deletion timestamps for isolated test account: %v", err)
	}
	sortIDsDescending(wantIDs)
	var gotIDs []string
	cursor := ""
	for {
		path := "/api/tasks/deleted?limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		response := sendTaskRequest(t, server, cookieA, http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("list deleted page status=%d body=%s", response.Code, response.Body.String())
		}
		var page taskListWire
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode deleted page: %v", err)
		}
		for _, item := range page.Items {
			if item.DeletedAt == nil || item.Description != "private description" {
				t.Fatalf("listed row missing deleted state/details: %+v", item)
			}
			gotIDs = append(gotIDs, item.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("keyset pages = %v, want stable deleted_at/ID ordering %v", gotIDs, wantIDs)
	}

	foreignCursor := sendTaskRequest(t, server, cookieB, http.MethodGet, "/api/tasks/deleted?limit=2&cursor="+url.QueryEscape(cursorFromDeletedList(t, server, cookieA)), "")
	if foreignCursor.Code != http.StatusBadRequest {
		t.Fatalf("cross-owner cursor status=%d, want 400", foreignCursor.Code)
	}
	userBPage := sendTaskRequest(t, server, cookieB, http.MethodGet, "/api/tasks/deleted", "")
	var userBWire taskListWire
	if err := json.Unmarshal(userBPage.Body.Bytes(), &userBWire); err != nil || userBPage.Code != http.StatusOK || len(userBWire.Items) != 0 {
		t.Fatalf("user B listing status=%d items=%d err=%v body=%s; want empty isolated list", userBPage.Code, len(userBWire.Items), err, userBPage.Body.String())
	}
	invalidCursor := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/deleted?cursor=not-valid", "")
	if invalidCursor.Code != http.StatusBadRequest {
		t.Fatalf("malformed cursor status=%d, want 400", invalidCursor.Code)
	}
	invalidLimit := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/deleted?limit=101", "")
	if invalidLimit.Code != http.StatusBadRequest {
		t.Fatalf("oversize limit status=%d, want 400", invalidLimit.Code)
	}

	// The same inclusive 24-hour boundary as restore is used by the listing.
	inside := createDeleted("inside-recovery-window")
	outside := createDeleted("outside-recovery-window")
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET deleted_at = statement_timestamp() - interval '24 hours' + interval '1 second' WHERE id = $1`, inside.ID); err != nil {
		t.Fatalf("prepare in-window boundary row: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET deleted_at = statement_timestamp() - interval '24 hours' - interval '1 second' WHERE id = $1`, outside.ID); err != nil {
		t.Fatalf("prepare expired boundary row: %v", err)
	}
	boundaryPage := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/deleted?limit=100", "")
	var boundaryWire taskListWire
	if err := json.Unmarshal(boundaryPage.Body.Bytes(), &boundaryWire); err != nil || boundaryPage.Code != http.StatusOK {
		t.Fatalf("read recovery boundary listing status=%d err=%v body=%s", boundaryPage.Code, err, boundaryPage.Body.String())
	}
	foundInside, foundOutside := false, false
	for _, item := range boundaryWire.Items {
		foundInside = foundInside || item.ID == inside.ID
		foundOutside = foundOutside || item.ID == outside.ID
	}
	if !foundInside || foundOutside {
		t.Fatalf("recovery boundary inclusion inside/outside=%v/%v, want true/false", foundInside, foundOutside)
	}
	restoreExpired := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks/"+outside.ID+"/restore", fmt.Sprintf(`{"version":%d}`, outside.Version))
	if restoreExpired.Code != http.StatusConflict {
		t.Fatalf("expired restore status=%d body=%s, want 409", restoreExpired.Code, restoreExpired.Body.String())
	}

	// Individual, bulk and Undo-driven restores all disappear from this durable list.
	var rowVersion int64
	if err := pool.QueryRow(ctx, `SELECT version FROM app.tasks WHERE id = $1 AND user_id = $2`, wantIDs[0], userA.ID).Scan(&rowVersion); err != nil {
		t.Fatal(err)
	}
	path := "/api/tasks/" + wantIDs[0] + "/restore"
	restored := sendTaskRequest(t, server, cookieA, http.MethodPost, path, fmt.Sprintf(`{"version":%d}`, rowVersion))
	if restored.Code != http.StatusOK {
		t.Fatalf("restore listed task status=%d body=%s", restored.Code, restored.Body.String())
	}
	bulkTask, err := service.Create(ctx, userA.ID, "bulk-deleted-row", "bulk description", task.StatusTODO)
	if err != nil {
		t.Fatalf("create active bulk test task: %v", err)
	}
	bulkResult := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks/bulk", fmt.Sprintf(`{"action":"delete","tasks":[{"id":%q,"version":%d}]}`, bulkTask.ID, bulkTask.Version))
	if bulkResult.Code != http.StatusOK {
		t.Fatalf("bulk delete status=%d body=%s", bulkResult.Code, bulkResult.Body.String())
	}
	undoTask := createDeleted("undo-deleted-row")
	activityPage, err := service.Activity(ctx, userA.ID, undoTask.ID, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	var deleteEvent string
	for _, event := range activityPage.Items {
		if event.EventType == "TASK_DELETED" {
			deleteEvent = event.ID
			break
		}
	}
	if deleteEvent == "" {
		t.Fatal("could not find deletion event to exercise Undo list reconciliation")
	}
	var currentVersion int64
	if err := pool.QueryRow(ctx, `SELECT version FROM app.tasks WHERE id = $1 AND user_id = $2`, undoTask.ID, userA.ID).Scan(&currentVersion); err != nil {
		t.Fatal(err)
	}
	undo := sendTaskRequest(t, server, cookieA, http.MethodPost, "/api/tasks/"+undoTask.ID+"/activity/"+deleteEvent+"/undo", fmt.Sprintf(`{"version":%d}`, currentVersion))
	if undo.Code != http.StatusOK {
		t.Fatalf("Undo deletion status=%d body=%s", undo.Code, undo.Body.String())
	}
	finalA := sendTaskRequest(t, server, cookieA, http.MethodGet, "/api/tasks/deleted?limit=100", "")
	var finalAWire taskListWire
	if err := json.Unmarshal(finalA.Body.Bytes(), &finalAWire); err != nil || finalA.Code != http.StatusOK {
		t.Fatalf("read final deleted listing status=%d err=%v body=%s", finalA.Code, err, finalA.Body.String())
	}
	for _, item := range finalAWire.Items {
		if item.ID == wantIDs[0] || item.ID == undoTask.ID {
			t.Fatalf("restored task %s remained in deleted listing", item.ID)
		}
	}
	foundBulk := false
	for _, item := range finalAWire.Items {
		foundBulk = foundBulk || item.ID == bulkTask.ID
	}
	if !foundBulk {
		t.Fatal("bulk-deleted task was not present in persistent listing")
	}
}

func cursorFromDeletedList(t *testing.T, server *httptest.Server, cookie *http.Cookie) string {
	t.Helper()
	response := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks/deleted?limit=1", "")
	if response.Code != http.StatusOK {
		t.Fatalf("read first deleted page for cursor: status=%d body=%s", response.Code, response.Body.String())
	}
	var page taskListWire
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || page.NextCursor == nil {
		t.Fatalf("expected a next deleted cursor, err=%v body=%s", err, response.Body.String())
	}
	return *page.NextCursor
}

func sortIDsDescending(ids []string) {
	for left := 0; left < len(ids); left++ {
		for right := left + 1; right < len(ids); right++ {
			if ids[right] > ids[left] {
				ids[left], ids[right] = ids[right], ids[left]
			}
		}
	}
}
