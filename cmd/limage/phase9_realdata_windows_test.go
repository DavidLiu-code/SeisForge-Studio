//go:build windows

package main

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	pseudocachecore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudocache"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func phase9LoadProvidedProject(t *testing.T, project *projectcore.CrookedProject, plans map[string]pseudo3dcore.TextureSize, cache *pseudocachecore.Cache, lineWorkers int) ([]*pseudoLoadResult, time.Duration) {
	t.Helper()
	atomic.StoreInt64(&pseudoActiveRangeGen, 0)
	results := make([]*pseudoLoadResult, len(project.Lines))
	spec := segy.TraceCoordinateSpec{Source: segy.CoordinateAuto, CDPByte: 21, ScalarByte: 71, UnitsByte: 89}
	started := time.Now()
	bootstrap := 0
	for i, line := range project.Lines {
		if line.Name == "00551-280" {
			bootstrap = i
			break
		}
	}
	line := project.Lines[bootstrap]
	results[bootstrap] = performPseudoLoad(pseudoLoadJob{windowGen: 1, lineIndex: bootstrap, token: 1, line: line,
		spec: spec, size: plans[line.ID], gain: 0, clip: 99, displayMode: segy.DisplayAdaptive, textureCache: cache})
	if results[bootstrap].err != nil || results[bootstrap].multiplier != .1 {
		t.Fatalf("phase9 bootstrap failed: %+v", results[bootstrap])
	}
	multiplier := results[bootstrap].multiplier
	jobs := make(chan int)
	var workers sync.WaitGroup
	if lineWorkers < 1 {
		lineWorkers = 2
	}
	for worker := 0; worker < lineWorkers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				line := project.Lines[index]
				results[index] = performPseudoLoad(pseudoLoadJob{windowGen: 1, lineIndex: index, token: 1, line: line,
					spec: spec, size: plans[line.ID], projectMultiplier: multiplier, gain: 0, clip: 99,
					displayMode: segy.DisplayAdaptive, textureCache: cache})
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
	curtains := make([]pseudo3dcore.Curtain, 0, len(results))
	for index, result := range results {
		if result == nil || result.err != nil || result.texture == nil {
			t.Fatalf("phase9 line %s failed: %+v", project.Lines[index].Name, result)
		}
		line := project.Lines[index]
		timeMax := float64((line.Dataset.Metadata.SamplesPerTrace-1)*line.Dataset.Metadata.SampleIntervalUS) / 1000
		curtains = append(curtains, pseudo3dcore.Curtain{ID: line.ID, Name: line.Name, Points: result.points, U: result.u, TimeMaxMS: timeMax, Texture: result.texture})
	}
	if _, stats, err := pseudo3dcore.Render(pseudo3dcore.Scene{Curtains: curtains}, pseudo3dcore.DefaultCamera(), 1000, 700, pseudoPalette(2)); err != nil || stats.TexturedCurtains != 62 {
		t.Fatalf("phase9 final render failed: stats=%+v err=%v", stats, err)
	}
	return results, time.Since(started)
}

func TestPhase9ProvidedSu36ColdAndWarmTargets(t *testing.T) {
	if os.Getenv("LIMAGE_PHASE9_FULL_ACCEPTANCE") != "1" {
		t.Skip("set LIMAGE_PHASE9_FULL_ACCEPTANCE=1 for Phase 9 cold/warm performance acceptance")
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
	requests := make([]pseudo3dcore.TextureRequest, 0, len(project.Lines))
	for _, line := range project.Lines {
		requests = append(requests, pseudo3dcore.TextureRequest{ID: line.ID, Traces: int(line.Dataset.Metadata.TraceCount), Samples: line.Dataset.Metadata.SamplesPerTrace})
	}
	plans := pseudo3dcore.PlanTextureSizes(requests, pseudo3dcore.TextureBudget)
	cache, err := pseudocachecore.New(filepath.Join(t.TempDir(), "pseudo-textures"), pseudocachecore.DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	cold, coldDuration := phase9LoadProvidedProject(t, project, plans, cache, 2)
	for _, result := range cold {
		if result.cacheHit || result.cacheWrite == nil {
			t.Fatalf("cold result unexpectedly hit cache or omitted deferred write")
		}
		if err := cache.Store(result.cacheWrite.key, result.cacheWrite.entry); err != nil {
			t.Fatal(err)
		}
	}
	warm, warmDuration := phase9LoadProvidedProject(t, project, plans, cache, 2)
	hits := 0
	for _, result := range warm {
		if result.cacheHit && result.ioReadCalls == 0 {
			hits++
		}
	}
	t.Logf("Phase 8 reference median=4.651s Phase 9 cold=%s warm=%s hits=%d/62", coldDuration, warmDuration, hits)
	if coldDuration > 3250*time.Millisecond {
		t.Fatalf("cold Phase 9 load %s exceeds 3.25s target", coldDuration)
	}
	if warmDuration > 1500*time.Millisecond || hits != 62 {
		t.Fatalf("warm Phase 9 load %s hits=%d exceeds 1.5s/62-hit target", warmDuration, hits)
	}
}
