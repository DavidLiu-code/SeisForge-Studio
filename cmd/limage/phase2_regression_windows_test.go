//go:build windows

package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func TestPhase2CrookedDefaultsAndJSONV1(t *testing.T) {
	defaults := defaultCrookedView()
	if defaults.Version != 1 || defaults.AxisMode != crookedAxisDistance || defaults.PaletteIndex != 2 || defaults.GainPercent != 0 || defaults.Spec.Source != segy.CoordinateAuto || defaults.Spec.CDPByte != 21 || defaults.Spec.ScalarByte != 71 || defaults.Spec.UnitsByte != 89 {
		t.Fatalf("unexpected crooked defaults: %+v", defaults)
	}
	data, err := json.Marshal(defaults)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip crookedViewDefaults
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defaults, roundTrip) {
		t.Fatalf("crooked_view.json v1 did not round-trip: want=%+v got=%+v", defaults, roundTrip)
	}
}

func TestPhase2CrookedCDPAxisPreservesAcquisitionOrder(t *testing.T) {
	geometry, err := geometrycore.NewCrookedLine([]segy.TraceCoordinate{
		{Trace: 0, CDP: 30, X: 1, Y: 1, Valid: true, HasCDP: true},
		{Trace: 1, CDP: 20, X: 2, Y: 3, Valid: true, HasCDP: true},
		{Trace: 2, CDP: 10, X: 4, Y: 6, Valid: true, HasCDP: true},
	}, segy.DefaultCoordinateSpec())
	if err != nil {
		t.Fatal(err)
	}
	positions, usable := crookedAxisPositions(geometry, crookedAxisCDP)
	if !usable || !reflect.DeepEqual(positions, []float64{-30, -20, -10}) || !reflect.DeepEqual(geometry.TraceIndices, []int64{0, 1, 2}) {
		t.Fatalf("descending CDP changed trace order: positions=%v traces=%v usable=%v", positions, geometry.TraceIndices, usable)
	}
	geometry.CDP[1] = 40
	positions, usable = crookedAxisPositions(geometry, crookedAxisCDP)
	if usable || !reflect.DeepEqual(positions, []float64{0, 1, 2}) {
		t.Fatalf("non-monotonic CDP did not fall back to acquisition order: %v usable=%v", positions, usable)
	}
}

func TestPhase2CrookedUsesDedicatedWorkspace(t *testing.T) {
	initialization := phase1FunctionSource(t, "app_phase1_windows.go", "initializePhase1Application")
	if !strings.Contains(initialization, "&crookedWorkspaceAdapter{}") || strings.Contains(initialization, "lineWorkspaceAdapter{kind: workspacecore.KindCrooked}") {
		t.Fatalf("Crooked is not registered with its dedicated adapter: %s", initialization)
	}
	open := phase1FunctionSource(t, "crooked_windows.go", "startCrookedPrepare")
	if strings.Contains(open, "loadSelectedFile") || strings.Contains(open, "rerender") || strings.Contains(open, "workspaceSyncAFromMain") {
		t.Fatal("Crooked preparation re-entered the legacy 2-D rendering chain")
	}
}

func TestPhase2VolumeIndexProgressContract(t *testing.T) {
	if WM_VOLUME_INDEX_PROGRESS != WM_USER+304 || WM_VOLUME_EXPORT_PROGRESS != WM_USER+303 {
		t.Fatalf("volume progress messages collide: index=%d export=%d", WM_VOLUME_INDEX_PROGRESS, WM_VOLUME_EXPORT_PROGRESS)
	}
	prepare := phase1FunctionSource(t, "volume_windows.go", "startVolumePrepareForSide")
	if !strings.Contains(prepare, "BuildGeometryIndexCachedProgress") || !strings.Contains(prepare, "postVolumeIndexProgress") {
		t.Fatal("Volume preparation does not expose geometry index progress")
	}
}
