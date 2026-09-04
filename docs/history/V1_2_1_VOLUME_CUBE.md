# Limage v1.2.1 — Wider Workspace + 3-D Orthogonal Cube

## Wider unified workspace

The default unified A / 比 / 差 workspace is wider (1560 px) so long SEG-Y paths
are easier to read. In A-only mode most of the path row is assigned to A. After
B is loaded, the same row is split equally between A and B and both paths use a
center ellipsis rather than being clipped at the right edge.

The minimum resizable width is also increased so toolbar/path controls remain
usable.

## 体: 3-D orthogonal slice view

The **体** entry now opens a 3-D corner/cube style view by default instead of
three unrelated rectangles.

Three real seismic textures intersect inside one projected volume:

- **Inline plane**: Crossline × Time at the selected Inline;
- **Crossline plane**: Inline × Time at the selected Crossline;
- **Time Slice plane**: Crossline × Inline at the selected time.

The rendering is a lightweight CPU affine texture projection implemented in the
native x64 executable. It does not load a full seismic cube into RAM: the
vertical sections still use line-based 64-bit SEG-Y I/O and the horizontal
slice still uses the existing Time-Slab cache.

### Linked interaction

The top controls still step Inline, Crossline and Time independently. The three
planes move to the new physical position and are re-composited into the same
cube.

Mouse hover is mapped back from the projected plane to seismic coordinates.
Clicking:

- the Inline plane changes Crossline + Time;
- the Crossline plane changes Inline + Time;
- the Time Slice changes Inline + Crossline.

The linked crosshair is projected onto all three planes.

### Display modes

3-D is the default. A **平铺** button toggles back to the previous three-panel
2-D layout. When in the old layout the button changes to **3D**.

### Visual structure

The cube includes:

- a light gray 3-D volume frame;
- blue outlines around the three active slices;
- current Inline / Crossline / Time labels;
- Inline, Crossline and time-range axis labels;
- the selected Limage color map on all three textures.

## Compatibility

All existing v1.2.0 functionality remains available, including A-only browsing,
比 / 差, spectrum QC, automatic geometry detection, cached Time Slice browsing,
Q/W gain and E/R palette shortcuts.
