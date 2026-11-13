# API and route map

All application API routes are registered in `internal/httpapi/router.go`. The authenticated identity and role come from the server-side session; task and template ownership is derived from that identity. Mutating requests also pass configured same-origin checks. Admin routes require an authenticated Admin role.

## Health and frontend routes

| Method | Endpoint | Handler | Service | Repository | Frontend caller |
| --- | --- | --- | --- | --- | --- |
| `GET` | `/health/live` | Router closure | — | — | Platform health probe |
| `GET` | `/health/ready` | Router closure | `internal/health/service.go` | `internal/database/postgres.go` | Platform readiness probe |
| `GET` | `/`, `/index.html` | `serveFrontendPage` | — | — | Browser navigation |
| `GET` | `/styles/style.css`, `/styles/foundation.css`, `/styles/components.css`, `/styles/responsive.css` | Frontend asset allowlist | — | — | `frontend/pages/index.html` |
| `GET` | `/scripts/utils/presentation.js`, `/scripts/core/state.js`, `/scripts/auth/views.js`, `/scripts/services/http.js`, `/scripts/auth/sessions.js` | Frontend asset allowlist | — | — | `frontend/pages/index.html` deferred script order |
| `GET` | `/scripts/templates/templates.js`, `/scripts/core/workspace.js`, `/scripts/components/admin.js`, `/scripts/admin/dashboard.js`, `/scripts/preferences/time-zone.js`, `/scripts/tasks/workspace.js` | Frontend asset allowlist | — | — | `frontend/pages/index.html` deferred script order |

HTML partials under `frontend/pages/` and `frontend/sections/` are assembled from a fixed server-side allowlist and are not directly routable.

## Authentication and account preferences

| Method | Endpoint | Handler | Service | Repository | Frontend caller |
| --- | --- | --- | --- | --- | --- |
| `POST` | `/api/auth/register` | `AuthHandlers.register` | `internal/auth/service.go` | `internal/repository/auth.go` | `frontend/scripts/auth/sessions.js` |
| `POST` | `/api/auth/login` | `AuthHandlers.login` | `internal/auth/service.go` | `internal/repository/auth.go` | `frontend/scripts/auth/sessions.js` |
| `GET` | `/api/auth/me` | `AuthHandlers.me` + authentication middleware | `internal/auth/service.go` | `internal/repository/auth.go` | `frontend/scripts/auth/sessions.js` |
| `POST` | `/api/auth/logout` | `AuthHandlers.logout` | `internal/auth/service.go` | `internal/repository/auth.go` | `frontend/scripts/auth/sessions.js` |
| `GET` | `/api/preferences` | `PreferenceHandlers.get` + authentication middleware | Preference handler | `internal/repository/preferences.go` | `frontend/scripts/preferences/time-zone.js` |
| `PUT` | `/api/preferences` | `PreferenceHandlers.update` + authentication middleware | Preference handler | `internal/repository/preferences.go` | `frontend/scripts/preferences/time-zone.js` |

## Tasks, history, lifecycle, and bulk operations

| Method | Endpoint | Handler | Service | Repository | Frontend caller |
| --- | --- | --- | --- | --- | --- |
| `GET` | `/api/tasks` | `TaskHandlers.list` | `internal/task/service.go` | `internal/repository/tasks.go` | `frontend/scripts/tasks/workspace.js` |
| `POST` | `/api/tasks` | `TaskHandlers.create` | `internal/task/service.go` | `internal/repository/tasks.go` | `frontend/scripts/tasks/workspace.js` |
| `GET` | `/api/tasks/{taskID}` | `TaskHandlers.get` | `internal/task/service.go` | `internal/repository/tasks.go` | `frontend/scripts/tasks/workspace.js` |
| `PUT` | `/api/tasks/{taskID}` | `TaskHandlers.update` | `internal/task/service.go` | `internal/repository/tasks.go` | `frontend/scripts/tasks/workspace.js` |
| `DELETE` | `/api/tasks/{taskID}` | `TaskHandlers.delete` | `internal/task/service.go` | `internal/repository/tasks.go` | `frontend/scripts/tasks/workspace.js` |
| `POST` | `/api/tasks/{taskID}/restore` | `TaskHandlers.restore` | `internal/task/service.go` | `internal/repository/tasks.go` | `frontend/scripts/tasks/workspace.js` |
| `GET` | `/api/tasks/deleted` | `TaskHandlers.listDeleted` | `internal/task/service.go` | `internal/repository/tasks.go` | `frontend/scripts/tasks/workspace.js` |
| `GET` | `/api/tasks/deleted/count` | `TaskHandlers.deletedCount` | `internal/task/service.go` | `internal/repository/tasks.go` | `frontend/scripts/tasks/workspace.js` |
| `GET` | `/api/tasks/completed` | `TaskHandlers.listCompleted` | `internal/task/service.go` | `internal/repository/tasks.go` | `frontend/scripts/tasks/workspace.js` |
| `POST` | `/api/tasks/bulk` | `TaskHandlers.bulk` | `internal/task/service.go` | `internal/repository/tasks.go` | `frontend/scripts/tasks/workspace.js` |
| `GET` | `/api/tasks/{taskID}/activity` | `TaskHandlers.activity` | `internal/task/service.go` | `internal/repository/tasks.go` | `frontend/scripts/tasks/workspace.js` |
| `POST` | `/api/tasks/{taskID}/activity/{eventID}/undo` | `TaskHandlers.undoActivity` | `internal/task/service.go` | `internal/repository/tasks.go` | `frontend/scripts/tasks/workspace.js` |

These task routes are authenticated and owner-scoped. The bulk endpoint validates the batch atomically; the Admin all-user task endpoint below is a separate read-only route.

## Task templates

| Method | Endpoint | Handler | Service | Repository | Frontend caller |
| --- | --- | --- | --- | --- | --- |
| `GET` | `/api/task-templates` | `TemplateHandlers.list` | `internal/task/templates.go` | `internal/repository/templates.go` | `frontend/scripts/templates/templates.js` |
| `POST` | `/api/task-templates` | `TemplateHandlers.create` | `internal/task/templates.go` | `internal/repository/templates.go` | `frontend/scripts/templates/templates.js` |
| `GET` | `/api/task-templates/{templateID}` | `TemplateHandlers.get` | `internal/task/templates.go` | `internal/repository/templates.go` | `frontend/scripts/templates/templates.js` |
| `PUT` | `/api/task-templates/{templateID}` | `TemplateHandlers.update` | `internal/task/templates.go` | `internal/repository/templates.go` | `frontend/scripts/templates/templates.js` |
| `DELETE` | `/api/task-templates/{templateID}` | `TemplateHandlers.delete` | `internal/task/templates.go` | `internal/repository/templates.go` | `frontend/scripts/templates/templates.js` |
| `POST` | `/api/task-templates/{templateID}/apply` | `TemplateHandlers.apply` | `internal/task/templates.go` | `internal/repository/templates.go` | `frontend/scripts/templates/templates.js` |

## Admin

| Method | Endpoint | Handler | Service | Repository | Frontend caller |
| --- | --- | --- | --- | --- | --- |
| `GET` | `/api/admin/users` | `AdminHandlers.listUsers` + auth/Admin middleware | `internal/admin/service.go` | `internal/repository/admin.go` | `frontend/scripts/admin/dashboard.js` |
| `GET` | `/api/admin/tasks` | `AdminHandlers.listTasks` + auth/Admin middleware | `internal/admin/service.go` | `internal/repository/admin.go` | `frontend/scripts/admin/dashboard.js` |
| `PATCH` | `/api/admin/users/{userID}/role` | `AdminHandlers.changeUserRole` + auth/Admin middleware | `internal/admin/service.go` | `internal/repository/admin.go` | `frontend/scripts/admin/dashboard.js` |
| `PATCH` | `/api/admin/users/{userID}/status` | `AdminHandlers.changeUserStatus` + auth/Admin middleware | `internal/admin/service.go` | `internal/repository/admin.go` | `frontend/scripts/admin/dashboard.js` |
| `POST` | `/api/admin/users/{userID}/sessions/revoke` | `AdminHandlers.revokeUserSessions` + auth/Admin middleware | `internal/admin/service.go` | `internal/repository/admin.go` | `frontend/scripts/admin/dashboard.js` |

The Admin task list is read-only. Initial Admin selection is a guarded CLI operation, not an HTTP route: `cmd/adminctl/` → `internal/adminbootstrap/` → `internal/repository/admin_bootstrap.go`.
