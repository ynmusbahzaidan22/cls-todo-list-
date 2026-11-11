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
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/database"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/health"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/testdatabase"
)

func TestTaskActivityCreationAndUndoAsRuntimePostgreSQL(t *testing.T) {
	adminURL, adminTarget := testdatabase.TestURL(t)
	runtimeURL, runtimeTarget := testdatabase.URL(t, "RUNTIME_DATABASE_URL", "")
	if !testdatabase.SameTarget(adminTarget, runtimeTarget) {
		t.Fatal("RUNTIME_DATABASE_URL must identify the same dedicated test database as TEST_DATABASE_URL")
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
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := adminPool.Exec(cleanupCtx, `DELETE FROM app.users WHERE email=$1`, email); cleanupErr != nil {
			t.Errorf("clean up only the runtime integration account: %v", cleanupErr)
		}
	})
	registerRateLimitTestCleanup(t, adminPool, []string{remoteIP}, []string{email})

	if _, err := manager.Register(ctx, "Runtime User", email, "Runtime integration password 9!", remoteIP); err != nil {
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

	templateRepo := repository.NewTemplateRepository(runtimePool)
	template, err := templateRepo.CreateTemplate(ctx, user.ID, "Runtime cascade", "", []task.TemplateItem{{Title: "Tracked task", Priority: task.PriorityMedium}})
	if err != nil {
		t.Fatalf("runtime role could not create template fixture: %v", err)
	}
	createdFromTemplate, err := templateRepo.ApplyTemplate(ctx, user.ID, template.ID, template.Version, true)
	if err != nil || len(createdFromTemplate) != 1 {
		t.Fatalf("runtime role could not apply template and create application tracking rows: tasks=%d err=%v", len(createdFromTemplate), err)
	}
	var canSelectApplications, canInsertApplications, canUpdateApplications, canDeleteApplications, canTruncateApplications bool
	var canSelectApplicationItems, canInsertApplicationItems, canUpdateApplicationItems, canDeleteApplicationItems, canTruncateApplicationItems bool
	if err := runtimePool.QueryRow(ctx, `
		SELECT has_table_privilege(current_user, 'app.task_template_applications', 'SELECT'),
		       has_table_privilege(current_user, 'app.task_template_applications', 'INSERT'),
		       (has_table_privilege(current_user, 'app.task_template_applications', 'UPDATE') OR has_any_column_privilege(current_user, 'app.task_template_applications', 'UPDATE')),
		       has_table_privilege(current_user, 'app.task_template_applications', 'DELETE'),
		       has_table_privilege(current_user, 'app.task_template_applications', 'TRUNCATE'),
		       has_table_privilege(current_user, 'app.task_template_application_items', 'SELECT'),
		       has_table_privilege(current_user, 'app.task_template_application_items', 'INSERT'),
		       (has_table_privilege(current_user, 'app.task_template_application_items', 'UPDATE') OR has_any_column_privilege(current_user, 'app.task_template_application_items', 'UPDATE')),
		       has_table_privilege(current_user, 'app.task_template_application_items', 'DELETE'),
		       has_table_privilege(current_user, 'app.task_template_application_items', 'TRUNCATE')
	`).Scan(&canSelectApplications, &canInsertApplications, &canUpdateApplications, &canDeleteApplications, &canTruncateApplications,
		&canSelectApplicationItems, &canInsertApplicationItems, &canUpdateApplicationItems, &canDeleteApplicationItems, &canTruncateApplicationItems); err != nil {
		t.Fatalf("inspect template application privileges: %v", err)
	}
	if !canSelectApplications || !canInsertApplications || canUpdateApplications || canDeleteApplications || canTruncateApplications ||
		!canSelectApplicationItems || !canInsertApplicationItems || canUpdateApplicationItems || canDeleteApplicationItems || canTruncateApplicationItems {
		t.Fatalf("unexpected template tracking privileges: applications SELECT/INSERT/UPDATE/DELETE/TRUNCATE=%t/%t/%t/%t/%t, items=%t/%t/%t/%t/%t",
			canSelectApplications, canInsertApplications, canUpdateApplications, canDeleteApplications, canTruncateApplications,
			canSelectApplicationItems, canInsertApplicationItems, canUpdateApplicationItems, canDeleteApplicationItems, canTruncateApplicationItems)
	}

	privilegeTx, err := runtimePool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rollback-only direct tracking-write checks: %v", err)
	}
	for _, check := range []struct {
		name string
		sql  string
		args []any
	}{
		{"application UPDATE", `UPDATE app.task_template_applications SET user_id=user_id WHERE template_id=$1`, []any{template.ID}},
		{"application DELETE", `DELETE FROM app.task_template_applications WHERE template_id=$1`, []any{template.ID}},
		{"application TRUNCATE", `TRUNCATE app.task_template_applications`, nil},
		{"application item UPDATE", `UPDATE app.task_template_application_items SET user_id=user_id WHERE template_id=$1`, []any{template.ID}},
		{"application item DELETE", `DELETE FROM app.task_template_application_items WHERE template_id=$1`, []any{template.ID}},
		{"application item TRUNCATE", `TRUNCATE app.task_template_application_items`, nil},
	} {
		t.Run(check.name, func(t *testing.T) { assertRuntimePermissionDenied(t, ctx, privilegeTx, check.sql, check.args...) })
	}
	if err := privilegeTx.Rollback(ctx); err != nil {
		t.Fatalf("rollback direct tracking-write checks: %v", err)
	}

	if err := templateRepo.DeleteTemplate(ctx, user.ID, template.ID, template.Version); err != nil {
		t.Fatalf("runtime role could not delete template with protected tracking-table cascades: %v", err)
	}
	var remainingApplications, remainingApplicationItems int
	if err := runtimePool.QueryRow(ctx, `SELECT count(*) FROM app.task_template_applications WHERE template_id=$1`, template.ID).Scan(&remainingApplications); err != nil {
		t.Fatalf("check application cascade result: %v", err)
	}
	if err := runtimePool.QueryRow(ctx, `SELECT count(*) FROM app.task_template_application_items WHERE template_id=$1`, template.ID).Scan(&remainingApplicationItems); err != nil {
		t.Fatalf("check application-item cascade result: %v", err)
	}
	if remainingApplications != 0 || remainingApplicationItems != 0 {
		t.Fatalf("template delete left tracking rows: applications=%d items=%d", remainingApplications, remainingApplicationItems)
	}
	t.Log("runtime API activity/Undo and template tracking INSERT/SELECT passed; direct tracking mutations were denied and parent-template FK cascades succeeded")
}

func assertRuntimePermissionDenied(t *testing.T, ctx context.Context, tx pgx.Tx, statement string, args ...any) {
	t.Helper()
	const savepoint = "runtime_tracking_privilege_check"
	if _, err := tx.Exec(ctx, "SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("create tracking privilege-check savepoint: %v", err)
	}
	_, operationErr := tx.Exec(ctx, statement, args...)
	var pgError *pgconn.PgError
	denied := errors.As(operationErr, &pgError) && pgError.Code == "42501"
	if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("roll back tracking privilege-check statement: %v", err)
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("release tracking privilege-check savepoint: %v", err)
	}
	if !denied {
		t.Fatalf("statement error = %v, want PostgreSQL permission denied (42501)", operationErr)
	}
}
