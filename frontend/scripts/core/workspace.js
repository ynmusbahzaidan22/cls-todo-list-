// Task workspace initialization and account-scoped state.
"use strict";

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
  if (user.role === "ADMIN") {
    if (workspaceScreen === "admin-users") void loadAdminAccounts();
    else if (workspaceScreen === "admin-tasks") void loadAdminTasks();
    void loadAdminDashboardPreviews();
  } else if (["admin-users", "admin-tasks"].includes(workspaceScreen)) {
    setWorkspaceScreen("tasks", { replace: true, focus: false });
  }
  void loadDeletedTaskCount();
  if (workspaceScreen === "deleted") void loadDeletedTasks(true);
  else void loadDeletedTasks(true, DELETED_PREVIEW_SIZE);
  if (workspaceScreen === "completed") void loadCompletedTasks(true);
  void loadTemplates();
}
