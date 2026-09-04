//go:build windows

package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/workspace"
)

func TestPhase1DisplayParametersFrozen(t *testing.T) {
	if paletteIndex != 2 || clipPercent != 99 || agc || gainPercent != 0 || renderDisplayMode != segy.DisplayAdaptive || useLimits || limitMin != -1 || limitMax != 1 {
		t.Fatalf("2-D display defaults changed: palette=%d clip=%v agc=%v gain=%v mode=%v limits=%v [%v,%v]", paletteIndex, clipPercent, agc, gainPercent, renderDisplayMode, useLimits, limitMin, limitMax)
	}
	expectedNames := []string{
		"红灰青", "白灰黑", "黑灰白", "蓝灰红", "蓝灰褐1", "绿灰褐1", "蓝灰褐2", "绿灰褐2", "蓝灰橙", "绿灰橙",
		"绿灰褐3", "蓝灰褐3", "蓝黄褐", "绿灰红", "红黄蓝", "红蓝黄", "黄红绿", "绿红蓝", "灰黄橙", "灰绿黄",
	}
	if !reflect.DeepEqual(paletteNames, expectedNames) || len(legacyAnchors) != 20 {
		t.Fatalf("legacy palette catalog or order changed: %v", paletteNames)
	}
	if !reflect.DeepEqual(volumePaletteOrder, []int{1, 2, 0, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19}) {
		t.Fatalf("volume palette order changed: %v", volumePaletteOrder)
	}
	if !reflect.DeepEqual(volumeStyleComboOrder, []int{3, 0, 1, 2}) {
		t.Fatalf("volume style combo order changed: %v", volumeStyleComboOrder)
	}
	if !reflect.DeepEqual(volumeAxisFactorOptions, []float64{0.10, 0.15, 0.20, 0.25, 0.33, 0.40, 0.50, 0.67, 0.75, 0.85, 1.00, 1.10, 1.25, 1.50, 2.00, 2.50, 3.00, 4.00, 5.00, 6.00, 8.00}) {
		t.Fatalf("axis factor options changed: %v", volumeAxisFactorOptions)
	}
	if !reflect.DeepEqual(volumeFOVOptions, []float64{0, 5, 10, 15, 20, 25, 30, 35, 40, 45, 50, 55, 60, 70, 80, 90, 100, 110, 120}) {
		t.Fatalf("FOV options changed: %v", volumeFOVOptions)
	}
}

func TestPhase1VolumeViewJSONV1RoundTrip(t *testing.T) {
	want := volumeViewDefaults{
		Version: 1, ShowCoordinates: true, ViewSizeMode: 2, AspectMode: 2, StyleMode: 0, InteractionMode: 0,
		PaletteIndex: 2, AxisILFactor: 1, AxisXLFactor: 1, AxisZFactor: 1, FOV: 120,
		Azimuth: -57.52, Elevation: 24.44, Zoom: 1.2544, PanX: -70, PanY: 64,
		DefaultAspectIL: 1, DefaultAspectXL: 1, DefaultAspectZ: .55,
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got volumeViewDefaults
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("volume_view.json v1 did not round-trip:\nwant=%+v\n got=%+v", want, got)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	wantKeys := []string{"aspect_mode", "axis_il_factor", "axis_xl_factor", "axis_z_factor", "azimuth", "default_aspect_il", "default_aspect_xl", "default_aspect_z", "elevation", "fov", "interaction_mode", "palette_index", "pan_x", "pan_y", "show_coordinates", "style_mode", "version", "view_size_mode", "zoom"}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("volume_view.json v1 schema changed: %v", keys)
	}
}

func TestPhase1FactoryVolumeDefaultsFrozen(t *testing.T) {
	d := factoryVolumeViewDefaults()
	if d.Version != 1 || !d.ShowCoordinates || d.ViewSizeMode != 3 || d.AspectMode != 0 || d.StyleMode != 0 || d.InteractionMode != 0 || d.PaletteIndex != 2 ||
		d.AxisILFactor != 1 || d.AxisXLFactor != 1 || d.AxisZFactor != 1 || d.FOV != 35 || d.Azimuth != -145 || d.Elevation != 24 || d.Zoom != 1 || d.PanX != 0 || d.PanY != 0 ||
		d.DefaultAspectIL != 1 || d.DefaultAspectXL != 1 || d.DefaultAspectZ != .55 {
		t.Fatalf("factory 3-D defaults changed: %+v", d)
	}
}

func TestPhase1EntryPointsUseApplicationRouter(t *testing.T) {
	home := phase1FunctionSource(t, "workspace_windows.go", "openHomeFile")
	if !strings.Contains(home, "openApplicationPath") {
		t.Fatal("Home/Recent route does not call the Application router")
	}
	for _, forbidden := range []string{"loadFile(", "rerender(", "workspaceSyncAFromMain(", "completeWorkspaceOpen(", "showVolumeWindow("} {
		if strings.Contains(home, forbidden) {
			t.Fatalf("Home route references legacy display chain %q", forbidden)
		}
	}
	drop := phase1FunctionSource(t, "workspace_windows.go", "handleWorkspaceDropPaths")
	if !strings.Contains(drop, "openApplicationPath") {
		t.Fatal("initial drag/drop does not call the Application router")
	}
	activate := phase1FunctionSource(t, "workspace_windows.go", "activateStartHomeHit")
	if !strings.Contains(activate, "openHomeFile") {
		t.Fatal("Recent rows bypass the Home/Application route")
	}
	router := phase1FunctionSource(t, "app_phase1_windows.go", "openApplicationPath")
	if !strings.Contains(router, "application.OpenPath") {
		t.Fatal("Win32 router does not dispatch through Application.OpenPath")
	}
	volume := phase1FunctionSource(t, "volume_windows.go", "showVolumeWindow")
	if !strings.Contains(volume, "openCurrentDatasetInVolume") {
		t.Fatal("2-D volume button does not route the active Dataset to Workspace3D")
	}
}

func TestPhase1HomeCardAndRecentModeContract(t *testing.T) {
	if workspaceMode2D != 1 || workspaceMode3D != 2 || workspaceModeCrooked != 3 {
		t.Fatalf("persisted workspace modes changed: 2D=%d 3D=%d Crooked=%d", workspaceMode2D, workspaceMode3D, workspaceModeCrooked)
	}
	if phase1WorkspaceKind(workspaceMode2D) != workspace.Kind2D || phase1WorkspaceKind(workspaceMode3D) != workspace.Kind3D || phase1WorkspaceKind(workspaceModeCrooked) != workspace.KindCrooked {
		t.Fatalf("legacy-to-core workspace mapping is reversed: 2D=%v 3D=%v Crooked=%v", phase1WorkspaceKind(workspaceMode2D), phase1WorkspaceKind(workspaceMode3D), phase1WorkspaceKind(workspaceModeCrooked))
	}
	if phase1LegacyMode(workspace.Kind2D) != workspaceMode2D || phase1LegacyMode(workspace.Kind3D) != workspaceMode3D || phase1LegacyMode(workspace.KindCrooked) != workspaceModeCrooked {
		t.Fatal("core-to-legacy workspace mapping does not round-trip")
	}
}

func phase1FunctionSource(t *testing.T, filename, function string) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate regression test source")
	}
	path := filepath.Join(filepath.Dir(current), filename)
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Name.Name != function {
			continue
		}
		start := set.Position(fn.Pos()).Offset
		end := set.Position(fn.End()).Offset
		return string(content[start:end])
	}
	t.Fatalf("function %s not found in %s", function, filename)
	return ""
}
