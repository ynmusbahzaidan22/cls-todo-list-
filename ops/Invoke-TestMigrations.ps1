[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('status', 'migrate')]
    [string]$Action
)

$ErrorActionPreference = 'Stop'

function Get-PostgresTarget {
    param(
        [Parameter(Mandatory = $true)][string]$ConnectionUrl,
        [Parameter(Mandatory = $true)][bool]$RequireTestName
    )

    if ($ConnectionUrl -ne $ConnectionUrl.Trim() -or $ConnectionUrl -match '[\x00-\x1f\\#]' -or $ConnectionUrl -match '\s' -or $ConnectionUrl -match '%(?![0-9a-fA-F]{2})') {
        throw 'Database URL has an unsupported or ambiguous form.'
    }
    if ($ConnectionUrl -cnotmatch '^(postgres|postgresql)://([^/?#]+)(/[^/?#]+)(?:\?(.*))?$') {
        throw 'Database URL must be an explicit PostgreSQL URL with host and database.'
    }
    $authority = $Matches[2]
    $escapedPath = $Matches[3]
    $query = $Matches[4]
    if ($query -and $query -cnotmatch '^sslmode=(disable|allow|prefer|require|verify-ca|verify-full)$') {
        throw 'Database URL contains unsupported connection options.'
    }
    if ($escapedPath -match '%' -or $escapedPath -notmatch '^/[A-Za-z0-9_-]+$') {
        throw 'Database URL database name has an unsupported form.'
    }
    if ($authority -notmatch '^([^@]+)@([^@]+)$') {
        throw 'Database URL must include an explicit database user.'
    }
    $userInfo = $Matches[1]
    $hostPort = $Matches[2]
    if ($hostPort.Contains(',') -or $hostPort.Contains('%')) {
        throw 'Database URL host must be a single explicit host.'
    }
    $rawPort = ''
    if ($hostPort -match '^\[([0-9A-Fa-f:]+)\](?::([0-9]+))?$') {
        $rawHost = $Matches[1].ToLowerInvariant()
        $rawPort = $Matches[2]
        $parsedIP = $null
        if (-not [Net.IPAddress]::TryParse($rawHost, [ref]$parsedIP) -or $parsedIP.AddressFamily -ne [Net.Sockets.AddressFamily]::InterNetworkV6) {
            throw 'Database URL IPv6 host is invalid.'
        }
    }
    elseif ($hostPort -match '^([A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?)(?::([0-9]+))?$') {
        $rawHost = $Matches[1].ToLowerInvariant()
        $rawPort = $Matches[2]
        if ($rawHost.Contains('..')) {
            throw 'Database URL host is invalid.'
        }
    }
    else {
        throw 'Database URL host or port has an unsupported form.'
    }
    if ($rawPort -and $rawPort -notmatch '^(?:[1-9][0-9]{0,4})$') {
        throw 'Database URL port is invalid.'
    }
    $uri = $null
    if (-not [Uri]::TryCreate($ConnectionUrl, [UriKind]::Absolute, [ref]$uri) -or
        $uri.Scheme -notin @('postgres', 'postgresql') -or $uri.Fragment -or -not $uri.Host) {
        throw 'Database URL is invalid.'
    }
    $uriHost = $uri.DnsSafeHost.ToLowerInvariant()
    $rawHostIP = $null
    $uriHostIP = $null
    $hostsMatch = if ([Net.IPAddress]::TryParse($rawHost, [ref]$rawHostIP) -and [Net.IPAddress]::TryParse($uriHost, [ref]$uriHostIP)) {
        $rawHostIP.Equals($uriHostIP)
    }
    else {
        $uriHost -eq $rawHost
    }
    if (-not $hostsMatch) {
        throw 'Database URL host is ambiguous.'
    }
    $port = if ($rawPort) { [int]$rawPort } else { 5432 }
    if ($port -lt 1 -or $port -gt 65535) {
        throw 'Database URL port is invalid.'
    }
    $credentialParts = $userInfo.Split(':', 2)
    $user = [Uri]::UnescapeDataString($credentialParts[0])
    $password = if ($credentialParts.Count -eq 2) { [Uri]::UnescapeDataString($credentialParts[1]) } else { '' }
    if (-not $user -or $user -match '[\x00\r\n]' -or $password -match '[\x00\r\n]') {
        throw 'Database URL credentials have an unsupported form.'
    }
    $database = $uri.AbsolutePath.TrimStart('/')
    if ($database -ne $escapedPath.TrimStart('/')) {
        throw 'Database URL database name is ambiguous.'
    }

    $parsedHost = $rawHost
    if ($RequireTestName) {
        $isLoopback = $parsedHost -in @('localhost', '127.0.0.1', '::1')
        if (-not $isLoopback) {
            throw 'TEST_DATABASE_URL must use a loopback PostgreSQL host.'
        }
        if ($port -ne 25432) {
            throw 'TEST_DATABASE_URL must use the isolated test PostgreSQL port 25432.'
        }
        if ($database -cne 'todo_test') {
            throw 'TEST_DATABASE_URL must use the dedicated todo_test database.'
        }
    }

    $canonicalHost = $parsedHost
    $canonicalIP = $null
    if ($parsedHost -eq 'localhost' -or ([Net.IPAddress]::TryParse($parsedHost, [ref]$canonicalIP) -and [Net.IPAddress]::IsLoopback($canonicalIP))) {
        $canonicalHost = '<loopback>'
    }
    return [pscustomobject]@{
        Host = $canonicalHost
        ActualHost = $rawHost
        Port = $port
        Database = $database
        User = $user
        Password = $password
        SSLMode = if ($query) { $query.Substring('sslmode='.Length) } else { 'prefer' }
    }
}

if ([string]::IsNullOrWhiteSpace($env:TEST_DATABASE_URL)) {
    throw 'TEST_DATABASE_URL must be set to a dedicated disposable test database.'
}
$testTarget = Get-PostgresTarget -ConnectionUrl $env:TEST_DATABASE_URL -RequireTestName $true
if (-not [string]::IsNullOrWhiteSpace($env:DATABASE_URL)) {
    $developmentTarget = Get-PostgresTarget -ConnectionUrl $env:DATABASE_URL -RequireTestName $false
    if ($testTarget.Host -eq $developmentTarget.Host -and
        $testTarget.Port -eq $developmentTarget.Port -and
        $testTarget.Database -ieq $developmentTarget.Database) {
        throw 'TEST_DATABASE_URL must identify a different database from DATABASE_URL.'
    }
}

# Tern reads PostgreSQL connection values from process-only variables; the URL
# and credentials are not written to a config file or command-line arguments.
$pgVariableNames = @('PGHOST', 'PGPORT', 'PGDATABASE', 'PGUSER', 'PGPASSWORD', 'PGSSLMODE')
$previousPgValues = @{}
foreach ($name in $pgVariableNames) {
    $previousPgValues[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$env:PGHOST = $testTarget.ActualHost
$env:PGPORT = [string]$testTarget.Port
$env:PGDATABASE = $testTarget.Database
$env:PGUSER = $testTarget.User
$env:PGPASSWORD = $testTarget.Password
$env:PGSSLMODE = $testTarget.SSLMode

try {
    & tern $Action --config tern.test.conf --migrations migrations
    if ($LASTEXITCODE -ne 0) {
        throw "Tern $Action failed with exit code $LASTEXITCODE."
    }
}
finally {
    foreach ($name in $pgVariableNames) {
        if ($null -eq $previousPgValues[$name]) {
            Remove-Item "Env:$name" -ErrorAction SilentlyContinue
        }
        else {
            [Environment]::SetEnvironmentVariable($name, $previousPgValues[$name], 'Process')
        }
    }
}
