Set-StrictMode -Version Latest

function Read-StrictEnvironmentFile {
    param([Parameter(Mandatory)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw 'Development environment file is missing.'
    }

    $values = @{}
    foreach ($line in [System.IO.File]::ReadAllLines($Path)) {
        if ([string]::IsNullOrWhiteSpace($line) -or $line -match '^\s*#') { continue }
        if ($line -notmatch '^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$') {
            throw 'Development environment file contains an unsupported line.'
        }
        $name = $Matches[1]
        $value = $Matches[2].Trim()
        if ($values.ContainsKey($name)) { throw 'Development environment file contains a duplicate setting.' }
        if (($value.StartsWith('"') -and $value.EndsWith('"')) -or ($value.StartsWith("'") -and $value.EndsWith("'"))) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        if ($value -match '[\x00\r\n]') { throw 'Development environment file contains an invalid setting.' }
        $values[$name] = $value
    }
    return $values
}

function ConvertFrom-StrictDevelopmentDatabaseUrl {
    param([Parameter(Mandatory)][string]$Value)

    $pattern = '^(?:postgres|postgresql)://(?<user>[^:@/?#]+):(?<password>[^@/?#]*)@(?<host>[^:/?#]+):(?<port>[0-9]+)/(?<database>[A-Za-z0-9_-]+)(?<query>\?[^#]*)?$'
    if ($Value -cnotmatch $pattern -or $Value -match '[\x00-\x20\\#]' -or $Value -match '%(?![0-9A-Fa-f]{2})') {
        throw 'Development DATABASE_URL has an unsupported or ambiguous form.'
    }

    $urlMatch = $Matches.Clone()
    $query = $urlMatch.query
    if ($query -and $query -cnotmatch '^\?sslmode=disable$') {
        throw 'Development DATABASE_URL contains an unsupported connection option.'
    }
    $user = [Uri]::UnescapeDataString($urlMatch.user)
    $password = [Uri]::UnescapeDataString($urlMatch.password)
    $target = [pscustomobject]@{
        Host = $urlMatch.host.ToLowerInvariant()
        Port = [int]$urlMatch.port
        Database = $urlMatch.database
        User = $user
        Password = $password
    }
    if ($target.Host -cne '127.0.0.1' -or $target.Port -ne 15432 -or $target.Database -cne 'todo_dev' -or
        [string]::IsNullOrWhiteSpace($target.User) -or [string]::IsNullOrWhiteSpace($target.Password)) {
        throw 'Development DATABASE_URL must identify only 127.0.0.1:15432/todo_dev with explicit credentials.'
    }
    return $target
}

function Assert-DevelopmentMigrationConfiguration {
    param(
        [Parameter(Mandatory)][hashtable]$Environment,
        [Parameter(Mandatory)][string]$TernConfigText,
        [Parameter(Mandatory)][string]$Action,
        [int]$ExpectedCurrentVersion = -1,
        [Parameter(Mandatory)][int]$ExpectedTargetVersion,
        [switch]$AuthorizeDevelopmentMigration
    )

    foreach ($name in @('DATABASE_URL', 'POSTGRES_DB', 'POSTGRES_USER', 'POSTGRES_PASSWORD')) {
        if (-not $Environment.ContainsKey($name) -or [string]::IsNullOrWhiteSpace([string]$Environment[$name])) {
            throw "Required development setting is missing: $name."
        }
    }
    foreach ($name in $Environment.Keys) {
        if ($name -match '^PG[A-Z0-9_]*$') { throw 'Development environment file must not define libpq override variables.' }
    }
    $target = ConvertFrom-StrictDevelopmentDatabaseUrl ([string]$Environment.DATABASE_URL)
    if ([string]$Environment.POSTGRES_DB -cne 'todo_dev' -or
        [string]$Environment.POSTGRES_USER -notmatch '^[A-Za-z_][A-Za-z0-9_$]*$') {
        throw 'Development migration account or database name is invalid.'
    }
    if (($Environment.ContainsKey('POSTGRES_HOST') -and [string]$Environment.POSTGRES_HOST -cne '127.0.0.1') -or
        ($Environment.ContainsKey('POSTGRES_PORT') -and [string]$Environment.POSTGRES_PORT -cne '15432')) {
        throw 'Optional development host or port settings conflict with the guarded target.'
    }

    $configValues = @{}
    $insideDatabase = $false
    foreach ($line in ($TernConfigText -split "`r?`n")) {
        $trimmed = $line.Trim()
        if (-not $trimmed -or $trimmed.StartsWith('#')) { continue }
        if ($trimmed -match '^\[(?<section>[A-Za-z0-9_]+)\]$') {
            $insideDatabase = $Matches.section -ceq 'database'
            continue
        }
        if (-not $insideDatabase -or $trimmed -notmatch '^(?<key>[a-z_]+)\s*=\s*(?<value>.+?)\s*$') {
            throw 'Tern configuration contains an unsupported setting.'
        }
        $key = $Matches.key
        if ($configValues.ContainsKey($key)) { throw 'Tern configuration contains a duplicate setting.' }
        $configValues[$key] = $Matches.value
    }
    $expectedConfig = @{
        host = '127.0.0.1'
        port = '15432'
        database = '{{ env "POSTGRES_DB" }}'
        user = '{{ env "POSTGRES_USER" }}'
        password = '{{ env "POSTGRES_PASSWORD" }}'
        sslmode = 'disable'
    }
    if ($configValues.Count -ne $expectedConfig.Count) { throw 'Tern development database configuration has unexpected connection settings.' }
    foreach ($key in $expectedConfig.Keys) {
        if (-not $configValues.ContainsKey($key) -or $configValues[$key] -cne $expectedConfig[$key]) {
            throw 'Tern development database configuration does not match the guarded target.'
        }
    }
    if ($target.Database -cne [string]$Environment.POSTGRES_DB) { throw 'Development URL and Tern database names do not match.' }

    if ($ExpectedTargetVersion -lt 1 -or ($ExpectedCurrentVersion -ge 0 -and $ExpectedCurrentVersion -gt $ExpectedTargetVersion)) {
        throw 'Expected migration versions are invalid.'
    }
    if ($Action -ceq 'migrate') {
        if (-not $AuthorizeDevelopmentMigration) { throw 'Migration requires -AuthorizeDevelopmentMigration.' }
        if ($ExpectedCurrentVersion -lt 0) { throw 'Migration requires an explicit expected current version.' }
        if ($ExpectedTargetVersion -ne ($ExpectedCurrentVersion + 1)) {
            throw 'A development migration may advance exactly one explicitly expected version.'
        }
    }
    elseif ($Action -cne 'status') {
        throw 'Unsupported development migration action.'
    }
    return $target
}

function Get-NumberedMigrationInventory {
    param([Parameter(Mandatory)][string]$MigrationDirectory)

    $files = @(Get-ChildItem -LiteralPath $MigrationDirectory -File)
    if ($files.Count -eq 0) { throw 'Migration directory is empty.' }
    $versions = @()
    foreach ($file in $files) {
        if ($file.Name -cnotmatch '^(?<version>[0-9]{3})_[A-Za-z0-9_-]+\.sql$') {
            throw 'Migration directory contains an unrecognized file.'
        }
        $versions += [int]$Matches.version
    }
    $ordered = @($versions | Sort-Object -Unique)
    if ($ordered.Count -ne $files.Count) { throw 'Migration directory contains duplicate versions.' }
    for ($i = 0; $i -lt $ordered.Count; $i++) {
        if ($ordered[$i] -ne ($i + 1)) { throw 'Migration versions are not contiguous from 001.' }
    }
    return $ordered
}

function Assert-ExpectedMigrationInventory {
    param([Parameter(Mandatory)][int[]]$Versions, [Parameter(Mandatory)][int]$ExpectedTargetVersion)
    if ($Versions[-1] -ne $ExpectedTargetVersion) {
        throw 'Expected target version does not match the repository migration maximum.'
    }
}

function ConvertFrom-TernStatus {
    param([Parameter(Mandatory)][string[]]$Lines)
    $text = $Lines -join "`n"
    if ($text -notmatch '(?m)^version:\s*(?<current>[0-9]+)\s+of\s+(?<maximum>[0-9]+)\s*$') {
        throw 'Tern status did not report a parseable migration version.'
    }
    $current = [int]$Matches.current
    $maximum = [int]$Matches.maximum
    if ($text -notmatch '(?m)^status:\s*(?<status>.+?)\s*$') {
        throw 'Tern status did not report a parseable status value.'
    }
    $reportedStatus = $Matches.status
    if (($current -lt $maximum -and $reportedStatus -notmatch 'pending') -or
        ($current -eq $maximum -and $reportedStatus -cne 'up to date') -or $current -gt $maximum) {
        throw 'Tern status text and recorded migration version disagree.'
    }
    if ($text -notmatch '(?m)^database:\s*todo_dev\s*$' -or $text -notmatch '(?m)^host:\s*127\.0\.0\.1\s*$') {
        throw 'Tern status did not confirm the guarded development target.'
    }
    return [pscustomobject]@{ Current = $current; Maximum = $maximum; Status = $reportedStatus; Text = $text }
}

Export-ModuleMember -Function Read-StrictEnvironmentFile, ConvertFrom-StrictDevelopmentDatabaseUrl, Assert-DevelopmentMigrationConfiguration, Get-NumberedMigrationInventory, Assert-ExpectedMigrationInventory, ConvertFrom-TernStatus
