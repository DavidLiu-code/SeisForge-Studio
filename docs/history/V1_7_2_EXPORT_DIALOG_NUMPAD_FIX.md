# Limage v1.7.2 — 3D Export Dialog and Numeric Keypad Fix

## Fixed: truncated range labels

The 3-D SEG-Y range export dialog has been widened and re-laid out so the Chinese labels are not clipped at common Windows DPI scales.

- `Inline 起始：`
- `Crossline 起始：`
- `Time 起始：`
- `终止：`

The start/end edit columns are now aligned with more horizontal room, and the hint text no longer crowds the inputs.

## Fixed: NumPad numeric entry

The 3-D export range fields now normalize the physical numeric keypad before global 3-D shortcuts are processed.

Supported keys include:

- NumPad `0`–`9`
- NumPad decimal point
- NumPad `+` / `-`

The reported NumPad `1` problem is also handled when NumLock is off: Windows reports the physical NumPad-1 key as `End`; Limage recognizes the non-extended keypad form and inserts `1` in the numeric edit field. The dedicated keyboard `End` key remains a normal navigation key.

`Enter` now triggers Export and `Esc` closes the range dialog while a range field has focus.

## Scope

No changes were made to 3-D rendering, A/B/residual comparison, camera settings, saved defaults, or SEG-Y export semantics.
