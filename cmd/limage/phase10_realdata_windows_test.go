//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	pseudocachecore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudocache"
)

func TestPhase10ProvidedSu36AdaptiveReadersAndPersistentCache(t *testing.T) {
	if os.Getenv("LIMAGE_PHASE10_FULL_ACCEPTANCE") != "1" {
		t.Skip("set LIMAGE_PHASE10_FULL_ACCEPTANCE=1 for Phase 10 performance acceptance")
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
	requests := make([]pseudo3dcore.TextureRequest, 0, len(project.Lines))
	for _, line := range project.Lines {
		requests = append(requests, pseudo3dcore.TextureRequest{ID: line.ID, Traces: int(line.Dataset.Metadata.TraceCount), Samples: line.Dataset.Metadata.SamplesPerTrace})
	}
	plans := pseudo3dcore.PlanTextureSizes(requests, pseudo3dcore.TextureBudget)
	coldCache, err := pseudocachecore.New(filepath.Join(t.TempDir(), "empty-cache"), pseudocachecore.DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	cold, coldDuration := phase9LoadProvidedProject(t, project, plans, coldCache, pseudoLineWorkerCount())
	coldHits, blocks, bytesRead, textureBytes := 0, 0, int64(0), int64(0)
	for _, result := range cold {
		if result.cacheHit {
			coldHits++
		}
		blocks += result.ioReadCalls
		bytesRead += result.ioReadBytes
		if result.texture != nil {
			textureBytes += int64(len(result.texture.Indices))
		}
	}
	if coldHits != 0 || len(cold) != 62 {
		t.Fatalf("cold acceptance did not process 62 uncached lines: lines=%d hits=%d", len(cold), coldHits)
	}
	for _, result := range cold {
		if result.cacheWrite == nil {
			t.Fatal("cold result omitted its deferred persistent-cache write")
		}
		if err := coldCache.Store(result.cacheWrite.key, result.cacheWrite.entry); err != nil {
			t.Fatal(err)
		}
	}
	warm, warmDuration := phase9LoadProvidedProject(t, project, plans, coldCache, pseudoLineWorkerCount())
	warmHits := 0
	for _, result := range warm {
		if result.cacheHit && result.ioReadCalls == 0 {
			warmHits++
		}
	}
	t.Logf("actual-log Phase8=155.591s Phase9=119.926s; Phase10 uncached=%s blocks=%d bytes=%d textures=%d readers=%d; persistent-cache=%s hits=%d/62",
		coldDuration, blocks, bytesRead, textureBytes, pseudoLineWorkerCount(), warmDuration, warmHits)
	if coldDuration > 2500*time.Millisecond {
		t.Fatalf("Phase 10 uncached load %s exceeds 2.5s warm-filesystem target", coldDuration)
	}
	if warmHits != 62 || warmDuration > 1500*time.Millisecond {
		t.Fatalf("installed 62/62 cache load %s exceeds 1.5s target", warmDuration)
	}
}
