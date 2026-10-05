# Prestack Read-only A/B Compare Foundation

Version 1.10.3 adds a read-only A/B foundation to the independent Prestack
workspace.  It is deliberately metadata-first: A and B keep separate
`SeismicDataset`, reader, mapping, index, cancellation function and
generation.  Loading or failing B never changes A's gather, QC page or
ordinary browsing state.

## Matching contract

`internal/prestack/matching.go` matches the current A selection in A order.
It never assumes that physical trace numbers are shared between files.  The
candidate priority is:

1. stable SourceID + ReceiverID;
2. scaled Source/Receiver XY;
3. CDP + signed Header/Computed Offset;
4. invalid when no reliable key exists.

An ID reused at conflicting coordinates falls back to XY for that ID.  A
duplicate key is reported as ambiguous rather than resolved by map order.
`CompareMatchResult` contains pairs, A-only/B-only, ambiguous and invalid
trace lists; `Clone` and the JSON/CSV report builders return defensive,
metadata-only copies.

## Sampling and rendering

`SampleAxisCompatibility` checks sample count, sample interval, trace
delay/time-origin metadata and the current sample window.  A and B can still
be displayed when an axis is incompatible, but the Δ panel is disabled.  When
compatible, the renderer reads only the matched current display window,
computes A−B in floating point, applies a zero-symmetric range to Δ, expands
the final palette indices to BGRA and only then exchanges the clipped raster.
No full amplitude volume is retained.

## UI and lifecycle

The Compare tab provides **加载 B**, **关闭 B** and metadata-only match report
export.  Its three clipped panels show A, B and Δ.  B index and render
messages carry owner HWND/token, workspace, A/B dataset and mapping,
selection, compare-mode, display, resize, sample-window and panel-size
generations.  A stale result is discarded before exchanging any raster or
reader state.  Closing B cancels its index/render work and clears pending
buffers; leaving Compare or switching the workspace uses the same cancellation
path.  Reports use schema 1.1 and contain paths, selection, axis/window
compatibility, counts and physical metadata only.

The strict sample-count entry point is scoped to Prestack Compare.  The
historical shared 2-D/3-D residual renderer keeps its previous behavior so
ordinary Compare and volume workflows are not changed by this phase.

This phase does not implement NMO, velocity analysis, AVO, stacking,
interpolation, Rebin, persistent `.pidx` changes, GPU/Python processing or
SEG-Y write-back.  Existing `.lidx`, `.cidx`, `.pidx`, `.ptx` and JSON
parameter formats remain unchanged.
