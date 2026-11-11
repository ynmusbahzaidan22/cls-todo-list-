# Operations and production preparation

This guide covers local verification and production-oriented configuration. It does not deploy the application. No public host, TLS certificate, managed PostgreSQL service, or production secret store has been configured or tested in this repository.

## Local roles and secrets

The local Compose account `POSTGRES_USER` is the migration/administrative account. Keep it out of the API process. Development migrations must use `ops/Invoke-DevelopmentMigrations.ps1`; do not invoke Tern directly for `todo_dev`. The wrapper loads `.env` silently, checks the explicit development target against `DATABASE_URL` and `tern.conf`, verifies the running PostgreSQL container/volume and database identity, checks migration inventory/version, and refuses migration while any other database session exists. Each migrate invocation advances exactly one explicitly expected version and requires `-AuthorizeDevelopmentMigration`. Test migrations must continue to use their separate `ops/Invoke-TestMigrations.ps1` wrapper and `tern.test.conf`.

After schema migration, provision the application role with `ops/provision_runtime_role.sql`. The script defaults to `todo_runtime` and accepts the same psql `runtime_role` variable for a custom name. It prompts for a password through hidden input only when the role has no password; rerunning it does not rotate an existing credential. `ops/grant_runtime_privileges.sql` accepts the same role variable and reapplies grants after restoring or adding migrations. Run both as a PostgreSQL administrator while connected to the intended application database.

The application role must not own the database, any non-system schema, application relations, application functions, or application types. It must be `LOGIN`, `NOSUPERUSER`, `NOCREATEDB`, `NOCREATEROLE`, `NOINHERIT`, `NOREPLICATION`, and `NOBYPASSRLS`; it must have no role memberships, database temporary-table/create rights, schema create rights, application-table trigger rights, or grant options. It requires database `CONNECT` and `app` schema `USAGE`. Effective database `CREATE`/`TEMPORARY` and schema `CREATE` privileges are prohibited, including access inherited through `PUBLIC`; provisioning revokes database `CREATE`/`TEMPORARY` from `PUBLIC` in the target database and removes `CREATE` on `public` schema. These are database-scoped changes, so review the target before running the scripts.

The table-level runtime allowlist is exact: `users` = `SELECT`, `INSERT`; `sessions` = `SELECT`, `INSERT`, `UPDATE`, `DELETE`; `tasks` = `SELECT`, `INSERT`, `UPDATE`; `task_activity` = `SELECT`, `INSERT`; `task_templates`, `task_template_items`, and `auth_rate_limits` = `SELECT`, `INSERT`, `UPDATE`, `DELETE`; `task_template_applications` and `task_template_application_items` = `SELECT`, `INSERT`. For `users`, the only column-level update is `UPDATE(time_zone)`; all other columns and all other column-level privileges must match the table-level allowlist. `TRUNCATE`, `REFERENCES`, `TRIGGER`, and `MAINTAIN` are prohibited on every app table. The role receives no privileges on app sequences or functions, and may not own application relations, functions, or types.

The role scripts remove existing `PUBLIC` and runtime-role table, column, sequence, and function privileges in schema `app` before applying the table allowlist; they also clear role privileges in `app` and regrant only schema `USAGE`. The verifier uses PostgreSQL effective privilege functions (therefore including `PUBLIC` and inherited access), inspects role memberships and ownership, and rejects unexpected PUBLIC ACLs, default ACL entries for PUBLIC/the runtime role, and grant options. It fails closed for unknown or missing app tables. The repository currently has no application role intended to rely on `PUBLIC` access to app objects. Trigger functions remain callable by their installed triggers; the runtime role does not need direct function `EXECUTE` because it neither creates nor alters triggers. Do not use the runtime role for Tern.

The template repository only reads and inserts tracking rows; no application query directly updates or deletes them. Deleting a template remains supported: PostgreSQL's internal foreign-key cascade executes its child-table operation under the referenced table's constraint enforcement, so the runtime role does not need direct `DELETE` on tracking tables. The runtime role can read and append activity events but cannot update, delete, or truncate them. Existing or default ACLs outside what the scripts explicitly normalize can cause startup verification to fail; investigate and correct those ACLs as the owning administrator rather than weakening the verifier.

Keep `POSTGRES_PASSWORD`, the chosen `todo_runtime` password, and URLs containing either password in the ignored `.env` file or a deployment secret manager. `.env.example` uses placeholders only. For local development, `DATABASE_URL` points to the runtime role in `todo_dev`. PostgreSQL integration tests use a separate disposable Compose project and PostgreSQL instance; `TEST_DATABASE_URL` must target exactly database `todo_test` on loopback port `25432`, and must never point to `todo_dev` or contain user data. Never paste secret-bearing URLs into logs, tickets, or shell history.

## Guarded development migration: `todo_dev`

The only supported development migration target is the PostgreSQL container `todo-list-postgres-1`, database `todo_dev`, reached through `127.0.0.1:15432` and volume `todo-list_postgres-data`. The wrapper rejects `todo_test`, other databases, remote hosts, unexpected ports, malformed URLs, Tern configuration drift, non-contiguous migration files, mismatched expected versions, and any migration request without explicit authorization. It also requires the container to be running and healthy and confirms the SQL session's database identity and PostgreSQL major version. It does not create backups or automatically roll back a failed migration.

Read-only status discovers the current version while requiring the repository maximum:

```powershell
 .\ops\Invoke-DevelopmentMigrations.ps1 -Action status -ExpectedTargetVersion 15
```

On 2026-11-11, `todo_dev` advanced from 13/14 to 14/14 after a new custom-format backup was created, restored into a unique temporary database, and compared against the source. All nine pre-existing application tables had matching row counts and canonical row fingerprints; the schema fingerprints matched before and after restore. Migration 014 then created the Admin audit and final-Admin guard schema without changing any pre-existing table data. The current read-only status command is:

```powershell
 .\ops\Invoke-DevelopmentMigrations.ps1 -Action status -ExpectedCurrentVersion 14 -ExpectedTargetVersion 15
```

The wrapper refuses to migrate if any other `todo_dev` session is connected and repeats that check immediately before invoking Tern. This narrows, but cannot eliminate, the race where another local client connects between the final check and Tern's connection. Keep writers stopped throughout the migration window. If any identity, version, health, backup, restore, or comparison check fails, do not migrate. If Tern fails, preserve the database and backup, record the exit code, and investigate; never recreate the database or run an automatic rollback. The development wrapper is deliberately distinct from the test wrapper.

## Isolated PostgreSQL integration-test instance

The development service in `compose.yaml` uses project `todo-list`, host port `15432`, and volume `todo-list_postgres-data`. Keep that service and volume separate. The test service in `compose.test.yaml` uses project `todo-list-test`, database `todo_test`, host-only binding `127.0.0.1:25432`, a dedicated project-scoped network, and dedicated volume `todo-list-test_postgres-test-data`. The test network is not marked internal, so the test container may have outbound network access. It has no shared database volume or network with development. The test database's `todo_test_admin` is privileged only inside this disposable test server because runtime-role integration tests create cluster roles; never use development or production administrator credentials there.

Create ignored `.env.test` from the test-only settings in `.env.example`, replacing the admin and runtime password placeholders with distinct local secrets. Do not copy credentials from `.env`. Set `TEST_POSTGRES_DB` to exactly `todo_test`; ensure both URLs use a supported loopback host, port `25432`, and database `todo_test`. Load `.env.test` into the PowerShell process without printing it, verify required settings, validate `TEST_DATABASE_URL` with the shared guard, verify `RUNTIME_DATABASE_URL` uses the same isolated endpoint, and validate Compose before starting anything:

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
node --input-type=module -e "import { validateTestDatabaseConfiguration } from './e2e/test-database.js'; validateTestDatabaseConfiguration(); console.log('Test database URL configuration is valid.')"
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml config --quiet
```

Only after configuration validation succeeds, start the test PostgreSQL service and verify readiness and its dedicated volume:

```powershell
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml up -d postgres-test
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml ps
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml exec -T postgres-test pg_isready -U $env:TEST_POSTGRES_USER -d todo_test
docker volume inspect todo-list-test_postgres-test-data
```

The migration directory contains thirteen migrations, applied by Tern in numeric order from 001 through 013. Migration 011 adds user names and roles; migration 012 adds uniqueness for `(template_id, position)` in `app.task_template_items`, after checking for duplicates and aborting without changing rows if any are found; migration 013 makes `users.time_zone` nullable, using NULL for the device-time-zone default while preserving saved IANA overrides. Tern's down sections vary: 002, 003, and 005 have none; 004, 008, 009, and 010 guard rollback against relevant data (010 also needs duplicate-free positions when restoring the former primary key); 011 is intentionally forward-only; 013 refuses rollback while any null time-zone preferences exist; and other down sections remove their migration's objects, with 006 dropping its rate-limit rows. File inventory is not evidence that a database has applied every migration; inspect each target's Tern status before operations. Review the exact down SQL and its data conditions before any rollback.

Apply the test migrations before provisioning the runtime role, because the privilege scripts grant access to objects created by the migrations. The migration helper validates `TEST_DATABASE_URL`, derives Tern's process-only `PG*` variables from it, and uses only `tern.test.conf`:

```powershell
.\ops\Invoke-TestMigrations.ps1 -Action status
.\ops\Invoke-TestMigrations.ps1 -Action migrate
.\ops\Invoke-TestMigrations.ps1 -Action status
```

After migrations succeed, provision the restricted runtime role using the existing idempotent scripts and supported custom-role variable. The provision script prompts for an initial password with hidden input; use that same separate secret in `RUNTIME_DATABASE_URL`. The PostgreSQL administrator and its superuser privileges remain confined to this test container:

```powershell
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml cp ops/provision_runtime_role.sql postgres-test:/tmp/provision_runtime_role.sql
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml exec -it postgres-test psql -v ON_ERROR_STOP=1 -v runtime_role=todo_test_runtime -U $env:TEST_POSTGRES_USER -d todo_test -f /tmp/provision_runtime_role.sql
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml cp ops/grant_runtime_privileges.sql postgres-test:/tmp/grant_runtime_privileges.sql
docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml exec -T postgres-test psql -v ON_ERROR_STOP=1 -v runtime_role=todo_test_runtime -U $env:TEST_POSTGRES_USER -d todo_test -f /tmp/grant_runtime_privileges.sql
```

Set `TEST_DATABASE_URL` and `RUNTIME_DATABASE_URL` from `.env.test` in the same process. The Go and Playwright guards require loopback, port `25432`, exact database `todo_test`, reject target-changing URL options, and reject a test target equal to `DATABASE_URL`. `ops/Invoke-TestMigrations.ps1` derives Tern's process-only `PG*` variables from the validated test URL and uses only `tern.test.conf`; run its `status` action before `migrate`. Set `REQUIRE_POSTGRES_INTEGRATION=1` when running Go tests so missing test configuration fails instead of skipping. The test URL is never a fallback to `DATABASE_URL`.

Run PostgreSQL integration tests only after migrations and runtime-role provisioning complete. The already-loaded `.env.test` process must provide both database URLs; requiring integration coverage makes a missing test URL fail instead of silently skipping:

```powershell
$env:REQUIRE_POSTGRES_INTEGRATION = '1'
try { go test -count=1 ./... } finally { Remove-Item Env:REQUIRE_POSTGRES_INTEGRATION -ErrorAction SilentlyContinue }
```

To stop only the test service while preserving its isolated data, use `docker compose --env-file .env.test -p todo-list-test -f compose.test.yaml stop postgres-test`. Do not use `down -v`; deleting the test volume destroys the test database. Do not change, stop, or remove the development project or `todo-list_postgres-data` volume as part of test setup.

Rotate credentials by creating a new randomly generated runtime password in the secret manager, changing the PostgreSQL role password with `psql`'s `\\password todo_runtime` prompt, updating the application secret, and rolling the API instances. Confirm readiness before removing the previous credential. Rotate the migration/admin credential separately and restrict its use to migration and recovery operations.

## Production configuration

Set these values through a protected environment file or secret manager:

```text
APP_ENV=production
HTTP_ADDR=0.0.0.0:8080
DATABASE_URL=postgres://todo_runtime:<secret>@<database-host>:5432/<database>?sslmode=verify-full
DATABASE_RUNTIME_ROLE=todo_runtime
DATABASE_MAX_CONNS=10
TRUSTED_ORIGIN=https://<public-host>
SESSION_COOKIE_SECURE=true
TRUSTED_PROXY_CIDRS=<exact reverse-proxy peer CIDR>
TASK_CURSOR_SIGNING_KEY=<64 hexadecimal characters generated from 32 random bytes>
```

On Windows PowerShell, generate a value with `$rng=[Security.Cryptography.RandomNumberGenerator]::Create(); $bytes=New-Object byte[] 32; $rng.GetBytes($bytes); $rng.Dispose(); ($bytes | ForEach-Object { $_.ToString('x2') }) -join ''`; on systems with OpenSSL, use `openssl rand -hex 32`. Do not check the generated value into the repository.

Production startup rejects missing security settings, non-HTTPS origins, a database URL without `sslmode=verify-full`, a mismatch between the URL username and `DATABASE_RUNTIME_ROLE`, an empty trusted-proxy list, or a missing/invalid task cursor signing key. Generate the key with a cryptographically secure generator and keep it stable across API restarts and replicas; rotating it invalidates outstanding task-list cursors. Local development keeps loopback HTTP and `sslmode=disable`; when no cursor key is configured, the API generates a process-local key, so cursors from before a local server restart are rejected.

The application does not terminate TLS. The supported topology is a reverse proxy running on the host, TLS at that proxy, and the API published only on host loopback (`127.0.0.1`). The proxy must validate certificates, redirect HTTP to HTTPS, send the original public `Host`, and append `X-Forwarded-For`. Set `TRUSTED_PROXY_CIDRS` to the proxy's exact peer address range. The API ignores forwarding headers from every other peer. Do not expose the API port directly to the internet. `X-Forwarded-Proto` is not used to enable security behavior: production cookies are always Secure and the configured origin must be HTTPS.

The application sets a restrictive same-origin Content Security Policy, `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy`, request IDs, and HSTS in production. HSTS uses a one-year max age without `includeSubDomains`; only enable it after HTTPS is live and confirmed for the public host. The production Compose file binds the API to loopback, uses a non-root distroless image, read-only root filesystem, dropped capabilities, resource limits, and a readiness health check.

For host-based Nginx TLS termination, the relevant proxy location is:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

Configure TLS certificates, HTTP-to-HTTPS redirection, request body limits, and certificate renewal at the proxy. Restrict access to the proxy and host firewall. This sample was reviewed as configuration guidance; it has not been exercised against a real domain or certificate.

## Migrations and deployment

1. Build a reviewed image from the project root: `docker-compose.exe -f compose.production.yaml build`.
2. Connect Tern to the production database using a separate migration account and verified TLS. Apply migrations before rolling out an application version that requires them. The local `tern.conf` is for the local database; provide production connection settings through the deployment's protected migration environment.
3. Reapply the explicit runtime grants after adding new tables or functions, then validate the effective privileges. Keep migration ownership separate from runtime ownership.
4. Start or update the API with `docker-compose.exe --env-file <protected-env-file> -f compose.production.yaml up -d --build`. Verify container health and the HTTPS public readiness endpoint. Roll back the application image if readiness fails; take and verify a backup before schema changes.
5. For code-only releases, roll instances gradually and monitor readiness/error logs. For schema changes, use additive migrations first and remove obsolete schema only in a separately reviewed release.

The production Compose configuration was syntax-checked locally; it has not been started against production PostgreSQL or an actual reverse proxy. Docker image build and runtime still require the configured external database and proxy environment.

## Backup and restore

Prerequisites: Docker Compose, a PostgreSQL administrative/migration credential, enough free space for a custom-format dump, and a protected host backup directory outside the Docker volume and repository. The commands below use the local Compose service as an example; use the same PostgreSQL client major version as the server for production recovery.

```powershell
$id = Get-Date -Format 'yyyyMMdd-HHmmss'
$containerDump = "/tmp/todo-$id.dump"
$backupDir = Join-Path $env:USERPROFILE 'Backups\todo-list'
New-Item -ItemType Directory -Force -Path $backupDir | Out-Null
$backupFile = Join-Path $backupDir "todo-$id.dump"
docker-compose.exe -f compose.yaml exec -T postgres pg_dump -Fc -U $env:POSTGRES_USER -d $env:POSTGRES_DB -f $containerDump
docker-compose.exe -f compose.yaml cp "postgres:$containerDump" $backupFile
docker-compose.exe -f compose.yaml exec -T postgres rm -f $containerDump
Get-FileHash -Algorithm SHA256 $backupFile
```

Load the protected environment variables into the PowerShell process before running these commands. Restrict directory ACLs, encrypt backups at rest and in transit, store an off-host copy, define a retention schedule and owner, and periodically test decryption and restore. Do not place backup files in the application repository or PostgreSQL data volume. A database dump contains user data and session records; protect and expire it accordingly.

Restore only into a newly named isolated database. Never point `pg_restore` at the live database:

```powershell
$restoreDb = 'todo_restore_YYYYMMDD'
docker-compose.exe -f compose.yaml exec -T postgres createdb -U $env:POSTGRES_USER $restoreDb
$containerDump = "/tmp/$restoreDb.dump"
docker-compose.exe -f compose.yaml cp $backupFile "postgres:$containerDump"
docker-compose.exe -f compose.yaml exec -T postgres pg_restore --exit-on-error --no-owner --no-privileges -U $env:POSTGRES_USER -d $restoreDb $containerDump
docker-compose.exe -f compose.yaml cp ops/grant_runtime_privileges.sql postgres:/tmp/grant_runtime_privileges.sql
docker-compose.exe -f compose.yaml exec -T postgres psql -v ON_ERROR_STOP=1 -U $env:POSTGRES_USER -d $restoreDb -f /tmp/grant_runtime_privileges.sql
```

Point a temporary API process at the restored database using `todo_runtime`, run `/health/ready`, and make an authenticated read of restored task/activity/template data. Compare representative row counts and ownership relationships with the source. Drop only the isolated restore database after recording evidence and confirming the target name; never drop the live database or volume. Recovery objectives, backup retention, encryption keys, and off-host restore procedures remain the operator's responsibility.

## Operational checks and troubleshooting

- `GET /health/live` checks process liveness. `GET /health/ready` pings PostgreSQL and returns 503 when it is unavailable. Neither exposes database credentials or schema details.
- Match each `X-Request-ID` response header with the structured request log. Logs include method, path, status, request ID, and duration; they omit query strings, request bodies, cookies, passwords, and stack traces.
- If startup fails, check configuration names and role privileges without printing secret-bearing URLs. For a database error, verify DNS/network reachability, certificate trust and hostname, migrations, runtime grants, and pool limits.
- If an authentication endpoint returns 429, review the configured shared PostgreSQL window and wait for expiry. If PostgreSQL is down, authentication fails closed with a generic error; it never disables the rate limit.
- If requests through the proxy receive a host error or rate limits group clients incorrectly, verify `Host`, the actual proxy peer address, and the exact `TRUSTED_PROXY_CIDRS`. Forwarded headers from untrusted peers are ignored.
- The maintenance worker deletes expired/old revoked sessions and expired rate-limit rows in bounded batches every 15 minutes. Ordinary rate-limit requests do not purge unrelated expired keys; only a new key request at the 100,000-row cap performs the minimum locked, bounded expiry cleanup needed to make room, failing closed if capacity remains exhausted. It logs a generic warning if maintenance cleanup fails; authentication remains available subject to the existing shared rate limits.
- The runtime role has no schema-management rights. Run Tern with the migration account, not `DATABASE_URL`.

## Security review and deferred work

Local code/test review found no P0 or P1 issue after the fixes in this phase. Existing owner-scoped SQL, foreign keys, optimistic versions, transactions, bounded JSON request sizes, same-origin state-changing requests, Argon2id, hashed random sessions, and cookie protections remain in place. The auth service uses PostgreSQL-backed atomic IP/account limits with a hard cap of 100,000 active keys; limiter storage errors fail closed. Registration returns the same generic 202 response for new and existing accounts. Browser rendering uses text nodes rather than HTML insertion.

Email Verification and Password Reset are **DEFERRED — POST-DEPLOYMENT**. They require a real domain, HTTPS, and an email provider. No email tables, endpoints, screens, tokens, or delivery dependencies were added in this phase. Local verification does not certify production security. P0/P1 and P2/P3 findings must be reassessed after the actual host, proxy, database, secret store, and operational recovery process are configured.

## Admin account-management schema and privileges

Migration `014_admin_account_audit.sql` adds append-only `app.admin_audit_events`, a private `app.active_admin_guard` counter, and a `SECURITY DEFINER` row trigger that atomically rejects a transition which would leave no active Admin. The migration is intentionally forward-only: rolling it back would erase audit history and remove the final-Admin invariant. Migration `015_allow_initial_admin_bootstrap_audit.sql` permits only the system-attributed initial-bootstrap action and refuses rollback after such an event exists. Read-only Tern status on 2026-11-11 reported `todo_dev` at 14/15 and `todo_test` at 15/15; migration 015 was applied only to `todo_test`. The read-only `adminctl status` confirmed zero active Admins and guard count zero on `todo_dev`; no account was promoted. After applying migration 014 to another database, rerun `ops/provision_runtime_role.sql` and `ops/grant_runtime_privileges.sql` for that same database before starting the application.

The application runtime role retains table-level `SELECT`/`INSERT` on `app.users` and has column-level `UPDATE` only on `time_zone`, `role`, `status`, and `updated_at`; it has no table-level UPDATE, DELETE, TRUNCATE, or access to `app.active_admin_guard`. It may insert `app.admin_audit_events` but cannot read or change the audit trail. Each role/status/session-revocation action rechecks the actor's active Admin state inside a transaction, serializes account-management operations, revokes the target's active sessions, and commits its safe before/after audit record atomically. Keep the runtime grants and `VerifyRuntimeRole` policy synchronized; production startup must fail closed if effective privileges drift.

Only the Admin HTTP handlers expose role/status/session operations. The API rejects self-management, validates allowlisted role/status values, returns generic storage errors, and does not hard-delete users. Disabling preserves user-owned tasks and history. Re-enabling does not restore revoked sessions. Session revocation targets all active sessions for one non-self target; no raw session token or hash is listed or returned. Successful changes are audited with actor/target IDs, action, role/status or active-session counts, and timestamp; passwords, tokens, email, and other personal fields are excluded.

Normal registration continues to assign `USER`; there is no public Admin-registration endpoint. The dedicated `cmd/adminctl` tool provides a one-time status and bootstrap workflow. It requires an exact normalized email, a fixed verified database target, and an explicit confirmation flag for promotion. It refuses a nonzero active-Admin/guard count, a missing or ineligible target, a second bootstrap, unknown databases, and `todo_test` outside the dedicated automated-test mode. In one transaction it serializes account-management changes, locks the users table, guard row, and target account, promotes only the selected active USER, revokes the target's existing active sessions, writes a `INITIAL_ADMIN_BOOTSTRAPPED` audit event with a null system actor, and commits atomically. The audit payload contains only role, status, and session counts.

Future first-Admin procedure: register the real owner through the normal flow and complete any company-required activation. Before the bootstrap, use the guarded development migration procedure with its required verified backup to apply pending migration 015; current read-only status is `todo_dev` 14/15, so bootstrap must not be attempted until status reports 15/15. Load `.env` settings with `Read-StrictEnvironmentFile`; run `go run ./cmd/adminctl status --database todo_dev --email $ownerEmail`; proceed only when the active-Admin and guard counts are both zero and the exact target is eligible; then run `go run ./cmd/adminctl bootstrap --database todo_dev --email $ownerEmail --confirm-database todo_dev`. The owner must log in again because existing sessions are revoked. Manage subsequent Admins through the authenticated Admin interface. The current schema has no email-verification field, so account eligibility currently means an active account with a normal USER role and valid stored name/email. Actual initial-Admin provisioning remains pending creation and approval of the real owner account; no placeholder or existing development account is to be promoted.
