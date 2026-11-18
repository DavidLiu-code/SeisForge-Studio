//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	pseudoexportcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudoexport"
	segycore "github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func pseudoExportFileSHA256(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

// This opt-in test performs a real, deliberately small spatial+time crop,
// reopens the exported SEG-Y and proves that the source SEG-Y/DAT hashes did
// not change. It is kept out of ordinary unit-test runs because it leaves its
// timestamped validation result in the explicitly supplied output directory.
func TestPseudoExportRealProjectSmallCropAndSourceIntegrity(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("LIMAGE_PSEUDO_EXPORT_REAL_PROJECT"))
	outputParent := strings.TrimSpace(os.Getenv("LIMAGE_PSEUDO_EXPORT_REAL_OUTPUT"))
	if root == "" || outputParent == "" {
		t.Skip("set LIMAGE_PSEUDO_EXPORT_REAL_PROJECT and LIMAGE_PSEUDO_EXPORT_REAL_OUTPUT for a real export")
	}
	manager := dataset.NewManager()
	project, err := projectcore.OpenFolder(manager, root)
	if err != nil {
		t.Fatal(err)
	}
	var line *projectcore.CrookedProjectLine
	for _, candidate := range project.Lines {
		if !candidate.Valid() || candidate.Dataset.Metadata.TraceCount < 2 {
			continue
		}
		if line == nil || candidate.Dataset.Metadata.TraceCount < line.Dataset.Metadata.TraceCount {
			line = candidate
		}
	}
	if line == nil {
		t.Fatal("real project has no valid multi-trace line")
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
	if len(canonical.TraceIndices) < 2 {
		t.Fatal("real canonical geometry has fewer than two traces")
	}

	middle := len(canonical.TraceIndices) / 2
	left := middle - 1
	if left < 0 {
		left = 0
	}
	distance := math.Hypot(canonical.X[middle]-canonical.X[left], canonical.Y[middle]-canonical.Y[left])
	padding := math.Max(1, distance*.25)
	spatial := pseudo3dcore.XYRange{
		XMin:  math.Min(canonical.X[left], canonical.X[middle]) - padding,
		XMax:  math.Max(canonical.X[left], canonical.X[middle]) + padding,
		YMin:  math.Min(canonical.Y[left], canonical.Y[middle]) - padding,
		YMax:  math.Max(canonical.Y[left], canonical.Y[middle]) + padding,
		Valid: true,
	}
	metadata := line.Dataset.Metadata
	sampleStart := 5
	if metadata.SamplesPerTrace <= 24 {
		sampleStart = 0
	}
	sampleEnd := sampleStart + 20
	if sampleEnd >= metadata.SamplesPerTrace {
		sampleEnd = metadata.SamplesPerTrace - 1
	}
	if sampleEnd <= sampleStart {
		t.Fatal("real line has too few samples for a cropped time window")
	}
	timeRange := pseudo3dcore.TimeRange{
		StartMS: float64(sampleStart*metadata.SampleIntervalUS) / 1000,
		EndMS:   float64(sampleEnd*metadata.SampleIntervalUS) / 1000,
		Valid:   true,
	}
	plan, err := pseudoexportcore.BuildPlan(pseudoexportcore.PlanInput{
		ProjectName: filepath.Base(project.Root), NavigationPath: project.NavigationPath,
		SpatialRange: spatial, TimeRange: timeRange,
		Lines: []pseudoexportcore.LineInput{{
			ID: line.ID, Name: line.Name, SourcePath: line.Path,
			GeometryFingerprint: crookedGeometryFingerprint(line.ID, canonical), Geometry: canonical, Calibration: calibration,
			SampleIntervalUS: metadata.SampleIntervalUS, SamplesPerTrace: metadata.SamplesPerTrace,
			BytesPerSample: metadata.BytesPerSample, DataStart: metadata.DataStart, FormatCode: metadata.FormatCode,
			Endian: metadata.Endian, TraceCount: metadata.TraceCount, FileSize: metadata.FileSize,
			ExtendedTextHeaders: metadata.ExtendedTextHeaders,
			SampleStart:         sampleStart, SampleEnd: sampleEnd, SampleWindowSet: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Lines) != 1 || plan.TotalTraces < 1 || int64(plan.TotalTraces) >= metadata.TraceCount {
		t.Fatalf("real validation plan is not spatially cropped: traces=%d source=%d", plan.TotalTraces, metadata.TraceCount)
	}

	sourceHashBefore, err := pseudoExportFileSHA256(line.Path)
	if err != nil {
		t.Fatal(err)
	}
	var datHashBefore [sha256.Size]byte
	if project.NavigationPath != "" {
		datHashBefore, err = pseudoExportFileSHA256(project.NavigationPath)
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := pseudoexportcore.Export(context.Background(), plan, outputParent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.CompletedLines != 1 || result.FailedLines != 0 {
		t.Fatalf("real crop export did not complete cleanly: %+v", result)
	}
	paths := result.SortedOutputPaths()
	if len(paths) != 1 {
		t.Fatalf("real crop output paths = %v", paths)
	}
	output, err := segycore.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	outputInfo := output.Info
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
	wantSamples := sampleEnd - sampleStart + 1
	if outputInfo.TraceCount != int64(plan.TotalTraces) || outputInfo.SamplesPerTrace != wantSamples ||
		outputInfo.SampleIntervalUS != metadata.SampleIntervalUS || outputInfo.FormatCode != metadata.FormatCode ||
		outputInfo.Endian != metadata.Endian || outputInfo.DataStart != metadata.DataStart {
		t.Fatalf("reopened real crop metadata mismatch: %+v", outputInfo)
	}
	sourceHashAfter, err := pseudoExportFileSHA256(line.Path)
	if err != nil {
		t.Fatal(err)
	}
	if sourceHashBefore != sourceHashAfter {
		t.Fatal("source SEG-Y SHA-256 changed during crop export")
	}
	if project.NavigationPath != "" {
		datHashAfter, hashErr := pseudoExportFileSHA256(project.NavigationPath)
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		if datHashBefore != datHashAfter {
			t.Fatal("source DAT SHA-256 changed during crop export")
		}
	}
	manifestBytes, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest pseudoexportcore.PseudoExportResult
	if err = json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "completed" || len(manifest.Lines) != 1 || manifest.Lines[0].GeometryFingerprint == "" {
		t.Fatalf("real crop manifest is incomplete: %+v", manifest)
	}
	t.Logf("real pseudo crop validation: line=%s traces=%d samples=%d output=%s", line.Name, plan.TotalTraces, wantSamples, paths[0])
}
