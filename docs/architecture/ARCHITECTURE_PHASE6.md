# Limage Phase 6 architecture

## Restored Crooked workspace

Crooked and pseudo-3D top-level windows now pass through one restore helper. An iconic window receives `SW_RESTORE`; a normal or maximized window receives `SW_SHOW`. The helper then updates, foregrounds, and focuses the requested workspace. Opening Crooked no longer preserves a stale minimized state, and returning from pseudo-3D does not recreate the Crooked session.

## Pseudo-3D picking and 2-D navigation

`internal/pseudo3d` owns Win32-free projection queries alongside the CPU rasterizer. `Pick` tests the same projected curtain triangles used by rendering, applies the same perspective-correct interpolation, and chooses the smallest z-buffer depth. Its result contains curtain identity, accumulated-distance U, time fraction, real XY, and depth. `ProjectFrame`, `ProjectRange`, `ProjectCurtain`, and `UnprojectTimePlane` reuse the exact camera, FOV, pan, zoom, and X/Y/Z axis factors.

The Win32 adapter keeps a curtain-index-to-project-line/segment table with each completed render. A picked segment retains its clipped `PositionStart/PositionEnd` and support traces, allowing U to map back to the nearest original SEG-Y trace. Ctrl+double-click hides but does not destroy pseudo-3D, restores Crooked, activates the line, centers the horizontal view when required, and synchronizes the trace and time cursor. If that line is still preparing, a project-generation-scoped navigation target is applied after its Geometry and Reader become ready.

Hover uses the same picker at no more than 30 updates per second. It updates only status text and a projected dynamic outline; it never rebuilds textures or reopens a Reader.

## AOI editing and Geometry viewport

Ctrl+drag hit-tests the projected base/top edges and vertical corners of the XY-Time frame. A side changes one real-coordinate bound, a corner changes two, and the opposite side remains fixed. Without an AOI, full project bounds are the starting rectangle. Unprojection occurs on the time plane of the selected edge, values are clamped to project bounds, and each axis retains a minimum one-percent project span.

Dragging paints a throttled projected preview only. Mouse release commits one range generation through the Crooked owner; Esc discards the draft. Existing in-range checkbox choices are restored by line ID, newly eligible lines default to selected, stale Reader/render generations are cancelled, and out-of-range lines never enter the amplitude queue. No XY values are written to the workspace log.

The upper Geometry map has a session-only viewport separate from the pseudo AOI. Zoom Range uses the AOI with five-percent display padding and follows later pseudo-frame changes. Map Reset returns to full project bounds and stops following. Rendering, clipping, hit testing, current markers, and range conversion all use the same viewport transform. The lower 2-D seismic section is unchanged.

## Shared display styles

Geometry and pseudo-3D share the Volume3D order: Clean, CIGVis, Interpretation, and Standard. Standard is the migration default and preserves the Phase 5 appearance. Clean removes reference grids, frames, labels, cursor markers, and curtain outlines while retaining trajectory/seismic data; editing feedback appears temporarily while an AOI edge is hovered or dragged. CIGVis and Interpretation progressively add active-line, AOI, coordinate, and interpretation emphasis. Styles affect overlay drawing only, never palette indices, gain, clip, texture pixels, projection, or depth tests.

Style persistence is isolated in `crooked_style.json` v1. `crooked_view.json` v1, `pseudo_view.json` v2, `volume_view.json` v1, Recent records, `.cidx`, and `.lidx` remain unchanged.

## Frozen components and release

The regular 3-D renderer and its camera, projection, slice interaction, shortcuts, and parameter schema are untouched. Phase 6 ships independently as `Limage_v1.9.4_pseudo_linked_x64.exe` using the Windows GUI PE subsystem. The retained indexed-texture budget remains 192 MiB and Reader concurrency remains two.
