import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { assembleFrontendPage } from "../../tools/assemble-frontend-page.mjs";

const frontend = new URL("../../frontend/", import.meta.url);
const sourceFiles = [
  "scripts/utils/presentation.js",
  "scripts/core/state.js",
  "scripts/auth/views.js",
  "scripts/services/http.js",
  "scripts/auth/sessions.js",
  "scripts/templates/templates.js",
  "scripts/core/workspace.js",
  "scripts/components/admin.js",
  "scripts/admin/dashboard.js",
  "scripts/preferences/time-zone.js",
  "scripts/tasks/workspace.js",
];
const source = sourceFiles.map((path) => readFileSync(new URL(path, frontend), "utf8").replace(/^\/\/[^\n]*\n/, "")).join("");
const markup = await assembleFrontendPage(frontend);
const start = source.indexOf("function maskHeaderEmail(value) {");
const end = source.indexOf("\nlet currentUser = null;", start);
assert.ok(start >= 0 && end > start, "presentation helpers must remain in the frontend core state module");
const sandbox = {};
vm.runInNewContext(`${source.slice(start, end)}\nglobalThis.helpers = { maskHeaderEmail, togglePasswordVisibility, adminTaskViewModel, workspaceShowsTimeZone };`, sandbox);
const { adminTaskViewModel, maskHeaderEmail, togglePasswordVisibility, workspaceShowsTimeZone } = sandbox.helpers;

test("header email mask reveals only one local character and the final extension", () => {
  assert.equal(maskHeaderEmail("yosef.zidan@example.com"), "y•••@•••.com");
  assert.equal(maskHeaderEmail("a@b.c"), "a•••@•••.c");
  assert.equal(maskHeaderEmail("person@sub.example.co.uk"), "p•••@•••.uk");
});

test("short, malformed, and unusual header values stay masked", () => {
  for (const value of ["", "@example.com", "person@", "not-an-email", null, undefined]) {
    assert.equal(maskHeaderEmail(value), "•••@•••");
  }
  assert.equal(maskHeaderEmail("a@host."), "a•••@•••");
  assert.equal(maskHeaderEmail(`a@host.${"x".repeat(60)}`), "a•••@•••");
});

test("password visibility toggles accessibly without changing the input value", () => {
  const input = { type: "password", value: "unchanged" };
  const attributes = new Map();
  const classes = new Set();
  const button = {
    setAttribute(name, value) { attributes.set(name, value); },
    classList: { toggle(name, enabled) { if (enabled) classes.add(name); else classes.delete(name); } },
  };

  assert.equal(togglePasswordVisibility(input, button), true);
  assert.equal(input.type, "text");
  assert.equal(attributes.get("aria-label"), "Hide password");
  assert.equal(attributes.get("aria-pressed"), "true");
  assert.equal(classes.has("is-visible"), true);
  assert.equal(input.value, "unchanged");

  assert.equal(togglePasswordVisibility(input, button), false);
  assert.equal(input.type, "password");
  assert.equal(attributes.get("aria-label"), "Show password");
  assert.equal(attributes.get("aria-pressed"), "false");
  assert.equal(input.value, "unchanged");
});

test("admin task display model is read-only and excludes internal IDs", () => {
  const model = adminTaskViewModel({
    id: "internal-id-must-not-be-shown",
    title: "Prepare release",
    owner_name: "Project owner",
    status: "IN_PROGRESS",
    priority: "HIGH",
    due_date: "2026-11-20",
    deleted_at: null,
  });
  assert.equal(model.title, "Prepare release");
  assert.equal(model.ownerName, "Project owner");
  assert.equal(model.status, "IN_PROGRESS");
  assert.equal(model.priority, "HIGH");
  assert.equal(model.dueDate, "2026-11-20");
  assert.equal(model.deleted, false);
  assert.equal("id" in model, false);
  assert.equal(adminTaskViewModel({}).dueDate, null);
});

test("time-zone preference is shown only on the main Tasks screen", () => {
  assert.ok(markup.includes('id="timezone-preference"'));
  assert.match(source, /timeZonePreference\.hidden = !workspaceVisible \|\| !workspaceShowsTimeZone\(workspaceScreen\)/);
  assert.equal(workspaceShowsTimeZone("tasks"), true);
  for (const screen of ["deleted", "completed", "admin-users", "admin-tasks"]) {
    assert.equal(workspaceShowsTimeZone(screen), false, `${screen} must not show the account time-zone control`);
  }
});

test("admin dashboard previews five users and tasks, with separate paginated views", () => {
  const taskSectionStart = markup.indexOf('id="admin-tasks-section"');
  const taskSectionEnd = markup.indexOf('id="admin-users-view"', taskSectionStart);
  const taskSection = markup.slice(taskSectionStart, taskSectionEnd);
  assert.ok(markup.includes('id="admin-users-section"'));
  assert.match(taskSection, /All users’ tasks/);
  assert.match(taskSection, /Read only/);
  assert.match(taskSection, /id="admin-task-preview-loading"/);
  assert.match(taskSection, /id="admin-task-preview-empty"/);
  assert.match(taskSection, /id="admin-task-preview-error"/);
  assert.match(markup, /id="admin-user-preview-loading"/);
  assert.match(markup, /id="admin-user-preview-empty"/);
  assert.match(markup, /id="admin-user-preview-error"/);
  assert.match(markup, /id="admin-view-all-users"/);
  assert.match(markup, /id="admin-view-all-tasks"/);
  assert.match(markup, /id="admin-users-view"[\s\S]*id="admin-user-search-input"[\s\S]*id="admin-account-list"/);
  assert.match(markup, /id="admin-tasks-view"[\s\S]*id="admin-task-list"/);
  assert.match(markup, /Back to admin dashboard/);
  assert.match(source, /\/api\/admin\/users\?limit=5/);
  assert.match(source, /\/api\/admin\/tasks\?limit=5&state=all/);
  assert.match(source, /new URLSearchParams\(\{ limit: "20", q: adminUserSearchInput\.value\.trim\(\) \}\)/);
  assert.match(source, /new URLSearchParams\(\{ limit: "20", state: "all" \}\)/);
  assert.match(source, /adminUserSearchTimer = setTimeout\(\(\) => void loadAdminAccounts\(\), 250\)/);
  assert.match(source, /adminViewAllUsersButton\.addEventListener/);
  assert.match(source, /adminUsersBackButton\.addEventListener/);
  assert.match(source, /workspaceScreenFromHash/);
  assert.match(source, /more\.addEventListener\("click", \(\) => void loadAdminAccounts\(true\)\)/);
  assert.match(source, /more\.addEventListener\("click", \(\) => void loadAdminTasks\(true\)\)/);
  assert.doesNotMatch(source.slice(source.indexOf("function renderAdminTask"), source.indexOf("function renderAdminAccount")), /addEventListener|data-task-id/);
  const tasksView = markup.slice(markup.indexOf('id="admin-tasks-view"'), markup.indexOf("</section>", markup.indexOf('id="admin-tasks-view"')));
  assert.equal((tasksView.match(/<button\b/g) || []).length, 1, "the only button in the tasks view is its navigation button");
  assert.doesNotMatch(tasksView, /data-action/);
});

test("all sign-in and registration password inputs start hidden with accessible toggles", () => {
  const toggles = markup.match(/data-password-visibility-toggle/g) || [];
  const passwordInputs = markup.match(/type="password"/g) || [];
  assert.equal(toggles.length, 3);
  assert.equal(passwordInputs.length, 3);
  assert.equal((markup.match(/aria-label="Show password"/g) || []).length, 3);
  assert.equal((markup.match(/aria-pressed="false"/g) || []).length >= 3, true);
});
