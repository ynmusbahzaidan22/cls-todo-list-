import AxeBuilder from "@axe-core/playwright";
import { randomUUID } from "node:crypto";
import { Pool } from "pg";
import { expect, test } from "@playwright/test";
import { getTestDatabaseURL } from "./test-database.js";

const testDatabaseURL = getTestDatabaseURL();
test.describe.configure({ mode: "serial" });

const baseURL = process.env.PHASE8_BASE_URL || "http://127.0.0.1:18080";
const prefix = `phase16-due-${randomUUID().replaceAll("-", "").slice(0, 18)}`;
const accounts = [
  { email: `${prefix}-a@example.test`, password: `Phase16-${randomUUID()}-${randomUUID()}` },
  { email: `${prefix}-b@example.test`, password: `Phase16-${randomUUID()}-${randomUUID()}` },
];
let pool;

async function signIn(page, account) {
  await page.goto("/");
  await page.locator("#login-email").fill(account.email);
  await page.locator("#login-password").fill(account.password);
  await page.locator("#login-submit").click();
  await expect(page.locator("#todo-view")).toBeVisible();
}

function row(page, title) {
  return page.locator("#task-list .task-item").filter({ has: page.locator(".task-content h3", { hasText: new RegExp(`^${title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`) }) });
}

async function openTaskEdit(page, title) {
  await row(page, title).getByRole("button", { name: `View details and activity for ${title}` }).click();
  await page.locator("#activity-edit-task").click();
}

test.beforeAll(async () => {
  for (const account of accounts) {
    const response = await fetch(`${baseURL}/api/auth/register`, {
      method: "POST", headers: { "Content-Type": "application/json", Origin: baseURL }, body: JSON.stringify(account),
    });
    if (response.status !== 202) throw new Error(`Could not register isolated Phase 16 account (${response.status}).`);
  }
});

test.afterAll(async () => {
  try {
    pool ||= new Pool({ connectionString: testDatabaseURL, max: 1 });
    await pool.query("DELETE FROM app.users WHERE email = ANY($1::text[])", [accounts.map((account) => account.email)]);
    const { rows } = await pool.query("SELECT count(*)::int AS count FROM app.users WHERE email = ANY($1::text[])", [accounts.map((account) => account.email)]);
    if (rows[0].count !== 0) throw new Error("Phase 16 browser cleanup left its isolated accounts.");
  } finally { if (pool) await pool.end(); }
});

test("overdue status follows the currently opened device timezone, not the account", async ({ browser }) => {
  const civilToday = (zone) => {
    const parts = new Intl.DateTimeFormat("en", { timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit" }).formatToParts(new Date());
    const part = (type) => parts.find((item) => item.type === type)?.value;
    return `${part("year")}-${part("month")}-${part("day")}`;
  };
  const utcToday = civilToday("UTC");
  const eastToday = civilToday("Pacific/Kiritimati");
  const westToday = civilToday("Pacific/Pago_Pago");
  const [firstZone, secondZone] = eastToday !== utcToday
    ? ["UTC", "Pacific/Kiritimati"]
    : ["Pacific/Pago_Pago", "UTC"];
  const [firstDate, secondDate] = [civilToday(firstZone), civilToday(secondZone)].sort();
  expect(firstDate).not.toBe(secondDate);
  const title = `${prefix} device-local overdue`;
  const firstContext = await browser.newContext({ timezoneId: firstZone });
  const firstPage = await firstContext.newPage();
  try {
    await signIn(firstPage, accounts[0]);
    await expect(firstPage.locator("#account-time-zone")).toHaveCount(0);
    await firstPage.locator("#task-input").fill(title);
    await firstPage.locator("#task-options > summary").click();
    await firstPage.locator("#task-due-date-input").fill(firstDate);
    await firstPage.getByRole("button", { name: "Add Task" }).click();
    await expect(row(firstPage, title).locator(".task-due-date")).not.toContainText("Overdue");
  } finally {
    await firstContext.close();
  }

  const secondContext = await browser.newContext({ timezoneId: secondZone });
  const secondPage = await secondContext.newPage();
  try {
    await signIn(secondPage, accounts[0]);
    await expect(secondPage.locator("#account-time-zone")).toHaveCount(0);
    await expect(row(secondPage, title).locator(".task-due-date")).toContainText("Overdue");
    await secondPage.locator("#task-filter-toggle").click();
    await secondPage.locator("#task-due-filter").selectOption("overdue");
    await expect(row(secondPage, title)).toBeVisible();
  } finally {
    await secondContext.close();
  }
});

test("due date creation, editing, clearing, filtering, civil date display, timezone, ownership and responsive accessibility", async ({ page, context }) => {
  pool = new Pool({ connectionString: testDatabaseURL, max: 1 });
  await signIn(page, accounts[0]);
  const deviceTimeZone = await page.evaluate(() => Intl.DateTimeFormat().resolvedOptions().timeZone);
  await expect(page.locator("#account-time-zone")).toHaveCount(0);
  const dbDate = (await pool.query("SELECT to_char((clock_timestamp() AT TIME ZONE $1)::date, 'YYYY-MM-DD') AS today", [deviceTimeZone])).rows[0].today;
  const yesterdayDate = new Date(`${dbDate}T00:00:00Z`);
  yesterdayDate.setUTCDate(yesterdayDate.getUTCDate() - 1);
  const yesterday = yesterdayDate.toISOString().slice(0, 10);
  const tomorrowDate = new Date(`${dbDate}T00:00:00Z`);
  tomorrowDate.setUTCDate(tomorrowDate.getUTCDate() + 1);
  const tomorrow = tomorrowDate.toISOString().slice(0, 10);
  const title = `${prefix} dated task`;
  await page.locator("#task-input").fill(title);
  await page.locator("#task-options > summary").click();
  await page.locator("#task-due-date-input").fill(yesterday);
  await page.getByRole("button", { name: "Add Task" }).click();
  const datedRow = row(page, title);
  await expect(datedRow).toBeVisible();
  await expect(datedRow.locator(".task-due-date")).toContainText("Overdue");
  const renderedDate = await datedRow.locator(".task-due-date").textContent();
  expect(renderedDate).not.toContain("Date unavailable");
  const expectedCivilDate = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeZone: "UTC" }).format(new Date(`${yesterday}T00:00:00.000Z`));
  expect(renderedDate).toContain(expectedCivilDate);
  await page.reload();
  await expect(page.locator("#todo-view")).toBeVisible();
  await expect(row(page, title).locator(".task-due-date")).toContainText("Overdue");

  await page.locator("#task-filter-toggle").click();
  await page.locator("#task-due-filter").selectOption("overdue");
  await expect(datedRow).toBeVisible();
  await page.locator("#task-due-filter").selectOption("today");
  await expect(datedRow).toHaveCount(0);
  await page.locator("#task-due-filter").selectOption("");
  await expect(datedRow).toBeVisible();
  await openTaskEdit(page, title);
  await datedRow.locator('input[name="due_date"]').fill(tomorrow);
  await datedRow.getByRole("button", { name: "Save changes" }).click();
  await expect(datedRow.locator(".task-due-date")).toContainText("Due");
  await expect(datedRow.locator(".task-due-date")).not.toContainText("Overdue");
  await openTaskEdit(page, title);
  await datedRow.getByRole("button", { name: "Clear due date" }).click();
  await datedRow.getByRole("button", { name: "Save changes" }).click();
  await expect(datedRow.locator(".task-due-date")).toHaveCount(0);

  await datedRow.getByRole("button", { name: `View details and activity for ${title}` }).click();
  await expect(page.locator("#activity-list")).toContainText("Due date changed");
  await page.locator("#activity-close-button").click();

  for (const viewport of [{ width: 1440, height: 1000 }, { width: 768, height: 1024 }, { width: 375, height: 812 }]) {
    await page.setViewportSize(viewport);
    const axe = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    expect(axe.violations.filter((violation) => ["critical", "serious"].includes(violation.impact)), JSON.stringify(axe.violations)).toEqual([]);
  }

  await page.locator("#logout-button").click();
  await expect(page.locator("#auth-view")).toBeVisible();
  const otherTab = await context.newPage();
  await signIn(otherTab, accounts[1]);
  await expect(otherTab.locator("#task-list")).not.toContainText(title);
  const foreignTask = await pool.query("SELECT t.id FROM app.tasks t JOIN app.users u ON u.id=t.user_id WHERE u.email=$1 AND t.title=$2", [accounts[0].email, title]);
  expect(foreignTask.rows).toHaveLength(1);
  const foreignResponse = await otherTab.request.get(`/api/tasks/${foreignTask.rows[0].id}`);
  expect(foreignResponse.status()).toBe(404);
  await otherTab.reload();
  await expect(otherTab.locator("#task-list")).not.toContainText(title);
  await otherTab.close();
});
