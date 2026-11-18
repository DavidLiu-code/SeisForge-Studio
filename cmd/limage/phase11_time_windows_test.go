//go:build windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	pseudocachecore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudocache"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func writePhase11TimeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "phase11-time.sgy")
	coordinates := make([]testsegy.TraceCoordinate, 64)
	for i := range coordinates {
		coordinates[i] = testsegy.TraceCoordinate{X: int32(1000 + i*20), Y: int32(2000 + i*i), CDP: int32(500 + i)}
	}
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: len(coordinates), Samples: 200, CoordinateScalar: 1, Coordinates: coordinates}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPhase11PseudoLoadReadsOnlySelectedSamplesAndCachesExactWindow(t *testing.T) {
	path := writePhase11TimeFixture(t)
	data, err := dataset.NewManager().Open(path)
	if err != nil {
		t.Fatal(err)
	}
	line := &projectcore.CrookedProjectLine{ID: path, Name: "time-line", Path: path, Dataset: data}
	cache, err := pseudocachecore.New(filepath.Join(t.TempDir(), "cache"), 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	atomic.StoreInt64(&pseudoActiveRangeGen, 0)
	atomic.StoreInt64(&pseudoActiveTimeGen, 0)
	base := pseudoLoadJob{windowGen: 1, lineIndex: 0, token: 1, line: line,
		spec: segy.DefaultCoordinateSpecs()[0], size: pseudo3dcore.TextureSize{Width: 96, Height: 50},
		gain: 0, clip: 99, displayMode: segy.DisplayAdaptive, skipPersistentCache: true}
	full := base
	full.sampleStart, full.sampleEnd, full.timeStartMS, full.timeEndMS = 0, 199, 0, 398
	fullResult := performPseudoLoad(full)
	if fullResult.err != nil {
		t.Fatal(fullResult.err)
	}
	fullCacheJob := full
	fullCacheJob.geometry, fullCacheJob.geometryCalibrated, fullCacheJob.existingMultiplier = fullResult.geometry, true, fullResult.multiplier
	fullCacheJob.geometryFingerprint = crookedGeometryFingerprint(line.ID, fullResult.geometry)
	fullCacheJob.textureCache, fullCacheJob.skipPersistentCache = cache, false
	fullWrite := pseudoPrepareCacheWrite(fullCacheJob, fullResult)
	if fullWrite == nil {
		t.Fatal("complete-time cache fixture was not prepared")
	}
	if err := cache.Store(fullWrite.key, fullWrite.entry); err != nil {
		t.Fatal(err)
	}
	window := base
	window.geometry, window.geometryCalibrated, window.existingMultiplier = fullResult.geometry, true, fullResult.multiplier
	window.geometryFingerprint = fullCacheJob.geometryFingerprint
	window.sampleStart, window.sampleEnd, window.timeStartMS, window.timeEndMS = 50, 99, 100, 198
	window.skipPersistentCache, window.textureCache = false, cache
	coveringPreview, previewHit := pseudoTryPersistentCoveringPreview(window)
	if !previewHit || coveringPreview == nil || !coveringPreview.previewOnly || len(coveringPreview.segments) == 0 || !coveringPreview.segments[0].preview {
		t.Fatal("complete-time cache was not available as a selected-window preview")
	}
	windowResult := performPseudoLoad(window)
	if windowResult.err != nil || windowResult.texture == nil || len(windowResult.segments) == 0 {
		t.Fatalf("time-window load failed: err=%v texture=%v", windowResult.err, windowResult.texture != nil)
	}
	segment := windowResult.segments[0]
	if segment.sampleStart != 50 || segment.sampleEnd != 99 || segment.timeStartMS != 100 || segment.timeEndMS != 198 || segment.texture.Height != 50 {
		t.Fatalf("selected sample metadata changed: %+v", segment)
	}
	if windowResult.ioReadBytes*100 > fullResult.ioReadBytes*35 {
		t.Fatalf("25%% time window read too many bytes: window=%d full=%d", windowResult.ioReadBytes, fullResult.ioReadBytes)
	}
	if windowResult.cacheWrite == nil {
		t.Fatal("exact time window was not prepared for persistent cache")
	}
	if err := cache.Store(windowResult.cacheWrite.key, windowResult.cacheWrite.entry); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	warm := performPseudoLoad(window)
	if warm.err != nil || !warm.cacheHit || warm.ioReadCalls != 0 || warm.texture == nil {
		t.Fatalf("exact time cache did not bypass SEG-Y: hit=%v calls=%d err=%v", warm.cacheHit, warm.ioReadCalls, warm.err)
	}
	if !bytes.Equal(windowResult.texture.Indices, warm.texture.Indices) {
		t.Fatal("time-window cache changed indexed pixels")
	}
}

func TestPhase11VerticalPreviewCropAndTimeResize(t *testing.T) {
	texture := &pseudo3dcore.Texture{Width: 8, Height: 200, Indices: make([]byte, 8*200)}
	state := pseudoLineState{size: pseudo3dcore.TextureSize{Width: 8, Height: 50}, timeStartMS: 100, timeEndMS: 198,
		cachedSegments: []pseudoCurtainSegment{{texture: texture, sampleStart: 0, sampleEnd: 199, timeStartMS: 0, timeEndMS: 398,
			positionStart: 0, positionEnd: 10, points: []pseudo3dcore.Point{{X: 0, Y: 0}, {X: 10, Y: 0}}, u: []float64{0, 1}}}}
	preview := pseudoPreviewSegmentSourcesForWindow(&state, 50, 99)
	if len(preview) != 1 || !preview[0].preview || preview[0].texture.Height != 50 || preview[0].sampleStart != 50 || preview[0].sampleEnd != 99 {
		t.Fatalf("unexpected vertical preview: %+v", preview)
	}
	project := pseudo3dcore.TimeRange{StartMS: 0, EndMS: 4000, Valid: true}
	original := pseudo3dcore.TimeRange{StartMS: 500, EndMS: 1500, Valid: true}
	shrunk := resizedPseudoTimeRange(original, project, pseudoTimeStart, 900)
	if shrunk.StartMS != 900 || shrunk.EndMS != 1500 {
		t.Fatalf("time-start resize failed: %+v", shrunk)
	}
	minimum := resizedPseudoTimeRange(original, project, pseudoTimeEnd, 501)
	if minimum.EndMS-original.StartMS < 40-1e-9 {
		t.Fatalf("minimum project time span was not enforced: %+v", minimum)
	}
}

func TestPhase11UIRoutesTimeRangeWithoutChangingViewJSON(t *testing.T) {
	readSource := func(name string) string {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	source := readSource("crooked_windows.go")
	for _, required := range []string{"IDCROOKED_TIMERANGE", "beginCrookedPseudoTimeRangeMode", "commitCrookedPseudoTimeRange", "reloadPseudoForCrookedTimeRange"} {
		if !bytes.Contains([]byte(source), []byte(required)) {
			t.Fatalf("missing Phase 11 crooked UI route %q", required)
		}
	}
	pseudoSource := readSource("pseudo_windows.go")
	if bytes.Contains([]byte(pseudoSource), []byte("SampleStart: 0, SampleEnd: job.line.Dataset.Metadata.SamplesPerTrace - 1")) {
		t.Fatal("pseudo loader still hardcodes the complete sample range")
	}
}
