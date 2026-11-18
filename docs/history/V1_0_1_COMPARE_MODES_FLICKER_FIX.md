# Limage v1.0.1 — Compare modes and flicker fix

## New compare modes

The comparison window now exposes three explicit 3-D post-stack modes:

- **Inline** — select an Inline number and compare A/B along Crossline vs time.
- **Crossline** — select a Crossline number and compare A/B along Inline vs time.
- **Time Slice** — select a sample/time and compare A/B in Inline–Crossline space.

All three modes reuse the same cached SEG-Y geometry index. The Inline/Crossline views only read traces belonging to the requested geometry line and only the selected sample/time window.

The slider and ◀/▶ buttons are mode-aware:

- Inline mode: slider = Inline number.
- Crossline mode: slider = Crossline number.
- Time Slice mode: slider = sample/time.

Synchronized crosshair, annotations, palette changes and synchronized zoom remain available in all three modes. In Inline/Crossline mode annotations are stored in physical spatial-coordinate + sample coordinates rather than screen pixels.

## Why the old compare view flickered

The previous compare implementation invalidated the full window on every mouse movement. Windows could erase the client background before `WM_PAINT`, then Limage redrew both seismic rasters and finally the crosshair/annotation overlays. A/B synchronization therefore produced the visible sequence:

1. background erase,
2. seismic A/B redraw,
3. overlay redraw.

At high mouse-event rates this appeared as continuous flashing.

## Fix

The compare window now uses true GDI double buffering:

1. Create an off-screen compatible DC and bitmap.
2. Draw the complete comparison scene into that back buffer.
3. Draw crosshair, zoom rectangle and annotations into the same back buffer.
4. Transfer the finished frame to the window with one `BitBlt`.

In addition, `WM_ERASEBKGND` is consumed by the comparison window, so Windows no longer clears the background before each frame. `InvalidateRect` calls in the compare path use `erase=FALSE`.

This removes the principal source of white/gray flashing while preserving live synchronized crosshair movement.

## Added seismic-core support

`GeometryIndex.LineTraceNumbers` finds the nearest requested Inline/Crossline and returns traces ordered by the orthogonal spatial coordinate.

`File.RenderTraceIndices` renders an arbitrary ordered trace list with windowed sample I/O, AGC, the recovered legacy percentile gain, and manual Min/Max limits.

Unit tests now verify Inline and Crossline extraction and arbitrary trace-list rendering in addition to the existing geometry/time-slice tests.
