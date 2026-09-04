# Limage v1.6.8 — FOV coordinate calibration and composition refinement

This release refines the 3-D coordinate-ruler layout without changing the established rendering/interaction model.

## 1. Perspective-correct coordinate ticks

Previously, ruler endpoints were projected from 3-D correctly, but min/current/max tick positions were interpolated linearly between those two screen endpoints. That is exact for orthographic projection, but not for perspective projection.

v1.6.8 projects every IL / XL / T tick from its actual normalized 3-D position. The same screen-space ruler offset is then applied to the projected point. This keeps the current-coordinate tick aligned with the seismic geometry as FOV increases.

## 2. Endpoint label separation

IL and XL ruler endpoint values may share or nearly share a projected cube corner. Endpoint labels now receive two offsets:

- outward normal offset from the cube;
- tangential offset away from the shared corner.

The separation increases gently with FOV, because perspective convergence crowds labels more strongly at large FOV.

## 3. Lower scene composition

The seismic cube is now placed slightly below the strict geometric center of the view. The old bottom coordinate reserve was also reduced because the T ruler is attached at the left side rather than below the cube.

This reduces excessive blank space below the volume while leaving room above for IL/XL rulers.

## 4. Existing behavior retained

- CIGVis / interpretation / standard / clean visual presets;
- independent IL / XL / Z axis scaling;
- FOV 0–120 degrees;
- persistent Set Default view;
- Q/W volume gain;
- X/Y/Z/R/F shortcuts;
- independent edge-attached IL / XL / T coordinate rulers.
