# Prestack Compare acceptance gate before `.pidx`

## 2026-10-26 Compare development gate: PASS; full release NOT ACCEPTED

The filter-expansion repair now passes its GUI retest on the new independent
executable below. Interval/delay negative-axis checks and compatible reload
also pass. Five controlled load/close rounds and a final idle sample complete
the finite resource check. This passes the user's first-stage Compare
development gate using the existing small synthetic A/B fixtures; no confirmed
practical blocker remains for that scoped gate. It is not full formal-release
acceptance or a claim of absolute/long-running leak freedom.

The historical full release matrix below additionally includes real-data and
all-other-workspace smoke checks. Unexecuted items remain NOT EXECUTED, not
PASS, and still limit formal release; they are not silently added to this
small-fixture development gate. An earlier automated launch timed out, but
the manually started filterfix window was subsequently found (HWND `211940`,
PID `55976`); NOT RUN describes that earlier attempt, not the current results.
The checkout is `prestack-pidx` at `ad9aff9` plus uncommitted Compare acceptance
fixes. Automated tests, gofmt and `git diff --check` passed on October 26.
The evidence is ready for the first local Compare acceptance commit. Cache
work is still NOT STARTED and no commit or release is made by this record.
Earlier blocked runs and earlier
executable observations below are historical evidence, not this build's status.

```text
SeisForgeStudio_v1.10.4_compare_acceptance_filterfix_20261026_x64.exe
Bytes: 5929984
SHA-256: B04CD2A3597CD575B9C65885369935DB0BF5FFBC290CF60BA66AB751EA82E16A
PE Subsystem: 2 (Windows GUI)
Source: ad9aff9 + uncommitted Compare acceptance changes
```

Latest full `go test ./... -count=1`: PASS (`cmd/limage` 1.209s,
`internal/prestack` 0.362s, `internal/segy` 0.455s). `git diff --check`: PASS.
The selected Reader/resource/offscreen/filter regressions also pass with
count 10 (3.301s).

GUI observations on this B04CD2A3... filterfix executable:

- PASS: default classified selection 57; matched off gives 1, all-off 0,
  invalid 2, add ambiguous 6, add B-only 7, add A-only 8, add matched 64.
  The complete expansion works without double-click recovery.
- PASS: `A.sgy` + interval-mismatch B gives 60 matches, visible A/B and
  Delta reason `sample interval mismatch`; closing B preserves A.
- PASS: delay-mismatch B gives 60 matches and Delta reason
  `delay mismatch; time origin mismatch`; closing B preserves A.
- PASS: compatible reordered-half B reloads with 60 matches and Delta restored.
- PASS: Compare -> QC -> Compare, maximize/restore and minimize/recover leave
  no residuals. Reported image/window sizes are 2560 x 1380 maximized and
  1702 x 1140 restored; these are not independent client-size/DPI measurements.
- PASS for the finite resource check: five after-close samples keep GDI at
  222. At the latest idle (20:49:53, PID 55976), GDI/USER/handles/threads are
  `222/258/984/21`, with working set 89,591,808 bytes. USER, handles and threads
  return below both the first warmed sample and the third after-close sample.
  This supports bounded convergence in this
  five-round run, not an absolute no-leak guarantee. Exact samples are in the log.

The following October 26 GUI observations belong only to the earlier
`SeisForgeStudio_v1.10.4_compare_acceptance_20261026_x64.exe`
(5,929,472 bytes, SHA-256
`F445A2909E37B3773879F85108B7484F5DA9AF6530C39EBE58BE0B1F97196F1C`),
not to the new filterfix executable:

- PASS: A/B load with 60 matches. Change only B Receiver mapping byte `13`
  to `0`, rebuild, and obtain 60 XY matches; A Receiver mapping remains `13`.
- PASS: close B keeps A visible; choosing A itself as B is refused; corrupt B
  gives an explicit failure while A remains visible; reloading valid B recovers.
- PASS: `A.sgy` + `B_count_mismatch.sgy` still match 60 traces and display
  A/B; Delta explicitly reports `sample count mismatch`.
- PASS: linked left-drag selection reduces 60 to 34 columns and double-click
  resets it. Q/E shortcuts, AGC and fixed amplitude range `[-1,1]` work.
- PASS: maximize/restore, narrow stacked three-panel layout with horizontal
  toolbar scrolling, and minimize/recover. The reported narrow width of about
  842 pixels is the returned screenshot width, not a measured client width or
  DPI. Exact 640 x 480 / 640 x 600 client-size checks are not established by it.
- PASS for individual classified filters: matched 56, A-only 1, B-only 1,
  ambiguous 4, invalid 2 and all-off 0. Native CSV and JSON save dialogs were
  exercised; read-only reopening verified schema `1.1`, all 64 classification
  rows, A/B physical-trace metadata (including trace 0), matching CSV/JSON row
  order and no amplitude/waveform arrays. See the October 26 log for hashes.
- FAIL found on the earlier executable: expanding invalid 2 to invalid + ambiguous 6
  and then all 64 retains the old two-column horizontal view instead of resetting
  it. Double-click reset displays all 64 correctly, but is a recovery action,
  not a passing fix verification. The targeted repair has since passed the
  new filterfix GUI sequence recorded above; this old-build failure is retained
  as historical defect evidence.
- PASS: the earlier acceptance window closed normally. This alone does not
  establish resource convergence or Reader/GDI leak freedom.

The fixture directory is
`C:\Users\Administrator\AppData\Local\Temp\seisforge-compare-4068441488`.
Detailed observations, build provenance, pending checks and process resource
snapshots are recorded in ignored
`artifacts/prestack_release_candidate/compare-gate-2026-10-26.md`.

Additional automated evidence now covers all 32 core filter combinations,
ten repeated runs per combination, exact A/B selection order and immutable
match inputs. Real Reader worker tests cover cancellation/replacement at a
delivery barrier and Reader release. Native offscreen raster/Ctrl-preview
tests exercise 64 drawing cycles, resource counts and DC-state restoration.
These are not claims of leak-free live resize/WM_PAINT operation. Individual
category/all-off paths have earlier-build evidence, and the new filter-expansion
transition now has its own GUI PASS above. The 32-combination
core test does not require a second exhaustive GUI loop.

No immediate GUI check remains open in this finite Compare development gate.
Filter expansion/default, interval/delay and the controlled resource run are
passed. The count-mismatch result
on the earlier executable must not be attributed to
the new executable. Other items in the original required-manual-gate matrix
retain their recorded status, not an inferred PASS. The October 25 user-confirmed Ctrl marker,
physical number and A/B analysis result remains separately recorded evidence.
Native classified CSV/JSON exports are now verified and are not pending merely
because their earlier-build entries below remain historical. Resource
snapshots with dialog/IME initialization are observations only, not
a leak or leak-free conclusion. The prior input-stop handoff and launch timeout
are historical; the main acceptance session has resumed GUI input on the
manually started filterfix window. Codex and other user applications were not
closed. At the final idle handoff, input stopped and the node kernel was reset;
the filterfix window remains open. The Compare development gate is PASS,
the full release matrix is NOT ACCEPTED, cache work is NOT STARTED, and no
commit has been made.

## 2026-10-25 follow-up (historical partial GUI evidence)

Current checkout is `prestack-pidx` at `ad9aff9` plus uncommitted Compare
fixes. The October 7 blocked result below is historical, not the current
availability of Windows automation. Native GUI testing was possible on
October 25. Full acceptance remains **PENDING**; do not start `.pidx` or
create an acceptance commit until the remaining manual checks pass.

This follow-up corrects shared-control hide/show flicker, keeps the toolbar
on one row and the summary on two physical lines, and preserves empty A/B
logical slots instead of compacting the two images independently. Gain now
changes the shared floating-point display range before palette conversion;
manual limits take precedence. Nested zoom/pan uses the visible logical and
sample ranges. Closing B clears its tasks/results and starts one independent
A render (the earlier GUI run exposed a permanent white A panel).

Ctrl preview is a display-only overlay: hold Ctrl over an A/B image to snap
a vertical marker to the prospective logical trace and label the real
1-based per-file physical trace. Paired available sides share the marker;
missing sides and disabled Delta are not selectable. Preview and click use
the same screen-support/physical mapping. Release Ctrl, leave the image,
change focus/page, resize or invalidate the frame to clear the overlay.
No Reader, sample processing or background render is started by preview.

Automated: `go test ./... -count=1` and `git diff --check` pass on Windows
with `go1.26.7 windows/amd64` and a local `GOCACHE`. Added tests cover gain
and manual ranges, sparse/missing-side slots, nested interaction, close-B
state cleanup, control placement, two-line layout and Ctrl preview mapping,
clipping bounds, missing sides, resize/unfinished-frame rejection and cleanup.
These tests do not substitute for actual Ctrl-click or runtime leak checks.

GUI observed using generated 5 Shot x 12 Receiver inputs:

- PASS: A Gather, unloaded-B Compare, loading reordered half-amplitude B,
  60 matches and A/B/Delta images; close B preserves visible A; reload B.
- PASS on the preceding gate2 build: nested left-drag zoom on A and B,
  double-click reset, E palette and Q gain changes, manual amplitude range,
  incompatible sample interval with explicit Delta reason, narrow stacked
  panels and maximize. Those interactions still need final-build smoke checks.
- PASS on gate2: GUI CSV export reopened as 60 metadata-only matched rows,
  with incompatible-axis reason and real A/B physical numbers. JSON is
  automatically tested but its native save-dialog path is not yet exercised.
- PASS (user-assisted, October 25, Ctrl-preview build): holding/releasing Ctrl
  shows/clears the prospective trace marker and physical number; A and B
  Ctrl-click analysis agrees with the corresponding file and preview number.
  User confirmation: "标线、道号和 A/B 分析均正常".
- PENDING at the October 25 handoff: B Mapping/remap, category filter paths,
  failure/same-file paths, repeated minimize/restore/resize and long-running
  Reader/GDI/task-count checks. The real-file/all-other-workspace GUI matrix
  has not been completed in this follow-up.

The October 25 remaining-gate audit identified two additional lifecycle defects: the
B Mapping Auto command incorrectly invoked A indexing, and B completion
could silently disappear after page/selection generation changed, leaving
the loading flag set. Both have since received targeted fixes and regression
tests in the October 26 candidate; B independent manual editing/rebuild also
has the October 26 GUI evidence above. Automatic/rapid-change runtime checks
must still be recorded separately.

October 25 independent test executable (not RC1 and not a cache build):

```text
SeisForgeStudio_v1.10.4_compare_ctrl_preview_20261025_x64.exe
Bytes: 5916160
SHA-256: F4A671A56DCECDBF3F2883B2B7169BC57C9B09A9CEAB836C5997544BDB6233C6
PE Subsystem: 2 (Windows GUI)
Source: ad9aff9 + uncommitted Compare acceptance/preview changes
```

Detailed local observations and fixture/report paths are recorded in ignored
`artifacts/prestack_release_candidate/compare-gate-2026-10-25.md`. No EXE,
fixture, cache or user data is committed. Main-worktree interpolation changes
remain untouched; no commit, tag, push or release has been made in this follow-up.

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

## Historical full formal-release matrix (broader scope)

This retained matrix is broader than the current first-stage small-fixture
Compare development gate. In particular, real-data and all-workspace smoke
checks below are not claimed PASS by the finite acceptance result above.

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

Unavailable or failed checks in this broader matrix remain recorded as such
and block full formal-release acceptance. The scoped Compare development gate
is evaluated separately above; its PASS is not a promotion, tag or release.

## Development build and deferred scope

An independent development executable may be built without overwriting RC1:

```powershell
.\build_windows_release.ps1 -Output SeisForgeStudio_v1.10.4_compare_acceptance_test_x64.exe
```

The small-Gather follow-up uses a separate output name,
`SeisForgeStudio_v1.10.4_compare_gatherfix_x64.exe`, preserving the previous
acceptance binary for rollback. Neither development executable replaces RC1.

The narrow-window follow-up is also independent:
`SeisForgeStudio_v1.10.4_narrow_window_x64.exe`. Prestack previously enforced a
1120 x 700 minimum client area. It now allows a 640 x 480 client area (the
outer window also includes the Windows frame/title bar). Default creation
remains 1360 x 880. Ordinary 2-D/3-D/crooked/pseudo-3-D minimum sizes are not
changed.

At narrow widths a native horizontal scrollbar appears below the tab strip.
It moves only page controls, preserving the two Gather toolbar rows; it does
not pan the data, change the selection/sample window, or start a render. The
Gather/Geometry/Compare/QC scenes use the actual client width, not the toolbar
virtual width. Widening the window removes the scrollbar when the controls
fit. Compare explicitly requests more height if three panel titles plus
positive-height images cannot fit; it does not create inverted raster bounds.

Additional manual checks (not implied by UI-free layout tests):

- Shrink to 640 x 480, then widen through 800, 1100, 1280 and 1360 client
  pixels; maximize/restore repeatedly. Verify controls and old frames do not
  leave pixels under the scene/footer as the scrollbar appears/disappears.
- Scroll to each toolbar end using arrows, track/thumb and keyboard. Check
  gather type/key, multi-gather, CMP/Offset bin controls, exports, shared
  Image/Wiggle options, Geometry layers and Compare filters remain accessible.
- Scroll controls without changing the view: logical trace count, sample
  window and Ctrl-click physical mapping must remain unchanged. Verify both
  progress and completion controls stay anchored at the bottom right.
- Check Compare at 640 x 600 and 640 x 480 with B loaded. The October 25
  two-line summary allows three stacked panels at the minimum size; show a
  height hint only when panel titles plus positive rasters genuinely cannot
  fit. QC may still request more height for its summary/distributions.

The October 8 minimum-size follow-up initially left the pre-existing Mapping
target/first-label overlap unchanged. The October 26 acceptance candidate
places the target on its own row, moves the fields/actions/summary below it,
retains the two-choice dropdown height and synchronizes the A/B summary when
the target changes. Dedicated layout/native-summary tests cover these repairs;
the independent B edit/rebuild has the GUI evidence recorded above.

Its embedded version remains `1.10.4-rc1`. Record its exact source commit,
Go version, size, SHA-256 and Windows GUI PE subsystem `2` in the ignored
acceptance log; this is not the tagged RC1 binary or a `.pidx` test build.

Deferred until the scoped Compare development gate passes: `BuildIndexCached`, user-cache identity
and fingerprints, explicit streaming `.pidx` format, atomic publication,
cache equality/corruption/cancellation/concurrency tests, A/B cache integration
and cache documentation. No `.pidx` implementation or cache commit is present
in this stage. NMO, stacking, Rebin, interpolation, GPU/Python and SEG-Y
write-back remain out of scope.
