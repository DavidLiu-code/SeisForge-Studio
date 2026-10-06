param(
    [string]$Output = "SeisForgeStudio_v1.10.3_azimuth_wiggle_x64.exe"
)

$ErrorActionPreference = "Stop"
$repoPath = (Resolve-Path -LiteralPath $PSScriptRoot).Path
$outputPath = if ([System.IO.Path]::IsPathRooted($Output)) { $Output } else { Join-Path $repoPath $Output }

$goCandidates = @(
    (Join-Path $env:ProgramFiles "Go\bin\go.exe"),
    "C:\Go\bin\go.exe"
)
$goExe = $null
foreach ($candidate in $goCandidates) {
    if ($candidate -and (Test-Path -LiteralPath $candidate)) {
        $goExe = $candidate
        break
    }
}
if (-not $goExe) {
    $command = Get-Command go.exe -ErrorAction SilentlyContinue
    if ($command) { $goExe = $command.Source }
}
if (-not $goExe) { throw "Go toolchain was not found." }

Push-Location $repoPath
try {
    # Keep release verification independent of a locked or ACL-restricted
    # user-wide Go build cache. The repository ignores this temporary cache.
    $releaseGoCache = Join-Path $repoPath ".tmp-go-cache"
    $env:GOCACHE = $releaseGoCache
    & $goExe test ./... -count=1
    if ($LASTEXITCODE -ne 0) { throw "go test failed with exit code $LASTEXITCODE" }

    & $goExe build -trimpath '-ldflags=-s -w -H=windowsgui' -o $outputPath ./cmd/limage
    if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
} finally {
    Pop-Location
}

$bytes = [System.IO.File]::ReadAllBytes($outputPath)
if ($bytes.Length -lt 512) { throw "Built executable is too small to be a valid PE image." }
$peOffset = [BitConverter]::ToInt32($bytes, 0x3c)
if ($peOffset -lt 0 -or $peOffset + 96 -ge $bytes.Length) { throw "Invalid PE header offset." }
if ($bytes[$peOffset] -ne 0x50 -or $bytes[$peOffset + 1] -ne 0x45) { throw "Built file has no PE signature." }
$optionalHeader = $peOffset + 24
$subsystem = [BitConverter]::ToUInt16($bytes, $optionalHeader + 68)
if ($subsystem -ne 2) {
    throw "PE subsystem is $subsystem; expected 2 (Windows GUI)."
}

$hash = (Get-FileHash -LiteralPath $outputPath -Algorithm SHA256).Hash.ToLowerInvariant()
[pscustomobject]@{
    Executable = $outputPath
    Bytes = (Get-Item -LiteralPath $outputPath).Length
    PESubsystem = "Windows GUI (2)"
    SHA256 = $hash
}
