//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func prestackGatherRegressionReader(t *testing.T) (*segy.File, []int64, []float64) {
	t.Helper()
	const traceCount, sampleCount = 5 * 12, 256
	headers := make([]testsegy.TraceHeader, traceCount)
	logical := make([]int64, traceCount)
	positions := make([]float64, traceCount)
	for trace := range logical {
		shot, receiver := trace/12, trace%12
		logical[trace], positions[trace] = int64(trace), float64(trace)
		headers[trace] = testsegy.TraceHeader{
			SourceID: int32(1000 + shot), ReceiverID: int32(2000 + receiver),
			CDP: int32(3000 + trace), Offset: int32((receiver - 5) * 20),
			SourceX: int32(shot * 100), ReceiverX: int32(receiver * 20), ReceiverY: 100,
			Scalar: 1, Units: 1, SampleCount: sampleCount, SampleIntervalUS: 2000,
		}
	}
	path := filepath.Join(t.TempDir(), "gather_5shot_12receiver_256.sgy")
	if err := testsegy.Write(path, testsegy.Options{
		Rows: 5, Cols: 12, Samples: sampleCount, SampleIntervalUS: 2000, TraceHeaders: headers,
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if reader.Info.TraceCount != traceCount || reader.Info.SamplesPerTrace != sampleCount {
		t.Fatalf("unexpected real fixture axis: %+v", reader.Info)
	}
	return reader, logical, positions
}

func TestPrestackGatherAdaptiveRealReaderUsesLogicalSources(t *testing.T) {
	reader, logical, positions := prestackGatherRegressionReader(t)
	for _, tc := range []struct {
		name                       string
		width, height, first, last int
	}{
		{"default_60_by_256", 1274, 662, 0, 255},
		{"short_sample_window", 1274, 662, 100, 115},
		{"wide_scene", 2474, 982, 0, 255},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, _ := prestackOutputColumns(logical, positions, tc.width)
			if len(rendered) != tc.width || len(rendered) <= len(logical) {
				t.Fatal("regression requires repeated physical traces in the pixel-column mapping")
			}
			options := segy.RenderOptions{
				Width: tc.width, Height: tc.height, SampleStart: tc.first, SampleEnd: tc.last,
				DisplayMode: segy.DisplayAdaptive, ReadStrategy: segy.ReadStrategySparseMapped,
				ClipPercent: 99, Workers: 2,
			}
			// The original request expanded sixty unique sources to every screen
			// column before entering the sparse smooth renderer. Small sample
			// windows select that path and expose its duplicate-read rejection.
			if _, _, err := reader.RenderTraceIndices(rendered, options); err == nil || !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("old expanded Adaptive request did not reproduce the duplicate error: %v", err)
			}
			input := prestackRenderInputTraces(logical, rendered, options.DisplayMode)
			if !reflect.DeepEqual(input, logical) {
				t.Fatalf("Adaptive input lost unique logical sources: %v", input)
			}
			pixels, stats, err := reader.RenderTraceIndices(input, options)
			if err != nil {
				t.Fatalf("logical Adaptive request failed: %v", err)
			}
			if len(pixels) != tc.width*tc.height || stats.InputTraceCount != len(logical) {
				t.Fatalf("wrong pixel/source units: pixels=%d, shape=%d x %d, input=%d", len(pixels), tc.width, tc.height, stats.InputTraceCount)
			}
			legacyOptions := options
			legacyOptions.ReadStrategy = segy.ReadStrategyDefault
			legacyPixels, _, err := reader.RenderTraceIndices(logical, legacyOptions)
			if err != nil || !bytes.Equal(pixels, legacyPixels) {
				t.Fatalf("sparse logical raster differs from the legacy logical raster: err=%v", err)
			}
			result := &prestackRenderResult{width: tc.width, height: tc.height, indices: pixels, bgra: crookedPaletteBGRA(pixels, 2)}
			if !prestackValidRenderBuffers(result) {
				t.Fatalf("valid Reader raster failed the strict exchange-buffer contract: indices=%d BGRA=%d", len(result.indices), len(result.bgra))
			}
		})
	}
}

func TestPrestackGatherAdaptiveInputIsASnapshot(t *testing.T) {
	logical := []int64{5, 3, 0, 2}
	rendered := []int64{5, 5, 3, 3, 0, 0, 2, 2}
	input := prestackRenderInputTraces(logical, rendered, segy.DisplayAdaptive)
	want := append([]int64(nil), logical...)
	logical[0], rendered[0] = 59, 58
	if !reflect.DeepEqual(input, want) {
		t.Fatalf("Adaptive worker input aliases mutable selection or pixel columns: %v", input)
	}
}

func TestPrestackGatherNearestRealReaderPreservesWiggleAndOffsetColumns(t *testing.T) {
	reader, logical, positions := prestackGatherRegressionReader(t)
	wiggleWidth := prestackWiggleRasterWidth(len(logical), 80, 5)
	wiggleColumns, _ := prestackOutputColumns(logical, positions, wiggleWidth)
	offsets := make([]float64, len(positions))
	for i := range offsets {
		offsets[i] = float64(i*20 - 600)
	}
	offsetColumns, _ := prestackOffsetColumns(logical, offsets, 1274)
	for _, tc := range []struct {
		name    string
		columns []int64
	}{
		{"wiggle_decimated", wiggleColumns},
		{"offset_repeated_columns", offsetColumns},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := prestackRenderInputTraces(logical, tc.columns, segy.DisplayNearest)
			if !reflect.DeepEqual(input, tc.columns) {
				t.Fatalf("Nearest replaced the chosen physical-column mapping: got=%v want=%v", input, tc.columns)
			}
			options := segy.RenderOptions{
				Width: len(tc.columns), Height: 512, SampleStart: 0, SampleEnd: 255,
				DisplayMode: segy.DisplayNearest, ReadStrategy: segy.ReadStrategySparseMapped,
				ClipPercent: 99, Workers: 2,
			}
			pixels, _, err := reader.RenderTraceIndices(input, options)
			if err != nil {
				t.Fatal(err)
			}
			options.ReadStrategy = segy.ReadStrategyDefault
			legacyPixels, _, err := reader.RenderTraceIndices(tc.columns, options)
			if err != nil || !bytes.Equal(pixels, legacyPixels) || len(pixels) != options.Width*options.Height {
				t.Fatalf("Nearest changed Wiggle/Offset pixels or dimensions: err=%v", err)
			}
		})
	}
}

func prestackGatherCompletionFixture(t *testing.T) (*prestackRenderResult, *prestackSession, prestackAsyncToken, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	current := prestackAsyncToken{
		owner: 17, ownerToken: 2, datasetGeneration: 4, selectionGeneration: 5,
		sizeGeneration: 6, workspaceGeneration: 3, sceneWidth: 2, sceneHeight: 3, page: 0,
	}
	state := &prestackSession{
		ownerToken: 2, datasetGeneration: 4, selectionGeneration: 5, resizeGeneration: 6,
		workspaceGeneration: 3, renderGeneration: 10, rendering: true, renderCancel: cancel,
		indices: []byte{1, 2, 3, 4}, bgra: bytes.Repeat([]byte{7}, 16),
		imageWidth: 2, imageHeight: 2, renderedTraces: []int64{0, 1},
		renderedPositions: []float64{0, 1}, renderedColumns: 2,
	}
	result := &prestackRenderResult{
		generation: 10, workspaceGeneration: 3, token: current, width: 2, height: 3,
		indices: []byte{0, 1, 2, 3, 4, 5}, bgra: make([]byte, 24),
	}
	return result, state, current, ctx
}

func assertPrestackGatherOldFrame(t *testing.T, state *prestackSession) {
	t.Helper()
	if !bytes.Equal(state.indices, []byte{1, 2, 3, 4}) || !bytes.Equal(state.bgra, bytes.Repeat([]byte{7}, 16)) ||
		state.imageWidth != 2 || state.imageHeight != 2 || state.renderedColumns != 2 ||
		!reflect.DeepEqual(state.renderedTraces, []int64{0, 1}) || !reflect.DeepEqual(state.renderedPositions, []float64{0, 1}) {
		t.Fatal("completion helper changed the last complete frame before a successful exchange")
	}
}

func TestPrestackGatherCompletionCurrentErrorEndsBusyAndKeepsFrame(t *testing.T) {
	result, state, current, ctx := prestackGatherCompletionFixture(t)
	wantErr := errors.New("fixture Reader failed")
	result.err, result.indices, result.bgra = wantErr, nil, nil
	accepted, err := acceptPrestackRenderCompletion(result, 10, state, current)
	if !accepted || !errors.Is(err, wantErr) {
		t.Fatalf("current Reader error was masked by empty image buffers: accepted=%v err=%v", accepted, err)
	}
	if state.rendering || state.renderCancel != nil || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("current failed render retained its busy state or cancellation context")
	}
	assertPrestackGatherOldFrame(t, state)
}

func TestPrestackGatherCompletionStaleErrorCannotEndCurrentBusy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*prestackRenderResult, *prestackSession, *prestackAsyncToken, *int64)
	}{
		{"result_generation", func(r *prestackRenderResult, _ *prestackSession, _ *prestackAsyncToken, _ *int64) { r.generation = 9 }},
		{"message_generation", func(_ *prestackRenderResult, _ *prestackSession, _ *prestackAsyncToken, generation *int64) {
			*generation = 9
		}},
		{"matching_old_result_and_message", func(r *prestackRenderResult, _ *prestackSession, _ *prestackAsyncToken, generation *int64) {
			r.generation, *generation = 9, 9
		}},
		{"new_current_generation", func(_ *prestackRenderResult, s *prestackSession, _ *prestackAsyncToken, _ *int64) {
			s.renderGeneration = 11
		}},
		{"result_workspace", func(r *prestackRenderResult, _ *prestackSession, _ *prestackAsyncToken, _ *int64) {
			r.workspaceGeneration++
		}},
		{"owner", func(_ *prestackRenderResult, _ *prestackSession, current *prestackAsyncToken, _ *int64) {
			current.owner++
		}},
		{"reopened_owner", func(_ *prestackRenderResult, _ *prestackSession, current *prestackAsyncToken, _ *int64) {
			current.ownerToken++
		}},
		{"dataset", func(_ *prestackRenderResult, _ *prestackSession, current *prestackAsyncToken, _ *int64) {
			current.datasetGeneration++
		}},
		{"token_workspace", func(_ *prestackRenderResult, _ *prestackSession, current *prestackAsyncToken, _ *int64) {
			current.workspaceGeneration++
		}},
		{"selection", func(_ *prestackRenderResult, _ *prestackSession, current *prestackAsyncToken, _ *int64) {
			current.selectionGeneration++
		}},
		{"resize", func(_ *prestackRenderResult, _ *prestackSession, current *prestackAsyncToken, _ *int64) {
			current.sizeGeneration++
		}},
		{"hidden_gather_page", func(_ *prestackRenderResult, _ *prestackSession, current *prestackAsyncToken, _ *int64) {
			current.page = 3
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, state, current, ctx := prestackGatherCompletionFixture(t)
			result.err, result.indices, result.bgra = errors.New("stale Reader failed"), nil, nil
			generation := int64(10)
			tc.mutate(result, state, &current, &generation)
			accepted, err := acceptPrestackRenderCompletion(result, generation, state, current)
			if accepted || err != nil || !state.rendering || state.renderCancel == nil || ctx.Err() != nil {
				t.Fatalf("stale failure affected the active task: accepted=%v err=%v busy=%v ctx=%v", accepted, err, state.rendering, ctx.Err())
			}
			assertPrestackGatherOldFrame(t, state)
		})
	}
}

func TestPrestackGatherCompletionMalformedSuccessEndsBusyWithoutRelaxingBuffers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*prestackRenderResult)
	}{
		{"missing_indices", func(r *prestackRenderResult) { r.indices = nil }},
		{"short_indices", func(r *prestackRenderResult) { r.indices = r.indices[:5] }},
		{"long_indices", func(r *prestackRenderResult) { r.indices = append(r.indices, 0) }},
		{"missing_bgra", func(r *prestackRenderResult) { r.bgra = nil }},
		{"short_bgra", func(r *prestackRenderResult) { r.bgra = r.bgra[:23] }},
		{"long_bgra", func(r *prestackRenderResult) { r.bgra = append(r.bgra, 0) }},
		{"zero_width", func(r *prestackRenderResult) { r.width = 0 }},
		{"over_width_limit", func(r *prestackRenderResult) { r.width = 4097 }},
		{"over_height_limit", func(r *prestackRenderResult) { r.height = 1537 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, state, current, ctx := prestackGatherCompletionFixture(t)
			tc.mutate(result)
			accepted, err := acceptPrestackRenderCompletion(result, 10, state, current)
			if !accepted || err == nil || state.rendering || state.renderCancel != nil || !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("malformed current raster did not terminate with an error: accepted=%v err=%v busy=%v ctx=%v", accepted, err, state.rendering, ctx.Err())
			}
			assertPrestackGatherOldFrame(t, state)
		})
	}
}

func TestPrestackGatherCompletionValidSuccessEndsBusy(t *testing.T) {
	result, state, current, ctx := prestackGatherCompletionFixture(t)
	accepted, err := acceptPrestackRenderCompletion(result, 10, state, current)
	if !accepted || err != nil || state.rendering || state.renderCancel != nil || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("valid current raster was rejected or left busy: accepted=%v err=%v busy=%v ctx=%v", accepted, err, state.rendering, ctx.Err())
	}
	assertPrestackGatherOldFrame(t, state)
}
