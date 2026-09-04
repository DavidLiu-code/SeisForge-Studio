# Limage v1.4.3 — CIGVis-oriented minimal 3D mode

This version further simplifies the native 3-D orthoslice display to move closer to the visual style of CIGVis.

## Highlights

1. **New 极简 / 标准 toggle**
   - Added a toolbar button for switching between:
     - **极简模式**: closest to a clean CIGVis-like look.
     - **标准模式**: keeps more auxiliary spatial hints.
   - Default is now **极简模式**.

2. **Cleaner 3-D scene in 极简模式**
   - The outer guide frame is reduced to only a few faint rear-corner edges.
   - Footer IL/XL range labels are hidden.
   - The orientation widget is hidden.
   - The time ruler remains, but its labels are more compact.

3. **Stronger slice-centric display**
   - Active slice remains most prominent.
   - Inactive slices are slightly softer.
   - Plane outlines stay visible only for the active / hover / dragging slice.

4. **Still preserves true 3-D interaction**
   - Z-buffer depth ordering is preserved.
   - Hover / drag feedback is preserved.
   - Double-click to 2-D full view still works.

## Recommended usage

- Use **极简模式** when you want the 3-D view to feel closest to CIGVis.
- Use **标准模式** if you need more explicit orientation hints.
