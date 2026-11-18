# Fimage64 v4 — cached/reopen-safe Open workflow

This revision changes the reconstructed **数据加载 / Open** workflow so that reopening it does not clear the user's current context.

## Re-clicking Open

If seismic data is already displayed, clicking **Open** now repopulates the load dialog with the currently cached selection:

- same input file path;
- previous 起始道 / 终止道;
- previous 间隔道;
- file sample count and sample interval;
- previous 数据增益;
- previous 起始时间 / 终止时间;
- detected workstation/PC endian radio selection.

The existing seismic image remains the active cached dataset while the dialog is open.

## Cancel-safe behavior

Pressing **取消**, closing the load dialog, or abandoning a new file selection leaves the existing seismic section unchanged.

## Transactional replacement

A new dataset is committed only after:

1. the new SEG-Y file opens successfully; and
2. the selected trace/time window renders successfully.

The old file handle is kept open until both steps succeed. If the new load or render fails, the new file is closed and the previous cached seismic data/range remain active.

This prevents the common destructive workflow where pressing Open clears the current section before a replacement has been validated.
