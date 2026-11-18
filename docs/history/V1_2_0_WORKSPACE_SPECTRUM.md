# Limage v1.2.0 — Unified Workspace + Spectrum QC

## Unified entry workspace

The former A/B compare window is now the visible application root.

- With only **A** loaded, the same workspace browses **Inline / Crossline / Time Slice**.
- Click **比** to load B and upgrade the current view to synchronized A/B QC.
- **差** is disabled until B exists. When enabled, it adds the A-B residual panel.
- **体** remains a separate entry to the linked three-view volume browser.
- **谱** opens spectrum QC for the current visible seismic context.

The old entry-window controls were migrated to the workspace toolbar: Open, Save,
color map, Fixed, colorbar/mark, data information, parameters, Zoom, Origin,
feedback, Update and About.

The old compact main window still exists internally as a hidden compatibility
controller so that the reconstructed legacy dialogs can be reused without
rewriting them again. It is not shown to the user.

## A-only browsing

A no longer requires B before geometry preparation. Automatic IL/XL detection,
persistent geometry indexing, line extraction, Time-Slab caching, palette/gain,
zoom, crosshair and annotations all work with A alone.

Opening a new A clears B/residual state. Loading B later rebuilds a shared A/B
geometry context while preserving the selected A trace/time range.

## Spectrum QC

The spectrum button uses the current view rather than a hard-coded trace.

### Inline / Crossline

Limage obtains the traces in the currently visible line/range. If B is present,
A and B are paired by the common geometry coordinate before spectrum averaging.

### Time Slice

The current visible IL/XL bounds are used to find traces in the spatial window.
The frequency analysis uses the current A time/sample window, not a single time
sample, because a temporal spectrum cannot be estimated from one Time Slice.
A/B traces are paired by common (Inline, Crossline) bins when B exists.

### Estimator

For speed and stability:

1. uniformly sample at most 96 traces from the current region;
2. remove each trace mean;
3. apply a Hann window;
4. use a radix-2 FFT with up to 4096 samples;
5. average the linear amplitude spectra;
6. normalize each data set to 0 dB peak for comparison.

With B loaded the spectrum window shows:

- normalized A spectrum;
- normalized B spectrum;
- A-B spectral difference in dB, interpolated onto A's frequency grid up to the
  common Nyquist frequency;
- A/B dominant-frequency estimates, trace count and FFT length.

The spectrum implementation uses the same 64-bit windowed SEG-Y I/O and does not
load a full cube into memory.

## Startup behavior

Limage now opens directly into the unified workspace. The range-first data-load
dialog appears only after clicking Open (or when a SEG-Y path is supplied on the
command line).

## Keyboard shortcuts

Within the workspace:

- Q: gain +1%
- W: gain -1%
- E: next color map
- R: previous color map
