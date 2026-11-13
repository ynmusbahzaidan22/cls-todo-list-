# Project discussion guide

Use this as a concise walkthrough, then open the linked source when asked about implementation detail. Database and deployment statements should be tied to the current environment: migration files do not prove that a given database has applied them.

## Quick lookup for the discussion

| If asked about | Open |
| --- | --- |
| Login | `frontend/pages/login/auth.html`, `frontend/pages/login/form.html`, `frontend/scripts/auth/views.js` |
| Signup | `frontend/pages/signup/form.html`, `frontend/scripts/auth/sessions.js` |
| User Dashboard | `frontend/pages/user-dashboard/dashboard.html`, `frontend/sections/tasks/`, `frontend/scripts/core/workspace.js` |
| Admin Dashboard | `frontend/pages/admin-dashboard/dashboard.html`, `frontend/scripts/admin/dashboard.js`, `frontend/scripts/components/admin.js` |
| Login API call | `frontend/scripts/auth/sessions.js`, `internal/httpapi/auth_handlers.go` |
| Session validation | `internal/httpapi/auth_handlers.go`, `internal/auth/service.go`, `internal/repository/auth.go` |
| Admin permissions | `internal/httpapi/auth_handlers.go`, `internal/admin/service.go`, `internal/database/postgres.go` |
| Routes | `internal/httpapi/router.go` |
| Handlers | `internal/httpapi/auth_handlers.go`, `internal/httpapi/task_handlers.go`, `internal/httpapi/template_handlers.go`, `internal/httpapi/admin_handlers.go`, `internal/httpapi/preference_handlers.go` |
| Business logic | `internal/auth/`, `internal/task/`, `internal/admin/`, `internal/adminbootstrap/` |
| Database queries | `internal/repository/` |
| Models and DTOs | `internal/auth/types.go`, `internal/task/types.go`, `internal/admin/types.go`, handler request/response structs |
| Migrations | `migrations/001_*.sql` through `migrations/015_*.sql`; target state is checked with `tern*.conf` and `ops/Invoke-*.ps1` |
| Tests | `internal/**/**_test.go`, `tests/frontend/`, `tests/e2e/`, `tests/e2e-support/` |
| Docker | `Dockerfile`, `.dockerignore`, `compose.yaml`, `compose.test.yaml`, `compose.production.yaml` |
| Application configuration | `internal/config/config.go`, `cmd/api/main.go`, `tern.conf`, `tern.test.conf`, `tern.production.conf` |

## 30-second overview

The application is a same-origin web app: a semantic HTML and Vanilla JavaScript client talks to a Go HTTP API, which delegates business rules to services and persistence to PostgreSQL repositories. Authentication uses server-side sessions in secure cookies. SQL migrations are versioned with Tern, and distinct wrappers protect development, isolated test, and production migration targets.

## Architecture walkthrough

```text
Browser
  → internal/httpapi/router.go
  → internal/httpapi/*_handlers.go
  → internal/auth, internal/task, internal/admin
  → internal/repository/*.go
  → pgx pool in internal/database/postgres.go
  → PostgreSQL app schema
```

The frontend page is `frontend/pages/index.html`. The deferred script list in that page wires shared state, authentication, templates, workspace/task interactions, Admin UI, and preferences. The server exposes explicit asset routes rather than a repository-wide static file server.

## Feature traces

### Registration and login

`POST /api/auth/register` and `POST /api/auth/login` enter through `internal/httpapi/auth_handlers.go`. The auth service applies account rules and `internal/auth/password.go` verifies or creates Argon2id hashes. `internal/repository/auth.go` persists users and sessions. The browser session is a cookie; the session token is validated server-side on protected requests.

### Task lifecycle

Task requests are registered in `internal/httpapi/router.go`, handled in `internal/httpapi/task_handlers.go`, checked in `internal/task/service.go`, and implemented by `internal/repository/tasks.go`. Ownership is derived from the authenticated session context. History and Undo are stored through the task repository and activity schema, while bulk operations enforce atomic ownership/version checks.

### Templates

Template HTTP routes live in `internal/httpapi/template_handlers.go`; domain operations are in `internal/task/templates.go`; owner-scoped SQL is in `internal/repository/templates.go`. `migrations/005_create_task_templates.sql`, `010_track_task_template_applications.sql`, and `012_enforce_template_item_position_uniqueness.sql` establish core template storage and constraints.

### Admin

The router wraps Admin endpoints with authentication and Admin authorization. The trusted role comes from the server-side user/session record, not client-supplied fields. `internal/admin/` defines use cases, `internal/repository/admin.go` performs privileged reads/management writes, and `frontend/scripts/admin/dashboard.js` renders the UI. Initial Admin selection is done by the guarded `adminctl` operational command; it is not an unauthenticated web endpoint.

### Runtime database privilege policy

`ops/provision_runtime_role.sql` and `ops/grant_runtime_privileges.sql` create the separate application role and narrow its privileges. At startup in production, `internal/database/postgres.go` checks effective privileges. Migration commands use owner-level credentials; the API uses the runtime role.

## Security points to explain

- Session cookies are `HttpOnly`; production secure-cookie configuration uses `Secure` and the `__Host-` prefix. Session records are server-side and can be revoked.
- Passwords are stored as Argon2id hashes, never as clear text.
- Services and repository queries bind task/template operations to the authenticated owner ID.
- Admin routes require both an authenticated session and the Admin role; UI visibility does not grant access.
- PostgreSQL uses separate migration-owner and runtime roles. Runtime grants are verified at startup.
- Test database helpers reject non-isolated targets; Playwright uses a dedicated disposable test database configuration.

## Frequently asked design questions

| Question | Short answer | Source to show |
| --- | --- | --- |
| Why Go layers? | HTTP parsing, domain policy, and SQL persistence have separate responsibilities and focused tests. | `cmd/api/main.go`, `internal/httpapi/`, `internal/task/`, `internal/repository/` |
| How is identity established? | A random session cookie is looked up in server-side session storage; protected handlers use the resolved account. | `internal/httpapi/auth_handlers.go`, `internal/repository/auth.go` |
| How is user data isolated? | The service/repository receives the authenticated owner identity and SQL scopes records to it. | `internal/repository/tasks.go`, `internal/repository/templates.go` |
| Can an Admin edit any user's tasks from the Admin task view? | No. That view is intentionally read-only; account-management routes are separate. | `internal/httpapi/admin_handlers.go`, `internal/repository/admin.go` |
| What does a migration file prove? | Only that a migration is in source history. Tern state must be checked on a named database to establish application status. | `migrations/`, `tern*.conf`, `ops/Invoke-*.ps1` |
| How are migrations prevented from targeting the wrong environment? | Environment-specific Tern config plus explicit guarded PowerShell wrappers validate target identity before applying. | `ops/`, `tern*.conf` |

## Useful safe commands

```powershell
go test ./internal/auth ./internal/task ./internal/admin ./internal/httpapi
go test ./...
go vet ./...
go build ./cmd/api ./cmd/adminctl
npm run test:frontend
npm run test:html
npm run test:e2e
```

PostgreSQL integration and Playwright commands require the documented isolated test environment. Load `.env.test` using the workflow in `README.md`/`docs/OPERATIONS.md`; never substitute a development or production URL. `go test ./...` can skip database integration tests when the explicit test URL is absent unless integration is required, so inspect test output for skips when live coverage is expected.
