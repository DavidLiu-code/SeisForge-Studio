//go:build windows

package main

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

type phase11AcceptanceMetrics struct {
	duration         time.Duration
	readBytes        int64
	textureBytes     int64
	lines            int
	inputTraces      int64
	supportTraces    int64
	mappedSegments   int
	fallbackSegments int
}

func phase11LoadProvidedTimeWindow(t *testing.T, project *projectcore.CrookedProject, fraction float64, strategy segy.RenderReadStrategy) phase11AcceptanceMetrics {
	t.Helper()
	atomic.StoreInt64(&pseudoActiveRangeGen, 0)
	atomic.StoreInt64(&pseudoActiveTimeGen, 0)

	maximumMS := 0.0
	for _, line := range project.Lines {
		metadata := line.Dataset.Metadata
		lineMaximum := float64((metadata.SamplesPerTrace-1)*metadata.SampleIntervalUS) / 1000
		if lineMaximum > maximumMS {
			maximumMS = lineMaximum
		}
	}
	selected := pseudo3dcore.TimeRange{}
	if fraction < 1 {
		selected = pseudo3dcore.TimeRange{StartMS: 0, EndMS: maximumMS * fraction, Valid: true}
	}

	type preparedLine struct {
		start, end int
		actual     pseudo3dcore.TimeRange
	}
	prepared := make([]preparedLine, len(project.Lines))
	requests := make([]pseudo3dcore.TextureRequest, 0, len(project.Lines))
	fullSamples := make(map[string]int, len(project.Lines))
	for index, line := range project.Lines {
		start, end, actual, ok := pseudoLineTimeWindow(line, selected)
		if !ok {
			t.Fatalf("line %s has no overlap with %.0f%% time window", line.Name, fraction*100)
		}
		prepared[index] = preparedLine{start: start, end: end, actual: actual}
		requests = append(requests, pseudo3dcore.TextureRequest{ID: line.ID, Traces: int(line.Dataset.Metadata.TraceCount), Samples: end - start + 1})
		fullSamples[line.ID] = line.Dataset.Metadata.SamplesPerTrace
	}
	plans := pseudo3dcore.PlanTimeWindowTextureSizes(requests, fullSamples, pseudo3dcore.TextureBudget)
	results := make([]*pseudoLoadResult, len(project.Lines))
	spec := segy.TraceCoordinateSpec{Source: segy.CoordinateAuto, CDPByte: 21, ScalarByte: 71, UnitsByte: 89}
	startTime := time.Now()

	bootstrap := 0
	for index, line := range project.Lines {
		if line.Name == "00551-280" {
			bootstrap = index
			break
		}
	}
	makeJob := func(index int, multiplier float64) pseudoLoadJob {
		line := project.Lines[index]
		window := prepared[index]
		return pseudoLoadJob{windowGen: 1, lineIndex: index, token: 1, line: line, spec: spec,
			size: plans[line.ID], projectMultiplier: multiplier, gain: 0, clip: 99, displayMode: segy.DisplayAdaptive,
			readStrategy: strategy,
			timeRange:    selected, sampleStart: window.start, sampleEnd: window.end,
			timeStartMS: window.actual.StartMS, timeEndMS: window.actual.EndMS}
	}
	results[bootstrap] = performPseudoLoad(makeJob(bootstrap, 0))
	if results[bootstrap] == nil || results[bootstrap].err != nil {
		t.Fatalf("bootstrap line failed: %+v", results[bootstrap])
	}
	multiplier := results[bootstrap].multiplier

	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < pseudoLineWorkerCount(); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = performPseudoLoad(makeJob(index, multiplier))
			}
		}()
	}
	for index := range project.Lines {
		if index != bootstrap {
			jobs <- index
		}
	}
	close(jobs)
	workers.Wait()

	metrics := phase11AcceptanceMetrics{lines: len(results)}
	curtains := make([]pseudo3dcore.Curtain, 0, len(results))
	for index, result := range results {
		if result == nil || result.err != nil || result.texture == nil {
			t.Fatalf("line %s failed: %+v", project.Lines[index].Name, result)
		}
		metrics.readBytes += result.ioReadBytes
		metrics.inputTraces += int64(result.inputTraceCount)
		metrics.supportTraces += int64(result.supportTraceCount)
		metrics.mappedSegments += result.mappedSegments
		metrics.fallbackSegments += result.fallbackSegments
		metrics.textureBytes += int64(len(result.texture.Indices))
		curtains = append(curtains, pseudo3dcore.Curtain{ID: project.Lines[index].ID, Name: project.Lines[index].Name,
			Points: result.points, U: result.u, TimeStartMS: prepared[index].actual.StartMS, TimeEndMS: prepared[index].actual.EndMS, Texture: result.texture})
	}
	if _, stats, err := pseudo3dcore.Render(pseudo3dcore.Scene{Curtains: curtains}, pseudo3dcore.DefaultCamera(), 1000, 700, pseudoPalette(2)); err != nil || stats.TexturedCurtains != len(results) {
		t.Fatalf("final render failed: stats=%+v err=%v", stats, err)
	}
	metrics.duration = time.Since(startTime)
	return metrics
}

func TestPhase11ProvidedSu36FullHalfQuarterTimeWindows(t *testing.T) {
	if os.Getenv("LIMAGE_PHASE11_FULL_ACCEPTANCE") != "1" {
		t.Skip("set LIMAGE_PHASE11_FULL_ACCEPTANCE=1 for Phase 11 time-window acceptance")
	}
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
	if project.ValidLineCount() != 62 {
		t.Fatalf("project has %d valid lines, want 62", project.ValidLineCount())
	}

	full := phase11LoadProvidedTimeWindow(t, project, 1, segy.ReadStrategySparseMapped)
	half := phase11LoadProvidedTimeWindow(t, project, .5, segy.ReadStrategySparseMapped)
	quarter := phase11LoadProvidedTimeWindow(t, project, .25, segy.ReadStrategySparseMapped)
	t.Logf("Phase11 62 lines: full=%s bytes=%d textures=%d support=%d/%d mapped=%d fallback=%d; 50%%=%s bytes=%d textures=%d; 25%%=%s bytes=%d textures=%d",
		full.duration, full.readBytes, full.textureBytes, full.supportTraces, full.inputTraces, full.mappedSegments, full.fallbackSegments,
		half.duration, half.readBytes, half.textureBytes,
		quarter.duration, quarter.readBytes, quarter.textureBytes)
	if quarter.lines != 62 || half.lines != 62 || full.lines != 62 {
		t.Fatalf("unexpected processed line count: full=%d half=%d quarter=%d", full.lines, half.lines, quarter.lines)
	}
	if quarter.readBytes*100 > full.readBytes*35 {
		t.Fatalf("25%% time window read %d bytes, more than 35%% of full %d", quarter.readBytes, full.readBytes)
	}
	if quarter.textureBytes*100 > full.textureBytes*35 {
		t.Fatalf("25%% time window retained %d texture bytes, more than 35%% of full %d", quarter.textureBytes, full.textureBytes)
	}
	if quarter.duration >= full.duration {
		t.Fatalf("25%% time window %s was not faster than full window %s", quarter.duration, full.duration)
	}
}
