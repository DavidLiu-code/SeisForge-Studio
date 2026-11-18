package pseudoexport

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func testGeometry(x []float64) *geometry.CrookedLineGeometry {
	g := &geometry.CrookedLineGeometry{
		TraceIndices: make([]int64, len(x)), X: append([]float64(nil), x...), Y: make([]float64, len(x)),
		Distance: make([]float64, len(x)), CDP: make([]int32, len(x)), HasCDP: make([]bool, len(x)),
	}
	for i := range x {
		g.TraceIndices[i] = int64(i)
		g.Y[i] = 5
		g.CDP[i], g.HasCDP[i] = int32(100+i), true
		if i > 0 {
			g.Distance[i] = g.Distance[i-1] + math.Abs(x[i]-x[i-1])
		}
	}
	if len(x) > 0 {
		g.TotalDistance = g.Distance[len(x)-1]
	}
	return g
}

func lineInput(name, path string, g *geometry.CrookedLineGeometry, dt, ns int) LineInput {
	traceCount := int64(len(g.TraceIndices))
	fileSize := int64(3600) + traceCount*int64(240+ns*4)
	return LineInput{
		ID: strings.ToLower(name), Name: name, SourcePath: path, Geometry: g, GeometryFingerprint: "fp-" + name,
		Calibration:      project.CoordinateCalibration{Multiplier: .1, OffsetX: 420000, OffsetY: 4250000, Residual: 1.25, Matches: 8, Inliers: 7, Accepted: true},
		SampleIntervalUS: dt, SamplesPerTrace: ns, BytesPerSample: 4, DataStart: 3600,
		FormatCode: 5, Endian: segy.Big, TraceCount: traceCount, FileSize: fileSize,
	}
}

func TestBuildPlanRequiresRange(t *testing.T) {
	_, err := BuildPlan(PlanInput{Lines: []LineInput{lineInput("a", "a.sgy", testGeometry([]float64{0, 1}), 2000, 10)}})
	if err == nil || !strings.Contains(err.Error(), "spatial or time") {
		t.Fatalf("expected missing range error, got %v", err)
	}
}

func TestBuildPlanSpatialReentryKeepsBoundarySupportsAndCanonicalOrder(t *testing.T) {
	g := testGeometry([]float64{-5, 5, 15, 20, 20, 15, 5, -5})
	plan, err := BuildPlan(PlanInput{
		ProjectName: "survey", SpatialRange: pseudo3d.XYRange{XMin: 0, XMax: 10, YMin: 0, YMax: 10, Valid: true},
		Lines: []LineInput{lineInput("bent", "bent.sgy", g, 2000, 12)},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{0, 1, 2, 5, 6, 7}
	if !reflect.DeepEqual(plan.Lines[0].TraceIndices, want) {
		t.Fatalf("boundary support/re-entry selection = %v, want %v", plan.Lines[0].TraceIndices, want)
	}
	if plan.TotalTraces != len(want) {
		t.Fatalf("total traces = %d", plan.TotalTraces)
	}
	actual, clipped := plan.Lines[0].ActualSpatialRange, plan.Lines[0].ClippedSpatialRange
	if !actual.Valid || actual.XMin != -5 || actual.XMax != 15 || actual.YMin != 5 || actual.YMax != 5 {
		t.Fatalf("unexpected exported support bounds: %+v", actual)
	}
	if !clipped.Valid || clipped.XMin != 0 || clipped.XMax != 10 || clipped.YMin != 5 || clipped.YMax != 5 {
		t.Fatalf("unexpected exact clipped curtain bounds: %+v", clipped)
	}
}

func TestBuildPlanTimeOnlySnapsEachSampleRate(t *testing.T) {
	g := testGeometry([]float64{0, 1, 2})
	plan, err := BuildPlan(PlanInput{
		TimeRange: pseudo3d.TimeRange{StartMS: 3.1, EndMS: 8.2, Valid: true},
		Lines: []LineInput{
			lineInput("two-ms", `c:\a\same.sgy`, g, 2000, 20),
			lineInput("one-ms", `d:\b\same.sgy`, g, 1000, 20),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Lines[0].SampleStart != 2 || plan.Lines[0].SampleEnd != 4 {
		t.Fatalf("2 ms window = %d..%d", plan.Lines[0].SampleStart, plan.Lines[0].SampleEnd)
	}
	if plan.Lines[1].SampleStart != 3 || plan.Lines[1].SampleEnd != 8 {
		t.Fatalf("1 ms window = %d..%d", plan.Lines[1].SampleStart, plan.Lines[1].SampleEnd)
	}
	if plan.Lines[0].OutputName == plan.Lines[1].OutputName || !strings.Contains(plan.Lines[0].OutputName, "_") || !strings.Contains(plan.Lines[1].OutputName, "_") {
		t.Fatalf("duplicate basenames were not assigned stable distinct names: %q %q", plan.Lines[0].OutputName, plan.Lines[1].OutputName)
	}
	again, err := BuildPlan(PlanInput{TimeRange: plan.TimeRange, Lines: []LineInput{
		lineInput("two-ms", `c:\a\same.sgy`, g, 2000, 20), lineInput("one-ms", `d:\b\same.sgy`, g, 1000, 20),
	}})
	if err != nil || again.Lines[0].OutputName != plan.Lines[0].OutputName || again.Lines[1].OutputName != plan.Lines[1].OutputName {
		t.Fatalf("stable names changed: %+v, err=%v", again.Lines, err)
	}
}

func TestBuildPlanUsesFrozenTimeWindowAndRejectsEmptyIntersection(t *testing.T) {
	g := testGeometry([]float64{0, 1, 2})
	in := lineInput("frozen", "frozen.sgy", g, 2000, 20)
	in.SampleWindowSet, in.SampleStart, in.SampleEnd = true, 4, 9
	plan, err := BuildPlan(PlanInput{TimeRange: pseudo3d.TimeRange{StartMS: 1, EndMS: 30, Valid: true}, Lines: []LineInput{in}})
	if err != nil || plan.Lines[0].SampleStart != 4 || plan.Lines[0].SampleEnd != 9 {
		t.Fatalf("frozen window not preserved: %+v, err=%v", plan.Lines, err)
	}
	_, err = BuildPlan(PlanInput{
		SpatialRange: pseudo3d.XYRange{XMin: 100, XMax: 110, YMin: 100, YMax: 110, Valid: true},
		Lines:        []LineInput{lineInput("outside", "outside.sgy", g, 2000, 20)},
	})
	if err == nil || !strings.Contains(err.Error(), "no line intersection") {
		t.Fatalf("expected empty intersection, got %v", err)
	}
}

func TestBuildPlanEstimateUsesFrozenBytesPerSampleAndDataStart(t *testing.T) {
	in := lineInput("integer16-ext", "integer16-ext.sgy", testGeometry([]float64{0, 1, 2}), 4000, 20)
	in.BytesPerSample, in.DataStart = 2, 6800
	in.FileSize = in.DataStart + in.TraceCount*int64(240+in.SamplesPerTrace*in.BytesPerSample)
	plan, err := BuildPlan(PlanInput{
		TimeRange: pseudo3d.TimeRange{StartMS: 8, EndMS: 24, Valid: true}, Lines: []LineInput{in},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 8..24 ms at 4 ms is samples 2..6 inclusive: five 2-byte samples.
	want := int64(6800 + 3*(240+5*2))
	if plan.EstimatedBytes != want || plan.Lines[0].EstimatedBytes != want {
		t.Fatalf("estimate = %d/%d, want %d", plan.EstimatedBytes, plan.Lines[0].EstimatedBytes, want)
	}
}

func makeTestSEGYSynthetic(t *testing.T, path string, traces, samples, dt int) []byte {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 3600)
	for i := 0; i < 3200; i++ {
		header[i] = byte((i*13 + 17) % 251)
	}
	binary.BigEndian.PutUint16(header[3216:3218], uint16(dt))
	binary.BigEndian.PutUint16(header[3218:3220], uint16(dt))
	binary.BigEndian.PutUint16(header[3220:3222], uint16(samples))
	binary.BigEndian.PutUint16(header[3222:3224], uint16(samples))
	binary.BigEndian.PutUint16(header[3224:3226], 5)
	if _, err = f.Write(header); err != nil {
		t.Fatal(err)
	}
	for trace := 0; trace < traces; trace++ {
		raw := make([]byte, 240+samples*4)
		binary.BigEndian.PutUint32(raw[20:24], uint32(900+trace))
		scalar := int16(-10)
		binary.BigEndian.PutUint16(raw[70:72], uint16(scalar))
		binary.BigEndian.PutUint32(raw[72:76], uint32(100000+trace*20))
		binary.BigEndian.PutUint32(raw[76:80], uint32(200000+trace*30))
		binary.BigEndian.PutUint16(raw[114:116], uint16(samples))
		binary.BigEndian.PutUint16(raw[116:118], uint16(dt))
		for sample := 0; sample < samples; sample++ {
			binary.BigEndian.PutUint32(raw[240+sample*4:244+sample*4], math.Float32bits(float32(trace*1000+sample)))
		}
		if _, err = f.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes
}

func TestExportCopiesDATWritesManifestAndContinuesAfterLineFailure(t *testing.T) {
	root := t.TempDir()
	goodPath := filepath.Join(root, "source", "good.sgy")
	sourceBytes := makeTestSEGYSynthetic(t, goodPath, 8, 12, 2000)
	datPath := filepath.Join(root, "survey.dat")
	datBytes := []byte("line sp trace x y min max\r\nA 1 2 3 4 5 6\r\n")
	if err := os.WriteFile(datPath, datBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	g := testGeometry([]float64{-5, 5, 15, 20, 20, 15, 5, -5})
	good := lineInput("good", goodPath, g, 2000, 12)
	bad := lineInput("bad", filepath.Join(root, "missing.sgy"), g, 2000, 12)
	plan, err := BuildPlan(PlanInput{
		ProjectName: "示例/测线", NavigationPath: datPath,
		SpatialRange: pseudo3d.XYRange{XMin: 0, XMax: 10, YMin: 0, YMax: 10, Valid: true},
		TimeRange:    pseudo3d.TimeRange{StartMS: 4, EndMS: 14, Valid: true}, Lines: []LineInput{bad, good},
	})
	if err != nil {
		t.Fatal(err)
	}
	var progress []float64
	result, err := Export(context.Background(), plan, filepath.Join(root, "exports"), func(update PseudoExportProgress) {
		progress = append(progress, update.Percent)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "partial" || result.CompletedLines != 1 || result.FailedLines != 1 || len(result.Lines) != 2 {
		t.Fatalf("unexpected partial result: %+v", result)
	}
	for i := 1; i < len(progress); i++ {
		if progress[i] < progress[i-1] {
			t.Fatalf("progress regressed: %v", progress)
		}
	}
	if progress[len(progress)-1] != 100 {
		t.Fatalf("final progress = %g", progress[len(progress)-1])
	}
	if got, err := os.ReadFile(result.NavigationCopy.OutputPath); err != nil || !reflect.DeepEqual(got, datBytes) {
		t.Fatalf("DAT copy differs: err=%v", err)
	}
	wantDATHash := sha256.Sum256(datBytes)
	if result.NavigationCopy.SHA256 != fmtHash(wantDATHash) {
		t.Fatalf("DAT hash = %s", result.NavigationCopy.SHA256)
	}
	if got, err := os.ReadFile(goodPath); err != nil || !reflect.DeepEqual(got, sourceBytes) {
		t.Fatalf("source SEG-Y changed: err=%v", err)
	}
	var completed PseudoLineExportResult
	for _, line := range result.Lines {
		if line.Status == "completed" {
			completed = line
		}
	}
	output, err := segy.Open(completed.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if output.Info.TraceCount != 6 || output.Info.SamplesPerTrace != 6 || output.Info.SampleIntervalUS != 2000 || output.Info.FormatCode != 5 {
		t.Fatalf("bad reopened cropped output: %+v", output.Info)
	}
	if completed.OutputTraceCount != 6 || completed.OutputDataStart != 3600 || completed.OutputFormatCode != 5 {
		t.Fatalf("missing output verification diagnostics: %+v", completed)
	}
	manifestBytes, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest PseudoExportResult
	if err = json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "partial" || manifest.NavigationCopy.SHA256 == "" || len(manifest.Lines) != 2 || manifest.Lines[1].GeometryFingerprint == "" {
		t.Fatalf("manifest lacks diagnostics: %+v", manifest)
	}
}

func TestExportCancellationPreservesCompletedLineAndWritesManifest(t *testing.T) {
	root := t.TempDir()
	firstPath, secondPath, thirdPath := filepath.Join(root, "first.sgy"), filepath.Join(root, "second.sgy"), filepath.Join(root, "third.sgy")
	makeTestSEGYSynthetic(t, firstPath, 4, 16, 1000)
	makeTestSEGYSynthetic(t, secondPath, 40, 16, 1000)
	makeTestSEGYSynthetic(t, thirdPath, 4, 16, 1000)
	firstGeometry := testGeometry([]float64{0, 1, 2, 3})
	secondX := make([]float64, 40)
	for i := range secondX {
		secondX[i] = float64(i)
	}
	plan, err := BuildPlan(PlanInput{
		TimeRange: pseudo3d.TimeRange{StartMS: 2, EndMS: 10, Valid: true},
		Lines: []LineInput{
			lineInput("first", firstPath, firstGeometry, 1000, 16),
			lineInput("second", secondPath, testGeometry(secondX), 1000, 16),
			lineInput("third", thirdPath, firstGeometry, 1000, 16),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var progress []float64
	result, err := Export(ctx, plan, filepath.Join(root, "out"), func(update PseudoExportProgress) {
		progress = append(progress, update.Percent)
		if update.LineIndex == 2 && update.TraceDone >= 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || !result.Cancelled || result.Status != "cancelled" {
		t.Fatalf("expected cancellation result, got status=%q cancelled=%v err=%v", result.Status, result.Cancelled, err)
	}
	if result.CompletedLines != 1 || len(result.Lines) != 3 || result.Lines[0].Status != "completed" || result.Lines[1].Status != "cancelled" || result.Lines[2].Status != "not_started" {
		t.Fatalf("completed/cancelled lines = %+v", result.Lines)
	}
	for i := 1; i < len(progress); i++ {
		if progress[i] < progress[i-1] {
			t.Fatalf("cancellation progress regressed: %v", progress)
		}
	}
	if _, err = os.Stat(result.Lines[0].OutputPath); err != nil {
		t.Fatalf("completed output was not preserved: %v", err)
	}
	if _, err = os.Stat(result.Lines[1].OutputPath); !os.IsNotExist(err) {
		t.Fatalf("cancelled partial output remains: %v", err)
	}
	manifestBytes, readErr := os.ReadFile(result.ManifestPath)
	if readErr != nil || !strings.Contains(string(manifestBytes), `"status": "cancelled"`) {
		t.Fatalf("cancel manifest missing: err=%v", readErr)
	}
}

func TestExportRejectsUnsafeDuplicateAndReservedOutputNames(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sgy")
	makeTestSEGYSynthetic(t, sourcePath, 4, 12, 2000)
	base, err := BuildPlan(PlanInput{
		TimeRange: pseudo3d.TimeRange{StartMS: 2, EndMS: 8, Valid: true},
		Lines:     []LineInput{lineInput("safe", sourcePath, testGeometry([]float64{0, 1, 2, 3}), 2000, 12)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafeName := range []string{`..\escape.sgy`, `../escape.sgy`, `C:\escape.sgy`, "pseudo_crop_manifest.json", "CON.sgy", "trailing.sgy "} {
		plan := base
		plan.Lines = append([]PseudoLineExportPlan(nil), base.Lines...)
		plan.Lines[0].OutputName = unsafeName
		if _, exportErr := Export(context.Background(), plan, filepath.Join(root, "out"), nil); exportErr == nil {
			t.Fatalf("unsafe output name was accepted: %q", unsafeName)
		}
	}

	duplicate := base
	duplicate.Lines = []PseudoLineExportPlan{base.Lines[0], base.Lines[0]}
	duplicate.Lines[1].SourcePath = filepath.Join(root, "other.sgy")
	duplicate.Lines[1].ID = "other"
	duplicate.TotalTraces *= 2
	duplicate.EstimatedBytes *= 2
	if _, exportErr := Export(context.Background(), duplicate, filepath.Join(root, "out"), nil); exportErr == nil || !strings.Contains(exportErr.Error(), "duplicated") {
		t.Fatalf("duplicate output name was accepted: %v", exportErr)
	}

	reservedDAT := base
	reservedDAT.NavigationPath = filepath.Join(root, base.Lines[0].OutputName)
	if _, exportErr := Export(context.Background(), reservedDAT, filepath.Join(root, "out"), nil); exportErr == nil || !strings.Contains(exportErr.Error(), "reserved") {
		t.Fatalf("DAT/output name collision was accepted: %v", exportErr)
	}
}

func TestExportNavigationCopyFailureMakesBatchPartial(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "line.sgy")
	makeTestSEGYSynthetic(t, source, 3, 10, 2000)
	plan, err := BuildPlan(PlanInput{
		NavigationPath: filepath.Join(root, "missing.dat"), TimeRange: pseudo3d.TimeRange{StartMS: 2, EndMS: 8, Valid: true},
		Lines: []LineInput{lineInput("line", source, testGeometry([]float64{0, 1, 2}), 2000, 10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Export(context.Background(), plan, filepath.Join(root, "out"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "partial" || result.CompletedLines != 1 || result.NavigationCopy.Status != "failed" || len(result.Errors) == 0 {
		t.Fatalf("DAT failure not represented as partial: %+v", result)
	}
}

func TestExportRejectsSourceMetadataChangedAfterSnapshot(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "line.sgy")
	makeTestSEGYSynthetic(t, source, 3, 10, 2000)
	plan, err := BuildPlan(PlanInput{
		TimeRange: pseudo3d.TimeRange{StartMS: 2, EndMS: 8, Valid: true},
		Lines:     []LineInput{lineInput("line", source, testGeometry([]float64{0, 1, 2}), 2000, 10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := Export(context.Background(), plan, filepath.Join(root, "out"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || result.FailedLines != 1 || len(result.Lines) != 1 || !strings.Contains(result.Lines[0].Error, "metadata changed") {
		t.Fatalf("changed source was not rejected: %+v", result)
	}
	if _, err = os.Stat(result.Lines[0].OutputPath); !os.IsNotExist(err) {
		t.Fatalf("changed source produced an output: %v", err)
	}
}

func fmtHash(value [32]byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(value)*2)
	for i, b := range value {
		out[i*2], out[i*2+1] = digits[b>>4], digits[b&15]
	}
	return string(out)
}
