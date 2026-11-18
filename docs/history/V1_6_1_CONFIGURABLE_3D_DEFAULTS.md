# Limage v1.6.1 — Configurable 3D view and persistent defaults

v1.6.1 extends the CIGVis-oriented 3-D slice viewer with user-configurable coordinates, axis lengths, FOV and persistent default view settings.

## 1. Coordinate display switch

A new **坐标** checkbox controls the in-scene coordinate aids:

- right-side time ruler (`0 / current / Tmax`),
- lower-left `IL / XL / T` orientation widget,
- compact coordinate/range labels used by the Standard preset.

The seismic slices and their true intersection lines are unaffected.

## 2. Independent IL / XL / Z display lengths

The second toolbar row now contains:

- `IL×`
- `XL×`
- `Z×`

Each factor multiplies the geometry-driven seismic display aspect. This keeps the useful automatic IL:XL compression while allowing the user to tune each dimension independently.

Available factors range from `0.25` to `3.00`.

### CIGVis-compatible keyboard control

- `z`: increase Z-axis length
- `Shift+z` (`Z`): decrease Z-axis length

The keyboard shortcut updates the `Z×` control immediately.

## 3. FOV / projection control

A new **FOV** control provides:

- `0° Ortho`: orthographic projection
- `5° ... 60°`: perspective projection

Keyboard behavior follows CIGVis:

- `f`: increase FOV
- `Shift+f` (`F`): decrease FOV

The CPU renderer now performs a perspective projection when FOV is nonzero. Projected seismic planes are rasterized as quadrilaterals (two triangles) so the texture, depth test and picking remain consistent when perspective is enabled.

Press `a` to show the current camera parameters in the status line:

- azimuth,
- elevation,
- zoom,
- FOV,
- pan,
- IL / XL / Z factors.

## 4. Set as default

The new **设为默认** button stores the current 3-D view configuration in the Windows user profile:

`%LOCALAPPDATA%\Limage\volume_view.json`

The saved state includes:

- coordinate visibility,
- view size preset,
- geometry/balanced aspect mode,
- CIGVis / interpretation / standard visual style,
- CIGVis / classic interaction mode,
- color map,
- IL / XL / Z length factors,
- FOV,
- camera azimuth and elevation,
- zoom,
- camera pan.

When a new seismic data set is opened, Limage loads this view automatically. Inline, crossline and time-slice positions are **not** stored, because they are data-dependent; a new data set still starts from its own center slices.

`Space` / **复位** returns the camera to the saved default camera pose when a saved default exists.

## 5. Existing CIGVis-style interaction remains

- Left drag: rotate
- Right drag / wheel: zoom
- Shift + left drag: pan
- Ctrl + left drag: move a slice
- `D`: toggle direct slice-drag mode
- `Space`: reset camera
- Double click slice: 2-D full view
- Esc: return from 2-D full view

The strict CIGVis visual preset still draws no ordinary slice rectangle borders and no volume box: only the three true slice-intersection lines remain.
