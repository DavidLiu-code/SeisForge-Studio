# Limage v1.5.0 — 3D Volume Stable Release

This release consolidates the CIGVis-inspired volume-view iterations into a more complete and practical 3-D seismic cube viewer.

## 1. Robust color-map selection

The volume palette combo now keeps the two grayscale palettes at the top:

1. 白灰黑
2. 黑灰白

followed by the remaining recovered Fimage palettes. The combo index is mapped explicitly to the original 20-palette table, so selecting a color map and then returning to grayscale remains reliable. The drop-down is also taller for easier palette browsing.

## 2. Multiple volume display sizes

A new **大小** selector provides:

- 紧凑
- 标准
- 大 (default)
- 填充

The view uses auto-fit and resets the manual wheel zoom when a preset is selected.

## 3. Geometry-driven IL:XL aspect

A new **比例** selector provides:

- 实际 (default): derives the horizontal display ratio from the SEG-Y IL/XL geometry.
- 均衡: displays IL and XL as 1:1.

For extremely elongated surveys, the visual anisotropy is capped at 5:1 to prevent a practically unusable paper-thin face while still preserving the real survey trend. Time is slightly compressed for seismic interpretation.

Importantly, the same stretched geometry is used by both projection and the software Z-buffer, so depth testing/picking remain physically consistent with what is shown on screen.

## 4. Larger default composition / auto-fit

- Startup volume window increased to 1520×960.
- Default view preset is **大**.
- The cube occupies substantially more of the effective canvas.
- A compact right-side strip is reserved for the time ruler.

## 5. Better slice visual hierarchy

- Inline/Crossline remain nearly opaque and carry the main interpretation detail.
- Time Slice is slightly softened so it no longer dominates the scene.
- Active slice: muted blue border.
- Inactive slices: faint gray-blue border.
- Hover: yellow.
- Dragging: orange.

## 6. Quiet intersection lines

The three IL×XL / IL×T / XL×T intersection lines remain, but the resting color is now a subdued warm tone. Hover and dragging make them brighter only when interaction feedback is useful.

## 7. Faint complete bounding box

The finite seismic-volume boundary is restored as a very light full bounding box. This gives the three floating slices a clear cube context without returning to the visually heavy wireframe of early versions.

## 8. Time ruler refinement

The ruler is closer to the cube and now includes explicit top/current/bottom ticks:

- 0 ms
- current time
- maximum time

## 9. Orientation widget restored

The IL / XL / T orientation widget is retained even in minimal mode because camera rotation otherwise makes survey direction ambiguous. It remains small and subdued.

## Existing interaction retained

- Right-drag: rotate camera
- Mouse wheel: zoom
- Space: reset camera
- Drag slice edge: move IL / XL / Time Slice
- Double click a 3-D slice: 2-D full view
- Esc/double-click: return from 2-D full view
- Z-buffer depth ordering
- Time-slice cache / background prefetch
- Existing A/B compare, spectrum, export and licensing functionality

## Validation

- `go test ./...`: PASS on the Linux build/test host.
- Windows x64 GUI build: PASS.
- Target binary: PE32+ GUI x86-64.
