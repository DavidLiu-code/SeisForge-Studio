//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestPerformPseudoLoadBuildsCalibratedIndexedCurtainAndClosesReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calibrated-curtain.sgy")
	coordinates := make([]testsegy.TraceCoordinate, 24)
	navigation := make([]projectcore.NavigationPoint, 24)
	for i := range coordinates {
		coordinates[i] = testsegy.TraceCoordinate{X: int32(10000 + i*100), Y: int32(20000 + i*i*20), CDP: int32(500 + i)}
		navigation[i] = projectcore.NavigationPoint{Trace: int64(500 + i), X: float64(coordinates[i].X) * .1, Y: float64(coordinates[i].Y) * .1}
	}
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: len(coordinates), Samples: 32, CoordinateScalar: 0, Coordinates: coordinates}); err != nil {
		t.Fatal(err)
	}
	data, err := dataset.NewManager().Open(path)
	if err != nil {
		t.Fatal(err)
	}
	line := &projectcore.CrookedProjectLine{ID: path, Name: "test-line", Path: path, Dataset: data, Navigation: navigation}
	result := performPseudoLoad(pseudoLoadJob{windowGen: 1, lineIndex: 0, token: 1, line: line,
		spec: segy.TraceCoordinateSpec{Source: segy.CoordinateAuto, CDPByte: 21, ScalarByte: 71, UnitsByte: 89},
		size: pseudo3dcore.TextureSize{Width: 64, Height: 48}, gain: 0, clip: 99, displayMode: segy.DisplayAdaptive})
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.multiplier != .1 || result.geometry == nil || result.texture == nil || len(result.texture.Indices) != 64*48 || len(result.points) < 2 {
		t.Fatalf("unexpected pseudo curtain result: multiplier=%g geometry=%v texture=%v pixels=%d points=%d", result.multiplier, result.geometry != nil, result.texture != nil, len(result.texture.Indices), len(result.points))
	}
	if result.geometry.X[0] != 1000 || result.geometry.Y[0] != 2000 {
		t.Fatalf("coordinate calibration was not applied: X=%g Y=%g", result.geometry.X[0], result.geometry.Y[0])
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("pseudo load retained an open SEG-Y reader: %v", err)
	}
}

func TestPseudoNavigationTrajectoryUsesTrueXYEndpoints(t *testing.T) {
	points, u := pseudoNavigationTrajectory([]projectcore.NavigationPoint{{X: 10, Y: 20}, {X: 20, Y: 25}, {X: 40, Y: 60}})
	if len(points) != 3 || len(u) != 3 || points[0].X != 10 || points[0].Y != 20 || points[2].X != 40 || points[2].Y != 60 || u[0] != 0 || u[2] != 1 {
		t.Fatalf("unexpected navigation trajectory: points=%+v u=%+v", points, u)
	}
}

func TestPseudoSelectionDropsTextureAndCanBeReenabled(t *testing.T) {
	previousState, previousHwnd := pseudoState, pseudoHwnd
	defer func() { pseudoState, pseudoHwnd = previousState, previousHwnd }()
	pseudoHwnd = 0
	pseudoState = pseudoSession{lines: []pseudoLineState{{
		line:     &projectcore.CrookedProjectLine{ID: "line", Name: "line", Dataset: &dataset.SeismicDataset{}},
		selected: true, ready: true, texture: &pseudo3dcore.Texture{Width: 2, Height: 2, Indices: []byte{0, 1, 2, 3}},
	}}}
	applyPseudoSelection(0, false, false)
	if pseudoState.lines[0].selected || pseudoState.lines[0].texture != nil || pseudoState.lines[0].ready {
		t.Fatalf("unchecking did not release retained display data: %+v", pseudoState.lines[0])
	}
	applyPseudoSelection(0, true, false)
	if !pseudoState.lines[0].selected {
		t.Fatal("line could not be selected again")
	}
}

func TestPerformPseudoLoadClipsTextureGeometryToSpatialRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "range-curtain.sgy")
	coordinates := make([]testsegy.TraceCoordinate, 20)
	for i := range coordinates {
		coordinates[i] = testsegy.TraceCoordinate{X: int32(i), Y: 5, CDP: int32(100 + i)}
	}
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: len(coordinates), Samples: 24, CoordinateScalar: 1, Coordinates: coordinates}); err != nil {
		t.Fatal(err)
	}
	data, err := dataset.NewManager().Open(path)
	if err != nil {
		t.Fatal(err)
	}
	line := &projectcore.CrookedProjectLine{ID: path, Name: "range-line", Path: path, Dataset: data}
	selected := pseudo3dcore.XYRange{XMin: 5, XMax: 12, YMin: 0, YMax: 10, Valid: true}
	result := performPseudoLoad(pseudoLoadJob{windowGen: 1, lineIndex: 0, token: 1, line: line,
		spec: segy.TraceCoordinateSpec{Source: segy.CoordinateAuto, CDPByte: 21, ScalarByte: 71, UnitsByte: 89},
		size: pseudo3dcore.TextureSize{Width: 48, Height: 32}, gain: 0, clip: 99, displayMode: segy.DisplayAdaptive, spatialRange: selected})
	if result.err != nil || result.outOfRange || len(result.segments) != 1 {
		t.Fatalf("unexpected clipped result: segments=%d outside=%v err=%v", len(result.segments), result.outOfRange, result.err)
	}
	for _, point := range result.segments[0].points {
		if !selected.Contains(point) {
			t.Fatalf("curtain point escaped selected range: %+v", point)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("clipped pseudo load retained an open Reader: %v", err)
	}
}
