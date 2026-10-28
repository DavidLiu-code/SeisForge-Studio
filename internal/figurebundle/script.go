package figurebundle

import (
	"fmt"
	"strings"
)

func powerShellLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func renderScript(toolPath string) string {
	// Windows PowerShell 5.1 requires the BOM to decode non-ASCII tool paths.
	return "\ufeff# Requires Windows PowerShell 5.1 or PowerShell on Windows.\r\nparam(\r\n    [string]$Tool = " + powerShellLiteral(toolPath) + `,
    [string]$SourceA,
    [string]$SourceB,
    [string]$Output = (Join-Path $PSScriptRoot 'figure.png'),
    [switch]$Overwrite
)
$ErrorActionPreference = 'Stop'

# Quote for Windows native CommandLineToArgvW/CRT rules, not for a shell.
# No recipe value is evaluated as PowerShell, cmd.exe, or script source.
function ConvertTo-NativeArgument {
    param([AllowEmptyString()][string]$Value)
    $builder = New-Object System.Text.StringBuilder
    [void]$builder.Append([char]34)
    $slashes = 0
    foreach ($character in $Value.ToCharArray()) {
        if ($character -eq [char]92) { $slashes++; continue }
        if ($character -eq [char]34) {
            [void]$builder.Append([char]92, (2 * $slashes + 1))
            [void]$builder.Append([char]34)
        } else {
            if ($slashes -gt 0) { [void]$builder.Append([char]92, $slashes) }
            [void]$builder.Append($character)
        }
        $slashes = 0
    }
    if ($slashes -gt 0) { [void]$builder.Append([char]92, (2 * $slashes)) }
    [void]$builder.Append([char]34)
    return $builder.ToString()
}

$recipePath = Join-Path $PSScriptRoot 'recipe.json'
$recipe = Get-Content -LiteralPath $recipePath -Raw -Encoding UTF8 | ConvertFrom-Json
if (-not $PSBoundParameters.ContainsKey('SourceA')) { $SourceA = [string]$recipe.source_a.path }
if (-not $PSBoundParameters.ContainsKey('SourceB') -and $null -ne $recipe.source_b) { $SourceB = [string]$recipe.source_b.path }
$arguments = @('--render-recipe', $recipePath, '--output', $Output, '--source-a', $SourceA, '--result', ($Output + '.result.json'))
if (-not [string]::IsNullOrEmpty($SourceB)) { $arguments += @('--source-b', $SourceB) }
if ($Overwrite) { $arguments += '--overwrite' }
$argumentLine = ($arguments | ForEach-Object { ConvertTo-NativeArgument ([string]$_) }) -join ' '
$process = Start-Process -FilePath $Tool -ArgumentList $argumentLine -Wait -PassThru -WindowStyle Hidden
if ($process.ExitCode -ne 0) {
    [Console]::Error.WriteLine(('Figure rendering failed (exit {0}); inspect {1}' -f $process.ExitCode, ($Output + '.result.json')))
    exit ([int]$process.ExitCode)
}
` // The native renderer performs schema/version/source/license/pixel checks.
}

func readmeText(m BundleManifest) string {
	return fmt.Sprintf(`SeisForge reproducible figure bundle
Schema: %s
Application version: %s
Renderer SHA-256: %s
Expected pixels: %dx%d; SHA-256 %s

This bundle requires the original source data and the exact matching renderer
executable. It does not contain SEG-Y amplitude data. Source hashes cover the
complete files, so hashing a large input can take time and is never bypassed by
a metadata-cache hit. Source paths may be relocated with -SourceA/-SourceB;
the replacement files must still have the recorded identities/hashes.

On Windows, run render.ps1. Its -Tool parameter selects the matching renderer;
-Output selects the PNG path. Existing output is refused unless -Overwrite is
explicitly supplied. The renderer writes Output+'.result.json' and reports a
nonzero exit on validation/rendering failure. Recipe JSON contains data-only
numeric render parameters, not executable user code. No network is required.

If Restricted or AllSigned policy blocks this unsigned script, leave the
execution policy unchanged. Invoke the matching native EXE directly instead.
From the bundle directory, for example (prepend & in PowerShell):
  "C:\path\matching-renderer.exe" --render-recipe "recipe.json" --output "figure.png" --result "figure.png.result.json"
Use --source-a / --source-b only to relocate the same verified source files.
Use --overwrite only when replacement of an existing output is intended.
Native EXE replay does not require changing PowerShell script policy.

recipe.json is the reproducible input. reference.png is a verification image,
not the reproduction algorithm: the renderer must reread samples, rerender and
match the recorded pixel hash. Do not substitute copying reference.png.

Pixel hashing includes width/height and row-major 8-bit premultiplied RGBA
values, independent of PNG compression, metadata and image origin. PNG output
is decoded and its pixels verified before atomic publication. Bundle creation
refuses an existing directory. Cancellation is checked before publication;
the successful atomic rename is the commit point, not a partial-file state.

Keep all four bundle files together: recipe.json, reference.png, render.ps1,
README.txt. A valid original dataset, matching renderer version and applicable
license are still required; a bundle does not bypass licensing or validation.
`, m.Schema, m.ApplicationVersion, m.RendererSHA256, m.Width, m.Height, m.PixelSHA256)
}
