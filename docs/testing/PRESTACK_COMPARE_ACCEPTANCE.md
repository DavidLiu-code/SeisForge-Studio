# Prestack Compare acceptance gate before `.pidx`

## Status and rollback boundary

Baseline: `c70434652a6bf0f126868cb8e50c97c46115048f` (`v1.10.4-rc1`).
This work runs on the independent `prestack-pidx` worktree. The original
worktree's uncommitted display interpolation changes are not included.

As of 2026-10-07, automated acceptance passes, but Windows GUI acceptance is
**BLOCKED / NOT EXECUTED**. The supported computer-use connection failed to
initialize twice (sandbox setup refresh, then deny-read ACL application).
Recovery was stopped instead of using unsupported UI automation. Existing
Gather and unloaded-B screenshots are not evidence of a successful A/B/Delta
GUI run. `.pidx` implementation must not start until this gate is completed.

The acceptance changes form one local commit, independently revertible from
the subsequent cache work. No version promotion, tag movement, RC1 executable
overwrite, push or release is part of this stage.

## Issues discovered and corrected

1. B-index delivery checked the result's generation against the posted message,
   but not against the current B-task generation. A mutually consistent old
   result/message is now rejected after B is replaced or closed.
2. Metadata matching treated physical trace number `0` as unspecified when its
   logical selection position was nonzero. The first physical trace remains
   `0` after primary/secondary sorting, filtering and report construction.
   Only an explicitly negative synthetic trace number uses the position
   fallback; Reader-built records always carry real nonnegative trace numbers.
3. JSON classification rows used `omitempty` on integer trace numbers and lost
   the first physical trace. Present A/B sides now explicitly serialize `0`;
   absent sides remain omitted. CSV columns and report schema `1.1` are unchanged.

All changes are scoped to Prestack matching, delivery, metadata reports and
test fixtures. Ordinary 2-D/3-D/crooked/pseudo-3-D renderers are not modified.

## Follow-up: small Gather blank and Delta fixture choice

The user subsequently opened `A.sgy` and reported Gather remaining at
"正在刷新当前道集". Reader-render regression now reproduces the actual failure:
60 physical traces were first expanded to approximately 1274 pixel columns;
the 256-sample window expanded vertically enough to select Adaptive smoothing;
the sparse smooth support reader rejected repeated physical traces with
`sparse trace request contains a duplicate`. The completion handler then
discarded the error because it had no valid pixel buffer and never ended busy.

The Prestack-only correction keeps the unique logical gather as Adaptive
render input, with a separate output-column mapping for hover/analysis. The
explicit nearest-only Wiggle and Offset column lists remain unchanged. Current
errors now end their task and show failure; old-generation errors still cannot
end a newer task. Successful buffers retain strict length/dimension checks.
Compare completion uses the same identity-before-error-before-buffer order.
No shared SEG-Y renderer or ordinary viewer source was changed.

The user's Compare screenshot loaded **`A.sgy` + `B_classified.sgy`**, not the
positive Delta fixture. `B_classified.sgy` deliberately contains a header with
invalid sample-axis metadata; "当前道集内部采样轴不一致" is an expected reason
to disable Delta under the existing strict gather-axis contract. This guard
was not weakened to fabricate a difference. The empty Delta panel now shows
the reason directly. For the positive test load **`B_reordered_half.sgy`**;
use `A_classified.sgy` + `B_classified.sgy` only for classification/negative tests.

Additional Windows UI-free Reader tests cover 60x256 at 1274x662, a short
100..115 sample window, and a 2474x982 scene, comparing corrected sparse
pixels byte-for-byte with the existing non-sparse logical-source renderer.
Tests also cover cancellation/completion state and a real Reader A/B float
Delta raster expanded to 539x692 with a valid BGRA buffer. These tests are not
a claim of completed live GUI acceptance; the updated executable still needs
the user's GUI retest before cache development.

## Automated evidence

The fixture contains 5 Shots x 12 Receivers, with legal coordinate scalars
`0`, `10`, `-10`, `2`, `-2`, legal origin coordinates, and signed/zero Offsets.
The B physical order is the permutation `(physical * 17 + 7) mod 60`.
Files are generated in temporary directories, reopened through the actual
SEG-Y Reader, then indexed with `BuildIndex` and the default mapping.

`TestCompareAcceptanceReaderReorderedWaveformsAndSelections` verifies:

- 60 metadata pairs, stable A order and correct shuffled B physical numbers;
- actual Reader windows: `[1,2,3] - [0.5,1,1.5] = [0.5,1,1.5]`;
- four additional logical-trace-dependent waveform values that detect a
  wrong physical pairing independently of header matching;
- all six gather types, full-range selection, multi-key repeatability;
- positive/negative scalar decoding and valid `(0,0)` coordinates;
- unchanged source hashes after read-only operations on these small fixtures.

`TestCompareAcceptanceReaderClassificationReportsRepeat` verifies:

- matched 56, A-only 1, B-only 1, ambiguous A/B 2 each, invalid A/B 1 each;
- 64 logical rows with all filters and 57 with matched + A-only;
- ten identical runs with stable selection order and identical CSV/JSON bytes;
- JSON round-trip classifications/metadata and fixed CSV row order;
- no waveform/sample-array fields in reports.

`TestCompareAcceptanceReaderSampleAxisGate` reopens compatible and incompatible
files and checks count, interval, delay/time-origin and sample-window reasons.
Axis differences do not prevent metadata matching. This tests the compatibility
gate, **not the visible GUI rendering of a disabled Delta panel**.

`TestMatchRecordsKeepsPhysicalZeroAfterReordering`,
`TestCompareReportPreservesZeroPhysicalTraceJSON` and
`TestPrestackCompareBResultRejectsMatchingOldMessageGeneration` lock down the
three corrections above. Existing Windows UI-free tests also cover render
tokens, BGRA lengths, owner/page/dimension changes and panel rectangles from
1680x1089 down to 320x240. They do not prove actual HWND/GDI/resource behavior.

Run:

```powershell
gofmt -w <modified-Go-files>
go test ./... -count=1
git diff --check
```

The opt-in fixture-retention test is skipped in ordinary runs. That skip is
expected; it is not a skipped core regression. Live GUI checks and real-data
GUI smoke checks remain separately marked NOT EXECUTED.

## Generate manual fixtures

From this worktree, use a writable temporary parent directory:

```powershell
$env:SEISFORGE_COMPARE_FIXTURE_PARENT = [System.IO.Path]::GetTempPath()
go test ./internal/prestack -run '^TestCompareAcceptanceGenerateManualFixtures$' -count=1 -v
Remove-Item Env:\SEISFORGE_COMPARE_FIXTURE_PARENT
```

Each invocation creates a new `seisforge-compare-*` directory and prints the
exact paths. No real input file or existing fixture is overwritten. Files are
not committed, and automatic tests close their Readers. The retained files
are for manual acceptance and may be removed after the GUI has closed them.

| A | B | Expected metadata / axis result |
|---|---|---|
| `A.sgy` | `B_reordered_half.sgy` | 60 matched; axes compatible; B order aligned to A |
| `A.sgy` | `B_count_mismatch.sgy` | 60 matched; sample-count mismatch disables Delta |
| `A.sgy` | `B_interval_mismatch.sgy` | 60 matched; interval mismatch disables Delta |
| `A.sgy` | `B_delay_mismatch.sgy` | 60 matched; delay/time-origin mismatch disables Delta |
| `A_classified.sgy` | `B_classified.sgy` | 56/1/1/2/2/1/1 counts; intentional invalid-header axis disables Delta |

Default mapping uses 1-based header bytes: SourceID 9, ReceiverID 13,
CDP 21, signed Offset 37, scalar 71, Source XY 73/77, Receiver XY 81/85,
units 89, delay 109, sample count 115, sample interval 117. Generated normal
files use 256 samples, 2000 us and a 10 ms delay. Mapping confirmation is
required; the generator does not verify automatic GUI mapping detection.

## Required manual gate

Record executable path/hash, source commit, window size/DPI, input paths and
each result as PASS, FAIL or SKIPPED with a reason. Place screenshots, reports
and runtime logs in the ignored `artifacts/prestack_release_candidate/`
directory. Never commit input SEG-Y, runtime caches or temporary B files.

1. Open `A.sgy` in Prestack and confirm 60 logical physical traces. Load
   `B_reordered_half.sgy` in Compare. Confirm A/B/Delta are visible, the
   metadata count is 60 and the shuffled B waveform is correctly aligned.
   Display normalization can rescale Delta; inspect the first three samples
   through analysis rather than inferring raw numerical values from colors.
2. Ctrl-click A and B columns, including the first physical trace after CMP
   sorting, and verify the actual per-file physical number in single-trace
   analysis. Ordinary click must not open analysis. Check hover, linked zoom,
   double-click reset and spectrum/header analysis.
3. Load each incompatible B file. A/B must remain visible; Delta must show
   the specific incompatibility reason. Try a missing B file and same-file
   A/B; A must remain usable. Close B and reload repeatedly.
4. Open the classified A/B pair, select all categories, then each category
   separately. Check counts and order. Export CSV/JSON, including rows for
   internal trace 0. Reports must contain metadata only and explicit physical
   trace fields on the present side.
5. Edit B Mapping independently, rebuild its index, and quickly change
   mapping/gathers/pages during loading. A mapping/QC/gather state must remain
   unchanged; stale B index and render results must not appear.
6. Switch Gather/Geometry/Mapping/Compare/QC; resize continuously, maximize,
   restore, minimize and recover. Check all three panel bounds, footer and
   progress/completion controls. No old-size raster, black line, white block
   or chart remnant may escape the current content rectangle.
7. Repeat load/close/remap and window close while watching process Reader,
   handle/GDI and background-task counts. Code cleanup paths and UI-free
   generation tests are not a substitute for this resource check.
8. Open the supplied real SEG-Y read-only and smoke-test six gather types,
   QC, Geometry and Compare failure/close paths, then ordinary 2-D, 3-D,
   crooked and pseudo-3-D entry points. Do not copy the full amplitude volume
   or modify the source/caches/JSON settings as part of acceptance.

If any required check is unavailable or fails, retain the logs and stop before
cache development. Do not mark the candidate accepted or formally release it.

## Development build and deferred scope

An independent development executable may be built without overwriting RC1:

```powershell
.\build_windows_release.ps1 -Output SeisForgeStudio_v1.10.4_compare_acceptance_test_x64.exe
```

The small-Gather follow-up uses a separate output name,
`SeisForgeStudio_v1.10.4_compare_gatherfix_x64.exe`, preserving the previous
acceptance binary for rollback. Neither development executable replaces RC1.

Its embedded version remains `1.10.4-rc1`. Record its exact source commit,
Go version, size, SHA-256 and Windows GUI PE subsystem `2` in the ignored
acceptance log; this is not the tagged RC1 binary or a `.pidx` test build.

Deferred until the manual gate passes: `BuildIndexCached`, user-cache identity
and fingerprints, explicit streaming `.pidx` format, atomic publication,
cache equality/corruption/cancellation/concurrency tests, A/B cache integration
and cache documentation. No `.pidx` implementation or cache commit is present
in this stage. NMO, stacking, Rebin, interpolation, GPU/Python and SEG-Y
write-back remain out of scope.
