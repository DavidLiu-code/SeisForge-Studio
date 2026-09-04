# Limage v1.0.3 — Compare Controls, Auto Geometry, and Time-Slice Cache UX

## Fixes requested in this version

### 1. Compare colorbar switching
The compare-window palette selector now accepts both selection-change and selection-confirm notifications. More importantly, the currently displayed Time Slice amplitudes are retained in a compact in-memory copy independent of the slab LRU. Changing the colorbar therefore remaps A/B immediately and never requires the active slab to still reside in the disk-I/O cache.

E / R also cycle the next / previous compare palette without changing the main-window palette.

### 2. Q / W gain shortcuts in compare mode
The compare view is a separate top-level Win32 window. v1.0.2 only intercepted legacy shortcuts for the main-window child tree, so Q/W could not reach the compare view. v1.0.3 explicitly intercepts keyboard messages from the compare window and its child controls:

- Q: gain +1%
- W: gain -1%
- E: next compare palette
- R: previous compare palette

Gain remapping of Time Slice uses the retained current amplitudes, so it does not perform new SEG-Y I/O.

### 3. Auto IL/XL detection is now the default
After both A and B files are selected, Limage automatically samples trace headers and estimates Inline/Crossline bytes before building the 3-D geometry index. The detected A/B layouts may differ. Manual byte fields and the IL/XL swap control remain available as overrides.

If auto-detection cannot be completed, Limage falls back to the currently entered manual bytes instead of blocking the comparison workflow.

### 4. Wait cursor during Time-Slice caching
A wait cursor is shown while Limage is actively waiting for:

- automatic geometry detection,
- first geometry-index / first-slab preparation,
- a requested Time Slice slab that is not already in RAM.

Cache hits remain immediate and do not show the wait cursor. Background neighbor prefetch does not block interaction and therefore does not force the wait cursor.

### 5. Finer middle slider
The trackbar now uses much smaller page increments and denser ticks. Arrow/keyboard line movement is one sample (Time Slice) or one IL/XL coordinate. This makes fine positioning substantially easier than the previous large default Windows trackbar page jump.

### 6. Slab-cache retention fix
The RAM slab cache was increased from two to three blocks so bidirectional prefetch can retain:

- previous slab,
- current slab,
- next slab.

A regression test verifies that warming both neighbors does not evict the active slab.

## Validation

- `go test ./...` passes.
- Added `TestTimeSliceCacheKeepsCurrentAndBothNeighbors`.
- Windows cross-build succeeds for amd64.
- Output is a native `PE32+` x86-64 Windows GUI executable.
