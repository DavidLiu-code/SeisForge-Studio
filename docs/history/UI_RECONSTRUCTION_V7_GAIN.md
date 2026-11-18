# Fimage64 v7 — Recovered legacy data gain

This iteration restores the original Fimage v1.5.1 **数据增益 (%)** behavior more faithfully.

## What was recovered from the original x86 EXE

Static disassembly of `TImgForm.FormKeyDown` at original VA `0x4079D0` shows:

- `Q/q`: add exactly `1.0` to the data-gain value.
- `W/w`: subtract exactly `1.0` from the data-gain value.
- `E/e`: select the next color map.
- `R/r`: select the previous color map.

Static disassembly of the original display-range routine around `0x4035F4` shows that **data gain is not an amplitude multiplier**. Instead, it sorts the amplitudes and chooses display limits using symmetric percentiles:

- lower display limit ≈ `gain` percentile;
- upper display limit ≈ `100-gain` percentile.

Thus increasing gain suppresses extreme amplitudes and increases visible contrast of weaker reflections.

## v7 behavior

- Data-loading dialog retains `数据增益` and applies it when Enter/确定 is pressed.
- Valid gain range in the reconstructed version is `0–49%` to avoid crossing the two percentile bounds.
- Re-opening Open restores the cached gain together with the cached trace/time range.
- Browsing another file keeps the current gain as a convenient starting value.
- Parameter dialog `数据增益（%）` uses the same gain value.
- `Q` / `W` adjust gain by exactly one percentage point from the main viewer and immediately re-render the selected range.
- `E` / `R` cycle through the recovered 20 legacy color maps.
- Current gain is displayed in the status bar and Data information dialog.
- Zoom / Origin preserve the current gain.
- Explicit Min/Max value limits take precedence over automatic percentile gain.

## Large-data behavior

Gain changes operate only on the currently selected trace/time window. They do not expand the I/O range beyond the range accepted in 数据加载.

## Validation

A new unit test verifies that increasing gain narrows both tails of the display range according to the recovered percentile behavior. All Go tests pass, and the Windows build is a native `PE32+ x86-64` GUI executable.
