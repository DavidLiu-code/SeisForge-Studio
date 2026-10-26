# Prestack metadata cache v1

## Boundary

`BuildIndexCached` is an optional acceleration of `BuildIndex`, not a new
SEG-Y format, mapping detector, processing algorithm, or display cache. A and B
keep their own Dataset, Reader, Mapping, cancellation and generation. Mapping
detection/confirmation runs first. The cache never owns or closes the Reader;
the background index worker closes it before publishing its result to the UI.

The Compare development acceptance rollback point is `e4acd24`. It passed the
small synthetic A/B development gate, not the full real-data/formal-release
matrix. Cache work is a separate commit. The main checkout's interpolation
changes are not included.

## Identity

The key is SHA-256 over explicitly encoded format/decoder versions, canonical
source path, file size, nanosecond mtime, all HeaderMapping fields, Reader layout
(endian, format, bytes per sample, trace geometry, trace count, ns, dt and
extended text-header count), text encoding and metadata fingerprint.

The fingerprint hashes raw primary text/binary/extended text headers plus at
most 64 uniformly spaced 240-byte trace headers including the first and last.
The first and final identity checks both count toward `FingerprintReads`; a
257-trace hit normally reports 128 fingerprint header reads and zero full-scan
headers. No fingerprint read includes sample/amplitude bytes. The retained
open handle and current path are checked for same file, size and mtime, and the
binary header must still match the Reader's opening layout.

This is **not a full-file integrity proof**. Restored-mtime edits to an unsampled
trace header or to amplitude data may be undetected. Use the bypass option for
diagnostics. Publication is followed by another identity check to cover the
Windows retry interval; cold success normally reads up to 192 fingerprint
headers in total. No finite check can eliminate a file modification occurring
after the final source check; callers must not edit an input while it is used.

## Records and reconstruction

`scanIndex` produces the same decoded scan-stage records as the original
`BuildIndex`. The cache stores IDs, scaled source/receiver and original mapped
CDP XY, header offset, scalar/units, original/effective sample axis and validity
flags/error text. It does not store samples, Reader/UI objects, gather tables,
QC aggregates, acquisition relationships, gain, palette, sort, bins or view.

Raw records are streamed to a private temporary file **before** `rebuildIndex`
can replace fallback CDP XY with derived midpoints. There is no duplicate full
record or amplitude array. Cold and hit paths create a fresh index and run the
same deterministic gather/QC/acquisition reconstruction once. Bounds, warnings
and histograms are not accumulated into an already-published index. Existing
zero coordinates, zero Offset, missing fields and invalid-header semantics stay
unchanged.

## Encoding and publication

The independent namespace is
`os.UserCacheDir()/Limage/prestack_metadata/v1/sfmeta-v1-<key>.pidx`.
No sidecar is written beside the SEG-Y and no legacy/unknown `.pidx` is opened.
The explicit little-endian format is:

- 8-byte `SFPIDX\r\n` magic, uint32 format version and decoder version;
- uint32 identity length, uint64 record count and record payload length;
- exact identity bytes, then fixed 116-byte record fields and bounded UTF-8
  header-error text (maximum 4096 bytes per record);
- SHA-256 of the preceding header and payload.

Reader validation checks actual file size, version, full identity, record
count, lengths, finite coordinates, flags, physical order, trailing bytes and
checksum before use. The default whole-cache-file limit is 1 GiB; callers may
choose a smaller cap. Larger requested caps are clamped to 1 GiB. Oversize
caches fall back to the normal memory index, not truncated records.

A unique same-directory `.sfmeta-v1-*.tmp` is buffered, flushed, synced and
closed. After derived reconstruction, cancellation and source identity pass,
Windows uses `MoveFileExW(REPLACE_EXISTING | WRITE_THROUGH)`; other platforms use
same-directory rename. The destination is never removed first. Windows sharing
conflicts receive a short bounded, cancellable retry. Each task cleans only its
own temporary file. Readers see a complete old or new file. Cancellation or a
source change racing after the atomic rename can leave a complete cache for the
old identity, but no index is returned after the final gate fails. The old-key
file is not removed, since another publisher may have replaced it. No global cache
eviction or deletion of other tasks' files is performed.

Missing, corrupt, truncated, unknown-version, oversized or mismatched caches
are misses. Directory/write/publication failures become `WriteWarning`; a
valid in-memory index remains usable. Source read failures, source changes and
cancellation are errors: no index is returned. A final UI generation/owner
gate still rejects any result or progress belonging to a stale A/B task.

## UI and observability

Both final-mapping A/B index workers use the same cache API. Progress retains
owner and dataset/mapping/selection tokens; accepted completion reports cache
hit or scan fallback using the existing status bar. No toolbar row was added.
Completion of header caching is not completion of amplitude rendering.

`IndexBuildStats` records hit, miss reason, path, scanned headers, fingerprint
reads from completed successful identity passes, elapsed duration and nonfatal
warning. Partial reads of an identity pass that returns an error are not counted.
A missing new
identity path cannot distinguish first use from a changed identity without a
separate manifest; it is reported as unavailable, not falsely as a known cause.

See [user controls](../PRESTACK_METADATA_CACHE.md) and
[cache acceptance](../testing/PRESTACK_METADATA_CACHE_ACCEPTANCE.md).
