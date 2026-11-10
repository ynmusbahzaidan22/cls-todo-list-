package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/database"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/health"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

func TestTaskActivityCreationAndUndoAsRuntimePostgreSQL(t *testing.T) {
	runtimeURL := os.Getenv("RUNTIME_DATABASE_URL")
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if runtimeURL == "" || adminURL == "" {
		t.Skip("RUNTIME_DATABASE_URL and TEST_DATABASE_URL are required for runtime PostgreSQL verification")
	}
	for name, connectionString := range map[string]string{"RUNTIME_DATABASE_URL": runtimeURL, "TEST_DATABASE_URL": adminURL} {
		parsed, err := url.Parse(connectionString)
		if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
			t.Fatalf("%s must target loopback PostgreSQL", name)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtimePool, err := database.Open(ctx, runtimeURL, 3)
	if err != nil {
		t.Fatalf("open runtime application pool: %v", err)
	}
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		runtimePool.Close()
		t.Fatalf("open administrative cleanup pool: %v", err)
	}
	t.Cleanup(adminPool.Close)
	t.Cleanup(runtimePool.Close)
	parsedRuntimeURL, _ := url.Parse(runtimeURL)
	if err := database.VerifyRuntimeRole(ctx, runtimePool, parsedRuntimeURL.User.Username()); err != nil {
		t.Fatalf("application runtime role verification failed: %v", err)
	}

	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate runtime application fixture identity: %v", err)
	}
	identity := hex.EncodeToString(suffix[:])
	email := "runtime-undo-" + identity + "@example.test"
	remoteIP := "runtime-undo-" + identity
	manager, err := auth.NewManager(repository.NewAuthRepository(runtimePool))
	if err != nil {
		t.Fatalf("initialize runtime authentication service: %v", err)
	}
	registerKey := "register:ip:" + remoteIP
	loginIPKey := "login:ip:" + remoteIP
	accountDigest := sha256.Sum256([]byte(email))
	loginEmailKey := "login:email:" + base64.RawURLEncoding.EncodeToString(accountDigest[:])
	rateLimitHashes := [][]byte{rateLimitKeyHash(registerKey), rateLimitKeyHash(loginIPKey), rateLimitKeyHash(loginEmailKey)}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := adminPool.Exec(cleanupCtx, `DELETE FROM app.users WHERE email=$1`, email); cleanupErr != nil {
			t.Errorf("clean up only the runtime integration account: %v", cleanupErr)
		}
		for _, keyHash := range rateLimitHashes {
			if _, cleanupErr := adminPool.Exec(cleanupCtx, `DELETE FROM app.auth_rate_limits WHERE key_hash=$1`, keyHash); cleanupErr != nil {
				t.Errorf("clean up only the runtime integration rate-limit key: %v", cleanupErr)
			}
		}
	})

	if _, err := manager.Register(ctx, email, "Runtime integration password 9!", remoteIP); err != nil {
		t.Fatalf("register fixture through runtime role: %v", err)
	}
	user, token, _, err := manager.Login(ctx, email, "Runtime integration password 9!", remoteIP)
	if err != nil {
		t.Fatalf("login fixture through runtime role: %v", err)
	}
	cookie := &http.Cookie{Name: "todo_session", Value: token}
	taskService := task.NewService(repository.NewTaskRepository(runtimePool))
	server := httptest.NewServer(NewRouter(health.NewService(runtimePool), testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(manager, "todo_session", false), Tasks: NewTaskHandlers(taskService), TrustedOrigin: "http://127.0.0.1:8080",
	}))
	t.Cleanup(server.Close)

	created := postTask(t, server, cookie, "http://127.0.0.1:8080", `{"title":"Runtime original"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("runtime API task creation failed: status=%d body=%s", created.Code, created.Body.String())
	}
	createdTask := decodeTaskResponse(t, created)
	if events := getActivityPage(t, server, cookie, createdTask.ID, "").Items; len(events) != 1 || events[0].EventType != "TASK_CREATED" || events[0].ActorUserID != user.ID {
		t.Fatalf("runtime task creation event missing or has wrong actor: %+v", events)
	}

	updated := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+createdTask.ID, `{"title":"Runtime changed","description":"","status":"TODO","version":1}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("runtime API task update failed: status=%d body=%s", updated.Code, updated.Body.String())
	}
	updatedTask := decodeTaskResponse(t, updated)
	activity := getActivityPage(t, server, cookie, createdTask.ID, "").Items
	var titleEventID string
	for _, event := range activity {
		if event.EventType == "TASK_TITLE_UPDATED" {
			titleEventID = event.ID
			break
		}
	}
	if titleEventID == "" {
		t.Fatalf("runtime API update did not append title activity: %+v", activity)
	}

	undo := sendTaskRequest(t, server, cookie, http.MethodPost, fmt.Sprintf("/api/tasks/%s/activity/%s/undo", createdTask.ID, titleEventID), `{"version":2}`)
	if undo.Code != http.StatusOK {
		t.Fatalf("runtime API Undo failed: status=%d body=%s", undo.Code, undo.Body.String())
	}
	var undoResponse struct {
		Task     taskWire     `json:"task"`
		Activity activityWire `json:"activity"`
	}
	if err := json.Unmarshal(undo.Body.Bytes(), &undoResponse); err != nil {
		t.Fatalf("decode runtime Undo response: %v", err)
	}
	if undoResponse.Task.Title != "Runtime original" || undoResponse.Task.Version != updatedTask.Version+1 || undoResponse.Activity.EventType != "TASK_UNDONE" || undoResponse.Activity.ActorUserID != user.ID {
		t.Fatalf("runtime Undo result/activity is incorrect: %+v", undoResponse)
	}
	finalHistory := getActivityPage(t, server, cookie, createdTask.ID, "").Items
	if len(finalHistory) != 3 || finalHistory[0].EventType != "TASK_UNDONE" {
		t.Fatalf("runtime Undo did not preserve original history and append its own event: %+v", finalHistory)
	}
	t.Log("runtime API create, task update, activity append, and Undo succeeded through todo_runtime")
}

func rateLimitKeyHash(key string) []byte {
	hash := sha256.Sum256([]byte(key))
	return hash[:]
}
