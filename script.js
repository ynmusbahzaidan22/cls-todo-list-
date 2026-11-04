"use strict";

// حالة التطبيق: تبقى المهام في الذاكرة طوال فترة فتح الصفحة فقط.
const tasks = [];
let nextTaskId = 1;

// مراجع عناصر HTML التي يحدّثها التطبيق أو يستقبل منها التفاعل.
const taskForm = document.querySelector("#task-form");
const taskInput = document.querySelector("#task-input");
const taskList = document.querySelector("#task-list");
const taskSummary = document.querySelector("#task-summary");
const emptyState = document.querySelector("#empty-state");
const inputFeedback = document.querySelector("#input-feedback");

// يحسب المهام المتبقية والمكتملة، ثم يحدّث الملخص القابل لقارئ الشاشة.
function updateSummary() {
  const completedCount = tasks.filter((task) => task.completed).length;
  const remainingCount = tasks.length - completedCount;

  if (tasks.length === 0) {
    taskSummary.textContent = "";
    return;
  }

  if (remainingCount === 0) {
    taskSummary.textContent = "All tasks completed";
    return;
  }

  const remainingLabel = `${remainingCount} ${remainingCount === 1 ? "task" : "tasks"} left`;
  const completedLabel = `${completedCount} completed`;
  taskSummary.textContent = `${remainingLabel} \u00b7 ${completedLabel}`;
}

// يبني عناصر القائمة من بيانات المهام ويزامنها مع الحالة الفارغة والملخص.
function renderTasks() {
  taskList.replaceChildren();
  emptyState.hidden = tasks.length > 0;

  for (const task of tasks) {
    // كل عنصر يحتوي على مربع اختيار مسمّى، نص آمن، وزر حذف خاص بالمهمة.
    const item = document.createElement("li");
    item.className = "task-item";
    item.dataset.taskId = task.id;
    if (task.completed) item.classList.add("is-complete");

    const label = document.createElement("label");
    label.className = "task-label";

    const checkbox = document.createElement("input");
    checkbox.className = "task-checkbox";
    checkbox.type = "checkbox";
    checkbox.checked = task.completed;
    checkbox.setAttribute("aria-label", `Mark ${task.text} as ${task.completed ? "incomplete" : "complete"}`);

    const text = document.createElement("span");
    text.className = "task-text";
    text.textContent = task.text;

    const deleteButton = document.createElement("button");
    deleteButton.className = "delete-button";
    deleteButton.type = "button";
    deleteButton.dataset.action = "delete";
    deleteButton.setAttribute("aria-label", `Delete ${task.text}`);
    deleteButton.textContent = "Delete";

    label.append(checkbox, text);
    item.append(label, deleteButton);
    taskList.append(item);
  }

  updateSummary();
}

// يضيف مهمة جديدة بمعرّف ثابت، ثم يعيد رسم القائمة والعداد.
function addTask(text) {
  tasks.push({
    id: nextTaskId,
    text,
    completed: false,
  });
  nextTaskId += 1;
  renderTasks();
}

// يعالج الإرسال من الزر أو Enter، ويتحقق من النص قبل إضافته.
taskForm.addEventListener("submit", (event) => {
  event.preventDefault();
  const text = taskInput.value.trim();

  if (!text) {
    taskInput.setAttribute("aria-invalid", "true");
    inputFeedback.textContent = "Please enter a task.";
    taskInput.focus();
    return;
  }

  addTask(text);
  taskInput.value = "";
  taskInput.removeAttribute("aria-invalid");
  inputFeedback.textContent = "";
  taskInput.focus();
});

// يمسح رسالة الخطأ بمجرد أن يبدأ المستخدم بالكتابة مجددًا.
taskInput.addEventListener("input", () => {
  taskInput.removeAttribute("aria-invalid");
  inputFeedback.textContent = "";
});

// مستمع واحد للقائمة كلها يحدّث حالة المهمة عند تغيير مربع الاختيار.
taskList.addEventListener("change", (event) => {
  if (!(event.target instanceof HTMLInputElement) || !event.target.matches(".task-checkbox")) return;

  const item = event.target.closest(".task-item");
  const task = tasks.find((entry) => String(entry.id) === item?.dataset.taskId);
  if (!task) return;

  task.completed = event.target.checked;
  item.classList.toggle("is-complete", task.completed);
  event.target.setAttribute("aria-label", `Mark ${task.text} as ${task.completed ? "incomplete" : "complete"}`);
  updateSummary();
});

// تفويض الأحداث يجعل زر الحذف يعمل أيضًا مع عناصر القائمة التي أُنشئت لاحقًا.
taskList.addEventListener("click", (event) => {
  if (!(event.target instanceof Element)) return;
  const deleteButton = event.target.closest('button[data-action="delete"]');
  if (!deleteButton) return;

  const item = deleteButton.closest(".task-item");
  const taskIndex = tasks.findIndex((task) => String(task.id) === item?.dataset.taskId);
  if (taskIndex === -1) return;

  tasks.splice(taskIndex, 1);
  renderTasks();
});

// يرسم حالة البداية: قائمة فارغة ورسالة توجيهية وملخص بلا أرقام.
renderTasks();
