$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSScriptRoot 'ProductionMigrationGuard.psm1') -Force
Import-Module (Join-Path $PSScriptRoot 'DevelopmentMigrationGuard.psm1') -Force

$url = 'postgresql://neondb_owner:fixture-secret@ep-test.c-7.eu-central-1.aws.neon.tech:5432/neondb?sslmode=verify-full'
$config = @'
[database]
host = {{ env "PRODUCTION_POSTGRES_HOST" }}
port = {{ env "PRODUCTION_POSTGRES_PORT" }}
database = {{ env "PRODUCTION_POSTGRES_DB" }}
user = {{ env "PRODUCTION_POSTGRES_USER" }}
password = {{ env "PRODUCTION_POSTGRES_PASSWORD" }}
sslmode = verify-full
'@
$valid = @{ PRODUCTION_DATABASE_URL = $url }
$checks = 0

function Assert-TestPass {
    param([string]$Name, [scriptblock]$Action)
    $null = & $Action
    $script:checks++
    Write-Output "PASS $Name"
}

function Assert-TestReject {
    param([string]$Name, [scriptblock]$Action)
    $rejected = $false
    $message = ''
    try { $null = & $Action } catch { $rejected = $true; $message = $_.Exception.Message }
    if (-not $rejected) { throw "Safety check failed to reject: $Name" }
    if ($message.Contains('fixture-secret')) { throw "Safety check exposed a credential while rejecting: $Name" }
    $script:checks++
    Write-Output "PASS rejects $Name"
}

Assert-TestPass 'accepts only explicit direct owner target with verify-full' {
    $target = ConvertFrom-StrictProductionDatabaseUrl $url
    if ($target.Host -cne 'ep-test.c-7.eu-central-1.aws.neon.tech' -or $target.Port -ne 5432 -or $target.Database -cne 'neondb' -or $target.User -cne 'neondb_owner' -or $target.SSLMode -cne 'verify-full') { throw 'Parsed production target mismatch.' }
}
foreach ($case in @(
    @{ Name = 'wrong database'; Value = $url.Replace('/neondb?', '/other?') }
    @{ Name = 'runtime role used for migration'; Value = $url.Replace('neondb_owner', 'todo_runtime') }
    @{ Name = 'pooler endpoint'; Value = $url.Replace('ep-test.c-7.', 'ep-test-pooler.c-7.') }
    @{ Name = 'wrong region'; Value = $url.Replace('eu-central-1', 'us-east-1') }
    @{ Name = 'wrong port'; Value = $url.Replace(':5432/', ':6432/') }
    @{ Name = 'TLS require'; Value = $url.Replace('verify-full', 'require') }
    @{ Name = 'duplicate sslmode'; Value = $url + '&sslmode=verify-full' }
    @{ Name = 'target override query'; Value = $url + '&host=other.example' }
    @{ Name = 'encoded query key'; Value = $url + '&%73slmode=verify-full' }
    @{ Name = 'ambiguous fragment'; Value = $url + '#fragment' }
)) {
    Assert-TestReject $case.Name { ConvertFrom-StrictProductionDatabaseUrl $case.Value }
}
Assert-TestPass 'accepts guarded 0 of 15 status configuration' {
    $null = Assert-ProductionMigrationConfiguration -Environment $valid -TernConfigText $config -Action status -ExpectedTargetVersion 15
}
Assert-TestReject 'blocks migrate without explicit authorization' {
    Assert-ProductionMigrationConfiguration -Environment $valid -TernConfigText $config -Action migrate -ExpectedCurrentVersion 0 -ExpectedTargetVersion 15
}
Assert-TestReject 'blocks a nonzero production starting version' {
    Assert-ProductionMigrationConfiguration -Environment $valid -TernConfigText $config -Action migrate -ExpectedCurrentVersion 1 -ExpectedTargetVersion 15 -AuthorizeProductionMigration
}
Assert-TestReject 'blocks targets other than 15' {
    Assert-ProductionMigrationConfiguration -Environment $valid -TernConfigText $config -Action status -ExpectedTargetVersion 14
}
Assert-TestReject 'rejects inherited libpq overrides' {
    $candidate = $valid.Clone(); $candidate.PGHOST = 'unexpected'
    Assert-ProductionMigrationConfiguration -Environment $candidate -TernConfigText $config -Action status -ExpectedTargetVersion 15
}
Assert-TestReject 'rejects changed Tern TLS configuration' {
    Assert-ProductionMigrationConfiguration -Environment $valid -TernConfigText $config.Replace('sslmode = verify-full','sslmode = require') -Action status -ExpectedTargetVersion 15
}
$parsed = ConvertFrom-ProductionTernStatus -Lines @('status: migration(s) pending', 'version:  0 of 15', 'host:     ep-test.c-7.eu-central-1.aws.neon.tech', 'database: neondb') -ExpectedHost 'ep-test.c-7.eu-central-1.aws.neon.tech'
Assert-TestPass 'parses production Tern status' {
    if ($parsed.Current -ne 0 -or $parsed.Maximum -ne 15 -or $parsed.Status -notmatch 'pending') { throw 'Tern status parse mismatch.' }
}
Assert-TestReject 'rejects status for another host' {
    ConvertFrom-ProductionTernStatus -Lines @('status: migration(s) pending', 'version:  0 of 15', 'host:     other', 'database: neondb') -ExpectedHost 'ep-test.c-7.eu-central-1.aws.neon.tech'
}

Write-Output "Production migration guard checks passed: $checks."
