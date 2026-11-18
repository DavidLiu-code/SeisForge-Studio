# Limage v1.5.1 — Final simple 3-D volume visual specification

This release implements the final simplified visual hierarchy for the native Limage 3-D orthoslice viewer.

## Final visual hierarchy

**Seismic texture > active slice > slice intersections > time/orientation aids > bounding box**

### Bounding box
- Minimal mode now draws only the projected outer silhouette of the finite volume.
- Internal projected box lines are removed completely in the default minimal mode.
- Standard mode can still show the complete 12 true volume edges for users who need additional spatial context.
- Default outline is extremely light gray.

### Slice borders
- Inactive: 1 px light gray-blue.
- Active: 2 px blue.
- Hover: 2 px yellow.
- Dragging: 2 px orange in minimal mode, 3 px in standard mode.

### Slice intersections
- Always available as a quiet, soft yellow 1 px guide at rest.
- Hover strengthens to 2 px yellow.
- Drag strengthens to orange.

### Slice opacity
- Inline / Crossline: ~0.98, active vertical slice 1.0.
- Time Slice: ~0.88 even when active, so the large horizontal plane does not dominate the scene.

### Display aspect
Literal survey sampling is no longer mapped directly into screen geometry for extreme surveys.

- IL:XL trend is preserved.
- Extreme horizontal anisotropy is capped at 2.6:1.
- Vertical T display size is about 0.52 of the dominant horizontal dimension.

For a very long IL survey this gives a display-space relationship close to:

    dominant horizontal : short horizontal : T
    1.00                : 0.38–0.45        : ~0.52

This keeps the seismic cube readable instead of turning it into a thin wall.

### View size
Five fit presets are now available:
- 紧凑
- 标准
- 大
- 超大 (default)
- 填充

### Cursor guidance
In default minimal 3-D mode the transient mouse crosshair is drawn only on the active/hovered slice instead of all three slices. Standard mode retains all three crosshairs.

### Existing behavior preserved
- Z-buffer depth testing and picking use the same stretched display geometry.
- Right-drag camera rotation.
- Mouse-wheel zoom.
- Space resets the camera.
- Slice-edge dragging.
- Double-click slice -> 2-D full view.
- Existing licensing and SEG-Y/cache logic are unchanged.
