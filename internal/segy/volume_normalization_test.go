package segy

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestRenderTraceIndicesValuesPreservesLegacyPixels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "volume-values.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 7, Cols: 9, Samples: 48, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	traces := []int64{2, 8, 14, 20, 26, 32, 38, 44, 50, 56}
	for _, options := range []RenderOptions{
		{Width: 73, Height: 67, SampleStart: 1, SampleEnd: 45, GainPercent: 0, ClipPercent: 99, DisplayMode: DisplayNearest, Workers: 2},
		{Width: 83, Height: 71, SampleStart: 2, SampleEnd: 43, GainPercent: 17, ClipPercent: 99, DisplayMode: DisplaySmooth, Workers: 4},
		{Width: 79, Height: 69, SampleStart: 0, SampleEnd: 47, GainPercent: 9, ClipPercent: 99, AGC: true, DisplayMode: DisplayAdaptive, Workers: 2},
	} {
		legacy, legacyStats, err := file.RenderTraceIndices(traces, options)
		if err != nil {
			t.Fatal(err)
		}
		values, valueStats, err := file.RenderTraceIndicesValues(traces, options)
		if err != nil {
			t.Fatal(err)
		}
		mapped := MapAmplitudeValues(values, legacyStats.MapMin, legacyStats.MapMax)
		if !bytes.Equal(mapped, legacy) {
			t.Fatalf("shared-range mapping changed trace-index pixels for %+v", options)
		}
		if valueStats.ObservedMin != legacyStats.ObservedMin || valueStats.ObservedMax != legacyStats.ObservedMax ||
			valueStats.TraceStart != legacyStats.TraceStart || valueStats.TraceEnd != legacyStats.TraceEnd ||
			valueStats.SampleStart != legacyStats.SampleStart || valueStats.SampleEnd != legacyStats.SampleEnd {
			t.Fatalf("value-render statistics diverged:\nlegacy=%+v\nvalues=%+v", legacyStats, valueStats)
		}
	}
}

func TestMapAmplitudeValuesPlacesZeroAtSharedMidpoint(t *testing.T) {
	pixels := MapAmplitudeValues([]float64{-10, 0, 10}, -10, 10)
	if len(pixels) != 3 || pixels[0] != 255 || pixels[1] != 128 || pixels[2] != 0 {
		t.Fatalf("unexpected shared normalization indices: %v", pixels)
	}
}
