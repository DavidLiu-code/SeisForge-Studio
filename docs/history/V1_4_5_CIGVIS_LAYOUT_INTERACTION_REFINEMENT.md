# Limage v1.4.5 — CIGVis-style layout and interaction refinement

This release continues the simplification of the 3-D volume viewer with an emphasis on reducing UI chrome and keeping the seismic slices visually dominant.

## Changes

1. **Compact controller strip**
   - `Inline / Crossline / Time` labels are shortened to `IL / XL / T`.
   - Buttons and edit boxes are tightened.
   - Color palette and view controls are consolidated into one compact row.
   - The status row is shorter.

2. **Larger visual canvas**
   - Reduced the top and bottom scene margins.
   - Increased usable 3-D canvas area.
   - Adjusted fit proportions so the orthoslices feel less boxed-in.

3. **Dynamic intersection hierarchy**
   - At rest, intersection lines are subtle warm guides.
   - On hover, they become brighter yellow.
   - During dragging, they become thicker orange.
   - This keeps the scene quiet until the user interacts.

4. **Softer active-slice outline**
   - In minimal mode, the active slice border is now a very muted blue.
   - Hover/drag feedback still remains obvious.

5. **Default camera tuned again**
   - Default orthographic angle updated for a cleaner view of all three slices.
   - Slightly reduced zoom gives the scene more breathing room.

## Recommended mode

Use the default **极简** 3-D mode for the closest CIGVis-like visual experience.
