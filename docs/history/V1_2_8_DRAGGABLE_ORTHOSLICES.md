# Limage v1.2.8 – draggable 3-D orthogonal slices

## Changes

### 1. Time Slice now moves through the cube
The Time Slice plane is positioned from the current sample/time coordinate, exactly like the Inline and Crossline planes are positioned from the current geometry coordinate. Changing Time with the toolbar or by dragging its plane moves the horizontal plane through the 3-D cube.

### 2. Unified range information below the cube
The lower information row now reports all three volume axes in the same location and style:

- `Inline <min> → <max>`
- `Crossline <min> → <max>`
- `Time <start> → <end>`

Time uses Limage's adaptive units: values below 1 s are shown in ms; values at/above 1 s are shown in seconds.

### 3. Direct slice dragging
In 3-D volume mode, move the pointer close to the border of a slice:

- Inline border -> diagonal double-arrow cursor
- Crossline border -> opposite diagonal double-arrow cursor
- Time Slice border -> vertical double-arrow cursor

Press and drag to move that slice continuously. Release the mouse to snap to the nearest valid Inline, Crossline, or time sample.

Inline/Crossline display updates are throttled to approximately 25 fps while dragging. For Time Slice, a cached slab is rendered live; if the requested time is not cached, the plane outline still moves immediately and the correct slice is loaded on mouse release with the wait cursor.

The original top arrow buttons remain available for one-step fine adjustment.
