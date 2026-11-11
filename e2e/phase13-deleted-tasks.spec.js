import { randomUUID } from "node:crypto";
import { Pool } from "pg";
import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { getTestDatabaseURL, registerEmailRateLimitCleanup } from "./test-database.js";

const testDatabaseURL = getTestDatabaseURL();
test.describe.configure({ mode: "serial" });

const baseURL = process.env.PHASE8_BASE_URL || "http://127.0.0.1:18080";
const runId = randomUUID().replaceAll("-", "").slice(0, 16);
const prefix = `phase13-deleted-e2e-${runId}`;
const password = `Phase13-${randomUUID()}-${randomUUID()}`;
const accountA = { name: "Deleted User A", email: `${prefix}-a@example.test`, password };
const accountB = { name: "Deleted User B", email: `${prefix}-b@example.test`, password };
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
  if (response.status !== 202) throw new Error(`Could not create isolated Phase 13 account (${response.status}).`);
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

async function createAndDelete(page, title) {
  const createdResponse = await page.request.post(`${baseURL}/api/tasks`, {
    headers: { Origin: baseURL },
    data: { title, description: "persisted recovery test" },
  });
  expect(createdResponse.status()).toBe(201);
  const created = await createdResponse.json();
  const deletedResponse = await page.request.delete(`${baseURL}/api/tasks/${created.id}`, {
    headers: { Origin: baseURL },
    data: { version: created.version },
  });
  expect(deletedResponse.status()).toBe(200);
  return await deletedResponse.json();
}

function deletedRow(page, title) {
  return page.locator("#deleted-task-list .deleted-task-item").filter({ hasText: title });
}

function deletedRowExact(page, title) {
  const escaped = title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return page.locator("#deleted-task-list .deleted-task-item").filter({
    has: page.locator(".task-content h3", { hasText: new RegExp(`^${escaped}$`) }),
  });
}

test.beforeAll(async () => {
  await register(accountA);
  const db = await database();
  const result = await db.query(
    `INSERT INTO app.users (email, password_hash)
     SELECT $1, password_hash FROM app.users WHERE email = $2`,
    [accountB.email, accountA.email],
  );
  if (result.rowCount !== 1) throw new Error("Could not create the isolated second Phase 13 account.");
});

test.afterAll(async () => {
  try {
    const db = await database();
    await db.query("DELETE FROM app.users WHERE email LIKE $1", [`${prefix}%`]);
    const { rows } = await db.query("SELECT count(*)::int AS count FROM app.users WHERE email LIKE $1", [`${prefix}%`]);
    if (rows[0].count !== 0) throw new Error(`Phase 13 cleanup left ${rows[0].count} test accounts.`);
  } finally {
    if (pool) await pool.end();
  }
});

test("retains deleted tasks after reload and pages through all recoverable tasks", async ({ page }) => {
  await signIn(page, accountA);
  const titles = Array.from({ length: 22 }, (_, index) => `${prefix} page ${String(index + 1).padStart(2, "0")}`);
  for (const title of titles) await createAndDelete(page, title);

  await page.reload();
  await expect(page.locator("#deleted-task-count")).toHaveText("22");
  await expect(page.locator("#deleted-preview-section")).toBeVisible();
  await expect(page.locator("#deleted-preview-list .deleted-task-item")).toHaveCount(5);
  await expect(page.locator("#open-deleted-view")).toBeVisible();
  await openRecentlyDeleted(page);
  await expect(page.locator("#recently-deleted-section")).toBeVisible();
  await expect(page.locator("#deleted-task-list .deleted-task-item")).toHaveCount(20);
  await page.getByRole("button", { name: "Load more deleted tasks" }).click();
  await expect(page.locator("#deleted-task-list .deleted-task-item")).toHaveCount(22);
  await expect(deletedRow(page, titles[0])).toContainText("Restorable for up to 24 hours");

  for (const viewport of [{ width: 1440, height: 1000 }, { width: 768, height: 1000 }, { width: 375, height: 812 }]) {
    await page.setViewportSize(viewport);
    const axe = await new AxeBuilder({ page }).include("#recently-deleted-section").analyze();
    expect(axe.violations.filter((issue) => ["critical", "serious"].includes(issue.impact))).toEqual([]);
  }

  await deletedRow(page, titles[0]).getByRole("button", { name: `Restore ${titles[0]}` }).click();
  await expect(deletedRow(page, titles[0])).toHaveCount(0);
  await expect(page.locator("#deleted-task-count")).toHaveText("21");
  await expect(page.locator("#task-list")).toContainText(titles[0]);
  await page.reload();
  await expect(deletedRow(page, titles[0])).toHaveCount(0);
});

test("isolates the durable deleted list when the signed-in account changes", async ({ page }) => {
  await signIn(page, accountA);
  const title = `${prefix} account A only`;
  await createAndDelete(page, title);
  await page.reload();
  await openRecentlyDeleted(page);
  await expect(deletedRow(page, title)).toBeVisible();

  await page.locator("#logout-button").click();
  await expect(page.locator("#auth-view")).toBeVisible();
  await page.route("**/api/tasks/deleted?*", (route) => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: { code: "internal_error" } }) }));
  await signIn(page, accountB);
  await openRecentlyDeleted(page);
  await expect(page.locator("#deleted-load-error")).toBeVisible();
  await page.unroute("**/api/tasks/deleted?*");
  await page.getByRole("button", { name: "Try again" }).last().click();
  await expect(page.locator("#deleted-empty")).toContainText("No tasks have been deleted");
  await expect(page.locator("#recently-deleted-section")).toBeVisible();
  await expect(deletedRow(page, title)).toHaveCount(0);
  await page.reload();
  await expect(deletedRow(page, title)).toHaveCount(0);

  const foreignTask = await page.request.get(`${baseURL}/api/tasks/deleted`);
  expect(foreignTask.status()).toBe(200);
  expect(await foreignTask.json()).toMatchObject({ items: [], next_cursor: null });
});

test("does not list tasks beyond the 24-hour recovery window", async ({ page }) => {
  await signIn(page, accountA);
  const title = `${prefix} expired task`;
  const deleted = await createAndDelete(page, title);
  const db = await database();
  await db.query(
    `UPDATE app.tasks SET deleted_at = statement_timestamp() - interval '24 hours' - interval '1 second'
     WHERE id = $1 AND user_id = (SELECT id FROM app.users WHERE email = $2)`,
    [deleted.id, accountA.email],
  );

  await page.reload();
  await openRecentlyDeleted(page);
  await expect(page.locator("#deleted-empty")).toContainText("No tasks have been deleted");
  await expect(deletedRow(page, title)).toHaveCount(0);
  const restore = await page.request.post(`${baseURL}/api/tasks/${deleted.id}/restore`, {
    headers: { Origin: baseURL },
    data: { version: deleted.version },
  });
  expect(restore.status()).toBe(409);
  expect(await restore.json()).toMatchObject({ error: { code: "recovery_expired" } });
});

test("refreshes stale deleted-task details after a conflicting restore attempt", async ({ page }) => {
  await signIn(page, accountA);
  const title = `${prefix} stale restore`;
  const deleted = await createAndDelete(page, title);
  await page.reload();
  await openRecentlyDeleted(page);
  const oldRow = deletedRow(page, title);
  await expect(oldRow).toBeVisible();

  const db = await database();
  const updatedTitle = `${title} updated elsewhere`;
  await db.query(
    `UPDATE app.tasks SET title = $1, version = version + 1, updated_at = clock_timestamp()
     WHERE id = $2 AND user_id = (SELECT id FROM app.users WHERE email = $3) AND deleted_at IS NOT NULL`,
    [updatedTitle, deleted.id, accountA.email],
  );

  await oldRow.getByRole("button", { name: `Restore ${title}` }).click();
  await expect(page.locator("#task-feedback")).toContainText("changed elsewhere");
  await expect(deletedRowExact(page, updatedTitle)).toBeVisible();
  await expect(deletedRowExact(page, title)).toHaveCount(0);
  await expect(deletedRowExact(page, updatedTitle).getByRole("button", { name: `Restore ${updatedTitle}` })).toBeEnabled();
  await expect(page.locator("#task-list")).not.toContainText(updatedTitle);
});
