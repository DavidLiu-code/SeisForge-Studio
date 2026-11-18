# Fimage64 v3 — 数据加载 / Range-first reconstruction

This revision reconstructs the original Fimage `TTypeForm` (Caption: `数据加载`) from the supplied 32-bit executable and makes it the first-stage loader.

## Recovered workflow

1. The program opens the original-style **数据加载** dialog before decoding seismic samples.
2. **浏览** selects the input file.
3. Only the SEG-Y binary header and filesystem metadata are inspected to populate:
   - 起始道 / 终止道
   - 采样点
   - 采样率（毫秒）
   - 起始时间 / 终止时间（毫秒）
4. The user edits the desired trace/time window and optional 间隔道.
5. Only after **确定** does the renderer decode seismic amplitudes.

## Range semantics

- 起始道 / 终止道 are **1-based** like the original UI.
- 间隔道 = 0: every trace.
- 间隔道 = 1: every second trace.
- 间隔道 = 9: every tenth trace.
- 起始时间 / 终止时间 are milliseconds and are converted to sample indices using 采样率.

## I/O optimization

v2 selected a sample range at render time but still read each selected trace in full. v3 adds `readTraceWindow()` to the x64 SEG-Y core. The byte offset is now:

`DataStart + trace*TraceBytes + 240 + sampleStart*BytesPerSample`

and the read size is only:

`(sampleEnd-sampleStart+1)*BytesPerSample`.

Thus time cropping occurs at **disk I/O time**, not after loading the complete trace. Trace decimation is also applied before trace reads.

## Current input-format status

The original radio-button interface is preserved for:

- 有卷头工作站格式
- 无卷头工作站格式
- 有卷头微机格式
- 无卷头微机格式
- 二进制数据文件

The v3 computation core currently enables the two **有卷头** modes through robust SEG-Y header detection (big/little endian). The no-header/raw modes remain visible for interface fidelity and are the next compatibility target.
