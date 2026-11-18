//go:build windows

package main

import (
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func pseudoCanonicalCalibrationFixture(t *testing.T, name string, calibration projectcore.CoordinateCalibration) (*projectcore.CrookedProjectLine, *geometrycore.CrookedLineGeometry) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".sgy")
	coordinates := make([]testsegy.TraceCoordinate, 18)
	values := make([]segy.TraceCoordinate, len(coordinates))
	for i := range coordinates {
		x := int32(10000 + i*120)
		y := int32(30000 + i*i*17)
		coordinates[i] = testsegy.TraceCoordinate{X: x, Y: y, CDP: int32(700 + i)}
		values[i] = segy.TraceCoordinate{Trace: int64(i), X: float64(x), Y: float64(y), CDP: int32(700 + i), HasCDP: true, Valid: true}
	}
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: len(coordinates), Samples: 28, CoordinateScalar: 1, Coordinates: coordinates}); err != nil {
		t.Fatal(err)
	}
	data, err := dataset.NewManager().Open(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := geometrycore.NewCrookedLine(values, segy.DefaultCoordinateSpec())
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := projectcore.TransformGeometry(raw, calibration)
	if err != nil {
		t.Fatal(err)
	}
	line := &projectcore.CrookedProjectLine{ID: path, Name: name, Path: path, Dataset: data}
	return line, canonical
}

func TestPseudoCanonicalMixedScaleTranslationNeverUsesProjectMultiplier(t *testing.T) {
	calibrations := []projectcore.CoordinateCalibration{
		{Multiplier: .1, OffsetX: 420000, OffsetY: -17000, Accepted: true},
		{Multiplier: 1, OffsetX: -325000, OffsetY: 81000, Accepted: true},
	}
	states := make([]pseudoLineState, 0, len(calibrations))
	for i, calibration := range calibrations {
		line, canonical := pseudoCanonicalCalibrationFixture(t, string(rune('a'+i)), calibration)
		fingerprint := crookedGeometryFingerprint(line.ID, canonical)
		job := pseudoLoadJob{
			windowGen: 1, lineIndex: i, token: 1, line: line, geometry: canonical,
			geometryCalibrated: true, geometryFingerprint: fingerprint, existingMultiplier: calibration.Multiplier,
			// Deliberately incompatible: applying this legacy project scale to
			// either canonical line would make the regression immediately visible.
			projectMultiplier: 100, spec: segy.DefaultCoordinateSpec(),
			size: pseudo3dcore.TextureSize{Width: 48, Height: 32}, clip: 99,
			displayMode: segy.DisplayAdaptive, skipPersistentCache: true,
		}
		result := performPseudoLoad(job)
		if result.err != nil {
			t.Fatal(result.err)
		}
		if !result.geometryCalibrated || result.geometryFingerprint != fingerprint {
			t.Fatalf("canonical identity was lost: calibrated=%v fingerprint=%q want=%q", result.geometryCalibrated, result.geometryFingerprint, fingerprint)
		}
		if !reflect.DeepEqual(result.geometry.X, canonical.X) || !reflect.DeepEqual(result.geometry.Y, canonical.Y) || !reflect.DeepEqual(result.geometry.TraceIndices, canonical.TraceIndices) {
			t.Fatalf("canonical geometry %d was rescaled by project multiplier", i)
		}
		states = append(states, pseudoLineState{line: line, geometry: result.geometry, geometryCalibrated: true,
			geometryFingerprint: fingerprint, multiplier: calibration.Multiplier, ready: true})
	}

	previous := pseudoState
	defer func() { pseudoState = previous }()
	pseudoState = pseudoSession{lines: states, projectMultiplier: 100}
	before := make([][2][]float64, len(states))
	for i := range states {
		before[i] = [2][]float64{append([]float64(nil), states[i].geometry.X...), append([]float64(nil), states[i].geometry.Y...)}
	}
	applyPseudoProjectMultiplierToReadyLines(100)
	for i := range pseudoState.lines {
		state := &pseudoState.lines[i]
		if !state.ready || !state.geometryCalibrated || state.multiplier != calibrations[i].Multiplier {
			t.Fatalf("canonical state %d was invalidated by legacy normalization: %+v", i, state)
		}
		if !reflect.DeepEqual(state.geometry.X, before[i][0]) || !reflect.DeepEqual(state.geometry.Y, before[i][1]) {
			t.Fatalf("canonical state %d moved after project multiplier propagation", i)
		}
	}
}

func TestPseudoPersistentCacheIdentityIncludesFinalCanonicalGeometry(t *testing.T) {
	line, canonical := pseudoCanonicalCalibrationFixture(t, "cache-canonical", projectcore.CoordinateCalibration{
		Multiplier: .1, OffsetX: 400000, OffsetY: -20000, Accepted: true,
	})
	base := pseudoLoadJob{line: line, geometry: canonical, geometryCalibrated: true,
		geometryFingerprint: crookedGeometryFingerprint(line.ID, canonical), spec: segy.DefaultCoordinateSpec(),
		size: pseudo3dcore.TextureSize{Width: 32, Height: 24}, clip: 99, displayMode: segy.DisplayAdaptive}
	first, ok := pseudoPersistentCacheKey(base)
	if !ok || first.CanonicalGeometryFingerprint == "" {
		t.Fatal("canonical cache key omitted final geometry fingerprint")
	}

	shiftedCalibration := projectcore.CoordinateCalibration{Multiplier: 1, OffsetX: 17, OffsetY: -31, Accepted: true}
	shifted, err := projectcore.TransformGeometry(canonical, shiftedCalibration)
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.geometry = shifted
	changed.geometryFingerprint = crookedGeometryFingerprint(line.ID, shifted)
	second, ok := pseudoPersistentCacheKey(changed)
	if !ok {
		t.Fatal("shifted canonical geometry did not produce a cache key")
	}
	firstDigest, _ := first.Digest()
	secondDigest, _ := second.Digest()
	if firstDigest == secondDigest || first.CanonicalGeometryFingerprint == second.CanonicalGeometryFingerprint {
		t.Fatal("canonical scale/translation change reused the old persistent cache identity")
	}
	if math.IsNaN(shifted.X[0]) || math.IsNaN(shifted.Y[0]) {
		t.Fatal("invalid shifted geometry fixture")
	}
}

func TestPseudoProjectExcludesFailedCanonicalLineWithoutDATFallback(t *testing.T) {
	readyLine, readyGeometry := pseudoCanonicalCalibrationFixture(t, "ready-canonical", projectcore.CoordinateCalibration{
		Multiplier: .1, OffsetX: 420000, OffsetY: -17000, Accepted: true,
	})
	failedLine, failedRaw := pseudoCanonicalCalibrationFixture(t, "failed-canonical", projectcore.CoordinateCalibration{
		Multiplier: 1, Accepted: true,
	})
	// If the old fallback is reached these remote DAT coordinates create a
	// selectable curtain and dramatically expand the project bounds.
	failedLine.Navigation = []projectcore.NavigationPoint{
		{Trace: 700, X: -9e8, Y: 8e8},
		{Trace: 717, X: -8e8, Y: 9e8},
	}
	project := &projectcore.CrookedProject{Root: "canonical-project", Lines: []*projectcore.CrookedProjectLine{readyLine, failedLine}}
	readyState := crookedProjectLineState{line: readyLine, geometry: readyGeometry, rawGeometry: readyGeometry, ready: true, multiplier: .1,
		calibration: projectcore.CoordinateCalibration{Multiplier: .1, OffsetX: 420000, OffsetY: -17000, Accepted: true}}
	readyState.fingerprint = crookedGeometryFingerprint(readyLine.ID, readyGeometry)
	readyState.mapPath = canonicalCrookedMapPath(&readyState, crookedCanonicalMapPointLimit)
	failedState := crookedProjectLineState{line: failedLine, rawGeometry: failedRaw, ready: false,
		errorText: "DAT 与 SEG-Y 道头坐标校准失败", calibration: projectcore.CoordinateCalibration{Multiplier: 1, FailureReason: "coordinate mismatch"}}

	previousPseudo, previousCrooked, previousHwnd := pseudoState, crookedState, pseudoHwnd
	previousRangeGen := atomic.LoadInt64(&pseudoActiveRangeGen)
	previousTimeGen := atomic.LoadInt64(&pseudoActiveTimeGen)
	defer func() {
		pseudoState, crookedState, pseudoHwnd = previousPseudo, previousCrooked, previousHwnd
		atomic.StoreInt64(&pseudoActiveRangeGen, previousRangeGen)
		atomic.StoreInt64(&pseudoActiveTimeGen, previousTimeGen)
	}()
	atomic.StoreInt64(&pseudoActiveRangeGen, 0)
	atomic.StoreInt64(&pseudoActiveTimeGen, 0)
	pseudoHwnd = 0
	crookedState = crookedSession{project: project, projectLines: []crookedProjectLineState{readyState, failedState},
		activeLine: 0, projectGeometryReady: true}
	pseudoState = pseudoSession{project: project, activeLine: 0, restoreSelections: map[string]bool{},
		spatialRange: pseudo3dcore.XYRange{}, timeRange: pseudo3dcore.TimeRange{}}
	initializePseudoLines()

	if len(pseudoState.lines) != 2 || !pseudoState.lines[0].selected || !pseudoState.lines[0].geometryCalibrated {
		t.Fatalf("ready canonical line was not selected: %+v", pseudoState.lines)
	}
	failed := &pseudoState.lines[1]
	if failed.selected || pseudoLineSelectable(failed) || !failed.canonicalUnavailable || failed.geometry != nil || len(failed.points) != 0 || failed.size.Width != 0 || failed.size.Height != 0 {
		t.Fatalf("failed canonical line entered pseudo workflow: %+v", failed)
	}
	if !strings.Contains(failed.errorText, "校准失败") || pseudoLineStatus(failed) != "规范几何错误" {
		t.Fatalf("failed canonical diagnostic was not retained: status=%q error=%q", pseudoLineStatus(failed), failed.errorText)
	}
	if _, ok := pseudoPersistentCacheKey(pseudoLoadJob{line: failedLine, canonicalUnavailable: true,
		size: pseudo3dcore.TextureSize{Width: 32, Height: 24}}); ok {
		t.Fatal("failed canonical line produced a persistent cache identity")
	}
	scene, refs := currentPseudoScene()
	if len(scene.Curtains) != 1 || len(refs) != 1 || scene.Curtains[0].ID != readyLine.ID {
		t.Fatalf("failed canonical line entered pseudo scene: curtains=%+v refs=%+v", scene.Curtains, refs)
	}
	if candidates := crookedPseudoRangeCandidateCount(); candidates != 1 {
		t.Fatalf("failed canonical line was counted as a range candidate: %d", candidates)
	}
	if bounds := crookedProjectBounds(); !bounds.HasXY || bounds.XMin < -1e8 || bounds.YMax > 1e8 {
		t.Fatalf("failed DAT fallback polluted canonical project bounds: %+v", bounds)
	}

	// Range changes must not reactivate the failed line through its DAT path.
	updatePseudoRangeInPlace(pseudo3dcore.XYRange{XMin: -1e9, XMax: 1e9, YMin: -1e9, YMax: 1e9, Valid: true}, 1)
	failed = &pseudoState.lines[1]
	if failed.selected || failed.inRange || len(failed.points) != 0 || pseudoLineSelectable(failed) {
		t.Fatalf("range update reactivated failed canonical DAT geometry: %+v", failed)
	}
}
