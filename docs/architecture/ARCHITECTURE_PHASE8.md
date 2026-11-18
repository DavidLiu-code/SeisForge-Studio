# Limage Phase 8 architecture

## Independent Geometry overlays

The Crooked Geometry map keeps `pseudoRange` and `geometryViewport` as independent state. Painting now draws the persistent pseudo-3-D AOI first, then an active AOI edit rectangle, and finally the Geometry zoom preview. The zoom preview has its own pale one-pixel dashed renderer, so it cannot replace or be confused with the blue two-pixel AOI. A completed zoom changes only `geometryViewport`; double-click reset, line switching, and repeated zooms do not mutate `pseudoRange`. Clean style hides persistent guides but temporarily exposes the rectangle for the interaction currently in progress.

## Double-click and camera gesture state

Pseudo-3-D uses an explicit pending-left-drag state. A left press becomes camera rotation or Shift-pan only after five pixels of screen movement; releasing before that threshold does not enqueue a render. A plain double-click therefore performs the retained Z-buffer curtain pick without camera jitter, then restores the Crooked window and synchronizes line, original trace/CDP, calibrated XY, and time. Ctrl remains reserved for dragging projected AOI frame edges or corners. Empty-space double-click stays in pseudo-3-D and reports that no loaded curtain was hit.

## Persistent progress completion

The pseudo toolbar now separates transient status text from a compact progress result. `makePseudoProgressView` converts selected, loaded, failed, and refine state into a monotonic percentage and one of `准备中`, `加载`, `精修`, or an explicit `完成` result. The completion label remains visible beside the full progress bar even when hover, linked-cursor, or camera messages replace the long status text. A failed subset uses the native progress error state and reports both displayed and failed counts.

## Shared display style

Pseudo-3-D exposes the same Clean, CIGVis, Interpretation, and Standard order as Crooked Geometry. Both selectors call one shared style transaction, update each other programmatically, and persist through the existing `crooked_style.json` v1. Style changes invalidate Geometry guides and enqueue only a pseudo scene redraw from retained indexed textures; no SEG-Y Reader or amplitude texture rebuild is involved. Standard remains the factory default.

## Frozen components and delivery

The regular Volume3D renderer and camera, `volume_view.json` v1, `crooked_view.json` v1, `pseudo_view.json` v2, `crooked_style.json` v1 schema, 20 palettes, gain/clip defaults, `.lidx`, and `.cidx` remain unchanged. Phase 8 ships independently as `Limage_v1.9.6_pseudo_polish_x64.exe` with the Windows GUI PE subsystem. Time-range clipping and named AOI/camera bookmarks remain deferred.
