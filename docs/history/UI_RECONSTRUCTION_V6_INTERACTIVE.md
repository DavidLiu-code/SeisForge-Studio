# Fimage64 v6 — interaction pass

This pass continues the static reconstruction of the supplied Fimage v1.5.1 executable while keeping the range-first 64-bit SEG-Y reader.

## Keyboard behavior

- In `数据加载`, **Enter = 确定** regardless of whether focus is on an edit box or button.
- In `数据加载`, **Esc = 取消** and the previously cached seismic section remains visible.
- The same Enter/Esc behavior is also enabled for the reconstructed `参数` window.

The reconstructed windows are ordinary Win32 windows rather than resource-based dialog boxes, so v6 intercepts these keys in the main message loop to reproduce the original VCL dialog behavior.

## Functional Zoom

The legacy `ZoomTBtnClick`, `PBoxMouseDown`, `PBoxMouseMove`, and `PBoxMouseUp` handlers were recovered at x86 addresses 0x4055D8, 0x4063CC, 0x406434, and 0x4065EC. v6 now implements the corresponding workflow:

1. Press the Zoom toolbar button.
2. The cursor changes to a crosshair.
3. Drag a rectangle over the seismic image.
4. The rectangle is mapped back to the current trace/sample coordinates.
5. Only the selected trace/time window is re-read and rendered.

The selected rectangle is drawn while dragging and very small accidental rectangles are ignored, matching the behavior visible in the recovered legacy handler.

## Safe Origin / Restore

The original range accepted in `数据加载` is now stored as the **Origin range**. Zoom changes only the current view. Pressing `Origin/还原` restores the accepted load range, not the full SEG-Y file.

This is important for large datasets: selecting 1000 traces out of a 100,000-trace file, zooming, and restoring will stay within those 1000 traces.

## Mouse coordinates

Moving the mouse over the seismic image now reports:

- trace number,
- sample index,
- time in milliseconds.

This conversion uses the current displayed trace/sample window and does not perform extra disk reads.

## Fixed button

The recovered Fixed button now has useful behavior:

- checked: resize stretches the cached rendered bitmap only (no additional seismic I/O),
- unchecked: after the user finishes resizing the main window, the selected seismic window is re-rendered at the new display resolution.

## Preserved behavior

v6 keeps the v4/v5 transactional cache behavior: opening the load dialog, cancelling, closing it, or failing to load a new file does not clear the previously displayed seismic section.
