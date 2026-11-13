import AxeBuilder from "@axe-core/playwright";
import { randomUUID } from "node:crypto";
import { Pool } from "pg";
import { expect, test } from "@playwright/test";
import { getTestDatabaseURL, registerEmailRateLimitCleanup } from "./test-database.js";

const testDatabaseURL = getTestDatabaseURL();
test.describe.configure({ mode: "serial" });

const baseURL = process.env.PHASE8_BASE_URL || "http://127.0.0.1:18080";
const prefix = `phase17-18-${randomUUID().replaceAll("-", "").slice(0, 18)}`;
const accounts = [0, 1].map((index) => ({ name: `Lifecycle User ${index + 1}`, email: `${prefix}-${index}@example.test`, password: `Phase17-18-${randomUUID()}-${randomUUID()}` }));
const testClientIP = registerEmailRateLimitCleanup(test, accounts.map((account) => account.email));
test.use({ extraHTTPHeaders: { "X-Forwarded-For": testClientIP } });
const testViewports = [{ width: 1440, height: 1000 }, { width: 768, height: 1024 }, { width: 375, height: 812 }];
let pool;

async function signIn(page, account) {
  await page.goto("/");
  await page.locator("#login-email").fill(account.email);
  await page.locator("#login-password").fill(account.password);
  await page.locator("#login-submit").click();
  await expect(page.locator("#todo-view")).toBeVisible();
}

async function createTask(page, title, status = "TODO", dueDate = null) {
  const response = await page.request.post("/api/tasks", {
    headers: { Origin: baseURL },
    data: { title, status, ...(dueDate ? { due_date: dueDate } : {}) },
  });
  expect(response.status()).toBe(201);
  return response.json();
}

async function setCompletionTime(taskID, expression) {
  await pool.query(`UPDATE app.tasks SET completed_at = ${expression} WHERE id = $1`, [taskID]);
}

async function assertNoSeriousAxeIssues(page) {
  const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  expect(result.violations.filter((item) => ["critical", "serious"].includes(item.impact)), JSON.stringify(result.violations)).toEqual([]);
}

test.beforeAll(async () => {
  for (const account of accounts) {
    const response = await fetch(`${baseURL}/api/auth/register`, {
      method: "POST", headers: { "Content-Type": "application/json", Origin: baseURL, "X-Forwarded-For": testClientIP }, body: JSON.stringify(account),
    });
    if (response.status !== 202) throw new Error(`Could not register isolated Phase 17/18 account (${response.status}).`);
  }
  pool = new Pool({ connectionString: testDatabaseURL, max: 1 });
});

test.afterAll(async () => {
  try {
    if (!pool) pool = new Pool({ connectionString: testDatabaseURL, max: 1 });
    await pool.query("DELETE FROM app.users WHERE email = ANY($1::text[])", [accounts.map((item) => item.email)]);
    const { rows } = await pool.query("SELECT count(*)::int AS count FROM app.users WHERE email = ANY($1::text[])", [accounts.map((item) => item.email)]);
    if (rows[0].count !== 0) throw new Error("Phase 17/18 browser cleanup left its isolated account.");
  } finally { if (pool) await pool.end(); }
});

test("active ordering, 12-hour transition, refresh/navigation, due states, and keyboard access", async ({ page }) => {
  await signIn(page, accounts[0]);
  const progressTitle = `${prefix}-progress`;
  const todoTitle = `${prefix}-todo`;
  const recentTitle = `${prefix}-recent`;
  const archivedTitle = `${prefix}-archived`;
  const progress = await createTask(page, progressTitle, "IN_PROGRESS");
  const todo = await createTask(page, todoTitle);
  const recent = await createTask(page, recentTitle, "DONE");
  const archived = await createTask(page, archivedTitle, "DONE");
  await pool.query("UPDATE app.tasks SET created_at = statement_timestamp() WHERE id = ANY($1::uuid[])", [[progress.id, todo.id, recent.id]]);
  await setCompletionTime(recent.id, "statement_timestamp() - interval '11 hours 59 minutes 57 seconds'");
  await setCompletionTime(archived.id, "statement_timestamp() - interval '13 hours'");
  await page.clock.install({ time: new Date() });
  await page.reload();

  const taskTitles = page.locator("#task-list > .task-item .task-content .task-text");
  await expect(taskTitles.filter({ hasText: progressTitle })).toHaveCount(1);
  await expect(taskTitles.filter({ hasText: todoTitle })).toHaveCount(1);
  const activeTitles = await taskTitles.allTextContents();
  expect(activeTitles.slice(0, 2)).toEqual([progressTitle, todoTitle]);
  await expect(page.locator("#recently-completed-group")).toBeVisible();
  await page.locator("#recently-completed-details > summary").click();
  await expect(page.locator("#recently-completed-task-list")).toContainText(recentTitle);
  await expect(page.locator("#recently-completed-task-list")).not.toContainText(archivedTitle);

  // The page's normal timer detects the cutoff; no server worker is involved.
  await page.waitForTimeout(4_000);
  await page.clock.fastForward("00:00:31");
  await expect(page.locator("#recently-completed-group")).toBeHidden();
  await page.locator("#open-completed").focus();
  await page.keyboard.press("Enter");
  await expect(page.locator("#completed-tasks-section")).toBeVisible();
  await expect(page.locator("#completed-total-count")).toHaveText("2");
  await expect(page.locator("#completed-task-list")).toContainText(recentTitle);
  await page.reload();
  await expect(page.locator("#completed-tasks-section")).toBeVisible();
  await expect(page.locator("#completed-task-list")).toContainText(recentTitle);
  await page.locator("#completed-back-to-tasks").focus();
  await page.keyboard.press("Enter");
  await expect(page.locator("#task-section")).toBeVisible();

  const localDate = await page.evaluate(() => {
    const part = (type) => new Intl.DateTimeFormat("en", { year: "numeric", month: "2-digit", day: "2-digit" }).formatToParts(new Date()).find((value) => value.type === type)?.value;
    return `${part("year")}-${part("month")}-${part("day")}`;
  });
  const yesterdayDate = new Date(`${localDate}T00:00:00Z`);
  yesterdayDate.setUTCDate(yesterdayDate.getUTCDate() - 1);
  const yesterday = yesterdayDate.toISOString().slice(0, 10);
  const dueTodayTitle = `${prefix}-due-today`;
  const overdueTitle = `${prefix}-overdue`;
  const futureTitle = `${prefix}-future`;
  const dueTodayTask = await createTask(page, dueTodayTitle, "TODO", localDate);
  const overdueTask = await createTask(page, overdueTitle, "TODO", yesterday);
  const futureTask = await createTask(page, futureTitle, "TODO", "2099-01-01");
  await page.reload();
  const dueTodayRow = page.locator(`#task-list [data-task-id="${dueTodayTask.id}"]`);
  const overdueRow = page.locator(`#task-list [data-task-id="${overdueTask.id}"]`);
  const futureRow = page.locator(`#task-list [data-task-id="${futureTask.id}"]`);
  await expect(dueTodayRow.locator(".task-due-date")).toHaveClass(/is-due-today/);
  await expect(overdueRow.locator(".task-due-date")).toHaveClass(/is-overdue/);
  await expect(futureRow.locator(".task-due-date")).not.toHaveClass(/is-overdue|is-due-today/);

  for (const viewport of testViewports) {
    await page.setViewportSize(viewport);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), `horizontal overflow at ${viewport.width}px`).toBeTruthy();
    await assertNoSeriousAxeIssues(page);
  }
});

test("completed archive periods, 10-item paging, read-only presentation, and history", async ({ page }) => {
  await signIn(page, accounts[1]);
  const fixtures = [];
  const ageDays = [2, 2, 2, 2, 10, 10, 10, 40, 40, 400, 400];
  for (let index = 0; index < ageDays.length; index++) {
    const task = await createTask(page, `${prefix}-archive-${String(index).padStart(2, "0")}`, "DONE");
    fixtures.push(task);
  }
  for (let index = 0; index < fixtures.length; index++) {
    const days = ageDays[index];
    await pool.query("UPDATE app.tasks SET completed_at = statement_timestamp() - ($1::int * interval '1 day') WHERE id = $2", [days, fixtures[index].id]);
  }
  await page.locator("#open-completed").focus();
  await page.keyboard.press("Enter");
  await expect(page.locator("#completed-tasks-section")).toBeVisible();
  await expect(page.locator("#completed-total-count")).toHaveText("11");
  await expect(page.locator("#completed-shown-count")).toHaveText("10");
  await expect(page.locator("#completed-more-button")).toBeVisible();

  const zone = "Asia/Amman";
  for (const period of ["all", "week", "month", "year"]) {
    const expected = await pool.query(`WITH bounds AS (
      SELECT CASE $1
        WHEN 'week' THEN date_trunc('week', statement_timestamp() AT TIME ZONE $2) AT TIME ZONE $2
        WHEN 'month' THEN date_trunc('month', statement_timestamp() AT TIME ZONE $2) AT TIME ZONE $2
        WHEN 'year' THEN date_trunc('year', statement_timestamp() AT TIME ZONE $2) AT TIME ZONE $2 END AS from_time,
      CASE $1
        WHEN 'week' THEN (date_trunc('week', statement_timestamp() AT TIME ZONE $2) + interval '7 days') AT TIME ZONE $2
        WHEN 'month' THEN (date_trunc('month', statement_timestamp() AT TIME ZONE $2) + interval '1 month') AT TIME ZONE $2
        WHEN 'year' THEN (date_trunc('year', statement_timestamp() AT TIME ZONE $2) + interval '1 year') AT TIME ZONE $2 END AS before_time)
      SELECT count(*)::int FROM app.tasks CROSS JOIN bounds WHERE id = ANY($3::uuid[]) AND status='DONE'
        AND completed_at <= statement_timestamp() - interval '12 hours'
        AND ($1='all' OR (completed_at >= bounds.from_time AND completed_at < bounds.before_time))`, [period, zone, fixtures.map((item) => item.id)]);
    await page.locator("#completed-period-filter").selectOption(period);
    await expect(page.locator("#completed-total-count")).toHaveText(String(expected.rows[0].count));
    await expect(page.locator("#completed-shown-count")).toHaveText(String(Math.min(expected.rows[0].count, 10)));
  }

  await page.locator("#completed-period-filter").selectOption("all");
  await page.locator("#completed-more-button").click();
  await expect(page.locator("#completed-shown-count")).toHaveText("11");
  await expect(page.locator("#completed-more-button")).toBeHidden();
  const archiveRow = page.locator(`#completed-task-list [data-task-id="${fixtures[0].id}"]`);
  await expect(archiveRow.getByRole("button", { name: /View history for completed task/ })).toBeVisible();
  await expect(archiveRow.locator("select, input[type=checkbox]")).toHaveCount(0);
  await expect(archiveRow.getByRole("button", { name: /Edit|Delete|Undo/ })).toHaveCount(0);
  await archiveRow.getByRole("button", { name: /View history for completed task/ }).click();
  await expect(page.locator("#activity-dialog")).toBeVisible();
  await expect(page.locator("#activity-task-title")).toHaveText(fixtures[0].title);
  await page.locator("#activity-close-button").click();

  for (const viewport of testViewports) {
    await page.setViewportSize(viewport);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), `archive horizontal overflow at ${viewport.width}px`).toBeTruthy();
    await assertNoSeriousAxeIssues(page);
  }
});
