# Limage v9 — Synchronized Comparison + Accelerated Time Slice

## New comparison workflow

A new **“比”** button has been added to the main toolbar.

1. Open the first SEG-Y normally. It becomes data **A**.
2. Click **比**.
3. Click **B...** and choose the processed/reference SEG-Y.
4. Use **剖面对比** for side-by-side vertical-section QC.
5. For a post-stack 3-D volume, choose **Time Slice**.

### Synchronized section comparison

- A/B use the same trace and time window.
- Drag a rectangle on either A or B: both panels zoom to the same trace/time range.
- **还原** restores the comparison range that was active when the compare window opened.
- Palette changes are propagated without rereading the section.
- Gain / explicit Min-Max changes are synchronized to the comparison.

## Post-stack Time Slice comparison

Time Slice assumes that each spatial bin has approximately one trace. Limage verifies this by checking duplicate `(Inline, Crossline)` pairs. Data that contains many traces per bin is treated as likely prestack and Time Slice is rejected.

### Geometry header bytes

Default SEG-Y Rev.1 positions:

- Inline: byte **189**
- Crossline: byte **193**

Both are editable in the Time Slice toolbar for surveys that use non-standard trace-header layouts.

### Shared QC display

A and B time slices use:

- the same Inline/Crossline spatial bounds;
- the same time in milliseconds (sample indices are converted when dt differs);
- the same color map;
- the same display gain / explicit Min-Max range;
- a **shared amplitude mapping**, so color differences are physically comparable.

Drag a rectangle on either time slice to synchronously zoom both A/B maps. Spatial zoom is an in-memory rerasterization and does **not** trigger new seismic disk reads.

## Time Slice acceleration

### 1. Persistent geometry index

The first Time Slice request scans only the 240-byte trace headers and records:

- trace number;
- Inline;
- Crossline;
- IL/XL rank and geometry dimensions.

The index is stored in the operating-system user cache, normally under:

`%LOCALAPPDATA%\\Limage\\geometry`

The cache key includes the SEG-Y path, file size, modification time, and selected IL/XL header byte locations. Therefore an edited SEG-Y or changed header convention automatically gets a different index.

### 2. Sample-slab RAM cache

Limage does not perform one independent disk operation for every Time Slice. It loads a block of adjacent samples per trace and stores it sample-major in RAM.

The block is automatically selected from **4 / 8 / 16 / 32 samples** so one block is approximately bounded near 32 MiB for large volumes. Two blocks per volume are retained with LRU replacement.

Consequences:

- the first slice in a block requires disk I/O;
- neighboring Time Slices are RAM-cache hits;
- moving the slider inside the current slab is primarily memory access + rasterization;
- adjacent slabs are prefetched in the background.

### 3. Parallel windowed reads

Time-slab loading uses a bounded worker pool (up to 16 workers). Each trace read contains only the requested sample slab rather than the entire trace.

### 4. Background loading

When the slider moves outside the cached slab, the old Time Slice remains displayed while the new slab is loaded in the background. Completed data are then posted back to the UI thread.

## Included test cubes

Two small standard-header post-stack 3-D SEG-Y files are included:

- `test_poststack_A.sgy`
- `test_poststack_B.sgy`

Geometry: 40 inlines × 50 crosslines, 300 samples/trace, dt = 2 ms, IEEE float32. Inline/Crossline are stored at bytes 189/193.

Suggested test:

1. Open `test_poststack_A.sgy` in the main window.
2. Click **比**.
3. Select `test_poststack_B.sgy` as B.
4. Switch to **Time Slice**.
5. Move the slider around 150–450 ms.
6. Notice that adjacent slider positions reuse the same slab cache.
7. Drag a rectangle on either map to test synchronized IL/XL zoom.
8. Click **还原** to return to the full geometry.

## Current limitations

- Time Slice currently targets regular fixed-length SEG-Y traces.
- Geometry is currently based on 32-bit integer Inline/Crossline trace-header values.
- Prestack gathers with many repeated IL/XL bins are intentionally not treated as Time Slice volumes.
- The first geometry scan of a very large uncached file can take time; later sessions reuse the persistent geometry index.
- Raw/no-header legacy formats are not yet part of the Time Slice engine.
