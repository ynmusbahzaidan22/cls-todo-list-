// Pure frontend formatters and view models, shared by feature scripts and focused tests.
"use strict";

function maskHeaderEmail(value) {
  if (typeof value !== "string") return "•••@•••";
  const email = value.trim();
  const at = email.lastIndexOf("@");
  if (at < 1 || at === email.length - 1) return "•••@•••";
  const firstLocalCharacter = Array.from(email.slice(0, at))[0] || "•";
  const domain = email.slice(at + 1);
  const dot = domain.lastIndexOf(".");
  const candidateExtension = dot > 0 ? domain.slice(dot) : "";
  const extension = /^\.[a-z\d-]{1,24}$/i.test(candidateExtension) ? candidateExtension : "";
  return `${firstLocalCharacter}•••@•••${extension}`;
}

function togglePasswordVisibility(input, button) {
  if (!input || !button || (input.type !== "password" && input.type !== "text")) return false;
  const visible = input.type === "password";
  input.type = visible ? "text" : "password";
  button.setAttribute("aria-pressed", String(visible));
  button.setAttribute("aria-label", visible ? "Hide password" : "Show password");
  button.classList?.toggle("is-visible", visible);
  return visible;
}

function adminTaskViewModel(task) {
  const validStatuses = new Set(["TODO", "IN_PROGRESS", "DONE"]);
  const validPriorities = new Set(["LOW", "MEDIUM", "HIGH", "URGENT"]);
  return {
    title: typeof task?.title === "string" && task.title.trim() ? task.title : "Untitled task",
    ownerName: typeof task?.owner_name === "string" && task.owner_name.trim() ? task.owner_name : "Unknown owner",
    status: validStatuses.has(task?.status) ? task.status : "Unknown status",
    priority: validPriorities.has(task?.priority) ? task.priority : "Unknown priority",
    dueDate: typeof task?.due_date === "string" && /^\d{4}-\d{2}-\d{2}$/.test(task.due_date) ? task.due_date : null,
    deleted: Boolean(task?.deleted_at),
  };
}

function workspaceShowsTimeZone(screen) {
  return screen === "tasks";
}
