"use strict";

// Authentication views share one explicit state so an unavailable API is not mistaken for logout.
let authState = { status: "checking-session" };
let authMode = "login";
let currentUser = null;
const DEVICE_TIME_ZONE = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
let activeTimeZone = DEVICE_TIME_ZONE;
let timeZoneRequestGeneration = 0;
const COMPLETED_PAGE_SIZE = 10;

const checkingView = document.querySelector("#checking-view");
const sessionErrorView = document.querySelector("#session-error-view");
const authView = document.querySelector("#auth-view");
const authenticatedView = document.querySelector("#authenticated-view");
const todoView = document.querySelector("#todo-view");
const tasksScreen = document.querySelector("#tasks-screen");
const workspaceJumpLinks = document.querySelector(".workspace-jump-links");
const recentlyDeletedScreen = document.querySelector("#recently-deleted-section");
const completedTasksScreen = document.querySelector("#completed-tasks-section");
const openCompletedButton = document.querySelector("#open-completed");
const completedTaskList = document.querySelector("#completed-task-list");
const completedLoading = document.querySelector("#completed-loading");
const completedLoadError = document.querySelector("#completed-load-error");
const completedLoadErrorMessage = document.querySelector("#completed-load-error-message");
const completedEmpty = document.querySelector("#completed-empty");
const completedMoreButton = document.querySelector("#completed-more-button");
const completedPaginationFeedback = document.querySelector("#completed-pagination-feedback");
const completedPeriodFilter = document.querySelector("#completed-period-filter");
const completedTotalCount = document.querySelector("#completed-total-count");
const completedShownCount = document.querySelector("#completed-shown-count");
const completedCountBadge = document.querySelector("#completed-task-count");
const deletedPreviewSection = document.querySelector("#deleted-preview-section");
const deletedPreviewList = document.querySelector("#deleted-preview-list");
const deletedPreviewCount = document.querySelector("#deleted-preview-count");
const deletedPreviewLoading = document.querySelector("#deleted-preview-loading");
const deletedPreviewError = document.querySelector("#deleted-preview-error");
const deletedPreviewRetry = document.querySelector("#retry-deleted-preview");
const openDeletedViewButton = document.querySelector("#open-deleted-view");
const recentlyDeletedCountBadge = document.querySelector("#deleted-task-count");
const recentlyDeletedViewCount = document.querySelector("#deleted-view-count");
const recentlyDeletedViewFeedback = document.querySelector("#deleted-view-feedback");
const backToTasksButton = document.querySelector("#deleted-back-to-tasks");
const authTitle = document.querySelector("#auth-title");
const authIntro = document.querySelector("#auth-intro");
const authFeedback = document.querySelector("#auth-feedback");
const logoutFeedback = document.querySelector("#logout-feedback");
const loginForm = document.querySelector("#login-form");
const registerForm = document.querySelector("#register-form");
const loginEmail = document.querySelector("#login-email");
const loginPassword = document.querySelector("#login-password");
const registerName = document.querySelector("#register-name");
const registerEmail = document.querySelector("#register-email");
const registerPassword = document.querySelector("#register-password");
const registerConfirmPassword = document.querySelector("#register-confirm-password");
const logoutButton = document.querySelector("#logout-button");
const adminAccountPanel = document.querySelector("#admin-account-panel");
const adminAccountList = document.querySelector("#admin-account-list");
const adminAccountLoading = document.querySelector("#admin-account-loading");
const adminAccountError = document.querySelector("#admin-account-error");
const adminAccountFeedback = document.querySelector("#admin-account-feedback");
let adminAccountCursor = null;
const loginSubmitLabel = document.querySelector("#login-submit-label");
const registerSubmitLabel = document.querySelector("#register-submit-label");
const signedInEmail = document.querySelector("#signed-in-email");
const timeZonePreferenceSummary = document.querySelector("#timezone-preference-summary");
const timeZoneOverrideInput = document.querySelector("#timezone-override-input");
const timeZonePreferenceFeedback = document.querySelector("#timezone-preference-feedback");
const useDeviceTimeZoneButton = document.querySelector("#use-device-timezone");
const retrySessionButton = document.querySelector("#retry-session-button");
const authTabs = [...document.querySelectorAll("[data-auth-mode]")];
const authChannel = typeof BroadcastChannel === "function" ? new BroadcastChannel("todo-list-auth") : null;
let workspaceScreen = window.location.hash === "#recently-deleted" ? "deleted" : window.location.hash === "#completed" ? "completed" : "tasks";

function announceSessionChange(type) {
  authChannel?.postMessage({ type });
}

authChannel?.addEventListener("message", (event) => {
  if (!["session-changed", "signed-out"].includes(event.data?.type)) return;
  currentUser = null;
  clearTaskWorkspace();
  clearLogoutFeedback();
  setAuthMode("login");
  void checkSession();
});

window.addEventListener("focus", () => {
  if (currentUser && authState.status === "authenticated") void verifyWorkspaceSession();
});

function renderAuthState() {
  const status = authState.status;
  const operation = authState.operation;
  const errorContext = authState.context;

  checkingView.hidden = status !== "checking-session";
  sessionErrorView.hidden = !(status === "recoverable-error" && errorContext === "session");
  authView.hidden = ![
    "unauthenticated",
    "submitting-login",
    "submitting-register",
  ].includes(status) && !(status === "recoverable-error" && errorContext === "form");
  authenticatedView.hidden = ![
    "authenticated",
    "submitting-logout",
  ].includes(status) && !(status === "recoverable-error" && errorContext === "logout");
  const workspaceVisible = !authenticatedView.hidden && Boolean(currentUser);
  adminAccountPanel.hidden = !workspaceVisible || currentUser?.role !== "ADMIN";
  todoView.hidden = !workspaceVisible;
  tasksScreen.hidden = !workspaceVisible || workspaceScreen !== "tasks";
  recentlyDeletedScreen.hidden = !workspaceVisible || workspaceScreen !== "deleted";
  completedTasksScreen.hidden = !workspaceVisible || workspaceScreen !== "completed";
  document.body.classList.toggle("workspace-page", workspaceVisible);

  if (authenticatedView.hidden) {
    document.title = "Sign in | My To-Do List";
  } else {
    document.title = "My To-Do List | Workspace";
  }

  const isSubmitting = status.startsWith("submitting-");
  for (const tab of authTabs) tab.disabled = isSubmitting;
  for (const input of authView.querySelectorAll("input")) input.disabled = isSubmitting;
  for (const button of authView.querySelectorAll(".auth-submit")) button.disabled = isSubmitting;
  loginForm.setAttribute("aria-busy", String(status === "submitting-login"));
  registerForm.setAttribute("aria-busy", String(status === "submitting-register"));
  loginSubmitLabel.textContent = status === "submitting-login" ? "Signing in…" : "Sign in";
  registerSubmitLabel.textContent = status === "submitting-register" ? "Creating account…" : "Create account";
  logoutButton.disabled = operation === "logout" && isSubmitting;
  logoutButton.textContent = logoutButton.disabled ? "Signing out…" : "Sign out";

  if (status === "authenticated" || status === "submitting-logout" || errorContext === "logout") {
    signedInEmail.textContent = currentUser?.email ?? "";
  }
  for (const link of workspaceJumpLinks.querySelectorAll("a")) {
    const active = (link.id === "open-completed" && workspaceScreen === "completed")
      || (link.id === "open-tasks" && workspaceScreen === "tasks");
    if (active) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
}

function setAuthState(status, details = {}) {
  authState = { status, ...details };
  renderAuthState();
}

function setWorkspaceScreen(screen, { replace = false, focus = true } = {}) {
  if (!currentUser) return;
  workspaceScreen = screen === "deleted" ? "deleted" : screen === "completed" ? "completed" : "tasks";
  const hash = workspaceScreen === "deleted" ? "#recently-deleted" : workspaceScreen === "completed" ? "#completed" : "#tasks";
  if (window.location.hash !== hash) {
    const method = replace ? "replaceState" : "pushState";
    window.history[method]({ workspaceScreen }, "", hash);
  }
  recentlyDeletedViewFeedback.hidden = true;
  renderAuthState();
  if (workspaceScreen === "deleted") {
    void loadDeletedTaskCount();
    void loadDeletedTasks(true);
    if (focus) document.querySelector("#recently-deleted-title").focus({ preventScroll: true });
  } else if (workspaceScreen === "completed") {
    void loadCompletedTasks(true);
    if (focus) document.querySelector("#completed-tasks-title").focus({ preventScroll: true });
  } else {
    void loadTasks(true);
    if (focus) document.querySelector("#page-title").focus({ preventScroll: true });
  }
}

openDeletedViewButton.addEventListener("click", () => {
  setWorkspaceScreen("deleted");
});
openCompletedButton.addEventListener("click", (event) => {
  event.preventDefault();
  setWorkspaceScreen("completed");
});
workspaceJumpLinks.addEventListener("click", (event) => {
  const link = event.target.closest('a[href="#task-section"], a[href="#template-section"]');
  if (!link || !["deleted", "completed"].includes(workspaceScreen)) return;
  event.preventDefault();
  const target = link.getAttribute("href");
  setWorkspaceScreen("tasks", { focus: false });
  requestAnimationFrame(() => document.querySelector(target)?.scrollIntoView({ behavior: "smooth", block: "start" }));
});
backToTasksButton.addEventListener("click", () => setWorkspaceScreen("tasks"));
document.querySelector("#completed-back-to-tasks").addEventListener("click", () => setWorkspaceScreen("tasks"));
window.addEventListener("popstate", () => {
  workspaceScreen = window.location.hash === "#recently-deleted" ? "deleted" : window.location.hash === "#completed" ? "completed" : "tasks";
  renderAuthState();
  if (workspaceScreen === "deleted" && currentUser) {
    void loadDeletedTaskCount();
    void loadDeletedTasks(true);
  }
  if (workspaceScreen === "completed" && currentUser) void loadCompletedTasks(true);
});

function setAuthMode(mode, { preserveFeedback = false } = {}) {
  authMode = mode;
  const isLogin = mode === "login";

  loginForm.hidden = !isLogin;
  registerForm.hidden = isLogin;
  authTitle.textContent = isLogin ? "Welcome back" : "Create your account";
  authIntro.textContent = isLogin
    ? "Sign in to pick up where you left off."
    : "Set up your account and make this space yours.";

  for (const tab of authTabs) {
    const isActive = tab.dataset.authMode === mode;
    tab.classList.toggle("is-active", isActive);
    tab.setAttribute("aria-pressed", String(isActive));
  }

  clearPasswordFields();
  clearFieldErrors();
  if (!preserveFeedback) clearAuthFeedback();
}

function showAuthFeedback(message, kind = "error") {
  authFeedback.textContent = message;
  authFeedback.dataset.kind = kind;
  authFeedback.hidden = false;
}

function clearAuthFeedback() {
  authFeedback.textContent = "";
  authFeedback.hidden = true;
  delete authFeedback.dataset.kind;
}

function showLogoutFeedback(message, kind = "error") {
  logoutFeedback.textContent = message;
  logoutFeedback.dataset.kind = kind;
  logoutFeedback.hidden = false;
}

function clearLogoutFeedback() {
  logoutFeedback.textContent = "";
  logoutFeedback.hidden = true;
  delete logoutFeedback.dataset.kind;
}

function setFieldError(input, message) {
  const error = document.querySelector(`#${input.id}-error`);
  input.setAttribute("aria-invalid", String(Boolean(message)));
  error.textContent = message;
}

function clearFieldErrors(form = null) {
  const inputs = form ? form.querySelectorAll("input") : document.querySelectorAll(".auth-form input");
  for (const input of inputs) setFieldError(input, "");
}

function clearPasswordFields() {
  loginPassword.value = "";
  registerPassword.value = "";
  registerConfirmPassword.value = "";
}

function validateEmail(input) {
  const value = input.value.trim();
  if (!value) {
    setFieldError(input, "Enter your email address.");
    return "";
  }
  if (!input.validity.valid) {
    setFieldError(input, "Enter a valid email address.");
    return "";
  }
  setFieldError(input, "");
  return value;
}

function validateDisplayName(input) {
  const value = input.value.trim();
  const characterCount = Array.from(value).length;
  if (characterCount < 1 || characterCount > 100) {
    setFieldError(input, "Enter a name with 1 to 100 characters.");
    return "";
  }
  setFieldError(input, "");
  return value;
}

function validatePassword(input, { registration = false } = {}) {
  const value = input.value;
  if (!value) {
    setFieldError(input, "Enter your password.");
    return false;
  }
  if (registration) {
    const characterCount = Array.from(value).length;
    const byteCount = new TextEncoder().encode(value).length;
    if (characterCount < 12 || characterCount > 128 || byteCount > 1024) {
      setFieldError(input, "Use a password between 12 and 128 characters.");
      return false;
    }
  }
  setFieldError(input, "");
  return true;
}

function getPublicUser(data) {
  if (!data || typeof data.id !== "string" || typeof data.name !== "string" || typeof data.email !== "string" || !["USER", "ADMIN"].includes(data.role)) {
    throw new Error("The server returned an unexpected response.");
  }
  return { id: data.id, name: data.name, email: data.email, role: data.role };
}

async function readJSON(response) {
  try {
    return await response.json();
  } catch {
    return null;
  }
}

function messageForAPIError(response, payload, operation) {
  const code = payload?.error?.code;
  if (code === "unauthorized") {
    return "Email or password is incorrect.";
  }
  if (code === "rate_limited") {
    return "Too many attempts. Wait a little and try again.";
  }
  if (code === "invalid_request") {
    return "Check the information and try again.";
  }
  if (operation === "logout") {
    return "We couldn't confirm sign-out. Try again or refresh to check your session.";
  }
  if (response.status >= 500) {
    return "We couldn't complete that request right now. Please try again.";
  }
  return operation === "register"
    ? "We couldn't create your account. Check the information and try again."
    : "We couldn't sign you in. Check your details and try again.";
}

async function postAuth(path, payload) {
  const response = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    cache: "no-store",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  const data = await readJSON(response);
  return { response, data };
}

async function checkSession() {
  clearAuthFeedback();
  clearLogoutFeedback();
  setAuthState("checking-session");

  try {
    const response = await fetch("/api/auth/me", {
      method: "GET",
      credentials: "same-origin",
      cache: "no-store",
      headers: { Accept: "application/json" },
    });

    if (response.status === 401) {
      currentUser = null;
      clearTaskWorkspace();
      setAuthMode("login");
      setAuthState("unauthenticated");
      return;
    }
    if (!response.ok) throw new Error("Session check failed.");

    currentUser = getPublicUser(await readJSON(response));
    setAuthState("authenticated");
    initializeTaskWorkspace(currentUser);
  } catch {
    currentUser = null;
    setAuthState("recoverable-error", { context: "session" });
  }
}

async function verifyWorkspaceSession() {
  const ownerId = currentUser?.id;
  const generation = taskSessionGeneration;
  if (!ownerId) return;
  try {
    const response = await fetch("/api/auth/me", {
      method: "GET",
      credentials: "same-origin",
      cache: "no-store",
      headers: { Accept: "application/json" },
    });
    if (!taskIsCurrent(generation, ownerId)) return;
    if (response.status === 401) {
      expireTaskSession(generation, ownerId);
      return;
    }
    if (!response.ok) return;
    const user = getPublicUser(await readJSON(response));
    if (user.id === ownerId) return;

    currentUser = user;
    clearTaskWorkspace();
    setAuthState("authenticated");
    initializeTaskWorkspace(user);
  } catch {
    // A temporary session-check failure does not sign the user out or clear their visible tasks.
  }
}

async function submitLogin(event) {
  event.preventDefault();
  if (authState.status.startsWith("submitting-")) return;

  clearAuthFeedback();
  const email = validateEmail(loginEmail);
  const passwordIsValid = validatePassword(loginPassword);
  if (!email || !passwordIsValid) return;

  setAuthState("submitting-login", { operation: "login" });
  try {
    const { response, data } = await postAuth("/api/auth/login", {
      email,
      password: loginPassword.value,
    });
    if (!response.ok) {
      setAuthState("unauthenticated");
      showAuthFeedback(messageForAPIError(response, data, "login"));
      return;
    }

    currentUser = getPublicUser(data);
    clearLogoutFeedback();
    setAuthState("authenticated");
    initializeTaskWorkspace(currentUser);
    announceSessionChange("session-changed");
  } catch {
    setAuthState("recoverable-error", { context: "form" });
    showAuthFeedback("We couldn't reach the sign-in service. Check your connection and try again.");
  } finally {
    loginPassword.value = "";
  }
}

async function submitRegistration(event) {
  event.preventDefault();
  if (authState.status.startsWith("submitting-")) return;

  clearAuthFeedback();
  const name = validateDisplayName(registerName);
  const email = validateEmail(registerEmail);
  const passwordIsValid = validatePassword(registerPassword, { registration: true });
  const confirmationMatches = registerConfirmPassword.value === registerPassword.value;
  setFieldError(registerConfirmPassword, confirmationMatches ? "" : "The passwords don't match.");
  if (!name || !email || !passwordIsValid || !confirmationMatches) return;

  setAuthState("submitting-register", { operation: "register" });
  try {
    const { response, data } = await postAuth("/api/auth/register", {
      name,
      email,
      password: registerPassword.value,
    });
    if (!response.ok) {
      setAuthState("unauthenticated");
      showAuthFeedback(messageForAPIError(response, data, "register"));
      return;
    }

    setAuthMode("login", { preserveFeedback: true });
    showAuthFeedback("If an account can be created, you can now sign in.", "success");
    setAuthState("unauthenticated");
    loginEmail.focus();
  } catch {
    setAuthState("recoverable-error", { context: "form" });
    showAuthFeedback("We couldn't reach the registration service. Check your connection and try again.");
  } finally {
    registerPassword.value = "";
    registerConfirmPassword.value = "";
  }
}

async function signOut() {
  if (authState.status.startsWith("submitting-") || !currentUser) return;

  clearLogoutFeedback();
  setAuthState("submitting-logout", { operation: "logout" });
  try {
    const response = await fetch("/api/auth/logout", {
      method: "POST",
      credentials: "same-origin",
      cache: "no-store",
    });
    if (!response.ok) {
      const data = await readJSON(response);
      setAuthState("recoverable-error", { context: "logout" });
      showLogoutFeedback(messageForAPIError(response, data, "logout"));
      return;
    }

    currentUser = null;
    clearTaskWorkspace();
    setAuthMode("login");
    setAuthState("unauthenticated");
    announceSessionChange("signed-out");
    showAuthFeedback("You have signed out.", "success");
    loginEmail.focus();
  } catch {
    setAuthState("recoverable-error", { context: "logout" });
    showLogoutFeedback("We couldn't confirm sign-out. Try again or refresh to check your session.");
  }
}

for (const tab of authTabs) {
  tab.addEventListener("click", () => {
    if (authState.status.startsWith("submitting-")) return;
    setAuthMode(tab.dataset.authMode);
  });
}

loginForm.addEventListener("submit", submitLogin);
registerForm.addEventListener("submit", submitRegistration);
logoutButton.addEventListener("click", signOut);
retrySessionButton.addEventListener("click", checkSession);

for (const input of document.querySelectorAll(".auth-form input")) {
  input.addEventListener("input", () => {
    setFieldError(input, "");
    if (authFeedback.dataset.kind === "error") clearAuthFeedback();
  });
}

// Task records are temporary render state; PostgreSQL remains the source of truth.
const TASK_PAGE_SIZE = 20;
const DELETED_PREVIEW_SIZE = 5;
const taskForm = document.querySelector("#task-form");
const taskInput = document.querySelector("#task-input");
const taskOptionsDetails = document.querySelector("#task-options");
const taskDescriptionInput = document.querySelector("#task-description-input");
const taskPriorityInput = document.querySelector("#task-priority-input");
const taskDueDateInput = document.querySelector("#task-due-date-input");
const clearNewDueDateButton = document.querySelector("#clear-new-due-date");
const taskList = document.querySelector("#task-list");
const recentlyCompletedGroup = document.querySelector("#recently-completed-group");
const recentlyCompletedDetails = document.querySelector("#recently-completed-details");
const recentlyCompletedCount = document.querySelector("#recently-completed-count");
const recentlyCompletedTaskList = document.querySelector("#recently-completed-task-list");
const taskSearchInput = document.querySelector("#task-search");
const taskStatusFilter = document.querySelector("#task-status-filter");
const taskPriorityFilter = document.querySelector("#task-priority-filter");
const taskDueFilter = document.querySelector("#task-due-filter");
const taskFilterToggle = document.querySelector("#task-filter-toggle");
const taskFilterPanel = document.querySelector("#task-filter-panel");
const clearTaskFiltersButton = document.querySelector("#clear-task-filters");
const activeTaskFilters = document.querySelector("#active-task-filters");
const taskFilterCount = document.querySelector("#task-filter-count");
const deletedTaskList = document.querySelector("#deleted-task-list");
const taskSummary = document.querySelector("#task-summary");
const taskShownCount = document.querySelector("#task-shown-count");
const taskCompletedCount = document.querySelector("#task-completed-count");
const emptyState = document.querySelector("#empty-state");
const emptyStateDefault = document.querySelector("#empty-state-default");
const emptyStateSearch = document.querySelector("#empty-state-search");
const emptyStateStatus = document.querySelector("#empty-state-status");
const emptyStateSearchStatus = document.querySelector("#empty-state-search-status");
const emptyStateFilters = document.querySelector("#empty-state-filters");
const inputFeedback = document.querySelector("#input-feedback");
const taskFeedback = document.querySelector("#task-feedback");
const bulkControls = document.querySelector("#bulk-controls");
const bulkActions = document.querySelector("#bulk-actions");
const selectAllVisible = document.querySelector("#select-all-visible");
const bulkSelectionCount = document.querySelector("#bulk-selection-count");
const bulkStatus = document.querySelector("#bulk-status");
const bulkApplyStatusButton = document.querySelector("#bulk-apply-status");
const bulkDeleteButton = document.querySelector("#bulk-delete");
const bulkConfirmDialog = document.querySelector("#bulk-confirm-dialog");
const bulkConfirmDescription = document.querySelector("#bulk-confirm-description");
const bulkCancelDeleteButton = document.querySelector("#bulk-cancel-delete");
const bulkConfirmDeleteButton = document.querySelector("#bulk-confirm-delete");
const taskLoading = document.querySelector("#task-loading");
const taskLoadError = document.querySelector("#task-load-error");
const taskLoadErrorMessage = document.querySelector("#task-load-error-message");
const retryTaskLoadButton = document.querySelector("#retry-task-load-button");
const paginationFeedback = document.querySelector("#pagination-feedback");
const loadMoreButton = document.querySelector("#load-more-button");
const recentlyDeletedSection = document.querySelector("#recently-deleted-section");
const deletedLoading = document.querySelector("#deleted-loading");
const deletedLoadError = document.querySelector("#deleted-load-error");
const deletedLoadErrorMessage = document.querySelector("#deleted-load-error-message");
const retryDeletedLoadButton = document.querySelector("#retry-deleted-load-button");
const deletedEmpty = document.querySelector("#deleted-empty");
const deletedMoreButton = document.querySelector("#deleted-more-button");
const deletedPaginationFeedback = document.querySelector("#deleted-pagination-feedback");
const activityDialog = document.querySelector("#activity-dialog");
const activityTaskTitle = document.querySelector("#activity-task-title");
const activityDialogContent = document.querySelector("#activity-dialog-content");
const activityTaskFooter = document.querySelector("#activity-task-footer");
const activityEditTaskButton = document.querySelector("#activity-edit-task");
const activityDeleteTaskButton = document.querySelector("#activity-delete-task");
const activityLoading = document.querySelector("#activity-loading");
const activityError = document.querySelector("#activity-error");
const activityErrorMessage = document.querySelector("#activity-error-message");
const activityRetryButton = document.querySelector("#activity-retry-button");
const activityEmpty = document.querySelector("#activity-empty");
const activityList = document.querySelector("#activity-list");
const activityMoreButton = document.querySelector("#activity-more-button");
const activityActionFeedback = document.querySelector("#activity-action-feedback");
const templateList = document.querySelector("#template-list");
const templateFeedback = document.querySelector("#template-feedback");
const templateLoading = document.querySelector("#template-loading");
const templateEmpty = document.querySelector("#template-empty");
const templateEditorDialog = document.querySelector("#template-editor-dialog");
const templateEditorForm = document.querySelector("#template-editor-form");
const templateEditorTitle = document.querySelector("#template-editor-title");
const templateNameInput = document.querySelector("#template-name");
const templateDescriptionInput = document.querySelector("#template-description");
const templateItemFields = document.querySelector("#template-item-fields");
const templateEditorError = document.querySelector("#template-editor-error");
const templateItemsEmpty = document.querySelector("#template-items-empty");
const templateSaveButton = document.querySelector("#template-save-button");
const templateTaskPicker = document.querySelector("#template-task-picker");
const templateTaskPickerSearch = document.querySelector("#template-task-picker-search");
const templateTaskPickerList = document.querySelector("#template-picker-list");
const templateTaskPickerLoading = document.querySelector("#template-picker-loading");
const templateTaskPickerError = document.querySelector("#template-picker-error");
const templateTaskPickerEmpty = document.querySelector("#template-picker-empty");
const templateTaskPickerMore = document.querySelector("#template-picker-more");
const templateDetailDialog = document.querySelector("#template-detail-dialog");
const templateApplyDialog = document.querySelector("#template-apply-dialog");

let templateState = { ownerId: null, items: [], loading: false, pending: false, editorId: null, editorVersion: null, active: null, applying: null, requestId: 0 };
let templatePickerState = { ownerId: null, items: [], query: "", cursor: null, loading: false, loadingMore: false, error: "", requestId: 0 };
let templatePickerSearchTimer = null;

function renderTemplates() {
  templateLoading.hidden = !templateState.loading;
  templateEmpty.hidden = templateState.loading || templateState.items.length > 0;
  templateList.replaceChildren();
  for (const item of templateState.items) {
    const row = document.createElement("li"); row.className = "template-item";
    const content = document.createElement("div"); content.className = "template-content";
    const title = document.createElement("h3"); title.textContent = item.name;
    const detail = document.createElement("p"); detail.textContent = `${item.item_count} task${item.item_count === 1 ? "" : "s"}${item.description ? ` · ${item.description}` : ""}`;
    content.append(title, detail);
    const actions = document.createElement("div"); actions.className = "template-actions";
    for (const [action, label] of [["view", "View"]]) {
      const button = document.createElement("button"); button.type = "button"; button.className = "secondary-button";
      button.dataset.templateAction = action; button.dataset.templateId = item.id; button.textContent = label;
      button.disabled = templateState.pending; actions.append(button);
    }
    row.append(content, actions); templateList.append(row);
  }
}

function setTemplateFeedback(message, kind = "error") {
  templateFeedback.textContent = message; templateFeedback.dataset.kind = kind; templateFeedback.hidden = !message;
}

function templateErrorMessage(error, fallback) {
  if (error instanceof TypeError) return "Couldn't reach the server. Check your connection and try again.";
  return error?.message || fallback;
}

async function loadTemplates() {
  const ownerId = currentUser?.id; if (!ownerId || templateState.ownerId !== ownerId) return;
  const requestId = ++templateState.requestId; templateState.loading = true; renderTemplates();
  try {
    const { response, data } = await taskRequest("/api/task-templates");
    if (requestId !== templateState.requestId || currentUser?.id !== ownerId || templateState.ownerId !== ownerId) return;
    if (response.status === 401) { expireTaskSession(taskSessionGeneration, ownerId); return; }
    if (!response.ok || !Array.isArray(data?.items)) throw new Error(taskErrorMessage(response, data, "We couldn't load your templates."));
    templateState.items = data.items; setTemplateFeedback("");
  } catch (error) {
    if (requestId === templateState.requestId && currentUser?.id === ownerId) setTemplateFeedback(templateErrorMessage(error, "We couldn't load your templates."));
  } finally { if (requestId === templateState.requestId) { templateState.loading = false; renderTemplates(); } }
}

function resetTemplateState(ownerId = null) {
  templateState.requestId += 1; templateState = { ownerId, items: [], loading: false, pending: false, editorId: null, editorVersion: null, active: null, applying: null, requestId: templateState.requestId };
  templatePickerState.requestId += 1;
  templatePickerState = { ownerId, items: [], query: "", cursor: null, loading: false, loadingMore: false, error: "", requestId: templatePickerState.requestId };
  clearTimeout(templatePickerSearchTimer);
  templateTaskPicker.hidden = true;
  templateTaskPickerSearch.value = "";
  templateTaskPickerList.replaceChildren();
  templateEditorDialog.close(); templateDetailDialog.close(); templateApplyDialog.close();
  templateEditorForm.reset(); templateItemFields.replaceChildren(); setTemplateFeedback(""); renderTemplates();
}

function addTemplateItemRow(item = {}) {
  if (templateItemFields.children.length >= 50) return;
  const fieldset = document.createElement("fieldset"); fieldset.className = "template-item-fields";
  if (item.id) fieldset.dataset.templateItemId = item.id;
  const number = templateItemFields.children.length + 1; const titleID = `template-item-title-${number}`; const descID = `template-item-description-${number}`;
  const titleLabel = document.createElement("label"); titleLabel.className = "input-label"; titleLabel.htmlFor = titleID; titleLabel.textContent = `Task ${number} title`;
  titleLabel.dataset.templateLabel = "title";
  const title = document.createElement("input"); title.type = "text"; title.id = titleID; title.maxLength = 200; title.required = true; title.value = item.title || "";
  const descLabel = document.createElement("label"); descLabel.className = "input-label"; descLabel.htmlFor = descID; descLabel.textContent = "Description (optional)";
  descLabel.dataset.templateLabel = "description";
  const description = document.createElement("textarea"); description.id = descID; description.rows = 2; description.maxLength = 10000; description.value = item.description || "";
  const priorityID = `template-item-priority-${number}`;
  const priorityLabel = document.createElement("label"); priorityLabel.className = "input-label"; priorityLabel.htmlFor = priorityID; priorityLabel.textContent = `Task ${number} priority`; priorityLabel.dataset.templateLabel = "priority";
  const priority = document.createElement("select"); priority.id = priorityID; priority.className = "task-priority-select"; appendPriorityOptions(priority); priority.value = item.priority || "MEDIUM";
  const dueOffsetID = `template-item-due-offset-${number}`;
  const dueOffsetLabel = document.createElement("label"); dueOffsetLabel.className = "input-label"; dueOffsetLabel.htmlFor = dueOffsetID; dueOffsetLabel.textContent = `Task ${number} due in days (optional)`; dueOffsetLabel.dataset.templateLabel = "due-offset";
  const dueOffset = document.createElement("input"); dueOffset.id = dueOffsetID; dueOffset.type = "number"; dueOffset.min = "0"; dueOffset.max = "3650"; dueOffset.step = "1"; dueOffset.value = Number.isInteger(item.due_in_days) ? String(item.due_in_days) : "";
  const remove = document.createElement("button"); remove.type = "button"; remove.className = "secondary-button"; remove.textContent = "Remove item"; remove.addEventListener("click", () => { fieldset.remove(); renumberTemplateItems(); renderTemplateTaskPicker(); });
  fieldset.append(titleLabel, title, descLabel, description, priorityLabel, priority, dueOffsetLabel, dueOffset, remove); templateItemFields.append(fieldset);
  templateItemsEmpty.hidden = templateItemFields.children.length > 0;
  renderTemplateTaskPicker();
}

function renumberTemplateItems() {
  [...templateItemFields.children].forEach((row, index) => {
    const number = index + 1;
    const titleLabel = row.querySelector('[data-template-label="title"]');
    const title = row.querySelector("input");
    const descriptionLabel = row.querySelector('[data-template-label="description"]');
    const description = row.querySelector("textarea");
    const priorityLabel = row.querySelector('[data-template-label="priority"]');
    const priority = row.querySelector("select");
    const dueOffsetLabel = row.querySelector('[data-template-label="due-offset"]');
    const dueOffset = row.querySelector('input[type="number"]');
    title.id = `template-item-title-${number}`; titleLabel.htmlFor = title.id; titleLabel.textContent = `Task ${number} title`;
    description.id = `template-item-description-${number}`; descriptionLabel.htmlFor = description.id;
    priority.id = `template-item-priority-${number}`; priorityLabel.htmlFor = priority.id; priorityLabel.textContent = `Task ${number} priority`;
    dueOffset.id = `template-item-due-offset-${number}`; dueOffsetLabel.htmlFor = dueOffset.id; dueOffsetLabel.textContent = `Task ${number} due in days (optional)`;
  });
  templateItemsEmpty.hidden = templateItemFields.children.length > 0;
  renderTemplateTaskPicker();
}

function normalizedTemplateTitle(value) {
  return value.trim().normalize("NFC").toLocaleLowerCase();
}

function currentTemplateTitles() {
  return new Set([...templateItemFields.querySelectorAll('input[type="text"]')].map((input) => normalizedTemplateTitle(input.value)).filter(Boolean));
}

function renderTemplateTaskPicker() {
  const hasItems = templatePickerState.items.length > 0;
  const busy = templatePickerState.loading || templatePickerState.loadingMore;
  templateTaskPickerLoading.hidden = !busy;
  templateTaskPickerError.textContent = templatePickerState.error;
  templateTaskPickerError.hidden = !templatePickerState.error;
  templateTaskPickerEmpty.hidden = busy || Boolean(templatePickerState.error) || hasItems;
  templateTaskPickerMore.hidden = !templatePickerState.cursor || Boolean(templatePickerState.error);
  templateTaskPickerMore.disabled = busy;
  templateTaskPickerMore.textContent = templatePickerState.loadingMore ? "Loading…" : "Load more tasks";
  templateTaskPickerList.replaceChildren();
  const titles = currentTemplateTitles();
  for (const task of templatePickerState.items) {
    const row = document.createElement("li");
    const content = document.createElement("div");
    const title = document.createElement("strong");
    title.textContent = task.title;
    const details = document.createElement("p");
    details.textContent = `${priorityLabel(task.priority)} priority${task.due_date ? ` · Due ${formatCivilDate(task.due_date)}` : ""}`;
    content.append(title, details);
    const add = document.createElement("button");
    add.type = "button";
    add.className = "secondary-button";
    add.textContent = titles.has(normalizedTemplateTitle(task.title)) ? "Already added" : "Add";
    add.disabled = templateState.pending || templateItemFields.children.length >= 50 || titles.has(normalizedTemplateTitle(task.title));
    add.setAttribute("aria-label", `${add.textContent}: ${task.title}`);
    add.addEventListener("click", () => {
      if (templateItemFields.children.length >= 50 || currentTemplateTitles().has(normalizedTemplateTitle(task.title))) return;
      addTemplateItemRow({ title: task.title, description: task.description, priority: task.priority });
      renderTemplateTaskPicker();
    });
    row.append(content, add);
    templateTaskPickerList.append(row);
  }
  const manualButton = document.querySelector("#template-add-manual");
  manualButton.disabled = templateItemFields.children.length >= 50;
}

async function loadTemplatePickerTasks(reset = false) {
  const ownerId = currentUser?.id;
  if (!ownerId) return;
  if (reset) {
    templatePickerState.requestId += 1;
    templatePickerState = {
      ownerId,
      items: [],
      query: templateTaskPickerSearch.value.trim(),
      cursor: null,
      loading: true,
      loadingMore: false,
      error: "",
      requestId: templatePickerState.requestId,
    };
  } else {
    if (templatePickerState.loading || templatePickerState.loadingMore || !templatePickerState.cursor) return;
    templatePickerState.loadingMore = true;
    templatePickerState.error = "";
  }
  const generation = taskSessionGeneration;
  const requestId = templatePickerState.requestId;
  const query = templatePickerState.query;
  const cursor = reset ? null : templatePickerState.cursor;
  renderTemplateTaskPicker();
  const params = new URLSearchParams({ limit: "100" });
  if (query) params.set("q", query);
  if (cursor) params.set("cursor", cursor);
  try {
    const { response, data } = await taskRequest(`/api/tasks?${params.toString()}`);
    if (!taskIsCurrent(generation, ownerId) || requestId !== templatePickerState.requestId || templatePickerState.ownerId !== ownerId) return;
    if (response.status === 401) { expireTaskSession(generation, ownerId); return; }
    if (!response.ok) throw new Error(taskErrorMessage(response, data, "Couldn't load your tasks."));
    if (!Array.isArray(data?.items) || !(data.next_cursor === null || typeof data.next_cursor === "string")) throw new Error("The task list response was invalid.");
    const incoming = data.items.filter(isTaskRecord);
    if (incoming.length !== data.items.length) throw new Error("The task list contained invalid data.");
    if (reset) templatePickerState.items = incoming;
    else {
      const knownIds = new Set(templatePickerState.items.map((item) => item.id));
      templatePickerState.items.push(...incoming.filter((item) => !knownIds.has(item.id)));
    }
    templatePickerState.cursor = data.next_cursor;
  } catch (error) {
    if (!taskIsCurrent(generation, ownerId) || requestId !== templatePickerState.requestId) return;
    templatePickerState.error = templateErrorMessage(error, "Couldn't load your tasks.");
  } finally {
    if (requestId === templatePickerState.requestId) {
      templatePickerState.loading = false;
      templatePickerState.loadingMore = false;
      renderTemplateTaskPicker();
    }
  }
}

function openTemplateEditor(template = null) {
  templateState.editorId = template?.id || null; templateState.editorVersion = template?.version || null;
  templateEditorTitle.textContent = template ? "Edit template" : "Create template"; templateSaveButton.textContent = template ? "Save changes" : "Save template";
  templateNameInput.value = template?.name || ""; templateDescriptionInput.value = template?.description || "";
  templateTaskPicker.hidden = true;
  templateTaskPickerSearch.value = "";
  templatePickerState = { ownerId: currentUser?.id || null, items: [], query: "", cursor: null, loading: false, loadingMore: false, error: "", requestId: templatePickerState.requestId + 1 };
  templateTaskPickerList.replaceChildren();
  templateItemFields.replaceChildren();
  for (const item of template?.items || []) addTemplateItemRow(item);
  templateItemsEmpty.hidden = templateItemFields.children.length > 0;
  templateEditorError.hidden = true; templateEditorDialog.showModal(); templateNameInput.focus();
}

async function openTemplateDetail(summary) {
  const ownerId = currentUser?.id; if (!ownerId) return;
  const generation = taskSessionGeneration;
  templateState.active = null;
  try {
    const { response, data } = await taskRequest(`/api/task-templates/${summary.id}`);
    if (generation !== taskSessionGeneration || currentUser?.id !== ownerId) return;
    if (response.status === 401) { expireTaskSession(generation, ownerId); return; }
    if (!response.ok) throw new Error(taskErrorMessage(response, data, "Couldn't load template details."));
    templateState.active = data; document.querySelector("#template-detail-title").textContent = data.name;
    document.querySelector("#template-detail-description").textContent = data.description || "";
    const list = document.querySelector("#template-detail-items"); list.replaceChildren();
    for (const item of data.items) { const row = document.createElement("li"); const title = document.createElement("strong"); title.textContent = item.title; const description = document.createElement("p"); description.textContent = item.description; const priority = document.createElement("p"); priority.textContent = `Priority: ${priorityLabel(item.priority)}`; row.append(title, description, priority); if (Number.isInteger(item.due_in_days)) { const due = document.createElement("p"); due.textContent = `Due ${item.due_in_days === 0 ? "on the day this template is used" : `${item.due_in_days} days after this template is used`}`; row.append(due); } list.append(row); }
    document.querySelector("#template-detail-feedback").hidden = true; templateDetailDialog.showModal();
    return true;
  } catch (error) { setTemplateFeedback(templateErrorMessage(error, "Couldn't load template details.")); }
  return false;
}

async function saveTemplate(event) {
  event.preventDefault(); if (templateState.pending || !currentUser) return;
  if (templateItemFields.children.length < 1) {
    templateEditorError.textContent = "Add at least one task to the template.";
    templateEditorError.hidden = false;
    return;
  }
  const ownerId = currentUser.id; const generation = taskSessionGeneration;
  const items = [...templateItemFields.children].map((row) => {
    const offset = row.querySelector('input[type="number"]').value;
    return { ...(row.dataset.templateItemId ? { id: row.dataset.templateItemId } : {}), title: row.querySelector('input[type="text"]').value, description: row.querySelector("textarea").value, priority: row.querySelector("select").value, due_in_days: offset === "" ? null : Number(offset) };
  });
  const body = { name: templateNameInput.value, description: templateDescriptionInput.value, items };
  if (templateState.editorVersion !== null) body.version = templateState.editorVersion;
  templateState.pending = true; templateSaveButton.disabled = true; templateEditorForm.setAttribute("aria-busy", "true");
  try {
    const path = templateState.editorId ? `/api/task-templates/${templateState.editorId}` : "/api/task-templates";
    const { response, data } = await taskRequest(path, { method: templateState.editorId ? "PUT" : "POST", body });
    if (generation !== taskSessionGeneration || currentUser?.id !== ownerId) return;
    if (response.status === 401) { expireTaskSession(generation, ownerId); return; }
    if (!response.ok) throw new Error(taskErrorMessage(response, data, "Couldn't save the template."));
    templateEditorDialog.close(); setTemplateFeedback(templateState.editorId ? "Template updated." : "Template created.", "success"); await loadTemplates();
  } catch (error) { templateEditorError.textContent = templateErrorMessage(error, "Couldn't save the template."); templateEditorError.hidden = false; }
  finally { templateState.pending = false; templateSaveButton.disabled = false; templateEditorForm.removeAttribute("aria-busy"); renderTemplates(); }
}

async function deleteTemplate(summary) {
  if (templateState.pending || !window.confirm(`Delete template “${summary.name}”? Existing tasks will not be affected.`)) return;
  const ownerId = currentUser?.id; const generation = taskSessionGeneration; if (!ownerId) return;
  templateState.pending = true;
  const deleteButton = document.querySelector("#template-detail-delete");
  deleteButton.disabled = true;
  renderTemplates();
  try {
    const { response, data } = await taskRequest(`/api/task-templates/${summary.id}`, { method: "DELETE", body: { version: summary.version } });
    if (generation !== taskSessionGeneration || currentUser?.id !== ownerId) return;
    if (response.status === 401) { expireTaskSession(generation, ownerId); return; }
    if (!response.ok) throw new Error(taskErrorMessage(response, data, "Couldn't delete the template."));
    templateDetailDialog.close();
    templateState.active = null;
    setTemplateFeedback("Template deleted. Existing tasks were not changed.", "success"); await loadTemplates();
  } catch (error) {
    const feedback = document.querySelector("#template-detail-feedback");
    feedback.textContent = templateErrorMessage(error, "Couldn't delete the template.");
    feedback.hidden = false;
  }
  finally { templateState.pending = false; deleteButton.disabled = false; renderTemplates(); }
}

async function applyTemplate() {
  const template = templateState.applying; if (!template || templateState.pending || !currentUser) return;
  templateState.pending = true; document.querySelector("#template-apply-confirm").disabled = true;
  const ownerId = currentUser.id; const generation = taskSessionGeneration;
  try {
    const { response, data } = await taskRequest(`/api/task-templates/${template.id}/apply`, { method: "POST", body: { version: template.version, mode: templateState.applyingMode || "incremental", time_zone: activeTimeZone } });
    if (!taskIsCurrent(generation, ownerId)) return;
    if (response.status === 401) { expireTaskSession(generation, ownerId); return; }
    if (!response.ok) throw new Error(taskErrorMessage(response, data, "Couldn't create tasks from this template."));
    if (!Array.isArray(data?.items)) throw new Error("The server returned an invalid result. Refresh tasks before retrying.");
    templateApplyDialog.close(); templateDetailDialog.close(); taskMutationGeneration += 1; await loadTasks(true);
    const message = templateState.applyingMode === "new_set"
      ? `${data.items.length} task${data.items.length === 1 ? "" : "s"} created as a new set from “${template.name}”.`
      : data.items.length
        ? `${data.items.length} new template task${data.items.length === 1 ? " was" : "s were"} added to the latest set from “${template.name}”.`
        : `No new items to add. The latest set already includes every task in “${template.name}”. Create a new set if you want another full copy.`;
    setTaskFeedback(message, "success");
  } catch (error) {
    templateApplyDialog.close();
    if (templateState.active && currentUser) {
      const feedback = document.querySelector("#template-detail-feedback");
      feedback.textContent = templateErrorMessage(error, "Couldn't create tasks from this template.");
      feedback.hidden = false;
      templateDetailDialog.showModal();
    } else {
      setTaskFeedback(templateErrorMessage(error, "Couldn't create tasks from this template."));
    }
  }
  finally { templateState.pending = false; document.querySelector("#template-apply-confirm").disabled = false; renderTemplates(); }
}

document.querySelector("#new-template-button").addEventListener("click", () => openTemplateEditor());
document.querySelector("#add-template-item").addEventListener("click", () => {
  templateTaskPicker.hidden = false;
  templateTaskPickerSearch.focus();
  if (templatePickerState.ownerId !== currentUser?.id || (!templatePickerState.items.length && !templatePickerState.loading)) void loadTemplatePickerTasks(true);
  else renderTemplateTaskPicker();
});
document.querySelector("#template-task-picker-close").addEventListener("click", () => { templateTaskPicker.hidden = true; document.querySelector("#add-template-item").focus(); });
templateTaskPickerSearch.addEventListener("input", () => {
  clearTimeout(templatePickerSearchTimer);
  templatePickerSearchTimer = setTimeout(() => { void loadTemplatePickerTasks(true); }, 220);
});
templateTaskPickerMore.addEventListener("click", () => { void loadTemplatePickerTasks(false); });
document.querySelector("#template-add-manual").addEventListener("click", () => {
  addTemplateItemRow();
  templateTaskPicker.hidden = true;
  templateItemFields.lastElementChild?.querySelector('input[type="text"]')?.focus();
});
templateEditorForm.addEventListener("submit", saveTemplate);
for (const id of ["template-editor-close", "template-editor-cancel"]) document.querySelector(`#${id}`).addEventListener("click", () => templateEditorDialog.close());
for (const [dialog, button] of [[templateDetailDialog, "template-detail-close"]]) document.querySelector(`#${button}`).addEventListener("click", () => dialog.close());
document.querySelector("#template-detail-edit").addEventListener("click", () => { const item = templateState.active; templateDetailDialog.close(); if (item) openTemplateEditor(item); });
document.querySelector("#template-detail-delete").addEventListener("click", () => { if (templateState.active) void deleteTemplate(templateState.active); });
function confirmTemplateApply(mode) {
  const item = templateState.active;
  if (!item) return;
  templateState.applying = item;
  templateState.applyingMode = mode;
  const description = document.querySelector("#template-apply-description");
  const confirmButton = document.querySelector("#template-apply-confirm");
  if (mode === "new_set") {
    document.querySelector("#template-apply-title").textContent = "Create a complete new task set?";
    description.textContent = `This creates all ${item.items.length} template tasks as a separate new set. Existing tasks and earlier sets are unchanged.`;
    confirmButton.textContent = "Create new set";
  } else {
    document.querySelector("#template-apply-title").textContent = "Add new template items?";
    description.textContent = `This adds only items not yet added to the current set from “${item.name}”. If no set exists, this starts the first one. Existing items will not be duplicated.`;
    confirmButton.textContent = "Add new items";
  }
  confirmButton.disabled = false;
  // Keep dialogs sequential: nested native dialogs create overlapping backdrops
  // and can leave the template detail dialog with a second scrollbar.
  templateDetailDialog.close();
  templateApplyDialog.showModal();
  document.querySelector("#template-apply-cancel").focus();
}
document.querySelector("#template-detail-apply").addEventListener("click", () => confirmTemplateApply("incremental"));
document.querySelector("#template-detail-new-set").addEventListener("click", () => confirmTemplateApply("new_set"));
function dismissTemplateApplyConfirmation() {
  templateApplyDialog.close();
  if (templateState.active && currentUser) templateDetailDialog.showModal();
}
document.querySelector("#template-apply-cancel").addEventListener("click", dismissTemplateApplyConfirmation);
templateApplyDialog.addEventListener("cancel", (event) => {
  event.preventDefault();
  dismissTemplateApplyConfirmation();
});
document.querySelector("#template-apply-confirm").addEventListener("click", () => { void applyTemplate(); });
templateList.addEventListener("click", (event) => {
  const button = event.target.closest('button[data-template-action="view"]');
  if (!button) return;
  const summary = templateState.items.find((item) => item.id === button.dataset.templateId);
  if (summary) void openTemplateDetail(summary);
});
document.querySelector("#template-detail-close").addEventListener("click", () => templateDetailDialog.close());

let taskWorkspace = createEmptyTaskWorkspace();
let taskSessionGeneration = 0;
let taskLoadGeneration = 0;
let taskFilterTimer = null;
let deletedLoadGeneration = 0;
let completedLoadGeneration = 0;
let taskMutationGeneration = 0;
let taskCreatePending = false;
let bulkPending = false;
let editingTaskId = null;
let activityState = { taskId: null, ownerId: null, task: null, items: [], nextCursor: null, loading: false, loadingMore: false, error: "", requestId: 0 };
let activityRequestPromise = null;
const busyTaskIds = new Set();

function createEmptyTaskWorkspace(ownerId = null) {
  return {
    ownerId,
    items: [],
    nextCursor: null,
    hasMore: false,
    loading: false,
    loadingMore: false,
    loadError: false,
    pageError: "",
    searchQuery: "",
    statusFilter: "",
    priorityFilter: "",
    dueFilter: "",
    recentlyDeleted: [],
    deletedCount: null,
    deletedNextCursor: null,
    deletedHasMore: false,
    deletedLoading: false,
    deletedLoadingMore: false,
    deletedLoadError: "",
    completedItems: [],
    completedPeriod: "all",
    completedTotalCount: null,
    completedNextCursor: null,
    completedHasMore: false,
    completedLoading: false,
    completedLoadingMore: false,
    completedLoadError: "",
    completedPageError: "",
    selectedIds: new Set(),
  };
}

async function initializeTaskWorkspace(user) {
  if (!user?.id) return;
  if (taskWorkspace.ownerId !== user.id) {
    taskSessionGeneration += 1;
    taskLoadGeneration += 1;
    taskMutationGeneration = 0;
    bulkPending = false;
    taskWorkspace = createEmptyTaskWorkspace(user.id);
    completedCountBadge.hidden = true;
    completedCountBadge.textContent = "";
    taskSearchInput.value = "";
    taskStatusFilter.value = "";
      taskPriorityFilter.value = "";
    taskDueFilter.value = "";
    completedPeriodFilter.value = "all";
    busyTaskIds.clear();
    editingTaskId = null;
    resetTaskFormControls();
    clearTaskFeedback();
    taskForm.reset();
    resetTemplateState(user.id);
  }
  const generation = taskSessionGeneration;
  const preferenceGeneration = ++timeZoneRequestGeneration;
  await loadTimeZonePreference(user.id, generation, preferenceGeneration);
  if (!taskIsCurrent(generation, user.id) || preferenceGeneration !== timeZoneRequestGeneration) return;
  void loadTasks(true);
  if (user.role === "ADMIN") void loadAdminAccounts();
  void loadDeletedTaskCount();
  if (workspaceScreen === "deleted") void loadDeletedTasks(true);
  else void loadDeletedTasks(true, DELETED_PREVIEW_SIZE);
  if (workspaceScreen === "completed") void loadCompletedTasks(true);
  void loadTemplates();
}

async function loadAdminAccounts(append = false, preserveFeedback = false) {
  if (currentUser?.role !== "ADMIN") return;
  const actorId = currentUser.id;
  const cursor = append ? adminAccountCursor : null;
  if (!append) {
    adminAccountCursor = null;
    adminAccountList.replaceChildren();
    if (!preserveFeedback) adminAccountFeedback.textContent = "";
  }
  adminAccountError.hidden = true;
  adminAccountLoading.hidden = false;
  adminAccountList.setAttribute("aria-busy", "true");
  try {
    const query = new URLSearchParams({ limit: "100" });
    if (cursor) query.set("cursor", cursor);
    const { response, data } = await taskRequest(`/api/admin/users?${query.toString()}`);
    if (currentUser?.id !== actorId) return false;
    if (!response.ok || !Array.isArray(data?.items)) throw new Error("We couldn't load account administration.");
    for (const account of data.items) renderAdminAccount(account);
    adminAccountCursor = typeof data.next_cursor === "string" ? data.next_cursor : null;
    adminAccountList.querySelector(".admin-load-more")?.remove();
    const more = document.createElement("button");
    more.type = "button";
    more.className = "secondary-button admin-load-more";
    more.textContent = "Load more accounts";
    more.hidden = !adminAccountCursor;
    more.addEventListener("click", () => void loadAdminAccounts(true));
    adminAccountList.append(more);
    return true;
  } catch {
    if (currentUser?.id === actorId) {
      adminAccountError.textContent = "Account administration is unavailable. Refresh and try again.";
      adminAccountError.hidden = false;
    }
    return false;
  } finally {
    adminAccountLoading.hidden = true;
    adminAccountList.setAttribute("aria-busy", "false");
  }
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
    adminAccountFeedback.textContent = refreshed ? successMessage : "The account change succeeded, but the list could not be refreshed.";
  } catch (error) {
    adminAccountFeedback.textContent = error instanceof Error ? error.message : "The account change could not be completed.";
  }
}

function validBrowserTimeZone(value) {
  if (typeof value !== "string" || !value || value.length > 100 || value === "Local") return false;
  try {
    new Intl.DateTimeFormat("en", { timeZone: value });
    return true;
  } catch {
    return false;
  }
}

function renderTimeZonePreference() {
  if (!timeZonePreferenceSummary || !timeZoneOverrideInput) return;
  const isDevice = activeTimeZone === DEVICE_TIME_ZONE;
  timeZonePreferenceSummary.textContent = isDevice ? `Using your device time zone (${DEVICE_TIME_ZONE})` : `Using ${activeTimeZone}`;
  timeZoneOverrideInput.value = isDevice ? "" : activeTimeZone;
}

async function loadTimeZonePreference(ownerId, generation, preferenceGeneration) {
  try {
    const { response, data } = await taskRequest("/api/preferences");
    if (!taskIsCurrent(generation, ownerId) || preferenceGeneration !== timeZoneRequestGeneration) return;
    if (response.status === 401) {
      expireTaskSession(generation, ownerId);
      return;
    }
    if (!response.ok || !data || !(data.time_zone === null || validBrowserTimeZone(data.time_zone))) {
      throw new Error("Preference response was invalid.");
    }
    activeTimeZone = data.time_zone || DEVICE_TIME_ZONE;
    renderTimeZonePreference();
    if (timeZonePreferenceFeedback) timeZonePreferenceFeedback.hidden = true;
  } catch {
    if (!taskIsCurrent(generation, ownerId) || preferenceGeneration !== timeZoneRequestGeneration) return;
    activeTimeZone = DEVICE_TIME_ZONE;
    renderTimeZonePreference();
    if (timeZonePreferenceFeedback) {
      timeZonePreferenceFeedback.textContent = "We couldn't load your saved time zone. Using this device's time zone until preferences can be refreshed.";
      timeZonePreferenceFeedback.dataset.kind = "error";
      timeZonePreferenceFeedback.hidden = false;
    }
  }
}

async function saveTimeZonePreference(timeZone) {
  if (!currentUser?.id) return;
  const requested = timeZone === null ? DEVICE_TIME_ZONE : timeZone;
  if (!validBrowserTimeZone(requested)) {
    if (timeZonePreferenceFeedback) {
      timeZonePreferenceFeedback.textContent = "Enter a valid IANA time zone, such as America/New_York.";
      timeZonePreferenceFeedback.dataset.kind = "error";
      timeZonePreferenceFeedback.hidden = false;
    }
    return;
  }
  const ownerId = currentUser.id;
  const generation = taskSessionGeneration;
  const { response, data } = await taskRequest("/api/preferences", {
    method: "PUT",
    body: { time_zone: timeZone },
  }).catch(() => ({ response: null, data: null }));
  if (!taskIsCurrent(generation, ownerId)) return;
  if (!response?.ok || data?.time_zone !== timeZone) {
    if (response?.status === 401) expireTaskSession(generation, ownerId);
    if (timeZonePreferenceFeedback) {
      timeZonePreferenceFeedback.textContent = response ? taskErrorMessage(response, data, "We couldn't save the time zone.") : "We couldn't reach the preference service. Try again.";
      timeZonePreferenceFeedback.dataset.kind = "error";
      timeZonePreferenceFeedback.hidden = false;
    }
    return;
  }
  activeTimeZone = requested;
  renderTimeZonePreference();
  if (timeZonePreferenceFeedback) {
    timeZonePreferenceFeedback.textContent = timeZone === null ? "Using this device's time zone." : "Time zone preference saved.";
    timeZonePreferenceFeedback.dataset.kind = "success";
    timeZonePreferenceFeedback.hidden = false;
  }
  lastLocalCalendarDate = currentAccountDate();
  taskWorkspace.selectedIds.clear();
  void loadTasks(true);
  void loadDeletedTasks(true, workspaceScreen === "deleted" ? TASK_PAGE_SIZE : DELETED_PREVIEW_SIZE);
  void loadCompletedTasks(true);
  void loadTemplates();
}

function clearTaskWorkspace() {
  clearTimeout(taskFilterTimer);
  taskSearchInput.value = "";
  taskStatusFilter.value = "";
  taskPriorityFilter.value = "";
  taskDueFilter.value = "";
  taskSessionGeneration += 1;
  taskLoadGeneration += 1;
  taskMutationGeneration = 0;
  taskWorkspace = createEmptyTaskWorkspace();
  renderDeletedTaskRows();
  completedCountBadge.hidden = true;
  completedCountBadge.textContent = "";
  resetTemplateState(null);
  activityState = { taskId: null, ownerId: null, task: null, items: [], nextCursor: null, loading: false, loadingMore: false, error: "", requestId: activityState.requestId + 1 };
  activityRequestPromise = null;
  activityDialog.close();
  bulkConfirmDialog.close();
  renderActivity();
  busyTaskIds.clear();
  editingTaskId = null;
  taskCreatePending = false;
  bulkPending = false;
  resetTaskFormControls();
  taskForm.reset();
  taskOptionsDetails.open = false;
  clearTaskFeedback();
  renderTaskScreen();
}

function resetTaskFormControls() {
  taskForm.removeAttribute("aria-busy");
  taskInput.disabled = false;
  taskDescriptionInput.disabled = false;
  taskPriorityInput.disabled = false;
  const submitButton = taskForm.querySelector('button[type="submit"]');
  submitButton.disabled = false;
  submitButton.textContent = "Add Task";
}

function clearTaskFeedback() {
  inputFeedback.textContent = "";
  taskFeedback.textContent = "";
  taskFeedback.hidden = true;
  taskInput.removeAttribute("aria-invalid");
}

function setTaskFeedback(message, kind = "error") {
  taskFeedback.textContent = message;
  taskFeedback.dataset.kind = kind;
  taskFeedback.hidden = !message;
}

function setDeletedViewFeedback(message, kind = "success") {
  recentlyDeletedViewFeedback.textContent = message;
  recentlyDeletedViewFeedback.dataset.kind = kind;
  recentlyDeletedViewFeedback.hidden = !message;
}

function taskIsCurrent(generation, ownerId) {
  return generation === taskSessionGeneration && currentUser?.id === ownerId && taskWorkspace.ownerId === ownerId;
}

async function taskRequest(path, { method = "GET", body } = {}) {
  const options = {
    method,
    credentials: "same-origin",
    cache: "no-store",
    headers: { Accept: "application/json" },
  };
  if (body !== undefined) {
    options.headers["Content-Type"] = "application/json";
    options.body = JSON.stringify(body);
  }
  const response = await fetch(path, options);
  return { response, data: response.status === 204 ? null : await readJSON(response) };
}

function taskErrorMessage(response, data, fallback = "We couldn't complete that task request.") {
  switch (data?.error?.code) {
    case "invalid_task": return "Check the task title and description, then try again.";
    case "invalid_task_id": return "This task link is invalid. Refresh the task list and try again.";
    case "invalid_pagination": return "We couldn't load that page of tasks. Try again.";
    case "task_not_found": return "This task is no longer available.";
    case "version_conflict": return "This task changed elsewhere. Reload the latest version before saving.";
    case "task_archived": return "This task has been completed for more than 12 hours and is read-only. You can still view its history.";
    case "recovery_expired": return "This task is outside the 24-hour recovery window and can no longer be restored.";
    case "request_too_large": return "The task details are too long to send.";
    default:
      if (response?.status >= 500) return "The task service is temporarily unavailable. Please try again.";
      return fallback;
  }
}

function expireTaskSession(generation, ownerId) {
  if (!taskIsCurrent(generation, ownerId)) return;
  currentUser = null;
  clearTaskWorkspace();
  setAuthMode("login");
  setAuthState("unauthenticated");
  showAuthFeedback("Your session has expired. Sign in again to continue.");
  loginEmail.focus();
}

async function loadTasks(reset = false) {
  const ownerId = currentUser?.id;
  if (!ownerId || taskWorkspace.ownerId !== ownerId) return;
  if (reset) {
    taskLoadGeneration += 1;
    taskWorkspace.loading = true;
    taskWorkspace.loadingMore = false;
    taskWorkspace.loadError = false;
    taskWorkspace.pageError = "";
  } else {
    if (taskWorkspace.loading || taskWorkspace.loadingMore || !taskWorkspace.hasMore || !taskWorkspace.nextCursor) return;
    taskWorkspace.loadingMore = true;
    taskWorkspace.pageError = "";
  }
  const generation = taskSessionGeneration;
  const requestGeneration = taskLoadGeneration;
  const mutationGeneration = taskMutationGeneration;
  const cursor = reset ? null : taskWorkspace.nextCursor;
  renderTaskScreen();

  const path = new URLSearchParams({ limit: String(TASK_PAGE_SIZE) });
  if (taskWorkspace.searchQuery) path.set("q", taskWorkspace.searchQuery);
  if (taskWorkspace.statusFilter) path.set("status", taskWorkspace.statusFilter);
  if (taskWorkspace.priorityFilter) path.set("priority", taskWorkspace.priorityFilter);
  if (taskWorkspace.dueFilter) path.set("due", taskWorkspace.dueFilter);
  path.set("tz", activeTimeZone);
  if (cursor) path.set("cursor", cursor);
  try {
    const { response, data } = await taskRequest(`/api/tasks?${path.toString()}`);
    if (!taskIsCurrent(generation, ownerId) || requestGeneration !== taskLoadGeneration) return;
    if (response.status === 401) {
      expireTaskSession(generation, ownerId);
      return;
    }
    if (!response.ok) {
      const message = taskErrorMessage(response, data, "We couldn't load your tasks.");
      if (reset) {
        taskWorkspace.loading = false;
        taskWorkspace.loadError = true;
        taskLoadErrorMessage.textContent = message;
      } else {
        taskWorkspace.loadingMore = false;
        taskWorkspace.pageError = message;
      }
      renderTaskScreen();
      return;
    }
    if (!data || !Array.isArray(data.items) || !(data.next_cursor === null || typeof data.next_cursor === "string")) {
      throw new Error("The server returned an unexpected task list.");
    }
    if (mutationGeneration !== taskMutationGeneration) {
      void loadTasks(true);
      return;
    }
    const incoming = data.items.filter(isTaskRecord);
    if (incoming.length !== data.items.length) throw new Error("The server returned invalid task data.");
    if (reset) {
      taskWorkspace.items = incoming;
      const incomingIds = new Set(incoming.map((item) => item.id));
      taskWorkspace.selectedIds = new Set([...taskWorkspace.selectedIds].filter((id) => incomingIds.has(id)));
    } else {
      const existingIds = new Set(taskWorkspace.items.map((item) => item.id));
      taskWorkspace.items.push(...incoming.filter((item) => !existingIds.has(item.id)));
    }
    taskWorkspace.nextCursor = data.next_cursor;
    taskWorkspace.hasMore = Boolean(data.next_cursor);
    taskWorkspace.loading = false;
    taskWorkspace.loadingMore = false;
    taskWorkspace.loadError = false;
    taskWorkspace.pageError = "";
    renderTaskScreen();
  } catch {
    if (!taskIsCurrent(generation, ownerId) || requestGeneration !== taskLoadGeneration) return;
    if (reset) {
      taskWorkspace.loading = false;
      taskWorkspace.loadError = true;
      taskLoadErrorMessage.textContent = "We couldn't reach your tasks. Check your connection and try again.";
    } else {
      taskWorkspace.loadingMore = false;
      taskWorkspace.pageError = "We couldn't load more tasks. Check your connection and try again.";
    }
    renderTaskScreen();
  }
}

async function loadDeletedTasks(reset = false, limit = TASK_PAGE_SIZE) {
  const ownerId = currentUser?.id;
  if (!ownerId || taskWorkspace.ownerId !== ownerId) return;
  if (reset) {
    deletedLoadGeneration += 1;
    taskWorkspace.deletedLoading = true;
    taskWorkspace.deletedLoadingMore = false;
    taskWorkspace.deletedLoadError = "";
  } else {
    if (taskWorkspace.deletedLoading || taskWorkspace.deletedLoadingMore || !taskWorkspace.deletedHasMore || !taskWorkspace.deletedNextCursor) return;
    taskWorkspace.deletedLoadingMore = true;
  }
  const generation = taskSessionGeneration;
  const requestGeneration = deletedLoadGeneration;
  const mutationGeneration = taskMutationGeneration;
  const cursor = reset ? null : taskWorkspace.deletedNextCursor;
  renderDeletedTaskRows();
  const query = new URLSearchParams({ limit: String(limit) });
  if (cursor) query.set("cursor", cursor);
  try {
    const { response, data } = await taskRequest(`/api/tasks/deleted?${query.toString()}`);
    if (!taskIsCurrent(generation, ownerId) || requestGeneration !== deletedLoadGeneration) return;
    if (response.status === 401) {
      expireTaskSession(generation, ownerId);
      return;
    }
    if (!response.ok) throw new Error(taskErrorMessage(response, data, "We couldn't load recently deleted tasks."));
    if (!data || !Array.isArray(data.items) || !(data.next_cursor === null || typeof data.next_cursor === "string")) {
      throw new Error("The server returned an unexpected deleted-task list.");
    }
    if (mutationGeneration !== taskMutationGeneration) {
      void loadDeletedTasks(true, limit);
      return;
    }
    const incoming = data.items.filter((item) => isTaskRecord(item) && typeof item.deleted_at === "string");
    if (incoming.length !== data.items.length) throw new Error("The server returned invalid deleted-task data.");
    if (reset) {
      taskWorkspace.recentlyDeleted = incoming;
    } else {
      const existingIds = new Set(taskWorkspace.recentlyDeleted.map((item) => item.id));
      taskWorkspace.recentlyDeleted.push(...incoming.filter((item) => !existingIds.has(item.id)));
    }
    taskWorkspace.deletedNextCursor = data.next_cursor;
    taskWorkspace.deletedHasMore = Boolean(data.next_cursor);
    taskWorkspace.deletedLoading = false;
    taskWorkspace.deletedLoadingMore = false;
    taskWorkspace.deletedLoadError = "";
    renderDeletedTaskRows();
  } catch (error) {
    if (!taskIsCurrent(generation, ownerId) || requestGeneration !== deletedLoadGeneration) return;
    taskWorkspace.deletedLoading = false;
    taskWorkspace.deletedLoadingMore = false;
    taskWorkspace.deletedLoadError = error instanceof Error && error.message
      ? error.message
      : "We couldn't reach recently deleted tasks. Check your connection and try again.";
    renderDeletedTaskRows();
  }
}

async function loadDeletedTaskCount() {
  const ownerId = currentUser?.id;
  if (!ownerId || taskWorkspace.ownerId !== ownerId) return;
  const generation = taskSessionGeneration;
  const { response, data } = await taskRequest("/api/tasks/deleted/count").catch(() => ({ response: null, data: null }));
  if (!taskIsCurrent(generation, ownerId)) return;
  if (response?.status === 401) {
    expireTaskSession(generation, ownerId);
    return;
  }
  if (!response?.ok || !Number.isSafeInteger(data?.count) || data.count < 0) return;
  taskWorkspace.deletedCount = data.count;
  renderDeletedTaskRows();
}

async function loadCompletedTasks(reset = false) {
  const ownerId = currentUser?.id;
  if (!ownerId || taskWorkspace.ownerId !== ownerId) return;
  if (reset) {
    completedLoadGeneration += 1;
    taskWorkspace.completedLoading = true;
    taskWorkspace.completedLoadingMore = false;
    taskWorkspace.completedLoadError = "";
    taskWorkspace.completedPageError = "";
  } else {
    if (taskWorkspace.completedLoading || taskWorkspace.completedLoadingMore || !taskWorkspace.completedHasMore || !taskWorkspace.completedNextCursor) return;
    taskWorkspace.completedLoadingMore = true;
    taskWorkspace.completedPageError = "";
  }
  const generation = taskSessionGeneration;
  const requestGeneration = completedLoadGeneration;
  const mutationGeneration = taskMutationGeneration;
  const cursor = reset ? null : taskWorkspace.completedNextCursor;
  renderCompletedTasks();
  const query = new URLSearchParams({ limit: String(COMPLETED_PAGE_SIZE), period: taskWorkspace.completedPeriod, tz: activeTimeZone });
  if (cursor) query.set("cursor", cursor);
  try {
    const { response, data } = await taskRequest("/api/tasks/completed?" + query.toString());
    if (!taskIsCurrent(generation, ownerId) || requestGeneration !== completedLoadGeneration) return;
    if (response.status === 401) {
      expireTaskSession(generation, ownerId);
      return;
    }
    if (!response.ok || !data || !Array.isArray(data.items) || !(data.next_cursor === null || typeof data.next_cursor === "string") || !Number.isSafeInteger(data.total_count) || data.total_count < 0) {
      throw new Error(taskErrorMessage(response, data, "We couldn't load completed tasks."));
    }
    if (mutationGeneration !== taskMutationGeneration) {
      void loadCompletedTasks(true);
      return;
    }
    const incoming = data.items.filter((item) => isTaskRecord(item) && item.status === "DONE" && typeof item.completed_at === "string");
    if (incoming.length !== data.items.length) throw new Error("The server returned invalid completed-task data.");
    if (reset) taskWorkspace.completedItems = incoming;
    else {
      const ids = new Set(taskWorkspace.completedItems.map((item) => item.id));
      taskWorkspace.completedItems.push(...incoming.filter((item) => !ids.has(item.id)));
    }
    taskWorkspace.completedNextCursor = data.next_cursor;
    taskWorkspace.completedHasMore = Boolean(data.next_cursor);
    taskWorkspace.completedTotalCount = data.total_count;
    if (taskWorkspace.completedPeriod === "all") {
      completedCountBadge.textContent = String(data.total_count);
      completedCountBadge.hidden = false;
    }
    taskWorkspace.completedLoading = false;
    taskWorkspace.completedLoadingMore = false;
    taskWorkspace.completedLoadError = "";
    taskWorkspace.completedPageError = "";
    renderCompletedTasks();
  } catch (error) {
    if (!taskIsCurrent(generation, ownerId) || requestGeneration !== completedLoadGeneration) return;
    taskWorkspace.completedLoading = false;
    taskWorkspace.completedLoadingMore = false;
    if (reset) taskWorkspace.completedLoadError = error instanceof Error ? error.message : "We couldn't reach completed tasks. Check your connection and try again.";
    else taskWorkspace.completedPageError = error instanceof Error ? error.message : "We couldn't load more completed tasks. Try again.";
    renderCompletedTasks();
  }
}

function renderCompletedTasks() {
  const items = taskWorkspace.completedItems;
  completedLoading.hidden = !taskWorkspace.completedLoading || items.length > 0;
  completedLoadError.hidden = !taskWorkspace.completedLoadError;
  completedLoadErrorMessage.textContent = taskWorkspace.completedLoadError;
  completedEmpty.hidden = taskWorkspace.completedLoading || Boolean(taskWorkspace.completedLoadError) || items.length > 0 || taskWorkspace.completedHasMore;
  completedTotalCount.textContent = taskWorkspace.completedTotalCount === null ? "—" : String(taskWorkspace.completedTotalCount);
  completedShownCount.textContent = String(items.length);
  completedTaskList.replaceChildren();
  for (const task of items) {
    const item = document.createElement("li");
    item.className = "task-item completed-task-item";
    item.dataset.taskId = task.id;
    const content = document.createElement("div");
    content.className = "task-content";
    const heading = document.createElement("div");
    heading.className = "task-title-row";
    const title = document.createElement("h3");
    title.className = "task-text";
    title.textContent = task.title;
    const status = document.createElement("span");
    status.className = "status-badge status-done";
    status.textContent = "Completed";
    const priority = document.createElement("span");
    priority.className = "priority-badge priority-" + task.priority.toLowerCase();
    priority.textContent = "Priority: " + priorityLabel(task.priority);
    heading.append(title, status, priority);
    content.append(heading);
    if (task.description) {
      const description = document.createElement("p");
      description.className = "task-description";
      description.textContent = task.description;
      content.append(description);
    }
    const completedAt = document.createElement("p");
    completedAt.className = "completed-task-date";
    const date = new Date(task.completed_at);
     completedAt.textContent = Number.isNaN(date.valueOf()) ? "Completed" : "Completed " + new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short", timeZone: activeTimeZone }).format(date);
    content.append(completedAt);
    const actions = document.createElement("div");
    actions.className = "task-actions";
    const view = makeTaskButton("View history", "completed-history", "task-action-button");
    view.dataset.taskId = task.id;
    view.setAttribute("aria-label", "View history for completed task " + task.title);
    actions.append(view);
    item.append(content, actions);
    completedTaskList.append(item);
  }
  completedMoreButton.hidden = !taskWorkspace.completedHasMore || Boolean(taskWorkspace.completedLoadError);
  completedMoreButton.disabled = taskWorkspace.completedLoading || taskWorkspace.completedLoadingMore;
  completedMoreButton.textContent = taskWorkspace.completedLoadingMore ? "Loading…" : "View more completed tasks";
  completedPaginationFeedback.hidden = !taskWorkspace.completedPageError;
  completedPaginationFeedback.textContent = taskWorkspace.completedPageError;
}

async function refreshRecentlyDeletedState() {
  await loadDeletedTaskCount();
  if (workspaceScreen === "deleted") await loadDeletedTasks(true);
  else await loadDeletedTasks(true, DELETED_PREVIEW_SIZE);
}

function isTaskRecord(value) {
  return Boolean(value && typeof value.id === "string" && typeof value.title === "string"
    && typeof value.description === "string" && ["TODO", "IN_PROGRESS", "DONE"].includes(value.status)
    && ["LOW", "MEDIUM", "HIGH", "URGENT"].includes(value.priority)
    && (value.due_date === null || (typeof value.due_date === "string" && /^\d{4}-\d{2}-\d{2}$/.test(value.due_date)))
    && typeof value.is_overdue === "boolean"
    && Number.isInteger(value.version) && value.version > 0);
}

function renderTaskScreen() {
  const hasItems = taskWorkspace.items.length > 0;
  const initialLoading = taskWorkspace.loading && !hasItems;
  // A background refresh must not insert/remove a loading row above an already rendered list.
  taskLoading.hidden = !taskWorkspace.loading || hasItems;
  taskLoadError.hidden = !taskWorkspace.loadError;
  taskList.hidden = initialLoading || (taskWorkspace.loadError && !hasItems);
  emptyState.hidden = taskWorkspace.loading || taskWorkspace.loadError || hasItems || taskWorkspace.hasMore;
  const hasPriorityFilter = Boolean(taskWorkspace.priorityFilter || taskWorkspace.dueFilter);
  emptyStateFilters.hidden = !hasPriorityFilter;
  emptyStateDefault.hidden = hasPriorityFilter || Boolean(taskWorkspace.searchQuery || taskWorkspace.statusFilter);
  emptyStateSearch.hidden = hasPriorityFilter || !taskWorkspace.searchQuery || Boolean(taskWorkspace.statusFilter);
  emptyStateStatus.hidden = hasPriorityFilter || Boolean(taskWorkspace.searchQuery) || !taskWorkspace.statusFilter;
  emptyStateSearchStatus.hidden = hasPriorityFilter || !taskWorkspace.searchQuery || !taskWorkspace.statusFilter;
  taskSummary.hidden = !hasItems;
  if (hasItems) {
    const recentlyCompleted = taskWorkspace.items.filter((item) => item.status === "DONE").length;
    taskShownCount.textContent = String(taskWorkspace.items.length - recentlyCompleted);
    taskCompletedCount.textContent = String(recentlyCompleted);
  }
  renderActiveTaskFilters();
  retryTaskLoadButton.disabled = taskWorkspace.loading;
  loadMoreButton.hidden = !taskWorkspace.hasMore || taskWorkspace.loadError;
  loadMoreButton.disabled = taskWorkspace.loadingMore || taskWorkspace.loading;
  loadMoreButton.textContent = taskWorkspace.loadingMore ? "Loading…" : "Load more tasks";
  paginationFeedback.textContent = taskWorkspace.pageError;
  paginationFeedback.hidden = !taskWorkspace.pageError;
  if (taskWorkspace.loadError && hasItems) {
    taskLoadErrorMessage.textContent = "We couldn't refresh your tasks. The tasks already loaded are still shown.";
  }
  renderTaskRows();
  renderBulkControls();
  renderDeletedTaskRows();
}

function renderActiveTaskFilters() {
  const filters = [];
  if (taskWorkspace.searchQuery) filters.push({ key: "query", label: `Search: ${taskWorkspace.searchQuery}` });
  if (taskWorkspace.statusFilter) filters.push({ key: "status", label: `Status: ${statusLabel(taskWorkspace.statusFilter)}` });
  if (taskWorkspace.priorityFilter) filters.push({ key: "priority", label: `Priority: ${priorityLabel(taskWorkspace.priorityFilter)}` });
  if (taskWorkspace.dueFilter) {
    const dueLabels = { overdue: "Overdue", today: "Due today", upcoming: "Upcoming", none: "No due date" };
    filters.push({ key: "due", label: `Due date: ${dueLabels[taskWorkspace.dueFilter] || taskWorkspace.dueFilter}` });
  }

  activeTaskFilters.replaceChildren();
  activeTaskFilters.hidden = filters.length === 0;
  clearTaskFiltersButton.hidden = filters.length === 0;
  taskFilterCount.textContent = String(filters.length);
  taskFilterCount.hidden = filters.length === 0;
  taskFilterToggle.setAttribute("aria-label", filters.length ? `Filters, ${filters.length} active` : "Filters");

  for (const filter of filters) {
    const chip = document.createElement("button");
    chip.type = "button";
    chip.className = "active-filter-chip";
    chip.dataset.removeFilter = filter.key;
    chip.setAttribute("aria-label", `Remove ${filter.label} filter`);
    const label = document.createElement("span");
    label.textContent = filter.label;
    const remove = document.createElement("span");
    remove.className = "active-filter-remove";
    remove.setAttribute("aria-hidden", "true");
    remove.textContent = "×";
    chip.append(label, remove);
    activeTaskFilters.append(chip);
  }
}

function setTaskFilterPanelOpen(open) {
  taskFilterPanel.hidden = !open;
  taskFilterToggle.setAttribute("aria-expanded", String(open));
}

function statusLabel(status) {
  return status === "IN_PROGRESS" ? "In progress" : status === "DONE" ? "Done" : "To do";
}

function priorityLabel(priority) {
  return ({ LOW: "Low", MEDIUM: "Medium", HIGH: "High", URGENT: "Urgent" })[priority] || "Medium";
}

function formatCivilDate(value) {
  if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return "Date unavailable";
  const [year, month, day] = value.split("-").map(Number);
  const civil = new Date(0);
  civil.setUTCHours(0, 0, 0, 0);
  civil.setUTCFullYear(year, month - 1, day);
  if (civil.getUTCFullYear() !== year || civil.getUTCMonth() !== month - 1 || civil.getUTCDate() !== day) return "Date unavailable";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeZone: "UTC" }).format(civil);
}

function appendPriorityOptions(select) {
  for (const [value, label] of [["LOW", "Low"], ["MEDIUM", "Medium"], ["HIGH", "High"], ["URGENT", "Urgent"]]) {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = label;
    select.append(option);
  }
}

function makeTaskButton(label, action, className = "secondary-button") {
  const button = document.createElement("button");
  button.type = "button";
  button.className = className;
  button.dataset.action = action;
  button.textContent = label;
  return button;
}

function renderTaskRows() {
  const wasRecentlyCompletedOpen = recentlyCompletedDetails.open;
  taskList.replaceChildren();
  recentlyCompletedTaskList.replaceChildren();
  const recentlyCompletedTasks = taskWorkspace.items.filter((task) => task.status === "DONE");
  recentlyCompletedGroup.hidden = recentlyCompletedTasks.length === 0;
  recentlyCompletedCount.textContent = String(recentlyCompletedTasks.length);
  const hasActiveFilters = Boolean(taskWorkspace.searchQuery || taskWorkspace.statusFilter
    || taskWorkspace.priorityFilter || taskWorkspace.dueFilter);
  recentlyCompletedDetails.open = wasRecentlyCompletedOpen || hasActiveFilters;
  for (const task of taskWorkspace.items) {
    const item = document.createElement("li");
    item.className = "task-item";
    if (task.status === "DONE") item.classList.add("is-complete");
    item.dataset.taskId = task.id;
    const busy = busyTaskIds.has(task.id);

    const content = document.createElement("div");
    content.className = "task-content";
    content.hidden = editingTaskId === task.id;
    const heading = document.createElement("div");
    heading.className = "task-title-row";
    const title = document.createElement("h3");
    title.className = "task-text";
    title.textContent = task.title;
    const badge = document.createElement("span");
    badge.className = `status-badge status-${task.status.toLowerCase().replace("_", "-")}`;
    badge.textContent = statusLabel(task.status);
    const priorityBadge = document.createElement("span");
    priorityBadge.className = `priority-badge priority-${task.priority.toLowerCase()}`;
    priorityBadge.textContent = `Priority: ${priorityLabel(task.priority)}`;
    if (task.priority === "URGENT") priorityBadge.setAttribute("aria-label", "Urgent priority");
    const selectLabel = document.createElement("label");
    selectLabel.className = "task-select-control";
    const selectCheckbox = document.createElement("input");
    selectCheckbox.type = "checkbox";
    selectCheckbox.dataset.action = "select-task";
    selectCheckbox.checked = taskWorkspace.selectedIds.has(task.id);
    selectCheckbox.disabled = bulkPending || busy;
    selectCheckbox.setAttribute("aria-label", `Select ${task.title}`);
    selectLabel.append(selectCheckbox);
    heading.append(selectLabel, title, badge, priorityBadge);
    content.append(heading);
    if (task.due_date) {
      const due = document.createElement("p");
      const overdue = isTaskOverdueInActiveZone(task);
      const dueToday = isTaskDueTodayInActiveZone(task);
      due.className = `task-due-date${overdue ? " is-overdue" : dueToday ? " is-due-today" : ""}`;
      due.textContent = overdue
        ? `Overdue · Due ${formatCivilDate(task.due_date)}`
        : dueToday ? `Due today · ${formatCivilDate(task.due_date)}` : `Due ${formatCivilDate(task.due_date)}`;
      content.append(due);
    }
    if (task.description) {
      const description = document.createElement("p");
      description.className = "task-description";
      description.textContent = task.description;
      content.append(description);
    }

    const editForm = document.createElement("form");
    editForm.className = "task-edit-form";
    editForm.dataset.taskId = task.id;
    editForm.hidden = editingTaskId !== task.id;
    editForm.setAttribute("aria-label", `Edit ${task.title}`);
    const titleLabel = document.createElement("label");
    titleLabel.textContent = "Task title";
    const titleInput = document.createElement("input");
    titleInput.name = "title";
    titleInput.type = "text";
    titleInput.maxLength = 400;
    titleInput.required = true;
    titleInput.value = task.title;
    titleLabel.append(titleInput);
    const descriptionLabel = document.createElement("label");
    descriptionLabel.textContent = "Description";
    const descriptionInput = document.createElement("textarea");
    descriptionInput.name = "description";
    descriptionInput.maxLength = 20000;
    descriptionInput.rows = 2;
    descriptionInput.value = task.description;
    descriptionLabel.append(descriptionInput);
    const priorityEditLabel = document.createElement("label");
    priorityEditLabel.textContent = "Priority";
    const priorityEditSelect = document.createElement("select");
    priorityEditSelect.name = "priority";
    priorityEditSelect.className = "task-priority-select";
    appendPriorityOptions(priorityEditSelect);
    priorityEditSelect.value = task.priority;
    priorityEditLabel.append(priorityEditSelect);
    const dueEditLabel = document.createElement("label");
    dueEditLabel.textContent = "Due date";
    const dueEditInput = document.createElement("input");
    dueEditInput.name = "due_date";
    dueEditInput.type = "date";
    dueEditInput.value = task.due_date || "";
    dueEditLabel.append(dueEditInput);
    const editActions = document.createElement("div");
    editActions.className = "task-actions";
    const saveButton = document.createElement("button");
    saveButton.type = "submit";
    saveButton.className = "primary-button";
    saveButton.disabled = busy;
    saveButton.textContent = busy ? "Saving…" : "Save changes";
    const cancelButton = makeTaskButton("Cancel", "cancel-edit");
    cancelButton.disabled = busy;
    editActions.append(saveButton, cancelButton);
    const clearDueButton = makeTaskButton("Clear due date", "clear-due-date");
    editForm.append(titleLabel, descriptionLabel, priorityEditLabel, dueEditLabel, clearDueButton, editActions);

    const controls = document.createElement("div");
    controls.className = "task-controls";
    const statusLabelElement = document.createElement("label");
    statusLabelElement.className = "status-control";
    const statusDescription = document.createElement("span");
    statusDescription.className = "visually-hidden";
    statusDescription.textContent = `Status for ${task.title}`;
    const statusSelect = document.createElement("select");
    statusSelect.className = "task-status-select";
    statusSelect.dataset.action = "status";
    statusSelect.disabled = busy || editingTaskId === task.id;
    for (const [value, label] of [["TODO", "To do"], ["IN_PROGRESS", "In progress"], ["DONE", "Done"]]) {
      const option = document.createElement("option");
      option.value = value;
      option.textContent = label;
      statusSelect.append(option);
    }
    statusSelect.value = task.status;
    statusLabelElement.append(statusDescription, statusSelect);
    const viewButton = makeTaskButton("View", "view", "task-action-button task-view-button");
    viewButton.disabled = busy || editingTaskId === task.id;
    viewButton.setAttribute("aria-label", `View details and activity for ${task.title}`);

    controls.append(statusLabelElement, viewButton);

    const rowFeedback = document.createElement("div");
    rowFeedback.className = "task-row-feedback";
    rowFeedback.dataset.feedbackFor = task.id;
    rowFeedback.hidden = true;
    item.append(content, editForm, controls, rowFeedback);
    if (task.status === "DONE") recentlyCompletedTaskList.append(item);
    else taskList.append(item);
  }
  if (recentlyCompletedTasks.length > 0) taskList.append(recentlyCompletedGroup);
}

function renderBulkControls() {
  const visibleIds = getVisibleTaskIds();
  const selectedVisible = visibleIds.filter((id) => taskWorkspace.selectedIds.has(id)).length;
  const hasSelection = taskWorkspace.selectedIds.size > 0;
  bulkControls.hidden = visibleIds.length === 0;
  bulkControls.classList.toggle("has-selection", hasSelection);
  bulkActions.hidden = !hasSelection;
  selectAllVisible.checked = visibleIds.length > 0 && selectedVisible === visibleIds.length;
  selectAllVisible.indeterminate = selectedVisible > 0 && selectedVisible < visibleIds.length;
  selectAllVisible.disabled = bulkPending || taskWorkspace.loading || visibleIds.length === 0;
  bulkSelectionCount.textContent = `${taskWorkspace.selectedIds.size} selected`;
  bulkStatus.disabled = bulkPending || !hasSelection;
  bulkApplyStatusButton.disabled = bulkPending || !hasSelection;
  bulkApplyStatusButton.textContent = bulkPending ? "Applying…" : "Apply status";
  bulkDeleteButton.disabled = bulkPending || !hasSelection;
  bulkDeleteButton.textContent = bulkPending ? "Deleting…" : "Delete selected";
  for (const checkbox of taskList.querySelectorAll('input[data-action="select-task"]')) {
    checkbox.disabled = bulkPending;
  }
}

function getVisibleTaskIds() {
  return taskWorkspace.items
    .filter((item) => item.status !== "DONE" || recentlyCompletedDetails.open)
    .map((item) => item.id);
}

function setBulkSelection(ids) {
  taskWorkspace.selectedIds = new Set(ids);
  renderBulkControls();
  for (const checkbox of taskList.querySelectorAll('input[data-action="select-task"]')) {
    const row = checkbox.closest(".task-item");
    checkbox.checked = taskWorkspace.selectedIds.has(row?.dataset.taskId);
  }
}

async function submitBulkOperation(action, status = "") {
  if (bulkPending || !currentUser || taskWorkspace.selectedIds.size === 0) return;
  const selected = taskWorkspace.items.filter((item) => taskWorkspace.selectedIds.has(item.id));
  if (!selected.length || selected.length !== taskWorkspace.selectedIds.size) {
    setBulkSelection(selected.map((item) => item.id));
    setTaskFeedback("Some selected tasks are no longer visible. Review the selection and try again.");
    return;
  }
  bulkPending = true;
  const generation = taskSessionGeneration;
  const ownerId = currentUser.id;
  clearTaskFeedback();
  renderTaskRows();
  renderBulkControls();
  const { response, data } = await taskRequest("/api/tasks/bulk", {
    method: "POST",
    body: { action, ...(action === "status" ? { status } : {}), tasks: selected.map(({ id, version }) => ({ id, version })) },
  }).catch(() => ({ response: null, data: null }));
  if (!taskIsCurrent(generation, ownerId)) return;
  bulkPending = false;
  if (response?.status === 401) {
    expireTaskSession(generation, ownerId);
    return;
  }
  const validResponse = response?.ok && Array.isArray(data?.items) && data.items.length === selected.length
    && data.items.every((item) => isTaskRecord(item) && selected.some((original) => original.id === item.id)
      && (action === "delete" ? Boolean(item.deleted_at) : !item.deleted_at));
  if (!validResponse) {
    const conflict = response?.status === 409;
    setTaskFeedback(response
      ? conflict ? "One or more tasks changed elsewhere. No tasks were changed; the current task list has been refreshed." : taskErrorMessage(response, data, "The bulk operation could not be completed. No changes were applied.")
      : "We couldn't confirm the bulk operation. Refreshing tasks to show the current state.");
    await loadTasks(true);
    if (activityDialog.open && selected.some((item) => item.id === activityState.taskId)) await loadActivity(true);
    renderTaskScreen();
    return;
  }
  taskMutationGeneration += 1;
  if (action === "delete") {
    taskWorkspace.recentlyDeleted = taskWorkspace.recentlyDeleted.filter((item) => !taskWorkspace.selectedIds.has(item.id));
  }
  const changedTaskIds = new Set(data.items.map((item) => item.id));
  if (action === "delete") {
    taskWorkspace.items = taskWorkspace.items.filter((item) => !changedTaskIds.has(item.id));
  } else {
    const updatedById = new Map(data.items.map((item) => [item.id, item]));
    taskWorkspace.items = taskWorkspace.items.map((item) => updatedById.get(item.id) ?? item);
    taskWorkspace.items.sort(compareTaskOrder);
  }
  taskWorkspace.selectedIds.clear();
  setTaskFeedback(action === "delete"
    ? `${data.items.length} tasks deleted. You can restore them for 24 hours.`
    : `${data.items.length} tasks updated to ${statusLabel(status)}.`, "success");
  renderTaskScreen();
  // Keep the visible rows in place while the canonical first page is refreshed.
  void loadTasks(true);
  if (action === "delete") await refreshRecentlyDeletedState();
  if (activityDialog.open && changedTaskIds.has(activityState.taskId)) await loadActivity(true);
}

function openBulkDeleteConfirmation() {
  if (bulkPending || taskWorkspace.selectedIds.size === 0) return;
  bulkConfirmDescription.textContent = `Delete ${taskWorkspace.selectedIds.size} selected task${taskWorkspace.selectedIds.size === 1 ? "" : "s"}? They can be restored for 24 hours.`;
  bulkConfirmDialog.showModal();
  bulkCancelDeleteButton.focus();
}

function renderDeletedTaskRows() {
  deletedTaskList.replaceChildren();
  const deletedTasks = taskWorkspace.recentlyDeleted;
  recentlyDeletedScreen.hidden = !currentUser || taskWorkspace.ownerId !== currentUser.id || workspaceScreen !== "deleted";
  const deletedCount = taskWorkspace.deletedCount;
  recentlyDeletedCountBadge.hidden = deletedCount === null || deletedCount === 0;
  recentlyDeletedCountBadge.textContent = deletedCount === null ? "" : String(deletedCount);
  recentlyDeletedCountBadge.setAttribute("aria-label", deletedCount === null ? "" : `${deletedCount} deleted tasks`);
  recentlyDeletedViewCount.textContent = deletedCount === null
    ? "Checking available tasks…"
    : `${deletedCount} task${deletedCount === 1 ? "" : "s"} available`;
  deletedLoading.hidden = !taskWorkspace.deletedLoading || deletedTasks.length > 0;
  deletedLoadError.hidden = !taskWorkspace.deletedLoadError;
  deletedLoadErrorMessage.textContent = taskWorkspace.deletedLoadError;
  retryDeletedLoadButton.disabled = taskWorkspace.deletedLoading;
  deletedEmpty.hidden = taskWorkspace.deletedLoading || Boolean(taskWorkspace.deletedLoadError) || deletedTasks.length > 0 || taskWorkspace.deletedHasMore;
  deletedMoreButton.hidden = !taskWorkspace.deletedHasMore || Boolean(taskWorkspace.deletedLoadError);
  deletedMoreButton.disabled = taskWorkspace.deletedLoading || taskWorkspace.deletedLoadingMore;
  deletedMoreButton.textContent = taskWorkspace.deletedLoadingMore ? "Loading…" : "Load more deleted tasks";
  deletedPaginationFeedback.hidden = !taskWorkspace.deletedLoadError || deletedTasks.length === 0;
  deletedPaginationFeedback.textContent = taskWorkspace.deletedLoadError;
  for (const task of deletedTasks) {
    deletedTaskList.append(createDeletedTaskItem(task));
  }
  renderDeletedPreview();
}

function createDeletedTaskItem(task, compact = false) {
  const item = document.createElement("li");
  item.className = compact ? "task-item deleted-task-item deleted-preview-item" : "task-item deleted-task-item";
  item.dataset.taskId = task.id;
  const content = document.createElement("div");
  content.className = "task-content";
  const title = document.createElement("h3");
  title.className = "task-text";
  title.textContent = task.title;
  content.append(title);
  if (!compact && task.description) {
    const description = document.createElement("p");
    description.className = "task-description";
    description.textContent = task.description;
    content.append(description);
  }
  const deletedAt = document.createElement("p");
  deletedAt.className = "deleted-task-meta";
  deletedAt.textContent = `Deleted ${new Date(task.deleted_at).toLocaleString()}. Restorable for up to 24 hours.`;
  content.append(deletedAt);
  const restoreButton = makeTaskButton(task.restoreExpired ? "Recovery expired" : "Restore", "restore", "secondary-button");
  restoreButton.dataset.taskId = task.id;
  restoreButton.disabled = task.restoreExpired || busyTaskIds.has(task.id);
  restoreButton.setAttribute("aria-label", `Restore ${task.title}`);
  const actions = document.createElement("div");
  actions.className = "task-actions";
  const historyButton = makeTaskButton("History", "activity", "task-action-button");
  historyButton.dataset.taskId = task.id;
  historyButton.setAttribute("aria-label", `View activity history for ${task.title}`);
  actions.append(historyButton, restoreButton);
  item.append(content, actions);
  return item;
}

function renderDeletedPreview() {
  deletedPreviewList.replaceChildren();
  const count = taskWorkspace.deletedCount;
  const hasDeletedTasks = currentUser && taskWorkspace.ownerId === currentUser.id && count > 0;
  deletedPreviewSection.hidden = !hasDeletedTasks;
  if (!hasDeletedTasks) return;
  deletedPreviewCount.textContent = "Tasks can be restored for 24 hours.";
  openDeletedViewButton.hidden = false;
  deletedPreviewLoading.hidden = !taskWorkspace.deletedLoading || taskWorkspace.recentlyDeleted.length > 0;
  deletedPreviewError.hidden = !taskWorkspace.deletedLoadError;
  deletedPreviewError.textContent = taskWorkspace.deletedLoadError || "Couldn't load deleted tasks.";
  deletedPreviewRetry.hidden = !taskWorkspace.deletedLoadError;
  for (const task of taskWorkspace.recentlyDeleted.slice(0, DELETED_PREVIEW_SIZE)) {
    deletedPreviewList.append(createDeletedTaskItem(task, true));
  }
}

function replaceTask(updated) {
  const existingIndex = taskWorkspace.items.findIndex((item) => item.id === updated.id);
  if (existingIndex >= 0) taskWorkspace.items[existingIndex] = updated;
  taskWorkspace.items.sort(compareTaskOrder);
  taskMutationGeneration += 1;
  if (activityState.taskId === updated.id) activityState.task = updated;
  renderTaskScreen();
  void loadTasks(true);
  if (activityDialog.open && activityState.taskId === updated.id) void loadActivity(true);
}

function compareTaskOrder(left, right) {
  const rank = (status) => status === "IN_PROGRESS" ? 0 : status === "DONE" ? 2 : 1;
  const rankDifference = rank(left.status) - rank(right.status);
  if (rankDifference) return rankDifference;
  const createdDifference = Date.parse(right.created_at) - Date.parse(left.created_at);
  if (Number.isFinite(createdDifference) && createdDifference) return createdDifference;
  return right.id.localeCompare(left.id);
}

function showTaskRowFeedback(taskId, message, canReload = false) {
  const row = [...taskList.querySelectorAll(".task-item")].find((item) => item.dataset.taskId === taskId);
  const feedback = row?.querySelector(".task-row-feedback");
  if (!feedback) return;
  feedback.replaceChildren();
  const messageNode = document.createElement("p");
  messageNode.textContent = message;
  feedback.append(messageNode);
  if (canReload) {
    const reloadButton = makeTaskButton("Reload latest task", "reload-latest");
    reloadButton.dataset.taskId = taskId;
    feedback.append(reloadButton);
  }
  feedback.hidden = false;
  feedback.setAttribute("role", "alert");
}

function taskById(taskId) {
  return taskWorkspace.items.find((task) => task.id === taskId);
}

function renderActivityTaskDetails() {
  const task = taskById(activityState.taskId) || activityState.task;
  if (!task) return;
  activityTaskTitle.textContent = task.title;
  activityTaskFooter.hidden = Boolean(task.deleted_at) || Boolean(activityState.archived);
  activityEditTaskButton.disabled = busyTaskIds.has(task.id) || Boolean(activityState.archived);
  activityDeleteTaskButton.disabled = busyTaskIds.has(task.id) || Boolean(activityState.archived);
}

function renderActivity() {
  renderActivityTaskDetails();
  const hasItems = activityState.items.length > 0;
  activityLoading.hidden = !activityState.loading && !activityState.loadingMore;
  activityError.hidden = !activityState.error;
  activityErrorMessage.textContent = activityState.error || "We couldn't load this task's history.";
  activityEmpty.hidden = activityState.loading || Boolean(activityState.error) || hasItems;
  activityList.replaceChildren();
  for (const event of activityState.items) {
    const item = document.createElement("li");
    item.className = "activity-event";
    const marker = document.createElement("span");
    marker.className = "activity-marker";
    marker.setAttribute("aria-hidden", "true");
    const content = document.createElement("div");
    content.className = "activity-event-content";
    const heading = document.createElement("div");
    heading.className = "activity-event-heading";
    const description = document.createElement("p");
    description.className = "activity-event-description";
    description.textContent = describeActivity(event);
    const date = document.createElement("time");
    date.className = "activity-event-date";
    date.dateTime = event.created_at;
    const parsedDate = new Date(event.created_at);
     date.textContent = Number.isNaN(parsedDate.valueOf()) ? "Time unavailable" : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short", timeZone: activeTimeZone }).format(parsedDate);
    const actor = document.createElement("span");
    actor.className = "activity-event-actor";
    actor.textContent = event.actor_user_id && event.actor_user_id === currentUser?.id ? "You" : "Account user";
    heading.append(description, date, actor);
    content.append(heading);
    const values = activityValueDetails(event);
    if (values) {
      const detail = document.createElement("p");
      detail.className = "activity-event-values";
      detail.textContent = values;
      content.append(detail);
    }
    const action = document.createElement("div");
    action.className = "activity-event-action";
    if (!activityState.archived && event.undo_available === true && Number.isInteger(event.undo_version) && event.undo_version > 0) {
      const undoButton = makeTaskButton(activityState.undoingEventId === event.id ? "Undoing…" : "Undo", "undo-activity");
      undoButton.dataset.eventId = event.id;
      undoButton.disabled = Boolean(activityState.undoingEventId);
      undoButton.setAttribute("aria-label", `Undo ${describeActivity(event).toLowerCase()}`);
      action.append(undoButton);
    } else {
      const unavailable = document.createElement("span");
      unavailable.className = "activity-undo-unavailable";
      unavailable.textContent = activityState.archived ? "This task is archived and read-only." : undoUnavailableMessage(event.undo_unavailable_reason);
      action.append(unavailable);
    }
    content.append(action);
    item.append(marker, content);
    activityList.append(item);
  }
  activityMoreButton.hidden = !activityState.nextCursor || Boolean(activityState.error);
  activityMoreButton.disabled = activityState.loading || activityState.loadingMore;
  activityMoreButton.textContent = activityState.loadingMore ? "Loading…" : "Load older activity";
  activityDialogContent.setAttribute("aria-busy", String(activityState.loading || activityState.loadingMore));
}

function describeActivity(event) {
  const labels = {
    TASK_CREATED: "Task created",
    TASK_TITLE_UPDATED: "Title updated",
    TASK_DESCRIPTION_UPDATED: "Description updated",
    TASK_UPDATED: "Task details updated",
    TASK_STATUS_CHANGED: "Status changed",
    TASK_PRIORITY_CHANGED: "Priority changed",
    TASK_DUE_DATE_CHANGED: "Due date changed",
    TASK_DELETED: "Task deleted",
    TASK_RESTORED: "Task restored",
    TASK_UNDONE: "Previous action undone",
  };
  return labels[event.event_type] || "Task activity";
}

function activityValueDetails(event) {
  let previous;
  let current;
  try {
    previous = event.previous_value == null ? null : typeof event.previous_value === "string" ? JSON.parse(event.previous_value) : event.previous_value;
    current = event.new_value == null ? null : typeof event.new_value === "string" ? JSON.parse(event.new_value) : event.new_value;
  } catch {
    return "Details unavailable.";
  }
  const format = (value) => value == null || value === "" ? "Empty" : String(value);
  if (event.event_type === "TASK_CREATED") return current ? `Title: ${format(current.title)} · Status: ${format(current.status)} · Priority: ${format(current.priority || "MEDIUM")}` : "";
  if (event.event_type === "TASK_TITLE_UPDATED") return `From “${format(previous?.title)}” to “${format(current?.title)}”`;
  if (event.event_type === "TASK_DESCRIPTION_UPDATED") return `From “${format(previous?.description)}” to “${format(current?.description)}”`;
  if (event.event_type === "TASK_STATUS_CHANGED") return `From “${format(previous?.status)}” to “${format(current?.status)}”`;
  if (event.event_type === "TASK_PRIORITY_CHANGED") return `From “${format(previous?.priority)}” to “${format(current?.priority)}”`;
  if (event.event_type === "TASK_DUE_DATE_CHANGED") return `Due date: ${previous?.due_date ? formatCivilDate(previous.due_date) : "None"} → ${current?.due_date ? formatCivilDate(current.due_date) : "None"}`;
  if (event.event_type === "TASK_UPDATED") {
    return `Title: “${format(previous?.title)}” → “${format(current?.title)}” · Description: “${format(previous?.description)}” → “${format(current?.description)}”`;
  }
  if (event.event_type === "TASK_DELETED" || event.event_type === "TASK_RESTORED") return "";
  if (event.event_type === "TASK_UNDONE") {
    const original = {
      TASK_TITLE_UPDATED: "title change",
      TASK_DESCRIPTION_UPDATED: "description change",
      TASK_UPDATED: "task details change",
      TASK_STATUS_CHANGED: "status change",
      TASK_PRIORITY_CHANGED: "priority change",
      TASK_DELETED: "deletion",
    }[current?.original_event_type] || "previous action";
    const fields = current?.restored_fields && typeof current.restored_fields === "object"
      ? Object.entries(current.restored_fields).map(([key, value]) => `${key}: ${format(value)}`).join(" · ")
      : "Previous values restored.";
    return `Undid ${original}. ${fields}`;
  }
  return "";
}

function undoUnavailableMessage(reason) {
  const messages = {
    unsupported: "Undo is not supported for this action.",
    already_undone: "This action has already been undone.",
    changed_since_event: "Undo is unavailable because this field changed later.",
    task_deleted: "Restore the task before undoing this change.",
    task_restored: "This deletion was already restored.",
    recovery_expired: "The 24-hour undo window has expired.",
  };
  return messages[reason] || "Undo is unavailable for this activity.";
}

function openActivity(task, archived = false) {
  if (!currentUser || activityRequestPromise) return;
  activityActionFeedback.hidden = true;
  activityActionFeedback.textContent = "";
  delete activityActionFeedback.dataset.kind;
  activityState = { taskId: task.id, ownerId: currentUser.id, task, archived, items: [], nextCursor: null, loading: true, loadingMore: false, error: "", requestId: activityState.requestId + 1 };
  activityTaskTitle.textContent = task.title;
  activityDialog.showModal();
  renderActivity();
  void loadActivity(true);
}

async function loadActivity(reset = false) {
  const { taskId, ownerId } = activityState;
  if (!taskId || !ownerId || currentUser?.id !== ownerId || !activityDialog.open) return;
  if (activityRequestPromise) return activityRequestPromise;
  if (!reset && (!activityState.nextCursor || activityState.loading || activityState.loadingMore)) return;
  const requestId = reset ? activityState.requestId + 1 : activityState.requestId;
  activityState.requestId = requestId;
  const sessionGeneration = taskSessionGeneration;
  const cursor = reset ? null : activityState.nextCursor;
  activityState.loading = reset;
  activityState.loadingMore = !reset;
  activityState.error = "";
  renderActivity();
  const query = new URLSearchParams({ limit: String(TASK_PAGE_SIZE) });
  if (cursor) query.set("cursor", cursor);
  const requestPromise = (async () => {
    try {
      const { response, data } = await taskRequest(`/api/tasks/${encodeURIComponent(taskId)}/activity?${query}`);
      if (sessionGeneration !== taskSessionGeneration || currentUser?.id !== ownerId || activityState.requestId !== requestId || activityState.taskId !== taskId) return;
      if (response.status === 401) {
        expireTaskSession(sessionGeneration, ownerId);
        return;
      }
      if (!response.ok || !data || !Array.isArray(data.items) || !(data.next_cursor === null || typeof data.next_cursor === "string")) {
        activityState.error = taskErrorMessage(response, data, "We couldn't load this task's history.");
        if (!reset) activityState.loadingMore = false;
        renderActivity();
        return;
      }
      const validEvents = data.items.every((event) => event && event.task_id === taskId && typeof event.id === "string" && typeof event.event_type === "string" && typeof event.created_at === "string");
      if (!validEvents) throw new Error("The server returned invalid activity data.");
      if (reset) activityState.items = data.items;
      else {
        const ids = new Set(activityState.items.map((event) => event.id));
        activityState.items.push(...data.items.filter((event) => !ids.has(event.id)));
      }
      activityState.nextCursor = data.next_cursor;
      activityState.loading = false;
      activityState.loadingMore = false;
      renderActivity();
    } catch {
      if (sessionGeneration !== taskSessionGeneration || currentUser?.id !== ownerId || activityState.requestId !== requestId || activityState.taskId !== taskId) return;
      activityState.error = "We couldn't reach the activity service. Check your connection and try again.";
      activityState.loading = false;
      activityState.loadingMore = false;
      renderActivity();
    } finally {
      if (activityRequestPromise === requestPromise) activityRequestPromise = null;
    }
  })();
  activityRequestPromise = requestPromise;
  return requestPromise;
}

async function undoActivity(original) {
  const { taskId, ownerId } = activityState;
  if (!taskId || !ownerId || currentUser?.id !== ownerId || activityState.undoingEventId || !original.undo_available) return;
  activityState.undoingEventId = original.id;
  activityActionFeedback.hidden = true;
  activityActionFeedback.textContent = "";
  delete activityActionFeedback.dataset.kind;
  renderActivity();
  const generation = taskSessionGeneration;
  const { response, data } = await taskRequest(`/api/tasks/${encodeURIComponent(taskId)}/activity/${encodeURIComponent(original.id)}/undo`, {
    method: "POST",
    body: { version: original.undo_version },
  }).catch(() => ({ response: null, data: null }));
  if (!taskIsCurrent(generation, ownerId) || activityState.taskId !== taskId) return;
  if (response?.status === 401) {
    expireTaskSession(generation, ownerId);
    return;
  }
  if (!response?.ok || !isTaskRecord(data?.task) || data?.activity?.event_type !== "TASK_UNDONE") {
    if (!response) {
      activityActionFeedback.textContent = "We couldn't confirm the Undo result. Refresh the history before trying again.";
    } else if (response.status === 409) {
      activityActionFeedback.textContent = data?.error?.code === "undo_already_applied"
        ? "This action was already undone. The history has been refreshed."
        : data?.error?.code === "undo_expired"
          ? "The deletion's 24-hour Undo window has expired."
          : "The task changed after this history was loaded. No newer changes were overwritten; the task and history are being refreshed.";
    } else {
      activityActionFeedback.textContent = taskErrorMessage(response, data, "We couldn't undo this action.");
    }
    activityActionFeedback.dataset.kind = "error";
    activityActionFeedback.hidden = false;
    if (response?.status === 409 || !response) {
      void loadTasks(true);
      await loadActivity(true);
      await refreshRecentlyDeletedState();
    }
    activityState.undoingEventId = null;
    renderActivity();
    return;
  }

  const updated = data.task;
  if (workspaceScreen === "deleted") setWorkspaceScreen("tasks", { focus: false });
  taskMutationGeneration += 1;
  taskWorkspace.recentlyDeleted = taskWorkspace.recentlyDeleted.filter((item) => item.id !== updated.id);
  taskWorkspace.items = [];
  taskWorkspace.nextCursor = null;
  taskWorkspace.hasMore = false;
  taskWorkspace.selectedIds.clear();
  setTaskFeedback("Undo completed. The task and activity history have been updated.", "success");
  renderTaskScreen();
  activityActionFeedback.textContent = "Undo completed. The task was updated and the original history was preserved.";
  activityActionFeedback.dataset.kind = "success";
  activityActionFeedback.hidden = false;
  await loadActivity(true);
  void loadTasks(true);
  await refreshRecentlyDeletedState();
  activityState.undoingEventId = null;
  renderActivity();
}

function validTaskFields(title, description) {
  if (!title.trim() || Array.from(title.trim()).length > 200) return "Enter a title with 1 to 200 characters.";
  if (Array.from(description).length > 10000) return "Keep the description to 10,000 characters or fewer.";
  return "";
}

async function submitNewTask(event) {
  event.preventDefault();
  if (taskCreatePending || !currentUser) return;
  clearTaskFeedback();
  const title = taskInput.value.trim();
  const description = taskDescriptionInput.value;
  const priority = taskPriorityInput.value;
  const dueDate = taskDueDateInput.value || null;
  const validationMessage = validTaskFields(title, description);
  if (validationMessage) {
    taskInput.setAttribute("aria-invalid", "true");
    inputFeedback.textContent = validationMessage;
    taskInput.focus();
    return;
  }
  taskCreatePending = true;
  taskForm.setAttribute("aria-busy", "true");
  taskInput.disabled = true;
  taskDescriptionInput.disabled = true;
  taskPriorityInput.disabled = true;
  taskDueDateInput.disabled = true;
  const submitButton = taskForm.querySelector('button[type="submit"]');
  submitButton.disabled = true;
  submitButton.textContent = "Adding…";
  const generation = taskSessionGeneration;
  const ownerId = currentUser.id;
  renderTaskScreen();
  try {
    const { response, data } = await taskRequest("/api/tasks", { method: "POST", body: { title, description, priority, due_date: dueDate } });
    if (!taskIsCurrent(generation, ownerId)) return;
    if (response.status === 401) {
      expireTaskSession(generation, ownerId);
      return;
    }
    if (!response.ok || !isTaskRecord(data)) {
      setTaskFeedback(taskErrorMessage(response, data, "We couldn't add this task."));
      return;
    }
    taskMutationGeneration += 1;
    taskWorkspace.items = [];
    taskWorkspace.nextCursor = null;
    taskWorkspace.hasMore = false;
    taskWorkspace.selectedIds.clear();
    taskInput.value = "";
    taskDescriptionInput.value = "";
    taskPriorityInput.value = "MEDIUM";
    taskDueDateInput.value = "";
    taskOptionsDetails.open = false;
    taskInput.removeAttribute("aria-invalid");
    inputFeedback.textContent = "";
    setTaskFeedback("Task added and saved.", "success");
    renderTaskScreen();
    void loadTasks(true);
    taskInput.focus();
  } catch {
    if (taskIsCurrent(generation, ownerId)) setTaskFeedback("We couldn't reach your tasks. Your task wasn't saved; try again.");
  } finally {
    if (taskIsCurrent(generation, ownerId)) {
      taskCreatePending = false;
      taskForm.removeAttribute("aria-busy");
      taskInput.disabled = false;
      taskDescriptionInput.disabled = false;
      taskPriorityInput.disabled = false;
      taskDueDateInput.disabled = false;
      submitButton.disabled = false;
      submitButton.textContent = "Add Task";
    }
  }
}

async function saveTaskEdit(form, taskId) {
  const task = taskById(taskId);
  if (!task || busyTaskIds.has(taskId) || !currentUser) return;
  const formData = new FormData(form);
  const title = String(formData.get("title") ?? "").trim();
  const description = String(formData.get("description") ?? "");
  const validationMessage = validTaskFields(title, description);
  if (validationMessage) {
    showTaskRowFeedback(taskId, validationMessage);
    form.querySelector('[name="title"]').focus();
    return;
  }
  busyTaskIds.add(taskId);
  const generation = taskSessionGeneration;
  const ownerId = currentUser.id;
  const row = form.closest(".task-item");
  row?.classList.add("is-busy");
  for (const control of row?.querySelectorAll("button, input, textarea, select") ?? []) control.disabled = true;
  const { response, data } = await taskRequest(`/api/tasks/${encodeURIComponent(taskId)}`, {
    method: "PUT",
    body: { title, description, status: task.status, priority: String(formData.get("priority") || task.priority), due_date: String(formData.get("due_date") || "") || null, version: task.version },
  }).catch(() => ({ response: null, data: null }));
  if (!taskIsCurrent(generation, ownerId)) return;
  busyTaskIds.delete(taskId);
  row?.classList.remove("is-busy");
  if (response?.status === 401) {
    expireTaskSession(generation, ownerId);
    return;
  }
  if (!response?.ok || !isTaskRecord(data)) {
    if (response?.status === 409 && data?.error?.code === "version_conflict") {
      showTaskRowFeedback(taskId, "This task changed elsewhere. Your edits are still here. Reload the latest version to continue.", true);
    } else {
      showTaskRowFeedback(taskId, response ? taskErrorMessage(response, data, "We couldn't save your changes.") : "We couldn't reach the server. Your edits are still here; try again.");
    }
    renderTaskRowsKeepingEdit(taskId);
    return;
  }
  editingTaskId = null;
  replaceTask(data);
  setTaskFeedback("Changes saved.", "success");
}

function renderTaskRowsKeepingEdit(taskId) {
  const savedTitle = [...taskList.querySelectorAll(".task-item")].find((item) => item.dataset.taskId === taskId)?.querySelector('[name="title"]')?.value;
  const savedDescription = [...taskList.querySelectorAll(".task-item")].find((item) => item.dataset.taskId === taskId)?.querySelector('[name="description"]')?.value;
  const row = [...taskList.querySelectorAll(".task-item")].find((item) => item.dataset.taskId === taskId);
  if (!row) return;
  const editForm = row.querySelector(".task-edit-form");
  const titleInput = editForm.querySelector('[name="title"]');
  const descriptionInput = editForm.querySelector('[name="description"]');
  titleInput.disabled = false;
  descriptionInput.disabled = false;
  if (savedTitle !== undefined) titleInput.value = savedTitle;
  if (savedDescription !== undefined) descriptionInput.value = savedDescription;
  editForm.hidden = false;
  row.querySelector(".task-content").hidden = true;
  row.querySelector(".task-status-select").disabled = true;
  row.querySelector('[data-action="view"]')?.setAttribute("disabled", "");
  row.querySelector('[type="submit"]').disabled = false;
  row.querySelector('[data-action="cancel-edit"]').disabled = false;
}

async function changeTaskStatus(select, taskId) {
  const task = taskById(taskId);
  if (!task || busyTaskIds.has(taskId) || !currentUser) return;
  const requestedStatus = select.value;
  select.value = task.status;
  if (requestedStatus === task.status) return;
  await updateTask(task, { title: task.title, description: task.description, status: requestedStatus, priority: task.priority }, taskId);
}

async function changeTaskPriority(select, taskId) {
  const task = taskById(taskId);
  if (!task || busyTaskIds.has(taskId) || !currentUser) return;
  const requestedPriority = select.value;
  select.value = task.priority;
  if (requestedPriority === task.priority) return;
  await updateTask(task, { title: task.title, description: task.description, status: task.status, priority: requestedPriority }, taskId);
}

async function updateTask(task, fields, taskId) {
  if (busyTaskIds.has(taskId) || !currentUser) return;
  busyTaskIds.add(taskId);
  const generation = taskSessionGeneration;
  const ownerId = currentUser.id;
  const row = [...taskList.querySelectorAll(".task-item")].find((item) => item.dataset.taskId === taskId);
  row?.classList.add("is-busy");
  for (const control of row?.querySelectorAll("button, input, select") ?? []) control.disabled = true;
  if (activityDialog.open && activityState.taskId === taskId) renderActivityTaskDetails();
  const { response, data } = await taskRequest(`/api/tasks/${encodeURIComponent(taskId)}`, {
    method: "PUT",
    body: { ...fields, version: task.version },
  }).catch(() => ({ response: null, data: null }));
  if (!taskIsCurrent(generation, ownerId)) return;
  busyTaskIds.delete(taskId);
  if (activityDialog.open && activityState.taskId === taskId) renderActivityTaskDetails();
  if (response?.status === 401) {
    expireTaskSession(generation, ownerId);
    return;
  }
  if (!response?.ok || !isTaskRecord(data)) {
    row?.classList.remove("is-busy");
    renderTaskRows();
    if (response?.status === 409 && data?.error?.code === "version_conflict") {
      showTaskRowFeedback(taskId, "This task changed elsewhere. Reload the latest version before changing it.", true);
    } else {
      showTaskRowFeedback(taskId, response ? taskErrorMessage(response, data, "We couldn't update this task.") : "We couldn't reach the server. The task wasn't changed.");
    }
    if (activityDialog.open && activityState.taskId === taskId) {
      activityActionFeedback.textContent = response?.status === 409
        ? "This task changed elsewhere. No changes were applied; refresh the task and history before trying again."
        : response ? taskErrorMessage(response, data, "We couldn't update this task.") : "We couldn't reach the server. The task wasn't changed.";
      activityActionFeedback.dataset.kind = "error";
      activityActionFeedback.hidden = false;
      renderActivityTaskDetails();
    }
    return;
  }
  replaceTask(data);
  if (fields.status !== task.status) {
    // The updated status badge and row position provide immediate confirmation without
    // inserting a banner that shifts the entire workspace during a routine action.
    clearTaskFeedback();
  } else {
    setTaskFeedback(fields.priority !== task.priority ? "Task priority updated." : "Task updated.", "success");
  }
  if (activityDialog.open && activityState.taskId === taskId) {
    activityActionFeedback.textContent = fields.status !== task.status ? "Task status updated." : "Task updated.";
    activityActionFeedback.dataset.kind = "success";
    activityActionFeedback.hidden = false;
  }
}

async function deleteTask(taskId) {
  const task = taskById(taskId);
  if (!task || busyTaskIds.has(taskId) || !currentUser) return;
  if (!window.confirm(`Delete “${task.title}”? You can restore it for 24 hours.`)) return;
  busyTaskIds.add(taskId);
  const generation = taskSessionGeneration;
  const ownerId = currentUser.id;
  if (activityDialog.open && activityState.taskId === taskId) activityDialog.close();
  renderTaskRows();
  const { response, data } = await taskRequest(`/api/tasks/${encodeURIComponent(taskId)}`, {
    method: "DELETE",
    body: { version: task.version },
  }).catch(() => ({ response: null, data: null }));
  if (!taskIsCurrent(generation, ownerId)) return;
  busyTaskIds.delete(taskId);
  if (response?.status === 401) {
    expireTaskSession(generation, ownerId);
    return;
  }
  if (!response?.ok || !isTaskRecord(data) || !data.deleted_at) {
    renderTaskRows();
    showTaskRowFeedback(taskId, response ? taskErrorMessage(response, data, "We couldn't delete this task. It is still in your list.") : "We couldn't reach the server. The task is still in your list.", response?.status === 409);
    return;
  }
  taskMutationGeneration += 1;
  taskWorkspace.items = [];
  taskWorkspace.nextCursor = null;
  taskWorkspace.hasMore = false;
  taskWorkspace.selectedIds.delete(taskId);
  taskWorkspace.recentlyDeleted = taskWorkspace.recentlyDeleted.filter((item) => item.id !== taskId);
  setTaskFeedback("Task deleted. You can restore it for 24 hours.", "success");
  renderTaskScreen();
  void loadTasks(true);
  await refreshRecentlyDeletedState();
}

async function restoreTask(taskId) {
  const task = taskWorkspace.recentlyDeleted.find((item) => item.id === taskId);
  if (!task || task.restoreExpired || busyTaskIds.has(taskId) || !currentUser) return;
  busyTaskIds.add(taskId);
  const generation = taskSessionGeneration;
  const ownerId = currentUser.id;
  renderDeletedTaskRows();
  const { response, data } = await taskRequest(`/api/tasks/${encodeURIComponent(taskId)}/restore`, {
    method: "POST",
    body: { version: task.version },
  }).catch(() => ({ response: null, data: null }));
  if (!taskIsCurrent(generation, ownerId)) return;
  busyTaskIds.delete(taskId);
  if (response?.status === 401) {
    expireTaskSession(generation, ownerId);
    return;
  }
  if (!response?.ok || !isTaskRecord(data) || data.deleted_at) {
    if (response?.status === 409 && data?.error?.code === "recovery_expired") {
      taskWorkspace.recentlyDeleted = taskWorkspace.recentlyDeleted.filter((item) => item.id !== taskId);
      setTaskFeedback("This task is outside the 24-hour recovery window and can no longer be restored.");
      await refreshRecentlyDeletedState();
    } else if (response?.status === 409 && data?.error?.code === "version_conflict") {
      const latest = await taskRequest(`/api/tasks/${encodeURIComponent(taskId)}`).catch(() => ({ response: null, data: null }));
      if (!taskIsCurrent(generation, ownerId)) return;
      if (latest.response?.status === 401) {
        expireTaskSession(generation, ownerId);
        return;
      }
      if (latest.response?.ok && isTaskRecord(latest.data)) {
        if (latest.data.deleted_at) {
          setTaskFeedback("This deleted task changed elsewhere. Recently deleted tasks are being refreshed.");
        } else {
          taskWorkspace.recentlyDeleted = taskWorkspace.recentlyDeleted.filter((item) => item.id !== taskId);
          taskWorkspace.items = [latest.data, ...taskWorkspace.items.filter((item) => item.id !== taskId)];
          taskMutationGeneration += 1;
          setTaskFeedback("This task was restored elsewhere. The latest active version is now shown.", "success");
        }
        await refreshRecentlyDeletedState();
      } else {
        setTaskFeedback("This task changed elsewhere. Recently deleted is being refreshed before you try again.");
        await refreshRecentlyDeletedState();
      }
    } else {
      setTaskFeedback(response ? taskErrorMessage(response, data, "We couldn't restore this task.") : "We couldn't reach the server. The task remains deleted.");
    }
    if (workspaceScreen === "deleted") setDeletedViewFeedback(taskFeedback.textContent, "error");
    renderTaskScreen();
    return;
  }
  taskMutationGeneration += 1;
  taskWorkspace.recentlyDeleted = taskWorkspace.recentlyDeleted.filter((item) => item.id !== taskId);
  taskWorkspace.items = [];
  taskWorkspace.nextCursor = null;
  taskWorkspace.hasMore = false;
  taskWorkspace.selectedIds.clear();
  setTaskFeedback("Task restored.", "success");
  setDeletedViewFeedback("Task restored. Return to your tasks to see it in the active list.", "success");
  renderTaskScreen();
  void loadTasks(true);
  await refreshRecentlyDeletedState();
}

async function reloadLatestTask(taskId) {
  if (!currentUser) return;
  const generation = taskSessionGeneration;
  const ownerId = currentUser.id;
  const { response, data } = await taskRequest(`/api/tasks/${encodeURIComponent(taskId)}`).catch(() => ({ response: null, data: null }));
  if (!taskIsCurrent(generation, ownerId)) return;
  if (response?.status === 401) {
    expireTaskSession(generation, ownerId);
    return;
  }
  if (!response?.ok || !isTaskRecord(data)) {
    setTaskFeedback(response ? taskErrorMessage(response, data, "We couldn't reload this task. Your edits are still on screen.") : "We couldn't reach the server. Your edits are still on screen.");
    return;
  }
  editingTaskId = null;
  taskWorkspace.items = taskWorkspace.items.map((item) => item.id === taskId ? data : item);
  taskMutationGeneration += 1;
  renderTaskScreen();
  setTaskFeedback("Latest task version loaded. Your previous unsaved edits were kept until you chose to reload.", "success");
}

taskForm.addEventListener("submit", submitNewTask);
clearNewDueDateButton.addEventListener("click", () => { taskDueDateInput.value = ""; taskDueDateInput.focus(); });
taskInput.addEventListener("input", () => {
  taskInput.removeAttribute("aria-invalid");
  inputFeedback.textContent = "";
});
taskDescriptionInput.addEventListener("input", () => { inputFeedback.textContent = ""; });
function updateTaskFilters(delay = 250) {
  if (!currentUser || taskWorkspace.ownerId !== currentUser.id) return;
  const query = taskSearchInput.value.trim();
  const status = taskStatusFilter.value;
  const priority = taskPriorityFilter.value;
  const due = taskDueFilter.value;
  if (query === taskWorkspace.searchQuery && status === taskWorkspace.statusFilter && priority === taskWorkspace.priorityFilter && due === taskWorkspace.dueFilter) return;
  clearTimeout(taskFilterTimer);
  taskWorkspace.searchQuery = query;
  taskWorkspace.statusFilter = status;
  taskWorkspace.priorityFilter = priority;
  taskWorkspace.dueFilter = due;
  taskWorkspace.selectedIds.clear();
  taskWorkspace.items = [];
  taskWorkspace.nextCursor = null;
  taskWorkspace.hasMore = false;
  taskWorkspace.loading = true;
  taskWorkspace.loadingMore = false;
  taskWorkspace.loadError = false;
  taskWorkspace.pageError = "";
  taskLoadGeneration += 1;
  renderTaskScreen();
  taskFilterTimer = setTimeout(() => {
    taskFilterTimer = null;
    void loadTasks(true);
  }, delay);
}

taskSearchInput.addEventListener("input", () => updateTaskFilters());
taskStatusFilter.addEventListener("change", () => updateTaskFilters());
taskPriorityFilter.addEventListener("change", () => updateTaskFilters(0));
taskDueFilter.addEventListener("change", () => updateTaskFilters(0));
taskFilterToggle.addEventListener("click", () => {
  setTaskFilterPanelOpen(taskFilterPanel.hidden);
});
document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape" || taskFilterPanel.hidden) return;
  setTaskFilterPanelOpen(false);
  taskFilterToggle.focus();
});
activeTaskFilters.addEventListener("click", (event) => {
  const chip = event.target.closest("[data-remove-filter]");
  if (!chip) return;
  const inputs = {
    query: taskSearchInput,
    status: taskStatusFilter,
    priority: taskPriorityFilter,
    due: taskDueFilter,
  };
  const input = inputs[chip.dataset.removeFilter];
  if (!input) return;
  input.value = "";
  updateTaskFilters(0);
  input.focus();
});
clearTaskFiltersButton.addEventListener("click", () => {
  taskSearchInput.value = "";
  taskStatusFilter.value = "";
  taskPriorityFilter.value = "";
  taskDueFilter.value = "";
  updateTaskFilters(0);
  taskSearchInput.focus();
});
retryTaskLoadButton.addEventListener("click", () => { void loadTasks(true); });
loadMoreButton.addEventListener("click", () => { void loadTasks(false); });
retryDeletedLoadButton.addEventListener("click", () => { void loadDeletedTasks(true); });
deletedPreviewRetry.addEventListener("click", () => { void loadDeletedTasks(true, DELETED_PREVIEW_SIZE); });
deletedMoreButton.addEventListener("click", () => { void loadDeletedTasks(false); });
document.querySelector("#retry-completed-load-button").addEventListener("click", () => { void loadCompletedTasks(true); });
completedMoreButton.addEventListener("click", () => { void loadCompletedTasks(false); });
completedPeriodFilter.addEventListener("change", () => {
  taskWorkspace.completedPeriod = completedPeriodFilter.value;
  taskWorkspace.completedItems = [];
  taskWorkspace.completedNextCursor = null;
  taskWorkspace.completedHasMore = false;
  taskWorkspace.completedTotalCount = null;
  void loadCompletedTasks(true);
});
selectAllVisible.addEventListener("change", () => {
  const visibleIds = getVisibleTaskIds();
  const visibleIdSet = new Set(visibleIds);
  setBulkSelection(selectAllVisible.checked
    ? [...taskWorkspace.selectedIds, ...visibleIds]
    : [...taskWorkspace.selectedIds].filter((id) => !visibleIdSet.has(id)));
});
bulkApplyStatusButton.addEventListener("click", () => { void submitBulkOperation("status", bulkStatus.value); });
bulkDeleteButton.addEventListener("click", openBulkDeleteConfirmation);
bulkCancelDeleteButton.addEventListener("click", () => bulkConfirmDialog.close());
bulkConfirmDeleteButton.addEventListener("click", () => {
  bulkConfirmDialog.close();
  void submitBulkOperation("delete");
});
bulkConfirmDialog.addEventListener("cancel", () => { bulkPending = false; });

taskList.addEventListener("submit", (event) => {
  const form = event.target.closest(".task-edit-form");
  if (!form) return;
  event.preventDefault();
  void saveTaskEdit(form, form.dataset.taskId);
});

recentlyCompletedDetails.addEventListener("toggle", () => {
  const hasActiveFilters = Boolean(taskWorkspace.searchQuery || taskWorkspace.statusFilter
    || taskWorkspace.priorityFilter || taskWorkspace.dueFilter);
  if (!recentlyCompletedDetails.open) {
    if (hasActiveFilters) {
      recentlyCompletedDetails.open = true;
      return;
    }
    const visibleIds = new Set(taskWorkspace.items.filter((item) => item.status !== "DONE").map((item) => item.id));
    setBulkSelection([...taskWorkspace.selectedIds].filter((id) => visibleIds.has(id)));
    return;
  }
  renderBulkControls();
});

taskList.addEventListener("change", (event) => {
  if (event.target.matches('input[data-action="select-task"]')) {
    const taskId = event.target.closest(".task-item")?.dataset.taskId;
    if (taskId) {
      const next = new Set(taskWorkspace.selectedIds);
      if (event.target.checked) next.add(taskId);
      else next.delete(taskId);
      setBulkSelection(next);
    }
    return;
  }
  if (event.target.matches(".task-priority-select") && !event.target.closest(".task-edit-form")) {
    const taskId = event.target.closest(".task-item")?.dataset.taskId;
    if (taskId) void changeTaskPriority(event.target, taskId);
    return;
  }
  if (!event.target.matches(".task-status-select")) return;
  const taskId = event.target.closest(".task-item")?.dataset.taskId;
  if (taskId) void changeTaskStatus(event.target, taskId);
});

taskList.addEventListener("click", (event) => {
  const button = event.target.closest("button[data-action]");
  if (!button) return;
  const taskId = button.closest(".task-item")?.dataset.taskId;
  if (!taskId) return;
  if (button.dataset.action === "clear-due-date") {
    const input = button.closest(".task-edit-form")?.querySelector('[name="due_date"]');
    if (input) { input.value = ""; input.focus(); }
  } else if (button.dataset.action === "edit") {
    editingTaskId = taskId;
    renderTaskRows();
    taskList.querySelector(`[data-task-id="${CSS.escape(taskId)}"] [name="title"]`)?.focus();
  } else if (button.dataset.action === "cancel-edit") {
    editingTaskId = null;
    renderTaskRows();
  } else if (button.dataset.action === "delete") {
    void deleteTask(taskId);
  } else if (button.dataset.action === "reload-latest") {
    void reloadLatestTask(taskId);
  } else if (button.dataset.action === "activity") {
    const task = taskById(taskId);
    if (task) openActivity(task);
  } else if (button.dataset.action === "view") {
    const task = taskById(taskId);
    if (task) openActivity(task);
  }
});

completedTaskList.addEventListener("click", (event) => {
  const button = event.target.closest('button[data-action="completed-history"]');
  if (!button) return;
  const task = taskWorkspace.completedItems.find((item) => item.id === button.dataset.taskId);
  if (task) openActivity(task, true);
});

let lastLocalCalendarDate = currentAccountDate();
function currentAccountDate() {
  try {
    const parts = new Intl.DateTimeFormat("en", { timeZone: activeTimeZone, year: "numeric", month: "2-digit", day: "2-digit" }).formatToParts(new Date());
    const get = (type) => parts.find((part) => part.type === type)?.value;
    return `${get("year")}-${get("month")}-${get("day")}`;
  }
  catch { return ""; }
}

function isTaskOverdueInActiveZone(task) {
  const today = currentAccountDate();
  return Boolean(task.due_date && task.status !== "DONE" && !task.deleted_at && today && task.due_date < today);
}
function isTaskDueTodayInActiveZone(task) {
  const today = currentAccountDate();
  return Boolean(task.due_date && task.status !== "DONE" && !task.deleted_at && today && task.due_date === today);
}
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState !== "visible" || !currentUser) return;
  const today = currentAccountDate();
  if (today && today !== lastLocalCalendarDate) { lastLocalCalendarDate = today; taskWorkspace.selectedIds.clear(); void loadTasks(true); }
  else { void loadTasks(true); }
});
setInterval(() => {
  if (document.visibilityState !== "visible" || !currentUser) return;
  const today = currentAccountDate();
  const archiveBoundaryReached = taskWorkspace.items.some((item) =>
    item.status === "DONE" && typeof item.completed_at === "string"
      && Date.parse(item.completed_at) + 12 * 60 * 60 * 1000 <= Date.now());
  if ((today && lastLocalCalendarDate && today !== lastLocalCalendarDate) || archiveBoundaryReached) {
    lastLocalCalendarDate = today;
    taskWorkspace.selectedIds.clear();
    void loadTasks(true);
    if (workspaceScreen === "completed") void loadCompletedTasks(true);
  } else if (today) lastLocalCalendarDate = today;
}, 30_000);

deletedTaskList.addEventListener("click", handleDeletedTaskListClick);
deletedPreviewList.addEventListener("click", handleDeletedTaskListClick);
document.querySelector("#timezone-preference-form")?.addEventListener("submit", (event) => {
  event.preventDefault();
  const value = timeZoneOverrideInput?.value.trim() || "";
  if (!value) {
    if (timeZonePreferenceFeedback) {
      timeZonePreferenceFeedback.textContent = "Enter an IANA time zone or choose Use device time zone.";
      timeZonePreferenceFeedback.dataset.kind = "error";
      timeZonePreferenceFeedback.hidden = false;
    }
    return;
  }
  void saveTimeZonePreference(value);
});
useDeviceTimeZoneButton?.addEventListener("click", () => void saveTimeZonePreference(null));

function handleDeletedTaskListClick(event) {
  const historyButton = event.target.closest('button[data-action="activity"]');
  if (historyButton?.dataset.taskId) {
    const deletedTask = taskWorkspace.recentlyDeleted.find((item) => item.id === historyButton.dataset.taskId);
    if (deletedTask) openActivity(deletedTask);
    return;
  }
  const button = event.target.closest('button[data-action="restore"]');
  if (button?.dataset.taskId) void restoreTask(button.dataset.taskId);
}

document.querySelector("#activity-close-button").addEventListener("click", () => activityDialog.close());
activityEditTaskButton.addEventListener("click", () => {
  const task = taskById(activityState.taskId);
  if (!task) return;
  activityDialog.close();
  editingTaskId = task.id;
  renderTaskRows();
  const row = taskList.querySelector(`[data-task-id="${CSS.escape(task.id)}"]`);
  row?.scrollIntoView({ block: "center", behavior: "smooth" });
  row?.querySelector('[name="title"]')?.focus({ preventScroll: true });
});
activityDeleteTaskButton.addEventListener("click", () => {
  if (activityState.taskId) void deleteTask(activityState.taskId);
});
activityDialog.addEventListener("close", () => {
  activityState.requestId += 1;
  activityState.loading = false;
  activityState.loadingMore = false;
  activityRequestPromise = null;
});
activityDialog.addEventListener("click", (event) => {
  if (event.target === activityDialog) activityDialog.close();
});
activityRetryButton.addEventListener("click", () => { void loadActivity(true); });
activityMoreButton.addEventListener("click", () => { void loadActivity(false); });
activityList.addEventListener("click", (event) => {
  const button = event.target.closest('button[data-action="undo-activity"]');
  if (!button) return;
  const original = activityState.items.find((item) => item.id === button.dataset.eventId);
  if (original) void undoActivity(original);
});

renderTaskScreen();
renderAuthState();
checkSession();
