# Limage v1.6.4 — 3-D axes, camera and volume gain refinement

## 1. Wider independent X / Y / Z scaling range

The three display-axis multipliers now support:

`0.10× ... 8.00×`

The keyboard controls remain:

- `X / Shift+X`: increase / decrease Inline-axis length
- `Y / Shift+Y`: increase / decrease Crossline-axis length
- `Z / Shift+Z`: increase / decrease Time-axis length
- `R / Shift+R`: overall view scale

These multipliers are applied on top of the selected base aspect mode (True / Balanced / Saved Default).

## 2. More CIGVis-like factory camera

For users who have not saved their own default view, the factory camera is now a left-rear oblique perspective:

- Azimuth: -145°
- Elevation: 24°
- FOV: 35°

A user-saved default still has priority. `Space` restores that saved camera when one exists.

## 3. Full IL / XL / T coordinate rulers

The previous isolated right-side Time ruler has been replaced by a shared 3-D ruler triad on the screen-left/rear side of the cuboid.

When `坐标轴` is enabled, Limage now shows:

- Inline axis: minimum / current / maximum Inline coordinate
- Crossline axis: minimum / current / maximum Crossline coordinate
- Time axis: minimum / current / maximum time

The small IL / XL / T orientation widget is retained in the lower-left margin.

Turning `坐标轴` off hides both the numeric rulers and the orientation widget.

## 4. Q / W volume gain bug fixed

The global legacy percentile gain value was changing, but the 3-D volume cache was not refreshed by the shared Q/W shortcut handler.

Now Q/W triggers a complete 3-D display remap:

- Inline texture refresh
- Crossline texture refresh
- Time Slice contrast/range refresh

The three volume planes therefore respond to the same percentile-style gain used by the rest of Limage.

## 5. Existing view-default persistence retained

`设为默认` continues to save:

- style / interaction
- coordinate-axis visibility
- aspect mode and saved base aspect
- X / Y / Z multipliers
- FOV
- azimuth / elevation / zoom / pan
- palette and view-size mode

The settings remain stored in `%LOCALAPPDATA%\\Limage\\volume_view.json`.
