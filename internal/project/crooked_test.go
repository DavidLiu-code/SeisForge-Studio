package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestParseNavigation(t *testing.T) {
	input := "# lineName sp trace x y min max\nline-A 1.5 20 100.25 200.5 10 30\nline-A 1.0 10 90 195 10 30\nline-B 2 1 4 5 1 2\n"
	lines, err := ParseNavigation(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || len(lines["line-A"]) != 2 || lines["line-A"][0].Trace != 10 || lines["line-A"][1].X != 100.25 {
		t.Fatalf("unexpected navigation: %+v", lines)
	}
	if _, err := ParseNavigation(strings.NewReader("bad row\n")); err == nil {
		t.Fatal("malformed navigation was accepted")
	}
}

func TestOpenFolderMatchesNavigationAndKeepsBrokenLine(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		t.Helper()
		if err := testsegy.Write(filepath.Join(dir, name), testsegy.Options{Rows: 1, Cols: 8, Samples: 4}); err != nil {
			t.Fatal(err)
		}
	}
	write("survey-line10.sgy")
	write("survey-line2.sgy")
	if err := os.WriteFile(filepath.Join(dir, "z-broken.sgy"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "archive.rar"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := testsegy.Write(filepath.Join(dir, "nested", "nested.sgy"), testsegy.Options{Rows: 1, Cols: 2}); err != nil {
		t.Fatal(err)
	}
	dat := "# header\nline2 0 1 100 200 1 8\nline2 1 8 107 200 1 8\nline10 0 1 300 400 1 8\nline10 1 8 307 400 1 8\n"
	if err := os.WriteFile(filepath.Join(dir, "survey.dat"), []byte(dat), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := OpenFolder(dataset.NewManager(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lines) != 3 || p.ValidLineCount() != 2 || p.NavigationLineCount() != 2 {
		t.Fatalf("unexpected project counts: lines=%d valid=%d nav=%d", len(p.Lines), p.ValidLineCount(), p.NavigationLineCount())
	}
	if p.Lines[0].Name != "line2" || p.Lines[1].Name != "line10" {
		t.Fatalf("natural order or DAT matching failed: %q, %q", p.Lines[0].Name, p.Lines[1].Name)
	}
	if p.Lines[2].OpenError == "" {
		t.Fatal("broken SEG-Y was not retained as an error line")
	}
}

func TestOpenFolderRejectsAllInvalid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.sgy"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFolder(dataset.NewManager(), dir); err == nil {
		t.Fatal("all-invalid project was accepted")
	}
}

func TestCoordinateCalibrationAndScaledCopy(t *testing.T) {
	values := []segy.TraceCoordinate{
		{Trace: 0, CDP: 110, HasCDP: true, X: 1100, Y: 2000, Valid: true},
		{Trace: 1, CDP: 150, HasCDP: true, X: 1500, Y: 2000, Valid: true},
		{Trace: 2, CDP: 190, HasCDP: true, X: 1900, Y: 2000, Valid: true},
	}
	g, err := geometry.NewCrookedLine(values, segy.TraceCoordinateSpec{Source: segy.CoordinateEnsemble, XByte: 181, YByte: 185, CDPByte: 21, ScalarByte: 71, UnitsByte: 89})
	if err != nil {
		t.Fatal(err)
	}
	// The header trajectory is ten times larger and uses a different local
	// origin.  Shape determines the decimal multiplier; the median offset then
	// aligns the two coordinate systems.
	nav := []NavigationPoint{{Trace: 100, X: 600, Y: 900}, {Trace: 200, X: 700, Y: 900}}
	calibration := InferCoordinateMultiplier(g, nav)
	if !calibration.Accepted || calibration.Multiplier != 0.1 || calibration.OffsetX != 500 || calibration.OffsetY != 700 || calibration.Matches != 2 || calibration.Inliers != 2 || calibration.FailureReason != "" {
		t.Fatalf("unexpected calibration: %+v", calibration)
	}
	transformed, err := TransformGeometry(g, calibration)
	if err != nil {
		t.Fatal(err)
	}
	if transformed.X[0] != 610 || transformed.Y[0] != 900 || transformed.TotalDistance != 80 {
		t.Fatalf("transformed geometry mismatch: X=%v Y=%v distance=%v", transformed.X, transformed.Y, transformed.TotalDistance)
	}

	// The legacy helper remains a pure scale for old callers.
	scaled, err := ScaleGeometry(g, calibration.Multiplier)
	if err != nil {
		t.Fatal(err)
	}
	if scaled.X[0] != 110 || scaled.Y[0] != 200 || scaled.TotalDistance != 80 {
		t.Fatalf("scaled geometry mismatch: X=%v Y=%v distance=%v", scaled.X, scaled.Y, scaled.TotalDistance)
	}
	if g.X[0] != 1100 {
		t.Fatal("calibration mutated cached geometry")
	}
}

func TestCoordinateCalibrationIgnoresNavigationOutlier(t *testing.T) {
	values := []segy.TraceCoordinate{
		{Trace: 0, CDP: 100, HasCDP: true, X: 1000, Y: 2000, Valid: true},
		{Trace: 1, CDP: 125, HasCDP: true, X: 1250, Y: 2050, Valid: true},
		{Trace: 2, CDP: 150, HasCDP: true, X: 1500, Y: 2100, Valid: true},
		{Trace: 3, CDP: 175, HasCDP: true, X: 1750, Y: 2150, Valid: true},
		{Trace: 4, CDP: 200, HasCDP: true, X: 2000, Y: 2200, Valid: true},
	}
	g, err := geometry.NewCrookedLine(values, segy.TraceCoordinateSpec{Source: segy.CoordinateEnsemble, XByte: 181, YByte: 185, CDPByte: 21, ScalarByte: 71, UnitsByte: 89})
	if err != nil {
		t.Fatal(err)
	}
	nav := []NavigationPoint{
		{Trace: 100, X: 600, Y: 900},
		{Trace: 125, X: 625, Y: 905},
		// One bad navigation pick must not move the fitted scale or origin.
		{Trace: 150, X: 1150, Y: 510},
		{Trace: 175, X: 675, Y: 915},
		{Trace: 200, X: 700, Y: 920},
	}
	calibration := InferCoordinateCalibration(g, nav)
	if !calibration.Accepted || calibration.Multiplier != 0.1 || calibration.OffsetX != 500 || calibration.OffsetY != 700 || calibration.Matches != 5 || calibration.Inliers != 4 {
		t.Fatalf("outlier changed robust calibration: %+v", calibration)
	}
	if g.X[0] != 1000 || g.Y[0] != 2000 {
		t.Fatal("calibration mutated raw header geometry")
	}
}

func TestCalibrationPairsMatchEveryNavigationPointAcrossReverseNonMonotonicCDP(t *testing.T) {
	// Trace order deliberately starts and ends on interior CDPs and folds back
	// twice.  The old endpoint shortcut selected CDP 175 and 125 as the DAT
	// endpoints merely because they were the first and last geometry entries.
	cdps := []int32{175, 200, 150, 100, 125}
	values := make([]segy.TraceCoordinate, 0, len(cdps))
	for trace, cdp := range cdps {
		values = append(values, segy.TraceCoordinate{
			Trace:  int64(trace),
			CDP:    cdp,
			HasCDP: true,
			X:      float64(cdp) * 10,
			Y:      2000 + float64(cdp-100)*2,
			Valid:  true,
		})
	}
	g, err := geometry.NewCrookedLine(values, segy.TraceCoordinateSpec{Source: segy.CoordinateEnsemble, XByte: 181, YByte: 185, CDPByte: 21, ScalarByte: 71, UnitsByte: 89})
	if err != nil {
		t.Fatal(err)
	}
	rawX := append([]float64(nil), g.X...)
	rawY := append([]float64(nil), g.Y...)
	nav := []NavigationPoint{
		{Trace: 100, X: 600, Y: 900},
		{Trace: 150, X: 650, Y: 910},
		{Trace: 200, X: 700, Y: 920},
	}

	pairs := calibrationPairs(g, nav)
	if len(pairs) != len(nav) {
		t.Fatalf("matched pairs=%d want=%d: %+v", len(pairs), len(nav), pairs)
	}
	for i, wantHeaderX := range []float64{1000, 1500, 2000} {
		if pairs[i].headerX != wantHeaderX {
			t.Fatalf("pair %d matched header X=%v want=%v (pairs=%+v)", i, pairs[i].headerX, wantHeaderX, pairs)
		}
	}

	calibration := InferCoordinateCalibration(g, nav)
	if !calibration.Accepted || calibration.Multiplier != 0.1 || calibration.OffsetX != 500 || calibration.OffsetY != 700 || calibration.Matches != 3 || calibration.Inliers != 3 {
		t.Fatalf("reverse/non-monotonic calibration mismatch: %+v", calibration)
	}
	transformed, err := TransformGeometry(g, calibration)
	if err != nil {
		t.Fatal(err)
	}
	for i, cdp := range cdps {
		wantX := 500 + float64(cdp)
		wantY := 900 + float64(cdp-100)*0.2
		if transformed.X[i] != wantX || transformed.Y[i] != wantY {
			t.Fatalf("transformed point %d=(%v,%v) want=(%v,%v)", i, transformed.X[i], transformed.Y[i], wantX, wantY)
		}
		if g.X[i] != rawX[i] || g.Y[i] != rawY[i] {
			t.Fatalf("calibration mutated raw geometry at %d: got=(%v,%v) want=(%v,%v)", i, g.X[i], g.Y[i], rawX[i], rawY[i])
		}
	}
}

func TestOpenPathsDeduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "line.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 4}); err != nil {
		t.Fatal(err)
	}
	p, err := OpenPaths(dataset.NewManager(), []string{path, path})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lines) != 1 {
		t.Fatalf("duplicate paths retained: %d", len(p.Lines))
	}
}
