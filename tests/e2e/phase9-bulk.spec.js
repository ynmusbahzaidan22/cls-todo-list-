import AxeBuilder from "@axe-core/playwright";
import { randomUUID } from "node:crypto";
import { Pool } from "pg";
import { expect, test } from "@playwright/test";
import { getTestDatabaseURL, registerEmailRateLimitCleanup } from "./test-database.js";
import { expectMaskedHeaderEmail } from "../e2e-support/header-email.js";

const testDatabaseURL = getTestDatabaseURL();
test.describe.configure({ mode: "serial" });

const baseURL = process.env.PHASE8_BASE_URL || "http://127.0.0.1:18080";
const runId = randomUUID().replaceAll("-", "").slice(0, 16);
const emailPrefix = `phase9-bulk-e2e-${runId}`;
const password = `Phase9-${randomUUID()}-${randomUUID()}`;
const accountA = { name: "Bulk User A", email: `${emailPrefix}-a@example.test`, password };
const accountB = { name: "Bulk User B", email: `${emailPrefix}-b@example.test`, password };
const accountPagination = { name: "Bulk Pagination User", email: `${emailPrefix}-pagination@example.test`, password };
const testClientIP = registerEmailRateLimitCleanup(test, [accountA.email, accountB.email, accountPagination.email]);
test.use({ extraHTTPHeaders: { "X-Forwarded-For": testClientIP } });
let databasePool;

async function database() {
  if (databasePool) return databasePool;
  const connectionString = testDatabaseURL;
  databasePool = new Pool({ connectionString, max: 1 });
  return databasePool;
}

async function createAccount(account) {
  const response = await fetch(`${baseURL}/api/auth/register`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Origin: baseURL, "X-Forwarded-For": testClientIP },
    body: JSON.stringify(account),
  });
  if (response.status !== 202) throw new Error(`Could not create isolated Phase 9 account (${response.status}).`);
}

async function signIn(page, account) {
  await page.goto("/");
  await expect(page.locator("#auth-view")).toBeVisible();
  await page.locator("#login-email").fill(account.email);
  await page.locator("#login-password").fill(account.password);
  await page.locator("#login-submit").click();
  await expect(page.locator("#todo-view")).toBeVisible();
  await expectMaskedHeaderEmail(page, account.email);
}

async function openRecentlyDeleted(page) {
  const viewAll = page.locator("#open-deleted-view");
  if (await viewAll.isVisible()) await viewAll.click();
  else await page.goto("/#recently-deleted");
  await expect(page.locator("#recently-deleted-section")).toBeVisible();
}

function taskRow(page, title) {
  const escaped = new RegExp(`^${title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`);
  return page.locator("#task-list .task-item").filter({ has: page.locator(".task-content h3", { hasText: escaped }) });
}

async function addTask(page, title) {
  await page.locator("#task-input").fill(title);
  await page.getByRole("button", { name: "Add Task" }).click();
  await expect(taskRow(page, title)).toBeVisible();
}

async function selectTasks(page, titles) {
  for (const title of titles) await taskRow(page, title).getByRole("checkbox", { name: `Select ${title}` }).check();
  await expect(page.locator("#bulk-selection-count")).toHaveText(`${titles.length} selected`);
}

test.beforeAll(async () => {
  await createAccount(accountA);
  await createAccount(accountB);
  const pool = await database();
  const cloned = await pool.query(
    `INSERT INTO app.users (email, password_hash)
     SELECT $1, password_hash FROM app.users WHERE email = $2`,
    [accountPagination.email, accountA.email],
  );
  if (cloned.rowCount !== 1) throw new Error("Could not create the isolated pagination-test account.");
});

test.afterAll(async () => {
  try {
    const pool = await database();
    await pool.query("DELETE FROM app.users WHERE email LIKE $1", [`${emailPrefix}%`]);
    const { rows } = await pool.query("SELECT count(*)::int AS count FROM app.users WHERE email LIKE $1", [`${emailPrefix}%`]);
    if (rows[0].count !== 0) throw new Error(`Phase 9 cleanup left ${rows[0].count} test accounts.`);
  } finally {
    if (databasePool) await databasePool.end();
  }
});

test("bulk status and confirmed delete update task and history, with normal Undo compatibility", async ({ page }) => {
  await signIn(page, accountA);
  const first = `${emailPrefix} first`;
  const second = `${emailPrefix} second`;
  await addTask(page, first);
  await addTask(page, second);

  const firstSelection = taskRow(page, first).getByRole("checkbox", { name: `Select ${first}` });
  await firstSelection.focus();
  await firstSelection.press("Space");
  await taskRow(page, second).getByRole("checkbox", { name: `Select ${second}` }).check();
  await expect(page.locator("#bulk-selection-count")).toHaveText("2 selected");
  await page.locator("#select-all-visible").uncheck();
  await page.locator("#select-all-visible").check();
  await expect(page.locator("#bulk-selection-count")).toHaveText("2 selected");
  await page.locator("#bulk-status").selectOption("DONE");
  let releaseBulk;
  const bulkGate = new Promise((resolve) => { releaseBulk = resolve; });
  let bulkRequests = 0;
  await page.route("**/api/tasks/bulk", async (route) => {
    if (route.request().method() === "POST") {
      bulkRequests += 1;
      await bulkGate;
    }
    await route.continue();
  });
  await page.locator("#bulk-apply-status").click();
  await expect(page.locator("#bulk-apply-status")).toBeDisabled();
  await page.locator("#bulk-apply-status").evaluate((button) => button.click());
  expect(bulkRequests).toBe(1);
  releaseBulk();
  await expect(taskRow(page, first).locator(".status-badge")).toHaveText("Done");
  await expect(taskRow(page, second).locator(".status-badge")).toHaveText("Done");
  await expect(page.locator("#task-feedback")).toContainText("2 tasks updated to Done");
  await expect(page.locator("#bulk-selection-count")).toHaveText("0 selected");
  await page.unroute("**/api/tasks/bulk");

  await page.locator("#recently-completed-details > summary").click();
  const firstRow = taskRow(page, first);
  await firstRow.getByRole("button", { name: `View details and activity for ${first}` }).click();
  await expect(page.locator("#activity-list .activity-event")).toContainText(["Status changed"]);
  const statusEvent = page.locator("#activity-list .activity-event").filter({ hasText: "Status changed" });
  await statusEvent.getByRole("button", { name: "Undo status changed" }).click();
  await expect(taskRow(page, first).locator(".status-badge")).toHaveText("To do");
  await expect(page.locator("#activity-action-feedback")).toContainText("Undo completed");
  await page.locator("#activity-close-button").click();

  await selectTasks(page, [first, second]);
  await page.locator("#bulk-delete").click();
  await expect(page.locator("#bulk-confirm-dialog")).toBeVisible();
  await expect(page.locator("#bulk-confirm-description")).toContainText("Delete 2 selected tasks");
  await expect(page.locator("#bulk-cancel-delete")).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(page.locator("#bulk-confirm-delete")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.locator("#bulk-confirm-dialog")).toBeHidden();
  await page.locator("#bulk-delete").click();
  await page.locator("#bulk-cancel-delete").click();
  await expect(page.locator("#bulk-confirm-dialog")).toBeHidden();
  await page.locator("#bulk-delete").click();
  await page.locator("#bulk-confirm-delete").click();
  await expect(taskRow(page, first)).toHaveCount(0);
  await expect(taskRow(page, second)).toHaveCount(0);
  await expect(page.locator("#task-feedback")).toContainText("2 tasks deleted");
  await openRecentlyDeleted(page);
  await expect(page.locator("#deleted-task-list .deleted-task-item")).toHaveCount(2);
});

test("stale selection rejects atomically, refreshes state/history, and shows a clear conflict", async ({ page }) => {
  await signIn(page, accountA);
  const first = `${emailPrefix} stale first`;
  const second = `${emailPrefix} stale second`;
  await addTask(page, first);
  await addTask(page, second);
  await selectTasks(page, [first, second]);
  const rows = await (await page.request.get("/api/tasks?limit=100")).json();
  const staleTask = rows.items.find((item) => item.title === second);
  expect(staleTask).toBeTruthy();
  let raced = false;
  await page.route("**/api/tasks/bulk", async (route) => {
    if (!raced && route.request().method() === "POST") {
      raced = true;
      const update = await page.request.put(`/api/tasks/${staleTask.id}`, {
        headers: { Origin: baseURL },
        data: { title: second, description: "newer external value", status: "IN_PROGRESS", version: staleTask.version },
      });
      expect(update.status()).toBe(200);
    }
    await route.continue();
  });
  await page.locator("#bulk-status").selectOption("DONE");
  await page.locator("#bulk-apply-status").click();
  await expect(page.locator("#task-feedback")).toContainText("No tasks were changed");
  await expect(taskRow(page, first).locator(".status-badge")).toHaveText("To do");
  await expect(taskRow(page, second).locator(".status-badge")).toHaveText("In progress");
  await expect(page.locator("#bulk-selection-count")).toHaveText("2 selected");

  await taskRow(page, first).getByRole("button", { name: `View details and activity for ${first}` }).click();
  await expect(page.locator("#activity-list .activity-event")).toHaveCount(1);
  await expect(page.locator("#activity-list")).not.toContainText("Status changed");
  await page.locator("#activity-close-button").click();
  await page.unroute("**/api/tasks/bulk");
});

test("selection clears on account switch and bulk requests cannot target another account's task", async ({ page }) => {
  await signIn(page, accountA);
  const privateTitle = `${emailPrefix} private task`;
  await addTask(page, privateTitle);
  const taskData = await (await page.request.get("/api/tasks?limit=100")).json();
  const privateTask = taskData.items.find((item) => item.title === privateTitle);
  expect(privateTask).toBeTruthy();
  await taskRow(page, privateTitle).getByRole("checkbox", { name: `Select ${privateTitle}` }).check();
  await expect(page.locator("#bulk-selection-count")).toHaveText("1 selected");
  await page.locator("#logout-button").click();
  await expect(page.locator("#auth-view")).toBeVisible();
  await signIn(page, accountB);
  await expect(page.locator("#task-list")).not.toContainText(privateTitle);
  await expect(page.locator("#bulk-selection-count")).toHaveText("0 selected");
  const forbidden = await page.request.post("/api/tasks/bulk", {
    headers: { Origin: baseURL },
    data: { action: "delete", tasks: [{ id: privateTask.id, version: privateTask.version }] },
  });
  expect(forbidden.status()).toBe(404);
  await page.reload();
  await expectMaskedHeaderEmail(page, accountB.email);
  await expect(page.locator("#task-list")).not.toContainText(privateTitle);
  expect(await page.evaluate(() => Object.keys(localStorage))).toEqual([]);
});

test("keeps bulk selection on a retryable network error and remains accessible at mobile width", async ({ page }) => {
  await signIn(page, accountA);
  const title = `${emailPrefix} retry task`;
  await addTask(page, title);
  await selectTasks(page, [title]);
  await page.route("**/api/tasks/bulk", (route) => route.abort("failed"));
  await page.locator("#bulk-status").selectOption("DONE");
  await page.locator("#bulk-apply-status").click();
  await expect(page.locator("#task-feedback")).toContainText("couldn't confirm the bulk operation");
  await expect(taskRow(page, title).locator(".status-badge")).toHaveText("To do");
  await expect(page.locator("#bulk-selection-count")).toHaveText("1 selected");
  await page.unroute("**/api/tasks/bulk");

  for (const width of [1440, 768, 375]) {
    await page.setViewportSize({ width, height: 812 });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
    expect(overflow, `horizontal overflow at ${width}px`).toBeFalsy();
  }
  const results = await new AxeBuilder({ page }).include("#bulk-controls").withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  expect(results.violations.filter((violation) => ["critical", "serious"].includes(violation.impact))).toEqual([]);
});

test("an account switch during a pending bulk request does not restore the prior selection", async ({ page }) => {
  await signIn(page, accountA);
  const title = `${emailPrefix} switching task`;
  await addTask(page, title);
  const taskData = await (await page.request.get("/api/tasks?limit=100")).json();
  const taskBeforeSwitch = taskData.items.find((item) => item.title === title);
  expect(taskBeforeSwitch).toBeTruthy();
  await selectTasks(page, [title]);
  let releaseBulk;
  const bulkGate = new Promise((resolve) => { releaseBulk = resolve; });
  await page.route("**/api/tasks/bulk", async (route) => {
    if (route.request().method() === "POST") await bulkGate;
    await route.continue();
  });
  await page.locator("#bulk-status").selectOption("DONE");
  await page.locator("#bulk-apply-status").click();
  await expect(page.locator("#bulk-apply-status")).toBeDisabled();
  await page.locator("#logout-button").click();
  await expect(page.locator("#auth-view")).toBeVisible();
  await signIn(page, accountB);
  await expect(page.locator("#bulk-selection-count")).toHaveText("0 selected");
  expect(await page.evaluate(() => Object.keys(localStorage))).toEqual([]);
  releaseBulk();
  await expectMaskedHeaderEmail(page, accountB.email);
  await expect(page.locator("#task-list")).not.toContainText(title);
  await page.unroute("**/api/tasks/bulk");
  const pool = await database();
  const { rows } = await pool.query(
    `SELECT t.status, t.version,
            (SELECT count(*)::int FROM app.task_activity a WHERE a.task_id=t.id AND a.user_id=t.user_id) AS events
     FROM app.tasks t WHERE t.id=$1`,
    [taskBeforeSwitch.id],
  );
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ status: "TODO", events: 1 });
  expect(Number(rows[0].version)).toBe(taskBeforeSwitch.version);
});

test("select all applies only to currently rendered pages and pagination retains prior selection", async ({ page }) => {
  await signIn(page, accountPagination);
  for (let index = 0; index < 21; index += 1) {
    const response = await page.request.post("/api/tasks", {
      headers: { Origin: baseURL },
      data: { title: `${emailPrefix} page ${String(index).padStart(2, "0")}` },
    });
    expect(response.status()).toBe(201);
  }
  await page.reload();
  await expect(page.locator("#task-list .task-item")).toHaveCount(20);
  await page.locator("#select-all-visible").check();
  await expect(page.locator("#bulk-selection-count")).toHaveText("20 selected");
  await page.locator("#load-more-button").click();
  await expect(page.locator("#task-list .task-item")).toHaveCount(21);
  await expect(page.locator("#bulk-selection-count")).toHaveText("20 selected");
  await expect(page.locator('#task-list input[data-action="select-task"]:not(:checked)')).toHaveCount(1);
  await page.locator("#select-all-visible").check();
  await page.locator("#select-all-visible").uncheck();
  await expect(page.locator("#bulk-selection-count")).toHaveText("0 selected");
});
