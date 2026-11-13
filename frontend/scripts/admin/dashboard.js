// Admin dashboard previews, paging, and account management UI.
"use strict";

async function loadAdminDashboardPreviews() {
  if (currentUser?.role !== "ADMIN") return;
  const actorId = currentUser.id;
  const generation = ++adminPreviewLoadGeneration;
  for (const element of [adminUserPreviewLoading, adminTaskPreviewLoading]) element.hidden = false;
  adminUserPreviewError.hidden = true;
  adminTaskPreviewError.hidden = true;
  adminUserPreviewEmpty.hidden = true;
  adminTaskPreviewEmpty.hidden = true;
  const requests = [
    taskRequest("/api/admin/users?limit=5"),
    taskRequest("/api/admin/tasks?limit=5&state=all"),
  ];
  const [usersResult, tasksResult] = await Promise.allSettled(requests);
  if (currentUser?.id !== actorId || generation !== adminPreviewLoadGeneration) return;
  if (usersResult.status === "fulfilled" && usersResult.value.response.ok && Array.isArray(usersResult.value.data?.items)) {
    const users = usersResult.value.data.items;
    adminUserPreviewList.replaceChildren();
    users.forEach((account) => renderAdminUserPreview(account));
    adminUserPreviewCount.textContent = `${users.length} users shown`;
    adminUserPreviewEmpty.hidden = users.length > 0;
  } else {
    adminUserPreviewError.hidden = false;
    adminUserPreviewCount.textContent = "";
  }
  if (tasksResult.status === "fulfilled" && tasksResult.value.response.ok && Array.isArray(tasksResult.value.data?.items)) {
    const tasks = tasksResult.value.data.items;
    adminTaskPreviewList.replaceChildren();
    tasks.forEach((task) => renderAdminTask(task, adminTaskPreviewList));
    adminTaskPreviewCount.textContent = `${tasks.length} tasks shown`;
    adminTaskPreviewEmpty.hidden = tasks.length > 0;
  } else {
    adminTaskPreviewError.hidden = false;
    adminTaskPreviewCount.textContent = "";
  }
  adminUserPreviewLoading.hidden = true;
  adminTaskPreviewLoading.hidden = true;
}

async function loadAdminAccounts(append = false, preserveFeedback = false) {
  if (currentUser?.role !== "ADMIN" || workspaceScreen !== "admin-users") return false;
  if (append && !adminAccountLoading.hidden) return false;
  const actorId = currentUser.id;
  const generation = append ? adminAccountLoadGeneration : ++adminAccountLoadGeneration;
  const cursor = append ? adminAccountCursor : null;
  if (!append) {
    adminAccountCursor = null;
    adminAccountList.replaceChildren();
    if (!preserveFeedback) adminAccountFeedback.textContent = "";
  }
  adminAccountError.hidden = true;
  adminAccountEmpty.hidden = true;
  adminAccountLoading.hidden = false;
  adminAccountList.setAttribute("aria-busy", "true");
  try {
    const query = new URLSearchParams({ limit: "20", q: adminUserSearchInput.value.trim() });
    if (cursor) query.set("cursor", cursor);
    const { response, data } = await taskRequest(`/api/admin/users?${query.toString()}`);
    if (currentUser?.id !== actorId || generation !== adminAccountLoadGeneration) return false;
    if (!response.ok || !Array.isArray(data?.items)) throw new Error("We couldn't load account administration.");
    for (const account of data.items) renderAdminAccount(account);
    adminAccountCursor = typeof data.next_cursor === "string" ? data.next_cursor : null;
    const accountCount = adminAccountList.querySelectorAll(".admin-account-row").length;
    adminAccountCount.textContent = `${accountCount} users shown${adminAccountCursor ? " · more available" : ""}`;
    adminAccountEmpty.hidden = accountCount > 0 || Boolean(adminAccountCursor);
    adminAccountList.querySelector(".admin-load-more")?.remove();
    if (adminAccountCursor) {
      const more = document.createElement("button");
      more.type = "button";
      more.className = "secondary-button admin-load-more";
      more.textContent = "Load more users";
      more.addEventListener("click", () => void loadAdminAccounts(true));
      adminAccountList.append(more);
    }
    return true;
  } catch {
    if (currentUser?.id === actorId && generation === adminAccountLoadGeneration) {
      adminAccountError.textContent = "Account administration is unavailable. Refresh and try again.";
      adminAccountError.hidden = false;
    }
    return false;
  } finally {
    if (generation === adminAccountLoadGeneration) {
      adminAccountLoading.hidden = true;
      adminAccountList.setAttribute("aria-busy", "false");
    }
  }
}

async function loadAdminTasks(append = false) {
  if (currentUser?.role !== "ADMIN" || workspaceScreen !== "admin-tasks") return false;
  if (append && !adminTaskLoading.hidden) return false;
  const actorId = currentUser.id;
  const generation = append ? adminTaskLoadGeneration : ++adminTaskLoadGeneration;
  const cursor = append ? adminTaskCursor : null;
  if (!append) {
    adminTaskCursor = null;
    adminTaskList.replaceChildren();
    adminTaskEmpty.hidden = true;
  }
  adminTaskError.hidden = true;
  adminTaskLoading.hidden = false;
  adminTaskList.setAttribute("aria-busy", "true");
  try {
    const query = new URLSearchParams({ limit: "20", state: "all" });
    if (cursor) query.set("cursor", cursor);
    const { response, data } = await taskRequest(`/api/admin/tasks?${query.toString()}`);
    if (currentUser?.id !== actorId || generation !== adminTaskLoadGeneration) return false;
    if (!response.ok || !Array.isArray(data?.items)) throw new Error("Admin task listing unavailable.");
    for (const task of data.items) renderAdminTask(task);
    adminTaskCursor = typeof data.next_cursor === "string" ? data.next_cursor : null;
    const taskCount = adminTaskList.querySelectorAll(".admin-task-card").length;
    adminTaskCount.textContent = `${taskCount} tasks shown${adminTaskCursor ? " · more available" : ""}`;
    adminTaskList.querySelector(".admin-task-load-more")?.remove();
    if (adminTaskCursor) {
      const more = document.createElement("button");
      more.type = "button";
      more.className = "secondary-button admin-task-load-more";
      more.textContent = "Load more tasks";
      more.addEventListener("click", () => void loadAdminTasks(true));
      adminTaskList.append(more);
    }
    adminTaskEmpty.hidden = taskCount > 0 || Boolean(adminTaskCursor);
    return true;
  } catch {
    if (currentUser?.id === actorId && generation === adminTaskLoadGeneration) {
      adminTaskError.textContent = "Tasks are unavailable. Refresh and try again.";
      adminTaskError.hidden = false;
    }
    return false;
  } finally {
    if (generation === adminTaskLoadGeneration) {
      adminTaskLoading.hidden = true;
      adminTaskList.setAttribute("aria-busy", "false");
    }
  }
}

async function performAdminMutation(userId, action, body) {
  adminAccountFeedback.textContent = "Updating account…";
  try {
    const method = action === "sessions/revoke" ? "POST" : "PATCH";
    const { response, data } = await taskRequest(`/api/admin/users/${encodeURIComponent(userId)}/${action}`, { method, body });
    if (response.status === 409) throw new Error("The final active administrator cannot be disabled or demoted.");
    if (response.status === 403) throw new Error("This account-management action is not permitted.");
    if (!response.ok) throw new Error("The account change could not be completed.");
    const successMessage = action === "sessions/revoke" ? `Revoked ${data?.revoked_sessions ?? 0} active session(s).` : "Account updated. Its existing sessions were revoked.";
    const refreshed = await loadAdminAccounts(false, true);
    void loadAdminDashboardPreviews();
    adminAccountFeedback.textContent = refreshed ? successMessage : "The account change succeeded, but the list could not be refreshed.";
  } catch (error) {
    adminAccountFeedback.textContent = error instanceof Error ? error.message : "The account change could not be completed.";
  }
}
