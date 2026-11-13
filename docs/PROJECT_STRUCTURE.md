# Project structure

This map describes the checked-in layout at the current repository revision. The Go layers and migration directory stay at the repository root because Go package boundaries, Docker builds, and guarded migration scripts use those paths. Frontend assets and standalone Node tests are grouped by responsibility.

## Current tree

```text
ft/
├── frontend/
│   ├── pages/                 # Login, signup, user/Admin dashboard partials and shell
│   ├── sections/              # Navigation, tasks, templates, lifecycle, activity, preferences
│   ├── scripts/               # Auth, user/Admin behavior, API helpers, components and utilities
│   └── styles/                # Ordered stylesheet sections and entry point
├── cmd/                       # Go server and adminctl entry points
├── internal/                  # Go handlers, services, repositories, database and safety
├── migrations/                # Tern migrations 001–015, numeric order
├── ops/                       # Guarded migration and runtime-role operations
├── tests/                     # Frontend, Playwright, and E2E-support tests
├── docs/                      # Operations, handoff, checklist, and discussion guides
├── Dockerfile / compose*.yaml # Root-context application and database services
├── tern*.conf                 # Environment-specific Tern settings
├── README.md                  # Repository entry point
└── go.mod, go.sum, package*.json
```

## Quick navigation guide

| Question | Start here |
| --- | --- |
| How does the application start? | `cmd/api/main.go` |
| Where are HTTP routes registered? | `internal/httpapi/router.go` |
| Where are login and sessions implemented? | `internal/httpapi/auth_handlers.go`, `internal/auth/service.go`, `internal/repository/auth.go` |
| Where are passwords hashed? | `internal/auth/password.go` |
| Where are tasks implemented? | `internal/httpapi/task_handlers.go`, `internal/task/service.go`, `internal/repository/tasks.go` |
| Where is Admin implemented? | `internal/admin/`, `internal/httpapi/admin_handlers.go`, `internal/repository/admin.go` |
| Where are database queries? | `internal/repository/` |
| Where are migrations? | `migrations/` |
| Where is security configuration enforced? | `internal/config/config.go`, `internal/testdatabase/safety.go`, `internal/database/postgres.go`, `internal/httpapi/router.go` |
| Where are Docker and deployment settings? | `Dockerfile`, `compose*.yaml`, `ops/`, `tern*.conf` |
| Where are tests? | Go tests beside packages, `tests/frontend/`, `tests/e2e/`, and `tests/e2e-support/` |

## Root and configuration

| Path | Responsibility / key contents | When to open it |
| --- | --- | --- |
| `go.mod`, `go.sum` | Go module identity and pinned dependency checksums. | Dependency or build questions. |
| `package.json`, `package-lock.json` | Node checks, HTML validation, and Playwright command/dependency lock. | Frontend tooling or browser-test commands. |
| `.gitignore`, `.dockerignore` | Exclude local secrets, build output, test artifacts, and unnecessary Docker build context. | Before adding files or changing the build context. |
| `.env.example` | Names and placeholder shape of local configuration; it is a template, not a secret store. | Local setup. Do not treat placeholders as credentials. |
| `.vscode/settings.json` | Hides local environment files and generated folders from Explorer/search while keeping `.env.example` visible. | Editor setup. |

## Frontend

The browser app is served from one origin. `frontend/pages/index.html` is a small shell whose fixed include markers are expanded by the Go router from an internal allowlist. Partial files are not directly routable. The assembled response retains the original semantic markup; ordered classic scripts share existing page state and load with `defer`.

| Path | Responsibility / key contents | When to open it |
| --- | --- | --- |
| `frontend/pages/index.html` | Small document shell, asset order, and page/section include slots. | Page assembly or script/style loading order. |
| `frontend/pages/login/auth.html`, `frontend/pages/login/form.html` | Session/loading/error states, login view, and sign-in form. | Login screen markup. |
| `frontend/pages/signup/form.html` | Account creation form included in the auth view. | Signup markup. |
| `frontend/pages/user-dashboard/dashboard.html` | Authenticated workspace container and view order. | User dashboard composition. |
| `frontend/pages/admin-dashboard/dashboard.html` | Admin overview, user previews, and read-only cross-user task view. | Admin dashboard markup. |
| `frontend/sections/navigation/header.html` | Signed-in header, navigation, account label, and logout control. | Shared navigation. |
| `frontend/sections/tasks/` | Task form/list, task workspace, Recently Deleted preview/view, and Completed view. | Task and lifecycle sections. |
| `frontend/sections/templates/` | Template list and editor/detail/application dialogs. | Templates and modal markup. |
| `frontend/sections/activity/dialog.html` | Activity history and Undo dialog. | Task activity UI. |
| `frontend/sections/preferences/time-zone.html` | Account time-zone control embedded in the user workspace. | Preference markup. |
| `frontend/styles/style.css` | Ordered CSS entry point importing the sections below. | Style loading and global CSS order. |
| `frontend/styles/foundation.css` | First cascade section, including tokens and base/task styles. | Shared visual foundation. |
| `frontend/styles/components.css` | Middle cascade section for shared/auth/workspace components. | Component styling. |
| `frontend/styles/responsive.css` | Final cascade section, including later lifecycle/Admin styles and responsive rules. | Final overrides. |
| `frontend/scripts/core/state.js` | Shared UI state and DOM references. | View state or DOM element wiring. |
| `frontend/scripts/auth/views.js` | Login/register view rendering, field validation, and feedback. | Authentication screen behavior. |
| `frontend/scripts/services/http.js` | Shared JSON response parsing and safe API error messages. | Frontend response handling. |
| `frontend/scripts/utils/presentation.js` | Pure email masking, password-toggle, time-zone visibility, and Admin task view-model helpers. | Reusable presentation rules and focused UI tests. |
| `frontend/scripts/auth/sessions.js` | Session check, login, registration, and logout requests. | Browser authentication flow. |
| `frontend/scripts/tasks/workspace.js` | Task rendering and lifecycle actions, filters, history, Undo, and bulk operations. | Task interaction behavior. |
| `frontend/scripts/templates/templates.js` | Template list, editor, picker, and application flow. | Reusable task templates. |
| `frontend/scripts/admin/dashboard.js` | Admin previews, paged views, search, and account-management actions. | Admin browser UI. |
| `frontend/scripts/components/admin.js` | DOM renderers for Admin user previews, account rows, and read-only task cards. | Admin presentation without API loading or mutation policy. |
| `frontend/scripts/preferences/time-zone.js` | Account time-zone preference loading, validation, and saving. | Date display preferences. |
| `frontend/scripts/core/workspace.js` | Initializes and resets the authenticated workspace. | Account changes and workspace lifecycle. |

Script execution order is the order in `frontend/pages/index.html`. These remain classic deferred scripts so shared top-level state retains its existing behavior; do not reorder them casually.

## Backend entry points

| Path | Responsibility / key contents | When to open it |
| --- | --- | --- |
| `cmd/api/main.go` | Loads configuration, opens PostgreSQL, wires services/handlers, starts HTTP and maintenance, handles shutdown. | Server startup or dependency wiring. |
| `cmd/adminctl/main.go` | Guarded operational CLI for initial Admin bootstrap/status. | First-Admin operational procedure. |

## HTTP routes and handlers

| Path | Responsibility / key contents | When to open it |
| --- | --- | --- |
| `internal/httpapi/router.go` | Frontend asset allowlist, health routes, API route registration, middleware ordering. | Route, origin, proxy, or security-header questions. |
| `internal/httpapi/auth_handlers.go` | Registration, login, logout, current-user, session middleware, role checks. | Request-to-auth flow. |
| `internal/httpapi/task_handlers.go` | Task CRUD/listing, bulk, activity, Undo, search, pagination. | Task HTTP contract and validation. |
| `internal/httpapi/template_handlers.go` | Template CRUD/list/apply endpoints. | Template HTTP contract. |
| `internal/httpapi/admin_handlers.go` | Read-only Admin listing and protected account management endpoints. | Admin status/role/session APIs. |
| `internal/httpapi/preference_handlers.go` | Authenticated account preference API. | Time-zone preference endpoints. |

## Authentication and sessions

| Path | Responsibility / key contents | When to open it |
| --- | --- | --- |
| `internal/auth/service.go`, `internal/auth/types.go` | Authentication rules and domain types. | Registration/login behavior. |
| `internal/auth/password.go` | Argon2id password hashing and verification. | Password security discussion. |
| `internal/repository/auth.go` | User/session persistence, login/session queries, rate-limit storage. | Database implementation of auth. |
| `migrations/002_create_auth_and_tasks.sql`, `migrations/011_add_user_names_and_roles.sql`, `migrations/013_add_user_time_zone_preference.sql` | Auth schema, user role/name evolution, and account preference schema. | Schema history; do not infer application state from file presence. |

## Tasks and templates

| Path | Responsibility / key contents | When to open it |
| --- | --- | --- |
| `internal/task/service.go`, `internal/task/types.go` | Task business rules and request/result types. | Validation, lifecycle, and service behavior. |
| `internal/task/due_date.go` | Due-date and calendar/time-zone domain calculations. | Due/overdue behavior. |
| `internal/task/templates.go` | Template domain behavior and conversion to tasks. | Template rules. |
| `internal/repository/tasks.go` | Owner-scoped SQL, task history, bulk operations, pagination, and lifecycle queries. | Task SQL and ownership enforcement. |
| `internal/repository/templates.go` | Owner-scoped template persistence and item ordering. | Template SQL and isolation. |
| `migrations/003_create_task_activity.sql`, `migrations/005_create_task_templates.sql`, `migrations/007_add_deleted_task_listing_index.sql`, `migrations/008_add_task_priority.sql`, `migrations/009_add_due_dates_and_time_zones.sql`, `migrations/010_track_task_template_applications.sql`, `migrations/012_enforce_template_item_position_uniqueness.sql` | Task schema, history, lifecycle constraints, and template item ordering. | Database structure and query support. |

## Admin

| Path | Responsibility / key contents | When to open it |
| --- | --- | --- |
| `internal/admin/service.go`, `internal/admin/types.go` | Admin policy and response types. | Admin use cases. |
| `internal/repository/admin.go` | Admin user/task list and management queries. | Cross-user Admin SQL. |
| `internal/adminbootstrap/bootstrap.go` | Guarded, transactional initial Admin selection. | Bootstrap controls and transaction flow. |
| `internal/repository/admin_bootstrap.go` | Bootstrap persistence, guard locking, and audit record. | Bootstrap SQL. |
| `ops/provision_runtime_role.sql`, `ops/grant_runtime_privileges.sql` | Runtime database role provisioning and least-privilege grants. | Deployment database privileges. |

## Repository and PostgreSQL

| Path | Responsibility / key contents | When to open it |
| --- | --- | --- |
| `internal/repository/` | SQL operations grouped by accounts, tasks, templates, preferences, and Admin. | Persistence/query details. |
| `internal/database/postgres.go` | pgx pool opening and effective runtime-role verification. | Pool behavior and privilege policy. |
| `internal/database/postgres_test.go`, `runtime_role_test.go` | Database verifier unit/integration coverage. | Runtime privilege policy tests. |
| `internal/health/service.go` | Database readiness probe. | `/health/ready` behavior. |
| `internal/testdatabase/safety.go` | Fail-closed test URL parsing and isolation guard. | Any PostgreSQL integration test target. |

## Database configuration and migrations

| Path | Responsibility / key contents | When to open it |
| --- | --- | --- |
| `tern.conf`, `tern.test.conf`, `tern.production.conf` | Tern configurations for development, isolated test, and production targets. | Migration target resolution. |
| `migrations/001_create_app_schema.sql` | Creates the application schema foundation. | First schema migration. |
| `migrations/002_create_auth_and_tasks.sql` | Adds accounts, sessions, and initial task tables. | Core account/task schema. |
| `migrations/003_create_task_activity.sql`, `migrations/004_add_safe_task_undo.sql` | Adds task history and safe Undo support. | Activity and Undo schema. |
| `migrations/005_create_task_templates.sql`, `migrations/010_track_task_template_applications.sql`, `migrations/012_enforce_template_item_position_uniqueness.sql` | Adds templates, application tracking, and ordered-item uniqueness. | Template persistence and integrity. |
| `migrations/006_add_shared_rate_limits.sql` | Shared login/rate-limit persistence schema. | Rate-limit database model. |
| `migrations/007_add_deleted_task_listing_index.sql` | Adds deleted-task listing support. | Deleted-task query index. |
| `migrations/008_add_task_priority.sql`, `migrations/009_add_due_dates_and_time_zones.sql`, `migrations/013_allow_device_timezone_default.sql` | Adds priority, due-date/time-zone behavior, and the device-time-zone preference default. | Task scheduling and preference schema. |
| `migrations/011_add_user_names_and_roles.sql` | Adds account display name and role data. | Authorization account schema. |
| `migrations/014_admin_account_audit.sql`, `migrations/015_allow_initial_admin_bootstrap_audit.sql` | Adds Admin account audit and initial-bootstrap audit schema. | Admin safety/audit storage. |

The current source inventory is migrations `001` through `015` in numeric order. Tern's recorded version is database-specific and must be checked against the intended target; file presence is not an applied-state claim.

## Operations and deployment

| Path | Responsibility / key contents | When to open it |
| --- | --- | --- |
| `ops/Invoke-DevelopmentMigrations.ps1`, `ops/DevelopmentMigrationGuard.psm1` | Development-only guarded migration status/apply path. | Development migration operations. |
| `ops/Invoke-TestMigrations.ps1` | Isolated test migration workflow. | Test database migrations. |
| `ops/Invoke-ProductionMigrations.ps1`, `ops/ProductionMigrationGuard.psm1` | Explicit production migration gate. | Production migration planning/execution. |
| `ops/Test-DevelopmentMigrationGuard.ps1`, `ops/Test-ProductionMigrationGuard.ps1`, `ops/Test-RuntimeRoleScripts.ps1` | Static safety checks for migration guards and privilege scripts. | Operational guard validation. |
| `README.md` | Project entry point, setup, feature overview, and common verification commands. | First orientation. |
| `docs/OPERATIONS.md` | Operational roles, migration, backup, deployment, and incident guidance. | Safe operation. |
| `docs/PROJECT_COMPLETION_CHECKLIST.md`, `docs/FINAL_HANDOFF.md` | Historical project completion and handoff records. | Project history; date-sensitive claims remain historical. |
| `docs/` | Current structure, discussion, and API maps. | Project walkthroughs. |

## Docker

| Path | Responsibility / key contents | When to open it |
| --- | --- | --- |
| `Dockerfile` | Multi-stage Go build and runtime image; copies only `frontend/` into the runtime image. | Image content/startup. |
| `compose.yaml`, `compose.test.yaml`, `compose.production.yaml` | Development, isolated test, and production Compose definitions. | Local/test deployment wiring. |
| `.dockerignore` | Keeps local and irrelevant content out of the build context. | Docker build context review. |

## Tests

| Path | Responsibility / key contents | When to open it |
| --- | --- | --- |
| `tests/frontend/ui.test.js` | Focused browser markup/presentation behavior checks. | Frontend behavior checks without a browser/database. |
| `tests/e2e-support/` | Node unit tests for rate-limit fixture cleanup and Playwright configuration/database guards. | Test-harness safety without connecting to a database. |
| `tests/e2e/*.spec.js`, `tests/e2e/global-setup.js` | Browser integration scenarios and their setup/cleanup. | End-to-end test flows. |
| `tests/e2e/test-database.js` | Playwright database URL guards, fixture identity, and rate-limit cleanup support. | E2E isolation review. |
| `tools/assemble-frontend-page.mjs`, `tools/validate-frontend-html.mjs` | Shared allowlisted page assembly for frontend checks, then HTML validation without writing generated output. | HTML structure validation. |
| `tools/check-js-syntax.mjs` | Runs `node --check` over repository JavaScript files while skipping generated folders. | Frontend/tooling syntax checks. |
| `internal/**/**_test.go` | Go unit and PostgreSQL integration tests colocated with packages; same-package tests intentionally exercise unexported internals. | Package behavior and integration coverage. |
| `cmd/api/main_test.go`, `cmd/adminctl/main_test.go` | Entry-point and operational CLI tests. | Startup/shutdown or CLI verification. |

## Files normally not studied as application logic

`go.sum` and `package-lock.json` are generated dependency locks; `.cache/`, `node_modules/`, Playwright reports/results, coverage output, and local backups are generated/local artifacts. VS Code hides the lock files and generated folders but they remain available to the build. `.env` and `.env.test` are private local configuration and are ignored; never include their contents in documentation, review output, or Git.

Do not edit existing files under `migrations/` to change deployed history; add a new ordered migration instead. Update `go.sum` and `package-lock.json` through the corresponding Go/Node dependency tools rather than hand-editing checksums. Do not edit generated test reports or build/cache output.
