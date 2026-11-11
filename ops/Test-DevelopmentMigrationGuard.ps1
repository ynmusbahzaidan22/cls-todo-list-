$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSScriptRoot 'DevelopmentMigrationGuard.psm1') -Force

$config = @'
[database]
host = 127.0.0.1
port = 15432
database = {{ env "POSTGRES_DB" }}
user = {{ env "POSTGRES_USER" }}
password = {{ env "POSTGRES_PASSWORD" }}
sslmode = disable
'@
$valid = @{
    DATABASE_URL = 'postgres://runtime:fixture-secret@127.0.0.1:15432/todo_dev?sslmode=disable'
    POSTGRES_DB = 'todo_dev'
    POSTGRES_USER = 'todo_dev'
    POSTGRES_PASSWORD = 'admin-fixture-secret'
}
$checks = 0

function Assert-GuardPass {
    param([string]$Name, [scriptblock]$Action)
    $null = & $Action
    $script:checks++
    Write-Output "PASS $Name"
}

function Assert-GuardReject {
    param([string]$Name, [scriptblock]$Action, [string[]]$Secrets = @())
    $rejected = $false
    $message = ''
    try { $null = & $Action } catch { $rejected = $true; $message = $_.Exception.Message }
    if (-not $rejected) { throw "Safety check failed to reject: $Name" }
    foreach ($secret in $Secrets) {
        if ($message.Contains($secret)) { throw "Safety check exposed a secret while rejecting: $Name" }
    }
    $script:checks++
    Write-Output "PASS rejects $Name"
}

Assert-GuardPass 'accepts the exact development target' {
    $target = Assert-DevelopmentMigrationConfiguration -Environment $valid -TernConfigText $config -Action status -ExpectedCurrentVersion 12 -ExpectedTargetVersion 13
    if ($target.Host -cne '127.0.0.1' -or $target.Port -ne 15432 -or $target.Database -cne 'todo_dev') { throw 'Wrong parsed development target.' }
}
Assert-GuardPass 'read-only status may discover the current version' {
    $null = Assert-DevelopmentMigrationConfiguration -Environment $valid -TernConfigText $config -Action status -ExpectedTargetVersion 13
}

foreach ($case in @(
    @{ Name = 'isolated test database'; URL = 'postgres://runtime:fixture-secret@127.0.0.1:25432/todo_test' }
    @{ Name = 'wrong database'; URL = 'postgres://runtime:fixture-secret@127.0.0.1:15432/other_db' }
    @{ Name = 'remote host'; URL = 'postgres://runtime:fixture-secret@192.0.2.4:15432/todo_dev' }
    @{ Name = 'unexpected port'; URL = 'postgres://runtime:fixture-secret@127.0.0.1:25432/todo_dev' }
    @{ Name = 'malformed URL'; URL = 'postgres://runtime:fixture-secret@127.0.0.1:15432/todo_dev#fragment' }
    @{ Name = 'unsupported connection option'; URL = 'postgres://runtime:fixture-secret@127.0.0.1:15432/todo_dev?options=-csearch_path%3Dpublic' }
)) {
    $candidate = $valid.Clone()
    $candidate.DATABASE_URL = $case.URL
    Assert-GuardReject $case.Name { Assert-DevelopmentMigrationConfiguration -Environment $candidate -TernConfigText $config -Action status -ExpectedCurrentVersion 12 -ExpectedTargetVersion 13 } @('fixture-secret', 'admin-fixture-secret')
}

Assert-GuardReject 'missing required development setting' {
    $candidate = $valid.Clone(); $candidate.Remove('POSTGRES_PASSWORD')
    Assert-DevelopmentMigrationConfiguration -Environment $candidate -TernConfigText $config -Action status -ExpectedCurrentVersion 12 -ExpectedTargetVersion 13
}
Assert-GuardReject 'duplicate setting in environment file' {
    $tempPath = Join-Path ([System.IO.Path]::GetTempPath()) ('dev-migration-env-' + [guid]::NewGuid().ToString('N') + '.env')
    try {
        [System.IO.File]::WriteAllText($tempPath, "POSTGRES_DB=todo_dev`nPOSTGRES_DB=todo_test`n")
        Read-StrictEnvironmentFile -Path $tempPath
    }
    finally { Remove-Item -LiteralPath $tempPath -Force -ErrorAction SilentlyContinue }
}
foreach ($replacement in @(
    @{ Name = 'unexpected Tern host'; Old = 'host = 127.0.0.1'; New = 'host = 192.0.2.4' }
    @{ Name = 'unexpected Tern port'; Old = 'port = 15432'; New = 'port = 25432' }
    @{ Name = 'unexpected Tern database'; Old = 'database = {{ env "POSTGRES_DB" }}'; New = 'database = todo_test' }
    @{ Name = 'unexpected Tern user source'; Old = 'user = {{ env "POSTGRES_USER" }}'; New = 'user = runtime' }
    @{ Name = 'unexpected Tern password source'; Old = 'password = {{ env "POSTGRES_PASSWORD" }}'; New = 'password = hardcoded' }
    @{ Name = 'unexpected Tern connection option'; Old = 'sslmode = disable'; New = 'sslmode = require' }
)) {
    Assert-GuardReject $replacement.Name {
        $changedConfig = $config.Replace($replacement.Old, $replacement.New)
        Assert-DevelopmentMigrationConfiguration -Environment $valid -TernConfigText $changedConfig -Action status -ExpectedCurrentVersion 12 -ExpectedTargetVersion 13
    } @('fixture-secret', 'admin-fixture-secret')
}
Assert-GuardReject 'migration without explicit authorization' {
    Assert-DevelopmentMigrationConfiguration -Environment $valid -TernConfigText $config -Action migrate -ExpectedCurrentVersion 12 -ExpectedTargetVersion 13
}
Assert-GuardReject 'mismatched one-step version expectation' {
    Assert-DevelopmentMigrationConfiguration -Environment $valid -TernConfigText $config -Action migrate -ExpectedCurrentVersion 11 -ExpectedTargetVersion 13 -AuthorizeDevelopmentMigration
}

$inventory = @(Get-NumberedMigrationInventory -MigrationDirectory (Join-Path (Split-Path -Parent $PSScriptRoot) 'migrations'))
Assert-GuardPass 'accepts the exact contiguous repository migration inventory' {
    Assert-ExpectedMigrationInventory -Versions $inventory -ExpectedTargetVersion 13
}
Assert-GuardReject 'requested target differs from repository maximum' {
    Assert-ExpectedMigrationInventory -Versions $inventory -ExpectedTargetVersion 12
}

$parsed = ConvertFrom-TernStatus -Lines @('status: migration(s) pending', 'version:  12 of 13', 'host:     127.0.0.1', 'database: todo_dev')
Assert-GuardPass 'parses verified Tern status output' {
    if ($parsed.Current -ne 12 -or $parsed.Maximum -ne 13) { throw 'Tern version parse mismatch.' }
}
Assert-GuardReject 'Tern status for a different database' {
    ConvertFrom-TernStatus -Lines @('status: up to date', 'version:  13 of 13', 'host:     127.0.0.1', 'database: todo_test')
}
Assert-GuardReject 'contradictory Tern status and version' {
    ConvertFrom-TernStatus -Lines @('status: up to date', 'version:  12 of 13', 'host:     127.0.0.1', 'database: todo_dev')
}

Write-Output "Development migration guard checks passed: $checks."
