# Standalone regression tests. All repositories/fixtures live in one unique
# temporary directory; this does not modify the real Git index or history.
$ErrorActionPreference='Stop'
$auditScript=Join-Path $PSScriptRoot 'check_public_tree.ps1'
$shell=Join-Path $PSHOME 'pwsh.exe'
if (-not (Test-Path -LiteralPath $shell)) { $shell=Join-Path $PSHOME 'powershell.exe' }
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('seisforge-audit-test-'+[guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot | Out-Null

function Test-Git([string]$Root,[string[]]$Arguments) {
    & git -c "safe.directory=$Root" -c user.name=SeisForgeTest -c user.email=test@example.invalid -C $Root @Arguments | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "fixture git failed: $($Arguments -join ' ')" }
}
function New-Fixture([string]$Name) {
    $root=Join-Path $testRoot $Name
    New-Item -ItemType Directory -Path (Join-Path $root 'cmd/limage') -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $root 'go.mod'),"module example.invalid/public`n`ngo 1.23`n")
    [IO.File]::WriteAllText((Join-Path $root 'cmd/limage/main.go'),"package main`nfunc main() {}`n")
    Test-Git $root @('init','-q')
    Test-Git $root @('add','.')
    Test-Git $root @('commit','-q','-m','public fixture')
    return $root
}
function Assert-Audit([string]$Root,[int]$Expected,[string]$Message,[switch]$Build,[switch]$DefaultRepository) {
    if ($DefaultRepository) {
        $fixtureScripts=Join-Path $Root 'scripts'
        New-Item -ItemType Directory -Path $fixtureScripts -Force | Out-Null
        $fixtureAudit=Join-Path $fixtureScripts 'check_public_tree.ps1'
        [IO.File]::Copy($auditScript,$fixtureAudit)
        # No -Repository argument: use the script's directory, not caller CWD.
        $args=@('-NoProfile','-ExecutionPolicy','Bypass','-File',$fixtureAudit)
    } else {
        $args=@('-NoProfile','-ExecutionPolicy','Bypass','-File',$auditScript,'-Repository',$Root)
    }
    if ($Build) { $args+='-VerifyBuild' }
    $result=@(& $shell @args 2>&1)
    if ($LASTEXITCODE -ne $Expected -or ($result -join "`n") -notmatch $Message) { throw "Unexpected audit result ($LASTEXITCODE), expected $Expected / $Message`: $($result -join '`n')" }
}

try {
    $clean=New-Fixture 'clean'
    Assert-Audit $clean 0 'passed-committed-archive' -Build
    Assert-Audit $clean 0 'passed-committed-archive' -Build -DefaultRepository

    $staged=New-Fixture 'staged'
    New-Item -ItemType Directory -Path (Join-Path $staged 'private_algorithms') | Out-Null
    [IO.File]::WriteAllText((Join-Path $staged 'private_algorithms/module.cpp'),'private fixture; no algorithm')
    [IO.File]::WriteAllText((Join-Path $staged 'weights.onnx'),'not real weights')
    Test-Git $staged @('add','.')
    Assert-Audit $staged 1 'staged.*private_algorithms'

    $deps=New-Fixture 'deps'
    [IO.File]::AppendAllText((Join-Path $deps 'go.mod'),"`nreplace example.invalid/secret => C:/outside/private`n")
    Test-Git $deps @('add','go.mod')
    Assert-Audit $deps 1 'Local filesystem replace'

    $imports=New-Fixture 'imports'
    [IO.File]::WriteAllText((Join-Path $imports 'cmd/limage/main.go'),"//go:build private`npackage main`nimport _ `"example.invalid/private_algorithms/worker`"`nfunc main() {}`n")
    Test-Git $imports @('add','.')
    Assert-Audit $imports 1 'private algorithm package'

    $history=New-Fixture 'history'
    New-Item -ItemType Directory -Path (Join-Path $history 'experiments/random_cadzow') -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $history 'experiments/random_cadzow/worker.cpp'),'private fixture; no algorithm')
    Test-Git $history @('add','.')
    Test-Git $history @('commit','-q','-m','private fixture in history')
    Test-Git $history @('rm','experiments/random_cadzow/worker.cpp')
    Test-Git $history @('commit','-q','-m','remove current file only')
    Assert-Audit $history 1 'history:HEAD.*random_cadzow'
    Write-Host 'Public-tree audit regression tests passed (explicit/default repository clean archive build, staged paths, dependencies, private imports/tags, reachable deleted history).'
} finally {
    $resolved=[IO.Path]::GetFullPath($testRoot)
    $tempPrefix=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/')+[IO.Path]::DirectorySeparatorChar
    if ($resolved.StartsWith($tempPrefix,[StringComparison]::OrdinalIgnoreCase) -and [IO.Path]::GetFileName($resolved).StartsWith('seisforge-audit-test-')) {
        Remove-Item -LiteralPath $resolved -Recurse -Force
    } else { throw "Refusing test cleanup outside exact temporary root: $resolved" }
}
