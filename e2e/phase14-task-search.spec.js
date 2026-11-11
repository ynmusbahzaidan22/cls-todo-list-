import { randomUUID } from "node:crypto";
import { Pool } from "pg";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { getTestDatabaseURL, registerEmailRateLimitCleanup } from "./test-database.js";

const testDatabaseURL = getTestDatabaseURL();
test.describe.configure({ mode: "serial" });

const baseURL = process.env.PHASE8_BASE_URL || "http://127.0.0.1:18080";
const runId = randomUUID().replaceAll("-", "").slice(0, 16);
const prefix = `phase14-search-e2e-${runId}`;
const password = `Phase14-${randomUUID()}-${randomUUID()}`;
const accountA = { name: "Search User A", email: `${prefix}-a@example.test`, password };
const accountB = { name: "Search User B", email: `${prefix}-b@example.test`, password };
const testClientIP = registerEmailRateLimitCleanup(test, [accountA.email, accountB.email]);
test.use({ extraHTTPHeaders: { "X-Forwarded-For": testClientIP } });
let pool;

async function database() {
  if (pool) return pool;
  const connectionString = testDatabaseURL;
  pool = new Pool({ connectionString, max: 1 });
  return pool;
}

async function register(account) {
  const response = await fetch(`${baseURL}/api/auth/register`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Origin: baseURL, "X-Forwarded-For": testClientIP },
    body: JSON.stringify(account),
  });
  if (response.status !== 202) throw new Error(`Could not create isolated Phase 14 account (${response.status}).`);
}

async function signIn(page, account) {
  await page.goto("/");
  await expect(page.locator("#auth-view")).toBeVisible();
  await page.locator("#login-email").fill(account.email);
  await page.locator("#login-password").fill(account.password);
  await page.locator("#login-submit").click();
  await expect(page.locator("#todo-view")).toBeVisible();
  await expect(page.locator("#signed-in-email")).toHaveText(account.email);
}

async function openRecentlyDeleted(page) {
  const viewAll = page.locator("#open-deleted-view");
  if (await viewAll.isVisible()) await viewAll.click();
  else await page.goto("/#recently-deleted");
  await expect(page.locator("#recently-deleted-section")).toBeVisible();
}

async function createTask(page, title, description = "", status = "TODO") {
  const response = await page.request.post(`${baseURL}/api/tasks`, {
    headers: { Origin: baseURL },
    data: { title, description, status },
  });
  expect(response.status()).toBe(201);
  return await response.json();
}

function taskRow(page, title) {
  const escaped = new RegExp(`^${title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`);
  return page.locator("#task-list .task-item").filter({ has: page.locator(".task-content h3", { hasText: escaped }) });
}

test.beforeAll(async () => {
  await register(accountA);
  const db = await database();
  const result = await db.query(
    `INSERT INTO app.users (email, password_hash)
     SELECT $1, password_hash FROM app.users WHERE email = $2`,
    [accountB.email, accountA.email],
  );
  if (result.rowCount !== 1) throw new Error("Could not create the isolated second Phase 14 account.");
});

test.afterAll(async () => {
  try {
    const db = await database();
    await db.query("DELETE FROM app.users WHERE email LIKE $1", [`${prefix}%`]);
    const { rows } = await db.query("SELECT count(*)::int AS count FROM app.users WHERE email LIKE $1", [`${prefix}%`]);
    if (rows[0].count !== 0) throw new Error(`Phase 14 cleanup left ${rows[0].count} test accounts.`);
  } finally {
    if (pool) await pool.end();
  }
});

test("searches title and description, combines status, clears filters, and announces no matches", async ({ page }) => {
  const title = `${prefix} Needle lantern`;
  const description = `${prefix} Reference card`;
  const upper = `${prefix} NEEDLE upper`;
  await signIn(page, accountA);
  await createTask(page, title, "warm light", "TODO");
  await createTask(page, description, "a partial needle in the description", "IN_PROGRESS");
  await createTask(page, upper, "case insensitive", "DONE");
  await page.reload();

  const search = page.getByRole("searchbox", { name: "Search tasks" });
  const status = page.getByLabel("Status", { exact: true });
  await search.fill("needle");
  await expect(taskRow(page, title)).toBeVisible();
  await expect(taskRow(page, description)).toBeVisible();
  await expect(taskRow(page, upper)).toBeVisible();
  await expect(page.locator("#task-list .task-item")).toHaveCount(3);

  await page.locator("#task-filter-toggle").click();
  await status.selectOption("IN_PROGRESS");
  await expect(page.locator("#task-list .task-item")).toHaveCount(1);
  await expect(taskRow(page, description)).toBeVisible();
  await expect(taskRow(page, title)).toHaveCount(0);

  await search.fill("no such task");
  await expect(page.locator("#empty-state-search-status")).toBeVisible();
  await page.getByRole("button", { name: "Clear filters" }).click();
  await expect(search).toHaveValue("");
  await expect(status).toHaveValue("");
  await expect(taskRow(page, title)).toBeVisible();
});

test("keeps filters across pages, clears bulk selection on filter changes, and excludes Recently Deleted", async ({ page }) => {
  await signIn(page, accountA);
  const titles = Array.from({ length: 22 }, (_, index) => `${prefix} paged ${String(index + 1).padStart(2, "0")}`);
  for (const title of titles) await createTask(page, title, "pagination query", "TODO");
  const deletedTitle = `${prefix} deleted needle`;
  const deleted = await createTask(page, deletedTitle, "only in recovery", "TODO");
  const deleteResponse = await page.request.delete(`${baseURL}/api/tasks/${deleted.id}`, {
    headers: { Origin: baseURL }, data: { version: deleted.version },
  });
  expect(deleteResponse.status()).toBe(200);
  await page.reload();

  const search = page.getByRole("searchbox", { name: "Search tasks" });
  await search.fill(`${prefix} paged`);
  await expect(page.locator("#task-list .task-item")).toHaveCount(20);
  await page.locator("#task-list .task-item").first().getByRole("checkbox").check();
  await expect(page.locator("#bulk-selection-count")).toHaveText("1 selected");
  await page.getByRole("button", { name: "Load more tasks" }).click();
  await expect(page.locator("#task-list .task-item")).toHaveCount(22);
  await expect(page.locator("#bulk-selection-count")).toHaveText("1 selected");

  await search.fill(`${prefix} paged 01`);
  await expect(page.locator("#task-list .task-item")).toHaveCount(1);
  await expect(page.locator("#bulk-selection-count")).toHaveText("0 selected");
  await expect(page.locator('#task-list input[data-action="select-task"]')).not.toBeChecked();
  await expect(taskRow(page, deletedTitle)).toHaveCount(0);
  await openRecentlyDeleted(page);
  await expect(page.locator("#deleted-task-list")).toContainText(deletedTitle);
});

test("discards stale responses and offers retry after a filtered request error", async ({ page }) => {
  await signIn(page, accountA);
  const search = page.getByRole("searchbox", { name: "Search tasks" });
  let slowRequestSeen = false;
  let slowResponseCompleted = false;
  await page.route("**/api/tasks?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (query.get("q") === "phase14-slow-response") {
      slowRequestSeen = true;
      await new Promise((resolve) => setTimeout(resolve, 900));
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [{
          id: "00000000-0000-4000-8000-000000000099", title: "stale response task", description: "",
          status: "TODO", version: 1, created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
          completed_at: null, deleted_at: null,
        }], next_cursor: null }),
      });
      slowResponseCompleted = true;
      return;
    }
    if (query.get("q") === "phase14-retry") {
      await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: { code: "internal_error" } }) });
      return;
    }
    await route.continue();
  });

  await search.fill("phase14-slow-response");
  await expect.poll(() => slowRequestSeen).toBe(true);
  await search.fill("phase14-newer-query");
  await expect(page.locator("#empty-state-search")).toBeVisible();
  await expect.poll(() => slowResponseCompleted).toBe(true);
  await expect(page.locator("#task-list")).not.toContainText("stale response task");
  await search.fill("phase14-retry");
  await expect(page.locator("#task-load-error")).toBeVisible();
  await page.unroute("**/api/tasks?*");
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.locator("#empty-state-search")).toBeVisible();
});

test("clears search across accounts, keeps deleted tasks separate, and remains accessible responsively", async ({ page }) => {
  await signIn(page, accountA);
  const title = `${prefix} account A private search`;
  await createTask(page, title, "private details", "TODO");
  const deletedTitle = `${prefix} deleted separate`;
  const deleted = await createTask(page, deletedTitle, "recovery only", "TODO");
  const deletion = await page.request.delete(`${baseURL}/api/tasks/${deleted.id}`, {
    headers: { Origin: baseURL }, data: { version: deleted.version },
  });
  expect(deletion.status()).toBe(200);
  await page.reload();
  const search = page.getByRole("searchbox", { name: "Search tasks" });
  await search.fill(title);
  await expect(taskRow(page, title)).toBeVisible();
  await expect(taskRow(page, deletedTitle)).toHaveCount(0);

  for (const viewport of [{ width: 1440, height: 1000 }, { width: 768, height: 1000 }, { width: 375, height: 812 }]) {
    await page.setViewportSize(viewport);
    const axe = await new AxeBuilder({ page }).include(".task-section").analyze();
    expect(axe.violations.filter((issue) => ["critical", "serious"].includes(issue.impact))).toEqual([]);
  }
  await openRecentlyDeleted(page);
  await expect(page.locator("#deleted-task-list")).toContainText(deletedTitle);

  await page.locator("#logout-button").click();
  await expect(page.locator("#auth-view")).toBeVisible();
  await signIn(page, accountB);
  await expect(search).toHaveValue("");
  await expect(page.locator("#task-list")).not.toContainText(title);
  await openRecentlyDeleted(page);
  await expect(page.locator("#deleted-task-list")).not.toContainText(deletedTitle);
  await page.reload();
  await expect(page.locator("#task-list")).not.toContainText(title);
});

test("previews five recent deletions, opens the full recovery list, and hides when empty", async ({ page }) => {
  await signIn(page, accountB);
  const titles = Array.from({ length: 6 }, (_, index) => `${prefix} recovery preview ${index + 1}`);
  for (const title of titles) {
    const createdResponse = await page.request.post(`${baseURL}/api/tasks`, {
      headers: { Origin: baseURL }, data: { title },
    });
    expect(createdResponse.status()).toBe(201);
    const task = await createdResponse.json();
    const deletedResponse = await page.request.delete(`${baseURL}/api/tasks/${task.id}`, {
      headers: { Origin: baseURL }, data: { version: task.version },
    });
    expect(deletedResponse.status()).toBe(200);
  }

  await page.reload();
  await expect(page.locator("#deleted-preview-section")).toBeVisible();
  await expect(page.locator("#deleted-task-count")).toHaveText("6");
  await expect(page.locator("#deleted-preview-list .deleted-task-item")).toHaveCount(5);
  await page.locator("#open-deleted-view").click();
  await expect(page.locator("#recently-deleted-section")).toBeVisible();
  await expect(page.locator("#deleted-task-list .deleted-task-item")).toHaveCount(6);
  await page.locator("#deleted-back-to-tasks").click();

  for (let remaining = 6; remaining > 0; remaining -= 1) {
    const firstDeletedRow = page.locator("#deleted-preview-list .deleted-task-item").first();
    await expect(firstDeletedRow).toBeVisible();
    await firstDeletedRow.getByRole("button", { name: /^Restore / }).click();
    if (remaining > 1) await expect(page.locator("#deleted-task-count")).toHaveText(String(remaining - 1));
  }
  await expect(page.locator("#deleted-preview-section")).toBeHidden();
});

test("groups recent completions, keeps them available for Undo, and expands filtered results", async ({ page }) => {
  await signIn(page, accountB);
  const title = `${prefix} recent completion`;
  const createdResponse = await page.request.post(`${baseURL}/api/tasks`, {
    headers: { Origin: baseURL }, data: { title },
  });
  expect(createdResponse.status()).toBe(201);
  const task = await createdResponse.json();
  await page.reload();

  await page.getByRole("searchbox", { name: "Search tasks" }).fill(title);
  const row = taskRow(page, title);
  await row.locator('select[data-action="status"]').selectOption("DONE");
  await expect(page.locator("#recently-completed-group")).toBeVisible();
  await expect(page.locator("#recently-completed-count")).toHaveText("1");
  await expect(page.locator("#task-list > li.task-item:not(#recently-completed-group)")).toHaveCount(0);
  const completedRow = page.locator("#recently-completed-task-list .task-item").filter({
    has: page.locator(".task-content h3", { hasText: title }),
  });
  await expect(completedRow).toBeVisible();
  await expect(page.locator("#task-shown-count")).toHaveText("0");
  await expect(page.locator("#task-completed-count")).toHaveText("1");

  await completedRow.getByRole("button", { name: new RegExp(`View details and activity for ${title}`) }).click();
  await expect(page.locator("#activity-dialog")).toBeVisible();
  await expect(page.locator("#activity-list")).toContainText("Status changed");
  await page.locator("#activity-close-button").click();

  await page.locator("#recently-completed-details > summary").click();
  await expect(page.locator("#recently-completed-details")).toHaveJSProperty("open", true);
  await page.locator("#select-all-visible").check();
  await expect(page.locator("#bulk-selection-count")).toHaveText("1 selected");
  await page.locator("#recently-completed-details > summary").click();
  await expect(page.locator("#recently-completed-details")).toHaveJSProperty("open", true);
  await page.locator("#task-filter-toggle").click();
  await page.getByRole("button", { name: "Clear filters" }).click();
  await page.locator("#recently-completed-details > summary").click();
  await expect(page.locator("#bulk-selection-count")).toHaveText("0 selected");
});
