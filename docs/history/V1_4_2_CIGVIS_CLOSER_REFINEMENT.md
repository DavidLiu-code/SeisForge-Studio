# Limage v1.4.2 — Further refinement toward a cleaner CIGVis-like volume view

This iteration pushes the native 3-D orthoslice view closer to the cleaner visual logic of CIGVis.

## Main refinements

1. **Further simplified volume frame**
   - Reduced the guide wireframe to a minimal set of faint reference edges.
   - The volume is now anchored in space with less line clutter.

2. **More slice-focused rendering**
   - The active slice remains fully opaque.
   - Inactive slices are slightly softened instead of equally dominant.
   - Hovered slice becomes nearly opaque; dragged slice stays fully opaque.

3. **Cleaner text layout**
   - Bottom range labels were reduced to compact left/right hints.
   - Right-side time ruler labels were tightened.
   - The orientation widget was made smaller and lighter.

4. **Preserved true 3-D logic**
   - Depth ordering still uses the Z-buffer.
   - Slice intersections remain the primary spatial cue.
   - Active/hover/drag states continue to use blue/yellow/orange highlighting.

## Intended visual effect

Compared with v1.4.1, this version should look:
- less busy,
- more slice-centric,
- closer to a clean scientific viewer,
- easier to read at first glance.
