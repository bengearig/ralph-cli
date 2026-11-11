param(
    [string]$Version = $env:RALPH_VERSION,
    [string]$InstallDir = $env:RALPH_INSTALL_DIR
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$TagPattern = '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'

function Test-Tag([string]$Tag) {
    return ($Tag -cmatch $TagPattern) -and ($Tag -notmatch '\s')
}

function Get-LatestTag([string]$Base) {
    $url = "$Base/releases/latest"
    $location = $null
    try {
        $request = [System.Net.HttpWebRequest]::Create($url)
        $request.Method = 'HEAD'
        $request.AllowAutoRedirect = $false
        $response = $request.GetResponse()
        try {
            $location = $response.Headers['Location']
        } finally {
            $response.Dispose()
        }
    } catch {
        throw "cannot look up the latest release at ${url}: $($_.Exception.Message); pass -Version (or set RALPH_VERSION) to choose a release"
    }
    if (-not $location) {
        throw "no release found at $url"
    }
    $path = ($location -split '[?#]')[0].TrimEnd('/')
    $tag = $path.Substring($path.LastIndexOf('/') + 1)
    if (-not (Test-Tag $tag)) {
        throw "no release found at $url"
    }
    return $tag
}

function Install-Ralph {
    if ($Version -and -not (Test-Tag $Version)) {
        throw "invalid version ""$Version"""
    }

    $dir = $InstallDir
    if (-not $dir) {
        if (-not $env:LOCALAPPDATA) {
            throw 'LOCALAPPDATA is not set; use -InstallDir'
        }
        $dir = Join-Path $env:LOCALAPPDATA 'Programs\ralph'
    }
    if ($dir.Length -gt 3) {
        $dir = $dir.TrimEnd('\', '/')
    }

    $base = $env:RALPH_RELEASES_URL
    if (-not $base) {
        $base = 'https://github.com/bengearig/ralph-cli'
    }
    $base = $base.TrimEnd('/')

    $machine = $env:PROCESSOR_ARCHITEW6432
    if (-not $machine) {
        $machine = $env:PROCESSOR_ARCHITECTURE
    }
    switch ($machine) {
        'AMD64' { $arch = 'amd64' }
        'ARM64' { $arch = 'arm64' }
        default { throw "unsupported architecture: $machine" }
    }

    $tag = $Version
    if (-not $tag) {
        $tag = Get-LatestTag $base
    }

    $name = "ralph_${tag}_windows_${arch}.zip"
    $release = "$base/releases/download/$tag"
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('ralph-install-' + [Guid]::NewGuid().ToString('N'))
    $stage = $null

    try {
        New-Item -ItemType Directory -Path $tmp | Out-Null
        $archive = Join-Path $tmp $name
        $sums = Join-Path $tmp 'checksums.txt'

        $ProgressPreference = 'SilentlyContinue'
        foreach ($item in @(@($name, $archive), @('checksums.txt', $sums))) {
            try {
                Invoke-WebRequest -UseBasicParsing -Uri "$release/$($item[0])" -OutFile $item[1]
            } catch {
                throw "cannot download $release/$($item[0]): $($_.Exception.Message)"
            }
        }

        $want = $null
        foreach ($line in (Get-Content -LiteralPath $sums)) {
            if ($line -match '^([0-9A-Fa-f]{64}) [ *](\S+)\s*$' -and $Matches[2] -eq $name) {
                $want = $Matches[1]
                break
            }
        }
        if (-not $want) {
            throw "no checksum listed for $name"
        }
        $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash
        if ($got -ine $want) {
            throw "checksum mismatch for $name"
        }

        $extract = Join-Path $tmp 'extract'
        Expand-Archive -LiteralPath $archive -DestinationPath $extract
        $binary = Join-Path $extract 'ralph.exe'
        if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) {
            throw "$name does not contain ralph.exe"
        }

        New-Item -ItemType Directory -Force -Path $dir | Out-Null
        $target = Join-Path $dir 'ralph.exe'
        if (Test-Path -LiteralPath $target -PathType Container) {
            throw "$target is a directory"
        }
        $stage = Join-Path $dir ('.ralph-' + [Guid]::NewGuid().ToString('N') + '.tmp')
        Copy-Item -LiteralPath $binary -Destination $stage
        try {
            Move-Item -LiteralPath $stage -Destination $target -Force
            $stage = $null
        } catch {
            throw "cannot replace $target, which may be in use: close any running ralph and run ""ralph update"", or retry after stopping ralph ($($_.Exception.Message))"
        }
    } finally {
        if ($stage -and (Test-Path -LiteralPath $stage)) {
            Remove-Item -LiteralPath $stage -Force -ErrorAction SilentlyContinue
        }
        if (Test-Path -LiteralPath $tmp) {
            Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
        }
    }

    Write-Host "installed ralph $tag to $target"

    $want = $dir.TrimEnd('\')
    $onPath = $false
    foreach ($entry in ($env:Path -split ';')) {
        if ($entry.Trim().TrimEnd('\') -ieq $want) {
            $onPath = $true
            break
        }
    }
    if (-not $onPath) {
        $quoted = $dir.Replace("'", "''")
        Write-Host ''
        Write-Host "$dir is not on your PATH. Add it for your user account:"
        Write-Host "  [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ';$quoted', 'User')"
        Write-Host 'Then open a new terminal.'
    }
}

$runAsFile = $false
try { $runAsFile = [bool]$MyInvocation.MyCommand.Path } catch { }

try {
    Install-Ralph
} catch {
    [Console]::Error.WriteLine("error: $($_.Exception.Message)")
    if ($runAsFile) {
        exit 1
    }
    throw
}
