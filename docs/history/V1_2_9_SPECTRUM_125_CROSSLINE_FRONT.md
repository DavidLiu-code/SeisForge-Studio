# Limage v1.2.9 – Spectrum limit and 3-D view order

## Spectrum
- The spectrum QC window now defaults to a maximum frequency of **125 Hz**.
- If the SEG-Y Nyquist frequency is below 125 Hz, the physical Nyquist remains the maximum.
- Added 25-Hz x-axis ticks (0, 25, 50, 75, 100, 125 Hz where available).
- The A/B spectral-difference panel uses the same frequency limit.

## 3-D volume view
The visual/picking z-order has been corrected to:

1. Inline section — rear plane
2. Crossline section — foreground plane
3. Time Slice — top-most horizontal plane

Previously Crossline was rasterized before Inline, so Inline could cover it at intersections and make the camera/view direction appear incorrect.

Mouse hit-testing and draggable-edge priority now use the same foreground order (Time Slice -> Crossline -> Inline), so the plane you visually see in front is also the plane selected by the mouse.
