# Limage Phase 4 Architecture

## Delivered behavior

Phase 4 adds an empty Crooked workspace entry and a separate pseudo-3D curtain viewer. The Home Crooked card no longer owns file selection: it activates an empty shell, and the shell accepts one file, multiple files, a folder, Recent entries, or drag/drop. The regular 2-D and Volume3D routes remain unchanged.

Release executables are built with the Windows GUI PE subsystem. `build_windows_release.ps1` runs the full test suite, applies `-H=windowsgui`, and rejects a console-subsystem result. `build_windows_release.cmd` invokes that script with a local execution-policy bypass for machines whose default PowerShell policy blocks `.ps1` files.

## Empty workspace route

```text
Home Crooked card
  -> Application.OpenWorkspace(KindCrooked)
  -> WorkspaceManager.OpenEmpty
  -> crookedWorkspaceAdapter.OpenEmpty
  -> resetCrookedToEmpty
  -> show empty Crooked shell
```

`EmptyWorkspace` is optional and does not modify the original `Workspace.Open(OpenRequest)` contract. The manager assigns the generation and performs the same target-accepts-first atomic switch used by dataset and project opens. A committed empty activation clears `Application.current` and `Application.project`.

Opening a project from the empty shell continues through the Phase 3 collection route:

```text
Crooked file/folder/drop command
  -> Application.OpenCrookedFolder/OpenCrookedPaths
  -> WorkspaceManager.OpenProject
  -> crookedWorkspaceAdapter.OpenProject
  -> startCrookedProject
```

Recent entries deliberately bypass the empty route and reopen their saved project.

## Pseudo-3D data flow

The modeless `Limage64Pseudo3D` window belongs logically to the current Crooked project. Starting another project, emptying or closing the Crooked workspace, or changing workspaces closes or hides the pseudo window and invalidates its generations.

```text
CrookedProject + coordinate spec + calibrated geometry snapshot
  -> default selection: all valid lines
  -> two background line workers
     -> independent Dataset.OpenReader
     -> .cidx geometry build/cache when needed
     -> project-level DAT/CDP decimal coordinate calibration
     -> full-time 8-bit indexed section texture
     -> close Reader
  -> internal/pseudo3d curtain scene
  -> single latest-result render worker
  -> PostMessage to UI thread
  -> StretchDIBits back buffer
```

Each selected line becomes a set of vertical textured quads along acquisition-order XY coordinates. The renderer uses perspective-correct interpolation and a depth buffer; it does not interpolate amplitudes between lines or create a volume. Raw X/Y bounds and sample-interval-derived milliseconds are retained for labels. Vertical time scale is an independent display factor.

The active line bootstraps an unambiguous decimal multiplier before the remaining line queue starts. That multiplier is then applied to every line in the same navigation project, including sparse lines that cannot independently infer it. This is why the supplied 62-line survey consistently uses `0.1` rather than allowing isolated lines to fall back to `1`.

## Selection, memory, and lifetime

- The pseudo window uses a checkbox ListView. Check state controls visibility; row focus controls the orange highlighted curtain.
- All valid lines start checked. Invalid datasets remain unchecked and show an error state.
- Exactly two line workers can own SEG-Y readers concurrently. A reader closes immediately after its line geometry and indexed texture are ready.
- Retained textures are one byte per pixel, capped at 1024 x 1536 per line and planned under a 192 MiB aggregate budget. Unchecking a line drops its texture.
- Project/window, per-line load, and scene-render generations reject stale work. The render worker keeps only the most recent queued camera/selection scene.
- `WM_ERASEBKGND` is suppressed; the CPU scene is painted from a complete BGRA back buffer.

## Parameter compatibility

- `volume_windows.go` and `volume_view.json v1` are not used or modified by pseudo-3D.
- `crooked_view.json v1` remains unchanged and continues to own the shared 20-color palette and gain.
- `pseudo_view.json v1` owns only azimuth, elevation, FOV, zoom, pan, and vertical scale. Selection is intentionally not persisted, so every new project defaults to all valid lines.
- Existing `.lidx v1`, `.cidx v1`, Recent numeric modes, clip 99%, display mode, license, export, spectrum, and true 3-D behavior remain compatible.
