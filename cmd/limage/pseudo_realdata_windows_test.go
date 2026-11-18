//go:build windows

package main

import (
	"os"
	"sync"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// This acceptance test intentionally reads every provided SEG-Y and is gated
// from ordinary unit-test runs. Set LIMAGE_PHASE4_FULL_ACCEPTANCE=1 to run it.
func TestProvidedSu36Pseudo3DAllCurtains(t *testing.T) {
	if os.Getenv("LIMAGE_PHASE4_FULL_ACCEPTANCE") != "1" {
		t.Skip("set LIMAGE_PHASE4_FULL_ACCEPTANCE=1 for the 62-line disk acceptance")
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
	if project.ValidLineCount() != 62 || project.NavigationLineCount() != 62 {
		t.Fatalf("project mismatch: valid=%d navigation=%d", project.ValidLineCount(), project.NavigationLineCount())
	}
	requests := make([]pseudo3dcore.TextureRequest, 0, len(project.Lines))
	for _, line := range project.Lines {
		requests = append(requests, pseudo3dcore.TextureRequest{ID: line.ID, Traces: int(line.Dataset.Metadata.TraceCount), Samples: line.Dataset.Metadata.SamplesPerTrace})
	}
	plans := pseudo3dcore.PlanTextureSizes(requests, pseudo3dcore.TextureBudget)
	results := make([]*pseudoLoadResult, len(project.Lines))
	bootstrap := 0
	for i, line := range project.Lines {
		if line.Name == "00551-280" {
			bootstrap = i
			break
		}
	}
	bootstrapLine := project.Lines[bootstrap]
	results[bootstrap] = performPseudoLoad(pseudoLoadJob{windowGen: 1, lineIndex: bootstrap, token: 1, line: bootstrapLine,
		spec: segy.TraceCoordinateSpec{Source: segy.CoordinateAuto, CDPByte: 21, ScalarByte: 71, UnitsByte: 89}, size: plans[bootstrapLine.ID],
		gain: 0, clip: 99, displayMode: segy.DisplayAdaptive})
	if results[bootstrap].err != nil || !results[bootstrap].calibrationAccepted || results[bootstrap].multiplier != .1 {
		t.Fatalf("project multiplier bootstrap failed: %+v", results[bootstrap])
	}
	projectMultiplier := results[bootstrap].multiplier
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < 2; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				line := project.Lines[index]
				results[index] = performPseudoLoad(pseudoLoadJob{windowGen: 1, lineIndex: index, token: 1, line: line,
					spec: segy.TraceCoordinateSpec{Source: segy.CoordinateAuto, CDPByte: 21, ScalarByte: 71, UnitsByte: 89}, size: plans[line.ID],
					projectMultiplier: projectMultiplier, gain: 0, clip: 99, displayMode: segy.DisplayAdaptive})
			}
		}()
	}
	for i := range project.Lines {
		if i != bootstrap {
			jobs <- i
		}
	}
	close(jobs)
	workers.Wait()

	curtains := make([]pseudo3dcore.Curtain, 0, len(results))
	var textureBytes int64
	for i, result := range results {
		if result == nil || result.err != nil {
			if result == nil {
				t.Fatalf("line %d returned no result", i)
			}
			t.Fatalf("line %s failed: %v", project.Lines[i].Name, result.err)
		}
		if result.multiplier != .1 {
			t.Fatalf("line %s multiplier=%g want=0.1", project.Lines[i].Name, result.multiplier)
		}
		textureBytes += int64(len(result.texture.Indices))
		line := project.Lines[i]
		timeMax := float64((line.Dataset.Metadata.SamplesPerTrace-1)*line.Dataset.Metadata.SampleIntervalUS) / 1000
		curtains = append(curtains, pseudo3dcore.Curtain{ID: line.ID, Name: line.Name, Points: result.points, U: result.u, TimeMaxMS: timeMax, Texture: result.texture})
	}
	if textureBytes > pseudo3dcore.TextureBudget {
		t.Fatalf("retained texture bytes=%d exceed budget=%d", textureBytes, pseudo3dcore.TextureBudget)
	}
	image, stats, err := pseudo3dcore.Render(pseudo3dcore.Scene{Curtains: curtains}, pseudo3dcore.DefaultCamera(), 1000, 700, pseudoPalette(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(image) != 1000*700*4 || stats.TexturedCurtains != 62 || stats.Pixels == 0 {
		t.Fatalf("full project scene mismatch: image=%d stats=%+v", len(image), stats)
	}
}
