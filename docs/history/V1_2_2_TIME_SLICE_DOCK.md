# Limage v1.2.2 — Detached Time Slice in Volume View

## Change

The 3-D **体** view now places the current **Time Slice on the far right** of the workspace instead of intersecting it visually with the two vertical sections.

### Layout

- Left: Inline + Crossline vertical sections form the 3-D seismic corner.
- Right: the current Time Slice is rendered as a detached perspective plane.
- Thin locator lines remain on the two vertical sections to show the selected time/depth of the detached Time Slice.
- A short connector visually links the selected time level in the cube to the detached slice.

## Why

In v1.2.1 the Time Slice was drawn through the middle of the two vertical sections. The three projected polygons could overlap on screen, so a mouse location could satisfy more than one plane hit test. This made it possible for a visible Time Slice point to be interpreted as an Inline or Crossline point.

v1.2.2 separates the Time Slice hit region from the vertical-section hit regions. The seismic coordinate mapping is unchanged:

- Inline plane: Crossline × Time
- Crossline plane: Inline × Time
- Time Slice: Crossline × Inline at the currently selected time

## Interaction

- Hover/click on the detached right Time Slice updates Inline/Crossline as before.
- Hover/click on the two left vertical sections updates the other dimensions as before.
- The top Time ◀/▶ controls still change the selected time sample.
- The `平铺` button still switches back to the three 2-D linked views.

## Performance

The change is display-only. Limage still reads only the current Inline, Crossline, and cached Time-Slab data; it does not load the full 3-D cube into memory.
