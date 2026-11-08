# Prestack `.pidx` metadata cache acceptance — 2026-10-26

## Scope and rollback

- Worktree: `Limage_v1.8.7_src_pidx`, branch `prestack-pidx`.
- Compare acceptance boundary: `e4acd24` (parent `ad9aff9`). See
  [Compare development gate](PRESTACK_COMPARE_ACCEPTANCE.md).
- Cache implementation, A/B integration and tests form the next independent
  local commit. Revert that cache commit without reverting Compare/P0/P1.
- No push, release, version promotion, tag movement, RC1 overwrite or changes
  to the sibling checkout's uncommitted interpolation work.
- Source SEG-Y and existing `.lidx/.cidx/.ptx`, unknown `.pidx` and JSON formats
  are not modified. New synthetic files/cache paths live in temporary folders.

## Automated results: PASS

`go version go1.26.7 windows/amd64`, workspace-local `.tmp-go-cache`.

Commands run:

```powershell
gofmt -w <changed Go files>
go test ./... -count=1
go test ./internal/prestack -run '^TestBuildIndexCached' -count=10
git diff --check
go build -trimpath '-ldflags=-s -w -H=windowsgui' -o SeisForgeStudio_v1.10.4_pidx_test_x64.exe ./cmd/limage
```

All packages passed the final full-suite run at 21:07 (+08:00):
`cmd/limage` 1.003s, `internal/prestack` 1.207s, `internal/segy` 0.520s;
command wall time approximately 2.02s including formatting/diff/version checks.
Cache-specific tests repeated ten times passed in 8.928s; after adding the final
post-publication identity check, another three runs passed in 2.743s. The
subsequent full suite includes that guard. Build succeeded, PE Subsystem is
`2` (Windows GUI). Elapsed timings are observations, not performance assertions.

Coverage includes:

- Real Reader reopen for normal/classified 5 Shot x 12 Receiver fixtures;
  strict cold/uncached/hit whole-index equality, all six gather kinds, multi-key,
  QC JSON/CSV, acquisition relationships, Compare matches and reports.
- Stable raw mapped CDP midpoint preservation before derived rebuilding;
  disabled coordinate mapping, positive/negative scalar, valid zero coordinates,
  signed/zero Offset, ID conflict and missing/invalid header semantics.
- 257-trace hit with zero full-scan headers and bounded fingerprint reads;
  precise header-only ReaderAt regions (including extended text), no reads of
  amplitude bytes by the fingerprint. Cache bytes do not scale with ns, and
  altered amplitude contents do not enter the serialized metadata records.
- Every Mapping field, source size, mtime, layout and sampled header changes;
  binary opening snapshot checks; sampled edits with restored mtime.
- Truncation, unknown magic/version, flipped payload/digest, trailing bytes,
  re-signed invalid lengths/counts, cap overflow; all fall back to scanning.
- Unusable cache directory and publication failure are warnings; memory index
  remains valid, source Reader stays caller-owned, task temporary files vanish.
- Environment/option bypass skips cache read and write, including damaged
  entries; existing cache bytes are left untouched.
- Cold/hit cancellation, source closure and changes during progress callbacks
  fail without returning an index; eight concurrent publishers leave a readable
  full cache and no task temp files.
- A/B final independent Mapping gate; confirmation precedes cache lookup;
  B remapping misses only B's key, A stays unchanged. Dataset-to-Reader source
  changes fail before detection/cache; exclusive reopen verifies Reader release.
- A/B stale progress/completion token handling, close-B and cancellation clear
  only the appropriate pending results. Progress does not claim completion
  before the final source and UI-token gate. Existing Compare Reader/GDI tests
  remain passing; ordinary workspace/core regression tests remain passing.

No race-detector result is claimed (this toolchain environment has CGO disabled
and no C compiler configured). Tests are native Windows, not cross-compile-only.

## Real SEG-Y metadata smoke: PASS (read-only, not GUI)

Explicitly set `SEISFORGE_PRESTACK_REAL_SEGY` to the user-provided
`E:\地震局宋专家\tjd-shot11-38_common_offset_binned20m_full.sgy` and ran:

```powershell
go test ./internal/prestack -run '^TestMetadataCacheRealSourceSmoke$' -count=1 -v
```

Observed 68,432 records. Cold scan: `3.9957625s`; cache hit: `122.8305ms`.
The full derived index was deeply equal, including records, QC and associations.
Cold scanned 68,432 headers; hit scanned zero. Total fingerprint header reads
were 192/128. No waveform samples were read by this test. Cache was in the Go
test's temporary directory and removed by cleanup. This single warm-filesystem
comparison is not a general speedup benchmark and does not time rendering.

When the environment variable is absent, this optional test explicitly skips;
the actual opted-in run above is separate from the standard full-suite run.

## GUI evidence and remaining release checks

The preceding Compare development GUI gate and user-confirmed Ctrl+click
markers/physical trace analysis are recorded in the Compare acceptance file.
No new automated mouse session was started for cache implementation. The cache
build's status-bar wording/repeated-open UI has not been manually re-accepted;
A/B workers and token routing are covered by native UI-free tests, not claimed
as a new interactive GUI run.

Full real-data ordinary 2-D/3-D/crooked/pseudo-3-D GUI regression and long-running
resource soak remain outside this small-fixture/cache gate. No formal release
is authorized by these results.

## Artifact provenance

The independent output is `SeisForgeStudio_v1.10.4_pidx_test_x64.exe`; the embedded
application version remains `1.10.4-rc1` (no promotion). After the cache commit,
the same build command is repeated on the clean worktree. Exact source commit,
Go version, final EXE size, SHA-256 and PE Subsystem are recorded in ignored
`artifacts/prestack_release_candidate/pidx-build-2026-10-26.json` and the task
delivery message. This avoids embedding a self-referential commit/hash into
tracked source. EXE, fixtures, runtime caches and local logs are not committed.

See [cache architecture](../architecture/ARCHITECTURE_PRESTACK_METADATA_CACHE.md)
and [user instructions](../PRESTACK_METADATA_CACHE.md).
