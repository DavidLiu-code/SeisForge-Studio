# Prestack QC and Source–Receiver Architecture

## Scope

Version 1.10.3 adds metadata-only quality control and acquisition-point linkage to the independent Prestack workspace. Ordinary 2D, crooked 2D, true 3D, pseudo-3D and SEG-Y analysis keep their existing readers, amplitude mapping, palettes and parameter files.

## Data flow

`SEG-Y headers → PrestackIndex → gather selection / QC report / Geometry association → existing trace renderer`

`PrestackIndex` stores one compact record per physical trace. It never stores amplitude samples. `BuildIndex` calculates file counters, missing-field counters and 32-bin Fold/Offset/Azimuth distributions while scanning headers. `QualityStats` and `BuildQCReport` return defensive metadata-only copies. CSV and JSON exports contain no samples or amplitude values.

Source and Receiver points are built from SourceID/ReceiverID when IDs are stable. If an ID is reused at different coordinates, the complete role falls back to exact scaled XY keys. Source/Receiver association lists physical trace numbers and counterpart point indexes; the Geometry window uses a spatial point lookup and draws connections as a clipped dynamic overlay.

## UI and generations

The QC page is independent of the Gather renderer. Gather key lists retain an explicit “全部范围” item and are committed only after native combo selection closes. Render jobs carry owner, workspace, dataset, selection and resize identities. A result with any stale identity is discarded before buffer exchange.

During window resizing the last complete frame remains visible and is clipped/scaled only inside the scene rectangle. One debounced final render starts after the resize ends. Palette changes recolor the accepted index image using the current palette, preventing an older worker from restoring stale colors.

Geometry Source/Receiver clicks update only dynamic overlays. Plain clicks select and highlight relationships; double-click opens Shot/Receiver gathers; Ctrl+click opens the existing single-trace analysis with the physical trace number and header context. Escape clears the relationship selection.

## Deliberate boundaries

This release adds a read-only A/B matching foundation on the Compare page;
it does not write matched samples or alter A's gather.  It still does not add
NMO, velocity analysis, AVO, interpolation, stacking, SEG-Y write-back or a
persistent QC cache. Existing `.lidx`, `.cidx`, `.pidx`, `.ptx`, JSON
parameter files and true-3D rendering parameters are unchanged.  Matching
details and lifecycle boundaries are documented in
`ARCHITECTURE_PRESTACK_COMPARE.md`.
