# Limage v1.6.0 — CIGVis visual and interaction presets

v1.6.0 turns the 3-D orthoslice viewer into a preset-driven viewer rather than a single hard-coded style.

## 1. Visual presets

### CIGVis (default)
- No bounding box.
- No ordinary IL / XL / Time Slice rectangle borders.
- No transient 3-D cursor crosshair.
- The only persistent geometry lines are the three true slice intersections:
  - Inline × Crossline
  - Inline × Time
  - Crossline × Time
- Idle intersection color is neutral white/light gray.
- Hover highlights only the two intersections belonging to the hovered slice.
- Drag highlights only the two intersections belonging to the dragged slice.
- Orientation widget and compact time ruler remain available for seismic interpretation.

### Interpretation
- Very faint projected outer hull.
- Only active / hover / dragging slice borders are shown.
- Inactive slice borders are hidden.
- Soft warm intersection lines.

### Standard
- Full 12-edge volume box.
- All slice borders visible with active / hover / dragging hierarchy.
- IL / XL range hints retained.
- Intended for geometry checking and QC rather than publication-like viewing.

## 2. Interaction presets

### CIGVis (default)
The native Win32 viewer now follows the interaction semantics of the public CIGVis/VisPy viewer as closely as practical:

- Left drag: rotate turntable camera.
- Ctrl + Left drag: drag the slice under the pointer; the whole slice is pickable, not only its border.
- Shift + Left drag: pan camera.
- Right drag: zoom camera.
- Mouse wheel: zoom camera.
- D: toggle direct slice-drag mode. When ON, a normal left drag on a slice moves it.
- Space: reset camera.
- Single click without meaningful mouse movement: preserve Limage linked-slice positioning.
- Double click a slice: 2-D full view.
- Esc / double click: return to 3-D.

### Classic
- Right drag: rotate camera.
- Mouse wheel: zoom.
- Left drag near a slice edge: move slice.
- Left click: linked slice positioning.

## 3. Existing display controls retained

- View size: Compact / Standard / Large / Extra Large / Fill.
- Aspect: geometry trend / balanced.
- 20 legacy Fimage color maps, with white-gray-black and black-gray-white at the top of the 3-D palette list.
- Time Slice reduced contrast and restrained opacity.
- Software Z-buffer and picking use the same stretched IL/XL/T geometry as the visible projection.

## 4. Renderer note

The CIGVis preset reproduces the visual hierarchy and interaction semantics; it does not embed CIGVis, Python, PySide6, VisPy, or OpenGL. Limage v1.6.0 remains a native x64 Win32 application with a CPU software texture renderer and camera-aware Z-buffer.

A future GPU backend is still the route for pixel-level similarity in texture filtering, antialiasing, and very smooth camera motion.
