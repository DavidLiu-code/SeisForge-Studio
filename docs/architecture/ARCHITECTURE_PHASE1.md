# Limage v1.9.0 Phase 1 architecture

## Scope

Phase 1 introduces dataset, geometry, workspace and application boundaries while retaining the native Win32 renderers from v1.8.7. It does not implement Crooked geometry/UI, X/Y/CDP scanning, Map View or renderer file splitting.

The central rule is that data geometry and the selected display workspace are independent:

- `geometry.KindRegular3D` describes a dataset.
- `workspace.Kind3D` describes where a dataset is opened.
- A dataset can therefore be explicitly requested in the 3-D workspace before its geometry has been detected. If asynchronous detection fails, the 3-D shell remains active and shows the error.

## Packages and ownership

### `internal/dataset`

`SeismicDataset` owns only an absolute, cleaned path, immutable SEG-Y/file metadata and an optional `geometry.Geometry`. `Manager.Open` opens the file only long enough to validate and snapshot metadata, then closes it. `OpenReader` creates a new `*segy.File` on every call, so 2-D, A/B and 3-D lifetimes remain independent without copying amplitude arrays.

It does not own HWNDs, focus, comparison state, rendering buffers, camera state or a long-lived file handle.

### `internal/geometry`

The fixed interface is:

```go
type Geometry interface {
    Kind() Kind
    TraceCount() int64
    TraceLocation(trace int64) (TraceLocation, bool)
    Bounds() Bounds
}
```

`Line2DGeometry` represents a trace-ordered line. `RegularGridGeometry` wraps the existing `segy.GeometryIndex`; it does not alter the on-disk `.lidx` v1 cache. `GeometryIndex.TraceAt` is now exposed as a read-only exact-bin lookup over the same cached fields.

Automatic recommendation keeps the v1.8.7 Phase 1 thresholds exactly: score `>= 75` and confidence `>= 0.58`. It samples 3000 headers, matching the Volume worker. When two header locations contain the same valid grid word, the best candidate's grid confidence decides whether the dataset is three-dimensional; byte-pair ambiguity no longer forces an otherwise verified grid into Line2D. A weak or unavailable grid recommendation still resolves to Line2D. Crooked classification is reserved for a later phase.

### `internal/workspace`

Workspace numeric values retain Recent JSON compatibility: `Kind2D=1`, `Kind3D=2`, `KindCrooked=3`; `KindHome=0`, while `KindAuto=255` is a router request and is never registered as a real workspace. The Win32 compatibility constants are explicit `1/2/3` values in their own declaration, preventing unrelated `iota` constants from reversing Home-card routing.

The fixed adapter interface is:

```go
type Workspace interface {
    Kind() Kind
    Open(OpenRequest) error
    Show() error
    Hide() error
    Close() error
}
```

`OpenRequest` contains a Dataset, a request generation and optional inclusive trace/sample ranges. An omitted range means the complete dataset.

`Manager.Open` serializes transitions. The target must validate and accept `Open` before the current workspace is hidden. A synchronous failure leaves the previous workspace, generation and pending work valid. On commit, the previous workspace is retained in history; `CloseActive` and native `NotifyClosed` restore it. Each committed request advances a generation token, and `IsCurrent` rejects expired asynchronous completion.

`Adopt` exists only for the legacy range-load dialog, which still renders before Phase 1 sees the accepted Dataset. It performs no UI call.

### `internal/app`

`Application.OpenPath` is the only new path-based router:

```go
data, err := application.Data.Open(path)
if err == nil {
    err = application.Workspaces.Open(workspace.Kind3D, data)
}
```

`KindAuto` probes trace headers and resolves to `Kind2D` or `Kind3D`. `OpenDataset` routes an already-open Dataset and is used by the 2-D “体” button. `CurrentDataset` is updated only after WorkspaceManager commits the request.

## Win32 compatibility adapters

`cmd/limage/app_phase1_windows.go` registers four adapters:

- Home: owns Start Center visibility, no seismic data.
- Line2D: supplies request ranges to `loadSelectedFile`, then reuses the current render and compare synchronization functions.
- Volume3D: creates a hidden Volume shell, starts the existing geometry/index worker and becomes visible only when WorkspaceManager commits. It never calls the 2-D loader or renderer.
- Crooked placeholder: has its own `WorkspaceKind` but deliberately reuses the Line2D adapter/display during Phase 1.

The adapters are lifecycle boundaries; projection, depth testing, texture generation, camera, interaction and drawing remain in their original functions/files.

## Call chains

### Before Phase 1

```text
Home/Recent/drop
  -> decide pendingWorkspaceMode
  -> loadFile
  -> loadSelectedFile
  -> rerender
  -> workspaceSyncAFromMain
  -> completeWorkspaceOpen
       -> optional showVolumeWindow

2-D “体”
  -> showVolumeWindow
  -> showVolumeWindowForPath(sf.Info.Path)

Home 3-D interim fix
  -> openHome3DDirect
  -> showVolumeHomeShell
  -> loadVolumePathIntoShell
```

### Phase 1

```text
Home card / Recent / first drag-drop / command-line path
  -> openHomeFile or openApplicationPath
  -> Application.OpenPath
       -> DatasetManager.Open
       -> optional automatic Geometry probe
       -> WorkspaceManager.Open
            -> Line2D adapter OR Volume3D adapter OR Crooked placeholder

Home explicit 3-D
  -> Application.OpenPath(Kind3D)
       -> DatasetManager.Open
       -> WorkspaceManager.Open(Kind3D)
            -> Volume3D.Open
                 -> createVolumeWindowShellDeferred
                 -> loadVolumePathIntoShell
                 -> existing asynchronous detect/index/render chain

2-D “体”
  -> Application.CurrentDataset
  -> Application.OpenDataset(Kind3D, WithSampleRange(current range))
  -> WorkspaceManager.Open(Kind3D)
```

Home explicit 3-D has no reference to `loadFile`, `loadSelectedFile`, `rerender`, `workspaceSyncAFromMain`, `completeWorkspaceOpen` or `showVolumeWindow`. A geometry error is handled inside `handleVolumeReady`, updates the 3-D status/error dialog, and does not invoke any 2-D route.

Subsequent drops onto an already-loaded A workspace remain the existing A/B operation, not a new workspace open.

## Global state inventory

The following is the complete package-global state inventory after Phase 1, grouped by owner. Phase 1 does not claim that the Win32 globals are already eliminated; it puts a single boundary in front of them so later phases can move one cluster at a time.

| Owner/file | Global state |
| --- | --- |
| Phase 1 router (`app_phase1_windows.go`) | `application`, `phase1VolumeDataset`, `phase1VolumeWorkspaceGeneration`, `phase1ManagerClosingVolume` |
| Core dataset | per-Dataset `Path`, `Metadata`, mutex-protected optional `geometry`; no open Reader |
| Core workspace manager | registered adapters, active adapter, history stack, committed generation, transition/RW mutexes, trace callback |
| Core application | Data/Workspace services and mutex-protected current Dataset |
| Main 2-D data/display (`main_windows.go`) | `sf`, `indices`, `bgra`, `imageW/H`, `lastStats`; `paletteIndex`, `clipPercent`, `agc`, `gainPercent`, `renderDisplayMode`, `useLimits`, `limitMin/Max`; `traceStart/End/Step`, `sampleStart/End`; `originTraceStart/End/Step`, `originSampleStart/End`, `originValid`; zoom drag coordinates/mode, status text; main/parameter/mark HWNDs, controls, fonts, icons and bitmaps |
| Compare (`compare_windows.go`) | `compareAPath/BPath`, `compareMode`, A/B/D panels, B/diff visibility and map limits; `compareTD`; async mutex/pending prepare/slice/geometry results and three generations; detected header bytes/auto flag; current/origin trace/sample ranges; zoom snapshots/drag state; palette, crosshair, annotation tool/list; line/time coordinates; progress/busy/cache DC/bitmap state and export result channel; compare HWND/controls |
| Volume data/render (`volume_windows.go`) | `volumePath`, independent `volumeF/G/C`; IL/XL/Time panels and selected IL/XL/sample/range; palette and time values/raster/mask/range; crosshair location; busy/generation/pending result/mutex; base DC/bitmaps/dirty flag; `volume3D`, active/hover/focus axes; minimal/view-size/aspect/style/interaction/drag/coordinates; axis multipliers and default aspect; FOV and compare-hidden/force-on-ready flags |
| Volume A/B and camera (`volume_windows.go`) | compare mode/active side/two `volumeScenes`, render override, diff residual panels/key/note; azimuth/elevation/zoom/FOV/pan; rotation/pan/zoom drag starts and movement flags; saved default view/readiness; slice-drag axis/start indices/sample/time; Volume HWND/controls |
| Volume fixed catalogs (`volume_windows.go`) | `volumeAxisFactorOptions`, `volumeFOVOptions`, `volumePaletteOrder`, `volumeStyleComboOrder`, `pGetKeyStateVolume` |
| Home/Recent/drop (`workspace_windows.go`) | `pendingWorkspaceMode` (legacy dialog only), `activeWorkspaceMode`; Start Center state/load flag/hover/pressed/drag/fonts; Shell drop procedures |
| Native OLE drop (`drop_target_windows.go`) | OLE DLL procedures, initialization flag, HWND-to-target map, COM vtable and IID constants |
| Range-load dialog (`load_dialog_windows.go`) | load HWND/owner/controls, probed path/info/valid flag |
| Volume export (`volume_export_windows.go`) | export HWND/controls/result channel |
| Spectrum (`spectrum_windows.go`) | spectrum HWND and current spectrum view data |
| License (`license_windows.go`) | registry/volume procedures, license HWND/controls, activated/cancelled flags, current license and machine ID |
| Native platform layer (`main_windows.go`) | Win32 DLL handles and all User32/GDI32/Kernel32/CommonDialog/CommonControls/Shell procedure handles |
| Immutable visual assets/catalogs | `paletteNames`, `legacyAnchors`, `displayModeNames`, embedded icon bitmap/ICO data |

Persistent external state remains:

- `%LOCALAPPDATA%\Limage\volume_view.json` schema v1, unchanged.
- `%LOCALAPPDATA%\Limage\start_center.json` version 1 and Recent numeric modes 1/2/3, unchanged.
- Existing license files/registry behavior, unchanged.
- Geometry `.lidx` cache magic/version 1 and location, unchanged.
- `%LOCALAPPDATA%\Limage\workspace_trace.log`, new append-only lifecycle trace.

`workspace_trace.log` contains timestamp, workspace, previous workspace, Dataset basename, Geometry kind, generation, action and a path-redacted error. It never writes SEG-Y headers, coordinates or amplitude samples.

## Frozen rendering contract

Phase 1 does not move or alter:

- 3-D projection, z-buffer/depth test, texture/slice rasterization, camera math, pan/zoom/rotation or slice interaction formulas.
- `volume_view.json` v1 fields, load/save path and compatibility rules.
- FOV 0–120 options, axis multipliers 0.10–8, view size/aspect modes and camera clamps.
- Twenty legacy palettes/anchors, default palette 2, Volume palette order, gain 0–49%, clip 99%, adaptive display mode.
- Style enum (`CIGVis`, `解释`, `标准`, `干净`), its established combo order, or 2-D Full View behavior.
- Recent JSON, cache v1, shortcuts/NumPad, export, spectrum and license behavior.

Automated regressions freeze JSON v1 keys/round-trip, factory defaults, palette/style/FOV/axis orders, display defaults and a deterministic `RenderStats` plus rendered-pixel SHA-256 baseline.

## Compatibility API retirement order

1. Remove `pendingWorkspaceMode` and make the range-load dialog construct an `OpenRequest` directly; then remove `Application.AdoptDataset` and `WorkspaceManager.Adopt`.
2. Replace `loadFile` with `Application.OpenPath(Kind2D)` everywhere; keep `loadSelectedFile` only inside the Line2D adapter until renderer extraction.
3. Move `workspaceSyncAFromMain` behind a compare data-session service; remove `completeWorkspaceOpen` once no legacy dialog calls it.
4. Replace `showVolumeWindow` and `showVolumeWindowForPath` callers with `Application.OpenDataset/OpenPath(Kind3D)`; keep `loadVolumePathIntoShell` as a private Volume adapter implementation.
5. Split comparison and volume Renderer/session state out of package globals only after pixel and interaction regression baselines cover the move.
6. Replace the Crooked placeholder with an XY/CDP geometry implementation and dedicated UI in a later phase.

## Verification matrix

- Dataset: valid/corrupt SEG-Y, absolute metadata, validation-handle close, independent Readers and descriptor-size/no-amplitude-copy guard.
- Geometry: Line2D, RegularGrid wrapper, `TraceAt`, `TraceLocation`, `Bounds`, regular/non-grid classification and confidence thresholds.
- Workspace: registration, initial Home, compatibility adoption, atomic failure restoration, close restoration, invalid ranges, generations/stale completion and no 3-D-to-2-D cross-call.
- Router: explicit/automatic routes, invalid data, failed target preservation and source-level assertions for Home/Recent/drop/“体”.
- Parameters/render: JSON v1, factory values, catalogs/options, default gain/clip/mode, RenderStats and pixel hash.
- Existing packages: license, export, SEG-Y decode/render, geometry cache and time-slice tests remain in `go test ./... -count=1`.
