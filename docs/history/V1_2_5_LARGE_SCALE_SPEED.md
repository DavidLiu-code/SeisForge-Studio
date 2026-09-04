# Limage v1.2.5 — Large-scale first-load acceleration

## Why v1.2.4 could saturate the disk

The slow first load was primarily I/O-bound, not CPU-bound. Two expensive operations happened before an Inline/Crossline could be painted:

1. the geometry builder read the 240-byte header of **every trace** to construct the IL/XL index;
2. after geometry preparation, v1.2.4 eagerly loaded a complete Time-Slice slab even when the active direction was Inline or Crossline.

On a multi-million-trace post-stack volume this generates millions of small strided reads. More worker threads raise queue depth but cannot beat a disk that is already at 100% active time.

## v1.2.5 fast regular-grid geometry

For conventional ordered post-stack 3-D SEG-Y, Limage now:

1. reads the first two geometry headers;
2. finds the first line boundary with exponential + binary search;
3. infers fast/slow grid increments and possible serpentine line order;
4. validates the model at ~100 checkpoints spread over the entire file;
5. when all checkpoints agree exactly, generates the complete Geometry Index arithmetically in memory.

This changes geometry discovery from **O(number of traces) disk reads** to roughly **O(100) header reads** for a regular cube.

If any validation point disagrees, Limage automatically falls back to the exhaustive multi-threaded scanner, so irregular/missing-bin volumes retain correctness.

## Lazy Time Slice

Time-Slice slabs are no longer read during preparation of Inline/Crossline views.

- Inline selected -> only the selected Inline traces are read.
- Crossline selected -> only the selected Crossline traces are read.
- Time Slice selected -> then (and only then) the slab cache starts I/O.

This removes the largest unnecessary first-open disk pass seen in v1.2.4.

## Faster line navigation

When a dense post-stack grid is available, `LineTraceNumbers` now uses direct grid lookup instead of scanning every geometry entry whenever the user moves to the next Inline/Crossline.

## Synthetic benchmark

A sparse logical 1.248 GB regular test SEG-Y with 200,000 traces (200 × 1000 grid) was used to compare geometry construction on the same environment:

- v1.2.4 exhaustive geometry: **150.9 ms**
- v1.2.5 validated fast-grid geometry: **11.6 ms**
- headers touched by v1.2.5 fast path: **127**
- geometry speedup in this test: **~13×**

The user's real large volume should benefit more when the old path causes storage active time to remain at 100%, especially because v1.2.5 also removes the eager whole-volume Time-Slice slab read from Inline/Crossline startup.

## Multithreading policy

Limage still uses multiple workers for amplitude decoding, irregular full geometry scans, A/B preparation, and Time-Slice slabs. The regular-grid inference intentionally does not fan ~100 tiny validation reads across many workers because avoiding millions of reads is much more effective than increasing queue depth.
