# Limage v1.7.0 — Dual 3D Compare Workspace

## Overview

v1.7.0 adds a dual-volume 3-D comparison workspace while preserving the existing single-volume CIGVis-style viewer.

## Main changes

### 1. Slightly higher initial composition
The automatic fit center is moved slightly upward relative to v1.6.8. This keeps the improved coordinate layout while reducing the sense that the seismic cube sits too low in the window.

### 2. `比` inside the Volume viewer
The second-row toolbar now contains a `比` button.

- If B already exists in the 2-D compare workspace, Limage reuses that SEG-Y path.
- Otherwise, it opens the SEG-Y file picker.
- Once B is ready, the 3-D workspace splits into A (left) and B (right).
- `单` returns to the single-A view without destroying the parked B state; reopening compare is immediate.

### 3. Independent left/right 3-D state
A and B independently store:

- Inline / Crossline / Time slice position
- Azimuth / Elevation
- FOV
- Zoom / overall scale
- Pan
- IL / XL / Z display scale
- Aspect mode
- active slice

Click a side to make it active. The top slice/camera controls and keyboard shortcuts then act on that side only.

Palette, gain, visual style and coordinate visibility remain shared comparison conventions. Q/W gain and palette changes refresh both A and B.

### 4. `与左图一致`
When compare mode is active, a `与左图一致` button appears.

It maps the right B view to the left A view by copying:

- Camera azimuth/elevation
- FOV
- Zoom and Pan
- X/Y/Z scale factors
- Aspect mode
- active slice orientation

Position matching uses physical values rather than raw indices:

- IL -> nearest valid B inline
- XL -> nearest valid B crossline
- Time -> converted through physical time and B sample interval

This allows matching even when A and B have slightly different coordinate grids or sample intervals.

### 5. Compare rendering
Both scenes are rendered independently with their own software Z-buffer/camera transform. A compact label identifies `A 左图` and `B 右图`; the active side is marked in blue. A very light workspace separator is used between the two views and is not part of the seismic geometry.

## Interaction example

1. Open a 3-D SEG-Y volume and click `体`.
2. Click `比` and choose B.
3. Rotate/zoom the left and right sides independently.
4. Click the right side and change FOV or X/Y/Z scales; the left side remains unchanged.
5. Click `与左图一致` to immediately return B to A's slice position and camera/view transform.

## Notes

- The dual viewer still uses Limage's native CPU software rasterizer and Z-buffer.
- The existing licensing system and saved default-view configuration remain compatible.
- A/B gain and palette are intentionally shared to keep visual comparison meaningful.
