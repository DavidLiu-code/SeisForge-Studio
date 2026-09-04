//go:build windows

package project

import (
	"os"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func TestProvidedSu36SurveyProject(t *testing.T) {
	root := os.Getenv("SEISFORGE_TEST_CROOKED_PROJECT_DIR")
	if root == "" {
		t.Skip("set SEISFORGE_TEST_CROOKED_PROJECT_DIR to run the external survey acceptance test")
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("SEISFORGE_TEST_CROOKED_PROJECT_DIR is unavailable: %v", err)
	}
	p, err := OpenFolder(dataset.NewManager(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Lines) != 62 || p.ValidLineCount() != 62 || p.NavigationLineCount() != 62 {
		t.Fatalf("provided project mismatch: lines=%d valid=%d navigation=%d", len(p.Lines), p.ValidLineCount(), p.NavigationLineCount())
	}
	wantNavigation := map[string]int{"00627-280": 7, "00641-280": 20, "97583-2177": 51, "98640_280": 4}
	calibrationLines := make(map[string]*CrookedProjectLine)
	for _, line := range p.Lines {
		if want, ok := wantNavigation[line.Name]; ok && len(line.Navigation) != want {
			t.Fatalf("%s navigation points=%d want=%d", line.Name, len(line.Navigation), want)
		}
		if line.Name == "00551-280" || line.Name == "92549_289" {
			calibrationLines[line.Name] = line
		}
	}
	for _, name := range []string{"00551-280", "92549_289"} {
		calibrationLine := calibrationLines[name]
		if calibrationLine == nil {
			t.Fatalf("%s was not matched", name)
		}
		reader, err := calibrationLine.Dataset.OpenReader()
		if err != nil {
			t.Fatal(err)
		}
		build, buildErr := geometry.BuildCrookedCachedProgress(reader, segy.TraceCoordinateSpec{Source: segy.CoordinateAuto, CDPByte: 21, ScalarByte: 71, UnitsByte: 89}, 4, nil)
		_ = reader.Close()
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		calibration := InferCoordinateMultiplier(build.Geometry, calibrationLine.Navigation)
		if !calibration.Accepted || calibration.Multiplier != 0.1 {
			t.Fatalf("%s coordinate calibration mismatch: %+v", name, calibration)
		}
	}
}
