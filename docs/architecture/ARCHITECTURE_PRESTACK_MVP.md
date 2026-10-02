# Prestack MVP — SeisForge Studio 1.10.0

## Baseline and isolation

The baseline is `070cefc` (v1.9.15 with the 2-D loading/repaint fixes).
Before implementation a source-directory-external backup was made at
`../prestack_phase0_baseline_20261001`, with a SHA-256 manifest. Old executables
are retained. The new module is a separate workspace in the same application,
not a separate seismic decoder or a replacement for the post-stack viewer.

The pre-implementation audit found no retained `internal/prestack` package or
`.pidx` implementation in this checkout. Existing reusable boundaries are
`SeismicDataset.OpenReader`, `segy.File`, arbitrary physical-trace rendering,
the coordinate/header analysis code, the workspace manager, and the non-modal
trace analysis window. The legacy 2-D compare and volume windows remain
post-stack consumers of `GeometryIndex`.

## Data flow

`Application.OpenPath(KindPrestack)` opens immutable dataset metadata and routes
to the independent Prestack adapter. A cancellable header-only scan produces
compact trace metadata and CMP/Shot/Receiver grouping. Gather changes select
physical SEG-Y trace numbers; the existing SEG-Y reader/render functions read
only the chosen sample windows and produce the display raster. Geometry and
Fold consume metadata only.

Workspace kind 4 is appended; persisted kinds 1/2/3 retain their meaning. Auto
routing is unchanged. A duplicate IL/XL in the prestack index is fold, whereas
the post-stack `.lidx` index continues to require its original grid semantics.

## Ownership and async results

The Prestack session owns its dataset, mapping, index, selection, viewport,
palette/gain and native controls. Background scans and renders use independent
Readers, close them on exit and post results to the UI thread. Generations and
cancellation reject results from a replaced dataset, mapping or selection.
Workers do not draw into HWNDs. A failed scan leaves the workspace available for
mapping correction; it does not switch to the post-stack renderer.

Metadata contains no samples. Geometry drawings use bounded detail and clipping;
statistics and grouping use all indexed traces. Image mode reuses the existing
amplitude mapping. Wiggle is a display representation of on-demand samples,
not an edited or permanently normalized seismic dataset. Single-trace selection
passes the physical trace number to the existing waveform/header/spectrum window.

## Compatibility and deferred work

No `.lidx`, `.cidx`, `.ptx`, existing view JSON or source SEG-Y is rewritten.
No prestack disk cache is created. Mapping and indices are session-local. Recent
uses its existing schema with mode 4. No NMO, stacking, velocity analysis,
offset interpolation, processing, source writing, GPU or Python runtime is added.

A/B gather matching and differences, midpoint Rebin and `.pidx` persistence are
deferred. The Compare page explicitly reports that it is unavailable; it must
not assume that trace N in A matches trace N in B.

## Verification

Synthetic tests exercise grouping, fold, sorting, coordinate scalars, malformed
mapping, cancellation and independent routing. Full regression command:
`go test ./... -count=1`. The release script builds a Windows GUI image and
verifies PE Subsystem 2. Real-data acceptance should use the optional test input,
without placing proprietary SEG-Y samples into the repository.
