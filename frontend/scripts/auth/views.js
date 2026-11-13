// Authentication view rendering, validation, and form feedback.
"use strict";

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
  adminDashboardView.hidden = workspaceScreen !== "tasks";
  adminUsersView.hidden = workspaceScreen !== "admin-users";
  adminTasksView.hidden = workspaceScreen !== "admin-tasks";
  timeZonePreference.hidden = !workspaceVisible || !workspaceShowsTimeZone(workspaceScreen);
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
  for (const button of authView.querySelectorAll(".auth-submit, .password-visibility-toggle")) button.disabled = isSubmitting;
  loginForm.setAttribute("aria-busy", String(status === "submitting-login"));
  registerForm.setAttribute("aria-busy", String(status === "submitting-register"));
  loginSubmitLabel.textContent = status === "submitting-login" ? "Signing in…" : "Sign in";
  registerSubmitLabel.textContent = status === "submitting-register" ? "Creating account…" : "Create account";
  logoutButton.disabled = operation === "logout" && isSubmitting;
  logoutButton.textContent = logoutButton.disabled ? "Signing out…" : "Sign out";

  if (status === "authenticated" || status === "submitting-logout" || errorContext === "logout") {
    signedInEmail.textContent = maskHeaderEmail(currentUser?.email);
  }
  for (const link of workspaceJumpLinks.querySelectorAll("a")) {
    const active = (link.id === "open-completed" && workspaceScreen === "completed")
      || (link.id === "open-tasks" && workspaceScreen === "tasks");
    if (active) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
}

for (const button of passwordVisibilityToggles) {
  button.addEventListener("click", () => {
    const input = document.getElementById(button.getAttribute("aria-controls"));
    togglePasswordVisibility(input, button);
  });
}

function setAuthState(status, details = {}) {
  authState = { status, ...details };
  renderAuthState();
}

function setWorkspaceScreen(screen, { replace = false, focus = true } = {}) {
  if (!currentUser) return;
  const previousWorkspaceScreen = workspaceScreen;
  const allowedScreens = ["tasks", "deleted", "completed", "admin-users", "admin-tasks"];
  workspaceScreen = allowedScreens.includes(screen) ? screen : "tasks";
  if (["admin-users", "admin-tasks"].includes(workspaceScreen) && currentUser.role !== "ADMIN") workspaceScreen = "tasks";
  const hash = {
    deleted: "#recently-deleted",
    completed: "#completed",
    "admin-users": "#admin-users",
    "admin-tasks": "#admin-tasks",
    tasks: "#tasks",
  }[workspaceScreen];
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
  } else if (workspaceScreen === "admin-users") {
    void loadAdminAccounts();
    if (focus) adminUsersViewTitle.focus({ preventScroll: true });
  } else if (workspaceScreen === "admin-tasks") {
    void loadAdminTasks();
    if (focus) adminTasksViewTitle.focus({ preventScroll: true });
  } else {
    void loadTasks(true);
    if (["admin-users", "admin-tasks"].includes(previousWorkspaceScreen)) void loadAdminDashboardPreviews();
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
adminViewAllUsersButton.addEventListener("click", () => setWorkspaceScreen("admin-users"));
adminViewAllTasksButton.addEventListener("click", () => setWorkspaceScreen("admin-tasks"));
adminUsersBackButton.addEventListener("click", () => setWorkspaceScreen("tasks"));
adminTasksBackButton.addEventListener("click", () => setWorkspaceScreen("tasks"));
adminUserSearchInput.addEventListener("input", () => {
  clearTimeout(adminUserSearchTimer);
  adminUserSearchTimer = setTimeout(() => void loadAdminAccounts(), 250);
});
window.addEventListener("popstate", () => {
  workspaceScreen = workspaceScreenFromHash(window.location.hash);
  if (["admin-users", "admin-tasks"].includes(workspaceScreen) && currentUser?.role !== "ADMIN") workspaceScreen = "tasks";
  renderAuthState();
  if (workspaceScreen === "deleted" && currentUser) {
    void loadDeletedTaskCount();
    void loadDeletedTasks(true);
  }
  if (workspaceScreen === "completed" && currentUser) void loadCompletedTasks(true);
  if (workspaceScreen === "admin-users" && currentUser) void loadAdminAccounts();
  if (workspaceScreen === "admin-tasks" && currentUser) void loadAdminTasks();
  if (workspaceScreen === "tasks" && currentUser?.role === "ADMIN") void loadAdminDashboardPreviews();
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
  for (const input of [loginPassword, registerPassword, registerConfirmPassword]) resetPasswordVisibility(input);
  loginPassword.value = "";
  registerPassword.value = "";
  registerConfirmPassword.value = "";
}

function resetPasswordVisibility(input) {
  if (input?.type !== "text") return;
  const button = passwordVisibilityToggles.find((candidate) => candidate.getAttribute("aria-controls") === input.id);
  if (button) togglePasswordVisibility(input, button);
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
