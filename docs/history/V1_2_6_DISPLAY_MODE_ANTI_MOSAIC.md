# Limage v1.2.6 – anti-mosaic display modes

## Problem
When the user zooms into a small trace/sample window, the previous renderer used nearest-neighbour sampling in both trace and sample directions. This caused obvious blocky/mosaic artifacts after enlargement.

## Fixes in v1.2.6
1. Added three display modes in **参数 -> 波形显示**:
   - **快速像素**: legacy nearest-neighbour behavior, fastest.
   - **平滑插值**: bilinear interpolation, always smooth.
   - **自适应**: automatically switches to bilinear when the visible trace/sample grid is being enlarged.
2. Added **HALFTONE** stretch mode for final Windows GDI blit, improving visual quality when the on-screen bitmap is stretched.
3. Applied the same display-mode logic to:
   - main 2-D view
   - compare A/B view
   - volume orthogonal slice panels
   - residual compare rendering

## Recommendation
Use **自适应** as default. It keeps the large-scale browsing path fast, while removing the mosaic effect during zoomed-in inspection.
