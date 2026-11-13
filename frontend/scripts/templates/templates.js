// Template loading, editing, application, and picker interactions.
"use strict";

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
