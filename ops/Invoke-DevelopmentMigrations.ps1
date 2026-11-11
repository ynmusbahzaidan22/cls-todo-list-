[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('status', 'migrate')]
    [string]$Action,

    [ValidateRange(-1, 999)]
    [int]$ExpectedCurrentVersion = -1,

    [Parameter(Mandatory = $true)]
    [ValidateRange(1, 999)]
    [int]$ExpectedTargetVersion,

    [switch]$AuthorizeDevelopmentMigration
)

$ErrorActionPreference = 'Stop'
$scriptRoot = Split-Path -Parent $PSScriptRoot
Import-Module (Join-Path $PSScriptRoot 'DevelopmentMigrationGuard.psm1') -Force

function Invoke-DockerReadOnly {
    param([Parameter(Mandatory)][string[]]$Arguments)
    $output = & docker.exe @Arguments 2>$null
    if ($LASTEXITCODE -ne 0) { throw 'Read-only Docker inspection failed.' }
    return @($output | ForEach-Object { [string]$_ })
}

function Assert-DevelopmentContainer {
    $format = '{{.Id}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}|{{index .Config.Labels `com.docker.compose.project`}}|{{index .Config.Labels `com.docker.compose.service`}}|{{.Config.Image}}|{{json .NetworkSettings.Ports}}|{{range .Mounts}}{{.Name}}:{{.Destination}};{{end}}'
    $lines = @(Invoke-DockerReadOnly @('inspect', 'todo-list-postgres-1', '--format', $format))
    if ($lines.Count -ne 1) { throw 'Expected development PostgreSQL container was not uniquely identified.' }
    $parts = $lines[0].Split('|', 8)
    if ($parts.Count -ne 8 -or $parts[0] -notmatch '^[0-9a-f]{64}$' -or $parts[1] -cne 'running' -or
        $parts[2] -cne 'healthy' -or $parts[3] -cne 'todo-list' -or $parts[4] -cne 'postgres' -or
        $parts[5] -notmatch '^postgres:18(?:\.[0-9]+)?$') {
        throw 'Development PostgreSQL container identity or health did not match the approved target.'
    }
    $ports = $parts[6] | ConvertFrom-Json
    $bindings = @($ports.'5432/tcp')
    if ($bindings.Count -ne 1 -or $bindings[0].HostIp -cne '127.0.0.1' -or $bindings[0].HostPort -cne '15432') {
        throw 'Development PostgreSQL port binding did not match the approved target.'
    }
    if ($parts[7] -cne 'todo-list_postgres-data:/var/lib/postgresql;') {
        throw 'Development PostgreSQL data volume did not match the approved target.'
    }
}

function Invoke-DatabaseIdentityCheck {
    param([Parameter(Mandatory)][string]$AdminUser)

    $sql = @'
SELECT 'IDENTITY|' || current_database() || '|' || current_user || '|' ||
       (SELECT d.datdba::regrole::text FROM pg_database d WHERE d.datname = current_database()) || '|' ||
       current_setting('server_version') || '|' || r.rolsuper::text || '|' || r.rolcreatedb::text
FROM pg_roles r WHERE r.rolname = current_user;
SELECT 'OTHER_SESSIONS|' || count(*)::text
FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid();
'@
    $lines = @(Invoke-DockerReadOnly @('exec', 'todo-list-postgres-1', 'psql', '-X', '-U', $AdminUser, '-d', 'todo_dev', '-At', '-c', $sql))
    $identity = @($lines | Where-Object { $_ -match '^IDENTITY\|' })
    $sessions = @($lines | Where-Object { $_ -match '^OTHER_SESSIONS\|' })
    if ($identity.Count -ne 1 -or $sessions.Count -ne 1) { throw 'Database identity verification returned an unexpected result.' }
    $fields = $identity[0].Split('|')
    $sessionFields = $sessions[0].Split('|')
    if ($fields.Count -ne 7 -or $fields[1] -cne 'todo_dev' -or $fields[2] -cne $AdminUser -or $fields[3] -cne $AdminUser -or
        $fields[4] -notmatch '^18\.6(?:\s|$)' -or ($fields[5] -cne 'true' -and $fields[6] -cne 'true') -or
        $sessionFields.Count -ne 2 -or $sessionFields[1] -notmatch '^[0-9]+$') {
        throw 'Database identity, PostgreSQL version, or migration-account privileges did not match the expected development target.'
    }
    return [int]$sessionFields[1]
}

function Invoke-TernStatus {
    Push-Location $scriptRoot
    try {
        $output = & tern status --config tern.conf --migrations migrations 2>&1
        $code = $LASTEXITCODE
    }
    finally { Pop-Location }
    if ($code -ne 0) { throw "Tern status failed with exit code $code; diagnostic output was suppressed." }
    return ConvertFrom-TernStatus -Lines @($output | ForEach-Object { [string]$_ })
}

$processNames = @('DATABASE_URL', 'POSTGRES_DB', 'POSTGRES_USER', 'POSTGRES_PASSWORD', 'PGHOST', 'PGPORT', 'PGDATABASE', 'PGUSER', 'PGPASSWORD', 'PGSSLMODE')
$previous = @{}
foreach ($name in $processNames) { $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }

try {
    $stage = 'environment and configuration validation'
    $environment = Read-StrictEnvironmentFile -Path (Join-Path $scriptRoot '.env')
    $ternConfigText = Get-Content -LiteralPath (Join-Path $scriptRoot 'tern.conf') -Raw
    $target = Assert-DevelopmentMigrationConfiguration -Environment $environment -TernConfigText $ternConfigText -Action $Action -ExpectedCurrentVersion $ExpectedCurrentVersion -ExpectedTargetVersion $ExpectedTargetVersion -AuthorizeDevelopmentMigration:$AuthorizeDevelopmentMigration
    $versions = Get-NumberedMigrationInventory -MigrationDirectory (Join-Path $scriptRoot 'migrations')
    Assert-ExpectedMigrationInventory -Versions $versions -ExpectedTargetVersion $ExpectedTargetVersion

    $env:DATABASE_URL = [string]$environment.DATABASE_URL
    $env:POSTGRES_DB = [string]$environment.POSTGRES_DB
    $env:POSTGRES_USER = [string]$environment.POSTGRES_USER
    $env:POSTGRES_PASSWORD = [string]$environment.POSTGRES_PASSWORD
    foreach ($name in @('PGHOST', 'PGPORT', 'PGDATABASE', 'PGUSER', 'PGPASSWORD', 'PGSSLMODE')) { Remove-Item "Env:$name" -ErrorAction SilentlyContinue }

    $stage = 'container identity verification'
    Assert-DevelopmentContainer
    $stage = 'database identity verification'
    $otherSessions = Invoke-DatabaseIdentityCheck -AdminUser ([string]$environment.POSTGRES_USER)
    $stage = 'Tern status verification'
    $status = Invoke-TernStatus
    if ($status.Maximum -ne $ExpectedTargetVersion -or ($ExpectedCurrentVersion -ge 0 -and $status.Current -ne $ExpectedCurrentVersion)) {
        throw "Observed Tern version $($status.Current)/$($status.Maximum) did not match expected $ExpectedCurrentVersion/$ExpectedTargetVersion."
    }

    if ($Action -ceq 'status') {
        Write-Output 'Target verified: 127.0.0.1:15432/todo_dev (PostgreSQL 18.x).'
        Write-Output "Tern version: $($status.Current)/$($status.Maximum)."
        Write-Output "Other database sessions observed: $otherSessions."
        Write-Output "Pending migration count: $($status.Maximum - $status.Current)."
        exit 0
    }

    $stage = 'writer-session exclusion verification'
    if ($otherSessions -ne 0) { throw 'Migration refused because other database sessions are connected; stop application writers and retry.' }
    $confirmSessions = Invoke-DatabaseIdentityCheck -AdminUser ([string]$environment.POSTGRES_USER)
    if ($confirmSessions -ne 0) { throw 'Migration refused because a database session appeared during preflight.' }

    $stage = 'Tern migration execution'
    Push-Location $scriptRoot
    try {
        $null = & tern migrate --config tern.conf --migrations migrations 2>&1
        $migrateExit = $LASTEXITCODE
    }
    finally { Pop-Location }
    Write-Output "TERN_MIGRATE_EXIT_CODE=$migrateExit"
    if ($migrateExit -ne 0) { throw "Tern migration failed with exit code $migrateExit; no automatic rollback was attempted." }

    $stage = 'post-migration Tern status verification'
    $final = Invoke-TernStatus
    if ($final.Current -ne $ExpectedTargetVersion -or $final.Maximum -ne $ExpectedTargetVersion) {
        throw 'Post-migration Tern version did not reach the explicitly requested target.'
    }
    Write-Output "Migration complete: target version $($final.Current)/$($final.Maximum)."
}
catch {
    Write-Error ("Development migration workflow stopped during {0}: {1}" -f $stage, $_.Exception.Message)
    exit 1
}
finally {
    foreach ($name in $processNames) {
        if ($null -eq $previous[$name]) { Remove-Item "Env:$name" -ErrorAction SilentlyContinue }
        else { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
    }
}
