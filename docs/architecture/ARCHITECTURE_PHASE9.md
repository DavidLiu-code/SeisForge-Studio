# Limage Phase 9 architecture

## Coalesced SEG-Y I/O

Pseudo-3D alone opts into `segy.ReadStrategyCoalesced`; the legacy/default render strategy used by 2-D, Crooked 2-D, comparison, export, and true Volume3D remains unchanged. The fast path first resolves the unique physical traces required by the output raster. Nearby trace sample windows are grouped while a block remains at most 8 MiB and the skipped file gap remains at most 64 KiB. A line may have at most four block reads in flight, the process has an eight-read semaphore, and the existing pseudo loader still owns exactly two independent line Readers.

The block reader decodes IEEE float, IBM float, and integer SEG-Y into the same floating-point values and runs the same nearest/adaptive interpolation and palette-index mapping as the legacy path. `RenderStats` reports coalesced block/read count and bytes. With gain zero, observed minimum and maximum are already the exact order endpoints, so no copy or sort is made. Non-zero gain uses deterministic exact two-sided order selection and is byte-for-byte checked against the previous full sort.

## Progressive and cancellable scene rendering

Line completion no longer requests a full-resolution scene for every curtain. The active line can request an immediate half-resolution preview. Other ready lines are merged behind a 200 ms gate, limiting loading previews to five per second. When all selected candidates have either completed or failed, the UI submits one full-resolution final render and retains the explicit `最终渲染` state until that result is swapped into the window.

Each render request has a monotonically increasing render generation. `pseudo3d.RenderWithCancel` checks that generation at curtain boundaries; a camera, AOI, style, or newer loading scene cancels superseded work without posting an error. AOI's existing preview/refine transaction remains distinct from loading previews, so only an AOI preview schedules its full refinement. Completion reports successful/failed curtains plus persistent-cache hits and coalesced I/O totals in the workspace trace without logging seismic amplitudes.

## Persistent indexed-texture cache

`internal/pseudocache` owns a UI-free v1 record format under the operating-system user cache directory at `Limage/pseudo_textures`. The default and hard pruning budget is 1 GiB. Each immutable `.ptx` record contains calibrated Crooked geometry, decimated curtain/picking coordinates, original trace/position mapping, and the final eight-bit amplitude-index texture. It never contains raw floating-point amplitudes.

The SHA-256-derived key covers absolute source path, size and modification time, SEG-Y layout/format/endian, coordinate-header settings, navigation fingerprint, texture algorithm version and dimensions, gain, clip, AGC, display mode, and sample range. Palette, style, camera, and AOI are deliberately excluded: palette changes recolor indices, and a matching full-line texture can be windowed by the existing AOI segment logic. Records use a fixed magic/version header, bounded lengths, and a CRC32 payload checksum. Corrupt entries are ignored and removed. A temporary file is synced and atomically renamed, and modification/access time drives strict oldest-first pruning.

Cache lookup occurs before `Dataset.OpenReader`; a complete hit therefore opens no SEG-Y handle. Cold results are merely retained as pending cache writes during loading. Only after the final scene is displayed are they sent to the session's single background writer, keeping first paint free of cache-write latency. Closing the pseudo window cancels queued writes; valid records already atomically committed remain reusable.

## Frozen behavior and validation

Phase 9 does not modify the 20 palettes, gain/clip definitions, AGC behavior, AOI semantics, camera controls, true Volume3D renderer, `volume_view.json`, `crooked_view.json`, `pseudo_view.json`, `.lidx`, or `.cidx`. Tests compare coalesced and legacy indexed pixels for IEEE, IBM, and integer data; cover cache CRC/invalidation/atomic LRU behavior and renderer cancellation; and verify the supplied 62-line survey. On the acceptance machine the Phase 8 reference median was 4.651 s, while Phase 9 measured 1.192 s without a persistent texture hit and 0.183 s with 62/62 hits, including one final 1000×700 scene render. Retained in-memory textures remain governed by the existing 192 MiB budget.

The independent deliverable is `Limage_v1.9.7_pseudo_fast_x64.exe`, built with the Windows GUI subsystem. Time-range clipping and AOI/camera bookmarks remain deferred to later phases.
