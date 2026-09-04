# Limage v1.0.2 — Compare Hover Flicker Fix

This release focuses only on eliminating the remaining flicker when the mouse moves over the A/B comparison images.

## Root cause in v1.0.1

v1.0.1 already suppressed `WM_ERASEBKGND` and used an off-screen bitmap, but every `WM_MOUSEMOVE` still called a whole-window `InvalidateRect`. As a result, both seismic panels, axes, annotations, crosshair and status-related UI were repeatedly recomposed at mouse-event frequency. The screen no longer flashed white, but the repeated full-frame GDI work could still look like image flicker/jitter.

## v1.0.2 rendering architecture

The comparison window now has a persistent static scene cache:

`seismic raster + axes + committed annotations -> persistent compatible bitmap`

The static bitmap is rebuilt only when content really changes, such as:

- switching Inline / Crossline / Time Slice;
- changing the line or time slice;
- changing palette, gain or amplitude limits;
- zoom/origin;
- adding/deleting an annotation;
- resizing the compare window.

Passive mouse hover does **not** rebuild this bitmap.

### Crosshair strip invalidation

For every mouse move Limage remembers the old crosshair world coordinate, computes the new world coordinate, and invalidates only:

- old vertical strip;
- old horizontal strip;
- new vertical strip;
- new horizontal strip;

for both A and B panels. Each strip is only a few pixels wide. `BeginPaint` restores those dirty pixels from the static bitmap and draws the synchronized crosshair over them.

There is no whole-window `InvalidateRect` in the passive-hover path.

### Persistent rather than per-frame buffering

v1.0.1 created/deleted a full compatible bitmap for every paint. v1.0.2 keeps the compatible DC/bitmap alive for the compare window and reuses it. This removes repeated GDI bitmap allocation during mouse movement.

### Status throttling

Crosshair graphics remain unthrottled, but the coordinate text in the status control is updated at most once every 40 ms (~25 Hz). This avoids repainting a child STATIC control at raw `WM_MOUSEMOVE` frequency.

### Annotation and zoom dragging

Rectangle/ellipse/line and zoom previews can still request a larger overlay refresh while actively dragging, but they restore from the cached seismic scene; they do not regenerate the seismic raster.

### Background erase

`WM_ERASEBKGND` remains suppressed, so the previous white/gray erase flash cannot return.

## Modes retained

- Inline comparison
- Crossline comparison
- Time Slice comparison
- synchronized A/B crosshair
- synchronized zoom
- shared comparison palette
- rectangle / ellipse / line annotations
- automatic IL/XL header detection
- accelerated time-slice geometry/slab cache

## Build

- Product: Limage
- Version: 1.0.2
- Target: Windows x86-64
- Format: PE32+ Windows GUI

The Windows GUI cannot be interactively launched in the Linux build environment, so final subjective hover smoothness should be confirmed on Windows. The code path that previously caused whole-window repaint on every mouse move has been removed.
