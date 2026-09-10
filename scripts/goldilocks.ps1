# Thin Windows launcher. The Go child inherits the original standard handles.
$ErrorActionPreference = 'Stop'
$exitCode = 1
try {
    $env:PLUGIN_ROOT = Split-Path $PSScriptRoot -Parent
    $version = (Get-Content -Raw (Join-Path $env:PLUGIN_ROOT '.codex-plugin/plugin.json') | ConvertFrom-Json).version
    if ($version -notmatch '^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$') { throw 'missing or invalid plugin version' }
    $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    if ($arch -notin @('AMD64', 'ARM64')) { throw "unsupported Windows architecture: $arch" }
    $platform = "Windows-$arch"
    $asset = "goldilocks-$platform.exe"
    $checksums = @(Get-Content (Join-Path $PSScriptRoot 'SHA256SUMS') | Where-Object { ($_ -split '\s+')[1] -eq $asset })
    if ($checksums.Count -ne 1) { throw "missing or duplicate checksum for $asset" }
    $expected = ($checksums[0] -split '\s+')[0]
    if ($expected -cnotmatch '^[0-9a-f]{64}$') { throw "invalid checksum for $asset" }
    $data = if ($env:PLUGIN_DATA) { $env:PLUGIN_DATA } else { Join-Path $env:LOCALAPPDATA 'goldilocks' }
    $cache = Join-Path $data "bin/$version/$platform"
    $binary = Join-Path $cache 'goldilocks.exe'
    function Test-CachedBinary {
        (Test-Path -LiteralPath $binary -PathType Leaf) -and ((Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash -eq $expected)
    }
    if (-not (Test-CachedBinary)) {
        [void][IO.Directory]::CreateDirectory($cache)
        $lock = $null
        $deadline = [DateTime]::UtcNow.AddSeconds(75)
        try {
            while (-not $lock) {
                try { $lock = [IO.File]::Open((Join-Path $cache 'download.lock'), 'OpenOrCreate', 'ReadWrite', 'None') }
                catch [IO.IOException] {
                    if ([DateTime]::UtcNow -ge $deadline) { throw "download lock timed out: $cache" }
                    Start-Sleep -Milliseconds 200
                }
            }
            if (-not (Test-CachedBinary)) {
                $temporary = Join-Path $cache 'download.tmp'
                try {
                    [Console]::Error.WriteLine("goldilocks: downloading $version for $platform")
                    # curl.exe is included in supported modern Windows versions.
                    & curl.exe --fail --location --silent --show-error --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 60 --output $temporary "https://github.com/baranwang/goldilocks/releases/download/v$version/$asset"
                    if ($LASTEXITCODE -ne 0) { throw "download failed for v$version/$asset; retry when the release and network are available" }
                    if ((Get-FileHash -LiteralPath $temporary -Algorithm SHA256).Hash -ne $expected) { throw "SHA-256 mismatch for $asset" }
                    if (Test-Path -LiteralPath $binary) { [IO.File]::Replace($temporary, $binary, $null) }
                    else { [IO.File]::Move($temporary, $binary) }
                } finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force } }
            }
        } finally { if ($lock) { $lock.Dispose() } }
    }
    $start = New-Object Diagnostics.ProcessStartInfo
    $start.FileName = $binary
    $start.UseShellExecute = $false
    # Windows native argument quoting, including empty args and trailing slashes.
    $start.Arguments = (($args | ForEach-Object { '"' + (($_ -replace '(\\*)"', '$1$1\"') -replace '(\\+)$', '$1$1') + '"' }) -join ' ')
    $child = [Diagnostics.Process]::Start($start)
    $child.WaitForExit()
    $exitCode = $child.ExitCode
    $child.Dispose()
} catch { [Console]::Error.WriteLine("goldilocks launcher: $_") }
exit $exitCode
