# Limage v1.6.7 — independent edge-attached 3D coordinate axes

## Why this change

The previous IL / XL / T rulers shared one common rear-top origin. At large FOV values this caused labels and current-coordinate values to cluster around one location and overlap each other or the seismic slices.

## New coordinate layout

- **IL ruler**: attached closely to the rear/top IL edge of the current camera view.
- **XL ruler**: attached independently to the rear/top XL edge.
- **T ruler**: attached independently to the visually leftmost vertical volume edge.
- The three rulers **do not share a common origin**.
- Each ruler shows minimum, current and maximum coordinates.
- Labels are placed on the outside of the projected volume.
- Ruler offsets are reduced to roughly 9–11 screen pixels so the axes visually belong to the seismic cube instead of forming a detached coordinate scaffold.

The edge selection is camera-aware, so rotation, FOV and X/Y/Z scale changes automatically reposition the rulers.

## Coordinate mapping

IL, XL and T values remain bound to their real normalized data coordinates (0 -> 1). Screen direction may reverse after camera rotation, but coordinate labels remain tied to the physical data axis rather than screen left/right ordering.
