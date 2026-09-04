# Limage v1.2.3 — 3D Time Label and Adaptive Time Units

## Changes

- Restored the v1.2.1 3-D orthogonal-slice geometry: Inline, Crossline and Time Slice intersect inside the same seismic cube.
- Reverted the v1.2.2 detached right-side Time Slice layout.
- Moved the `Time Slice ...` text away from the seismic texture into an opaque label box at the upper-right edge of the 3-D workspace.
- Added compact adaptive time formatting throughout compare/volume views:
  - values below 1 second use milliseconds (`250 ms`, `500 ms`);
  - values at or above 1 second use seconds (`1 s`, `1.50 s`, `2 s`).
- Applied the compact formatting to Inline/Crossline time-axis endpoints, Time Slice titles, hover/crosshair status text, cache status text, and volume time-range text.
- Main seismic hover status now uses the same compact time formatting.

## Why

Long labels such as `1500.000 ms` could overlap seismic images or be clipped so only trailing zeros were visible. The adaptive labels are shorter and the 3-D Time Slice title no longer sits on top of seismic texture.
