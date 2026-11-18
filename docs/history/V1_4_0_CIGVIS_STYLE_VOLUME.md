# Limage v1.4.0 — CIGVis-inspired 3D volume viewer

This release keeps Limage's native Windows x64 / SEG-Y / geometry-cache / Time-Slab architecture, but redesigns the **体** view around the visual interaction model used by CIGVis.

## Why this change
CIGVis treats Inline, Crossline, and Time slices as real axis-aligned 3-D image nodes under a turntable camera. Its slice nodes use depth testing and can be dragged along their normal direction. Limage now follows the same high-level visual model without requiring Python, PySide6, or VisPy at runtime.

## New camera model
- Rotatable orthographic turntable camera.
- Right mouse drag: rotate camera.
- Mouse wheel: zoom.
- Space: reset to the default interpretation view.
- Camera azimuth/elevation are shown in the status line.
- Orientation widget follows the camera automatically.

## Real depth relationship
- The software Z-buffer remains enabled.
- Depth is now computed from the current camera direction rather than a fixed projection.
- Front/back relationships update automatically when the camera rotates.
- Mouse picking uses the same depth relationship.

## Slice-node behavior retained
- Inline / Crossline / Time Slice remain true 3-D orthogonal planes.
- Edge hover highlights the slice.
- Dragging an edge moves that slice along its normal direction.
- Three explicit intersection lines remain visible.
- Double-click a slice for a full 2-D view; double-click/Esc returns to 3-D.

## Display style
- Near-opaque seismic textures.
- Active slice: strong outline.
- Hover/drag state: highlighted outline.
- Light bounding box.
- Dynamic right-side time ruler.
- Compact IL / XL / T orientation widget.

## Dependency policy
Limage does **not** embed CIGVis, Python, PySide6, or VisPy. The implementation is native and dependency-free for end users. CIGVis was used as a design reference for the 3-D interaction model.

## Attribution
Visualization design reference:
- Jintao Li, Yunzhi Shi, Xinming Wu, CIGVis.
- Repository: https://github.com/JintaoLee-Roger/cigvis
- License: MIT.
