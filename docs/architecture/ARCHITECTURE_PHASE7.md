# Limage Phase 7 architecture

## Geometry viewport gestures

The upper Crooked Geometry map now has one direct navigation gesture. A left-button press starts as a pending click; movement of at least five pixels on both axes promotes it to a rectangular viewport zoom. The selected world rectangle is expanded to the map aspect ratio, clamped to project bounds, and stored only in `geometryViewport`. A short click is dispatched after the Windows system double-click interval and keeps the existing line/trace selection behavior; a detected double-click cancels that pending click before restoring complete project bounds, so reset cannot accidentally switch lines. The former Zoom Range, Map Reset, and AOI-follow controls have been removed. The separate Range/Clear Range workflow still owns `pseudoRange`, so map navigation never changes pseudo-3-D loading.

## Linked 2-D and pseudo-3-D cursor

`internal/pseudo3d.LinkedCursor` carries project generation, line identity, original trace/CDP, calibrated XY, accumulated distance, sample, and time without amplitude data. Crooked publishes a changed trace/sample at no more than about 30 updates per second and forces the final position at mouse release or explicit navigation. The pseudo adapter rejects foreign generations, activates the matching list line, and selects/queues it only when its calibrated position lies inside the current AOI.

The linked marker is a dynamic overlay: `ProjectPoint` reuses the renderer projector to draw a red vertical trace line and time point over the retained scene bitmap. It does not rebuild indexed textures. Hidden pseudo windows retain the newest target, asynchronously prepared lines apply it when ready, and an AOI-external target changes status only. Clean style continues to hide cursor/highlight overlays.

## Incremental AOI transaction

AOI commits no longer destroy `pseudoSession`, restart workers, or discard camera and selection state. `updatePseudoRangeInPlace` advances an atomic range generation, invalidates affected line tokens, recomputes eligibility from cached Navigation/Geometry, and replans indexed textures under the existing 192 MiB budget. The two Reader workers skip queued stale generations before opening a file; in-flight stale work closes its Reader and cannot update UI state.

Exact cached curtain spans are retained per line. Range contraction crops and resamples their horizontal texture interval in memory and therefore opens no SEG-Y Reader. Unchanged spans reuse their texture directly. Expansion immediately renders the retained covered span, while only uncovered or boundary-changed runs enter the Reader queue; exact results replace previews as each line completes. Session cache eviction is least-recently-used at line granularity and counts live plus cached indexed bytes against the same 192 MiB budget. Gain changes invalidate every cached amplitude texture, while palette changes continue to recolor indices without rebuilding them.

The first range frame is rendered at half resolution and stretched through the existing double-buffered paint path. Projecting, picking, AOI handles, and linked markers always use the full scene rectangle, so interaction remains pixel-aligned while a preview is visible. Status reports reused and updating line counts, and workspace logging records only those counts and generations, never XY or seismic samples.

## Overlay lifetime and frozen components

Hover, linked cursor, and AOI overlays save the target DC, intersect its clip region with the pseudo scene rectangle, and restore it after painting. Their invalidation rectangles are also intersected with the scene. Every camera, FOV, axis, style, range, and scene change clears the old projected hover before requesting a new frame, preventing orange/yellow outlines from entering or remaining in toolbar and status controls.

The regular Volume3D renderer, camera, projection, shortcuts, `volume_view.json` v1, `crooked_view.json` v1, `pseudo_view.json` v2, 20 palettes, gain/clip defaults, `.lidx`, and `.cidx` formats are unchanged. Phase 7 ships independently as `Limage_v1.9.5_pseudo_sync_x64.exe` with the Windows GUI PE subsystem.
