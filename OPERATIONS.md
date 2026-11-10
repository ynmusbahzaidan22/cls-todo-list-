# Operations and production preparation

This guide covers local verification and production-oriented configuration. It does not deploy the application. No public host, TLS certificate, managed PostgreSQL service, or production secret store has been configured or tested in this repository.

## Local roles and secrets

The local Compose account `POSTGRES_USER` is the migration/administrative account. Keep it out of the API process. Run migrations with Tern using that administrative account, then provision the application role with `ops/provision_runtime_role.sql`. The script defaults to `todo_runtime` and accepts the same psql `runtime_role` variable for a custom name. It prompts for a password through hidden input only when the role has no password; rerunning it does not rotate an existing credential. `ops/grant_runtime_privileges.sql` accepts the same role variable and reapplies grants after restoring or adding migrations. Run both as a PostgreSQL administrator while connected to the intended application database.

The application role must not own the database, `app` schema, or application tables. It has no schema-create or database-create privilege and cannot truncate application tables. It can insert users, manage sessions, read and mutate owned tasks, read and append activity events but never update or delete existing activity, manage templates and items, and maintain bounded rate-limit rows. Startup verification rejects effective UPDATE, DELETE, or TRUNCATE privileges on `app.task_activity`. Do not use the runtime role for Tern.

Keep `POSTGRES_PASSWORD`, the chosen `todo_runtime` password, and URLs containing either password in the ignored `.env` file or a deployment secret manager. `.env.example` uses placeholders only. For local development, store the admin connection in `TEST_DATABASE_URL` and use the runtime connection in `DATABASE_URL`. Never paste secret-bearing URLs into logs, tickets, or shell history.

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
- The maintenance worker deletes expired/old revoked sessions and expired rate-limit rows in bounded batches every 15 minutes. It logs a generic warning if cleanup fails; authentication remains available subject to the existing shared rate limits.
- The runtime role has no schema-management rights. Run Tern with the migration account, not `DATABASE_URL`.

## Security review and deferred work

Local code/test review found no P0 or P1 issue after the fixes in this phase. Existing owner-scoped SQL, foreign keys, optimistic versions, transactions, bounded JSON request sizes, same-origin state-changing requests, Argon2id, hashed random sessions, and cookie protections remain in place. The auth service uses PostgreSQL-backed atomic IP/account limits with a hard cap of 100,000 active keys; limiter storage errors fail closed. Registration returns the same generic 202 response for new and existing accounts. Browser rendering uses text nodes rather than HTML insertion.

Email Verification and Password Reset are **DEFERRED — POST-DEPLOYMENT**. They require a real domain, HTTPS, and an email provider. No email tables, endpoints, screens, tokens, or delivery dependencies were added in this phase. Local verification does not certify production security. P0/P1 and P2/P3 findings must be reassessed after the actual host, proxy, database, secret store, and operational recovery process are configured.
