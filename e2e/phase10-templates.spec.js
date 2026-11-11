import AxeBuilder from "@axe-core/playwright";
import { randomUUID } from "node:crypto";
import { Pool } from "pg";
import { expect, test } from "@playwright/test";
import { getTestDatabaseURL, registerEmailRateLimitCleanup } from "./test-database.js";

const testDatabaseURL = getTestDatabaseURL();
test.describe.configure({ mode: "serial" });

const baseURL = process.env.PHASE8_BASE_URL || "http://127.0.0.1:18080";
const prefix = `phase10-template-e2e-${randomUUID().replaceAll("-", "").slice(0, 14)}`;
const password = `Phase10-${randomUUID()}!`;
const accountA = { name: "Template User A", email: `${prefix}-a@example.test`, password };
const accountB = { name: "Template User B", email: `${prefix}-b@example.test`, password };
const accountC = { name: "Template User C", email: `${prefix}-c@example.test`, password };
const testClientIP = registerEmailRateLimitCleanup(test, [accountA.email, accountB.email, accountC.email]);
test.use({ extraHTTPHeaders: { "X-Forwarded-For": testClientIP } });
let pool;

async function database() {
  if (pool) return pool;
  const connectionString = testDatabaseURL;
  pool = new Pool({ connectionString, max: 1 });
  return pool;
}

async function register(account) {
  const response = await fetch(`${baseURL}/api/auth/register`, { method: "POST", headers: { "Content-Type": "application/json", Origin: baseURL, "X-Forwarded-For": testClientIP }, body: JSON.stringify(account) });
  if (response.status !== 202) throw new Error(`Could not create isolated Phase 10 account (${response.status}).`);
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

function templateCard(page, name) { return page.locator("#template-list .template-item").filter({ has: page.getByRole("heading", { name, exact: true }) }); }
function taskRow(page, title) { const exact = new RegExp(`^${title.replace(/[.*+?^${}()|[\\]\\]/g, "\\$&")}$`); return page.locator("#task-list .task-item").filter({ has: page.locator(".task-content h3", { hasText: exact }) }); }

async function useTemplate(page, name, mode = "incremental") {
  await templateCard(page, name).getByRole("button", { name: "View" }).click();
  await expect(page.locator("#template-detail-dialog")).toBeVisible();
  await page.locator(mode === "new_set" ? "#template-detail-new-set" : "#template-detail-apply").click();
  await expect(page.locator("#template-apply-dialog")).toBeVisible();
  await page.locator("#template-apply-confirm").click();
}

test.beforeAll(async () => {
  await register(accountA);
  const db = await database();
  for (const account of [accountB, accountC]) {
    const result = await db.query(`INSERT INTO app.users (email, password_hash) SELECT $1, password_hash FROM app.users WHERE email = $2`, [account.email, accountA.email]);
    if (result.rowCount !== 1) throw new Error("Could not create an isolated Phase 10 account.");
  }
});

test.afterAll(async () => {
  try {
    const db = await database();
    await db.query("DELETE FROM app.users WHERE email LIKE $1", [`${prefix}%`]);
    const { rows } = await db.query("SELECT count(*)::int AS count FROM app.users WHERE email LIKE $1", [`${prefix}%`]);
    if (rows[0].count !== 0) throw new Error(`Phase 10 cleanup left ${rows[0].count} test accounts.`);
  } finally { if (pool) await pool.end(); }
});

test("incrementally adds new template items and offers an explicit fresh set", async ({ page }) => {
  await signIn(page, accountA);
  const name = `${prefix} morning routine`;
  const first = `${prefix} prepare breakfast`;
  const second = `${prefix} pack lunch`;
  await page.getByRole("button", { name: "Create template" }).click();
  await page.locator("#template-name").fill(name);
  await page.locator("#template-description").fill("Before work");
  await page.locator("#add-template-item").click();
  await page.locator("#template-add-manual").click();
  await page.locator("#template-item-title-1").fill(first);
  await page.locator("#template-item-description-1").fill("Use fresh ingredients");
  await page.locator("#add-template-item").click();
  await page.locator("#template-add-manual").click();
  await page.locator("#template-item-title-2").fill(second);
  await page.locator("#template-save-button").click();
  await expect(templateCard(page, name)).toBeVisible();
  await expect(templateCard(page, name)).toContainText("2 tasks");
  await expect(templateCard(page, name).getByRole("button")).toHaveCount(1);

  await templateCard(page, name).getByRole("button", { name: "View" }).click();
  await expect(page.locator("#template-detail-dialog")).toContainText(first);
  await expect(page.locator("#template-detail-dialog")).toContainText(second);
  const detailAxe = await new AxeBuilder({ page }).include("#template-detail-dialog").analyze();
  expect(detailAxe.violations.filter((issue) => ["critical", "serious"].includes(issue.impact))).toEqual([]);
  await page.locator("#template-detail-edit").click();
  await page.locator("#template-name").fill(`${name} edited`);
  await page.locator("#template-save-button").click();
  await expect(templateCard(page, `${name} edited`)).toBeVisible();

  await templateCard(page, `${name} edited`).getByRole("button", { name: "View" }).click();
  await page.locator("#template-detail-apply").click();
  await expect(page.locator("#template-apply-description")).toContainText("not yet added to the current set");
  await page.locator("#template-apply-confirm").click();
  await expect(page.locator("#task-feedback")).toContainText("2 new template tasks were added");
  await expect(taskRow(page, first)).toBeVisible();
  await expect(taskRow(page, second)).toBeVisible();
  const createdBeforeEdit = await page.request.get("/api/tasks?limit=100").then((response) => response.json());
  const before = createdBeforeEdit.items.filter((item) => [first, second].includes(item.title));
  expect(before).toHaveLength(2);

  await templateCard(page, `${name} edited`).getByRole("button", { name: "View" }).click();
  await page.locator("#template-detail-edit").click();
  await page.locator("#template-item-title-1").fill(`${first} changed`);
  await page.locator("#add-template-item").click();
  await page.locator("#template-add-manual").click();
  const third = `${prefix} review calendar`;
  await page.locator("#template-item-title-3").fill(third);
  await page.locator("#template-save-button").click();
  await expect(taskRow(page, first)).toBeVisible();
  await expect(taskRow(page, `${first} changed`)).toHaveCount(0);
  await useTemplate(page, `${name} edited`);
  await expect(page.locator("#task-feedback")).toContainText("1 new template task was added");
  await expect(taskRow(page, third)).toBeVisible();
  await expect(taskRow(page, first)).toBeVisible();
  const after = await page.request.get("/api/tasks?limit=100").then((response) => response.json());
  expect(after.items.filter((item) => item.title === first)).toHaveLength(1);
  expect(after.items.filter((item) => item.title === `${first} changed`)).toHaveLength(0);
  expect(after.items.filter((item) => item.title === third)).toHaveLength(1);

  await useTemplate(page, `${name} edited`, "new_set");
  await expect(page.locator("#task-feedback")).toContainText("3 tasks created as a new set");
  const withFreshSet = await page.request.get("/api/tasks?limit=100").then((response) => response.json());
  expect(withFreshSet.items.filter((item) => item.title === first)).toHaveLength(1);
  expect(withFreshSet.items.filter((item) => item.title === `${first} changed`)).toHaveLength(1);
  expect(withFreshSet.items.filter((item) => item.title === second)).toHaveLength(2);
  expect(withFreshSet.items.filter((item) => item.title === third)).toHaveLength(2);
  for (const item of before) {
    const history = await page.request.get(`/api/tasks/${item.id}/activity`).then((response) => response.json());
    expect(history.items.map((event) => event.event_type)).toEqual(["TASK_CREATED"]);
  }
  await expect(page.locator("#task-feedback")).toContainText("3 tasks created as a new set");
});

test("isolates private templates and generated tasks between accounts", async ({ page }) => {
  await signIn(page, accountA);
  const name = `${prefix} private`; const title = `${prefix} private item`;
  await page.getByRole("button", { name: "Create template" }).click();
  await page.locator("#template-name").fill(name); await page.locator("#add-template-item").click(); await page.locator("#template-add-manual").click(); await page.locator("#template-item-title-1").fill(title); await page.locator("#template-save-button").click();
  await expect(templateCard(page, name)).toBeVisible();
  const templates = await page.request.get("/api/task-templates").then((response) => response.json());
  const template = templates.items.find((item) => item.name === name); expect(template).toBeTruthy();
  await useTemplate(page, name);
  await expect(taskRow(page, title)).toBeVisible();
  const taskData = await page.request.get("/api/tasks?limit=100").then((response) => response.json());
  const privateTask = taskData.items.find((item) => item.title === title); expect(privateTask).toBeTruthy();

  await page.locator("#logout-button").click(); await expect(page.locator("#auth-view")).toBeVisible(); await signIn(page, accountB);
  await expect(page.locator("#template-list")).not.toContainText(name);
  await expect(page.locator("#task-list")).not.toContainText(title);
  expect((await page.request.get(`/api/task-templates/${template.id}`)).status()).toBe(404);
  expect((await page.request.post(`/api/task-templates/${template.id}/apply`, { headers: { Origin: baseURL }, data: { version: template.version } })).status()).toBe(404);
  await page.reload(); await expect(page.locator("#signed-in-email")).toHaveText(accountB.email);
  await expect(page.locator("#template-list")).not.toContainText(name);
  await expect(page.locator("#task-list")).not.toContainText(title);
});

test("adds an existing task to a template from the searchable task picker", async ({ page }) => {
  await signIn(page, accountC);
  const existingTitle = `${prefix} copy into template`;
  const templateName = `${prefix} picked tasks`;
  await page.locator("#task-input").fill(existingTitle);
  await page.locator("#task-form button[type=submit]").click();
  await expect(taskRow(page, existingTitle)).toBeVisible();

  await page.getByRole("button", { name: "Create template" }).click();
  await page.locator("#template-name").fill(templateName);
  await page.locator("#add-template-item").click();
  await expect(page.locator("#template-task-picker")).toBeVisible();
  const pickerRow = page.locator("#template-picker-list li").filter({ hasText: existingTitle });
  await expect(pickerRow).toBeVisible();
  await pickerRow.getByRole("button", { name: `Add: ${existingTitle}` }).click();
  await expect(page.locator("#template-item-title-1")).toHaveValue(existingTitle);
  await expect(pickerRow.getByRole("button", { name: `Already added: ${existingTitle}` })).toBeDisabled();
  await expect(page.locator("#template-item-description-1")).toHaveValue("");
  await expect(page.locator("#template-item-priority-1")).toHaveValue("MEDIUM");
  await page.locator("#template-save-button").click();
  await expect(templateCard(page, templateName)).toContainText("1 task");
  await templateCard(page, templateName).getByRole("button", { name: "View" }).click();
  await expect(page.locator("#template-detail-items")).toContainText(existingTitle);
  await expect(page.locator("#template-detail-dialog")).toHaveJSProperty("scrollWidth", await page.locator("#template-detail-dialog").evaluate((dialog) => dialog.clientWidth));
});

test("prevents duplicate template submission and supports responsive keyboard access", async ({ page }) => {
  await signIn(page, accountA);
  const name = `${prefix} duplicate guard`;
  await page.getByRole("button", { name: "Create template" }).click(); await page.locator("#template-name").fill(name); await page.locator("#add-template-item").click(); await page.locator("#template-add-manual").click(); await page.locator("#template-item-title-1").fill(`${prefix} one task`);
  let requests = 0; let release;
  const gate = new Promise((resolve) => { release = resolve; });
  await page.route("**/api/task-templates", async (route) => { if (route.request().method() === "POST") { requests++; await gate; } await route.continue(); });
  await page.locator("#template-save-button").click(); await expect(page.locator("#template-save-button")).toBeDisabled();
  await page.locator("#template-save-button").evaluate((button) => button.click()); expect(requests).toBe(1); release();
  await expect(templateCard(page, name)).toBeVisible(); await page.unroute("**/api/task-templates");

  for (const width of [1440, 768, 375]) {
    await page.setViewportSize({ width, height: 900 });
    await expect(page.getByRole("button", { name: "Create template" })).toBeVisible();
    const results = await new AxeBuilder({ page }).include("#todo-view").analyze();
    expect(results.violations.filter((issue) => ["critical", "serious"].includes(issue.impact)), `axe violations at ${width}px`).toEqual([]);
  }
  await page.getByRole("button", { name: "Create template" }).click();
  await expect(page.locator("#template-name")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.locator("#template-editor-dialog")).toBeHidden();
  await page.getByRole("button", { name: "Create template" }).click();
  const editorAxe = await new AxeBuilder({ page }).include("#template-editor-dialog").analyze();
  expect(editorAxe.violations.filter((issue) => ["critical", "serious"].includes(issue.impact))).toEqual([]);
  await page.locator("#template-editor-cancel").click();
  await page.getByRole("button", { name: "View" }).last().focus(); await expect(page.getByRole("button", { name: "View" }).last()).toBeFocused();
});

test("delete requires confirmation and never removes tasks already generated", async ({ page }) => {
  await signIn(page, accountA);
  const name = `${prefix} delete`; const title = `${prefix} retained task`;
  await page.getByRole("button", { name: "Create template" }).click(); await page.locator("#template-name").fill(name); await page.locator("#add-template-item").click(); await page.locator("#template-add-manual").click(); await page.locator("#template-item-title-1").fill(title); await page.locator("#template-save-button").click();
  await useTemplate(page, name); await expect(taskRow(page, title)).toBeVisible();
  page.on("dialog", (dialog) => dialog.accept());
  await templateCard(page, name).getByRole("button", { name: "View" }).click();
  await page.locator("#template-detail-delete").click();
  await expect(templateCard(page, name)).toHaveCount(0); await expect(taskRow(page, title)).toBeVisible();
});

test("shows recoverable network and server errors while keeping the editor available", async ({ page }) => {
  await signIn(page, accountA);
  const name = `${prefix} retryable`;
  await page.getByRole("button", { name: "Create template" }).click();
  await page.locator("#template-name").fill(name);
  await page.locator("#add-template-item").click();
  await page.locator("#template-add-manual").click();
  await page.locator("#template-item-title-1").fill(`${prefix} retry task`);
  await page.route("**/api/task-templates", (route) => route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: { code: "internal_error" } }) }));
  await page.locator("#template-save-button").click();
  await expect(page.locator("#template-editor-error")).toContainText("temporarily unavailable");
  await page.unroute("**/api/task-templates");
  await page.route("**/api/task-templates", (route) => route.abort());
  await page.locator("#template-save-button").click();
  await expect(page.locator("#template-editor-error")).toContainText("Check your connection");
  await page.unroute("**/api/task-templates");
  await page.locator("#template-editor-cancel").click();
});
