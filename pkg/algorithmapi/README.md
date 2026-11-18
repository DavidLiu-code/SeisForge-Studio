# External algorithm protocol v1

`pkg/algorithmapi` and `pkg/algorithmapi/artifact` are importable by a separately
maintained Go algorithm executable. They have no Win32, UI, SEG-Y or private
algorithm dependency. `manifest.schema.json` describes the interchange fields;
`ParseManifest` and `NormalizeParameters` provide the host's stricter bounded
validation, duplicate-ID checks and defensive normalized copies. Parameter
`step` defines permitted increments anchored at `min` (or zero if omitted), for
values and range endpoints. Integer increments are checked exactly; floating
increments tolerate representation noise. Off-grid values are rejected, never
silently rounded or quantized. `range` values are
objects with `min` and `max`; `group` values are nested objects. Integers must be
exactly representable by JSON numbers (absolute value at most 2^53-1).

Parameters may also declare `"ui": {"section": "common", "order": 0}` or
use `"advanced"` for optional placement in the host form. Lower order values
appear first, and equal orders retain manifest declaration order. Missing hints
keep the host's original declaration order. These fields do not change parameter
IDs, defaults, validation constraints, or the normalized values sent in
`RunRequest`. A module that reads its own manifest with a strict decoder must
use a decoder that understands the optional `ui` property before its manifest
adds this hint.

The host invokes an authorized executable directly (never through a shell):

```text
algorithm.exe --describe
algorithm.exe --run --request request.json --input input.sfdata --output output.sfdata
```

`--describe` returns one descriptor JSON object. `--run` writes JSON Lines events
to stdout: `started`, `progress`, `log`, and a final `result` with `status: ok`
and `output: output.sfdata`. Diagnostics belong on stderr. Requests contain
metadata and parameters, never sample arrays or original SEG-Y paths. The host
still treats external programs as trusted native code, not as a security sandbox.

## .sfdata v1

All integers and IEEE floats are little endian. Samples are contiguous on the
last axis. Supported shape/axes are `[trace,sample]` and
`[inline,crossline,sample]`. Host UI may restrict execution to 2D sections without
limiting the file protocol. `SampleStart` is a zero-based original sample offset;
`TimeOriginUS` is the original record origin, not an additional offset. Physical
sample time is `TimeOriginUS + (SampleStart + localSample) * SampleIntervalUS`.

The fixed 192-byte header contains:

| Offset | Encoding | Meaning |
| --- | --- | --- |
| 0 | 8 bytes | `SFDATA1` followed by NUL |
| 8 | uint32 | version = 1 |
| 12 | uint32 | metadata JSON length (maximum 1 MiB) |
| 16 | uint64 | flattened trace count |
| 24 | uint64 | samples per trace |
| 32, 40, 48 | uint64 each | geometry, mask, sample block lengths |
| 56, 88, 120, 152 | SHA-256 each | metadata, geometry, mask, sample blocks |
| 184 | 8 zero bytes | reserved |

Four blocks follow in the same order. Metadata JSON contains only dtype, shape,
axis names and sampling-axis scalars; coordinates and samples are never JSON
arrays. Each geometry record is 48 bytes: TraceID int64 (nonnegative), Inline
int32, Crossline int32, X/Y float64, per-trace original TimeOriginUS int64,
CoordinateValid uint8, and seven zero padding bytes. Invalid coordinates are
canonical `(0,0)` and distinct from a valid origin via CoordinateValid.

The mask is one byte per sample (0 or 1). Samples are float32 or float64. Invalid
samples are canonical zero; valid samples must be finite. Missing/invalid mask
positions cannot silently become valid output. Every block length, shape
multiplication and file size is checked before allocation. Open checks all
checksums and valid finite samples in bounded windows. `Fingerprint` is SHA-256
of the complete artifact, including metadata and headers, not only amplitudes.

`Write` reads one trace at a time, syncs a unique temporary file and publishes by
same-directory hard link; existing targets are never overwritten. A filesystem
without atomic hard-link support produces an explicit error rather than an
unsafe overwrite. Cancellation or failure removes this task's temporary file.
`ReadTraceWindow(trace,start,end)` uses zero-based, end-exclusive sample bounds.
Metadata accessors return defensive copies. `CheckCompatibility` requires exact
shape, axes, sample interval/origin/window, trace IDs/order, geometry and mask;
dtype may differ explicitly without hidden conversion. Export to a lower
precision format remains the host's responsibility.

The format is a data interchange contract, not executable code. The host still
validates task ownership, generation, authorization and output directory before
attaching an artifact as B. No Reader, HWND, GDI, amplitude cache or authentication
credential belongs in `AlgorithmRunRecord`.
