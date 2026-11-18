//go:build windows

package main

import (
	"math"
	"os"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func TestVolumeSharedSliceRangeUsesOneRangeAcrossOrthogonalSlices(t *testing.T) {
	inline := []float64{0, -0.02, -0.01, -0.015}
	crossline := []float64{-2, -1, 1, 2}
	timeValues := []float32{-1.5, 0, 1.5, 99}
	timeMask := []bool{true, true, true, false}
	lo, hi := volumeSharedSliceRange(inline, crossline, timeValues, timeMask, 0, 100, false, -1, 1)
	if lo != -2 || hi != 2 {
		t.Fatalf("unexpected shared range: %g .. %g", lo, hi)
	}
	pixels := segy.MapAmplitudeValues(inline, lo, hi)
	if pixels[0] != 128 {
		t.Fatalf("zero was not mapped to the shared midpoint: %v", pixels)
	}
	for _, values := range [][]float64{inline, crossline} {
		mapped := segy.MapAmplitudeValues(values, lo, hi)
		if len(mapped) != len(values) {
			t.Fatalf("shared mapping size changed: %d/%d", len(mapped), len(values))
		}
	}
}

func TestVolumeSharedSliceRangeManualAndGainRules(t *testing.T) {
	if lo, hi := volumeSharedSliceRange([]float64{-100, -10, 0, 10, 100}, nil, nil, nil, 20, 99, true, -3, 7); lo != -3 || hi != 7 {
		t.Fatalf("manual limits were not authoritative: %g .. %g", lo, hi)
	}
	values := make([]float64, 101)
	for index := range values {
		values[index] = float64(index - 50)
	}
	lo, hi := volumeSharedSliceRange(values, nil, nil, nil, 10, 100, false, 0, 0)
	if lo != -40 || hi != 40 {
		t.Fatalf("shared gain percentile changed: %g .. %g", lo, hi)
	}
	lo, hi = volumeSharedSliceRange([]float64{0, 0}, nil, nil, nil, 0, 100, false, 0, 0)
	if !(lo < 0 && hi > 0 && math.Abs(lo+hi) < 1e-12) {
		t.Fatalf("constant zero range was not expanded around zero: %g .. %g", lo, hi)
	}
	values = make([]float64, 1001)
	values[0] = -999999
	for index := 1; index < len(values); index++ {
		values[index] = float64(index%101 - 50)
	}
	lo, hi = volumeSharedSliceRange(values, nil, nil, nil, 0, 99, false, 0, 0)
	if lo <= -999999 || lo < -50 || hi > 50 {
		t.Fatalf("clip 99 did not reject the no-data endpoint: %g .. %g", lo, hi)
	}
}

func TestVolumeSharedNormalizationRemapsAllThreePanels(t *testing.T) {
	oldInline, oldCross := volumeInlineValues, volumeCrosslineValues
	oldTime, oldMask := volumeTimeRaster, volumeTimeMask
	oldInlinePanel, oldCrossPanel, oldTimePanel := volumeInline, volumeCrossline, volumeTime
	oldMin, oldMax := volumeTimeMin, volumeTimeMax
	oldGain, oldClip, oldUse, oldLimitMin, oldLimitMax := gainPercent, clipPercent, useLimits, limitMin, limitMax
	oldPalette := volumePaletteIndex
	t.Cleanup(func() {
		volumeInlineValues, volumeCrosslineValues = oldInline, oldCross
		volumeTimeRaster, volumeTimeMask = oldTime, oldMask
		volumeInline, volumeCrossline, volumeTime = oldInlinePanel, oldCrossPanel, oldTimePanel
		volumeTimeMin, volumeTimeMax = oldMin, oldMax
		gainPercent, clipPercent, useLimits, limitMin, limitMax = oldGain, oldClip, oldUse, oldLimitMin, oldLimitMax
		volumePaletteIndex = oldPalette
	})

	volumeInlineValues = []float64{0, -0.02, -0.01, -0.015}
	volumeCrosslineValues = []float64{-2, -1, 1, 2}
	volumeTimeRaster = []float32{-1.5, 0, 1.5, 99}
	volumeTimeMask = []bool{true, true, true, false}
	volumeInline = comparePanel{w: 2, h: 2}
	volumeCrossline = comparePanel{w: 2, h: 2}
	volumeTime = comparePanel{w: 2, h: 2}
	gainPercent, clipPercent, useLimits, limitMin, limitMax = 0, 100, false, -1, 1
	volumePaletteIndex = 2

	remapVolumeSharedNormalization()
	for name, panel := range map[string]comparePanel{"inline": volumeInline, "crossline": volumeCrossline, "time": volumeTime} {
		if panel.stats.MapMin != -2 || panel.stats.MapMax != 2 || len(panel.bgra) != 16 {
			t.Fatalf("%s did not receive the shared range: %+v pixels=%d", name, panel.stats, len(panel.bgra))
		}
	}
	last := volumeTime.bgra[12:16]
	if last[0] != 255 || last[1] != 255 || last[2] != 255 {
		t.Fatalf("invalid Time Slice cell lost its white mask: %v", last)
	}
}

func TestVolumeSceneStateKeepsRawSlicesAndSharedRange(t *testing.T) {
	old := captureVolumeSceneState()
	t.Cleanup(func() { applyVolumeSceneState(old) })
	wantInline := []float64{-2, 0, 2}
	wantCrossline := []float64{-1, 0, 1}
	applyVolumeSceneState(volumeSceneState{
		inlineValues:    wantInline,
		crosslineValues: wantCrossline,
		timeMin:         -7,
		timeMax:         9,
	})
	captured := captureVolumeSceneState()
	if len(captured.inlineValues) != len(wantInline) || len(captured.crosslineValues) != len(wantCrossline) || captured.timeMin != -7 || captured.timeMax != 9 {
		t.Fatalf("scene state lost shared-normalization data: %+v", captured)
	}
}

func TestVolumeSharedNormalizationProvidedZeroCornerData(t *testing.T) {
	if os.Getenv("LIMAGE_VOLUME_CLIM_ACCEPTANCE") != "1" {
		t.Skip("set LIMAGE_VOLUME_CLIM_ACCEPTANCE=1 for the provided 3-D zero-corner dataset")
	}
	path := os.Getenv("SEISFORGE_TEST_VOLUME_ZERO_CORNER_FILE")
	if path == "" {
		t.Skip("set SEISFORGE_TEST_VOLUME_ZERO_CORNER_FILE to run the external volume acceptance test")
	}
	file, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	geometry, _, err := file.BuildGeometryIndexCached(189, 21, 0)
	if err != nil {
		t.Fatal(err)
	}
	inlineTraces, _, inline, err := geometry.LineTraceNumbers(0, 419)
	if err != nil {
		t.Fatal(err)
	}
	crossTraces, _, crossline, err := geometry.LineTraceNumbers(1, 1220)
	if err != nil {
		t.Fatal(err)
	}
	options := segy.RenderOptions{Width: 900, Height: 900, SampleStart: 0, SampleEnd: file.Info.SamplesPerTrace - 1,
		GainPercent: 0, ClipPercent: 99, DisplayMode: segy.DisplayAdaptive, Workers: 4}
	inlineValues, inlineStats, err := file.RenderTraceIndicesValues(inlineTraces, options)
	if err != nil {
		t.Fatal(err)
	}
	crossValues, crossStats, err := file.RenderTraceIndicesValues(crossTraces, options)
	if err != nil {
		t.Fatal(err)
	}
	cache := segy.NewTimeSliceCache(file, geometry)
	timeValues, _, err := cache.GetSlice(491)
	if err != nil {
		t.Fatal(err)
	}
	raster, mask, err := geometry.RasterizeTimeSlice(timeValues, 900, 900, geometry.Bounds())
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := volumeSharedSliceRange(inlineValues, crossValues, raster, mask, 0, 99, false, -1, 1)
	zeroCount := 0
	for _, value := range inlineValues {
		if value == 0 {
			zeroCount++
		}
	}
	for _, value := range crossValues {
		if value == 0 {
			zeroCount++
		}
	}
	if lo <= -999999 || hi <= lo || lo >= 0 || hi <= 0 {
		t.Fatalf("shared clipped range did not reject the no-data sentinel or span zero: %g..%g", lo, hi)
	}
	nearBlack := func(pixels []byte) int {
		count := 0
		for _, pixel := range pixels {
			if pixel <= 8 {
				count++
			}
		}
		return count
	}
	localPixels := segy.MapAmplitudeValues(inlineValues, inlineStats.ObservedMin, inlineStats.ObservedMax)
	sharedPixels := segy.MapAmplitudeValues(inlineValues, lo, hi)
	localBlack, sharedBlack := nearBlack(localPixels), nearBlack(sharedPixels)
	if sharedBlack >= localBlack {
		t.Fatalf("shared normalization did not reduce the black collapse: local=%d shared=%d total=%d", localBlack, sharedBlack, len(inlineValues))
	}
	t.Logf("provided volume IL=%d local=%g..%g XL=%d local=%g..%g shared=%g..%g near_black=%d->%d rendered_zero_samples=%d grid=%dx%d",
		inline, inlineStats.ObservedMin, inlineStats.ObservedMax, crossline, crossStats.ObservedMin, crossStats.ObservedMax,
		lo, hi, localBlack, sharedBlack, zeroCount, len(geometry.InlineValues), len(geometry.CrosslineValues))
}
