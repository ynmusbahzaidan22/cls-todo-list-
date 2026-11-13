// Login, registration, server session validation, and logout.
"use strict";

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
    resetPasswordVisibility(loginPassword);
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
    resetPasswordVisibility(registerPassword);
    resetPasswordVisibility(registerConfirmPassword);
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
