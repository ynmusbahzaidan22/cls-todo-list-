// Small DOM renderers for read-only previews and Admin account/task cards.
"use strict";

function renderAdminUserPreview(account) {
  const card = document.createElement("article");
  card.className = "admin-user-preview-card";
  const name = document.createElement("strong");
  name.textContent = account?.name || "Unnamed user";
  const details = document.createElement("span");
  details.textContent = `${account?.role === "ADMIN" ? "Admin" : "User"} · ${account?.status === "ACTIVE" ? "Active" : "Disabled"}`;
  card.append(name, details);
  adminUserPreviewList.append(card);
}

function renderAdminTask(task, list = adminTaskList) {
  const summary = adminTaskViewModel(task);
  const card = document.createElement("article");
  card.className = "admin-task-card";

  const top = document.createElement("div");
  top.className = "admin-task-top";
  const heading = document.createElement("div");
  const title = document.createElement("h4");
  title.className = "admin-task-title";
  title.textContent = summary.title;
  const owner = document.createElement("p");
  owner.className = "admin-task-owner";
  owner.textContent = `Owner: ${summary.ownerName}`;
  heading.append(title, owner);

  const state = document.createElement("div");
  state.className = "admin-task-badges";
  const status = document.createElement("span");
  status.className = "admin-task-badge";
  status.textContent = summary.status.replaceAll("_", " ");
  state.append(status);
  if (summary.deleted) {
    const deleted = document.createElement("span");
    deleted.className = "admin-task-badge admin-task-badge-muted";
    deleted.textContent = "Deleted";
    state.append(deleted);
  }
  top.append(heading, state);

  const details = document.createElement("dl");
  details.className = "admin-task-details";
  const priority = document.createElement("div");
  const priorityTerm = document.createElement("dt");
  priorityTerm.textContent = "Priority";
  const priorityValue = document.createElement("dd");
  priorityValue.textContent = summary.priority;
  priority.append(priorityTerm, priorityValue);
  const dueDate = document.createElement("div");
  const dueTerm = document.createElement("dt");
  dueTerm.textContent = "Due date";
  const dueValue = document.createElement("dd");
  dueValue.textContent = summary.dueDate || "No due date";
  dueDate.append(dueTerm, dueValue);
  details.append(priority, dueDate);
  card.append(top, details);
  list.append(card);
}

function renderAdminAccount(account) {
  if (!account || typeof account.id !== "string" || typeof account.name !== "string" || typeof account.email !== "string") return;
  const row = document.createElement("article");
  row.className = "admin-account-row";
  const identity = document.createElement("div");
  const title = document.createElement("h3");
  title.textContent = account.name;
  const email = document.createElement("p");
  email.textContent = account.email;
  const state = document.createElement("p");
  state.textContent = `Role: ${account.role} · Status: ${account.status}`;
  identity.append(title, email, state);
  row.append(identity);
  if (account.protection_reason) {
    const note = document.createElement("p");
    note.className = "admin-protection-note";
    note.textContent = account.protection_reason === "self" ? "Your current account is protected from self-management." : "The final active administrator is protected.";
    row.append(note);
  }
  if (account.can_change_role) {
    const roleLabel = document.createElement("label");
    roleLabel.textContent = "Role";
    const role = document.createElement("select");
    role.setAttribute("aria-label", `Role for ${account.name}`);
    for (const value of ["USER", "ADMIN"]) {
      const option = document.createElement("option"); option.value = value; option.textContent = value; option.selected = account.role === value; role.append(option);
    }
    roleLabel.append(role);
    const changeRole = document.createElement("button");
    changeRole.type = "button"; changeRole.className = "secondary-button"; changeRole.textContent = "Change role";
    changeRole.addEventListener("click", () => {
      if (role.value === account.role || !window.confirm(`Change ${account.name}'s role to ${role.value}? Their current sessions will be revoked.`)) return;
      void performAdminMutation(account.id, "role", { role: role.value });
    });
    row.append(roleLabel, changeRole);
  }
  if (account.can_change_status) {
    const statusButton = document.createElement("button");
    statusButton.type = "button"; statusButton.className = account.status === "ACTIVE" ? "delete-button" : "secondary-button";
    statusButton.textContent = account.status === "ACTIVE" ? "Disable account" : "Enable account";
    statusButton.addEventListener("click", () => {
      const next = account.status === "ACTIVE" ? "DISABLED" : "ACTIVE";
      if (!window.confirm(`${next === "DISABLED" ? "Disable" : "Enable"} ${account.name}'s account? Existing sessions will be revoked.`)) return;
      void performAdminMutation(account.id, "status", { status: next });
    });
    row.append(statusButton);
  }
  if (account.can_revoke_sessions) {
    const revoke = document.createElement("button");
    revoke.type = "button"; revoke.className = "secondary-button"; revoke.textContent = "Revoke sessions";
    revoke.addEventListener("click", () => {
      if (window.confirm(`Revoke all active sessions for ${account.name}?`)) void performAdminMutation(account.id, "sessions/revoke", {});
    });
    row.append(revoke);
  }
  adminAccountList.append(row);
}
