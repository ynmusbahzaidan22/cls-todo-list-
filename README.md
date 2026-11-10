# To-Do List application

This repository contains an HTML, CSS, and Vanilla JavaScript frontend with local login and registration, plus a Go HTTP API backed by PostgreSQL. The Go server serves the allowlisted frontend files and API from one origin, without exposing other repository files. Authenticated task management is connected to the backend API, with PostgreSQL as the source of truth. See [OPERATIONS.md](OPERATIONS.md) for database roles, production configuration, reverse proxy assumptions, backups, and recovery.

## Requirements

- Go 1.26 or newer.
- The `go` and `gofmt` executables available through `PATH`. To select a non-default Go executable for Playwright's API server, set `GO_EXE` to its path; paths containing spaces are supported.
- Docker Desktop with the standalone `docker-compose.exe` command.
- Tern v2.4.3 for SQL migration commands.
- Node.js 20.12 or newer for browser and HTML checks.

## Local setup (Windows PowerShell)

1. Copy `.env.example` to `.env` and replace the local-only `POSTGRES_PASSWORD` placeholder. The example connection URLs point to the same local database and are placeholders; before running tests, provision a separate test database and set `TEST_DATABASE_URL` to that database. `DATABASE_URL` is reserved for the API runtime role used by local development.
2. Start PostgreSQL from the project directory:

   ```powershell
   docker-compose.exe -f compose.yaml up -d
   docker-compose.exe -f compose.yaml ps
   ```

   Wait until the `postgres` service reports `healthy`. Its port is bound to `127.0.0.1`; data lives in a named Docker volume, outside OneDrive.
3. Load the non-secret local settings into the current PowerShell process, install the pinned migration CLI, and inspect/apply migrations:

   ```powershell
   Get-Content .env | ForEach-Object {
     if ($_ -match '^\s*([^#=\s]+)\s*=\s*(.*?)\s*$') {
       [Environment]::SetEnvironmentVariable($matches[1], $matches[2], 'Process')
     }
   }
   go install github.com/jackc/tern/v2@v2.4.3
   tern version
   tern status --migrations migrations
   tern migrate --migrations migrations
   tern status --migrations migrations
   ```

   Tern reads `POSTGRES_DB`, `POSTGRES_USER`, and `POSTGRES_PASSWORD` while rendering `tern.conf`. The explicit `--migrations migrations` argument is required because Tern otherwise searches the current directory. Migrations 001–005 create the schema, auth/tasks, activity, Undo, and templates. Migration 006 adds bounded shared authentication rate-limit counters; migration 007 adds the recently-deleted listing index; migration 008 adds task and template-item priorities plus priority activity events; migration 009 adds due dates and account time zones; migration 010 adds stable template-item identities and tracks template application sets. Applying or deleting templates does not leave generated tasks linked to them. Tern tracks applied versions; rerunning the migrate command is safe. No down migrations are provided for the core application schema; migrations 008 and 010 have guarded rollbacks.
4. Provision the runtime account using the administrator session. If the role has no password, the psql script prompts once for an initial password without echoing it; rerunning the script does not rotate an existing password. Choose a strong password, then put it only in the `DATABASE_URL` user info in `.env`. Keep the administrator URL for the separately provisioned test database in `TEST_DATABASE_URL` when running tests:

   ```powershell
   docker-compose.exe -f compose.yaml cp ops/provision_runtime_role.sql postgres:/tmp/provision_runtime_role.sql
   docker-compose.exe -f compose.yaml exec postgres psql -v ON_ERROR_STOP=1 -U $env:POSTGRES_USER -d $env:POSTGRES_DB -f /tmp/provision_runtime_role.sql
   ```

   See [OPERATIONS.md](OPERATIONS.md) for rotation, production grants, backup, and recovery procedures.
5. Load settings and start the HTTP server. The local bind address is loopback-only, and unsafe API requests require the configured Origin:

   ```powershell
   Get-Content .env | ForEach-Object {
     if ($_ -match '^\s*([^#=\s]+)\s*=\s*(.*?)\s*$') {
       [Environment]::SetEnvironmentVariable($matches[1], $matches[2], 'Process')
     }
   }
   go run ./cmd/api
   ```

   The server exits with an error if configuration is invalid or PostgreSQL cannot be reached.
6. In a second PowerShell window, verify both health endpoints:

   ```powershell
   Invoke-RestMethod http://127.0.0.1:8080/health/live
   Invoke-RestMethod http://127.0.0.1:8080/health/ready
   ```

   `live` reports process liveness. `ready` pings PostgreSQL and returns HTTP 503 if the database is unavailable.

## Configuration

| Variable | Required | Default / example | Purpose |
| --- | --- | --- | --- |
| `APP_ENV` | No locally; required for production behavior | `development` / `production` | Selects explicit development or fail-closed production configuration. |
| `HTTP_ADDR` | No | `127.0.0.1:8080` | Loopback-only local HTTP listen address. Non-loopback binds require HTTPS and `SESSION_COOKIE_SECURE=true`. |
| `DATABASE_URL` | Yes | Runtime role URL to `127.0.0.1:15432` locally | PostgreSQL connection string; production requires `sslmode=verify-full`; never logged or returned to clients. |
| `DATABASE_RUNTIME_ROLE` | Production | `todo_runtime` | Must match the PostgreSQL URL username; startup checks its effective administrative/ownership privileges. |
| `DATABASE_MAX_CONNS` | No | `10` (allowed 1 to 100) | Upper bound on the pgx connection pool. |
| `TASK_CURSOR_SIGNING_KEY` | Production | 64 hexadecimal characters | Stable 32-byte HMAC key used to sign active-task pagination cursors. |
| `TRUSTED_ORIGIN` | No | `http://127.0.0.1:8080` | Exact browser origin permitted for state-changing requests. |
| `SESSION_COOKIE_SECURE` | No locally; required for production | `false` locally, `true` in production | Production always requires Secure cookies and an HTTPS origin. |
| `TRUSTED_PROXY_CIDRS` | Production | Empty locally | Exact reverse-proxy peer networks allowed to provide client IP forwarding headers. |
| `POSTGRES_DB` | Compose/migrations | `todo_dev` | Local database name. |
| `POSTGRES_USER` | Compose/migrations | `todo_dev` | Local database user. |
| `POSTGRES_PASSWORD` | Compose/migrations | Set a local-only value | Local database password. |
| `TEST_DATABASE_URL` | PostgreSQL and Playwright tests | Dedicated loopback test database URL | Explicit database URL for tests. It must identify a database separate from `DATABASE_URL`; Playwright never falls back to `DATABASE_URL`. |

`.env` is ignored by Git. `.env.example` contains placeholders only; its two example URLs share a database target and therefore must not be used unchanged for Playwright. Local migrations use the Compose administrative account; the API uses `todo_runtime`, and PostgreSQL integration tests use the explicitly configured `TEST_DATABASE_URL`. For E2E tests that URL must point to a separately provisioned test database. Production requires PostgreSQL TLS verification, explicit proxy CIDRs, secure cookies, and startup verification of runtime-role privileges. Real certificates, proxy, database service, and secret manager must still be provisioned by the deployment operator.

## Local authentication API

- `POST /api/auth/register` with `{"email":"you@example.test","password":"at-least-12-characters"}` creates a local account immediately; it returns a generic 202 response for new and existing addresses to avoid account enumeration. Email delivery and verification are deferred.
- `POST /api/auth/login` with the same fields sets an HttpOnly, SameSite=Lax session cookie and returns only the public user fields.
- `GET /api/auth/me` returns the user associated with the current valid session.
- `POST /api/auth/logout` revokes the current server-side session and clears the cookie.

The frontend checks `/api/auth/me` on page load and uses the browser's same-origin HttpOnly cookie. Login and registration submit directly to the Go API; registration does not require email verification. Passwords must be 12-128 characters. Session tokens are random and only their SHA-256 hashes are stored in PostgreSQL. Sessions expire after 7 days idle or 30 days absolute; bounded maintenance removes expired sessions and rate-limit buckets. PostgreSQL stores shared, atomic IP and account rate-limit counters; if PostgreSQL is unavailable, login and registration fail closed. Local development uses `SESSION_COOKIE_SECURE=false` only while bound to loopback. Production uses `__Host-` Secure cookies. Passwords use Argon2id.

`GET /api/preferences` and `PUT /api/preferences` expose the account's stored IANA time zone preference. The current task-entry UI uses the device's detected time zone for calendar display and sends it with date-sensitive list requests; it does not currently show a manual time-zone selector or synchronize that detected value through the preferences endpoint.

## Task API

All task endpoints require the authenticated session cookie. The server always derives ownership from that session; request bodies and query parameters cannot select another owner. Task IDs are UUIDs. Titles are trimmed and must contain 1-200 Unicode characters. Descriptions are optional and limited to 10,000 Unicode characters. Status is `TODO`, `IN_PROGRESS`, or `DONE` and defaults to `TODO` on creation.

| Method and path | Request | Success |
| --- | --- | --- |
| `POST /api/tasks` | `{"title":"Prepare report","description":"Optional","status":"TODO","priority":"HIGH","due_date":"2026-11-09"}` (`description`, `status`, `priority`, and `due_date` optional) | `201` with task JSON and `Location` header |
| `GET /api/tasks?q=report&status=TODO&priority=HIGH&due=overdue&tz=Asia%2FAmman&limit=20&cursor=…` | Optional search, status, priority, due-date filter (`overdue`, `today`, `upcoming`, or `none`), IANA `tz`, page size 1-100, and opaque `next_cursor` from the previous response | `200` with `{ "items": [...], "next_cursor": "…" }`; cursor is `null` on the last page |
| `GET /api/tasks/deleted/count` | None | `200` with the authenticated owner's recoverable deleted-task count |
| `GET /api/tasks/deleted?limit=20&cursor=…` | Optional page size 1-100 and opaque `next_cursor` from the previous response | `200` with the authenticated owner's recoverable soft-deleted tasks and the same page response shape |
| `GET /api/tasks/completed?period=week&tz=Asia%2FAmman&limit=20&cursor=…` | Optional `period` (`all`, `week`, `month`, or `year`; default `all`), IANA `tz`, page size 1-100, and opaque `next_cursor` | `200` with archived completed tasks, `next_cursor`, and `total_count` for the selected period |
| `POST /api/tasks/bulk` | Status: `{"action":"status","status":"DONE","tasks":[{"id":"…","version":1}]}`; deletion: `{"action":"delete","tasks":[{"id":"…","version":1}]}` | `200` with `{ "items": [...] }` in request order |
| `GET /api/tasks/{taskID}` | None | `200` with task JSON |
| `PUT /api/tasks/{taskID}` | `title`, `description`, `status`, and `version` are required; `priority` is optional and omission preserves its current value; `due_date` is optional and omission or `null` clears it | `200` with updated task JSON |
| `DELETE /api/tasks/{taskID}` | `{"version":1}` | `200` with soft-deleted task JSON |
| `POST /api/tasks/{taskID}/restore` | `{"version":2}` | `200` with restored task JSON when within 24 hours of deletion |

Task responses include `id`, `title`, `description`, `status`, `priority`, `due_date`, `is_overdue`, `completed_at`, `version`, `created_at`, `updated_at`, and `deleted_at`. Priority is `LOW`, `MEDIUM`, `HIGH`, or `URGENT`; omitted priority defaults to `MEDIUM`. Active tasks are ordered by workflow rank (`IN_PROGRESS`, then `TODO`, then recent `DONE` tasks still inside their review window), then by `created_at DESC, id DESC`; deleted tasks and older archived completions are excluded. `q` searches title and description for a case-insensitive literal substring; `%`, `_`, and `\\` are not wildcard operators. Search, status, priority, and due-date filters combine with AND semantics. Whitespace-only search means no search filter. Due-date filters are evaluated in the requested IANA time zone. Invalid, repeated, or oversized list query parameters and malformed, tampered, or filter-mismatched cursors return `400 invalid_pagination`. Active-list cursors are HMAC-signed and bound to the authenticated owner and normalized filters. Existing unsigned active-list cursors from older versions are rejected; restart pagination from the first page. Production requires a stable 32-byte `TASK_CURSOR_SIGNING_KEY` configured as 64 hexadecimal characters; rotation invalidates outstanding cursors. Local development generates a process-local key if omitted, so a server restart also invalidates cursors.

Due dates are calendar dates, not timestamps. The frontend detects the device's IANA time zone and uses it to display due-today and overdue states; date-sensitive task and completed-list queries send that zone. The API's `is_overdue` value for a task-list request uses the requested zone; a direct single-task read uses the stored account zone. The visible browser badge follows the device zone. An active task due today is highlighted as due today, and an incomplete task whose due date is before today in that zone is highlighted as overdue. These states are derived from the date and current time on each view; no background job is needed.

When a task is marked `DONE`, it remains in the active workspace for a 12-hour review window so Undo and follow-up edits remain available. After that window it appears in the read-only completed archive at `GET /api/tasks/completed`. The transition is calculated from the database clock when task lists/counts are queried; no worker or scheduled process is required. A task changed back to an active status returns to the active workspace. Archive period filters use local calendar boundaries in the requested IANA zone: weeks begin Monday, and date intervals use an inclusive start and exclusive end. Archive pages pin their cutoff and period boundaries in the cursor so counts and pagination remain consistent while browsing. Archived tasks cannot be edited, deleted, bulk-mutated, or undone through the API; their activity history remains readable.

Keyset pagination is not a database snapshot across requests: concurrent task changes can move a task into or out of a later page. Changing search, status, or priority starts again from the first page.

Priority is independent of workflow status. Create and edit forms support all four values. The workspace filter combines with search and status; clearing or changing filters resets pagination and selection. Bulk status/deletion operations preserve task priority. A changed priority creates a `TASK_PRIORITY_CHANGED` activity event and can be undone under the same version and same-field conflict rules as other task fields.

The deleted-task list contains only the authenticated owner's tasks whose `deleted_at` is within the inclusive 24-hour recovery window, ordered by `(deleted_at, id)` descending. Its opaque cursor pins the cutoff for a multi-page listing and is bound to the authenticated owner; malformed or cross-owner cursors return `400 invalid_pagination`. The cutoff is only a pagination snapshot: restore checks the current database time, so an item can expire after it was listed. Updates and deletes must include the version last read; a successful mutation increments it, while a stale version returns `409 version_conflict`. A task deleted for more than 24 hours returns `409 recovery_expired` on restore. Missing and other users' task IDs both return `404 task_not_found`. Invalid task data returns `422`; invalid IDs and pagination parameters return `400`. State-changing requests must use the configured trusted Origin. The frontend reloads the deleted-task list from PostgreSQL-backed API state after sign-in, page refresh, deletion, bulk deletion, restore, and Undo. Task activity itself is persisted in PostgreSQL.

Bulk operations are all-or-nothing and accept 1-100 unique task IDs. Every item must include its current positive version. IDs are locked in stable UUID order, checked against the authenticated owner and version, then changed in one PostgreSQL transaction. A missing or foreign task returns `404`; stale versions return `409 version_conflict`; empty, duplicate, oversized, malformed, or unsupported operations return `422`. Any validation, task-write, or activity-write failure rolls back every change. Bulk status updates preserve the `completed_at` rule. Each changed status adds its ordinary `TASK_STATUS_CHANGED` event; each deletion is a soft delete with its ordinary `TASK_DELETED` event. Each event remains individually compatible with existing Undo rules; no bulk-Undo behavior is added. The UI selects individual tasks or all tasks currently rendered across loaded pages; changing search or status clears the selection, and clearing or switching accounts reconciles it.

## Task Activity History

`GET /api/tasks/{taskID}/activity?limit=20&cursor=…` returns the authenticated owner's history for an existing task, including a soft-deleted task. It uses the same 1-100 page-size bound and opaque keyset cursor style as task listing. The response is `{ "items": [...], "next_cursor": "…" }`, with a `null` cursor on the final page. Events are ordered newest first by `(created_at, id)`. Missing and other users' tasks both return `404`; invalid IDs or pagination return `400`; an expired session returns `401`.

Each event includes `id`, `task_id`, `actor_user_id`, `event_type`, `previous_value`, `new_value`, and `created_at`. `TASK_CREATED`, `TASK_TITLE_UPDATED`, `TASK_DESCRIPTION_UPDATED`, `TASK_STATUS_CHANGED`, `TASK_PRIORITY_CHANGED`, `TASK_DELETED`, and `TASK_RESTORED` represent individual changes. When title and description change together, one `TASK_UPDATED` event records both previous and new fields; a simultaneous status change also gets its own `TASK_STATUS_CHANGED` event. Values are JSON objects keyed by the changed field. Events are append-only through the application API, and task mutations plus activity inserts share one PostgreSQL transaction. A physical task deletion cascades its history; ordinary task deletion is soft and preserves it. Activity rows are constrained to the owning task/user, and the actor is the authenticated owner. The frontend displays this timeline in an accessible dialog and does not persist history in browser storage.

## Task Undo

`POST /api/tasks/{taskID}/activity/{eventID}/undo` compensates for an eligible task title update, description update, combined details update, status change, priority change, or soft deletion. The JSON request body is `{"version":3}` using the current task version from the activity response's `undo_version`. The response is `{ "task": {...}, "activity": {...} }`. The server derives the actor from the authenticated session; client-supplied fields such as `actor_user_id` are rejected.

Undo changes only the fields affected by the original event. Before changing anything, the API verifies task and event ownership, the expected task version, that no later event changed the same field, and that the current values still match the original event's new values. A title Undo therefore preserves newer description, status, or priority changes. The task update, version increment, linked `TASK_UNDONE` history event, and one-time-use guarantee commit in one transaction. A later attempt returns `409 undo_already_applied`; stale or unsafe attempts return `409 version_conflict`; a deletion older than 24 hours returns `409 undo_expired`; unsupported events return `422 undo_unsupported`; foreign or missing task/event pairs return `404`.

Activity responses include `undo_available`, `undo_unavailable_reason`, and `undo_version`. The UI offers Undo only when the server reports it is currently eligible and explains why other events cannot be undone. Undo is not offered for task creation, restoration, or a `TASK_UNDONE` event, which prevents recursive Undo. The deletion history remains reachable from the Recently deleted list, including after a page refresh.

## Task Templates

Templates are private to their owner. The authenticated `/api/task-templates` endpoints support create, list, view, edit, delete, and apply. Each template contains 1–50 ordered items; names are limited to 100 characters and item titles/descriptions use the existing task limits. Item titles must be unique within a template after trimming and case-insensitive comparison. Each item stores a priority, defaulting to `MEDIUM`, and applying a template copies each item's priority to the generated task. **Add new items** creates the first task set, then adds only template items that were added later; reusing it does not duplicate prior items. Editing an existing template item does not rewrite tasks already created from it. **Create new set** intentionally creates a complete independent copy. Both operations confirm the action and create TODO tasks with ordinary `TASK_CREATED` activity events in one transaction. Template version checks reject stale edits, deletes, and applies. Undo intentionally continues to reject `TASK_CREATED`; no bulk-undo behavior is added.

## Known limitations and deferred capabilities

- The repository contains local Docker configuration and production-oriented examples, but no public deployment, real TLS endpoint, external production database, reverse proxy, secret manager, off-host backup service, or production recovery process has been configured or verified.
- Email verification and password reset are deferred; registration is local and does not send email.
- Task pagination is keyset-based but is not a database snapshot across separate requests; concurrent task changes can affect later pages. Completed archive cursors pin their cutoff and period boundaries.
- Undo intentionally does not reverse task creation, task restoration, or another Undo event. Deleted tasks can only be restored during the 24-hour recovery window.
- The visible due-date state follows the browser device time zone. The account preference endpoints remain available for stored IANA time zones, but the current interface does not expose a manual time-zone control.
- No CI/CD pipeline or production-scale load result is documented as configured in this repository.

## Verification

Run `node --test playwright-config-safety.test.js` to check Playwright configuration validation without starting the application or connecting to PostgreSQL.

```powershell
gofmt -l (Get-ChildItem -Recurse -Filter *.go | ForEach-Object FullName)
go test ./...
go build ./...
go vet ./...
node --check script.js
```

`gofmt -l` is a read-only formatting check; no output means all listed Go files are formatted. Run `npm run test:html` for HTML validation. The Playwright project has focused scripts for Phases 8, 9, 10, 13, 14, 15, 16, and 17–18, plus `npm run test:e2e` for the complete configured browser suite.

Database integration tests require PostgreSQL and applied migrations. Set `TEST_DATABASE_URL` to a dedicated test database in the current PowerShell process before running `go test ./...`; do not use a database containing development or production data. The integration tests accept loopback PostgreSQL only and clean up their uniquely named test accounts. Activity, Undo, and bulk-operation integration tests temporarily install uniquely named test-only PostgreSQL triggers to force transaction failures and remove those triggers and functions during cleanup. After applying migrations, run `tern migrate --migrations migrations` a second time and confirm it reports no pending migrations. Stop PostgreSQL with `docker-compose.exe -f compose.yaml down`; this preserves the named data volume. Do not add `-v` unless intentionally deleting local database data.

### Phase 8 browser QA

Browser tests use Playwright, Axe, and `pg` as Node development dependencies only; none are Go runtime dependencies. Node.js 20.12 or newer and a separately provisioned loopback PostgreSQL test database with all ten migrations applied are required. Set `TEST_DATABASE_URL` explicitly in the environment or in the ignored `.env` before running Playwright. `DATABASE_URL` is never used as a fallback, and configuration rejects a test URL that identifies the same database as `DATABASE_URL`, even when the credentials differ. The Go API process started by Playwright receives the validated test URL as both `DATABASE_URL` and `TEST_DATABASE_URL`, so it cannot silently connect through the development runtime URL. The configuration does not connect to, create, reset, drop, or migrate a database; provision and migrate the test database yourself. A loopback host is not proof that a database is disposable: never point E2E tests at development data or production data.

For example, set the URL to your own dedicated test database without putting it in a command-line argument or committing it. In PowerShell, set `$env:TEST_DATABASE_URL` in the current process; in a POSIX shell, export `TEST_DATABASE_URL`. Use a loopback host (`127.0.0.1`, `localhost`, or `::1`) and a database name different from the one in `DATABASE_URL`. Do not use the `.env.example` test URL unchanged; it is a setup placeholder, not a dedicated test database provisioned for E2E use. Go is resolved as `go` through `PATH` by default; set `GO_EXE` only when you need a specific executable. By default, Playwright uses the installed stable Chrome channel. Set `PLAYWRIGHT_CHROMIUM_EXECUTABLE` to an approved local browser binary or `PLAYWRIGHT_BROWSER_CHANNEL` to another installed Chromium channel if needed.


From PowerShell, install the locked test dependencies and run the suite:

```powershell
npm ci
npm run test:html
npm run test:phase8
```

`npm run test:phase8:headed` runs the same checks with a visible browser. On a machine without an installed Chrome channel, install the Playwright-managed browser with `npx playwright install chromium`, subject to the machine's normal software and execution policies. The suite starts `go run ./cmd/api`, registers per-run random test accounts, verifies Undo workflows, stale tabs, concurrent HTTP Undo, account isolation, session revocation, error/retry and loading states, responsive layouts, keyboard behavior, and critical/serious Axe findings. It deletes only the accounts with its unique `phase8-e2e-<run-id>` prefix in teardown. Browser traces, screenshots, reports, and `node_modules` are ignored by Git. No real user accounts or credentials are used.

### Phase 9 bulk-operation QA

Run the Phase 9 bulk suite separately with `npm run test:phase9`. The configured Playwright project also has focused scripts for Phases 13–18; `npm run test:e2e` runs all specs included by `playwright.config.js`. The Phase 9 tests use random account prefixes, assert cleanup, exercise all-or-nothing stale and mixed-owner requests, failure rollback, status/delete history and Undo compatibility, account switching, duplicate submission, pagination selection, mobile/tablet/desktop layout, keyboard confirmation, and Axe checks. No database migration is needed for bulk operations because they reuse existing task versioning and activity event types.

### Phase 13–18 browser QA

Use `npm run test:phase13` for Recently Deleted, `npm run test:phase14` for search and filters, `npm run test:phase16` for due dates and device-timezone behavior, and `npm run test:phase17-18` for the completed-task review window, archive, period navigation, and workspace ordering. Phases 15 and 10 have their own focused commands described below. The complete configured suite is `npm run test:e2e`. These are test commands only; they start the API and use isolated test accounts as defined by their specs. A passing focused run is not evidence that every browser spec passed.

### Phase 15 priority QA

Run `npm run test:phase15` for the Chrome/Playwright priority flow. It verifies all four values and the MEDIUM default, combined filters, editing, actor-labelled activity and Undo, template item persistence/application, and Axe checks at desktop, tablet, and mobile widths. PostgreSQL integration tests also check invalid values, filtering and cursor binding, legacy updates, event/Undo safety, ownership, concurrency, and rollback. Migration 008 is additive; do not roll it back after users have stored non-MEDIUM priorities or priority history.

### Phase 10 task-template QA

Run the Chrome template suite with `npm run test:phase10`. It covers create/edit/delete, repeated application, preservation of generated tasks and history, owner isolation, duplicate-submission prevention, recoverable errors, responsive widths, keyboard interaction, and Axe checks. PostgreSQL integration tests use uniquely named accounts and temporary scoped triggers, then clean up those test records.
