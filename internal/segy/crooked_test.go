package segy

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestTraceCoordinatesApplyScalarAndCustomBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordinates.sgy")
	coordinates := []testsegy.TraceCoordinate{{X: 1000, Y: 2000, CDP: 41}, {X: 1100, Y: 2200, CDP: 42}}
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 2, Samples: 2, CoordinateXByte: 73, CoordinateYByte: 77, CoordinateScalar: -10, CoordinateUnits: 1, Coordinates: coordinates}); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	spec := TraceCoordinateSpec{Source: CoordinateCustom, XByte: 73, YByte: 77, CDPByte: 21, ScalarByte: 71, UnitsByte: 89}
	location, err := file.ReadTraceCoordinate(0, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !location.Valid || !location.HasCDP || location.X != 100 || location.Y != 200 || location.CDP != 41 || location.Units != 1 {
		t.Fatalf("unexpected scaled coordinate: %+v", location)
	}
	values, err := file.ScanTraceCoordinates(spec, 2, nil)
	if err != nil || len(values) != 2 || values[1].X != 110 || values[1].Y != 220 {
		t.Fatalf("unexpected coordinate scan: %+v err=%v", values, err)
	}
}

func TestParallelCoordinateDetectionMatchesLegacyCandidateOrderAndValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "parallel-detection.sgy")
	coordinates := make([]testsegy.TraceCoordinate, 96)
	for i := range coordinates {
		coordinates[i] = testsegy.TraceCoordinate{X: int32(12000 + i*17), Y: int32(33000 + i*i), CDP: int32(700 + i)}
	}
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: len(coordinates), Samples: 4, CoordinateXByte: 73, CoordinateYByte: 77, Coordinates: coordinates}); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	const sampled = 41
	got, err := file.DetectCoordinateSpec(sampled)
	if err != nil {
		t.Fatal(err)
	}
	want := CoordinateDetectResult{Sampled: sampled}
	for _, spec := range DefaultCoordinateSpecs() {
		values := make([]TraceCoordinate, 0, sampled)
		for i := 0; i < sampled; i++ {
			trace := int64(i) * (file.Info.TraceCount - 1) / int64(sampled-1)
			value, err := file.ReadTraceCoordinate(trace, spec)
			if err != nil {
				t.Fatal(err)
			}
			values = append(values, value)
		}
		want.Candidates = append(want.Candidates, coordinateCandidateStats(spec, values))
	}
	best := -1
	for i, candidate := range want.Candidates {
		if candidate.UniqueCount >= 2 && candidate.ValidRatio > 0 && (best < 0 || candidate.Score > want.Candidates[best].Score+1e-9) {
			best = i
		}
	}
	if best < 0 {
		t.Fatal("legacy reference did not find a candidate")
	}
	want.Spec, want.Score, want.ValidRatio, want.UniqueCount = want.Candidates[best].Spec, want.Candidates[best].Score, want.Candidates[best].ValidRatio, want.Candidates[best].UniqueCount
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parallel coordinate detection changed results:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestCoordinateScalarRulesAndAngularUnitsRemainRaw(t *testing.T) {
	for _, test := range []struct {
		name     string
		scalar   int16
		units    int16
		expected float64
	}{
		{"positive", 10, 1, 1000},
		{"zero", 0, 1, 100},
		{"negative", -10, 2, 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.name+".sgy")
			if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 1, Samples: 1, CoordinateScalar: test.scalar, CoordinateUnits: test.units,
				Coordinates: []testsegy.TraceCoordinate{{X: 100, Y: 200, CDP: 1}}}); err != nil {
				t.Fatal(err)
			}
			file, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			location, err := file.ReadTraceCoordinate(0, DefaultCoordinateSpec())
			_ = file.Close()
			if err != nil || location.X != test.expected || location.Units != test.units {
				t.Fatalf("unexpected scalar/unit result: %+v err=%v", location, err)
			}
		})
	}
}

func TestDetectCoordinateSpecFallsBackToSourceXY(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source-xy.sgy")
	coordinates := make([]testsegy.TraceCoordinate, 24)
	for i := range coordinates {
		coordinates[i] = testsegy.TraceCoordinate{X: int32(1000 + i*10), Y: int32(2000 + i*i), CDP: int32(500 + i)}
	}
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: len(coordinates), Samples: 1, CoordinateXByte: 73, CoordinateYByte: 77, Coordinates: coordinates}); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	detection, err := file.DetectCoordinateSpec(3000)
	if err != nil {
		t.Fatal(err)
	}
	if detection.Spec.Source != CoordinateSourceXY || detection.Spec.XByte != 73 || detection.Spec.YByte != 77 || detection.ValidRatio != 1 {
		t.Fatalf("unexpected coordinate detection: %+v", detection)
	}
}

func TestRenderTracePositionsAcceptsIrregularSpacing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "irregular-render.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 3, Samples: 8}); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	pixels, stats, err := file.RenderTracePositions([]int64{0, 1, 2}, []float64{0, 1, 10}, 0, 10, RenderOptions{Width: 21, Height: 16, ClipPercent: 99, SampleStart: 0, SampleEnd: 7, DisplayMode: DisplayAdaptive})
	if err != nil {
		t.Fatal(err)
	}
	if len(pixels) != 21*16 || stats.TraceStart != 0 || stats.TraceEnd != 2 || stats.SampleEnd != 7 {
		t.Fatalf("unexpected positioned render: pixels=%d stats=%+v", len(pixels), stats)
	}
}
