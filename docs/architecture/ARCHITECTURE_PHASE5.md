# Limage Phase 5 architecture

## Spatial AOI ownership

The Crooked workspace owns a session-only `pseudo3d.XYRange`. It is cleared whenever a project is opened, replaced, or the workspace is reset. It is deliberately absent from Recent records, `crooked_view.json`, and `pseudo_view.json`.

The upper XY map remains a full-project navigation view. Its Range mode converts a screen drag into an axis-aligned world-coordinate rectangle and paints that rectangle as a transient overlay, without rebuilding the static seismic/map cache. The lower active 2-D section is never cropped by the pseudo-3D AOI.

## Two-stage filtering and clipping

Pseudo-3D first tests DAT navigation or an already available calibrated geometry with `PolylineIntersectsRange`. A known outside line is marked `范围外`, remains unchecked, and never enters the SEG-Y Reader queue. Lines with neither source remain eligible as `范围判断中` until their headers have been scanned.

After calibrated detailed geometry is available, `ClipTrajectory` uses acquisition-order segment clipping to produce independent `TraceRun` values. A line that leaves and re-enters the rectangle becomes multiple curtains; gaps are never joined. Each run contains only clipped XY vertices plus the real support traces needed by `RenderTracePositions`. At most one adjacent outside trace per boundary can be read for interpolation, but no out-of-range geometry or amplitude pixel reaches the scene.

Texture planning considers only coarse AOI candidates. A line allocation is divided between its exact runs in proportion to in-range length, while the retained global indexed-texture budget remains 192 MiB. The active eligible line loads first; remaining lines are ordered by estimated in-range length and then natural project order. Two Reader workers and generation checks remain the lifetime boundary.

Changing or clearing the AOI increments the range generation. An open pseudo window is cancelled and rebuilt against a snapshot of the new generation, so stale Readers, textures, and render results cannot update the current window. Workspace logging records only range set/clear and candidate counts, not XY values.

## 3-D interaction parity

`pseudo3d.Camera` now has independent X, Y, and vertical/Z display factors. Raw survey coordinates and AOI tests are never scaled; the factors affect projection only. The pseudo window exposes the same factor table, FOV options, and applicable shortcuts as Volume3D: X/Y/Z, R, F, Q/W, E/Shift+E, A, and Space. Keyboard routing occurs in the application message loop so shortcuts remain active with ListView, button, or combo focus.

Mouse interaction matches Volume3D CIGVis mode: left drag rotates, Shift+left drag pans, right drag and the wheel zoom. D and Ctrl+left report that pseudo-3D has no orthogonal slices instead of performing an unrelated action.

`pseudo_view.json` v2 stores the saved camera and X/Y/Z display factors. A v1 file is read in place with X=Y=1 and `vertical_scale` migrated to Z. Only the Set Default action writes the file; ordinary interaction changes session state. `crooked_view.json` v1 and `volume_view.json` v1 are unchanged.

## Frozen components and release

The regular volume renderer, its camera/projection formulas, `volume_view.json`, palette order, gain/clip defaults, geometry caches, and SEG-Y data are unchanged. Phase 5 ships independently as `Limage_v1.9.3_pseudo_aoi_x64.exe` using the Windows GUI PE subsystem.
