# Limage v1.2.4 — loading progress, multithreaded I/O, Compare toggle, double-click Zoom toggle

## 1. First-load progress

The unified workspace now shows a native Windows progress bar while the first 3-D dataset is prepared.
The stages are:

1. automatic Inline/Crossline header detection;
2. geometry-index scan / cache restore;
3. first Time-Slice slab load;
4. first Inline/Crossline render.

When a persistent geometry cache is available, the geometry stage completes immediately.

## 2. Multi-threaded loading

The display renderer now decodes trace windows in parallel. `RenderWithOptions` and `RenderTraceIndices` use an adaptive worker count (up to 8 by default). Each worker owns a contiguous screen-column range so nearby/repeated source traces remain local while `os.File.ReadAt` permits safe concurrent file access.

Geometry-index building remains multi-threaded (up to 16 workers), and A/B geometry indexes are built concurrently. Time-Slice slabs remain multi-threaded (up to 16 workers per volume) with previous/current/next slab caching.

A regression test verifies that 1-worker and 4-worker rendering produce identical output and that progress reaches 100%.

## 3. “比” is now a toggle

`比` now behaves like `差`:

- first click: choose/load B and enter A/B comparison;
- second click: leave comparison and show A only;
- B is **not closed or discarded** — its path, geometry, slice cache and current render are retained;
- click `比` again: immediately restore A/B using the cached B;
- leaving comparison automatically disables `差` and hides the residual panel.

This avoids repeated B selection and repeated geometry scanning.

## 4. Double-click Zoom toggle

After a rectangle Zoom, Limage stores both views:

- view before Zoom;
- exact view after Zoom.

Then, inside any seismic panel:

- double-click once: restore the view before the last Zoom;
- double-click again: return to exactly the same Zoomed region;
- repeat double-clicks to toggle between the pair.

The snapshot uses seismic/world coordinates rather than screen pixels, and works for Inline, Crossline and Time Slice. Changing mode or explicitly pressing Origin clears the previous double-click Zoom pair.

## 5. Notes

The Windows window classes now request `CS_DBLCLKS`, allowing native `WM_LBUTTONDBLCLK` handling. The compare button is a push-like check button and therefore visually indicates whether B is currently displayed.
