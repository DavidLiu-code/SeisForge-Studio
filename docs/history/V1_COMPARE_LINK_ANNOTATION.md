# Limage v1.0.0 — Comparison Interaction Update

## Added in this build

### 1. Colorbar control inside comparison window
- A dedicated legacy colorbar combo box is available directly in the A/B comparison window.
- All 20 reconstructed Fimage/Limage palettes are available.
- A/B use the same comparison palette by default for fair visual QC.
- The comparison palette is local to the comparison window and does not overwrite the main viewer palette.
- Section comparison recolors from cached 8-bit indices without rereading SEG-Y.
- Time Slice comparison recolors from cached time-slice slabs whenever available.

### 2. Linked crosshair
- `十字联动` is enabled by default.
- Moving the mouse over either A or B draws the corresponding crosshair in both views.
- Synchronization is based on seismic coordinates, not raw screen pixels.
- Section mode uses Trace/Sample coordinates and reports time in milliseconds.
- Time Slice mode uses Crossline/Inline coordinates and reports the current slice time.
- No seismic file I/O is performed simply to move the crosshair.

### 3. Synchronized annotations
Three annotation tools are included:
- `矩形`
- `椭圆`
- `线段`

Annotations created on A are immediately shown at the same seismic coordinates on B, and vice versa.
They are stored in data coordinates, so they remain registered when the window is resized or the display is synchronously zoomed.

`清除标注` removes the annotations from the comparison session.

### 4. Interaction model
- No annotation tool selected: mouse drag performs synchronized A/B zoom.
- Rectangle/Ellipse/Line selected: mouse drag creates an annotation instead of zooming.
- Linked crosshair continues to update while an annotation is being drawn.

## Version
The application version shown to the user is now `Limage v1.0.0 [x64]`.

## Contact
`GitHub Issues`
