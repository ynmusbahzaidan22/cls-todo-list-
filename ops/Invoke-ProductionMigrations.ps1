[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('status', 'migrate')]
    [string]$Action,

    [int]$ExpectedCurrentVersion = -1,

    [Parameter(Mandatory = $true)]
    [ValidateRange(1, 999)]
    [int]$ExpectedTargetVersion,

    [switch]$AuthorizeProductionMigration
)

$ErrorActionPreference = 'Stop'
$scriptRoot = Split-Path -Parent $PSScriptRoot
Import-Module (Join-Path $PSScriptRoot 'ProductionMigrationGuard.psm1') -Force
Import-Module (Join-Path $PSScriptRoot 'DevelopmentMigrationGuard.psm1') -Force

$processNames = @('PRODUCTION_DATABASE_URL', 'PRODUCTION_POSTGRES_HOST', 'PRODUCTION_POSTGRES_PORT', 'PRODUCTION_POSTGRES_DB', 'PRODUCTION_POSTGRES_USER', 'PRODUCTION_POSTGRES_PASSWORD', 'DATABASE_URL', 'PGHOST', 'PGPORT', 'PGDATABASE', 'PGUSER', 'PGPASSWORD', 'PGSSLMODE', 'PGSERVICE', 'PGSERVICEFILE', 'PGOPTIONS')
$previous = @{}
foreach ($name in $processNames) { $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }

try {
    $stage = 'production target and configuration validation'
    $environment = @{}
    foreach ($name in $processNames) {
        $value = [Environment]::GetEnvironmentVariable($name, 'Process')
        if ($null -ne $value) { $environment[$name] = $value }
    }
    $configPath = Join-Path $scriptRoot 'tern.production.conf'
    $configText = Get-Content -LiteralPath $configPath -Raw
    $target = Assert-ProductionMigrationConfiguration -Environment $environment -TernConfigText $configText -Action $Action -ExpectedCurrentVersion $ExpectedCurrentVersion -ExpectedTargetVersion $ExpectedTargetVersion -AuthorizeProductionMigration:$AuthorizeProductionMigration

    $stage = 'migration inventory validation'
    $versions = @(Get-NumberedMigrationInventory -MigrationDirectory (Join-Path $scriptRoot 'migrations'))
    Assert-ExpectedMigrationInventory -Versions $versions -ExpectedTargetVersion 15

    $env:PRODUCTION_POSTGRES_HOST = $target.Host
    $env:PRODUCTION_POSTGRES_PORT = [string]$target.Port
    $env:PRODUCTION_POSTGRES_DB = $target.Database
    $env:PRODUCTION_POSTGRES_USER = $target.User
    $env:PRODUCTION_POSTGRES_PASSWORD = $target.Password
    foreach ($name in @('DATABASE_URL', 'PGHOST', 'PGPORT', 'PGDATABASE', 'PGUSER', 'PGPASSWORD', 'PGSSLMODE', 'PGSERVICE', 'PGSERVICEFILE', 'PGOPTIONS')) {
        Remove-Item "Env:$name" -ErrorAction SilentlyContinue
    }

    function Invoke-ProductionTernStatus {
        param([string]$HostName)
        Push-Location $scriptRoot
        try {
            $output = & tern status --config tern.production.conf --migrations migrations 2>&1
            $code = $LASTEXITCODE
        }
        finally { Pop-Location }
        if ($code -ne 0) { throw "Tern status failed with exit code $code; database diagnostics are suppressed." }
        return ConvertFrom-ProductionTernStatus -Lines @($output | ForEach-Object { [string]$_ }) -ExpectedHost $HostName
    }

    $stage = 'read-only Tern status preflight'
    $status = Invoke-ProductionTernStatus -HostName $target.Host
    if ($status.Maximum -ne 15 -or ($ExpectedCurrentVersion -ge 0 -and $status.Current -ne $ExpectedCurrentVersion)) {
        throw "Observed production migration version $($status.Current)/$($status.Maximum) did not match the explicitly expected version."
    }
    if ($Action -ceq 'status') {
        Write-Output "Target verified: direct TLS connection to neondb on the approved Frankfurt endpoint."
        Write-Output "Tern version: $($status.Current)/$($status.Maximum); status: $($status.Status)."
        Write-Output "Pending migrations: $($status.Maximum - $status.Current)."
        exit 0
    }

    if ($status.Current -ne 0 -or $status.Maximum -ne 15) {
        throw 'Production bootstrap migration refused: expected an empty migration state at version 0 of 15.'
    }

    $stage = 'Tern migration execution'
    Push-Location $scriptRoot
    try {
        $null = & tern migrate --config tern.production.conf --migrations migrations 2>&1
        $migrateExit = $LASTEXITCODE
    }
    finally { Pop-Location }
    Write-Output "TERN_MIGRATE_EXIT_CODE=$migrateExit"
    if ($migrateExit -ne 0) { throw "Tern migration failed with exit code $migrateExit; no automatic rollback was attempted." }

    $stage = 'post-migration Tern verification'
    $final = Invoke-ProductionTernStatus -HostName $target.Host
    if ($final.Current -ne 15 -or $final.Maximum -ne 15 -or $final.Status -cne 'up to date') {
        throw 'Post-migration Tern status did not reach version 15 of 15.'
    }
    Write-Output 'Migration complete: 15 of 15.'
}
catch {
    Write-Error ("Production migration workflow stopped during {0}: {1}" -f $stage, $_.Exception.Message)
    exit 1
}
finally {
    foreach ($name in $processNames) {
        if ($null -eq $previous[$name]) { Remove-Item "Env:$name" -ErrorAction SilentlyContinue }
        else { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
    }
}
