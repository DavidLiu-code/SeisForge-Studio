package segy

import (
	"bytes"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func phase12RenderInputs() ([]int64, []float64) {
	indices, positions := make([]int64, 48), make([]float64, 48)
	for i := range indices {
		indices[i], positions[i] = int64(i), float64(i)+float64(i*i)/37
	}
	return indices, positions
}

func TestPhase12SparseMappedAndFallbackMatchLegacyPixels(t *testing.T) {
	for _, format := range []int{1, 2, 5} {
		for _, options := range []RenderOptions{
			{Width: 19, Height: 51, GainPercent: 0, ClipPercent: 99, SampleStart: 3, SampleEnd: 59, DisplayMode: DisplaySmooth, Workers: 4},
			{Width: 31, Height: 79, GainPercent: 17, ClipPercent: 99, AGC: true, SampleStart: 2, SampleEnd: 61, DisplayMode: DisplayAdaptive, Workers: 2},
			{Width: 15, Height: 43, GainPercent: 49, ClipPercent: 99, SampleStart: 4, SampleEnd: 55, DisplayMode: DisplayNearest, Workers: 4},
		} {
			path := writePhase9FormatFixture(t, format)
			indices, positions := phase12RenderInputs()

			legacyFile, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			legacy, legacyStats, err := legacyFile.RenderTracePositions(indices, positions, positions[2], positions[45], options)
			_ = legacyFile.Close()
			if err != nil {
				t.Fatal(err)
			}

			mappedFile, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			mappedOptions := options
			mappedOptions.ReadStrategy = ReadStrategySparseMapped
			mapped, mappedStats, err := mappedFile.RenderTracePositions(indices, positions, positions[2], positions[45], mappedOptions)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(mapped, legacy) {
				t.Fatalf("format %d sparse mapped pixels changed for %+v", format, options)
			}
			if !mappedStats.SparseIO || !mappedStats.MappedIO || mappedStats.SupportTraceCount <= 0 || mappedStats.SupportTraceCount > mappedStats.InputTraceCount || mappedStats.IOLogicalBytes <= 0 {
				t.Fatalf("format %d missing sparse mapped statistics: %+v", format, mappedStats)
			}
			if err := mappedFile.Close(); err != nil {
				t.Fatal(err)
			}

			fallbackFile, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			fallbackFile.mappingDisabled = true
			fallback, fallbackStats, err := fallbackFile.RenderTracePositions(indices, positions, positions[2], positions[45], mappedOptions)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(fallback, legacy) || fallbackStats.MappedIO || fallbackStats.IOFallbacks != 1 || fallbackStats.IOReadCalls <= 0 {
				t.Fatalf("format %d sparse fallback changed pixels or statistics: %+v", format, fallbackStats)
			}
			fallbackStats.IOReadCalls, fallbackStats.IOCoalescedBlocks, fallbackStats.IOReadBytes = 0, 0, 0
			fallbackStats.CoalescedIO, fallbackStats.SparseIO, fallbackStats.IOFallbacks = false, false, 0
			fallbackStats.InputTraceCount, fallbackStats.SupportTraceCount = 0, 0
			fallbackStats.IOLogicalBytes, fallbackStats.IODecodeNanos, fallbackStats.DecodedSampleCount = 0, 0, 0
			if fallbackStats != legacyStats {
				t.Fatalf("format %d sparse fallback changed seismic statistics:\nlegacy=%+v\nfallback=%+v", format, legacyStats, fallbackStats)
			}
			_ = fallbackFile.Close()
		}
	}
	if active := atomic.LoadInt64(&activeReadMappings); active != 0 {
		t.Fatalf("mapped readers leaked after close: %d", active)
	}
}

func TestPhase12SparseSmoothActuallyOmitsUnusedTraces(t *testing.T) {
	indices, positions := make([]int64, 200), make([]float64, 200)
	for i := range indices {
		indices[i], positions[i] = int64(i), float64(i)
	}
	plan, err := BuildTraceSupportPlan(indices, positions, 0, 199, 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.TraceIndices) > 64 || len(plan.TraceIndices)*100 > len(indices)*35 {
		t.Fatalf("support plan retained too many input traces: support=%d input=%d", len(plan.TraceIndices), len(indices))
	}
	if reflect.DeepEqual(plan.TraceIndices, indices) {
		t.Fatal("support plan unexpectedly retained every input trace")
	}
}

func TestPhase12SparseSamplePlanOmitsUnusedVerticalSamples(t *testing.T) {
	nearest := buildSparseSamplePlan(3072, 1536, false)
	if len(nearest.Rows) != 1536 || len(nearest.Samples) != 1536 {
		t.Fatalf("nearest vertical plan=%d rows/%d samples, want 1536/1536", len(nearest.Rows), len(nearest.Samples))
	}
	smooth := buildSparseSamplePlan(4000, 1000, true)
	if len(smooth.Rows) != 1000 || len(smooth.Samples) >= 2500 {
		t.Fatalf("smooth vertical plan retained too many samples: rows=%d support=%d", len(smooth.Rows), len(smooth.Samples))
	}
	for _, plan := range []sparseSamplePlan{nearest, smooth} {
		for index := 1; index < len(plan.Samples); index++ {
			if plan.Samples[index] <= plan.Samples[index-1] {
				t.Fatalf("vertical support samples are not strictly ordered at %d", index)
			}
		}
	}
}

func TestPhase12ReadMappingsAreCappedAndReleased(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows file mapping lifecycle test")
	}
	path := writePhase9FormatFixture(t, 5)
	files := make([]*File, maximumMappedReaders+1)
	for index := range files {
		file, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		files[index] = file
	}
	t.Cleanup(func() {
		for _, file := range files {
			_ = file.Close()
		}
	})
	atomic.StoreInt64(&peakReadMappings, 0)
	for index := 0; index < maximumMappedReaders; index++ {
		if _, ok := files[index].ensureReadMapping(); !ok {
			t.Fatalf("mapping %d failed", index)
		}
	}
	done := make(chan bool, 1)
	go func() {
		_, ok := files[maximumMappedReaders].ensureReadMapping()
		done <- ok
	}()
	select {
	case <-done:
		t.Fatal("fifth read mapping bypassed the four-reader cap")
	case <-time.After(50 * time.Millisecond):
	}
	if err := files[0].Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("queued mapping failed after a slot was released")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued mapping did not resume after a slot was released")
	}
	for index := 1; index < len(files); index++ {
		if err := files[index].Close(); err != nil {
			t.Fatal(err)
		}
	}
	if active := atomic.LoadInt64(&activeReadMappings); active != 0 {
		t.Fatalf("mapped readers leaked after lifecycle test: %d", active)
	}
	if peak := atomic.LoadInt64(&peakReadMappings); peak > maximumMappedReaders {
		t.Fatalf("mapped reader peak=%d, cap=%d", peak, maximumMappedReaders)
	}
}
