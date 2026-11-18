# Limage v1.9.0 Phase 2 architecture

## Purpose

Phase 2 replaces the Phase 1 Crooked placeholder with an independent post-stack crooked-line workspace and exposes progress while Volume builds its regular-grid index. The existing Line2D and Volume renderers, `volume_view.json` v1, `.lidx` v1, palette catalog, gain/clip defaults, camera and interaction formulas are unchanged.

## Crooked data flow

```text
Home / Recent / Crooked drop
  -> Application.OpenPath(KindCrooked, path)
  -> DatasetManager.Open
  -> WorkspaceManager.Open(KindCrooked)
  -> crookedWorkspaceAdapter.Open
  -> create hidden Crooked shell
  -> Dataset.OpenReader
  -> BuildCrookedCachedProgress
       -> auto-select Ensemble / Source / Group X/Y, or use custom bytes
       -> scan 240-byte trace headers only
       -> build CrookedLineGeometry and cumulative distance
       -> load/save .cidx v1 metadata cache
  -> PostMessage generation-tagged result
  -> render positioned traces with an independent Reader
```

The explicit Crooked route never calls `loadSelectedFile`, `rerender`, `workspaceSyncAFromMain` or the ordinary Line2D adapter. Missing coordinates leave the Crooked shell active with Retry and Header Settings controls. Switching or closing increments both async generations; stale readers/results are closed or discarded.

`KindAuto` remains conservative. Regular3D is tested first. If it fails, a sampled coordinate trajectory is recommended as Crooked only when at least 90% of sampled traces have valid X/Y, at least eight unique positions exist, the 95th-percentile step is at most ten times the median step, and sinuosity or perpendicular deviation proves that the line is not effectively straight. A straight XY line remains Line2D. Explicit workspace requests are never reclassified.

## Geometry and cache

`segy.TraceCoordinateSpec` uses 1-based trace-header bytes. Defaults are CDP 21, scalar 71, units 89 and automatic candidates Ensemble 181/185, Source 73/77, Group 81/85. Positive scalars multiply, negative scalars divide and zero means one. Angular coordinate units are retained without projection conversion.

`geometry.CrookedLineGeometry` stores only trace numbers, CDP, X/Y, cumulative distance, units and the selected header specification. It implements the unchanged `Geometry` interface. `TraceLocation` gains backward-compatible CDP/distance fields. Helpers provide distance lookup, nearest XY trace, bounds, CDP monotonicity and display sampling.

The `.cidx` v1 key includes absolute source path, file size, modification time and every configured trace-header byte. It is separate from and does not modify `.lidx` v1. Neither cache contains amplitude samples.

## Crooked UI ownership

The dedicated Win32 window owns one `crookedSession`: Dataset, workspace generation, independent Reader, immutable geometry, current trace/sample, fractional visible window, axis/palette/gain choices, raster buffers and async state. Map and section share the current geometry position.

- Top 30%: fitted XY polyline, bounds and current-trace marker.
- Bottom 70%: positioned seismic raster with time and Trace/CDP/Distance axes.
- Distance is the default. Monotonic CDP uses proportional CDP positions; non-monotonic CDP stays in acquisition order and is labelled accordingly.
- Map clicks select the nearest trace. Section hover/click updates map and time cursors. Wheel zooms around the pointer and drag pans the horizontal window.
- `crooked_view.json` v1 stores only Crooked axis, palette, gain and coordinate-header settings.

## Volume progress

Volume uses `BuildGeometryIndexCachedProgress`. Worker callbacks post `WM_VOLUME_INDEX_PROGRESS` with the `volumeGen` token; only the current token and workspace can update controls.

- 0-10%: file open and IL/XL detection.
- 10-90%: cache restore, fast regular inference or exhaustive header scan.
- 90-100%: initial time slice and scene preparation.

The progress bar is hidden/reset on ready, error, switch, stale result and close. `workspace_trace.log` records begin, cache hit, ready, error and stale discard events, not percentage messages or seismic content.

## Compatibility

- Recent numeric modes remain `Line2D=1`, `Volume3D=2`, `Crooked=3`.
- `volume_view.json` remains schema v1 and has no Crooked fields.
- Existing `.lidx` files, license state, exports, spectrum, A/B/difference and shortcuts are untouched.
- Phase 2 Crooked is single-dataset browsing; Crooked A/B, difference, spectrum, export, GIS, projection conversion and interpretation objects remain deferred.
