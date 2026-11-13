import AxeBuilder from "@axe-core/playwright";
import { randomUUID } from "node:crypto";
import { Pool } from "pg";
import { expect, test } from "@playwright/test";
import { getTestDatabaseURL, registerEmailRateLimitCleanup } from "./test-database.js";
import { captureVisual } from "../e2e-support/capture-visual.js";

const databaseURL = getTestDatabaseURL();
test.describe.configure({ mode: "serial" });

const baseURL = process.env.PHASE8_BASE_URL || "http://127.0.0.1:18080";
const prefix = `admin-account-${randomUUID().replaceAll("-", "").slice(0, 18)}`;
const password = `AdminPhase2-${randomUUID()}-${randomUUID()}`;
const admin = { name: "Admin Phase 2 fixture", email: `${prefix}-admin@example.test`, password };
const user = { name: "Managed account fixture", email: `${prefix}-user@example.test`, password };
const testClientIP = registerEmailRateLimitCleanup(test, [admin.email, user.email]);
test.use({ extraHTTPHeaders: { "X-Forwarded-For": testClientIP } });
let pool;
let baselineActiveAdmins = 0;

async function register(account) {
  const response = await fetch(`${baseURL}/api/auth/register`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Origin: baseURL, "X-Forwarded-For": testClientIP },
    body: JSON.stringify(account),
  });
  if (response.status !== 202) throw new Error(`Could not create isolated Admin Phase 2 fixture (${response.status}).`);
}

async function signIn(page, account) {
  await page.goto("/");
  if (account === user) await captureVisual(page, "login.png");
  await page.locator("#login-email").fill(account.email);
  await page.locator("#login-password").fill(account.password);
  await page.locator("#login-submit").click();
  await expect(page.locator("#todo-view")).toBeVisible();
  if (account === user) {
    await expect(page.locator("#task-loading")).toBeHidden();
    await expect(page.locator("#template-empty")).toBeVisible();
    await captureVisual(page, "user-dashboard.png");
  }
}

function accountRow(page, email) {
  return page.locator(".admin-account-row").filter({ hasText: email });
}

async function findAccountRow(page, account) {
  await page.locator("#admin-user-search-input").fill(account.name);
  const row = accountRow(page, account.email);
  await expect(row).toBeVisible();
  return row;
}

test.beforeAll(async () => {
  pool = new Pool({ connectionString: databaseURL, max: 1 });
  const { rows: identity } = await pool.query("SELECT current_database() AS database_name, current_user AS role_name, current_setting('server_version') AS server_version");
  if (identity[0]?.database_name !== "todo_test" || identity[0]?.role_name !== "todo_test_admin" || !identity[0]?.server_version.startsWith("18.6")) {
    throw new Error("Admin Phase 2 E2E fixture requires the isolated todo_test administrator on PostgreSQL 18.6.");
  }
  const { rows: baseline } = await pool.query("SELECT active_count FROM app.active_admin_guard WHERE singleton");
  if (baseline.length !== 1) throw new Error("Admin Phase 2 E2E fixture could not verify the administrator guard.");
  baselineActiveAdmins = Number(baseline[0].active_count);
  const { rows: existing } = await pool.query("SELECT count(*)::int AS count FROM app.users WHERE email = ANY($1::text[])", [[admin.email, user.email]]);
  if (existing[0].count !== 0) throw new Error("Refusing to reuse a pre-existing Admin Phase 2 fixture identity.");

  await register(admin);
  await register(user);
  const promoted = await pool.query("UPDATE app.users SET role='ADMIN' WHERE email=$1 AND role='USER'", [admin.email]);
  if (promoted.rowCount !== 1) throw new Error("Could not promote the unique Admin Phase 2 test fixture.");
});

test.afterAll(async () => {
  if (!pool) return;
  const emails = [admin.email, user.email];
  try {
    await pool.query("BEGIN");
    const { rows } = await pool.query(
      "SELECT count(*)::int AS count FROM app.users WHERE email=ANY($1::text[]) AND role='ADMIN' AND status='ACTIVE'",
      [emails],
    );
    const activeFixtureAdmins = rows[0].count;
    if (activeFixtureAdmins > 0) {
      await pool.query("UPDATE app.active_admin_guard SET active_count=$1 WHERE singleton", [baselineActiveAdmins + activeFixtureAdmins + 1]);
    }
    await pool.query(
      "DELETE FROM app.admin_audit_events WHERE actor_user_id IN (SELECT id FROM app.users WHERE email=ANY($1::text[])) OR target_user_id IN (SELECT id FROM app.users WHERE email=ANY($1::text[]))",
      [emails],
    );
    await pool.query("DELETE FROM app.tasks WHERE user_id IN (SELECT id FROM app.users WHERE email=ANY($1::text[]))", [emails]);
    await pool.query("DELETE FROM app.sessions WHERE user_id IN (SELECT id FROM app.users WHERE email=ANY($1::text[]))", [emails]);
    await pool.query("DELETE FROM app.users WHERE email=ANY($1::text[])", [emails]);
    await pool.query("UPDATE app.active_admin_guard SET active_count=$1 WHERE singleton", [baselineActiveAdmins]);
    await pool.query("COMMIT");
    const { rows: leftovers } = await pool.query("SELECT count(*)::int AS count FROM app.users WHERE email=ANY($1::text[])", [emails]);
    if (leftovers[0].count !== 0) throw new Error("Admin Phase 2 E2E cleanup left a fixture account.");
  } catch (error) {
    await pool.query("ROLLBACK").catch(() => {});
    throw new Error("Admin Phase 2 E2E fixture cleanup or verification failed.", { cause: error });
  } finally {
    await pool.end();
    pool = undefined;
  }
});

test("Admin can manage roles, status, and sessions with protected and accessible controls", async ({ browser, page }) => {
  const userContext = await browser.newContext({ extraHTTPHeaders: { "X-Forwarded-For": testClientIP } });
  const userPage = await userContext.newPage();
  try {
    await signIn(userPage, user);
    await expect(userPage.locator("#admin-account-panel")).toBeHidden();

    await signIn(page, admin);
    const panel = page.locator("#admin-account-panel");
    await expect(panel).toBeVisible();
    await expect(page.locator("#admin-user-preview-loading")).toBeHidden();
    await expect(page.locator("#admin-task-preview-loading")).toBeHidden();
    await captureVisual(page, "admin-dashboard.png");
    await page.locator("#admin-view-all-tasks").click();
    await expect(page.locator("#admin-tasks-view")).toBeVisible();
    await expect(page.locator("#admin-task-loading")).toBeHidden();
    await captureVisual(page, "admin-tasks-read-only.png");
    await page.locator("#admin-tasks-back").click();
    await page.locator("#admin-view-all-users").click();
    await expect(page.locator("#admin-users-view")).toBeVisible();
    const ownRow = await findAccountRow(page, admin);
    await expect(page.locator("#admin-account-loading")).toBeHidden();
    await captureVisual(page, "admin-users.png");
    await expect(ownRow).toContainText("Your current account is protected from self-management.");
    await expect(ownRow.getByRole("button", { name: "Change role" })).toHaveCount(0);
    await expect(ownRow.getByRole("button", { name: "Disable account" })).toHaveCount(0);

    const userRow = await findAccountRow(page, user);
    await userRow.getByRole("combobox", { name: `Role for ${user.name}` }).selectOption("ADMIN");
    page.once("dialog", (dialog) => dialog.accept());
    await userRow.getByRole("button", { name: "Change role" }).click();
    await expect(userRow).toContainText("Role: ADMIN");

    await userPage.request.get(`${baseURL}/api/auth/me`).then((response) => expect(response.status()).toBe(401));
    await userRow.getByRole("combobox", { name: `Role for ${user.name}` }).selectOption("USER");
    page.once("dialog", (dialog) => dialog.accept());
    await userRow.getByRole("button", { name: "Change role" }).click();
    await expect(userRow).toContainText("Role: USER");

    await signIn(userPage, user);
    page.once("dialog", (dialog) => dialog.accept());
    await userRow.getByRole("button", { name: "Disable account" }).click();
    await expect(userRow).toContainText("Status: DISABLED");
    await userPage.request.get(`${baseURL}/api/auth/me`).then((response) => expect(response.status()).toBe(401));

    page.once("dialog", (dialog) => dialog.accept());
    await userRow.getByRole("button", { name: "Enable account" }).click();
    await expect(userRow).toContainText("Status: ACTIVE");
    await signIn(userPage, user);
    page.once("dialog", (dialog) => dialog.accept());
    await userRow.getByRole("button", { name: "Revoke sessions" }).click();
    await expect(page.locator("#admin-account-feedback")).toContainText("Revoked");
    await userPage.request.get(`${baseURL}/api/auth/me`).then((response) => expect(response.status()).toBe(401));

    await page.route("**/api/admin/users/*/status", (route) => route.fulfill({
      status: 409,
      contentType: "application/json",
      body: JSON.stringify({ error: "conflict" }),
    }));
    page.once("dialog", (dialog) => dialog.accept());
    await userRow.getByRole("button", { name: "Disable account" }).click();
    await expect(page.locator("#admin-account-feedback")).toContainText("final active administrator");
    await page.unroute("**/api/admin/users/*/status");

    await page.setViewportSize({ width: 390, height: 844 });
    await expect(panel).toBeVisible();
    const accessibility = await new AxeBuilder({ page }).include("#admin-account-panel").analyze();
    expect(accessibility.violations).toEqual([]);
    await captureVisual(page, "admin-mobile.png");
  } finally {
    await userContext.close();
  }
});
