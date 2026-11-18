//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func figureGatherFixture(t *testing.T, width, height, display int, offset bool) (prestackSession, prestackFigurePayload) {
	t.Helper()
	reader, traces, positions := prestackGatherRegressionReader(t)
	// This fixture returns metadata/buffers, not a live Reader. Close here so
	// downstream CLI/export tests can independently assert handle lifetimes.
	defer reader.Close()
	data, err := dataset.NewManager().Open(reader.Info.Path)
	if err != nil {
		t.Fatal(err)
	}
	state := prestackSession{dataset: data, page: 0, palette: 2, display: display, wiggleDecimation: 4, gain: 7, agc: true,
		viewFirst: 3, viewLast: 51, sampleFirst: 100, sampleLast: 115,
		selection: prestackcore.GatherSelection{Type: prestackcore.GatherShot, Key: prestackcore.GatherKey{All: true}, Axis: prestackcore.AxisTrace},
		gather:    prestackcore.GatherResult{TraceIndices: traces, Positions: positions, OffsetRange: prestackcore.ValueRange{Min: -120, Max: 120, Valid: true}}}
	if offset {
		state.selection.Axis = prestackcore.AxisOffset
		for i := range positions {
			positions[i] = float64(i*i) - 400
		}
	}
	layout := prestackPanelLayoutForSize(width, height, 0)
	w, h := minInt(4096, maxInt(2, layout.Scene.width())), minInt(1536, maxInt(2, layout.Scene.height()))
	logical, pos := prestackDisplayedTracesForState(&state)
	mode := segy.DisplayAdaptive
	if offset {
		logical, pos = prestackOffsetColumns(logical, pos, w)
		mode = segy.DisplayNearest
	}
	if display == 1 {
		w = prestackWiggleRasterWidth(len(logical), w, state.wiggleDecimation)
		mode = segy.DisplayNearest
	}
	state.renderedTraces, state.renderedPositions = prestackOutputColumns(logical, pos, w)
	input := prestackRenderInputTraces(logical, state.renderedTraces, mode)
	state.imageWidth, state.imageHeight = w, h
	state.indices, state.stats, err = reader.RenderTraceIndices(input, segy.RenderOptions{Width: w, Height: h, SampleStart: state.sampleFirst, SampleEnd: state.sampleLast,
		AGC: state.agc, GainPercent: state.gain, ClipPercent: 99, DisplayMode: mode, Workers: 2, ReadStrategy: segy.ReadStrategySparseMapped})
	if err != nil {
		t.Fatal(err)
	}
	state.bgra = crookedPaletteBGRA(state.indices, state.palette)
	p, err := CapturePrestackFigurePayload(&state, width, height)
	if err != nil {
		t.Fatal(err)
	}
	return state, p
}

func TestPrestackFigureGatherReplayMatchesLivePainter(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		width, height, display int
		offset                 bool
	}{
		{"image_trace", 1280, 760, 0, false}, {"image_offset", 1280, 760, 0, true},
		{"wiggle_trace", 1280, 760, 1, false}, {"wiggle_offset", 900, 650, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, p := figureGatherFixture(t, tc.width, tc.height, tc.display, tc.offset)
			live, err := paintPrestackFigureDIB(p, func(dc uintptr) { paintPrestackGatherView(dc, &state, p.Scene, "") })
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var decoded prestackFigurePayload
			if err = json.Unmarshal(wire, &decoded); err != nil {
				t.Fatal(err)
			}
			got, err := RenderPrestackFigure(context.Background(), decoded, state.dataset.Path, "")
			if err != nil {
				t.Fatal(err)
			}
			if got.Rect != live.Rect || !bytes.Equal(got.Pix, live.Pix) {
				t.Fatalf("data-driven replay differs from real live painter: %v vs %v", got.Rect, live.Rect)
			}
			if got.Bounds().Dy() != int(p.Scene.Bottom+28-(p.Scene.Top-25)) {
				t.Fatal("crop omitted title or bottom axis")
			}
		})
	}
}

func TestPrestackFigureCaptureFreezesSelectionAndExcludesTransient(t *testing.T) {
	state, p := figureGatherFixture(t, 1280, 760, 0, false)
	before, err := PaintPrestackFigurePayload(p, &state)
	if err != nil {
		t.Fatal(err)
	}
	state.selection.Keys = []prestackcore.GatherKey{{ID: 42}}
	state.gather.TraceIndices[0] = 999999
	state.renderedTraces[0] = 999999
	state.renderedPositions[0] = -999999
	state.dragging = true
	state.dragX, state.dragY, state.dragCurrentX, state.dragCurrentY = 100, 200, 700, 500
	after, err := PaintPrestackFigurePayload(p, &state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before.Pix, after.Pix) {
		t.Fatal("reference paint borrowed mutable presentation or drag overlay")
	}
	if p.Gather.TraceIndices[0] == 999999 || p.RenderedTraces[0] == 999999 || p.RenderedPositions[0] == -999999 {
		t.Fatal("payload aliases live metadata")
	}
	state.rendering = true
	if _, err := CapturePrestackFigurePayload(&state, 1280, 760); err == nil {
		t.Fatal("mixed in-flight state accepted")
	}
}

func TestPrestackFigureCompareReplayMatchesLivePainter(t *testing.T) {
	for _, tc := range []struct {
		name        string
		width       int
		rows        []prestackcore.CompareLogicalRow
		withB, axis bool
		filter      uint32
	}{
		{"matched", 1280, []prestackcore.CompareLogicalRow{{Classification: prestackcore.CompareRowMatched, HasA: true, HasB: true, ATrace: 0, BTrace: 1}, {Classification: prestackcore.CompareRowMatched, HasA: true, HasB: true, ATrace: 3, BTrace: 4}}, true, true, 31},
		{"mixed_slots_narrow", 800, []prestackcore.CompareLogicalRow{{Classification: prestackcore.CompareRowAOnly, HasA: true, ATrace: 0}, {Classification: prestackcore.CompareRowMatched, HasA: true, HasB: true, ATrace: 1, BTrace: 2}, {Classification: prestackcore.CompareRowBOnly, HasB: true, BTrace: 4}}, true, true, 31},
		{"incompatible_axis", 1280, []prestackcore.CompareLogicalRow{{Classification: prestackcore.CompareRowMatched, HasA: true, HasB: true, ATrace: 1, BTrace: 3}}, true, false, 31},
		{"a_only", 1280, []prestackcore.CompareLogicalRow{{Classification: prestackcore.CompareRowAOnly, HasA: true, ATrace: 0}, {Classification: prestackcore.CompareRowAOnly, HasA: true, ATrace: 4}}, false, false, 31},
		{"empty_filter", 1280, nil, true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, _ := figureGatherFixture(t, tc.width, 760, 0, false)
			state.page = 3
			state.compareBAxis = prestackcore.SampleAxisCompatibility{Compatible: tc.axis, Reason: "test incompatible sample axis"}
			state.compareFilterMask = tc.filter
			state.compareRows, state.compareDisplayRows = tc.rows, tc.rows
			state.compareUseLimits, state.compareLimitMin, state.compareLimitMax = true, -2, 3
			if tc.withB {
				state.compareBDataset = state.dataset
			}
			layout := prestackPanelLayoutForSize(tc.width, 760, 3)
			panels := prestackComparePanelRectsForContent(prestackLayoutRECT(layout.Content), prestackCompareSummaryHeight)
			raster := prestackCompareRasterRect(panels[0])
			state.compareWidth, state.compareHeight = int(raster.Right-raster.Left), int(raster.Bottom-raster.Top)
			request := prestackCompareRenderRequest{token: prestackCompareRenderResult{width: state.compareWidth, height: state.compareHeight, palette: state.palette, sampleFirst: int64(state.sampleFirst), sampleLast: int64(state.sampleLast)},
				aData: state.dataset, bData: state.compareBDataset, rows: tc.rows, denseRows: prestackCompareUniformSides(tc.rows), agc: state.agc, gain: state.gain,
				useLimits: state.compareUseLimits, limitMin: state.compareLimitMin, limitMax: state.compareLimitMax, axisCompatible: tc.axis}
			result := renderPrestackCompareFrame(context.Background(), request)
			if result.err != nil || result.bErr != nil {
				t.Fatalf("production render failed: %v / %v", result.err, result.bErr)
			}
			state.compareA, state.compareB, state.compareDelta = result.a, result.b, result.delta
			p, err := CapturePrestackFigurePayload(&state, tc.width, 760)
			if err != nil {
				t.Fatal(err)
			}
			live, err := paintPrestackFigureDIB(p, func(dc uintptr) {
				paintPrestackCompareView(dc, &state, p.Content, p.Panels, prestackCompareSummaryLines(state), prestackCompareDeltaEmptyMessage(state))
			})
			if err != nil {
				t.Fatal(err)
			}
			bPath := ""
			if tc.withB {
				bPath = state.dataset.Path
			}
			got, err := RenderPrestackFigure(context.Background(), p, state.dataset.Path, bPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Pix, live.Pix) {
				t.Fatal("Compare replay differs from live production compositor")
			}
			if p.HasCompareDeltaImage != (tc.axis && prestackCompareHasMatchedSlot(tc.rows, state.compareWidth)) {
				t.Fatal("Delta availability changed")
			}
		})
	}
}

func TestPrestackFigureRejectsCorruptionAndCanceledWork(t *testing.T) {
	state, p := figureGatherFixture(t, 1280, 760, 0, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RenderPrestackFigure(ctx, p, state.dataset.Path, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
	for _, change := range []func(*prestackFigurePayload){
		func(p *prestackFigurePayload) { p.ClientWidth = 1 << 30 },
		func(p *prestackFigurePayload) { p.Crop.Left = -1 },
		func(p *prestackFigurePayload) { p.RenderTraces = []int64{-1} },
		func(p *prestackFigurePayload) { p.PaletteRGB[0] ^= 1 },
		func(p *prestackFigurePayload) { p.Options.SampleEnd = p.SourceAMetadata.SamplesPerTrace },
	} {
		bad := p
		change(&bad)
		if _, err := RenderPrestackFigure(context.Background(), bad, state.dataset.Path, ""); err == nil {
			t.Fatal("malformed figure accepted")
		}
	}
	prior := prestackState
	if _, err := RenderPrestackFigure(context.Background(), p, state.dataset.Path, ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prestackState, prior) {
		t.Fatal("offscreen replay mutated the live global session")
	}
}

func TestPrestackFigureValidatesAllMetadataBeforeRendering(t *testing.T) {
	_, original := figureGatherFixture(t, 1280, 760, 0, false)
	for _, tc := range []struct {
		name   string
		change func(*prestackFigurePayload)
	}{
		{"oversized_canvas", func(p *prestackFigurePayload) { p.ClientWidth, p.ClientHeight = 8192, 8192 }},
		{"stale_raster_width", func(p *prestackFigurePayload) { p.Options.Width++ }},
		{"stale_raster_height", func(p *prestackFigurePayload) { p.Options.Height++ }},
		{"scene_inconsistent_with_client", func(p *prestackFigurePayload) { p.Scene.Left++ }},
		{"crop_omits_axes", func(p *prestackFigurePayload) { p.Crop.Top++ }},
		{"font_height", func(p *prestackFigurePayload) { p.Font.Height = 1 << 30 }},
		{"font_width", func(p *prestackFigurePayload) { p.Font.Width = -1 << 30 }},
		{"font_rotation", func(p *prestackFigurePayload) { p.Font.Escapement = 1 << 30 }},
		{"font_weight", func(p *prestackFigurePayload) { p.Font.Weight = 1001 }},
		{"font_unterminated", func(p *prestackFigurePayload) {
			for i := range p.Font.FaceName {
				p.Font.FaceName[i] = 'A'
			}
		}},
		{"font_empty", func(p *prestackFigurePayload) { p.Font.FaceName[0] = 0 }},
		{"gather_id", func(p *prestackFigurePayload) { p.Gather.TraceIndices[0] = -1 }},
		{"gather_position", func(p *prestackFigurePayload) { p.Gather.Positions[0] = math.NaN() }},
		{"logical_id", func(p *prestackFigurePayload) { p.LogicalTraces[0] = p.SourceAMetadata.TraceCount }},
		{"logical_position", func(p *prestackFigurePayload) { p.LogicalPositions[0] = math.Inf(1) }},
		{"logical_view_mismatch", func(p *prestackFigurePayload) { p.LogicalTraces[0] = 0 }},
		{"logical_length", func(p *prestackFigurePayload) { p.LogicalPositions = p.LogicalPositions[:1] }},
		{"rendered_id", func(p *prestackFigurePayload) { p.RenderedTraces[0] = -1 }},
		{"rendered_position", func(p *prestackFigurePayload) { p.RenderedPositions[0] = math.Inf(-1) }},
		{"rendered_mapping", func(p *prestackFigurePayload) { p.RenderedTraces[0] = 0 }},
		{"render_input_mapping", func(p *prestackFigurePayload) { p.RenderTraces[0] = 0 }},
		{"view_negative", func(p *prestackFigurePayload) { p.ViewFirst = -1 }},
		{"view_past_end", func(p *prestackFigurePayload) { p.ViewLast = len(p.Gather.TraceIndices) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			var p prestackFigurePayload
			if err := json.Unmarshal(encoded, &p); err != nil {
				t.Fatal(err)
			}
			tc.change(&p)
			if err := p.validate(); err == nil {
				t.Fatal("malformed metadata was accepted")
			}
		})
	}
}

func TestPrestackFigureCompareBudgetAndExactLayout(t *testing.T) {
	_, p := figureGatherFixture(t, 1280, 760, 0, false)
	p.Page = 3
	p.Summary = []string{"frozen A/B summary", "frozen axis summary"}
	p.CompareRows = nil // An empty filter remains a legitimate blank figure.
	p.CompareDenseRows = false
	setSize := func(width, height int) {
		p.ClientWidth, p.ClientHeight = width, height
		layout := prestackPanelLayoutForSize(width, height, 3)
		p.Scene, p.Content = prestackLayoutRECT(layout.Scene), prestackLayoutRECT(layout.Content)
		p.Crop = p.Content
		p.Panels = prestackComparePanelRectsForContent(p.Content, prestackCompareSummaryHeight)
		raster := prestackCompareRasterRect(p.Panels[0])
		p.Options.Width, p.Options.Height = int(raster.Right-raster.Left), int(raster.Bottom-raster.Top)
	}
	setSize(3840, 2160)
	if err := p.validate(); err != nil {
		t.Fatalf("ordinary 4K empty-filter Compare refused: %v", err)
	}
	p.Panels[1].Left++
	if err := p.validate(); err == nil {
		t.Fatal("arbitrary Compare panel geometry accepted")
	}
	setSize(8192, 2048)
	if int64(p.Options.Width)*int64(p.Options.Height) <= prestackFigureMaxComparePixels {
		t.Fatal("fixture must exceed per-panel budget")
	}
	if err := p.validate(); err == nil || !strings.Contains(err.Error(), "export budget") {
		t.Fatalf("oversized multi-float-buffer Compare accepted: %v", err)
	}
}
