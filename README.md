# To-Do List application

This repository contains an HTML, CSS, and Vanilla JavaScript frontend with local login and registration, plus a Go HTTP API backed by PostgreSQL. The Go server serves the allowlisted frontend files and API from one origin, without exposing other repository files. Authenticated task management is connected to the backend API, with PostgreSQL as the source of truth. See [operations guide](docs/OPERATIONS.md) for database roles, production configuration, reverse proxy assumptions, backups, and recovery.

## Project map

- [Project structure](docs/PROJECT_STRUCTURE.md) — directories, key files, and how the layers connect.
- [Discussion guide](docs/DISCUSSION_GUIDE.md) — concise explanations of the main design choices and feature flows.
- [API map](docs/API_MAP.md) — HTTP routes, authentication requirements, and owning handlers.
- [Operations guide](docs/OPERATIONS.md) — migrations, roles, backups, and deployment safety.

```text
frontend/            HTML page/section partials, scripts, services, utilities, and CSS
cmd/ + internal/     Go entry points, routes, handlers, domain services, repositories, and database
migrations/ + ops/   Ordered Tern history and guarded database operations
tests/               Frontend unit checks, Playwright E2E specs, and E2E safety helpers
docs/                Current project guides and historical handoff/checklist
Dockerfile, compose*.yaml, tern*.conf   Root-level build/runtime configuration
```

The Go server assembles allowlisted HTML partials and serves the frontend from the same origin. Go code stays in the standard `cmd/` and `internal/` layout; migrations, Tern configs, operations scripts, and Docker files stay at the root because their guarded workflows and build contexts use those locations.

## Production

The live application is available at [https://cls-todo-list.onrender.com](https://cls-todo-list.onrender.com). It runs as a Docker web service on Render and uses Neon PostgreSQL. Production authentication uses same-origin, `HttpOnly`, `Secure`, `__Host-` cookies backed by server-side sessions; only session-token hashes are stored, with idle and absolute expiry. See [operations guide](docs/OPERATIONS.md) for the Render proxy assumptions and production configuration.

Production smoke evidence (2026-11-12): a read-only HTTPS request to `/health/ready` returned HTTP 200 with certificate verification successful. The owner reported a successful manual browser check covering registration, login and logout, task persistence, user isolation, individual and template-based task creation, USER access to Admin returning 403, and the Admin dashboard. The manual browser results are owner-reported, not an automated test result. The readiness response does not identify the deployed source commit.

## Requirements

- Go 1.26 or newer.
- The `go` and `gofmt` executables available through `PATH`. To select a non-default Go executable for Playwright's API server, set `GO_EXE` to its path; paths containing spaces are supported.
- Docker Desktop with the standalone `docker-compose.exe` command.
- Tern v2.4.3 for SQL migration commands.
- Node.js 20.12 or newer for browser and HTML checks.

## Local setup (Windows PowerShell)

1. Copy `.env.example` to `.env` and replace the local-only development placeholders. `DATABASE_URL` is reserved for the development runtime role and `todo_dev`. The test variables in that example are placeholders only: create a separate ignored `.env.test` as described below, and load it before running any PostgreSQL or Playwright integration tests. Those tests use the isolated `compose.test.yaml` project, its own volume, port `25432`, and exact database name `todo_test`; never point test URLs to the development service.
2. Start PostgreSQL from the project directory:

   ```powershell
   docker-compose.exe -f compose.yaml up -d
   docker-compose.exe -f compose.yaml ps
   ```

   Wait until the `postgres` service reports `healthy`. Its port is bound to `127.0.0.1`; data lives in a named Docker volume, outside OneDrive. The development service and volume are separate from the test environment described below.
3. Load the non-secret local settings into the current PowerShell process, install the pinned migration CLI, and inspect/apply migrations:

   ```powershell
   Get-Content .env | ForEach-Object {
     if ($_ -match '^\s*([^#=\s]+)\s*=\s*(.*?)\s*$') {
       [Environment]::SetEnvironmentVariable($matches[1], $matches[2], 'Process')
     }
   }
   go install github.com/jackc/tern/v2@v2.4.3
   tern version
    .\ops\Invoke-DevelopmentMigrations.ps1 -Action status -ExpectedTargetVersion 15
   ```

The development wrapper loads `.env` without displaying values and verifies that `DATABASE_URL`, `tern.conf`, the running PostgreSQL container, published loopback port, named volume, database identity, PostgreSQL version, and migration inventory all match the guarded development target. Its `status` action is read-only. The wrapper advances exactly one expected migration per invocation; `migrate` also requires an explicit authorization switch, expected current and target versions, and zero other database sessions. Before migrating, follow the verified backup and restore procedure in [operations guide](docs/OPERATIONS.md). Historical evidence records that on 2026-11-11 `todo_dev` advanced from 13/14 to 14/14 after creating and restoring a fresh custom-format backup and matching all pre-existing application-table row fingerprints and schema definitions. Current read-only Tern status is 14/15:

   ```powershell
    .\ops\Invoke-DevelopmentMigrations.ps1 -Action status -ExpectedCurrentVersion 14 -ExpectedTargetVersion 15
   ```

If status reports another version, stop and review the pending migrations; do not change expected versions merely to make a command pass. The repository contains migrations 001–015 in numeric order: 001 creates the `app` schema; 002 creates authentication and task data; 003 adds activity history; 004 adds Safe Undo; 005 creates templates; 006 adds shared authentication rate limits; 007 adds the recently-deleted listing index; 008 adds task and template-item priorities and priority activity events; 009 adds due dates and account time zones; 010 adds stable template-item identities and tracks template application sets; 011 adds user names and roles; 012 enforces unique item positions within each template; 013 allows a null account time zone to mean “follow this device” while preserving saved IANA overrides; 014 adds Admin audit history and the database-level final-Admin guard; and 015 permits a system-attributed initial-Admin bootstrap audit event. Migration files in the repository do not establish which versions are applied to a database. The latest read-only status during this task reported `todo_dev` at 14/15 and `todo_test` at 15/15; no migration was applied to `todo_dev`. For test migrations, use only the guarded `ops/Invoke-TestMigrations.ps1` with `tern.test.conf`. Applying or deleting templates does not leave generated tasks linked to them.

Down sections are migration-specific, not a general data reset. Migrations 002, 003, and 005 have no down section. Migration 001 drops the `app` schema and therefore succeeds only when the schema is empty. Migration 004 refuses rollback while Undo events exist; 008 and 009 refuse rollback while their non-default data or history exists; and 010 refuses rollback while template-application history exists. Migrations 006, 007, 012, and 013 remove their rate-limit table and state, recently-deleted index, template-position constraint, and device-time-zone support, respectively; 013 refuses rollback while any user has a null time zone. Migration 015 restores the prior audit constraint only if no initial-bootstrap event exists. Migrations 011 and 014 are intentionally forward-only and their down sections always raise an exception. Review each down section and its data conditions before requesting a rollback.
4. Provision the runtime account using the development administrator session. If the role has no password, the psql script prompts once for an initial password without echoing it; rerunning the script does not rotate an existing password. Choose a strong password, then put it only in the `DATABASE_URL` user info in `.env`. Keep test administrator settings in `.env.test` and `TEST_DATABASE_URL`; never point either runtime or test URL at the same database:

   ```powershell
   docker-compose.exe -f compose.yaml cp ops/provision_runtime_role.sql postgres:/tmp/provision_runtime_role.sql
   docker-compose.exe -f compose.yaml exec postgres psql -v ON_ERROR_STOP=1 -U $env:POSTGRES_USER -d $env:POSTGRES_DB -f /tmp/provision_runtime_role.sql
   ```

   See [operations guide](docs/OPERATIONS.md) for rotation, production grants, backup, and recovery procedures.
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
| `TRUSTED_PROXY_CIDRS` | Production CIDR mode | Empty locally | Explicit reverse-proxy peer networks. Required in production unless verified Render mode is selected. |
| `TRUSTED_PROXY_PROVIDER` | Production | Empty | Optional explicit provider mode. `render` is accepted only with verified Render web-service runtime metadata and cannot be combined with `TRUSTED_PROXY_CIDRS`. |
| `POSTGRES_DB` | Compose/migrations | `todo_dev` | Local database name. |
| `POSTGRES_USER` | Compose/migrations | `todo_dev` | Local database user. |
| `POSTGRES_PASSWORD` | Compose/migrations | Set a local-only value | Local database password. |
| `TEST_POSTGRES_DB` | Test Compose | `todo_test` | Database initialized only in the isolated test PostgreSQL instance. |
| `TEST_POSTGRES_USER` | Test Compose | `todo_test_admin` | Test-instance administrator; its privileges do not extend to development PostgreSQL. |
| `TEST_POSTGRES_PASSWORD` | Test Compose | Set a unique local-only value | Test-instance administrator password. |
| `TEST_DATABASE_URL` | PostgreSQL and Playwright tests | `127.0.0.1:25432/todo_test` | Explicit test-instance administrator URL. It must differ from `DATABASE_URL`; tests never fall back to `DATABASE_URL`. |
| `RUNTIME_DATABASE_URL` | Runtime-role integration tests | `127.0.0.1:25432/todo_test` | URL for the separately provisioned restricted `todo_test_runtime` role on the same isolated test server. |
| `REQUIRE_POSTGRES_INTEGRATION` | No | unset | Set to `1` or `true` to fail Go integration tests when `TEST_DATABASE_URL` is missing rather than skip them. |

`.env` and `.env.test` are ignored by Git. `.env.example` contains placeholders only, with a distinct exact `todo_test` target that must be provisioned before use. Local migrations use the development Compose administrative account; the API uses `todo_runtime`, and all PostgreSQL integration tests validate the explicit `TEST_DATABASE_URL` through the shared Go guard. Tests never fall back to `DATABASE_URL`; set `REQUIRE_POSTGRES_INTEGRATION=1` to make a missing test URL fail instead of skipping integration coverage. The validator requires loopback port `25432`, exact database `todo_test`, a different normalized target from `DATABASE_URL`, and rejects all connection query options except one valid `sslmode`. Playwright uses the same target validation. Production requires PostgreSQL TLS verification, explicit proxy CIDRs, secure cookies, and startup verification of runtime-role privileges. Real certificates, proxy, database service, and secret manager must still be provisioned by the deployment operator.

## Local authentication API

- `POST /api/auth/register` with `{"name":"Your name","email":"you@example.test","password":"at-least-12-characters"}` creates a local account immediately; `name` is trimmed and must contain 1–100 Unicode characters after trimming. The server assigns the `USER` role; registration does not accept a role. The endpoint returns a generic 202 response for new and existing addresses to avoid account enumeration. Email delivery and verification are deferred.
- `POST /api/auth/login` with the same fields sets an HttpOnly, SameSite=Lax session cookie and returns only the public user fields.
- `GET /api/auth/me` returns the user associated with the current valid session.
- `POST /api/auth/logout` revokes the current server-side session and clears the cookie.

The frontend checks `/api/auth/me` on page load and uses the browser's same-origin HttpOnly cookie. Login and registration submit directly to the Go API; registration does not require email verification. Passwords must be 12-128 characters. Session tokens are random and only their SHA-256 hashes are stored in PostgreSQL. Sessions expire after 7 days idle or 30 days absolute; bounded maintenance removes expired sessions and rate-limit buckets. PostgreSQL stores shared, atomic IP and account rate-limit counters; if PostgreSQL is unavailable, login and registration fail closed. Local development uses `SESSION_COOKIE_SECURE=false` only while bound to loopback. Production uses `__Host-` Secure cookies. Passwords use Argon2id.

`GET /api/preferences` and `PUT /api/preferences` expose the account's nullable IANA time zone preference. A saved valid IANA zone is a manual account-wide override; setting it to `null` returns the account to the current browser/device zone. The signed-in UI exposes this preference, validates IANA names, loads it from the server, and sends the active zone with date-sensitive list requests. An unset preference is not silently converted into a permanent account value.

## Administrator API and account management

`GET /api/admin/users` and `GET /api/admin/tasks` require a valid session for an account whose server-loaded role is exactly `ADMIN`. Missing or invalid sessions return `401`; authenticated non-admin or unknown-role accounts receive `403`. The client cannot grant itself access by sending a role in a request. Both endpoints use bounded keyset pagination with `limit` (default 20, maximum 100) and an opaque signed `cursor` returned as `next_cursor`; cursors are bound to the endpoint and task filters. Invalid, repeated, unknown, or filter-mismatched query parameters return `400 invalid_pagination`.

The user endpoint returns only `id`, `name`, `email`, `role`, `status`, `created_at`, and server-computed management capabilities. It never returns password hashes or session identifiers. The task endpoint returns task fields plus `owner_id` and `owner_name`; it does not return owner email or authentication data. Its default view includes all non-deleted tasks across owners, including every workflow status. `status` may be `TODO`, `IN_PROGRESS`, or `DONE`; `state` may be `active` (the default), `deleted`, or `all`.

Admin mutations use `PATCH /api/admin/users/{userID}/role` with `{"role":"USER|ADMIN"}`, `PATCH /api/admin/users/{userID}/status` with `{"status":"ACTIVE|DISABLED"}`, and `POST /api/admin/users/{userID}/sessions/revoke`. They require the same authenticated server-side Admin role and trusted same-origin checks as other state-changing requests. Role or status changes revoke all current sessions for the target in the same transaction; disabled accounts cannot log in or use existing sessions. Self-management is rejected, and a database trigger prevents disabling or demoting the final active Admin even under concurrent requests. Successful mutations append an audit event containing actor, target, action, and role/status or session-count before/after state; credentials, tokens, and personal fields are excluded. No endpoint hard-deletes users. The UI renders only server-reported capabilities; API authorization remains authoritative.

Migration `014_admin_account_audit.sql` creates the audit and final-Admin guard objects and is forward-only because rollback would erase audit history and the database invariant. Migration `015_allow_initial_admin_bootstrap_audit.sql` allows only the dedicated null-actor initial-bootstrap action; its rollback refuses once such an audit event exists. After applying migration 014 to a database, rerun the runtime-role grant workflow so the API can update only the `users` columns `time_zone`, `role`, `status`, and `updated_at`, insert audit events, and retain no direct access to the guard table. Migration-file presence does not establish that a database has applied it.

### One-time initial Admin bootstrap

`cmd/adminctl` is an operational CLI, not an HTTP endpoint. Its normal mode targets only the fixed local development endpoint `127.0.0.1:15432/todo_dev`; the isolated `todo_test` endpoint is accepted only in dedicated automated-test mode. A separate `--production` mode is pinned to the direct Frankfurt Neon `neondb` owner endpoint and requires verified TLS, PostgreSQL 18.6, Tern 15, an explicit database name, and an additional production-bootstrap authorization flag for writes. Production mode rejects inherited development/test URLs and PostgreSQL overrides, verifies the connected database owner, and never prints connection settings. See the production procedure in [operations guide](docs/OPERATIONS.md).

After the actual owner account has been registered normally and is active, first verify through the guarded development migration status that `todo_dev` is at 15/15; migration 015 is currently pending there and must be applied only through the separately authorized guarded development migration procedure after its backup preconditions. Then load the development administrator settings into the process using the strict environment reader. Enter the exact normalized owner email and run the read-only status command. Proceed only if it reports zero active Admins, a zero guard count, and an eligible target. Bootstrap requires the matching `--confirm-database todo_dev` flag:

```powershell
Import-Module .\ops\DevelopmentMigrationGuard.psm1 -Force
$settings = Read-StrictEnvironmentFile -Path (Join-Path (Get-Location) '.env')
foreach ($name in @('POSTGRES_DB', 'POSTGRES_USER', 'POSTGRES_PASSWORD', 'DATABASE_URL')) {
  [Environment]::SetEnvironmentVariable($name, [string]$settings[$name], 'Process')
}
foreach ($name in @('PGHOST', 'PGPORT', 'PGDATABASE', 'PGUSER', 'PGPASSWORD', 'PGSSLMODE', 'PGSERVICE', 'PGSERVICEFILE', 'PGPASSFILE')) {
  Remove-Item "Env:$name" -ErrorAction SilentlyContinue
}
$ownerEmail = Read-Host 'Exact normalized owner email'
go run ./cmd/adminctl status --database todo_dev --email $ownerEmail
if ($LASTEXITCODE -ne 0) { throw 'Bootstrap preflight failed; stop.' }
go run ./cmd/adminctl bootstrap --database todo_dev --email $ownerEmail --confirm-database todo_dev
if ($LASTEXITCODE -ne 0) { throw 'Bootstrap failed; inspect the safe CLI error and stop.' }
```

The development bootstrap locks account changes and the active-Admin guard in one transaction, promotes only the selected active USER account, revokes its sessions, writes a system-attributed audit event, and commits atomically. The production bootstrap uses the same transaction and guard but preserves existing sessions for the production owner flow. Subsequent Admins are managed through the authenticated Admin interface. Current implementation has no email-verification state; any company-required activation step must be completed before the account is eligible. The approved first Admin was bootstrapped on production on 2026-11-12; no personal account identifier is recorded here. Do not promote a placeholder or an existing development account.

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

Task timestamp instants are serialized in UTC RFC3339 form. `due_date` remains a `YYYY-MM-DD` civil calendar date, not a timestamp, and is not converted between time zones.

Due dates are calendar dates, not timestamps. The frontend detects the device's IANA time zone and uses it to display due-today and overdue states; date-sensitive task and completed-list queries send the active zone. The API's `is_overdue` value for a task-list request uses the requested zone; a direct single-task read uses the stored account zone or UTC when no manual zone is saved. The visible browser badge follows the selected device/manual zone. An active task due today is highlighted as due today, and an incomplete task whose due date is before today in that zone is highlighted as overdue. These states are derived from the date and current time on each view; no background job is needed.

When a task is marked `DONE`, it remains in the active workspace for a 12-hour review window so Undo and follow-up edits remain available. After that window it appears in the read-only completed archive at `GET /api/tasks/completed`. The transition is calculated from the database clock when task lists/counts are queried; no worker or scheduled process is required. A task changed back to an active status returns to the active workspace. Archive period filters use local calendar boundaries in the requested IANA zone: weeks begin Monday, and date intervals use an inclusive start and exclusive end. Archive pages pin their cutoff and period boundaries in the cursor so counts and pagination remain consistent while browsing. Archived tasks cannot be edited, deleted, bulk-mutated, or undone through the API; their activity history remains readable.

Task-list cursors use deterministic keyset ordering with a unique ID tie-breaker and pin a timestamp boundary; rows created or updated after that boundary are excluded from later pages, while deleted rows naturally disappear. This is a timestamp-bounded traversal, not a long-lived PostgreSQL MVCC snapshot: an in-flight transaction that began before the boundary and commits afterward can still expose a row with pre-boundary timestamps. Changing search, status, or priority starts again from the first page.

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
- Task and Admin pagination use signed keyset cursors, deterministic tie-breakers, and timestamp boundaries; rows changed after the boundary are excluded and deleted rows disappear. This is not a long-lived MVCC snapshot, so a pre-boundary in-flight transaction can commit afterward with pre-boundary timestamps. Completed archive cursors also pin their cutoff and period boundaries.
- Undo intentionally does not reverse task creation, task restoration, or another Undo event. Deleted tasks can only be restored during the 24-hour recovery window.
- Due dates remain civil calendar dates. The visible due-date state follows the browser/device IANA time zone unless the user saves a manual account-wide IANA override; clearing the override returns to device time. DST and date-boundary behavior is covered by PostgreSQL and browser tests.
- No CI/CD pipeline or production-scale load result is documented as configured in this repository.

## Verification

Run `npm run test:unit` for frontend presentation and E2E-configuration helper tests without starting the application or connecting to PostgreSQL.

```powershell
gofmt -l (Get-ChildItem -Recurse -Filter *.go | ForEach-Object FullName)
go test ./...
go build ./...
go vet ./...
Get-ChildItem frontend\scripts -Recurse -Filter *.js | ForEach-Object { node --check $_.FullName }
```

`gofmt -l` is a read-only formatting check; no output means all listed Go files are formatted. Run `npm run test:html` for HTML validation. The Playwright project has focused scripts for Phases 8, 9, 10, 13, 14, 15, 16, and 17–18, plus `npm run test:e2e` for the complete configured browser suite.

### Isolated PostgreSQL integration-test database

The full PostgreSQL suite uses a separate Compose project and PostgreSQL server because runtime-role tests create temporary server-level roles. The test service uses database `todo_test`, host port `25432` bound only to `127.0.0.1`, its own `todo-list-test_postgres-test-data` volume, and a dedicated project-scoped network. This network is not marked internal, so the test container may have outbound network access; it does not mount or share the development service volume/network. The test URL guard requires that exact host class, port, and database name; it also rejects a target equal to `DATABASE_URL` and unsupported connection parameters.

Create an ignored `.env.test` containing only the following settings. Replace each password placeholder with a different strong local secret. URL-safe random passwords avoid escaping ambiguity; if a password contains special characters, percent-encode them in the URL user-info values. `.env.test` matches the `.env.*` ignore rule and is excluded from Docker build context. Do not copy credentials from `.env`.

```text
TEST_POSTGRES_DB=todo_test
TEST_POSTGRES_USER=todo_test_admin
TEST_POSTGRES_PASSWORD=replace_with_a_unique_test_admin_password
TEST_DATABASE_URL=postgres://todo_test_admin:replace_with_a_unique_test_admin_password@127.0.0.1:25432/todo_test?sslmode=disable
RUNTIME_DATABASE_URL=postgres://todo_test_runtime:replace_with_a_unique_runtime_password@127.0.0.1:25432/todo_test?sslmode=disable
```

Load these values into the current PowerShell process without echoing them, check the required settings and exact database name, validate `TEST_DATABASE_URL` with the shared guard, verify `RUNTIME_DATABASE_URL` uses the same isolated endpoint, validate Compose configuration, and only then start the test service:

```powershell
Get-Content .env.test | ForEach-Object {
  if ($_ -match '^\s*([^#=\s]+)\s*=\s*(.*?)\s*$') {
    [Environment]::SetEnvironmentVariable($matches[1], $matches[2], 'Process')
  }
}
foreach ($name in @('TEST_POSTGRES_DB', 'TEST_POSTGRES_USER', 'TEST_POSTGRES_PASSWORD', 'TEST_DATABASE_URL', 'RUNTIME_DATABASE_URL')) {
  if ([string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($name, 'Process'))) { throw "Required test setting is missing: $name" }
}
if ($env:TEST_POSTGRES_DB -cne 'todo_test') { throw 'TEST_POSTGRES_DB must be todo_test.' }
node --input-type=module -e "import { validateTestDatabaseConfiguration } from './tests/e2e/test-database.js'; validateTestDatabaseConfiguration(); console.log('Test database URL configuration is valid.')"
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml config --quiet
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml up -d postgres-test
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml ps
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml exec -T postgres-test pg_isready -U $env:TEST_POSTGRES_USER -d todo_test
docker volume inspect todo-list-test_postgres-test-data
```

Apply test migrations before provisioning the runtime role, because the privilege scripts grant access to schema objects created by those migrations. The helper validates `TEST_DATABASE_URL`, derives Tern's process-only `PG*` settings from it, and uses `tern.test.conf` (never the development `tern.conf`):

```powershell
.\ops\Invoke-TestMigrations.ps1 -Action status
.\ops\Invoke-TestMigrations.ps1 -Action migrate
.\ops\Invoke-TestMigrations.ps1 -Action status
```

The test container's `todo_test_admin` is a PostgreSQL administrator only inside this isolated instance. The existing runtime-role test checks the admin remains a superuser while exercising role creation and privilege checks. After migrations succeed, provision a distinct restricted application role using the existing scripts' supported `runtime_role` variable; the provision script prompts for its password without echoing it. Use that same separate secret in `RUNTIME_DATABASE_URL`:

```powershell
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml cp ops/provision_runtime_role.sql postgres-test:/tmp/provision_runtime_role.sql
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml exec -it postgres-test psql -v ON_ERROR_STOP=1 -v runtime_role=todo_test_runtime -U $env:TEST_POSTGRES_USER -d todo_test -f /tmp/provision_runtime_role.sql
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml cp ops/grant_runtime_privileges.sql postgres-test:/tmp/grant_runtime_privileges.sql
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml exec -T postgres-test psql -v ON_ERROR_STOP=1 -v runtime_role=todo_test_runtime -U $env:TEST_POSTGRES_USER -d todo_test -f /tmp/grant_runtime_privileges.sql
```

After migrations and role provisioning succeed, run PostgreSQL integration tests. The loaded process must contain both `TEST_DATABASE_URL` and `RUNTIME_DATABASE_URL`; `REQUIRE_POSTGRES_INTEGRATION` makes missing test configuration fail instead of silently skipping:

```powershell
$env:REQUIRE_POSTGRES_INTEGRATION = '1'
try { go test -count=1 ./... } finally { Remove-Item Env:REQUIRE_POSTGRES_INTEGRATION -ErrorAction SilentlyContinue }
```

The Go and Playwright guards never fall back to `DATABASE_URL`; runtime-role tests use `RUNTIME_DATABASE_URL` and verify that it targets the same isolated endpoint. Set its password to the same separate secret entered at the hidden runtime-role prompt. Never use development or production data for tests. To stop only the test service while retaining its data, run:

```powershell
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml stop postgres-test
```

Do not use `down -v`; removing the named volume would delete the test database. Development commands continue to use `compose.yaml`, `tern.conf`, and the separate `todo-list_postgres-data` volume.

### Phase 8 browser QA

Browser tests use Playwright, Axe, and `pg` as Node development dependencies only; none are Go runtime dependencies. Node.js 20.12 or newer and the isolated test PostgreSQL database described above, with required migrations applied, are required. Load the dedicated `.env.test` settings into the current process before running Playwright; do not rely on test placeholders copied into `.env`. `DATABASE_URL` is never used as a fallback, and configuration rejects a test URL that identifies the same database as `DATABASE_URL`, even when credentials differ. The Go API process started by Playwright receives the validated test URL as `DATABASE_URL`, not the parent process's database variables, so it cannot silently use the development runtime URL. Playwright configuration itself does not connect to, create, reset, drop, or migrate a database. Never point E2E tests at development or production data.

The test URL must use one supported loopback spelling, port `25432`, and database `todo_test`; all connection options other than one valid `sslmode` are rejected. Do not use `.env.example` test URLs unchanged; they are placeholders, not provisioned credentials. Go is resolved as `go` through `PATH` by default; set `GO_EXE` only when you need a specific executable. By default, Playwright uses the installed stable Chrome channel. Set `PLAYWRIGHT_CHROMIUM_EXECUTABLE` to an approved local browser binary or `PLAYWRIGHT_BROWSER_CHANNEL` to another installed Chromium channel if needed.


From PowerShell, install the locked test dependencies and run the suite:

```powershell
npm ci
npm run test:frontend
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
