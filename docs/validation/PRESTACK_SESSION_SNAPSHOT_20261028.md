# Prestack session snapshot validation — 2026-10-28

## Scope

This validation covers the versioned metadata-only session snapshot and its
Gather/Compare toolbar entry points. No source SEG-Y, amplitude samples or
legacy cache format is written.

## Automated checks

- `internal/prestacksession`: schema validation, defensive copies, round-trip,
  unknown-version rejection, trailing-data rejection, overwrite protection and
  atomic temporary-file cleanup.
- Windows cross-compilation of the Prestack UI and snapshot restore path.
- Full repository tests and `git diff --check` passed before the snapshot
  commit.

### Reproducible gate record

- Go toolchain: `go1.26.7 windows/amd64`
- Test command: `go test ./... -count=1` — passed for all packages.
- Diff check: `git diff --check` — passed.
- Build command: `build_windows_release.ps1 -Output SeisForgeStudio_v1.10.4_prestack_session_test_x64.exe`
- PE subsystem: `2` (Windows GUI)
- Test executable size: `6,512,640` bytes
- Test executable SHA-256: `63222918955a41203b05757eae2e3273a86ffdfd6758ebc3921197faf413ddbb`
- Figure recipe rollback commit: `ac05e14`
- Session snapshot commit: recorded as the next commit in this worktree.

## Manual checks

On Windows, verify A-only restore, A/B restore, missing B, moved source with
manual relocation, mapping changes, Compare filters, and page/resize behavior.
Interactive Windows GUI checks were not executed in this environment; they
remain an explicit acceptance step. Record GUI observations separately from
automated results before publishing a release.
