import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const source = readFileSync(new URL("./script.js", import.meta.url), "utf8");
const markup = readFileSync(new URL("./index.html", import.meta.url), "utf8");
const start = source.indexOf("function maskHeaderEmail(value) {");
const end = source.indexOf("\nlet currentUser = null;", start);
assert.ok(start >= 0 && end > start, "presentation helpers must remain at the top of script.js");
const sandbox = {};
vm.runInNewContext(`${source.slice(start, end)}\nglobalThis.helpers = { maskHeaderEmail, togglePasswordVisibility, adminTaskViewModel };`, sandbox);
const { adminTaskViewModel, maskHeaderEmail, togglePasswordVisibility } = sandbox.helpers;

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

test("admin dashboard separates user management from all-task read-only states", () => {
  const taskSectionStart = markup.indexOf('id="admin-tasks-section"');
  const taskSectionEnd = markup.indexOf("</section>", taskSectionStart);
  const taskSection = markup.slice(taskSectionStart, taskSectionEnd);
  assert.ok(markup.includes('id="admin-users-section"'));
  assert.match(taskSection, /All users’ tasks/);
  assert.match(taskSection, /Read only/);
  assert.match(taskSection, /id="admin-task-loading"/);
  assert.match(taskSection, /id="admin-task-empty"/);
  assert.match(taskSection, /id="admin-task-error"/);
  assert.match(markup, /id="admin-account-loading"/);
  assert.match(markup, /id="admin-account-empty"/);
  assert.match(markup, /id="admin-account-error"/);
  assert.match(source, /\/api\/admin\/tasks\?/);
  assert.match(source, /state: "all"/);
  assert.doesNotMatch(source.slice(source.indexOf("function renderAdminTask"), source.indexOf("function renderAdminAccount")), /addEventListener|data-task-id/);
});

test("all sign-in and registration password inputs start hidden with accessible toggles", () => {
  const toggles = markup.match(/data-password-visibility-toggle/g) || [];
  const passwordInputs = markup.match(/type="password"/g) || [];
  assert.equal(toggles.length, 3);
  assert.equal(passwordInputs.length, 3);
  assert.equal((markup.match(/aria-label="Show password"/g) || []).length, 3);
  assert.equal((markup.match(/aria-pressed="false"/g) || []).length >= 3, true);
});
