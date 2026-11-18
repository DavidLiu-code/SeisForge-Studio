# Limage v1.3.0 — Export current seismic section to SEG-Y

## New toolbar action
The unified browse/compare workspace adds a compact **出** button beside **谱**.

## Export semantics
- Inline mode: exports the currently displayed Inline, ordered by Crossline.
- Crossline mode: exports the currently displayed Crossline, ordered by Inline.
- The current horizontal zoom/range is honored.
- The current time/sample zoom is honored.
- Time Slice is not exported as a conventional 2-D time section; Limage asks the user to switch to Inline or Crossline.

## A / B / residual
- A-only: exports A directly.
- A+B: choose A or B.
- If residual display is active, A-B can also be exported.

## SEG-Y fidelity
For A or B export, Limage copies the selected source sample bytes directly without decode/re-encode. Thus IBM float, IEEE float and integer source sample formats are preserved exactly. Textual, binary, extended textual and trace headers are preserved, while required cropped-section fields are updated:
- samples per trace
- sample interval
- fixed-length trace flag
- trace-header samples/dt
- trace delay time, shifted by the cropped sample start

A-B residual export is written as IEEE float32 while retaining A's geometry/trace-header metadata.

## Large-file behavior
The exporter uses 64-bit source offsets and a 4 MiB buffered writer. It runs in a background goroutine, keeps the UI responsive, shows the existing workspace progress bar, and uses the waiting cursor while exporting.

## Validation
Added regression tests for:
1. cropped A/B section export and SEG-Y reopen;
2. sample-count/header updates;
3. physical delay-time shift after time cropping;
4. A-B IEEE-float residual export.
