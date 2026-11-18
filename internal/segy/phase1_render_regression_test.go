package segy

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestPhase1RenderStatsAndPixelsFrozen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "render-baseline.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 8, Cols: 8, Samples: 12, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pixels, stats, err := f.RenderWithOptions(RenderOptions{
		Width: 40, Height: 30, AGC: false, ClipPercent: 99, GainPercent: 0,
		TraceStart: 3, TraceEnd: 58, TraceStep: 2, SampleStart: 2, SampleEnd: 10,
		DisplayMode: DisplayAdaptive, Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(pixels))
	const expectedHash = "8d0768a086935b92cc8675f83a275253d13b3ee35415e32783f7f51149fa2932"
	if hash != expectedHash {
		t.Fatalf("render pixels changed: hash=%s stats=%+v", hash, stats)
	}
	wantStats := RenderStats{ObservedMin: 302, ObservedMax: 5710, MapMin: 302, MapMax: 5710, TraceStart: 3, TraceEnd: 57, TraceStep: 2, SampleStart: 2, SampleEnd: 10}
	if stats != wantStats {
		t.Fatalf("render stats changed:\nwant=%+v\n got=%+v", wantStats, stats)
	}
}
