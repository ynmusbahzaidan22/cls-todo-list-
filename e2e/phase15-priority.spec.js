import AxeBuilder from "@axe-core/playwright";
import { randomUUID } from "node:crypto";
import { Pool } from "pg";
import { expect, test } from "@playwright/test";
import { getTestDatabaseURL, registerEmailRateLimitCleanup } from "./test-database.js";

const testDatabaseURL = getTestDatabaseURL();
test.describe.configure({ mode: "serial" });

const baseURL = process.env.PHASE8_BASE_URL || "http://127.0.0.1:18080";
const prefix = `phase15-priority-${randomUUID().replaceAll("-", "").slice(0, 18)}`;
const account = { name: "Priority User", email: `${prefix}@example.test`, password: `Phase15-${randomUUID()}-${randomUUID()}` };
const testClientIP = registerEmailRateLimitCleanup(test, [account.email]);
test.use({ extraHTTPHeaders: { "X-Forwarded-For": testClientIP } });
let pool;

function taskRow(page, title) {
  const exact = new RegExp(`^${title.replace(/[.*+?^${}()|[\\]\\]/g, "\\$&")}$`);
  return page.locator("#task-list .task-item").filter({ has: page.locator(".task-content h3", { hasText: exact }) });
}

async function signIn(page) {
  await page.goto("/");
  await page.locator("#login-email").fill(account.email);
  await page.locator("#login-password").fill(account.password);
  await page.locator("#login-submit").click();
  await expect(page.locator("#todo-view")).toBeVisible();
}

async function addTask(page, title, priority) {
  await page.locator("#task-input").fill(title);
  if (priority) {
    await page.locator("#task-options > summary").click();
    await page.locator("#task-priority-input").selectOption(priority);
  }
  await page.getByRole("button", { name: "Add Task" }).click();
  const row = taskRow(page, title);
  await expect(row).toBeVisible();
  return row;
}

test.beforeAll(async () => {
  const response = await fetch(`${baseURL}/api/auth/register`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Origin: baseURL, "X-Forwarded-For": testClientIP },
    body: JSON.stringify(account),
  });
  if (response.status !== 202) throw new Error(`Could not create isolated Phase 15 browser account (${response.status}).`);
});

test.afterAll(async () => {
  try {
    pool = pool || new Pool({ connectionString: testDatabaseURL, max: 1 });
    await pool.query("DELETE FROM app.users WHERE email = $1", [account.email]);
    const { rows } = await pool.query("SELECT count(*)::int AS count FROM app.users WHERE email = $1", [account.email]);
    if (rows[0].count !== 0) throw new Error("Phase 15 cleanup left its isolated test account.");
  } finally {
    if (pool) await pool.end();
  }
});

test("creates, displays, filters, edits, records and undoes priority; templates preserve item priorities", async ({ page }) => {
  await signIn(page);
  const priorities = [
    ["LOW", "Low"], ["MEDIUM", "Medium"], ["HIGH", "High"], ["URGENT", "Urgent"],
  ];
  const titles = new Map();
  for (const [value, label] of priorities) {
    const title = `${prefix} ${value}`;
    titles.set(value, title);
    const row = await addTask(page, title, value);
    await expect(row.locator(".priority-badge")).toHaveText(`Priority: ${label}`);
  }
  const defaultTitle = `${prefix} default`;
  const defaultRow = await addTask(page, defaultTitle);
  await expect(defaultRow.locator(".priority-badge")).toHaveText("Priority: Medium");

  const search = page.getByRole("searchbox", { name: "Search tasks" });
  const status = page.getByLabel("Status", { exact: true });
  const priorityFilter = page.locator("#task-priority-filter");
  await search.fill(prefix);
  await page.locator("#task-filter-toggle").click();
  await status.selectOption("TODO");
  await priorityFilter.selectOption("HIGH");
  await expect(taskRow(page, titles.get("HIGH"))).toBeVisible();
  await expect(page.locator("#task-list .task-item")).toHaveCount(1);
  await priorityFilter.selectOption("");

  const highRow = taskRow(page, titles.get("HIGH"));
  await highRow.getByRole("button", { name: `View details and activity for ${titles.get("HIGH")}` }).click();
  await page.locator("#activity-edit-task").click();
  await highRow.locator('select[name="priority"]').selectOption("URGENT");
  await highRow.getByRole("button", { name: "Save changes" }).click();
  await expect(highRow.locator(".priority-badge")).toHaveText("Priority: Urgent");
  await expect(page.locator("#task-feedback")).toContainText("Changes saved.");
  await highRow.getByRole("button", { name: `View details and activity for ${titles.get("HIGH")}` }).click();
  const priorityEvent = page.locator("#activity-list .activity-event").filter({ hasText: "Priority changed" }).filter({ hasText: "HIGH" });
  await expect(priorityEvent).toContainText("URGENT");
  await expect(priorityEvent.locator(".activity-event-actor")).toHaveText("You");
  await priorityEvent.getByRole("button", { name: "Undo priority changed" }).click();
  await expect(page.locator("#activity-action-feedback")).toContainText("original history was preserved");
  await expect(page.locator("#activity-list .activity-event").filter({ hasText: "Previous action undone" })).toBeVisible();
  await page.locator("#activity-close-button").click();
  await expect(taskRow(page, titles.get("HIGH")).locator(".priority-badge")).toHaveText("Priority: High");

  await page.getByRole("button", { name: "Create template" }).click();
  const templateName = `${prefix} template`;
  await page.locator("#template-name").fill(templateName);
  await page.locator("#add-template-item").click();
  await page.locator("#template-add-manual").click();
  const urgentTemplateTitle = `${prefix} template urgent`;
  await page.locator("#template-item-title-1").fill(urgentTemplateTitle);
  await page.locator("#template-item-priority-1").selectOption("URGENT");
  await page.locator("#add-template-item").click();
  await page.locator("#template-add-manual").click();
  const lowTemplateTitle = `${prefix} template low`;
  await page.locator("#template-item-title-2").fill(lowTemplateTitle);
  await page.locator("#template-item-priority-2").selectOption("LOW");
  await page.locator("#template-save-button").click();
  const template = page.locator("#template-list .template-item").filter({ has: page.getByRole("heading", { name: templateName, exact: true }) });
  await expect(template).toBeVisible();
  await template.getByRole("button", { name: "View" }).click();
  await expect(page.locator("#template-detail-dialog")).toContainText("Priority: Urgent");
  await expect(page.locator("#template-detail-dialog")).toContainText("Priority: Low");
  const detailAxe = await new AxeBuilder({ page }).include("#template-detail-dialog").analyze();
  expect(detailAxe.violations.filter((issue) => ["critical", "serious"].includes(issue.impact))).toEqual([]);
  await page.locator("#template-detail-apply").click();
  await page.locator("#template-apply-confirm").click();
  await expect(taskRow(page, urgentTemplateTitle).locator(".priority-badge")).toHaveText("Priority: Urgent");
  await expect(taskRow(page, lowTemplateTitle).locator(".priority-badge")).toHaveText("Priority: Low");

  for (const width of [1440, 768, 375]) {
    await page.setViewportSize({ width, height: 900 });
    const axe = await new AxeBuilder({ page }).include("#todo-view").analyze();
    expect(axe.violations.filter((issue) => ["critical", "serious"].includes(issue.impact))).toEqual([]);
  }
});
