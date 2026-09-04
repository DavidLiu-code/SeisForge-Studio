# Limage v1.6.2 — FOV / Shortcut / Perspective Seam Fix

## 1. Expanded FOV range

The 3-D FOV selector now provides:

`0, 5, 10, ... 60, 70, 80, 90, 100, 110, 120 degrees`

- `0°` remains orthographic.
- `>0°` uses perspective projection.
- Saved user defaults support the full 0–120° range.

## 2. Perspective seam fix

The former CPU perspective rasterizer used affine screen-space interpolation of texture coordinates and depth. At larger FOV values this could make the Z-buffer visibility boundary drift away from the mathematically projected slice-intersection line.

v1.6.2 changes the perspective triangle rasterizer to perspective-correct interpolation using reciprocal camera depth (`1/z`) for:

- texture coordinates;
- depth;
- perspective slice picking.

The visible plane-to-plane seam and the rendered 3-D intersection line therefore use the same camera/projective geometry.

## 3. Robust keyboard focus

3-D shortcuts are now routed at the application message-loop level and recognize the 3-D window, its child controls, popup combo lists, and the foreground 3-D window.

This fixes shortcuts becoming inactive after changing a view, FOV, style, axis combo, or clicking another 3-D control.

## 4. Revised 3-D shortcuts

| Key | Function |
| --- | --- |
| `x` / `Shift+x` | increase / decrease X (Inline) axis length |
| `y` / `Shift+y` | increase / decrease Y (Crossline) axis length |
| `z` / `Shift+z` | increase / decrease Z (Time) axis length |
| `r` / `Shift+r` | overall 3-D scale up / down |
| `f` / `Shift+f` | increase / decrease FOV |
| `q` / `w` | display gain +1% / -1% |
| `e` / `Shift+e` | next / previous color map |
| `a` | show current camera / axis parameters |
| `d` | toggle CIGVis direct drag mode |
| `Space` | restore saved default camera |
| `Esc` | return from 2-D Full View to 3-D |

`r` is no longer used as the previous-color-map shortcut inside the 3-D window; `Shift+e` now provides previous color map. Legacy E/R behavior remains unchanged in the original/compare windows.

## 5. Help

The development-time shortcut hint was removed from the toolbar. A dedicated **帮助** button now opens the complete 3-D shortcut / interaction reference.
