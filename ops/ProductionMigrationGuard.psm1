Set-StrictMode -Version Latest

function ConvertFrom-StrictProductionDatabaseUrl {
    param([Parameter(Mandatory)][string]$Value)

    $pattern = '^(?<scheme>postgres|postgresql)://(?<user>[^:@/?#]+):(?<password>[^@/?#]+)@(?<host>[^:/?#]+):(?<port>[0-9]+)/(?<database>[A-Za-z0-9_-]+)(?<query>\?[^#]*)?$'
    if ($Value -cnotmatch $pattern -or $Value -match '[\x00-\x20\\#]' -or $Value -match '%(?![0-9A-Fa-f]{2})') {
        throw 'Production DATABASE URL has an unsupported or ambiguous form.'
    }

    $url = $Matches.Clone()
    $user = [Uri]::UnescapeDataString($url.user)
    $password = [Uri]::UnescapeDataString($url.password)
    $hostName = $url.host.ToLowerInvariant()
    $database = [Uri]::UnescapeDataString($url.database)
    $port = 0
    if (-not [int]::TryParse($url.port, [ref]$port)) {
        throw 'Production DATABASE URL port is invalid.'
    }
    if ($hostName -cnotmatch '^ep-[a-z0-9-]+\.c-[0-9]+\.eu-central-1\.aws\.neon\.tech$' -or
        $hostName -match 'pooler' -or $port -ne 5432 -or $database -cne 'neondb' -or
        $user -cne 'neondb_owner' -or [string]::IsNullOrWhiteSpace($password) -or
        $password -match '[\x00\r\n]') {
        throw 'Production DATABASE URL does not identify the approved direct owner target.'
    }

    $parameters = @{}
    $query = $url.query
    if ($query) {
        foreach ($pair in $query.Substring(1).Split('&')) {
            if (-not $pair -or $pair -notmatch '^([^=]+)=([^=]*)$') {
                throw 'Production DATABASE URL query is malformed.'
            }
            $key = [Uri]::UnescapeDataString($Matches[1])
            $parameterValue = [Uri]::UnescapeDataString($Matches[2])
            if ($Matches[1] -cne $key -or $key -cnotin @('sslmode', 'channel_binding') -or
                $parameters.ContainsKey($key)) {
                throw 'Production DATABASE URL has an unsupported, duplicate, or non-canonical option.'
            }
            $parameters[$key] = $parameterValue
        }
    }
    if ($parameters.Count -lt 1 -or $parameters['sslmode'] -cne 'verify-full' -or
        ($parameters.ContainsKey('channel_binding') -and $parameters['channel_binding'] -cne 'require')) {
        throw 'Production DATABASE URL must use sslmode=verify-full and only approved TLS options.'
    }

    return [pscustomobject]@{
        Host = $hostName
        Port = $port
        Database = $database
        User = $user
        Password = $password
        SSLMode = 'verify-full'
    }
}

function Assert-ProductionMigrationConfiguration {
    param(
        [Parameter(Mandatory)][hashtable]$Environment,
        [Parameter(Mandatory)][string]$TernConfigText,
        [Parameter(Mandatory)][string]$Action,
        [int]$ExpectedCurrentVersion = -1,
        [Parameter(Mandatory)][int]$ExpectedTargetVersion,
        [switch]$AuthorizeProductionMigration
    )

    if (-not $Environment.ContainsKey('PRODUCTION_DATABASE_URL') -or
        [string]::IsNullOrWhiteSpace([string]$Environment.PRODUCTION_DATABASE_URL)) {
        throw 'PRODUCTION_DATABASE_URL is required in the process environment.'
    }
    foreach ($name in $Environment.Keys) {
        if ($name -match '^PG[A-Z0-9_]*$') {
            throw 'Production migration environment cannot contain libpq override variables.'
        }
    }

    $target = ConvertFrom-StrictProductionDatabaseUrl ([string]$Environment.PRODUCTION_DATABASE_URL)
    $configValues = @{}
    $insideDatabase = $false
    foreach ($line in ($TernConfigText -split "`r?`n")) {
        $trimmed = $line.Trim()
        if (-not $trimmed -or $trimmed.StartsWith('#')) { continue }
        if ($trimmed -match '^\[(?<section>[A-Za-z0-9_]+)\]$') {
            if ($Matches.section -cne 'database' -or $insideDatabase) {
                throw 'Production Tern configuration contains an unsupported section.'
            }
            $insideDatabase = $true
            continue
        }
        if (-not $insideDatabase -or $trimmed -notmatch '^(?<key>[a-z_]+)\s*=\s*(?<value>.+?)\s*$') {
            throw 'Production Tern configuration contains an unsupported setting.'
        }
        $key = $Matches.key
        if ($configValues.ContainsKey($key)) { throw 'Production Tern configuration contains duplicate settings.' }
        $configValues[$key] = $Matches.value
    }

    $expectedConfig = @{
        host = '{{ env "PRODUCTION_POSTGRES_HOST" }}'
        port = '{{ env "PRODUCTION_POSTGRES_PORT" }}'
        database = '{{ env "PRODUCTION_POSTGRES_DB" }}'
        user = '{{ env "PRODUCTION_POSTGRES_USER" }}'
        password = '{{ env "PRODUCTION_POSTGRES_PASSWORD" }}'
        sslmode = 'verify-full'
    }
    if ($configValues.Count -ne $expectedConfig.Count) {
        throw 'Production Tern configuration has unexpected connection settings.'
    }
    foreach ($key in $expectedConfig.Keys) {
        if (-not $configValues.ContainsKey($key) -or $configValues[$key] -cne $expectedConfig[$key]) {
            throw 'Production Tern configuration does not match the guarded target.'
        }
    }

    if ($ExpectedTargetVersion -ne 15 -or
        ($ExpectedCurrentVersion -ge 0 -and $ExpectedCurrentVersion -gt $ExpectedTargetVersion)) {
        throw 'Expected production migration versions are invalid.'
    }
    if ($Action -ceq 'migrate') {
        if (-not $AuthorizeProductionMigration) { throw 'Production migration requires -AuthorizeProductionMigration.' }
        if ($ExpectedCurrentVersion -ne 0 -or $ExpectedTargetVersion -ne 15) {
            throw 'The production bootstrap migration must explicitly advance version 0 to version 15.'
        }
    }
    elseif ($Action -cne 'status') {
        throw 'Unsupported production migration action.'
    }
    return $target
}

function ConvertFrom-ProductionTernStatus {
    param([Parameter(Mandatory)][string[]]$Lines, [Parameter(Mandatory)][string]$ExpectedHost)
    $text = $Lines -join "`n"
    if ($text -notmatch '(?m)^version:\s*(?<current>[0-9]+)\s+of\s+(?<maximum>[0-9]+)\s*$') {
        throw 'Tern status did not report a parseable migration version.'
    }
    $current = [int]$Matches.current
    $maximum = [int]$Matches.maximum
    if ($text -notmatch '(?m)^status:\s*(?<status>.+?)\s*$') {
        throw 'Tern status did not report a parseable status value.'
    }
    $status = $Matches.status
    if (($current -lt $maximum -and $status -notmatch 'pending') -or
        ($current -eq $maximum -and $status -cne 'up to date') -or $current -gt $maximum) {
        throw 'Tern status text and recorded migration version disagree.'
    }
    if ($text -notmatch '(?m)^database:\s*neondb\s*$' -or
        $text -notmatch ('(?m)^host:\s*' + [Regex]::Escape($ExpectedHost) + '\s*$')) {
        throw 'Tern status did not confirm the guarded production database target.'
    }
    return [pscustomobject]@{ Current = $current; Maximum = $maximum; Status = $status; Text = $text }
}

Export-ModuleMember -Function ConvertFrom-StrictProductionDatabaseUrl, Assert-ProductionMigrationConfiguration, ConvertFrom-ProductionTernStatus
