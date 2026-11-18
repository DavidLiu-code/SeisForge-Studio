//go:build windows

package main

import (
	"bytes"
	"math"
	"path/filepath"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func compareFloatBits(values []float64) []uint64 {
	out := make([]uint64, len(values))
	for i, value := range values {
		out[i] = math.Float64bits(value)
	}
	return out
}

func assertCompareFloatBits(t *testing.T, values []float64, want []uint64) {
	t.Helper()
	for i, value := range values {
		if math.Float64bits(value) != want[i] {
			t.Fatalf("display mapping changed raw value at %d", i)
		}
	}
}

func TestPrestackCompareGainChangesPixelsWithoutChangingFloatValues(t *testing.T) {
	a := []float64{-100, -4, -3, -2, -1, 0, 1, 2, 3, 4, 100, math.NaN()}
	b := []float64{-50, -2, -1.5, -1, -.5, 0, .5, 1, 1.5, 2, 50, math.Inf(1)}
	aBits, bBits := compareFloatBits(a), compareFloatBits(b)
	lo0, hi0 := prestackCompareSharedDisplayRange(a, b, 0, false, 0, 0)
	lo20, hi20 := prestackCompareSharedDisplayRange(a, b, 20, false, 0, 0)
	if lo0 != -100 || hi0 != 100 || lo20 <= lo0 || hi20 >= hi0 {
		t.Fatalf("gain did not trim pooled range: %g..%g -> %g..%g", lo0, hi0, lo20, hi20)
	}
	if bytes.Equal(prestackCompareValuesBGRA(a, lo0, hi0, 2), prestackCompareValuesBGRA(a, lo20, hi20, 2)) {
		t.Fatal("gain changed state but not A pixels")
	}
	if bytes.Equal(prestackCompareValuesBGRA(b, lo0, hi0, 2), prestackCompareValuesBGRA(b, lo20, hi20, 2)) {
		t.Fatal("gain changed state but not B pixels")
	}
	pa := prestackCompareValuesBGRA(a, lo20, hi20, 2)
	pb := prestackCompareValuesBGRA(b, lo20, hi20, 2)
	if !bytes.Equal(pa[5*4:6*4], pb[5*4:6*4]) {
		t.Fatal("identical A/B value must use the same palette mapping")
	}
	if !bytes.Equal(pa[11*4:12*4], []byte{255, 255, 255, 255}) || !bytes.Equal(pb[11*4:12*4], []byte{255, 255, 255, 255}) {
		t.Fatal("nonfinite slots must stay blank and not affect percentile range")
	}
	assertCompareFloatBits(t, a, aBits)
	assertCompareFloatBits(t, b, bBits)

	difference := []float64{-100, -4, -3, -2, -1, 0, 1, 2, 3, 4, 5, math.NaN()}
	dBits := compareFloatBits(difference)
	lo0, hi0 = prestackCompareDeltaDisplayRange(difference, 0, false, 0, 0)
	lo20, hi20 = prestackCompareDeltaDisplayRange(difference, 20, false, 0, 0)
	if lo0 != -hi0 || lo20 != -hi20 || hi20 >= hi0 || hi20 <= 0 {
		t.Fatalf("Delta gain lost symmetry or percentile clipping: %g..%g -> %g..%g", lo0, hi0, lo20, hi20)
	}
	if bytes.Equal(prestackCompareValuesBGRA(difference, lo0, hi0, 2), prestackCompareValuesBGRA(difference, lo20, hi20, 2)) {
		t.Fatal("gain did not change Delta pixels")
	}
	assertCompareFloatBits(t, difference, dBits)
}

func TestPrestackCompareManualRangeOverridesGain(t *testing.T) {
	values := []float64{-100, 0, 100}
	for _, gain := range []float64{0, 10, 49, math.NaN()} {
		lo, hi := prestackCompareSharedDisplayRange(values, values, gain, true, -2, 5)
		if lo != -2 || hi != 5 {
			t.Fatalf("manual A/B range changed with gain %g: %g..%g", gain, lo, hi)
		}
		lo, hi = prestackCompareDeltaDisplayRange(values, gain, true, -2, 5)
		if lo != -5 || hi != 5 {
			t.Fatalf("manual Delta range must be zero symmetric: %g..%g", lo, hi)
		}
	}
	for _, values := range [][]float64{nil, {math.NaN(), math.Inf(-1)}, {0, 0}} {
		lo, hi := prestackCompareDeltaDisplayRange(values, 49, false, 0, 0)
		if lo != -1 || hi != 1 {
			t.Fatalf("empty/zero Delta did not use safe range: %g..%g", lo, hi)
		}
	}
}

func TestPrestackCompareDisplayRangeMatchesExistingReaderMapping(t *testing.T) {
	dir := t.TempDir()
	open := func(name string, scale float32) *segy.File {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := testsegy.Write(path, testsegy.Options{Cols: 2, Samples: 100, SampleValue: func(trace, sample int) float32 {
			return float32(sample-50) * scale
		}}); err != nil {
			t.Fatal(err)
		}
		r, err := segy.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		return r
	}
	a, b := open("A.sgy", 1), open("B.sgy", .5)
	traces := []int64{0, 1}
	for _, gain := range []float64{-10, 0, 1, 10, 49, 70} {
		for _, manual := range []bool{false, true} {
			options := segy.RenderOptions{Width: 2, Height: 100, SampleStart: 0, SampleEnd: 99, DisplayMode: segy.DisplayNearest,
				GainPercent: gain, ClipPercent: 99, UseValueLimits: manual, MinValue: -10, MaxValue: 20}
			values, _, err := a.RenderTraceIndicesValues(traces, options)
			if err != nil {
				t.Fatal(err)
			}
			wantPixels, stats, err := a.RenderTraceIndices(traces, options)
			if err != nil {
				t.Fatal(err)
			}
			lo, hi := prestackCompareSharedDisplayRange(values, nil, gain, manual, options.MinValue, options.MaxValue)
			if lo != stats.MapMin || hi != stats.MapMax || !bytes.Equal(wantPixels, segy.MapAmplitudeValues(values, lo, hi)) {
				t.Fatalf("A/B mapping differs from existing renderer: gain=%g manual=%v range=%g..%g stats=%g..%g", gain, manual, lo, hi, stats.MapMin, stats.MapMax)
			}
			wantDelta, stats, err := segy.RenderTraceDifferencePairs(a, b, traces, traces, options)
			if err != nil {
				t.Fatal(err)
			}
			difference := make([]float64, len(values))
			for i, value := range values {
				difference[i] = value * .5
			}
			lo, hi = prestackCompareDeltaDisplayRange(difference, gain, manual, options.MinValue, options.MaxValue)
			if lo != stats.MapMin || hi != stats.MapMax || !bytes.Equal(wantDelta, segy.MapAmplitudeValues(difference, lo, hi)) {
				t.Fatalf("Delta mapping differs from existing renderer: gain=%g manual=%v range=%g..%g stats=%g..%g", gain, manual, lo, hi, stats.MapMin, stats.MapMax)
			}
		}
	}
}
