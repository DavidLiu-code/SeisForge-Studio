# Limage v1.6.3 — Taskbar / Clean Style / Default Aspect / Stable 2D Open

## 1. Aspect ratio modes

The 3-D `比例` selector now contains three modes:

- `实际`: true Inline:Crossline range ratio for the current dataset, with no visual 2.2:1 compression/clamp.
- `均衡`: 1:1 horizontal interpretation view.
- `默认`: reproduces the display shape captured by the last `设为默认` action.

`设为默认` now stores the current base scene shape in addition to X/Y/Z multipliers, FOV, camera azimuth/elevation, zoom, pan, palette, view size, style, interaction mode and coordinate visibility. After saving, the ratio selector switches to `默认`; future datasets automatically use that saved display shape.

## 2. Clean style

A fourth visual preset, `干净`, has been added.

Clean mode renders seismic texture only:

- no Bounding Box;
- no slice rectangle outlines;
- no IL×XL / IL×T / XL×T intersection lines;
- no 3-D cursor crosshair.

Coordinate ruler/orientation visibility remains independently controlled by the `坐标` checkbox.

## 3. Taskbar icon / top-level window fix

The visible 2-D workspace and 3-D volume viewer are now created as independent top-level `WS_EX_APPWINDOW` windows rather than owned windows of the hidden compatibility controller. This gives Windows a proper taskbar entry and uses the registered Limage class icon.

When the 3-D volume viewer opens, the 2-D workspace is hidden (state is preserved). Closing the 3-D viewer restores the 2-D workspace.

## 4. Double-click no longer moves slice position

Normal click only selects the current plane. It no longer relocates the orthoslice intersection.

- Double-click: open the selected plane in 2-D Full View without changing IL/XL/T intersection position.
- Ctrl + double-click: explicitly relocate the 3-D intersection to the clicked location and stay in 3-D.
- Slice movement remains available through Ctrl+drag / D drag mode / IL-XL-T controls.

This prevents the first click of a double-click sequence from unexpectedly changing the 3-D slice position before the 2-D view opens.
