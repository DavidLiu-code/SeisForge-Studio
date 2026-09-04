# Limage Phase 10 architecture

## Evidence from the real UI log

The supplied `%SEISFORGE_TEST_CROOKED_PROJECT_DIR%` project contains 62 SEG-Y files totalling 1,657,469,976 bytes. The retained UI trace gives comparable end-to-end pseudo load intervals:

| Build | Begin | Completed/last ready | Elapsed |
|---|---:|---:|---:|
| Phase 8 (`1.9.6`) | 11:46:12.559 | 11:48:48.150 | 155.591 s |
| Phase 9 (`1.9.7`) | 15:26:32.788 | 15:28:32.714 | 119.926 s |

Phase 9 was therefore 35.665 seconds, or 22.9%, faster in the user's actual first run, but it was still dominated by trace-header geometry detection. Its amplitude path used only 233 coalesced reads, yet automatic geometry selection separately read the same sampled trace header three times—once for Ensemble, Source, and Group—and repeated that scan before every `.cidx` hit.

## Single-pass coordinate detection and direct `.cidx` resolution

`segy.DetectCoordinateSpec` now reads each sampled 240-byte trace header once and decodes all three standard coordinate candidates from the retained bytes. Up to four header workers are used per line and a process-wide semaphore limits header I/O to sixteen operations. Candidate order, coordinate scaling, scores, confidence, and chosen geometry remain byte/value equivalent to the old three-pass implementation.

When all traces are sampled, the selected candidate already contains the complete coordinate sequence. A first geometry build therefore passes those values directly to `CrookedLineGeometry` instead of scanning every header again. If exactly one valid standard `.cidx v1` exists for the unchanged source identity, an automatic request resolves that cache before any header detection. If zero or multiple standard candidates are cached, normal automatic detection runs; the optimization never guesses between ambiguous cached settings. The `.cidx v1` path, key, and file schema are unchanged.

## Adaptive loading and quieter previews

Pseudo curtain loading uses two to four independent line Readers according to logical CPU count, capped at four. Each line receives only enough coalesced block workers to keep the existing global I/O ceiling at eight. On the 20-core acceptance machine this selects four line Readers and two block workers per line. Retained indexed textures remain limited to 192 MiB.

Loading previews are merged at a 500 ms gate (at most two per second) instead of 200 ms. The active line is still shown immediately, cancellation still occurs at curtain boundaries, and completion still performs exactly one full-resolution render. This removes CPU and memory-bandwidth competition without lowering final texture dimensions or seismic pixels.

Persistent texture lookup also treats an automatic coordinate request as compatible with an otherwise identical cache keyed by the selected standard coordinate candidate. All geometry, source metadata, gain/clip, AGC, display mode, dimensions, and sample-range checks still apply.

## Acceptance and frozen behavior

With persistent texture hits deliberately disabled but existing `.cidx` metadata available, the final 62-line automated acceptance completed amplitude generation plus one 1000×700 final render in 0.577 seconds on the acceptance machine; the controlled 62/62 persistent-cache repeat took 0.178 seconds. The prior Phase 9 two-reader automated reference was 1.192 seconds. These warm-filesystem measurements are not substituted for the user's UI log, so Phase 10 now records its own completion milliseconds and also displays elapsed time beside the completed progress state for a direct user-side comparison after delivery.

Tests cover single-pass/legacy detection equivalence, detection-header reuse, unambiguous direct cache resolution, ambiguous-cache fallback, auto/explicit persistent-cache compatibility, adaptive I/O bounds, and the existing Phase 1–9 regressions. True Volume3D rendering, camera behavior, 20 palettes, gain/clip definitions, AOI semantics, all view JSON schemas, `.lidx`, and `.cidx v1` remain unchanged.

The independent GUI deliverable is `Limage_v1.9.8_pseudo_faster_x64.exe`.
