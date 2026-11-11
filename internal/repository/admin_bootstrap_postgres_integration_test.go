package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/adminbootstrap"
	"github.com/ynmusbahzaidan22/cls-todo-list-/internal/testdatabase"
)

func TestAdminBootstrapPostgreSQL(t *testing.T) {
	pool := openAdminBootstrapPool(t)
	repo := NewAdminBootstrapRepository(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	assertBootstrapZeroBaseline(t, ctx, pool)

	goodID, disabledID := bootstrapUUID(t), bootstrapUUID(t)
	goodEmail, disabledEmail := bootstrapEmail(t), bootstrapEmail(t)
	registerBootstrapCleanup(t, pool, []string{goodID, disabledID})
	insertBootstrapUser(t, ctx, pool, goodID, goodEmail, "ACTIVE")
	insertBootstrapUser(t, ctx, pool, disabledID, disabledEmail, "DISABLED")

	before, err := repo.Status(ctx, "")
	if err != nil || before.ActiveAdmins != 0 || before.GuardAdmins != 0 {
		t.Fatalf("read-only initial status=%+v err=%v", before, err)
	}
	err = repo.Bootstrap(ctx, bootstrapEmail(t))
	if !errors.Is(err, adminbootstrap.ErrNotFound) {
		t.Fatalf("missing target error=%v", err)
	}
	if err := repo.Bootstrap(ctx, disabledEmail); !errors.Is(err, adminbootstrap.ErrIneligible) {
		t.Fatalf("disabled target error=%v", err)
	}
	status, err := repo.Status(ctx, goodEmail)
	if err != nil || !status.Eligible || !status.TargetExists || !status.TargetActive || status.TargetRole != "USER" || status.ActiveAdmins != 0 {
		t.Fatalf("eligible target status=%+v err=%v", status, err)
	}
	tokenHash := bootstrapSessionHash(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO app.sessions(token_hash,user_id,created_at,last_activity_at,idle_expires_at,absolute_expires_at) VALUES($1,$2::uuid,$3,$3,$4,$5)`, tokenHash[:], goodID, now, now.Add(time.Hour), now.Add(24*time.Hour)); err != nil {
		t.Fatalf("insert target session fixture: %v", err)
	}
	if err := repo.Bootstrap(ctx, goodEmail); err != nil {
		t.Fatalf("first bootstrap: %v", err)
	}
	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM app.users WHERE id=$1::uuid`, goodID).Scan(&role); err != nil || role != "ADMIN" {
		t.Fatalf("selected target role=%q err=%v", role, err)
	}
	var sessionCount, activeSessions, adminCount, guardCount, auditCount int64
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE revoked_at IS NULL) FROM app.sessions WHERE user_id=$1::uuid`, goodID).Scan(&sessionCount, &activeSessions); err != nil || sessionCount != 1 || activeSessions != 0 {
		t.Fatalf("session revocation counts total=%d active=%d err=%v", sessionCount, activeSessions, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE role='ADMIN' AND status='ACTIVE'`).Scan(&adminCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT active_count FROM app.active_admin_guard WHERE singleton`).Scan(&guardCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.admin_audit_events WHERE target_user_id=$1::uuid AND action='INITIAL_ADMIN_BOOTSTRAPPED' AND actor_user_id IS NULL AND before_state->>'role'='USER' AND after_state->>'role'='ADMIN'`, goodID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if adminCount != 1 || guardCount != 1 || auditCount != 1 {
		t.Fatalf("active admins=%d guard=%d bootstrap audit=%d", adminCount, guardCount, auditCount)
	}
	if err := repo.Bootstrap(ctx, disabledEmail); !errors.Is(err, adminbootstrap.ErrAdminExists) {
		t.Fatalf("second bootstrap error=%v", err)
	}
	after, err := repo.Status(ctx, "")
	if err != nil || after.ActiveAdmins != 1 || after.GuardAdmins != 1 {
		t.Fatalf("post bootstrap status=%+v err=%v", after, err)
	}
}

func TestAdminBootstrapConcurrentPostgreSQL(t *testing.T) {
	pool := openAdminBootstrapPool(t)
	repo := NewAdminBootstrapRepository(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	assertBootstrapZeroBaseline(t, ctx, pool)
	ids := []string{bootstrapUUID(t), bootstrapUUID(t)}
	emails := []string{bootstrapEmail(t), bootstrapEmail(t)}
	registerBootstrapCleanup(t, pool, ids)
	for i := range ids {
		insertBootstrapUser(t, ctx, pool, ids[i], emails[i], "ACTIVE")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, email := range emails {
		wg.Add(1)
		go func(value string) { defer wg.Done(); results <- repo.Bootstrap(ctx, value) }(email)
	}
	wg.Wait()
	close(results)
	success, rejected := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			rejected++
		}
	}
	var admins, guard int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE role='ADMIN' AND status='ACTIVE' AND id=ANY($1::uuid[])`, ids).Scan(&admins); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT active_count FROM app.active_admin_guard WHERE singleton`).Scan(&guard); err != nil {
		t.Fatal(err)
	}
	if success != 1 || rejected != 1 || admins != 1 || guard != 1 {
		t.Fatalf("concurrent results success=%d rejected=%d fixture_admins=%d guard=%d", success, rejected, admins, guard)
	}
}

func openAdminBootstrapPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	adminURL, target := testdatabase.URL(t, "TEST_DATABASE_URL", "")
	config, err := pgx.ParseConfig(adminURL)
	if err != nil || config.Host != "127.0.0.1" || config.Port != 25432 || config.Database != "todo_test" {
		t.Fatal("bootstrap integration requires the isolated test administrator target")
	}
	runtimeURL, runtimeTarget := testdatabase.URL(t, "RUNTIME_DATABASE_URL", "")
	runtimeConfig, err := pgx.ParseConfig(runtimeURL)
	if err != nil || runtimeConfig.Host != "127.0.0.1" || runtimeConfig.Port != 25432 || runtimeConfig.Database != "todo_test" || runtimeConfig.User != "todo_test_runtime" || target != runtimeTarget {
		t.Fatal("bootstrap integration runtime URL is not the same isolated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal("open isolated bootstrap test pool")
	}
	var database, version string
	if err := pool.QueryRow(ctx, `SELECT current_database(), current_setting('server_version')`).Scan(&database, &version); err != nil || database != "todo_test" || len(version) < 4 || version[:4] != "18.6" {
		pool.Close()
		t.Fatal("bootstrap integration database identity/version check failed")
	}
	if status, err := NewAdminBootstrapRepository(pool).Status(ctx, ""); err != nil || status.ActiveAdmins != 0 || status.GuardAdmins != 0 {
		pool.Close()
		t.Fatal("bootstrap integration requires a zero-Admin isolated test database")
	}
	t.Cleanup(pool.Close)
	return pool
}

func assertBootstrapZeroBaseline(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var active, guard int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE role='ADMIN' AND status='ACTIVE'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT active_count FROM app.active_admin_guard WHERE singleton`).Scan(&guard); err != nil {
		t.Fatal(err)
	}
	if active != 0 || guard != 0 {
		t.Fatalf("integration requires zero active Admins and a matching guard; observed counts %d/%d", active, guard)
	}
}

func insertBootstrapUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, email, status string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO app.users(id,name,email,password_hash,role,status) VALUES($1::uuid,'Bootstrap fixture',$2,'integration-only-hash','USER',$3)`, id, email, status); err != nil {
		t.Fatal("insert isolated bootstrap account fixture")
	}
}

func registerBootstrapCleanup(t *testing.T, pool *pgxpool.Pool, ids []string) {
	t.Helper()
	ids = append([]string(nil), ids...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Errorf("begin exact bootstrap fixture cleanup: %v", err)
			return
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		var active int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE id=ANY($1::uuid[]) AND role='ADMIN' AND status='ACTIVE'`, ids).Scan(&active); err != nil {
			t.Errorf("count exact bootstrap fixture Admins: %v", err)
			return
		}
		if active > 0 {
			if _, err := tx.Exec(ctx, `UPDATE app.active_admin_guard SET active_count=active_count+$1 WHERE singleton`, active); err != nil {
				t.Errorf("prepare exact bootstrap fixture cleanup: %v", err)
				return
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.admin_audit_events WHERE target_user_id=ANY($1::uuid[])`, ids); err != nil {
			t.Errorf("delete exact bootstrap fixture audit rows: %v", err)
			return
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.sessions WHERE user_id=ANY($1::uuid[])`, ids); err != nil {
			t.Errorf("delete exact bootstrap fixture sessions: %v", err)
			return
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.users WHERE id=ANY($1::uuid[])`, ids); err != nil {
			t.Errorf("delete exact bootstrap fixture accounts: %v", err)
			return
		}
		if active > 0 {
			if _, err := tx.Exec(ctx, `UPDATE app.active_admin_guard SET active_count=active_count-$1 WHERE singleton`, active); err != nil {
				t.Errorf("restore exact active Admin guard baseline: %v", err)
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("commit exact bootstrap fixture cleanup: %v", err)
			return
		}
		var remainingUsers, remainingSessions, remainingAudit, remainingAdmins, remainingGuard int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE id=ANY($1::uuid[])`, ids).Scan(&remainingUsers); err != nil {
			t.Errorf("verify bootstrap fixture account cleanup: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.sessions WHERE user_id=ANY($1::uuid[])`, ids).Scan(&remainingSessions); err != nil {
			t.Errorf("verify bootstrap fixture session cleanup: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.admin_audit_events WHERE target_user_id=ANY($1::uuid[])`, ids).Scan(&remainingAudit); err != nil {
			t.Errorf("verify bootstrap fixture audit cleanup: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.users WHERE role='ADMIN' AND status='ACTIVE'`).Scan(&remainingAdmins); err != nil {
			t.Errorf("verify active Admin cleanup: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT active_count FROM app.active_admin_guard WHERE singleton`).Scan(&remainingGuard); err != nil {
			t.Errorf("verify active Admin guard cleanup: %v", err)
		}
		if remainingUsers != 0 || remainingSessions != 0 || remainingAudit != 0 || remainingAdmins != 0 || remainingGuard != 0 {
			t.Errorf("bootstrap fixture cleanup residue counts users=%d sessions=%d audit=%d admins=%d guard=%d", remainingUsers, remainingSessions, remainingAudit, remainingAdmins, remainingGuard)
		}
	})
}

func bootstrapUUID(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal("generate bootstrap fixture ID")
	}
	value := hex.EncodeToString(raw[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", value[:8], value[8:12], value[12:16], value[16:20], value[20:])
}

func bootstrapEmail(t *testing.T) string {
	t.Helper()
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal("generate bootstrap fixture email")
	}
	return "bootstrap-" + hex.EncodeToString(raw[:]) + "@example.test"
}

func bootstrapSessionHash(t *testing.T) [32]byte {
	t.Helper()
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal("generate bootstrap fixture session")
	}
	return sha256.Sum256(token[:])
}
