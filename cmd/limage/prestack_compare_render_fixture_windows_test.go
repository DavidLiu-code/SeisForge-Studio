//go:build windows

package main

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestPrestackCompareReaderPositiveDeltaAndInvalidAxis(t *testing.T) {
	saved := prestackState
	t.Cleanup(func() { prestackState = saved })
	prestackState.sampleFirst, prestackState.sampleLast = 0, 255
	dir := t.TempDir()
	openFixture := func(name string, shuffled bool, scale float32, invalid bool) (*segy.File, *prestackcore.PrestackIndex) {
		t.Helper()
		order := make([]int, 60)
		headers := make([]testsegy.TraceHeader, 60)
		for physical := range order {
			logical := physical
			if shuffled {
				logical = (physical*17 + 7) % 60
			}
			order[physical] = logical
			shot, receiver := logical/12, logical%12
			headers[physical] = testsegy.TraceHeader{
				SourceID: int32(1000 + shot), ReceiverID: int32(2000 + receiver),
				CDP: int32(3000 + logical), Offset: int32((receiver - 5) * 20),
				SourceX: int32(shot * 100), ReceiverX: int32(receiver * 20),
				Scalar: 1, Units: 1, SampleCount: 256, SampleIntervalUS: 2000, DelayMS: 10,
			}
			if invalid && logical == 2 {
				headers[physical] = testsegy.TraceHeader{}
			}
		}
		path := filepath.Join(dir, name)
		err := testsegy.Write(path, testsegy.Options{Cols: 60, Samples: 256, SampleIntervalUS: 2000, TraceHeaders: headers,
			SampleValue: func(physical, sample int) float32 {
				if sample < 3 {
					return float32(sample+1) * scale
				}
				return float32(math.Sin(float64(sample)*.17+float64(order[physical])*.13)) * scale
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		reader, err := segy.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reader.Close() })
		index, err := prestackcore.BuildIndex(context.Background(), reader, prestackcore.DefaultHeaderMapping(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return reader, index
	}
	a, ai := openFixture("A.sgy", false, 1, false)
	b, bi := openFixture("B_reordered_half.sgy", true, .5, false)
	selection := prestackcore.GatherSelection{Type: prestackcore.GatherShot, Key: prestackcore.GatherKey{All: true}}
	ag, err := ai.Gather(selection)
	if err != nil {
		t.Fatal(err)
	}
	bg, err := bi.Gather(selection)
	if err != nil {
		t.Fatal(err)
	}
	axis := comparePrestackGatherAxes(ai, ag, bi, bg)
	if !axis.Compatible {
		t.Fatalf("positive reordered fixture unexpectedly disabled Delta: %+v", axis)
	}
	match, err := prestackcore.MatchGatherResults(ai, ag, bi, bg)
	if err != nil || len(match.Pairs) != 60 {
		t.Fatalf("positive reordered fixture did not match: %+v err=%v", match, err)
	}
	aTraces, bTraces := make([]int64, len(match.Pairs)), make([]int64, len(match.Pairs))
	for i, pair := range match.Pairs {
		aTraces[i], bTraces[i] = pair.ATrace, pair.BTrace
	}
	options := segy.RenderOptions{Width: 539, Height: 692, SampleStart: 0, SampleEnd: 255,
		ClipPercent: 99, DisplayMode: segy.DisplayAdaptive, ReadStrategy: segy.ReadStrategySparseMapped, Workers: 2}
	av, _, err := a.RenderTraceIndicesValues(aTraces, options)
	if err != nil {
		t.Fatal(err)
	}
	bv, _, err := b.RenderTraceIndicesValues(bTraces, options)
	if err != nil {
		t.Fatal(err)
	}
	delta, err := prestackcore.DifferenceValues(av, bv)
	if err != nil || len(delta) != options.Width*options.Height {
		t.Fatalf("Delta raster failed: size=%d err=%v", len(delta), err)
	}
	for i, value := range delta {
		if math.Abs(value-av[i]*.5) > 1e-10 {
			t.Fatalf("B reorder changed Delta at %d: A=%g B=%g Delta=%g", i, av[i], bv[i], value)
		}
	}
	lo, hi := prestackcore.SymmetricValueRange(delta)
	if lo >= 0 || hi <= 0 || lo != -hi {
		t.Fatalf("Delta must use a zero-symmetric nonempty range: %g..%g", lo, hi)
	}
	pixels := crookedPaletteBGRA(segy.MapAmplitudeValues(delta, lo, hi), 2)
	if !prestackValidCompareBuffer(pixels, options.Width, options.Height, false) {
		t.Fatal("positive Delta raster does not meet the UI BGRA contract")
	}
	_, invalidIndex := openFixture("B_invalid_header.sgy", true, .5, true)
	invalidGather, err := invalidIndex.Gather(selection)
	if err != nil {
		t.Fatal(err)
	}
	invalidAxis := comparePrestackGatherAxes(ai, ag, invalidIndex, invalidGather)
	if invalidAxis.Compatible || !strings.Contains(invalidAxis.Reason, "内部采样轴不一致") {
		t.Fatalf("invalid-header fixture must not silently allow Delta: %+v", invalidAxis)
	}
	message := prestackCompareDeltaEmptyMessage(prestackSession{compareBDataset: &dataset.SeismicDataset{}, compareBAxis: invalidAxis})
	if !strings.Contains(message, "Δ 已禁用") || !strings.Contains(message, invalidAxis.Reason) {
		t.Fatalf("Delta panel does not explain disabled output: %q", message)
	}
}

func TestPrestackCompareCompletionErrorEndsCurrentTask(t *testing.T) {
	failure := errors.New("reader failed")
	base := prestackSession{ownerToken: 2, workspaceGeneration: 3, datasetGeneration: 4,
		selectionGeneration: 5, mappingGeneration: 6, compareBGeneration: 7, compareBMappingGeneration: 8,
		compareModeGeneration: 9, compareDisplayGeneration: 10, resizeGeneration: 11,
		sampleFirst: 0, sampleLast: 255, page: 3, compareRenderGeneration: 12, compareRendering: true}
	result := &prestackCompareRenderResult{generation: 12, owner: 17, ownerToken: 2, workspaceGeneration: 3,
		datasetGeneration: 4, selectionGeneration: 5, aMappingGeneration: 6, bGeneration: 7, bMappingGeneration: 8,
		compareModeGeneration: 9, displayGeneration: 10, resizeGeneration: 11, sampleFirst: 0, sampleLast: 255,
		comparePage: 3, width: 20, height: 30, err: failure}
	state := base
	cancelled := false
	state.compareRenderCancel = func() { cancelled = true }
	if accepted, err := acceptPrestackCompareCompletion(result, 12, &state, 17, 20, 30); !accepted || !errors.Is(err, failure) || state.compareRendering || !cancelled || state.compareRenderCancel != nil {
		t.Fatalf("current failure left Compare busy: accepted=%v err=%v rendering=%v cancelled=%v", accepted, err, state.compareRendering, cancelled)
	}
	state = base
	state.compareRenderGeneration = 13
	if accepted, _ := acceptPrestackCompareCompletion(result, 12, &state, 17, 20, 30); accepted || !state.compareRendering {
		t.Fatal("old Compare failure ended the newer task")
	}
	state = base
	malformed := *result
	malformed.err = nil
	if accepted, err := acceptPrestackCompareCompletion(&malformed, 12, &state, 17, 20, 30); !accepted || err == nil || state.compareRendering {
		t.Fatal("malformed current frame was silently ignored")
	}
	state = base
	valid := malformed
	valid.a = make([]byte, 20*30*4)
	if accepted, err := acceptPrestackCompareCompletion(&valid, 12, &state, 17, 20, 30); !accepted || err != nil || state.compareRendering {
		t.Fatalf("valid A-only Compare frame rejected: accepted=%v err=%v", accepted, err)
	}
}
