# Limage Phase 3 Architecture

## Purpose

Phase 3 adds a collection-level crooked-line project while retaining the Phase
1 `Dataset`/`Workspace` split and the Phase 2 single-line renderer. It also
removes hover flicker by separating the Crooked static scene from transient
cursor overlays. Ordinary 2-D and Volume 3-D render paths are unchanged.

## Core ownership

- `internal/project.CrookedProject` owns project identity, navigation-file
  identity and an ordered list of datasets. It owns no reader, window, active
  selection, camera or render buffer.
- `CrookedProjectLine` owns a stable line ID, line name, immutable dataset
  descriptor, DAT navigation points and a metadata-open error when applicable.
- `Application.OpenCrookedFolder` and `OpenCrookedPaths` are the collection
  routers. They validate metadata before calling `WorkspaceManager.OpenProject`.
- `ProjectWorkspace` is an optional companion to the unchanged `Workspace`
  interface. Only the Crooked adapter implements it.
- `crookedSession` owns the active project, active line, per-line view state,
  active reader, active render image and asynchronous generations.

## Load chain

```text
Folder / multi-select / Recent / drop / command-line folder
  -> Application.OpenCrookedFolder or OpenCrookedPaths
  -> project.OpenFolder or OpenPaths
  -> metadata-only Dataset descriptors + optional DAT navigation
  -> WorkspaceManager.OpenProject (atomic switch)
  -> crookedWorkspaceAdapter.OpenProject
  -> startCrookedProject
  -> active line BuildCrookedCachedProgress + render
  -> optional two-worker metadata scan for lines without DAT navigation
```

The project map can appear immediately from DAT navigation. Only the active
line retains a `segy.File`; background geometry readers close after their header
scan. Project, active-load and render generations independently reject stale
results.

## Coordinate alignment

The `.cidx` v1 cache remains raw SEG-Y metadata. When DAT navigation exists,
the active display geometry is a scaled copy. CDP values are aligned with
piecewise DAT trace interpolation and evaluated against decimal multipliers
`10^-3` through `10^3`. A multiplier is accepted only with at least two
matches, residual no greater than 0.5% of line length (minimum five coordinate
units), and a threefold margin over the next candidate. The source data and
cache are never rewritten.

## Paint lifecycle

`crookedBaseDC` caches the background, project trajectories, active seismic
raster, axes and labels. `WM_PAINT` copies only the invalid update region with
`BitBlt`, then draws the hover line, active trace marker and section crosshair.
Hover invalidates old/new cursor strips and marker rectangles rather than the
full client. Base cache rebuilds are limited to resize, project/line changes,
new render output and display-parameter changes. GDI bitmap/DC selection is
restored before every cache deletion.

## Persistence and compatibility

- Workspace mode numbers remain 1/2/3.
- `start_center.json` v1 accepts an optional `paths` array for a multi-select
  Crooked Recent item; all prior entries still decode.
- `crooked_view.json` v1, `volume_view.json` v1, `.cidx` v1 and `.lidx` v1 are
  unchanged.
- Palette order, palette 2 default, gain/clip, adaptive display, Volume camera,
  projection and interaction formulas are unchanged.

## Future 3-D fusion boundary

The project model preserves each line's dataset timing metadata and world-space
navigation independently of the Win32 session. A later workspace may consume
these lines as XY-time seismic curtains and add interpolation/voxelization.
Phase 3 performs neither fusion nor multi-section amplitude overlay.
