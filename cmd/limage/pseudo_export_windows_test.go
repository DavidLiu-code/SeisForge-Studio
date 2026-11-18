//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	pseudoexportcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudoexport"
)

func pseudoExportFixture(name string, xOffset float64) (*projectcore.CrookedProjectLine, *geometrycore.CrookedLineGeometry, pseudoLineState) {
	path := filepath.Join(`C:\survey`, name+".sgy")
	data := &dataset.SeismicDataset{Path: path, Metadata: dataset.Metadata{
		FileSize: 100000, SampleIntervalUS: 2000, SamplesPerTrace: 10, FormatCode: 5,
		BytesPerSample: 4, DataStart: 3600, TraceBytes: 280, TraceCount: 5,
	}}
	line := &projectcore.CrookedProjectLine{ID: strings.ToLower(path), Name: name, Path: path, Dataset: data}
	geometry := &geometrycore.CrookedLineGeometry{
		TraceIndices: []int64{0, 1, 2, 3, 4},
		X:            []float64{xOffset, xOffset + 1, xOffset + 2, xOffset + 3, xOffset + 4},
		Y:            []float64{0, 0, 0, 0, 0},
		Distance:     []float64{0, 1, 2, 3, 4}, TotalDistance: 4,
	}
	state := pseudoLineState{line: line, geometry: geometry, geometryCalibrated: true, geometryFingerprint: "fp-" + name,
		selected: true, inRange: true, eligibilityKnown: true, timeInRange: true, ready: true,
		sampleStart: 1, sampleEnd: 3, segments: []pseudoCurtainSegment{{traceIndices: []int64{0, 1}}}, multiplier: 1}
	return line, geometry, state
}

func TestPseudoExportAvailabilityContract(t *testing.T) {
	line, _, ready := pseudoExportFixture("visible", 0)
	_, _, outside := pseudoExportFixture("outside", 100)
	outside.inRange, outside.ready, outside.segments = false, false, nil
	project := &projectcore.CrookedProject{Root: `C:\survey`, Lines: []*projectcore.CrookedProjectLine{line, outside.line}}
	session := pseudoSession{project: project, lines: []pseudoLineState{ready, outside}}
	if got := pseudoExportAvailabilityForSession(&session, false); got.Enabled || !strings.Contains(got.Reason, "空间或时间") {
		t.Fatalf("no-range availability = %+v", got)
	}
	session.spatialRange = pseudo3dcore.XYRange{XMin: .5, XMax: 2.5, YMin: -1, YMax: 1, Valid: true}
	if got := pseudoExportAvailabilityForSession(&session, false); !got.Enabled || got.VisibleReady != 1 {
		t.Fatalf("ready availability = %+v", got)
	}
	// A selected line outside the current AOI is not part of the current
	// curtain scene and must not leave the export button waiting forever.
	outside.loading = true
	session.lines[1] = outside
	if got := pseudoExportAvailabilityForSession(&session, false); !got.Enabled || got.VisibleReady != 1 {
		t.Fatalf("outside loading line blocked export: %+v", got)
	}
	session.rangeDragging = true
	if got := pseudoExportAvailabilityForSession(&session, false); got.Enabled || !strings.Contains(got.Reason, "拖动") {
		t.Fatalf("drag availability = %+v", got)
	}
	session.rangeDragging = false
	session.lines[0].loading = true
	if got := pseudoExportAvailabilityForSession(&session, false); got.Enabled || !strings.Contains(got.Reason, "加载") {
		t.Fatalf("load availability = %+v", got)
	}
	session.lines[0].loading = false
	if got := pseudoExportAvailabilityForSession(&session, true); got.Enabled || !strings.Contains(got.Reason, "正在运行") {
		t.Fatalf("running availability = %+v", got)
	}
}

func TestPseudoExportSnapshotVisibleOnlyAndImmutable(t *testing.T) {
	line, geometry, visible := pseudoExportFixture("visible", 0)
	outsideLine, _, outside := pseudoExportFixture("outside", 100)
	outside.inRange, outside.ready, outside.segments = false, false, nil
	project := &projectcore.CrookedProject{Root: `C:\survey`, NavigationPath: `C:\survey\survey.dat`,
		Lines: []*projectcore.CrookedProjectLine{line, outsideLine}}
	calibration := projectcore.CoordinateCalibration{Multiplier: .1, OffsetX: 12, OffsetY: -8, Matches: 5, Inliers: 5, Accepted: true}
	session := pseudoSession{project: project, lines: []pseudoLineState{visible, outside},
		spatialRange: pseudo3dcore.XYRange{XMin: .5, XMax: 2.5, YMin: -1, YMax: 1, Valid: true},
		timeRange:    pseudo3dcore.TimeRange{StartMS: 2, EndMS: 6, Valid: true}}
	projectStates := []crookedProjectLineState{{line: line, calibration: calibration}, {line: outsideLine}}

	input := pseudoExportPlanInputFromSession(&session, projectStates)
	if len(input.Lines) != 1 || input.Lines[0].ID != line.ID {
		t.Fatalf("snapshot lines = %+v", input.Lines)
	}
	if !input.Lines[0].SampleWindowSet || input.Lines[0].BytesPerSample != 4 || input.Lines[0].DataStart != 3600 {
		t.Fatalf("snapshot sample metadata = %+v", input.Lines[0])
	}
	if input.Lines[0].Calibration != calibration {
		t.Fatalf("snapshot calibration = %+v want %+v", input.Lines[0].Calibration, calibration)
	}
	geometry.X[1] = 999
	session.lines[0].sampleStart = 7
	if input.Lines[0].Geometry.X[1] != 1 || input.Lines[0].SampleStart != 1 {
		t.Fatalf("snapshot changed with live state: x=%v sample=%d", input.Lines[0].Geometry.X, input.Lines[0].SampleStart)
	}
	plan, err := pseudoexportcore.BuildPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Lines) != 1 || plan.TotalTraces == 0 || plan.Lines[0].SampleStart != 1 || plan.Lines[0].SampleEnd != 3 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestPseudoExportProgressLatestWinsAndGeneration(t *testing.T) {
	state := pseudoExportDeliveryState{generation: 9, hwnd: 123, running: true}
	if hwnd, posted := coalescePseudoExportProgress(&state, 9, pseudoexportcore.PseudoExportProgress{Percent: 10}); !posted || hwnd != 123 {
		t.Fatalf("first progress was not posted: hwnd=%d post=%t", hwnd, posted)
	}
	if _, posted := coalescePseudoExportProgress(&state, 9, pseudoexportcore.PseudoExportProgress{Percent: 75}); posted {
		t.Fatal("coalesced progress posted a second window message")
	}
	if state.progress == nil || state.progress.Percent != 75 {
		t.Fatalf("latest progress was not retained: %+v", state.progress)
	}
	if _, posted := coalescePseudoExportProgress(&state, 8, pseudoexportcore.PseudoExportProgress{Percent: 99}); posted || state.progress.Percent != 75 {
		t.Fatal("stale generation replaced current progress")
	}
	state.progress, state.progressPosted = nil, false
	if _, posted := coalescePseudoExportProgress(&state, 9, pseudoexportcore.PseudoExportProgress{Percent: 80}); !posted {
		t.Fatal("progress did not re-arm after UI consumption")
	}
}

func TestPseudoExportHelpAndWindowClass(t *testing.T) {
	help := pseudoShortcutHelpText()
	for _, want := range []string{"导出范围", "勾选", "XY AOI", "时间窗", "不包含相机"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help is missing %q", want)
		}
	}
	source, err := os.ReadFile("main_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `registerClass("Limage64PseudoExport"`) {
		t.Fatal("pseudo export progress window class is not registered")
	}
}

func TestPseudoExportRealProjectSnapshot(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("LIMAGE_PSEUDO_EXPORT_REAL_PROJECT"))
	if root == "" {
		t.Skip("set LIMAGE_PSEUDO_EXPORT_REAL_PROJECT for real-project snapshot coverage")
	}
	manager := dataset.NewManager()
	project, err := projectcore.OpenFolder(manager, root)
	if err != nil {
		t.Fatal(err)
	}
	var line *projectcore.CrookedProjectLine
	for _, candidate := range project.Lines {
		if candidate.Valid() {
			line = candidate
			break
		}
	}
	if line == nil {
		t.Fatal("real project has no valid line")
	}
	reader, err := line.Dataset.OpenReader()
	if err != nil {
		t.Fatal(err)
	}
	build, buildErr := geometrycore.BuildCrookedCachedProgress(reader, defaultCrookedView().Spec, 0, nil)
	closeErr := reader.Close()
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	canonical, calibration, err := applyProjectCalibration(line, build.Geometry)
	if err != nil {
		t.Fatal(err)
	}
	middle := len(canonical.X) / 2
	span := canonical.TotalDistance * .02
	if span <= 0 {
		span = 1
	}
	rangeXY := pseudo3dcore.XYRange{XMin: canonical.X[middle] - span, XMax: canonical.X[middle] + span,
		YMin: canonical.Y[middle] - span, YMax: canonical.Y[middle] + span, Valid: true}
	sampleEnd := 20
	if sampleEnd >= line.Dataset.Metadata.SamplesPerTrace {
		sampleEnd = line.Dataset.Metadata.SamplesPerTrace - 1
	}
	if sampleEnd < 1 {
		t.Fatal("real line has too few samples")
	}
	timeRange := pseudo3dcore.TimeRange{StartMS: 0, EndMS: float64(sampleEnd*line.Dataset.Metadata.SampleIntervalUS) / 1000, Valid: true}
	state := pseudoLineState{line: line, geometry: canonical, geometryCalibrated: true,
		geometryFingerprint: crookedGeometryFingerprint(line.ID, canonical), selected: true, inRange: true, eligibilityKnown: true,
		timeInRange: true, ready: true, sampleStart: 0, sampleEnd: sampleEnd, segments: []pseudoCurtainSegment{{traceIndices: []int64{canonical.TraceIndices[middle]}}}}
	session := pseudoSession{project: project, lines: []pseudoLineState{state}, spatialRange: rangeXY, timeRange: timeRange}
	input := pseudoExportPlanInputFromSession(&session, []crookedProjectLineState{{line: line, calibration: calibration}})
	plan, err := pseudoexportcore.BuildPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Lines) != 1 || plan.TotalTraces == 0 {
		t.Fatalf("real snapshot plan is empty: %+v", plan)
	}
}
