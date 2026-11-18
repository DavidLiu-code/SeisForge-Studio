# Limage v10 — Automatic Inline/Crossline Header Detection

## Goal

Automatically estimate the 1-based SEG-Y trace-header byte positions used for the two 3-D post-stack grid axes before building Inline, Crossline, or Time Slice views.

The detector reads **trace headers only**. It does not decode seismic amplitudes.

## UI

The comparison window now includes:

- **自动识别** — samples trace headers from A and B independently and estimates their IL/XL byte positions.
- **IL/XL交换** — swaps the two detected axes when a proprietary file uses the opposite axis naming/order.
- Manual `IL byte` and `XL byte` edits are retained as a fallback.

If A and B use different header layouts, Limage keeps separate internal byte positions for the two files even though the edit boxes show A's detected pair.

## Detection pipeline

### 1. Fast prior

Standard SEG-Y Rev.1 post-stack geometry uses 4-byte integers at 189 and 193. These positions receive a prior, but they are not forced.

### 2. Header-only sampling

Limage samples up to ~3,072 trace headers using several consecutive windows distributed from the beginning to the end of the file. This preserves local trace-order behavior without scanning every trace in a very large SEG-Y.

### 3. Candidate fields

Every possible 4-byte start position from 1 to 237 is decoded using the detected SEG-Y byte order. Each field is scored from:

- number of unique values;
- repeat ratio;
- zero ratio;
- local step regularity;
- plausible integer magnitude;
- 4-byte alignment as a tie-breaker.

### 4. Candidate axis pairs

The best individual fields are paired and evaluated by four strong 3-D geometry tests:

1. **One-axis transition ratio** — between neighboring traces, normally one grid axis changes while the other stays fixed. Trace sequence numbers and map coordinates often fail this test.
2. **Local rectangularity** — within each sampled window, unique `(axis-1, axis-2)` pairs should closely fill the product of the two local coordinate sets.
3. **Duplicate-bin ratio** — post-stack data should have approximately one trace per grid bin.
4. **Step consistency** — the changing grid coordinate usually has a stable integer increment.

### 5. X/Y false-positive protection

CDP X/Y coordinates can themselves form a regular 2-D grid. Therefore standard ensemble-X/Y positions 181/185 receive a soft penalty when the detector is looking for Inline/Crossline indices. They are not forbidden because proprietary files may repurpose fields.

### 6. Standard and alignment priors

The 189/193 pair gets a moderate standard prior. 1-based 4-byte aligned starts (`1,5,9,...`) receive a smaller tie-breaking prior. These priors mainly eliminate shifted-byte views such as 190/194 that can preserve the same numerical pattern in big-endian data.

### 7. Confidence and axis naming

The top pair is compared with the next-best alternatives. Limage reports a score and confidence level.

For non-standard files, the two mathematical grid axes can often be detected with high confidence, but **which one should be called Inline versus Crossline is not always mathematically identifiable from the trace headers alone**. Limage uses the slower-changing axis in trace order as the initial Inline label and marks that labeling as heuristic. The **IL/XL交换** button is provided for this case.

## Performance

Only 240-byte headers are sampled. On the included 2,000-trace test cubes, detection takes roughly 0.1 s in the current development environment. Detection of a custom-layout 432-trace cube takes about 0.07 s. Large volumes still read only the small sampled header set.

After detection, the existing persistent geometry cache is keyed by:

- file path;
- file size;
- modification time;
- detected Inline byte;
- detected Crossline byte.

Therefore the expensive full geometry-index build still occurs only once per file/header-layout combination.

## Validation included in this package

- `test_poststack_A.sgy`: standard 189/193 — detected 189/193.
- `test_poststack_B.sgy`: standard 189/193 — detected 189/193.
- `test_poststack_custom_bytes_101_137.sgy`: custom 101/137, with realistic X/Y distractors at 181/185 — detected 101/137.
- Unit test `TestDetectGeometryBytesStandardAndCustom` verifies both standard and custom layouts.

See `autogeometry_validation_v10.txt` and `test_results_v10.txt` for the recorded checks.
