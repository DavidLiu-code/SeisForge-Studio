# Limage v1.4.1 — Minimal volume view refinement

This version simplifies the 3-D volume display to reduce visual clutter and move closer to the cleaner CIGVis-style presentation.

## Changes

1. **Minimal context frame**
   - Replaced the full bright cube wireframe with a sparse, faint guide frame.
   - Keeps depth cues while reducing distracting lines.

2. **Only the active slice is outlined**
   - Inactive slice borders are hidden by default.
   - Active slice keeps a bright blue border.
   - Hover uses yellow; dragging uses orange.

3. **Intersection lines remain the main geometric cue**
   - The three slice intersection lines are kept in yellow.
   - Line width slightly reduced to balance clarity and simplicity.

4. **Removed overlapping in-scene labels**
   - Removed large Inline / Crossline text drawn inside the 3-D scene.
   - Removed the extra time badge in the top-right of the 3-D scene.
   - Slice state is already available in the control bar and status line.

5. **More compact orientation widget**
   - Reduced size and visual weight of the IL / XL / T indicator.

## Result

The 3-D view now emphasizes:
- seismic slice textures,
- slice intersections,
- the currently active slice,
- a lighter spatial context.
