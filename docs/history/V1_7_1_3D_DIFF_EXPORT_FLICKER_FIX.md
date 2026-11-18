# Limage v1.7.1 — 3D compare stability, residual and subvolume export

## 1. Camera rotation toolbar flicker fix

The 3-D window now uses `WS_CLIPCHILDREN`, and scene invalidation is restricted to the canvas/status region instead of the full client area. During left-drag camera rotation, native controller widgets remain idle; camera status text is updated only when the drag finishes.

This preserves the existing off-screen scene cache while preventing child buttons and combo boxes from visually flashing during rapid 3-D redraws.

## 2. 3-D residual view (`差`)

After B is loaded with `比`, a `差` toggle becomes available.

- OFF: A / B dual 3-D workspace.
- ON: A / B / A-B three-view 3-D QC workspace.
- A and B remain independently transformable.
- A-B follows A's slice position, camera, FOV, zoom/pan and axis display geometry so the residual is judged in exactly the same spatial frame as the left reference.
- Inline/Crossline residuals are computed from coordinate-matched traces using true seismic amplitudes.
- Time-slice residuals are rasterized on A's IL/XL geometry and use a zero-symmetric residual display range.
- The residual is not a screen-pixel subtraction.

## 3. 3-D range export (`出`)

The `出` button opens a range dialog with:

- source: A / B / A-B;
- Inline start/end;
- Crossline start/end;
- Time start/end in milliseconds.

Fields are initialized from the current 3-D dataset's available range and may be reduced to any desired subvolume.

### A / B output

The selected source traces are exported with original SEG-Y sample encoding, endian convention and trace headers. The binary/trace headers are updated for the cropped sample window.

### A-B output

A/B traces are matched by actual `(Inline, Crossline)` coordinates inside the requested box. The residual is written as IEEE float32 while retaining A's geometry/trace-header metadata. Matching sample intervals are required.

Export runs asynchronously and reports progress in the 3-D status line.

## 4. Existing v1.7.0 behavior retained

- A/B cameras and slice positions remain independent.
- `与左图一致` still maps B to A by actual IL/XL and physical time, then copies camera/FOV/axis geometry.
- CIGVis / interpretation / standard / clean visual presets are unchanged.
- X/Y/Z, R, F, Q/W and saved-default camera settings are retained.
