# Final Project Handoff

**Local engineering status: approved after Final Closure Phase 5 regression.** This handoff does not certify production readiness or a public deployment.

## Purpose and architecture

This is a multi-user task-management application. The frontend is HTML, CSS, and Vanilla JavaScript served from the same origin as a Go `net/http` API. PostgreSQL is the source of truth and the API accesses it through pgx. Local PostgreSQL runs in Docker Compose with a named persistent volume. SQL schema changes are versioned with Tern.

## Implemented capabilities

- Account registration, login, logout, server-side sessions, and account-specific data.
- Owner-scoped task creation, reading, editing, status transitions, soft deletion, and restore.
- Optimistic version checks, keyset pagination, search, and combined status/priority/due-date filters.
- Four priorities, calendar due dates, device-timezone display, due-today and overdue states.
- Append-only task activity history and safe, one-time Undo for supported changes.
- Atomic bulk status changes and deletion.
- Private task templates, incremental **Add new items**, and full-copy **Create new set** behavior.
- Persistent Recently Deleted listing with a 24-hour restore period.
- Recently completed tasks remain in the active workspace for a 12-hour review window. Afterward they appear in a read-only Completed archive with all/week/month/year filters and paginated history. Activity history remains viewable.

The completed transition is evaluated by PostgreSQL when task lists and counts are queried; it does not require a background scheduler. Active and completed cursors are keyset based. Active task pagination is not a database snapshot across separate requests, so concurrent changes can affect later pages.

## Authentication and database security

Passwords use Argon2id. Session tokens are random; only token hashes are stored in PostgreSQL. Sessions expire after seven idle days or thirty absolute days. State-changing requests check the configured trusted Origin. The API derives data ownership and activity actors from the authenticated session.

The migration/administrative account is separate from `todo_runtime`. Runtime verification confirmed that the API pool connects as `todo_runtime`. On `app.task_activity`, runtime has SELECT and INSERT but not UPDATE, DELETE, or TRUNCATE. Activity is append-only through application operations, and task changes with activity events share a transaction.

## Database state

The repository contains ten migrations (`001`–`010`). A read-only query of the current development database returned schema version **10**. The same read-only check returned 12 users, 15 sessions, 43 tasks, 209 activity rows, one template, and three template items, matching the pre-regression Phase 5 baseline. No migration was run during Final Closure Phase 6. The development PostgreSQL container was running and healthy, with the existing `todo-list_postgres-data` volume attached.

## Approved regression evidence

Final Closure Phase 5 used a separate PostgreSQL 18 test container, a `phase5_regression` database on port `15433`, and tmpfs without a Docker volume. It applied all ten migrations to that test database only. No development data or credentials were copied.

- `go test -count=1 -v ./...`: all packages passed, including 12 PostgreSQL integration test functions.
- `go test -count=10 -run '^TestTaskManagementOwnershipAndLifecyclePostgreSQL$' ./internal/httpapi`: passed 10/10.
- `go test -count=3 -run '^TestCompletedTaskLifecycleOrderingAndArchivePostgreSQL$' ./internal/httpapi`: passed 3/3.
- `npm run test:e2e -- --output <temporary-output-directory>`: Playwright passed 36/36 in installed Chrome.
- `go test -race -count=1 ./...`: passed in a temporary Linux Go 1.27.3 container with CGO enabled. The native Windows Go installation has `CGO_ENABLED=0`.
- `go build ./...`, `go vet ./...`, formatting checks, JavaScript syntax checks, `npm run test:html`, and `npm audit --audit-level=low`: passed; npm reported zero vulnerabilities.

The older full Playwright run that had one Phase 8 failure remains recorded as a historical failure. It did not recur in the later 36/36 full run. The temporary test database contained no remaining test rows or connections after teardown, and its container was removed. The existing development database and volume were preserved.

The Phase 5 Playwright output is preserved outside the repository at:

`<host-temp-directory>/<Phase-5-Playwright-output>`

OS temporary directories can be cleared by the operating system; this path is supplementary evidence. The test result and command are recorded above and in `PROJECT_COMPLETION_CHECKLIST.md`.

## Local setup and operation

Follow [README.md](README.md) for Windows PowerShell setup, local API startup, migration commands, and test scripts. Follow [OPERATIONS.md](OPERATIONS.md) for role provisioning, privilege boundaries, migrations, and protected backup/restore guidance. Keep `.env` local and ignored. Never use the administrative database URL for the API. Do not use `docker-compose down -v`; the `-v` option removes the local database volume.

At Final Closure Phase 6 inspection, the local API was running on `127.0.0.1:8080`; `/health/live` and `/health/ready` both returned HTTP 200. This is a point-in-time observation, not a service guarantee.

## Artifacts and preserved work

- The user-open `playwright-report/`, ignored `test-results/`, and `node_modules/` directories were preserved.
- The Phase 5 temporary Playwright output was preserved outside the repository.
- The prior Phase 11 database dump at `<host-temp-directory>/<Phase-11-database-dump>` was preserved because it may contain user data.
- `<host-temp-directory>/<Phase-11-restore-credentials-file>` was preserved without reading it because its contents could not be safely established without exposing credentials.
- Two exact Phase 11 grant-script copies were removed from the existing PostgreSQL container's temporary directory after confirming both temporary restore databases were absent. The database container and volume were not stopped, removed, or changed.
- `.env` was not read. No passwords, tokens, keys, or certificates were copied into this document.

## Remaining limitations and deployment prerequisites

- No production host, real HTTPS certificate, reverse proxy, external production PostgreSQL, secret manager, CI/CD pipeline, off-host backup destination, monitoring service, or tested recovery process is configured here.
- Email verification and password reset are deferred until a real domain, HTTPS deployment, and email provider exist.
- Production load, database capacity, and multi-instance operational behavior need deployment-specific measurement.
- The application has not been publicly deployed. Local regression approval is not a production security certification.

Before any real launch, provision a separate production database and least-privilege runtime role, configure HTTPS and trusted proxy/origin settings, use protected secrets, establish encrypted off-host backups and a tested restore procedure, configure monitoring and incident ownership, then rerun the deployment-specific security and regression gates.

## Repository handoff baseline

- Repository: `<repository-root>`
- Branch / HEAD: `main` / `79ff3403768fc37d450c2049c1a8405037e0566b`
- Upstream: `origin/main`; the branch was not ahead of or behind the remote when checked.
- Preserved tracked modifications: `index.html`, `script.js`, `style.css`.
- Preserved untracked project files include the Go source, migrations, operational/configuration files, Playwright suites, README, checklist, and other existing repository work.
- No staging, commit, push, reset, checkout, stash, or Git cleanup was performed.
