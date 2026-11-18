# Fimage64 v5 — coordinate axes and toolbar fidelity

This revision addresses two discrepancies identified by direct visual comparison with the supplied original Fimage v1.5.1 executable.

## 1. Reconstructed seismic coordinate frame

The main image no longer fills the entire client area. It reserves the original-style margins around the seismic raster and draws:

- blue top/bottom horizontal borders;
- blue left/right vertical borders;
- trace ticks and labels on both top and bottom;
- time ticks and labels on both left and right;
- approximately ten trace divisions, using a 1/2/5 nice-step rule;
- approximately six time divisions, also using a 1/2/5 nice-step rule.

The coordinates are derived from the currently cached/selected data range:

- x labels use the actual one-based SEG-Y trace numbers and honor `TraceStep`;
- y labels use `SampleStart`, `SampleEnd`, and the SEG-Y sample interval;
- the main time axis is displayed in seconds, matching the legacy Fimage screenshot, while the load dialog continues to accept milliseconds.

For 500 selected consecutive traces, the trace axis naturally produces the familiar legacy pattern:

`1, 51, 101, 151, ..., 451, 500`.

## 2. Correct seismic plot margins

At 96-DPI logical coordinates the reconstructed layout reserves approximately:

- toolbar: 30 px;
- left/right axis margins: 44 px;
- top axis margin: 38 px;
- bottom axis margin: 34 px;
- status bar: 16 px.

These values were chosen by matching the supplied side-by-side screenshot against the recovered original form geometry.

## 3. Fixed toolbar toggle rendering

The previous x64 build used a native `BS_AUTOCHECKBOX` button for legacy `tbsCheck` toolbar controls. Windows therefore drew an extra checkbox glyph beside the bitmap, visually covering/crowding the Fixed and Zoom icons.

v5 uses `BS_AUTOCHECKBOX | BS_PUSHLIKE | BS_BITMAP`, producing a pressed/unpressed toolbar-button appearance without the unwanted checkbox glyph. This more closely matches the original Borland `TToolButton` behavior.

## 4. Minimum window width

A minimum tracking width is now enforced so the recovered 406-pixel legacy toolbar cannot be clipped by narrowing the main window too far.

## 5. Preserved v4 behavior

- cached data remains visible when Open is clicked again;
- Cancel leaves the displayed section untouched;
- replacement loading is transactional;
- range-first SEG-Y I/O is retained;
- all 20 recovered legacy palettes remain available;
- manual Min/Max amplitude limits remain functional.

Build target: Windows x86-64 / PE32+ GUI.
