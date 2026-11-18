# Limage v1.5.2 — CPU/GDI 3D final tuning

This release applies the final parameter-only refinement to the current native CPU/GDI volume viewer. No new decorative UI was added.

## Final visual tuning

1. **Active slice border reduced**
   - Minimal mode: 1 px muted blue.
   - Standard mode: 2 px muted blue.
   - Hover remains yellow and dragging remains orange.

2. **Intersection lines further subdued**
   - Static intersection lines remain 1 px but use a much softer warm-yellow tone, approximating a 0.40–0.50 visual alpha on the white background.
   - Hover/drag states restore strong yellow/orange feedback.

3. **Short horizontal axis widened**
   - Extreme IL:XL display anisotropy is now limited to approximately 2.2:1 instead of 2.6:1.
   - Therefore the short horizontal axis remains about 0.455 of the long horizontal axis for very elongated surveys.
   - Projection, depth test, and picking continue to use the exact same display geometry.

4. **Time Slice contrast reduced without adding transparency**
   - IL/XL remain nearly opaque.
   - Time Slice remains around 0.88 opacity.
   - When display limits are automatic, Time Slice clim is widened by 15% around its midpoint, reducing texture contrast.
   - User-specified manual limits are preserved exactly and are not modified.

5. **Production UI cleanup**
   - Removed user-facing `CIGVis-style` / `more like cigvis` development messages.
   - Runtime status now uses concise `3D View` labels.
   - CIGVis attribution remains in documentation / THIRD_PARTY_NOTICES only.

## Visual hierarchy target

Seismic texture > active slice > intersections > time/orientation aids > bounding box.

This release is intended to be the final visual tuning pass for the CPU/GDI renderer. Further major visual improvements should move to a GPU renderer rather than continue pixel-level GDI tuning.
