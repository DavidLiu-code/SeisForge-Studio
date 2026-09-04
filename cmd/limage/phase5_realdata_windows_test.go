//go:build windows

package main

import (
	"math"
	"os"
	"sync"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func TestProvidedSu36PseudoAOIClipsBeforeAmplitudeLoad(t *testing.T) {
	if os.Getenv("LIMAGE_PHASE5_FULL_ACCEPTANCE") != "1" {
		t.Skip("set LIMAGE_PHASE5_FULL_ACCEPTANCE=1 for the Phase 5 disk acceptance")
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
	xMin, xMax, yMin, yMax := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, line := range project.Lines {
		for _, point := range line.Navigation {
			xMin, xMax = math.Min(xMin, point.X), math.Max(xMax, point.X)
			yMin, yMax = math.Min(yMin, point.Y), math.Max(yMax, point.Y)
		}
	}
	selected := pseudo3dcore.XYRange{XMin: xMin + (xMax-xMin)*.45, XMax: xMin + (xMax-xMin)*.55,
		YMin: yMin + (yMax-yMin)*.45, YMax: yMin + (yMax-yMin)*.55, Valid: true}
	candidates := make([]int, 0, len(project.Lines))
	requests := make([]pseudo3dcore.TextureRequest, 0, len(project.Lines))
	for index, line := range project.Lines {
		points := make([]pseudo3dcore.Point, len(line.Navigation))
		for i, point := range line.Navigation {
			points[i] = pseudo3dcore.Point{X: point.X, Y: point.Y}
		}
		if pseudo3dcore.PolylineIntersectsRange(points, selected) {
			candidates = append(candidates, index)
			requests = append(requests, pseudo3dcore.TextureRequest{ID: line.ID, Traces: int(line.Dataset.Metadata.TraceCount), Samples: line.Dataset.Metadata.SamplesPerTrace})
		}
	}
	if len(candidates) == 0 || len(candidates) >= len(project.Lines) {
		t.Fatalf("central AOI did not reduce the project: candidates=%d total=%d", len(candidates), len(project.Lines))
	}
	plans := pseudo3dcore.PlanTextureSizes(requests, pseudo3dcore.TextureBudget)
	results := make([]*pseudoLoadResult, len(project.Lines))
	first := candidates[0]
	firstLine := project.Lines[first]
	results[first] = performPseudoLoad(pseudoLoadJob{windowGen: 1, lineIndex: first, token: 1, line: firstLine,
		spec: segy.TraceCoordinateSpec{Source: segy.CoordinateAuto, CDPByte: 21, ScalarByte: 71, UnitsByte: 89}, size: plans[firstLine.ID],
		gain: 0, clip: 99, displayMode: segy.DisplayAdaptive, spatialRange: selected})
	if results[first].err != nil || results[first].outOfRange || results[first].multiplier != .1 {
		t.Fatalf("AOI bootstrap failed: %+v", results[first])
	}
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
					projectMultiplier: .1, gain: 0, clip: 99, displayMode: segy.DisplayAdaptive, spatialRange: selected})
			}
		}()
	}
	for _, index := range candidates[1:] {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	curtains := make([]pseudo3dcore.Curtain, 0, len(candidates))
	var textureBytes int64
	for _, index := range candidates {
		result := results[index]
		if result == nil || result.err != nil || result.outOfRange || result.multiplier != .1 {
			t.Fatalf("candidate %s failed: %+v", project.Lines[index].Name, result)
		}
		for segmentIndex, segment := range result.segments {
			for _, point := range segment.points {
				if !selected.Contains(point) {
					t.Fatalf("%s segment %d escaped AOI: %+v", project.Lines[index].Name, segmentIndex, point)
				}
			}
			textureBytes += int64(len(segment.texture.Indices))
			curtains = append(curtains, pseudo3dcore.Curtain{ID: project.Lines[index].ID, Name: project.Lines[index].Name,
				Points: segment.points, U: segment.u, TimeMaxMS: float64((project.Lines[index].Dataset.Metadata.SamplesPerTrace-1)*project.Lines[index].Dataset.Metadata.SampleIntervalUS) / 1000,
				Texture: segment.texture})
		}
	}
	if textureBytes > pseudo3dcore.TextureBudget {
		t.Fatalf("AOI textures exceed budget: %d", textureBytes)
	}
	image, stats, err := pseudo3dcore.Render(pseudo3dcore.Scene{Curtains: curtains,
		Bounds: pseudo3dcore.Bounds{XMin: selected.XMin, XMax: selected.XMax, YMin: selected.YMin, YMax: selected.YMax, TimeMaxMS: 5000, Valid: true}},
		pseudo3dcore.DefaultCamera(), 900, 650, pseudoPalette(2))
	if err != nil || len(image) != 900*650*4 || stats.TexturedCurtains < len(candidates) || stats.Pixels == 0 {
		t.Fatalf("AOI scene mismatch: image=%d candidates=%d stats=%+v err=%v", len(image), len(candidates), stats, err)
	}
	t.Logf("central AOI loaded %d/%d lines as %d clipped curtain segments using %d bytes", len(candidates), len(project.Lines), len(curtains), textureBytes)
}
