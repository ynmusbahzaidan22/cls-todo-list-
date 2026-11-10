package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/auth"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/health"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/repository"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/task"
)

type completedPageWire struct {
	Items      []taskWire `json:"items"`
	NextCursor *string    `json:"next_cursor"`
	TotalCount int        `json:"total_count"`
}

func TestCompletedTaskLifecycleOrderingAndArchivePostgreSQL(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test PostgreSQL: %v", err)
	}

	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate isolated account suffix: %v", err)
	}
	account := struct{ Email, Password string }{
		Email:    "phase17-18-" + hex.EncodeToString(suffix[:]) + "@example.test",
		Password: "phase 17 18 integration password",
	}
	otherAccount := struct{ Email, Password string }{
		Email:    "phase17-18-other-" + hex.EncodeToString(suffix[:]) + "@example.test",
		Password: "phase 17 18 other integration password",
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM app.users WHERE email = ANY($1::text[])`, []string{account.Email, otherAccount.Email}); cleanupErr != nil {
			t.Errorf("clean up only the isolated lifecycle accounts: %v", cleanupErr)
		}
	})

	manager, err := auth.NewManager(repository.NewAuthRepository(pool))
	if err != nil {
		t.Fatalf("initialize auth manager: %v", err)
	}
	user, err := manager.Register(ctx, account.Email, account.Password, "phase17-18-user")
	if err != nil {
		t.Fatalf("register isolated account: %v", err)
	}
	_, token, _, err := manager.Login(ctx, account.Email, account.Password, "phase17-18-login")
	if err != nil {
		t.Fatalf("create isolated session: %v", err)
	}
	cookie := &http.Cookie{Name: "todo_session", Value: token}
	if _, err := manager.Register(ctx, otherAccount.Email, otherAccount.Password, "phase17-18-other-user"); err != nil {
		t.Fatalf("register second isolated account: %v", err)
	}
	_, otherToken, _, err := manager.Login(ctx, otherAccount.Email, otherAccount.Password, "phase17-18-other-login")
	if err != nil {
		t.Fatalf("create second isolated session: %v", err)
	}
	otherCookie := &http.Cookie{Name: "todo_session", Value: otherToken}
	const origin = "http://127.0.0.1:8080"
	service := task.NewService(repository.NewTaskRepository(pool))
	server := httptest.NewServer(NewRouter(health.NewService(pool), testLogger(), t.TempDir(), RouterOptions{
		Auth: NewAuthHandlers(manager, "todo_session", false), Tasks: NewTaskHandlers(service), TrustedOrigin: origin,
	}))
	defer server.Close()

	prefix := "phase17-18-order-" + strings.Split(account.Email, "@")[0]
	progress := createLifecycleTask(t, server, cookie, origin, prefix+"-progress", task.StatusInProgress)
	todo := createLifecycleTask(t, server, cookie, origin, prefix+"-todo", task.StatusTODO)
	recentDone := createLifecycleTask(t, server, cookie, origin, prefix+"-recent", task.StatusDone)
	archived := createLifecycleTask(t, server, cookie, origin, prefix+"-archived", task.StatusDone)
	if _, err := pool.Exec(ctx, `WITH common AS (SELECT clock_timestamp() AS created)
		UPDATE app.tasks SET created_at = (SELECT created FROM common)
		WHERE user_id = $1 AND id = ANY($2::uuid[])`, user.ID, []string{progress.ID, todo.ID, recentDone.ID}); err != nil {
		t.Fatalf("set equal task ordering timestamps: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET completed_at = statement_timestamp() - interval '11 hours'
		WHERE id = $1 AND user_id = $2`, recentDone.ID, user.ID); err != nil {
		t.Fatalf("set recent completion timestamp: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET completed_at = statement_timestamp() - interval '13 hours'
		WHERE id = $1 AND user_id = $2`, archived.ID, user.ID); err != nil {
		t.Fatalf("set archived completion timestamp: %v", err)
	}

	// Traverse one row at a time. The same status-rank/time/ID tuple drives SQL
	// ordering and the signed keyset cursor.
	activeOrder := make([]string, 0, 3)
	seen := map[string]bool{}
	cursor := ""
	for {
		path := "/api/tasks?q=" + url.QueryEscape(prefix) + "&limit=1"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		response := sendTaskRequest(t, server, cookie, http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("list active tasks status=%d body=%s", response.Code, response.Body.String())
		}
		var page taskListWire
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode active task page: %v", err)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("active cursor duplicated task %s", item.ID)
			}
			seen[item.ID] = true
			activeOrder = append(activeOrder, item.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	wantOrder := []string{progress.ID, todo.ID, recentDone.ID}
	if fmt.Sprint(activeOrder) != fmt.Sprint(wantOrder) {
		t.Fatalf("active order=%v, want status rank then stable creation/ID order %v", activeOrder, wantOrder)
	}

	archiveResponse := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks/completed?period=all&limit=10&tz=Asia%2FAmman", "")
	if archiveResponse.Code != http.StatusOK {
		t.Fatalf("completed archive status=%d body=%s", archiveResponse.Code, archiveResponse.Body.String())
	}
	var archivePage completedPageWire
	if err := json.Unmarshal(archiveResponse.Body.Bytes(), &archivePage); err != nil {
		t.Fatalf("decode completed archive: %v", err)
	}
	if archivePage.TotalCount != 1 || len(archivePage.Items) != 1 || archivePage.Items[0].ID != archived.ID {
		t.Fatalf("completed archive did not apply the 12-hour cutoff: %+v", archivePage)
	}
	otherArchive := sendTaskRequest(t, server, otherCookie, http.MethodGet, "/api/tasks/completed?period=all&tz=Asia%2FAmman", "")
	var otherArchivePage completedPageWire
	if otherArchive.Code != http.StatusOK || json.Unmarshal(otherArchive.Body.Bytes(), &otherArchivePage) != nil || otherArchivePage.TotalCount != 0 || len(otherArchivePage.Items) != 0 {
		t.Fatalf("completed archive crossed account ownership: status=%d page=%+v body=%s", otherArchive.Code, otherArchivePage, otherArchive.Body.String())
	}
	if result := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks/"+archived.ID+"/activity", ""); result.Code != http.StatusOK {
		t.Fatalf("archived task history status=%d body=%s", result.Code, result.Body.String())
	}
	undoTask := createLifecycleTask(t, server, cookie, origin, prefix+"-archived-undo", task.StatusTODO)
	completedUpdate := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+undoTask.ID,
		fmt.Sprintf(`{"title":%q,"description":"","status":"DONE","version":1}`, undoTask.Title))
	if completedUpdate.Code != http.StatusOK {
		t.Fatalf("complete archived Undo fixture: status=%d body=%s", completedUpdate.Code, completedUpdate.Body.String())
	}
	undoTask = decodeTaskResponse(t, completedUpdate)
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET completed_at=statement_timestamp()-interval '13 hours' WHERE id=$1`, undoTask.ID); err != nil {
		t.Fatalf("archive Undo fixture: %v", err)
	}
	undoHistory := getActivityPage(t, server, cookie, undoTask.ID, "")
	var statusEventID string
	for _, event := range undoHistory.Items {
		if event.EventType == "TASK_STATUS_CHANGED" {
			statusEventID = event.ID
			break
		}
	}
	if statusEventID == "" {
		t.Fatal("archived task history omitted its status event")
	}
	undoResponse := sendTaskRequest(t, server, cookie, http.MethodPost,
		"/api/tasks/"+undoTask.ID+"/activity/"+statusEventID+"/undo", fmt.Sprintf(`{"version":%d}`, undoTask.Version))
	if undoResponse.Code != http.StatusConflict {
		t.Fatalf("archived task Undo status=%d, want 409 body=%s", undoResponse.Code, undoResponse.Body.String())
	}
	for name, request := range map[string]func() *httptest.ResponseRecorder{
		"update": func() *httptest.ResponseRecorder {
			return sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+archived.ID, fmt.Sprintf(`{"title":%q,"description":"","status":"DONE","version":1}`, prefix+"-changed"))
		},
		"delete": func() *httptest.ResponseRecorder {
			return sendTaskRequest(t, server, cookie, http.MethodDelete, "/api/tasks/"+archived.ID, `{"version":1}`)
		},
		"bulk status": func() *httptest.ResponseRecorder {
			return sendTaskRequest(t, server, cookie, http.MethodPost, "/api/tasks/bulk", fmt.Sprintf(`{"action":"status","status":"TODO","tasks":[{"id":%q,"version":1}]}`, archived.ID))
		},
	} {
		if result := request(); result.Code != http.StatusConflict {
			t.Errorf("archived task %s status=%d, want 409; body=%s", name, result.Code, result.Body.String())
		}
	}
	var preservedTitle, preservedStatus string
	var preservedVersion int64
	if err := pool.QueryRow(ctx, `SELECT title,status,version FROM app.tasks WHERE id=$1 AND user_id=$2`, archived.ID, user.ID).Scan(&preservedTitle, &preservedStatus, &preservedVersion); err != nil {
		t.Fatalf("read archived task after rejected writes: %v", err)
	}
	if preservedTitle != archived.Title || preservedStatus != task.StatusDone || preservedVersion != archived.Version {
		t.Fatalf("rejected archive mutations changed task: title=%q status=%s version=%d", preservedTitle, preservedStatus, preservedVersion)
	}

	// Compare all period totals to PostgreSQL's own local-calendar boundaries,
	// including inclusive starts and exclusive ends in an IANA timezone.
	zone := "Pacific/Kiritimati"
	periods := []string{"all", "week", "month", "year"}
	for index, period := range periods {
		fixture := createLifecycleTask(t, server, cookie, origin, fmt.Sprintf("%s-period-%02d-before", prefix, index), task.StatusDone)
		var periodStart, periodEnd time.Time
		switch period {
		case "all":
			periodStart = time.Now().UTC().AddDate(-2, 0, 0)
			periodEnd = periodStart
		default:
			query := `SELECT CASE $1
				WHEN 'week' THEN date_trunc('week', statement_timestamp() AT TIME ZONE $2) AT TIME ZONE $2
				WHEN 'month' THEN date_trunc('month', statement_timestamp() AT TIME ZONE $2) AT TIME ZONE $2
				ELSE date_trunc('year', statement_timestamp() AT TIME ZONE $2) AT TIME ZONE $2 END,
			CASE $1 WHEN 'week' THEN (date_trunc('week', statement_timestamp() AT TIME ZONE $2) + interval '7 days') AT TIME ZONE $2
				WHEN 'month' THEN (date_trunc('month', statement_timestamp() AT TIME ZONE $2) + interval '1 month') AT TIME ZONE $2
				ELSE (date_trunc('year', statement_timestamp() AT TIME ZONE $2) + interval '1 year') AT TIME ZONE $2 END`
			if err := pool.QueryRow(ctx, query, period, zone).Scan(&periodStart, &periodEnd); err != nil {
				t.Fatalf("calculate %s local-period boundary: %v", period, err)
			}
		}
		boundaryItems := []struct {
			title       string
			completedAt time.Time
		}{
			{fixture.Title, periodStart.Add(-time.Microsecond)},
			{fmt.Sprintf("%s-period-%02d-start", prefix, index), periodStart},
			{fmt.Sprintf("%s-period-%02d-end", prefix, index), periodEnd},
		}
		for boundaryIndex, item := range boundaryItems {
			id := fixture.ID
			if boundaryIndex > 0 {
				boundaryFixture := createLifecycleTask(t, server, cookie, origin, item.title, task.StatusDone)
				id = boundaryFixture.ID
			}
			if _, err := pool.Exec(ctx, `UPDATE app.tasks SET completed_at=$1 WHERE id=$2 AND user_id=$3`, item.completedAt, id, user.ID); err != nil {
				t.Fatalf("set %s period boundary fixture: %v", period, err)
			}
		}
	}
	for _, period := range periods {
		path := "/api/tasks/completed?period=" + period + "&limit=100&tz=" + url.QueryEscape(zone)
		response := sendTaskRequest(t, server, cookie, http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("completed %s filter status=%d body=%s", period, response.Code, response.Body.String())
		}
		var result completedPageWire
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode %s filter: %v", period, err)
		}
		var expected int
		countQuery := `WITH bounds AS (
			SELECT CASE $2
				WHEN 'week' THEN date_trunc('week', statement_timestamp() AT TIME ZONE $3) AT TIME ZONE $3
				WHEN 'month' THEN date_trunc('month', statement_timestamp() AT TIME ZONE $3) AT TIME ZONE $3
				WHEN 'year' THEN date_trunc('year', statement_timestamp() AT TIME ZONE $3) AT TIME ZONE $3 END AS from_time,
			CASE $2
				WHEN 'week' THEN (date_trunc('week', statement_timestamp() AT TIME ZONE $3) + interval '7 days') AT TIME ZONE $3
				WHEN 'month' THEN (date_trunc('month', statement_timestamp() AT TIME ZONE $3) + interval '1 month') AT TIME ZONE $3
				WHEN 'year' THEN (date_trunc('year', statement_timestamp() AT TIME ZONE $3) + interval '1 year') AT TIME ZONE $3 END AS before_time)
			SELECT count(*)::int FROM app.tasks CROSS JOIN bounds WHERE user_id=$1 AND deleted_at IS NULL AND status='DONE'
			AND completed_at <= statement_timestamp() - interval '12 hours'
			AND (bounds.from_time IS NULL OR (completed_at >= bounds.from_time AND completed_at < bounds.before_time))`
		if err := pool.QueryRow(ctx, countQuery, user.ID, period, zone).Scan(&expected); err != nil {
			t.Fatalf("count %s fixtures with database boundaries: %v", period, err)
		}
		if result.TotalCount != expected || len(result.Items) > expected {
			t.Fatalf("%s archive count=%d items=%d, database expected=%d", period, result.TotalCount, len(result.Items), expected)
		}
	}

	// Pin the cutoff across pages. A task that crosses the 12-hour boundary
	// between page requests must not change either that traversal's count or rows.
	crossingPrefix := prefix + "-cursor-"
	boundary := createLifecycleTask(t, server, cookie, origin, crossingPrefix+"boundary", task.StatusDone)
	for index := 0; index < 11; index++ {
		fixture := createLifecycleTask(t, server, cookie, origin, fmt.Sprintf("%spage-%02d", crossingPrefix, index), task.StatusDone)
		if _, err := pool.Exec(ctx, `UPDATE app.tasks SET completed_at=statement_timestamp()-interval '13 hours' WHERE id=$1`, fixture.ID); err != nil {
			t.Fatalf("age archive page fixture: %v", err)
		}
	}
	var stableTotal int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM app.tasks WHERE user_id=$1 AND deleted_at IS NULL AND status='DONE' AND completed_at <= statement_timestamp()-interval '12 hours'`, user.ID).Scan(&stableTotal); err != nil {
		t.Fatalf("count stable archive fixtures: %v", err)
	}
	first := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks/completed?period=all&limit=1&tz=UTC", "")
	var firstPage completedPageWire
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &firstPage) != nil || firstPage.TotalCount != stableTotal || firstPage.NextCursor == nil {
		t.Fatalf("first archive page count differs from stable DB fixture count %d: status=%d page=%+v body=%s", stableTotal, first.Code, firstPage, first.Body.String())
	}
	parts := strings.Split(*firstPage.NextCursor, ".")
	if len(parts) != 2 {
		t.Fatalf("completed cursor has an unexpected format")
	}
	cursorJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode signed completed cursor payload: %v", err)
	}
	var cursorState struct {
		Cutoff time.Time `json:"cutoff"`
	}
	if err := json.Unmarshal(cursorJSON, &cursorState); err != nil || cursorState.Cutoff.IsZero() {
		t.Fatalf("completed cursor did not pin its database cutoff: state=%+v err=%v", cursorState, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET completed_at=$1 WHERE id=$2 AND user_id=$3`, cursorState.Cutoff.Add(-12*time.Hour+time.Second), boundary.ID, user.ID); err != nil {
		t.Fatalf("place fixture immediately before the cursor cutoff: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	next := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks/completed?period=all&limit=1&tz=UTC&cursor="+url.QueryEscape(*firstPage.NextCursor), "")
	var nextPage completedPageWire
	if next.Code != http.StatusOK || json.Unmarshal(next.Body.Bytes(), &nextPage) != nil || nextPage.TotalCount != firstPage.TotalCount {
		t.Fatalf("archive page count changed as the boundary crossed: first=%d next=%+v status=%d body=%s", firstPage.TotalCount, nextPage, next.Code, next.Body.String())
	}
	if len(nextPage.Items) != 1 || nextPage.Items[0].ID == boundary.ID {
		t.Fatalf("cursor page mixed in a task outside its pinned cutoff: %+v", nextPage)
	}

	// The same task can leave DONE and be completed again; the fresh timestamp
	// puts it back in the review window without requiring a worker or migration.
	transition := createLifecycleTask(t, server, cookie, origin, prefix+"-transition", task.StatusDone)
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET completed_at=statement_timestamp()-interval '13 hours' WHERE id=$1`, transition.ID); err != nil {
		t.Fatalf("archive lifecycle transition fixture: %v", err)
	}
	archivedTask := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+transition.ID, fmt.Sprintf(`{"title":%q,"description":"","status":"TODO","version":1}`, transition.Title))
	if archivedTask.Code != http.StatusConflict {
		t.Fatalf("archived task unexpectedly left DONE: %d %s", archivedTask.Code, archivedTask.Body.String())
	}
	// Use an initially recent task to exercise the supported leave-and-recomplete path.
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET completed_at=statement_timestamp()-interval '11 hours' WHERE id=$1`, recentDone.ID); err != nil {
		t.Fatalf("make transition task recently completed: %v", err)
	}
	leave := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+recentDone.ID, fmt.Sprintf(`{"title":%q,"description":"","status":"TODO","version":1}`, recentDone.Title))
	if leave.Code != http.StatusOK {
		t.Fatalf("leave DONE status=%d body=%s", leave.Code, leave.Body.String())
	}
	left := decodeTaskResponse(t, leave)
	if left.CompletedAt != nil {
		t.Fatalf("leaving DONE retained completed_at: %+v", left)
	}
	completeAgain := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+recentDone.ID, fmt.Sprintf(`{"title":%q,"description":"","status":"DONE","version":%d}`, recentDone.Title, left.Version))
	if completeAgain.Code != http.StatusOK {
		t.Fatalf("recomplete status=%d body=%s", completeAgain.Code, completeAgain.Body.String())
	}
	recompleted := decodeTaskResponse(t, completeAgain)
	if recompleted.CompletedAt == nil || !recompleted.CompletedAt.After(*recentDone.CompletedAt) {
		t.Fatalf("recompletion did not restart the review clock: %+v", recompleted)
	}

	nearCutoff := createLifecycleTask(t, server, cookie, origin, prefix+"-concurrent-cutoff", task.StatusDone)
	if _, err := pool.Exec(ctx, `UPDATE app.tasks SET completed_at=statement_timestamp()-interval '12 hours'+interval '10 seconds' WHERE id=$1`, nearCutoff.ID); err != nil {
		t.Fatalf("place concurrent transition near the archive cutoff: %v", err)
	}
	startConcurrent := make(chan struct{})
	type mutationResult struct{ response *httptest.ResponseRecorder }
	concurrentResults := make(chan mutationResult, 2)
	for _, status := range []string{task.StatusTODO, task.StatusInProgress} {
		status := status
		go func() {
			<-startConcurrent
			response := sendTaskRequest(t, server, cookie, http.MethodPut, "/api/tasks/"+nearCutoff.ID,
				fmt.Sprintf(`{"title":%q,"description":"","status":%q,"version":1}`, nearCutoff.Title, status))
			concurrentResults <- mutationResult{response: response}
		}()
	}
	close(startConcurrent)
	statuses := make([]int, 0, 2)
	for index := 0; index < 2; index++ {
		statuses = append(statuses, (<-concurrentResults).response.Code)
	}
	if !((statuses[0] == http.StatusOK && statuses[1] == http.StatusConflict) || (statuses[1] == http.StatusOK && statuses[0] == http.StatusConflict)) {
		t.Fatalf("concurrent status updates near cutoff returned %v, want one success and one version conflict", statuses)
	}
	var finalStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM app.tasks WHERE id=$1 AND user_id=$2`, nearCutoff.ID, user.ID).Scan(&finalStatus); err != nil {
		t.Fatalf("read concurrent cutoff task state: %v", err)
	}
	pageAfterRace := sendTaskRequest(t, server, cookie, http.MethodGet, "/api/tasks?q="+url.QueryEscape(nearCutoff.Title), "")
	var raceList taskListWire
	if pageAfterRace.Code != http.StatusOK || json.Unmarshal(pageAfterRace.Body.Bytes(), &raceList) != nil || len(raceList.Items) != 1 || raceList.Items[0].ID != nearCutoff.ID || raceList.Items[0].Status != finalStatus {
		t.Fatalf("near-cutoff concurrent transition produced missing/duplicate active task: status=%d page=%+v body=%s", pageAfterRace.Code, raceList, pageAfterRace.Body.String())
	}
}

func createLifecycleTask(t *testing.T, server *httptest.Server, cookie *http.Cookie, origin, title, status string) taskWire {
	t.Helper()
	response := postTask(t, server, cookie, origin, fmt.Sprintf(`{"title":%q,"status":%q}`, title, status))
	if response.Code != http.StatusCreated {
		t.Fatalf("create lifecycle fixture %q: status=%d body=%s", title, response.Code, response.Body.String())
	}
	return decodeTaskResponse(t, response)
}
