# Poststack Processing and Compare Architecture

## Purpose

This document defines the read-only poststack processing path: open A, choose an algorithm, generate an independent derived B, and reuse the existing A/B/Delta Compare window. It is intentionally separate from prestack, ordinary 2D/3D rendering, cache formats, batch-plan formats, and session snapshots.

## Layers

### Processing layer

`internal/poststack` is pure Go and owns algorithm metadata, parameter validation, cancellation, progress, and numerical results. It must not import Win32 packages or access HWNDs, UI controls, or global Readers.

The core contracts are:

- `AlgorithmSpec` and `ParameterSpec` describe an algorithm and its editable parameters.
- `Params` is a copied parameter map; `Metadata` and `SampleWindow` describe the sample/geometry axes.
- `Algorithm.Run(ctx, input, params, progress)` is the pure, bounded-input numerical contract used by unit tests and embedders.
- `ProcessSection(ctx, SectionRequest, progress)` streams selected traces into a temporary `SectionResult`; it does not materialize the full amplitude volume in an `Input`. The production algorithm-window dispatcher uses the public descriptor registry and artifact runner.
- Registry operations list algorithms, resolve an ID, and report unavailable external helpers without pretending they succeeded.

The compatibility registry provides `Identity/Copy` and `Gain` (default 1.0, range 0..10, step 0.1). The desktop registry provides the public temporal filter and user-authorized external modules; see [external algorithm architecture](ARCHITECTURE_ALGORITHM_MODULES.md).

### Result storage

The desktop algorithm workflow stores B in a validated temporary artifact retaining input precision, real geometry, trace order and the captured sample axis. An explicit SEG-Y export preserves A headers where applicable, updating required format, output sequence, sample count and cropped Delay fields. A fractional-millisecond origin or an int16 Delay overflow is rejected instead of silently rounded. A is never overwritten.

Each successful result owns one private directory with Part1 (A-B) and Part2 (B). Replacing the current B retains other generated results for the result picker. Close B, replace A or close the owner retires the entire temporary result list. A failed removal caused by an independent Reader remains in a retirement queue and is retried after Reader closure. User-selected B files and exported results are never removed by this cleanup.

External modules use direct argv with bounded output and timeout/cancellation. The host validates trace shape, coordinates, time axis, checksums and finite valid samples before accepting a result. The Windows runner owns the launched process tree with a Job Object; this is a lifecycle control, not a sandbox.

### Processing session

The Windows session owns A metadata, algorithm/parameters, B result history, current B, cancellation, and independent workspace/dataset/processing/result/compare/render generations. A and B have separate Readers, Mapping, Index, cancellation functions, and dataset generations.

Workers receive immutable snapshots and return detached results. UI acceptance requires owner/window, workspace, dataset, processing, result, compare, render generations, sample window, panel dimensions, and source identity to match. Otherwise the result is released and discarded.

`poststack_processing_windows.go` uses a single guarded mailbox, not a blocking completion channel. Progress cannot overwrite a pending completion. Export completion rechecks the source identity as well as generation. Stale exports roll back only the files created by that job, identified by file identity; an unrelated replacement is not deleted. Source checks use open-handle identity plus size/mtime and are not a full-file cryptographic integrity guarantee.

Compare preparation has its own cancellation context. The new `BuildGeometryIndexCachedContextProgress` joins scanner workers before returning on cancellation; the original entry remains a Background wrapper with the same cache version and payload. Prepare/export progress carries the captured HWND and generation, checked again by the UI. Prepare/slice completion rechecks ownership inside the pending-result mutex, so an older worker cannot replace a newer pending result. Reader closure retries retired temporary-result cleanup.

### Compare layer

`cmd/limage/compare_windows.go` remains the single A/B/Delta renderer. Clicking Compare opens its workflow controls, including Load B and Generate B; the main poststack page does not duplicate those actions. Compare continues to provide shared palette/gain/AGC/range, zoom/pan/reset, hover, Ctrl+click physical-trace analysis, and exports.

This is an explicit toolbar/workspace mode of the existing window, with a `返回剖面` action, not a second copy of the renderer. Without usable 3D geometry, **only this processing workspace** uses `TraceOrderGeometry`: virtual Inline 1 and Crossline = source physical trace + 1. It is labeled single-line trace order, is not persisted as original geometry, and preserves generated B's source-trace mapping.

Derived B may cover only A's current sample window. The new `RenderTraceDifferencePairsWindowed` and `ExportDifferenceSectionWindowedContext` align B sample zero with the captured A origin and validate count/interval/Delay/window coverage before subtraction. The original `RenderTraceDifferencePairs` entry retains its previous semantics for other callers.

A and B use a shared display range. Delta is computed from floating-point samples as `A-B`, then mapped to a zero-symmetric palette. Incompatible sample count, interval, time origin, or geometry direction keeps A/B usable while disabling Delta with an explicit reason.

The session may keep multiple B results (for example a temporal filter and an explicitly installed public Copy example) and switch between them without mutating A. Replacing or closing B cancels old work and clears old Compare buffers before accepting a new result.

## Parameter window

Use a modeless native Win32 window for algorithm selection and parameters. It shows A metadata, dynamic controls with default/range/step/unit, current visible sample/trace range, helper path when required, output preview, progress, Preview, Generate B, Cancel, and Open Output Directory. Preview is limited to the visible panel; full generation runs only the requested range.

The current section direction and range come from Compare and are read-only in the parameter window. `预览当前窗口` and `运行处理` both process the captured visible section. Preview B is temporary and not added to the formal result list. Time Slice processing is not available. The result picker and explicit export operate on validated outputs; Compare export can additionally narrow the active view.

## Export and provenance

Export B and Delta using current visible order and sample window. The result dialog can export an optional residual and the primary output as separately identified files.

Existing outputs are never silently overwritten. Provenance records algorithm ID, validated parameters, source identity, sample axis, visible range, and output identity, but never stores amplitude arrays or full SEG-Y data.

## Explicit non-goals

No NMO, velocity analysis, stacking, AVO, Rebin, GPU/Python processing, SEG-Y write-back, cache schema changes, batch schema changes, or changes to ordinary 2D/3D/curved/pseudo-3D behavior.
