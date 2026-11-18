# Limage v1.3.3 — 3-D volume visual / interaction refinement

## Focus
This release does not add processing features. It refines the 3-D orthogonal slice viewer to make its visual hierarchy and interaction closer to a professional seismic interpretation viewer.

## 1. Depth Test / Depth Write
- The 3-D slice compositor continues to use a per-pixel software Z-buffer.
- Camera depth is now derived dynamically from the current orthographic projection basis:
  - screen-X projection row
  - screen-Y projection row
  - camera direction = cross(screen-X, screen-Y)
- Therefore slice visibility is independent of the order in which Inline, Crossline and Time Slice are submitted to the renderer.
- This is a CPU software implementation of the same depth-test/depth-write principle used by a GPU renderer.

## 2. Slice opacity
- Seismic planes are now effectively opaque by default.
- This removes dense gray texture stacking at intersections.
- Current / hover / dragging state is communicated using borders instead of strong plane transparency.

## 3. Visual hierarchy
- Bounding box: very light gray, 1 px.
- Inactive slice border: thin gray-blue.
- Active slice border: bright blue, 2 px.
- Hover slice border: yellow, 2 px.
- Dragging slice border: orange, 3 px.
- Slice intersections: yellow, 3 px.

## 4. Hover / dragging feedback
- Moving the mouse near a slice edge highlights that slice in yellow.
- Dragging changes the slice border to orange.
- Inline / Crossline / Time controls continue to update during dragging.
- Status shows Hover or Dragging state.

## 5. Time presentation
- The right-side time ruler remains the primary time-position indicator.
- The previous large Time Slice box is reduced to a compact `T xx` badge.
- Bottom range labels now show only `IL min–max` and `XL min–max`; time range is no longer duplicated there.

## 6. Orientation widget
- The lower-left widget is compact: `IL`, `XL`, `T`.
- Directions are derived from the actual current projection basis, so the widget is ready to follow future camera rotation.

## 7. Slice / Cursor information
The status line preserves the explicit distinction:
- `Slices: IL ... | XL ... | Time ...`
- `Cursor: IL ... | XL ... | Time ...`

## 8. Depth-aware mouse picking
- 3-D hit testing now uses the same depth calculation as rendering.
- If multiple slices overlap under the cursor, the physically nearer slice wins.
- Edge-drag hit testing also uses depth as a tie breaker.

## 9. Double-click 2-D full view
- Double-click Inline / Crossline / Time Slice in the 3-D viewer to open that slice as a large 2-D view in the same window.
- Double-click again, or press `Esc`, to return to the 3-D view.
- No SEG-Y data is reloaded for this transition.

## Projection
The default remains an interpretation-oriented orthographic/oblique projection. A perspective/orthographic user toggle and free camera rotation can be added later if required.
