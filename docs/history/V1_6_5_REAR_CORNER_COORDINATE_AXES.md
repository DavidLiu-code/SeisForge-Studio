# Limage v1.6.5 — rear-corner coordinate axes

## Why this change
The previous coordinate triad used the screen-left bottom corner. After camera rotation/FOV/aspect changes this could put IL/XL/T rulers in front of seismic slices, and the min/current/max labels could appear visually attached to the wrong side.

## New coordinate-axis rule
- Evaluate all eight cuboid corners in the current camera coordinate system.
- Choose the **farthest corner from the camera** as the shared IL/XL/T coordinate origin.
- Use screen-left only as a deterministic tie-breaker.
- Draw IL, XL and T from that same rear corner.
- Shift the whole ruler triad outward in screen space by 26 px so labels stay away from seismic texture.
- Tick labels are placed on the side away from the volume center.

## Correct value mapping
IL, XL and T values are mapped independently according to the selected rear corner:
- if an axis starts from its minimum side, ticks run min -> current -> max;
- if it starts from its maximum side, ticks run max -> current -> min.

Time follows the same rule, so changing camera octant no longer assumes that the T ruler always starts at Tmax.

## Result
The coordinate system behaves as a camera-aware background reference instead of a foreground overlay. It follows FOV, camera rotation and X/Y/Z display scaling while keeping labels outside the seismic slices as much as possible.
