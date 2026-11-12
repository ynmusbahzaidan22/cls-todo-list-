$ErrorActionPreference = 'Stop'

$opsRoot = $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($opsRoot)) { $opsRoot = Join-Path (Get-Location).Path 'ops' }
$provisionPath = Join-Path $opsRoot 'provision_runtime_role.sql'
$grantPath = Join-Path $opsRoot 'grant_runtime_privileges.sql'
$provision = Get-Content -LiteralPath $provisionPath -Raw
$grant = Get-Content -LiteralPath $grantPath -Raw
$checks = 0

function Assert-Contains {
    param([string]$Text, [string]$Pattern, [string]$Name)
    if ($Text -notmatch $Pattern) { throw "Runtime-role script contract failed: $Name" }
    $script:checks++
    Write-Output "PASS $Name"
}

function Assert-NotContains {
    param([string]$Text, [string]$Pattern, [string]$Name)
    if ($Text -match $Pattern) { throw "Runtime-role script contract failed: $Name" }
    $script:checks++
    Write-Output "PASS $Name"
}

Assert-Contains $provision '(?m)^\\set ON_ERROR_STOP on\s+BEGIN;' 'provisioning stops on error inside a transaction'
Assert-Contains $provision '(?m)^COMMIT;\s*$' 'provisioning commits only after all statements'
Assert-Contains $provision 'CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS' 'new role is created with restricted attributes'
Assert-Contains $provision 'runtime_role_invalid' 'existing role attributes and memberships are validated'
Assert-NotContains $provision '(?im)^\s*\\password\b' 'password management stays out of SQL provisioning scripts'
Assert-NotContains $provision '(?im)^\s*ALTER ROLE .*\bNOSUPERUSER\b' 'provisioning does not redundantly alter managed superuser attributes'
Assert-NotContains $provision '(?im)^\s*\\prompt\b' 'provisioning does not use an unmasked generic prompt'

Assert-Contains $grant '(?m)^\\set ON_ERROR_STOP on\s+BEGIN;' 'grants stop on error inside a transaction'
Assert-Contains $grant '(?m)^COMMIT;\s*$' 'grants commit only after all statements'
Assert-Contains $grant 'runtime_role_invalid' 'grant script validates existing role attributes and memberships'
Assert-NotContains $grant '(?im)^\s*ALTER ROLE .*\bNOSUPERUSER\b' 'grant script does not attempt managed role attribute changes'
Assert-Contains $grant 'GRANT UPDATE \(time_zone, role, status, updated_at\) ON app\.users' 'users updates remain column restricted'
Assert-Contains $grant 'GRANT SELECT, INSERT ON app\.task_template_applications, app\.task_template_application_items' 'tracking tables remain select and insert only'
Assert-Contains $grant 'REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA app FROM PUBLIC' 'public app function execution is revoked'

Write-Output "Runtime-role script checks passed: $checks."
