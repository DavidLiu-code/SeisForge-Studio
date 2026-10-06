//go:build windows

package main

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func crookedGeometryFixFixture(t *testing.T, lineID string, x, y []float64) (*projectcore.CrookedProjectLine, *geometrycore.CrookedLineGeometry) {
	t.Helper()
	if len(x) != len(y) || len(x) < 2 {
		t.Fatal("invalid crooked geometry fixture")
	}
	values := make([]segy.TraceCoordinate, len(x))
	for i := range values {
		values[i] = segy.TraceCoordinate{Trace: int64(10 + 3*i), CDP: int32(100 + 10*i), HasCDP: true, X: x[i], Y: y[i], Valid: true}
	}
	geometry, err := geometrycore.NewCrookedLine(values, segy.DefaultCoordinateSpec())
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately incompatible DAT coordinates make accidental fallback to
	// Navigation immediately visible in the assertions below.
	line := &projectcore.CrookedProjectLine{ID: lineID, Name: lineID, Navigation: []projectcore.NavigationPoint{
		{Trace: 100, X: 900000, Y: 800000},
		{Trace: 200, X: 900100, Y: 800100},
	}}
	return line, geometry
}

func TestCrookedGeometryFixCanonicalPathUsesOnlyCalibratedHeaderGeometry(t *testing.T) {
	line, geometry := crookedGeometryFixFixture(t, "line-canonical", []float64{0, 3, -2, 1, .5}, []float64{0, 1, 4, 2, 1.5})
	state := crookedProjectLineState{line: line, geometry: geometry}
	path := canonicalCrookedMapPath(&state, len(geometry.X))
	if path.LineID != line.ID || path.Fingerprint == "" || len(path.Points) != len(geometry.X) {
		t.Fatalf("canonical path identity mismatch: %+v", path)
	}
	for i, point := range path.Points {
		if point.X != geometry.X[i] || point.Y != geometry.Y[i] || point.TraceIndex != geometry.TraceIndices[i] || point.Position != i {
			t.Fatalf("canonical point %d did not preserve header geometry/trace mapping: %+v", i, point)
		}
		if point.X >= 800000 || point.Y >= 700000 {
			t.Fatalf("DAT navigation leaked into canonical path: %+v", point)
		}
	}

	sampled := canonicalCrookedMapPath(&state, 3)
	if len(sampled.Points) < 3 || sampled.Points[0].Position != 0 || sampled.Points[len(sampled.Points)-1].Position != len(geometry.X)-1 {
		t.Fatalf("sampled path lost acquisition endpoints: %+v", sampled.Points)
	}
	for _, point := range sampled.Points {
		if point.Position < 0 || point.Position >= len(geometry.X) || point.TraceIndex != geometry.TraceIndices[point.Position] || point.X != geometry.X[point.Position] || point.Y != geometry.Y[point.Position] {
			t.Fatalf("sampled path lost original trace position: %+v", point)
		}
	}
	wantBounds := geometry.Bounds()
	if got := crookedMapPathBounds(sampled); !reflect.DeepEqual(got, wantBounds) {
		t.Fatalf("sampled canonical bounds changed: got=%+v want=%+v", got, wantBounds)
	}
}

func TestCrookedGeometryFixSelectionCannotChangeProjectBounds(t *testing.T) {
	lineA, geometryA := crookedGeometryFixFixture(t, "line-a", []float64{0, 2, 4, 6}, []float64{0, 1, 0, 2})
	lineB, geometryB := crookedGeometryFixFixture(t, "line-b", []float64{-3, -1, 1, 2}, []float64{7, 9, 8, 10})
	states := []crookedProjectLineState{{line: lineA, geometry: geometryA}, {line: lineB, geometry: geometryB}}
	for i := range states {
		states[i].fingerprint = crookedGeometryFingerprint(states[i].line.ID, states[i].geometry)
		states[i].mapPath = canonicalCrookedMapPath(&states[i], crookedCanonicalMapPointLimit)
		states[i].ready = true
	}
	previous := crookedState
	defer func() { crookedState = previous }()
	crookedState = crookedSession{project: &projectcore.CrookedProject{}, projectLines: states, activeLine: 0, projectGeometryReady: true}
	before := crookedProjectBounds()
	crookedState.activeLine = 1
	after := crookedProjectBounds()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("selecting another line changed project bounds: before=%+v after=%+v", before, after)
	}
	crookedState.projectGeometryReady = false
	if got := crookedProjectBounds(); got.HasXY {
		t.Fatalf("partially built project geometry was published: %+v", got)
	}
}

func TestCrookedGeometryFixPathHitPreservesStartMiddleAndEndTrace(t *testing.T) {
	line, geometry := crookedGeometryFixFixture(t, "line-hit", []float64{0, 3, 7, 11, 14}, []float64{0, 2, 1, 5, 4})
	state := crookedProjectLineState{line: line, geometry: geometry}
	path := canonicalCrookedMapPath(&state, len(geometry.X))
	transform := makeCrookedMapTransform(RECT{Left: 0, Top: 0, Right: 1200, Bottom: 800}, crookedMapPathBounds(path))
	for _, position := range []int{0, len(geometry.X) / 2, len(geometry.X) - 1} {
		x, y := transform.toPixel(geometry.X[position], geometry.Y[position])
		gotPosition, gotTrace, distance, ok := nearestCrookedMapPathSegment(path, x, y, transform)
		if !ok || gotPosition != position || gotTrace != geometry.TraceIndices[position] || distance > 1 {
			t.Fatalf("path hit at position %d resolved position=%d trace=%d distance=%.3f ok=%v", position, gotPosition, gotTrace, distance, ok)
		}
	}
}

func TestCrookedGeometryFixPendingNavigationRestoresClickedTrace(t *testing.T) {
	line, geometry := crookedGeometryFixFixture(t, "line-navigation", []float64{0, 2, 5, 9, 14}, []float64{0, 1, 4, 3, 7})
	state := crookedProjectLineState{line: line, geometry: geometry, ready: true}
	state.fingerprint = crookedGeometryFingerprint(line.ID, geometry)
	state.mapPath = canonicalCrookedMapPath(&state, crookedCanonicalMapPointLimit)

	previousState := crookedState
	previousProjectGen := atomic.LoadInt64(&crookedProjectGen)
	previousSpecGen := atomic.LoadInt64(&crookedSpecGen)
	defer func() {
		crookedState = previousState
		atomic.StoreInt64(&crookedProjectGen, previousProjectGen)
		atomic.StoreInt64(&crookedSpecGen, previousSpecGen)
	}()
	atomic.StoreInt64(&crookedProjectGen, 71)
	atomic.StoreInt64(&crookedSpecGen, 9)
	targetPosition := len(geometry.TraceIndices) - 1
	crookedState = crookedSession{
		project:              &projectcore.CrookedProject{},
		projectLines:         []crookedProjectLineState{state},
		activeLine:           0,
		geometry:             geometry,
		currentPosition:      1,
		viewStart:            0,
		viewEnd:              1,
		projectGeometryReady: true,
		pendingNavigation: &crookedNavigationTarget{
			projectGeneration:   71,
			specGeneration:      9,
			lineIndex:           0,
			lineID:              line.ID,
			traceIndex:          geometry.TraceIndices[targetPosition],
			geometryFingerprint: state.fingerprint,
		},
	}
	if !applyCrookedPendingNavigation(false) || crookedState.currentPosition != targetPosition || crookedState.pendingNavigation != nil {
		t.Fatalf("clicked trace was not restored after line activation: position=%d pending=%+v", crookedState.currentPosition, crookedState.pendingNavigation)
	}

	crookedState.pendingNavigation = &crookedNavigationTarget{projectGeneration: 71, specGeneration: 9, lineIndex: 0, lineID: line.ID,
		traceIndex: geometry.TraceIndices[0], geometryFingerprint: "stale"}
	if applyCrookedPendingNavigation(false) || crookedState.pendingNavigation != nil {
		t.Fatal("stale geometry fingerprint was allowed to reposition the cursor")
	}
}

func TestCrookedGeometryFixRejectsStaleAsynchronousResults(t *testing.T) {
	line := &projectcore.CrookedProjectLine{ID: "line-current"}
	state := &crookedProjectLineState{line: line}
	result := &crookedProjectGeometryResult{projectGen: 17, specGen: 4, lineIndex: 0, lineID: line.ID}
	if !crookedProjectResultMatches(result, state, 17, 4) {
		t.Fatal("current project result was rejected")
	}
	for name, mutate := range map[string]func(*crookedProjectGeometryResult){
		"project generation": func(r *crookedProjectGeometryResult) { r.projectGen++ },
		"header generation":  func(r *crookedProjectGeometryResult) { r.specGen++ },
		"line identity":      func(r *crookedProjectGeometryResult) { r.lineID = "line-stale" },
	} {
		copyResult := *result
		mutate(&copyResult)
		if crookedProjectResultMatches(&copyResult, state, 17, 4) {
			t.Fatalf("stale %s result was accepted: %+v", name, copyResult)
		}
	}
}

func TestCrookedGeometryFixRobustScaleTranslationAndOutlier(t *testing.T) {
	const multiplier, offsetX, offsetY = .1, -20000.0, 5000.0
	values := make([]segy.TraceCoordinate, 9)
	navigation := make([]projectcore.NavigationPoint, len(values))
	for i := range values {
		headerX := 500000.0 + float64(i)*100
		headerY := 4200000.0 + float64(i*i)*20
		cdp := int32(1000 + i*10)
		values[i] = segy.TraceCoordinate{Trace: int64(i), CDP: cdp, HasCDP: true, X: headerX, Y: headerY, Valid: true}
		noise := float64((i%3)-1) * .2
		navigation[i] = projectcore.NavigationPoint{Trace: int64(cdp), X: headerX*multiplier + offsetX + noise, Y: headerY*multiplier + offsetY - noise}
	}
	// One bad navigation pick must not control the decimal scale or translation.
	navigation[4].X += 600
	navigation[4].Y -= 400
	raw, err := geometrycore.NewCrookedLine(values, segy.DefaultCoordinateSpec())
	if err != nil {
		t.Fatal(err)
	}
	rawX, rawY, rawDistance := append([]float64(nil), raw.X...), append([]float64(nil), raw.Y...), append([]float64(nil), raw.Distance...)
	calibration := projectcore.InferCoordinateCalibration(raw, navigation)
	if !calibration.Accepted || calibration.Multiplier != multiplier || math.Abs(calibration.OffsetX-offsetX) > .3 || math.Abs(calibration.OffsetY-offsetY) > .3 || calibration.Inliers < 8 {
		t.Fatalf("robust coordinate calibration mismatch: %+v", calibration)
	}
	transformed, err := projectcore.TransformGeometry(raw, calibration)
	if err != nil {
		t.Fatal(err)
	}
	for _, position := range []int{0, 2, 7, 8} {
		if math.Abs(transformed.X[position]-(raw.X[position]*multiplier+offsetX)) > .3 || math.Abs(transformed.Y[position]-(raw.Y[position]*multiplier+offsetY)) > .3 {
			t.Fatalf("transformed point %d mismatch: X=%f Y=%f", position, transformed.X[position], transformed.Y[position])
		}
	}
	if !reflect.DeepEqual(raw.X, rawX) || !reflect.DeepEqual(raw.Y, rawY) || !reflect.DeepEqual(raw.Distance, rawDistance) {
		t.Fatal("calibration mutated the raw cached geometry")
	}
}

func TestCrookedGeometryFixSelectionQueuesHitBeforeActivation(t *testing.T) {
	selection := phase1FunctionSource(t, "crooked_windows.go", "selectCrookedMapPoint")
	queued := strings.Index(selection, "pendingNavigation =")
	activated := strings.Index(selection, "activateCrookedProjectLine")
	if queued < 0 || activated < 0 || queued >= activated {
		t.Fatalf("map selection does not preserve the hit before asynchronous activation: %s", selection)
	}
	for _, function := range []string{"crookedProjectBounds", "drawCrookedProjectLine", "crookedMapHitAt"} {
		source := phase1FunctionSource(t, "crooked_windows.go", function)
		if strings.Contains(source, ".Navigation") {
			t.Fatalf("%s still switches to DAT navigation geometry", function)
		}
	}
}

func TestPrestackMVPReleaseIdentity(t *testing.T) {
	if APP_NAME != "SeisForge Studio" {
		t.Fatalf("unexpected application name: %q", APP_NAME)
	}
	if APP_VERSION != "1.10.3" {
		t.Fatalf("unexpected application version: %q", APP_VERSION)
	}
	build, err := os.ReadFile(filepath.Join("..", "..", "build_windows_release.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(build), `SeisForgeStudio_v1.10.3_azimuth_wiggle_x64.exe`) || !strings.Contains(string(build), `-H=windowsgui`) {
		t.Fatal("release script does not target the v1.10.3 Windows GUI artifact")
	}
}

func TestCrookedGeometryFixProvidedSurveyAll62CanonicalLines(t *testing.T) {
	root := os.Getenv("SEISFORGE_TEST_CROOKED_PROJECT_DIR")
	if root == "" {
		t.Skip("set SEISFORGE_TEST_CROOKED_PROJECT_DIR to run the external survey acceptance test")
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("SEISFORGE_TEST_CROOKED_PROJECT_DIR is unavailable: %v", err)
	}
	project, err := projectcore.OpenFolder(dataset.NewManager(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.Lines) != 62 || project.ValidLineCount() != 62 || project.NavigationLineCount() != 62 {
		t.Fatalf("provided project mismatch: lines=%d valid=%d navigation=%d", len(project.Lines), project.ValidLineCount(), project.NavigationLineCount())
	}

	start := time.Now()
	states := make([]crookedProjectLineState, len(project.Lines))
	found := map[string]bool{"01554-289": false, "H15FZ6398_286": false, "92549_289": false}
	spec := segy.TraceCoordinateSpec{Source: segy.CoordinateAuto, CDPByte: 21, ScalarByte: 71, UnitsByte: 89}
	for index, line := range project.Lines {
		reader, openErr := line.Dataset.OpenReader()
		if openErr != nil {
			t.Fatalf("open %s: %v", line.Name, openErr)
		}
		build, buildErr := geometrycore.BuildCrookedCachedProgress(reader, spec, maxInt(1, runtime.NumCPU()/2), nil)
		_ = reader.Close()
		if buildErr != nil {
			t.Fatalf("build %s: %v", line.Name, buildErr)
		}
		rawX, rawY := append([]float64(nil), build.Geometry.X...), append([]float64(nil), build.Geometry.Y...)
		states[index] = crookedProjectLineState{line: line, multiplier: 1}
		if err := setCrookedProjectLineGeometry(&states[index], build.Geometry); err != nil {
			t.Fatalf("calibrate %s: %v (diagnostics=%+v)", line.Name, err, states[index].calibration)
		}
		state := &states[index]
		if !state.ready || state.rawGeometry != build.Geometry || state.geometry == nil || len(state.mapPath.Points) < 2 || state.fingerprint == "" || state.mapPath.LineID != line.ID {
			t.Fatalf("canonical state for %s is incomplete: ready=%v path=%d fingerprint=%q", line.Name, state.ready, len(state.mapPath.Points), state.fingerprint)
		}
		if !reflect.DeepEqual(build.Geometry.X, rawX) || !reflect.DeepEqual(build.Geometry.Y, rawY) {
			t.Fatalf("calibration mutated cached geometry for %s", line.Name)
		}
		if got, want := crookedMapPathBounds(state.mapPath), state.geometry.Bounds(); !reflect.DeepEqual(got, want) {
			t.Fatalf("canonical path bounds mismatch for %s: got=%+v want=%+v", line.Name, got, want)
		}
		if again := canonicalCrookedMapPath(state, crookedCanonicalMapPointLimit); again.Fingerprint != state.mapPath.Fingerprint || !reflect.DeepEqual(again.Points, state.mapPath.Points) {
			t.Fatalf("canonical path for %s is not deterministic", line.Name)
		}
		if _, ok := found[line.Name]; ok {
			found[line.Name] = true
		}
		if line.Name == "92549_289" && (!state.calibration.Accepted || state.calibration.Multiplier != .1) {
			t.Fatalf("92549_289 coordinate calibration mismatch: %+v", state.calibration)
		}
	}
	for name, ok := range found {
		if !ok {
			t.Fatalf("provided survey is missing required regression line %s", name)
		}
	}

	previous := crookedState
	defer func() { crookedState = previous }()
	crookedState = crookedSession{project: project, projectLines: states, projectGeometryReady: true, activeLine: 0}
	wantBounds := crookedProjectBounds()
	for index := range states {
		crookedState.activeLine = index
		if got := crookedProjectBounds(); !reflect.DeepEqual(got, wantBounds) {
			t.Fatalf("activating real line %s changed project bounds: got=%+v want=%+v", project.Lines[index].Name, got, wantBounds)
		}
	}
	t.Logf("validated %d calibrated canonical SEG-Y paths in %s", len(states), time.Since(start))
}
