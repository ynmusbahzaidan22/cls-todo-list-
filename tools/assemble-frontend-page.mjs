import { readFile } from 'node:fs/promises';

const allowedPartials = new Set([
  'pages/login/auth.html',
  'pages/login/form.html',
  'pages/signup/form.html',
  'pages/user-dashboard/dashboard.html',
  'pages/admin-dashboard/dashboard.html',
  'sections/navigation/header.html',
  'sections/tasks/task-list.html',
  'sections/tasks/workspace.html',
  'sections/tasks/recently-deleted-preview.html',
  'sections/tasks/recently-deleted.html',
  'sections/tasks/completed.html',
  'sections/templates/templates.html',
  'sections/templates/dialogs.html',
  'sections/preferences/time-zone.html',
  'sections/activity/dialog.html',
]);
const markerStart = '<!-- include: ';
const markerEnd = ' -->';

export async function assembleFrontendPage(frontendURL) {
  let page = await readFile(new URL('pages/index.html', frontendURL), 'utf8');
  for (let count = 0; page.includes(markerStart); count++) {
    if (count >= 32) throw new Error('Frontend HTML has too many nested includes.');
    const start = page.indexOf(markerStart);
    const end = page.indexOf(markerEnd, start);
    if (end < 0) throw new Error('Frontend HTML contains an incomplete include marker.');
    const partialPath = page.slice(start + markerStart.length, end);
    if (!allowedPartials.has(partialPath)) throw new Error('Frontend HTML includes a non-allowlisted partial.');
    const partial = await readFile(new URL(partialPath, frontendURL), 'utf8');
    page = `${page.slice(0, start)}${partial}${page.slice(end + markerEnd.length)}`;
  }
  return page;
}
