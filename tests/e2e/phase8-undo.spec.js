import AxeBuilder from "@axe-core/playwright";
import { createHash, randomUUID } from "node:crypto";
import { Pool } from "pg";
import { expect, test } from "@playwright/test";
import { getTestDatabaseURL, registerEmailRateLimitCleanup } from "./test-database.js";
import { expectMaskedHeaderEmail } from "../e2e-support/header-email.js";

const testDatabaseURL = getTestDatabaseURL();
test.describe.configure({ mode: "serial" });

const baseURL = process.env.PHASE8_BASE_URL || "http://127.0.0.1:18080";
const runId = randomUUID().replaceAll("-", "").slice(0, 16);
const emailPrefix = `phase8-e2e-${runId}`;
const password = `Phase8-${randomUUID()}-${randomUUID()}`;
const accountA = { name: "Undo User A", email: `${emailPrefix}-a@example.test`, password };
const accountB = { name: "Undo User B", email: `${emailPrefix}-b@example.test`, password };
const accountFor = (label) => ({ name: `Undo ${label}`, email: `${emailPrefix}-${label}@example.test`, password });
const accountExpired = accountFor("expired");
const accountStale = accountFor("stale");
const accountConcurrent = accountFor("concurrent");
const accountSwitchA = accountFor("switch-a");
const accountDuplicate = accountFor("duplicate");
const accountSession = accountFor("session");
const accountServerError = accountFor("server-error");
const testClientIP = registerEmailRateLimitCleanup(test, [accountA, accountB, accountExpired, accountStale, accountConcurrent, accountSwitchA, accountDuplicate, accountSession, accountServerError]
  .map((account) => account.email).concat(`${emailPrefix}-ui@example.test`));
test.use({ extraHTTPHeaders: { "X-Forwarded-For": testClientIP } });
let databasePool;

async function createAccount(account) {
  const response = await fetch(`${baseURL}/api/auth/register`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Origin: baseURL,
      "X-Forwarded-For": testClientIP,
    },
    body: JSON.stringify(account),
  });
  if (response.status !== 202) {
    throw new Error(`Could not create isolated browser-test account (${response.status}).`);
  }
}

async function openSignIn(page, account) {
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

async function addTask(page, title, description = "") {
  await page.locator("#task-input").fill(title);
  if (description) {
    const options = page.locator("#task-options");
    if (!(await options.evaluate((element) => element.open))) await options.locator("summary").click();
    await page.locator("#task-description-input").fill(description);
  }
  await page.getByRole("button", { name: "Add Task" }).click();
  await expect(taskRow(page, title)).toBeVisible();
  await expect(page.locator("#task-feedback")).toContainText("Task added and saved.");
}

function taskRow(page, title) {
  const exactTitle = new RegExp(`^${title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`);
  return page.locator("#task-list .task-item").filter({
    has: page.locator(".task-content h3", { hasText: exactTitle }),
  });
}

async function editTask(page, oldTitle, newTitle, description) {
  const row = taskRow(page, oldTitle);
  await row.getByRole("button", { name: `View details and activity for ${oldTitle}` }).click();
  await page.locator("#activity-edit-task").click();
  await expect(row.locator('input[name="title"]')).toBeVisible();
  await row.locator('input[name="title"]').fill(newTitle);
  await row.locator('textarea[name="description"]').fill(description);
  await row.getByRole("button", { name: "Save changes" }).click();
  await expect(taskRow(page, newTitle)).toBeVisible();
}

async function deleteTaskFromDetails(page, title) {
  await taskRow(page, title).getByRole("button", { name: `View details and activity for ${title}` }).click();
  await expect(page.locator("#activity-dialog")).toBeVisible();
  await page.locator("#activity-delete-task").click();
  await expect(taskRow(page, title)).toHaveCount(0);
}

async function openHistory(page, title) {
  const active = taskRow(page, title);
  const deleted = page.locator("#deleted-task-list .task-item").filter({ hasText: title });
  if (await active.isVisible()) {
    await active.getByRole("button", { name: `View details and activity for ${title}` }).click();
  } else if (await active.count()) {
    const recentDetails = page.locator("#recently-completed-details");
    if (await recentDetails.count() && !(await recentDetails.evaluate((details) => details.open))) {
      await recentDetails.locator("summary").click();
    }
    if (await active.isVisible()) {
      await active.getByRole("button", { name: `View details and activity for ${title}` }).click();
    } else {
      await openRecentlyDeleted(page);
      await expect(deleted).toBeVisible();
      await deleted.getByRole("button", { name: "View activity history for " + title }).click();
    }
  } else {
    if (!(await deleted.isVisible())) {
      await openRecentlyDeleted(page);
    }
    await expect(deleted).toBeVisible();
    await deleted.getByRole("button", { name: "View activity history for " + title }).click();
  }
  await expect(page.locator("#activity-dialog")).toBeVisible();
  await expect(page.locator("#activity-loading")).toBeHidden();
}

function activityEvent(page, label, detail = "") {
  let event = page.locator("#activity-list .activity-event").filter({ hasText: label });
  if (detail) event = event.filter({ hasText: detail });
  return event;
}

async function database() {
  if (databasePool) return databasePool;
  const connectionString = testDatabaseURL;
  databasePool = new Pool({ connectionString, max: 1 });
  return databasePool;
}

test.beforeAll(async () => {
  await createAccount(accountA);
  await createAccount(accountB);

  // Clone the seed Argon2id hash into uniquely named disposable accounts so
  // repeated UI logins do not trip the app's intentional per-email rate limit.
  const pool = await database();
  for (const account of [accountExpired, accountStale, accountConcurrent, accountSwitchA, accountDuplicate, accountSession, accountServerError]) {
    const result = await pool.query(
      `INSERT INTO app.users (email, password_hash)
       SELECT $1, password_hash FROM app.users WHERE email = $2`,
      [account.email, accountA.email],
    );
    if (result.rowCount !== 1) throw new Error("Could not create an isolated browser-test account.");
  }
});

test.afterAll(async () => {
  if (!databasePool) databasePool = await database();
  try {
    // These accounts use a per-run random prefix; cascading FKs remove only their test data.
    await databasePool.query("DELETE FROM app.users WHERE email LIKE $1", [`${emailPrefix}%`]);
  } finally {
    await databasePool.end();
  }
});

test("registers through the UI and verifies validation, keyboard access, loading and retry", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("#auth-view")).toBeVisible();
  await page.getByRole("button", { name: "Create account" }).click();
  await page.locator("#register-submit").click();
  await expect(page.locator("#register-name-error")).toHaveText("Enter a name with 1 to 100 characters.");
  await expect(page.locator("#register-email-error")).toHaveText("Enter your email address.");
  await page.locator("#register-name").fill("Browser Test User");
  await page.locator("#register-email").fill(`${emailPrefix}-ui@example.test`);
  await page.locator("#register-password").fill(password);
  await page.locator("#register-confirm-password").fill(password);
  await page.locator("#register-submit").click();
  await expect(page.locator("#auth-feedback")).toContainText("If an account can be created, you can now sign in.");

  await page.locator("#login-email").fill(`${emailPrefix}-ui@example.test`);
  await page.locator("#login-password").fill(password);
  await page.locator("#login-submit").click();
  await expect(page.locator("#todo-view")).toBeVisible();

  // Hold the initial list request long enough to verify the announced loading state.
  let releaseList;
  const listGate = new Promise((resolve) => { releaseList = resolve; });
  let firstListRequest = true;
  await page.route("**/api/tasks?*", async (route) => {
    if (firstListRequest && route.request().method() === "GET") {
      firstListRequest = false;
      await listGate;
    }
    await route.continue();
  });
  await page.reload();
  await expect(page.locator("#task-loading")).toBeVisible();
  releaseList();
  await expect(page.locator("#task-loading")).toBeHidden();
  await page.unrouteAll();

  // A recoverable network failure displays an error and the retry control restores the list.
  let abortNextList = true;
  await page.route("**/api/tasks?*", async (route) => {
    if (abortNextList && route.request().method() === "GET") {
      abortNextList = false;
      await route.abort("failed");
      return;
    }
    await route.continue();
  });
  await page.reload();
  await expect(page.locator("#task-load-error")).toBeVisible();
  await page.locator("#retry-task-load-button").click();
  await expect(page.locator("#task-load-error")).toBeHidden();
  await page.unrouteAll();
});

test("undoes title, description, status and deletion; preserves history and explains unavailable actions", async ({ page }) => {
  await openSignIn(page, accountA);
  const originalTitle = `${emailPrefix} undo workflow`;
  await addTask(page, originalTitle, "Description before edit");
  await editTask(page, originalTitle, `${originalTitle} updated`, "Description before edit");
  const updatedTitle = `${originalTitle} updated`;
  let releaseHistory;
  const historyGate = new Promise((resolve) => { releaseHistory = resolve; });
  await page.route("**/activity?*", async (route) => {
    if (route.request().method() === "GET") await historyGate;
    await route.continue();
  });
  await taskRow(page, updatedTitle).getByRole("button", { name: `View details and activity for ${updatedTitle}` }).click();
  await expect(page.locator("#activity-dialog")).toBeVisible();
  await expect(page.locator("#activity-loading")).toBeVisible();
  releaseHistory();
  await expect(page.locator("#activity-loading")).toBeHidden();
  await page.unroute("**/activity?*");

  let titleEvent = activityEvent(page, "Title updated", updatedTitle);
  await expect(titleEvent.locator(".activity-event-actor")).toHaveText("You");
  await expect(titleEvent.getByRole("button", { name: "Undo title updated" })).toBeVisible();
  let releaseUndo;
  const undoGate = new Promise((resolve) => { releaseUndo = resolve; });
  let undoRequests = 0;
  await page.route("**/undo", async (route) => {
    if (route.request().method() === "POST") {
      undoRequests += 1;
      await undoGate;
    }
    await route.continue();
  });
  const titleUndo = titleEvent.getByRole("button", { name: "Undo title updated" });
  const titleUndoClick = titleUndo.click();
  const pendingUndo = page.locator('#activity-list button[data-action="undo-activity"]');
  await expect(pendingUndo).toBeDisabled();
  await pendingUndo.evaluate((button) => button.click());
  expect(undoRequests).toBe(1);
  releaseUndo();
  await titleUndoClick;
  await page.unroute("**/undo");
  await expect(page.locator("#activity-action-feedback")).toContainText("original history was preserved");
  await expect(taskRow(page, originalTitle)).toBeVisible();
  await expect(activityEvent(page, "Previous action undone", "Undid title change")).toBeVisible();
  await expect(activityEvent(page, "Previous action undone").getByRole("button", { name: /Undo/i })).toHaveCount(0);
  await expect(activityEvent(page, "Task created").getByText("Undo is not supported for this action.")).toBeVisible();

  await page.locator("#activity-close-button").click();
  await editTask(page, originalTitle, originalTitle, "Description after edit");
  await openHistory(page, originalTitle);
  const descriptionEvent = activityEvent(page, "Description updated", "Description after edit");
  await descriptionEvent.getByRole("button", { name: "Undo description updated" }).click();
  await expect(page.locator("#activity-action-feedback")).toContainText("Undo completed");
  await expect(taskRow(page, originalTitle).locator(".task-description")).toHaveText("Description before edit");

  await page.locator("#activity-close-button").click();
  const statusSelect = taskRow(page, originalTitle).locator('select[data-action="status"]');
  await statusSelect.selectOption("DONE");
  await expect(taskRow(page, originalTitle).locator(".status-badge")).toHaveText("Done");
  await openHistory(page, originalTitle);
  const statusEvent = activityEvent(page, "Status changed", "DONE");
  await statusEvent.getByRole("button", { name: "Undo status changed" }).click();
  await expect(taskRow(page, originalTitle).locator(".status-badge")).toHaveText("To do");

  await page.locator("#activity-close-button").click();
  page.once("dialog", async (dialog) => {
    expect(dialog.message()).toContain(`Delete “${originalTitle}”?`);
    await dialog.accept();
  });
  await deleteTaskFromDetails(page, originalTitle);
  await expect(taskRow(page, originalTitle)).toHaveCount(0);
  await openHistory(page, originalTitle);
  const deletionEvent = activityEvent(page, "Task deleted");
  await deletionEvent.getByRole("button", { name: "Undo task deleted" }).click();
  await expect(taskRow(page, originalTitle)).toBeVisible();
  await expect(page.locator("#activity-action-feedback")).toContainText("original history was preserved");
  await expect(activityEvent(page, "Previous action undone", "Undid deletion")).toBeVisible();

  // The same deletion undo cannot be used twice, and the server-provided reason is visible.
  await expect(activityEvent(page, "Task deleted").getByText("This action has already been undone.")).toBeVisible();
});

test("shows expired deletion as unavailable and keeps the dialog keyboard accessible at desktop, tablet and mobile", async ({ page }) => {
  await openSignIn(page, accountExpired);
  const title = `${emailPrefix} expired deletion`;
  await addTask(page, title);
  page.once("dialog", async (dialog) => {
    expect(dialog.message()).toContain(`Delete “${title}”?`);
    await dialog.accept();
  });
  await deleteTaskFromDetails(page, title);
  const deletedListResponsePromise = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return response.request().method() === "GET" && url.pathname === "/api/tasks/deleted";
  });
  await openRecentlyDeleted(page);
  const deletedListResponse = await deletedListResponsePromise;
  expect(deletedListResponse.status()).toBe(200);
  const deletedList = await deletedListResponse.json();
  expect(deletedList.items.some((item) => item.title === title)).toBe(true);
  await expect(page.locator("#deleted-task-list .task-item").filter({ hasText: title })).toBeVisible();

  const pool = await database();
  const expired = await pool.query(
    `WITH expired AS (
       UPDATE app.tasks t
          SET deleted_at = clock_timestamp() - interval '25 hours'
         FROM app.users u
        WHERE t.user_id = u.id AND u.email = $1 AND t.title = $2
        RETURNING t.id, t.user_id, t.deleted_at
     )
     UPDATE app.task_activity a
        SET new_value = jsonb_set(a.new_value, '{deleted_at}', to_jsonb(expired.deleted_at))
       FROM expired
      WHERE a.task_id = expired.id AND a.user_id = expired.user_id AND a.event_type = 'TASK_DELETED'
      RETURNING a.task_id`,
    [accountExpired.email, title],
  );
  expect(expired.rowCount).toBe(1);

  await openHistory(page, title);
  const deletion = activityEvent(page, "Task deleted");
  await expect(deletion.getByRole("button", { name: /Undo/i })).toHaveCount(0);
  await expect(deletion.getByText("The 24-hour Undo window has expired.")).toBeVisible();
  await expect(page.locator("#activity-close-button")).toBeFocused();

  for (const viewport of [{ width: 1440, height: 1000 }, { width: 768, height: 1024 }, { width: 375, height: 812 }]) {
    await page.setViewportSize(viewport);
    const hasHorizontalOverflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
    expect(hasHorizontalOverflow, `horizontal overflow at ${viewport.width}px`).toBe(false);
    await expect(page.locator("#activity-dialog")).toBeVisible();
  }

  const accessibility = await new AxeBuilder({ page }).include("#activity-dialog").analyze();
  const seriousAccessibility = accessibility.violations.filter((violation) => ["critical", "serious"].includes(violation.impact));
  const accessibilityDetails = seriousAccessibility.flatMap((violation) => violation.nodes.map((node) => ({
    rule: violation.id,
    impact: node.impact,
    target: node.target,
    message: node.any?.[0]?.message,
  })));
  expect(seriousAccessibility, JSON.stringify(accessibilityDetails)).toEqual([]);
  await page.keyboard.press("Escape");
  await expect(page.locator("#activity-dialog")).toBeHidden();
});

test("two tabs reject stale Undo, explain the conflict, and refresh task and history", async ({ context, page }) => {
  await openSignIn(page, accountStale);
  const before = `${emailPrefix} stale base`;
  const firstChange = `${emailPrefix} stale first`;
  const newer = `${emailPrefix} stale newest`;
  await addTask(page, before);
  await editTask(page, before, firstChange, "");
  await openHistory(page, firstChange);
  const staleEvent = activityEvent(page, "Title updated", firstChange);
  await expect(staleEvent.getByRole("button", { name: "Undo title updated" })).toBeVisible();

  const secondTab = await context.newPage();
  await secondTab.goto("/");
  await expect(secondTab.locator("#todo-view")).toBeVisible();
  await expect(taskRow(secondTab, firstChange)).toBeVisible();
  await editTask(secondTab, firstChange, newer, "");

  await staleEvent.getByRole("button", { name: "Undo title updated" }).click();
  await expect(page.locator("#activity-action-feedback")).toContainText("The task changed after this history was loaded");
  await expect(taskRow(page, newer)).toBeVisible();
  await expect(activityEvent(page, "Title updated", newer)).toBeVisible();
  const staleHistoryEvent = activityEvent(page, "Title updated", `From “${before}” to “${firstChange}”`);
  await expect(staleHistoryEvent.getByRole("button", { name: /Undo/i })).toHaveCount(0);
  await expect(staleHistoryEvent.getByText("Undo is unavailable because this field changed later.")).toBeVisible();
  await expect(activityEvent(page, "Previous action undone").filter({ hasText: firstChange })).toHaveCount(0);

  await secondTab.close();
});

test("only one of two concurrent HTTP Undo requests succeeds", async ({ page }) => {
  await openSignIn(page, accountConcurrent);
  const original = `${emailPrefix} concurrent base`;
  const changed = `${emailPrefix} concurrent updated`;
  await addTask(page, original);
  await editTask(page, original, changed, "");

  const taskResponse = await page.request.get("/api/tasks?limit=100");
  expect(taskResponse.ok()).toBeTruthy();
  const tasks = await taskResponse.json();
  const target = tasks.items.find((item) => item.title === changed);
  expect(target).toBeTruthy();
  const activityResponse = await page.request.get(`/api/tasks/${target.id}/activity?limit=100`);
  expect(activityResponse.ok()).toBeTruthy();
  const activity = await activityResponse.json();
  const event = activity.items.find((item) => item.event_type === "TASK_TITLE_UPDATED");
  expect(event?.undo_available).toBe(true);

  const undoPath = `/api/tasks/${target.id}/activity/${event.id}/undo`;
  const requests = await Promise.all([
    page.request.post(undoPath, { data: { version: event.undo_version }, headers: { Origin: baseURL } }),
    page.request.post(undoPath, { data: { version: event.undo_version }, headers: { Origin: baseURL } }),
  ]);
  expect(requests.map((response) => response.status()).sort()).toEqual([200, 409]);

  const finalTask = await page.request.get(`/api/tasks/${target.id}`);
  expect((await finalTask.json()).title).toBe(original);
  const finalHistory = await (await page.request.get(`/api/tasks/${target.id}/activity?limit=100`)).json();
  expect(finalHistory.items.filter((item) => item.event_type === "TASK_UNDONE" && item.undo_of_event_id === event.id)).toHaveLength(1);
});

test("account switching clears stale data across tabs, refresh, history and direct API access", async ({ context, page }) => {
  await openSignIn(page, accountSwitchA);
  const privateTitle = `${emailPrefix} account A private task`;
  await addTask(page, privateTitle);
  await openHistory(page, privateTitle);
  await expect(page.locator("#activity-list .activity-event")).toContainText(["Task created"]);

  const secondTab = await context.newPage();
  await secondTab.goto("/");
  await expect(taskRow(secondTab, privateTitle)).toBeVisible();
  const taskData = await (await page.request.get("/api/tasks?limit=100")).json();
  const privateTask = taskData.items.find((item) => item.title === privateTitle);
  expect(privateTask).toBeTruthy();

  await page.locator("#activity-close-button").click();
  await page.locator("#logout-button").click();
  await expect(page.locator("#auth-view")).toBeVisible();
  await page.locator("#login-email").fill(accountB.email);
  await page.locator("#login-password").fill(accountB.password);
  const accountBLoginResponsePromise = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/auth/login");
  await page.locator("#login-submit").click();
  const accountBLoginResponse = await accountBLoginResponsePromise;
  expect(accountBLoginResponse.status()).toBe(200);
  await expectMaskedHeaderEmail(page, accountB.email);
  await expect(taskRow(page, privateTitle)).toHaveCount(0);

  await expectMaskedHeaderEmail(secondTab, accountB.email);
  await expect(taskRow(secondTab, privateTitle)).toHaveCount(0);
  await expect(secondTab.locator("#activity-dialog")).toBeHidden();

  const sessionCookie = (await context.cookies(baseURL)).find((cookie) => cookie.name === "todo_session");
  const directEndpoint = `/api/tasks/${privateTask.id}`;
  const secondTabOrigin = new URL(secondTab.url()).origin;
  expect(sessionCookie).toBeDefined();
  expect(sessionCookie.httpOnly).toBe(true);
  expect(secondTabOrigin).toBe(new URL(baseURL).origin);
  const browserIdentity = await secondTab.evaluate(async (expectedEmail) => {
    const response = await fetch("/api/auth/me", { credentials: "same-origin", cache: "no-store" });
    const user = response.ok ? await response.json() : null;
    return { status: response.status, matchesExpectedUser: user?.email === expectedEmail };
  }, accountB.email);
  expect(browserIdentity).toMatchObject({ status: 200, matchesExpectedUser: true });
  const contextTaskResponse = await context.request.get(directEndpoint);
  const pageTaskResponse = await secondTab.request.get(directEndpoint);
  expect(page.request).toBe(context.request);
  expect(secondTab.request).toBe(context.request);
  expect(contextTaskResponse.status()).toBe(404);
  expect(pageTaskResponse.status()).toBe(404);
  const forbiddenActivity = await secondTab.request.get(`/api/tasks/${privateTask.id}/activity`);
  expect(forbiddenActivity.status()).toBe(404);

  await secondTab.reload();
  await expectMaskedHeaderEmail(secondTab, accountB.email);
  await expect(taskRow(secondTab, privateTitle)).toHaveCount(0);
  expect(await secondTab.evaluate(() => Object.keys(localStorage))).toEqual([]);
  await secondTab.close();
});

test("prevents duplicate task submission while a save is pending", async ({ page }) => {
  await openSignIn(page, accountDuplicate);
  const title = `${emailPrefix} duplicate submission`;
  let releaseRequest;
  const requestGate = new Promise((resolve) => { releaseRequest = resolve; });
  let createRequests = 0;
  await page.route("**/api/tasks", async (route) => {
    if (route.request().method() === "POST") {
      createRequests += 1;
      await requestGate;
    }
    await route.continue();
  });

  await page.locator("#task-input").fill(title);
  const save = page.getByRole("button", { name: "Add Task" }).click();
  await expect(page.getByRole("button", { name: "Adding…" })).toBeVisible();
  expect(createRequests).toBe(1);
  await page.locator("#task-form").evaluate((form) => form.requestSubmit());
  expect(createRequests).toBe(1);
  releaseRequest();
  await save;
  await expect(taskRow(page, title)).toBeVisible();
  await expect(page.locator("#task-feedback")).toContainText("Task added and saved.");
  await page.unrouteAll();
});

test("reports a transient Undo server error without changing the task and permits a safe retry", async ({ page }) => {
  await openSignIn(page, accountServerError);
  const original = `${emailPrefix} server error base`;
  const changed = `${emailPrefix} server error updated`;
  await addTask(page, original);
  await editTask(page, original, changed, "");
  await openHistory(page, changed);

  await page.route("**/undo", async (route) => {
    if (route.request().method() === "POST") {
      await route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ error: { code: "internal_error", message: "Request failed." } }),
      });
      return;
    }
    await route.continue();
  });
  const updateEvent = activityEvent(page, "Title updated", changed);
  await updateEvent.getByRole("button", { name: "Undo title updated" }).click();
  await expect(page.locator("#activity-action-feedback")).toContainText("The task service is temporarily unavailable");
  await expect(taskRow(page, changed)).toBeVisible();
  await expect(activityEvent(page, "Previous action undone")).toHaveCount(0);
  await page.unroute("**/undo");

  await activityEvent(page, "Title updated", changed).getByRole("button", { name: "Undo title updated" }).click();
  await expect(taskRow(page, original)).toBeVisible();
  await expect(page.locator("#activity-action-feedback")).toContainText("Undo completed");
});

test("server-expired sessions do not restore account data after a browser refresh", async ({ context, page }) => {
  await openSignIn(page, accountSession);
  const title = `${emailPrefix} session task`;
  await addTask(page, title);
  const cookie = (await context.cookies(baseURL)).find((item) => item.name === "todo_session");
  expect(cookie).toBeTruthy();
  const tokenHash = createHash("sha256").update(cookie.value).digest();
  const pool = await database();
  const expired = await pool.query(
    "UPDATE app.sessions SET idle_expires_at = created_at + interval '1 second' WHERE token_hash = $1 RETURNING idle_expires_at",
    [tokenHash],
  );
  expect(expired.rowCount).toBe(1);
  await new Promise((resolve) => setTimeout(resolve, Math.max(0, expired.rows[0].idle_expires_at.getTime() - Date.now()) + 50));
  await page.reload();
  await expect(page.locator("#auth-view")).toBeVisible();
  await expect(page.locator("#todo-view")).toBeHidden();
  await expect(page.getByText(title)).toHaveCount(0);
  expect(await page.evaluate(() => Object.keys(localStorage))).toEqual([]);
});
