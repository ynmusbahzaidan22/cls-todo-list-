# Final Project Handoff — Snapshot 2026-11-11

**Scope:** documentation snapshot for the current `feature/authorization` candidate. Final Closure Phase 5 results are historical evidence for an earlier candidate; the current-candidate regression is recorded separately below. This handoff does not certify production readiness or a public deployment.

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

Historical local verification confirmed that the migration/administrative account was separate from `todo_runtime`, the API pool connected as `todo_runtime`, and that role had SELECT/INSERT but not UPDATE/DELETE/TRUNCATE on `app.task_activity`. Application activity is designed as append-only, and task changes with activity events share a transaction. These historic checks do not establish current runtime database state.

## Task 3 Admin authorization and bootstrap

The current source retains the read-only endpoints `GET /api/admin/users` and `GET /api/admin/tasks`, and supports authenticated role changes, account disable/re-enable, and session revocation. Identity and role come from server-side sessions; self-management is rejected; the database guard protects the final active Admin. The operational `cmd/adminctl` tool provides fixed-target status and one-time initial-Admin bootstrap without an HTTP bootstrap route. Focused bootstrap unit and PostgreSQL tests passed against the isolated `todo_test` database. The full current-candidate regression results are recorded in the release-gate section below.

Read-only status on 2026-11-11 confirmed zero active Admins and a zero guard count in `todo_dev`. No account was promoted. The actual first-Admin bootstrap remains pending creation and approval of the real owner account. Migration 015 is also pending on `todo_dev`; the guarded, backup-protected development migration process must apply it before bootstrap. `todo_test` has migration 015 applied and is at 15/15.

## Database state

The repository contains migrations `001`-`015`. On 2026-11-11, read-only Tern status reported `todo_dev` at 14/15 and `todo_test` at 15/15. Migration 015 was applied only to the isolated test database for bootstrap verification; it was not applied to `todo_dev`. Earlier backup/restore and migration evidence remains historical and does not substitute for the current guarded status. Earlier references to migration 10/10 or 14/14 are historical and do not describe the current `todo_dev` state.

## Current-candidate release-gate verification - 2026-11-11

- Full Go suite with PostgreSQL integration enabled: `go test -json -count=1 -p 1 -parallel 1 ./...`; exit 0, 380 passed, 0 failed, 0 skipped across 12 packages.
- Full Playwright suite: `npm run test:e2e -- --reporter=list --output <external-temporary-directory>`; exit 0, 38 passed, 0 failed, 0 skipped, 0 timed out.
- Node configuration and rate-limit cleanup tests: `node --test playwright-config-safety.test.js rate-limit-cleanup.test.js`; exit 0, 32 passed, 0 failed or skipped.
- Static checks passed: `gofmt` reported no unformatted Go files (65 checked), `go vet -mod=readonly ./...`, JavaScript syntax checks (15 files), `npm run test:html`, and `git diff --check`.
- `go build -mod=readonly ./...` exited 0. Go printed a non-fatal warning that it could not write one module download stat-cache file because access was denied; the build itself succeeded.
- Before and after the full Go and Playwright runs, a read-only repeatable snapshot of all 11 `app` tables, all 4 pre-existing application rows, and schema/ACL/catalog fingerprints was identical. The final snapshot found no other database sessions or advisory locks. Guarded Tern status remained 15/15 for `todo_test`.
- After Playwright, the test API port was closed; the current run's API marker was absent, and no Go or Node process remained. Two older temporary API PID marker files from earlier runs remain outside the repository; their recorded PIDs were not running, and the files were left untouched. A Playwright-related Node process from this run was stopped by its exact PID after confirming its start time; no unrelated browser processes were stopped. No migrations were run. Test fixture writes were cleaned up, and the final database snapshot matched the baseline.

## Historical regression evidence — Final Closure Phase 5

The earlier Final Closure Phase 5 candidate used a separate PostgreSQL 18 test container, a `phase5_regression` database on port `15433`, and tmpfs without a Docker volume. It applied the ten migrations that existed at that time to that test database only. These results are historical and do not verify the current authorization candidate.

- `go test -count=1 -v ./...`: all packages passed, including 12 PostgreSQL integration test functions.
- `go test -count=10 -run '^TestTaskManagementOwnershipAndLifecyclePostgreSQL$' ./internal/httpapi`: passed 10/10.
- `go test -count=3 -run '^TestCompletedTaskLifecycleOrderingAndArchivePostgreSQL$' ./internal/httpapi`: passed 3/3.
- `npm run test:e2e -- --output <temporary-output-directory>`: Playwright passed 36/36 in installed Chrome.
- `go test -race -count=1 ./...`: passed in a temporary Linux Go 1.27.3 container with CGO enabled. The native Windows Go installation has `CGO_ENABLED=0`.
- `go build ./...`, `go vet ./...`, formatting checks, JavaScript syntax checks, `npm run test:html`, and `npm audit --audit-level=low`: passed; npm reported zero vulnerabilities.

The older full Playwright run that had one Phase 8 failure remains recorded as a historical failure. It did not recur in that later 36/36 run. The temporary test database contained no remaining test rows or connections after teardown, and its container was removed. The existing development database and volume were preserved at that time. Current-candidate results are recorded in the release-gate section above.

The Phase 5 Playwright output is preserved outside the repository at:

`<host-temp-directory>/<Phase-5-Playwright-output>`

OS temporary directories can be cleared by the operating system; this path is supplementary evidence. The test result and command are recorded above and in `PROJECT_COMPLETION_CHECKLIST.md`.

## Local setup and operation

Follow [README.md](README.md) for Windows PowerShell setup, local API startup, migration commands, and test scripts. Follow [OPERATIONS.md](OPERATIONS.md) for role provisioning, privilege boundaries, migrations, and protected backup/restore guidance. Keep `.env` local and ignored. Never use the administrative database URL for the API. Do not use `docker-compose down -v`; the `-v` option removes the local database volume.

At Final Closure Phase 6 inspection, the local API was running on `127.0.0.1:8080`; `/health/live` and `/health/ready` both returned HTTP 200. This is a point-in-time observation, not a service guarantee.

## Artifacts and preserved work

- The user-open `playwright-report/`, ignored `test-results/`, and `node_modules/` directories were preserved.
- The Phase 5 temporary Playwright output was preserved outside the repository.
- Later metadata-only inspection confirmed the Phase 11 database dump at `/tmp/p11_tpl_mw5fhcsinbkv.dump` inside container `todo-list-postgres-1`. It resides in that container's writable layer, not in the PostgreSQL volume. The dump was retained; no exact host-side copy path or current host-side copy has been verified.
- Later metadata-only checks reported the two grant-script copies absent at `/tmp/p11_tpl_a_mw5fhcsinbkv-grants.sql` and `/tmp/p11_tpl_b_mw5fhcsinbkv-grants.sql` inside the identified container.
- The credential JSON under the operator's local Windows temporary directory was later verified absent by exact-path metadata check. Its contents were not read.
- Historical records conflict: an earlier cleanup attempt was reported rejected and the files described as remaining; later records reported grant copies removed/absent and the credential file deleted/absent. The metadata findings establish reported path state at the later checks, but do not recover the exact time or mechanism. The dump remains retained according to the later inspection.
- `.env` was not read. No passwords, tokens, keys, or certificates were copied into this document.

## Remaining limitations and deployment prerequisites

- No production host, real HTTPS certificate, reverse proxy, external production PostgreSQL, secret manager, CI/CD pipeline, off-host backup destination, monitoring service, or tested recovery process is configured here.
- Email verification and password reset are deferred until a real domain, HTTPS deployment, and email provider exist.
- Production load, database capacity, and multi-instance operational behavior need deployment-specific measurement.
- The application has not been publicly deployed. Local regression approval is not a production security certification.

Before any real launch, provision a separate production database and least-privilege runtime role, configure HTTPS and trusted proxy/origin settings, use protected secrets, establish encrypted off-host backups and a tested restore procedure, configure monitoring and incident ownership, then rerun the deployment-specific security and regression gates.

## Repository handoff baseline

Read-only Git observation at this snapshot (2026-11-10):

- Repository: `<repository-root>`
- Branch / HEAD: `feature/authorization` / `1992114b16bedca0102a61d9d0f6b2b90b22a37f`.
- Upstream: none configured for this branch.
- Working tree: 41 modified tracked paths, 11 untracked paths, and 0 staged paths; it is not clean. The list includes existing user changes and is a point-in-time count, not a complete candidate manifest.
- This documentation task performed no Git mutation. The historical `main` / `79ff340...` / `origin/main` observation belongs to the earlier Final Closure Phase 6 snapshot and is not current.
