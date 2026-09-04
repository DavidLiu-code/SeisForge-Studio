# Limage v1.6.6 — Rear-top coordinate origin

## Change

The 3-D coordinate-axis origin is now constrained to the **top face** of the seismic volume.
Among the four top corners, Limage chooses the corner that is farthest from the current camera.

Because time/depth increases downward, this produces the preferred layout:

- IL and XL rulers start at the rear upper corner;
- T starts at the same rear upper corner and runs downward;
- coordinate labels stay above/behind the seismic slices more often;
- camera rotation, FOV changes, and X/Y/Z display scaling still trigger camera-aware rear-corner selection.

This replaces the v1.6.5 rule that could choose either a rear-top or rear-bottom corner.
