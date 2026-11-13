// Account time-zone preference loading and saving.
"use strict";

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
