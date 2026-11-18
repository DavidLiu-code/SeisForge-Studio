[CmdletBinding()]
param(
    [string]$Repository = '',
    [string[]]$HistoryRefs = @('HEAD', 'main'),
    [switch]$VerifyBuild,
    [string]$BuildRef = 'HEAD',
    [string]$ReportPath = ''
)

$ErrorActionPreference = 'Stop'
# Windows PowerShell 5.1 can evaluate parameter defaults before PSScriptRoot
# is available. Resolve the default only after the script body has started.
if ([string]::IsNullOrWhiteSpace($Repository)) {
    $Repository = Join-Path $PSScriptRoot '..'
}
$auditRoot = (Resolve-Path -LiteralPath $Repository).Path
$script:Findings = [System.Collections.Generic.List[object]]::new()
$script:Notes = [System.Collections.Generic.List[string]]::new()

function Invoke-AuditGit([string[]]$Arguments) {
    $result = @(& git -c "safe.directory=$auditRoot" -C $auditRoot @Arguments 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "git $($Arguments -join ' ') failed: $($result -join [Environment]::NewLine)" }
    return $result
}

function Add-Finding([string]$Scope, [string]$Path, [string]$Reason) {
    $script:Findings.Add([pscustomobject]@{ scope=$Scope; path=$Path; reason=$Reason })
}

function Get-PrivatePathReason([string]$Path) {
    $normalized = $Path.Replace('\', '/')
    if ($normalized -match '(?i)(^|/)(private_algorithms|algorithms-private|local_algorithms|private_plugins|private_build|training_data|model_weights)(/|$)' -or $normalized -match '(?i)(^|/)dist/private/') { return 'Private algorithm/model/training directory' }
    if ($normalized -match '(?i)(^|/)(go\.work|go\.work\.sum)$') { return 'Local Go workspace file' }
    if ($normalized -match '(?i)\.(exe|dll|pdb|lib|sfplugin|sfalgorithm|sfdata|pth|pt|onnx|safetensors|ckpt|h5|hdf5)$' -or $normalized -match '(?i)\.private\.zip$') { return 'Executable, native dependency, model weight or runtime artifact' }
    if ($normalized -match '(?i)(^|/)(random_cadzow|mpi[^/]*cadzow)(/|$)' -or $normalized -match '(?i)(cadzow|radon|fx[_-]?decon|curvelet)[^/]*\.(cpp|cc|c|cu|h|hpp|py|m|f90)$') { return 'Private numerical algorithm implementation' }
    return ''
}

function Inspect-Content([string]$Scope, [string]$Path, [string]$Content) {
    if ($Path -match '(^|/)manifest\.json$' -and $Content -match '(?i)"id"\s*:\s*"(?:cadzow|radon|fx[-_]?decon|curvelet)(?:[.\-_][^"]*)?"') { Add-Finding $Scope $Path 'Private algorithm installation manifest' }
    if ($Path -match '(^|/)\.gitmodules$' -and $Content -match '(?i)(private_algorithms|algorithms-private|private_plugins|cadzow|radon|fx[-_]?decon)') { Add-Finding $Scope $Path 'Private algorithm submodule declaration' }
    if ($Path -match '(^|/)(go\.mod|go\.sum)$') {
        if ($Content -match '(?m)=>\s*(?:\.{1,2}[/\\]|[A-Za-z]:[/\\]|[/\\]|~[/\\])') { Add-Finding $Scope $Path 'Local filesystem replace in dependency metadata' }
        if ($Content -match '(?i)(private_algorithms|algorithms-private|local_algorithms|private_plugins|private_build)') { Add-Finding $Scope $Path 'Private algorithm dependency in module metadata' }
    }
    if ($Path -match '\.go$') {
        if ($Content -match '(?m)^\s*//go:build[^\r\n]*(private|cadzow|radon|fxdecon)' -or $Content -match '(?m)^\s*//\s*\+build[^\r\n]*(private|cadzow|radon|fxdecon)') { Add-Finding $Scope $Path 'Private algorithm build tag in public source' }
        # Match import declarations only: public discovery paths mentioned in
        # comments, tests or ordinary string constants are not private imports.
        $imports = [regex]::Matches($Content, '(?ms)^\s*import\s*(?:\((.*?)\)|([^\r\n]+))')
        foreach ($import in $imports) {
            if ($import.Value -match '(?i)"[^"\r\n]*(private_algorithms|algorithms-private|local_algorithms|private_plugins|private_build)[^"\r\n]*"') { Add-Finding $Scope $Path 'Public code imports a private algorithm package' }
        }
    }
}

$commit = (Invoke-AuditGit @('rev-parse', 'HEAD') | Select-Object -First 1).ToString()
$status = @(Invoke-AuditGit @('status', '--porcelain'))
$tracked = @(Invoke-AuditGit @('-c', 'core.quotepath=false', 'ls-files'))
$staged = @(Invoke-AuditGit @('-c', 'core.quotepath=false', 'diff', '--cached', '--name-only', '--diff-filter=ACMR'))

foreach ($pathValue in $tracked) {
    $path = $pathValue.ToString()
    $reason = Get-PrivatePathReason $path
    if ($reason) { Add-Finding 'tracked' $path $reason }
    if ($path -match '(\.go$|(^|/)(go\.mod|go\.sum|manifest\.json|\.gitmodules)$)') {
        $local = Join-Path $auditRoot $path
        if (Test-Path -LiteralPath $local -PathType Leaf) { Inspect-Content 'working-tree' $path ([IO.File]::ReadAllText($local)) }
    }
}
foreach ($pathValue in $staged) {
    $path = $pathValue.ToString()
    $reason = Get-PrivatePathReason $path
    if ($reason) { Add-Finding 'staged' $path $reason }
    if ($path -match '(\.go$|(^|/)(go\.mod|go\.sum|manifest\.json|\.gitmodules)$)') {
        $content = (Invoke-AuditGit @('show', ":$path")) -join "`n"
        Inspect-Content 'staged' $path $content
    }
}

# File removal or .gitignore cannot erase reachable Git history. Check every
# object reachable from each requested publication ref, not just its tip tree.
$seenHistory = [System.Collections.Generic.HashSet[string]]::new()
$auditRefs=@($HistoryRefs)
if ($VerifyBuild -and $auditRefs -notcontains $BuildRef) { $auditRefs+= $BuildRef }
foreach ($ref in $auditRefs) {
    # Missing optional refs are expected (e.g. a fresh fixture has no main).
    # --quiet prevents PowerShell 5.1 from promoting native stderr to a
    # terminating NativeCommandError before the exit code can be checked.
    $exists = @(& git -c "safe.directory=$auditRoot" -C $auditRoot rev-parse --verify --quiet "$ref`^{commit}" 2>$null)
    if ($LASTEXITCODE -ne 0) { $script:Notes.Add("History ref '$ref' is absent; not scanned."); continue }
    foreach ($objectValue in (Invoke-AuditGit @('-c', 'core.quotepath=false', 'rev-list', '--objects', $ref))) {
        $object = $objectValue.ToString()
        if ($object -notmatch '^[0-9a-f]+ (.+)$') { continue }
        $path = $Matches[1]
        $reason = Get-PrivatePathReason $path
        if ($reason -and $seenHistory.Add("$ref|$path")) { Add-Finding "history:$ref" $path $reason }
    }
}

$build = [ordered]@{ requested=[bool]$VerifyBuild; reference=$BuildRef; status='not-requested'; commit=''; go_version='' }
if ($VerifyBuild) {
    if ($status.Count -gt 0) { $script:Notes.Add('Worktree is dirty. Isolated build verifies the committed archive ONLY, not uncommitted or untracked changes.') }
    if ($script:Findings.Count -gt 0) {
        $build.status='blocked-by-public-tree-audit'
    } else {
        $build.commit=(Invoke-AuditGit @('rev-parse', "$BuildRef`^{commit}") | Select-Object -First 1).ToString()
        $scratchParent=[IO.Path]::GetTempPath()
        $scratch=Join-Path $scratchParent ('seisforge-public-audit-'+[guid]::NewGuid().ToString('N'))
        $oldGOOS=$env:GOOS; $oldGOARCH=$env:GOARCH; $oldGOFLAGS=$env:GOFLAGS
        New-Item -ItemType Directory -Path $scratch | Out-Null
        try {
            $archive=Join-Path $scratch 'source.zip'
            Invoke-AuditGit @('archive', '--format=zip', "--output=$archive", $BuildRef) | Out-Null
            $source=Join-Path $scratch 'source'
            Expand-Archive -LiteralPath $archive -DestinationPath $source
            $env:GOOS='windows'; $env:GOARCH='amd64'; $env:GOFLAGS=''
            $build.go_version=(& go version) -join ' '
            if ($LASTEXITCODE -ne 0) { throw 'Go runtime unavailable for isolated build' }
            Push-Location -LiteralPath $source
            try {
                & go build -buildvcs=false -trimpath '-ldflags=-s -w -H=windowsgui' -o (Join-Path $scratch 'public.exe') ./cmd/limage
                if ($LASTEXITCODE -ne 0) { throw 'Isolated public Windows GUI build failed' }
            } finally { Pop-Location }
            $build.status='passed-committed-archive'
        } catch {
            $build.status='failed'
            Add-Finding 'build' $BuildRef $_.Exception.Message
        } finally {
            $env:GOOS=$oldGOOS; $env:GOARCH=$oldGOARCH; $env:GOFLAGS=$oldGOFLAGS
            # Exact, generated scratch directory; never recursively delete the
            # repository, an environment variable root or a computed wildcard.
            $resolvedScratch=[IO.Path]::GetFullPath($scratch)
            $resolvedParent=[IO.Path]::GetFullPath($scratchParent).TrimEnd('\','/')+[IO.Path]::DirectorySeparatorChar
            if ($resolvedScratch.StartsWith($resolvedParent,[StringComparison]::OrdinalIgnoreCase) -and [IO.Path]::GetFileName($resolvedScratch).StartsWith('seisforge-public-audit-')) {
                Remove-Item -LiteralPath $resolvedScratch -Recurse -Force
            } else { throw "Refusing scratch cleanup outside validated temporary directory: $resolvedScratch" }
        }
    }
}

$result=[ordered]@{ schema='seisforge.public-tree.audit.v1'; repository=$auditRoot; commit=$commit; dirty=($status.Count -gt 0); tracked_count=$tracked.Count; staged_count=$staged.Count; history_refs=$auditRefs; build=$build; findings=@($script:Findings.ToArray()); notes=@($script:Notes.ToArray()); passed=($script:Findings.Count -eq 0) }
if ($ReportPath) {
    $reportTarget=[IO.Path]::GetFullPath($ReportPath)
    if (Test-Path -LiteralPath $reportTarget) { throw "Audit report already exists; refusing overwrite: $reportTarget" }
    [IO.File]::WriteAllText($reportTarget,($result | ConvertTo-Json -Depth 12),[Text.UTF8Encoding]::new($false))
}
foreach ($note in $script:Notes) { Write-Host "NOTE: $note" }
foreach ($finding in $script:Findings) { Write-Host "BLOCK [$($finding.scope)] $($finding.path): $($finding.reason)" }
if ($script:Findings.Count -gt 0) {
    Write-Host "PUBLIC RELEASE BLOCKED: $($script:Findings.Count) findings. No history rewrite, file deletion, push or release was performed."
    exit 1
}
Write-Host "Public tree audit passed. Commit: $commit. Build: $($build.status)."
exit 0
