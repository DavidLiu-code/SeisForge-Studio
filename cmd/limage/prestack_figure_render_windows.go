//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"runtime"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const prestackFigurePayloadVersion = 1

// Compare uses several float rasters plus sort buffers simultaneously. Its
// native window renderer has no cap, so figure replay applies a conservative
// export-only cap before opening Readers or allocating any raster.
const prestackFigureMaxCanvasPixels = 16 << 20
const prestackFigureMaxComparePixels = 4 << 20

// The layout is exactly Win32 LOGFONTW. Keeping the entire descriptor avoids
// substituting today's default UI font when a saved figure is replayed.
type prestackFigureFont struct {
	Height, Width, Escapement, Orientation, Weight       int32
	Italic, Underline, StrikeOut, CharSet                byte
	OutPrecision, ClipPrecision, Quality, PitchAndFamily byte
	FaceName                                             [32]uint16
}

// Unlike segy.RenderOptions this wire DTO contains no function values.
type prestackFigureOptions struct {
	Width, Height, SampleStart, SampleEnd int
	AGC                                   bool
	ClipPercent, GainPercent              float64
	DisplayMode                           segy.DisplayMode
	ReadStrategy                          segy.RenderReadStrategy
	Workers                               int
}

func (o prestackFigureOptions) renderOptions() segy.RenderOptions {
	return segy.RenderOptions{Width: o.Width, Height: o.Height, SampleStart: o.SampleStart,
		SampleEnd: o.SampleEnd, AGC: o.AGC, ClipPercent: o.ClipPercent, GainPercent: o.GainPercent,
		DisplayMode: o.DisplayMode, ReadStrategy: o.ReadStrategy, Workers: o.Workers}
}

// prestackFigurePayload is data-selection and presentation state, never a
// screenshot or amplitude dump. Source integrity belongs to the outer bundle.
// RenderTraces and CompareRows are already resolved physical source identities:
// replay must not rerun an auto-detection or matching algorithm.
type prestackFigurePayload struct {
	Version, Page                               int
	ClientWidth, ClientHeight                   int
	Crop, Scene, Content                        RECT
	Panels                                      [3]RECT
	Font                                        prestackFigureFont
	Palette                                     int
	PaletteRGB                                  [256]uint32
	Display, WiggleDecimation                   int
	Options                                     prestackFigureOptions
	SourceAMetadata                             dataset.Metadata
	SourceBMetadata                             dataset.Metadata
	HasB                                        bool
	Selection                                   prestackcore.GatherSelection
	Mapping, BMapping                           prestackcore.HeaderMapping
	Gather                                      prestackcore.GatherResult
	ViewFirst, ViewLast                         int
	LogicalTraces, RenderTraces, RenderedTraces []int64
	LogicalPositions, RenderedPositions         []float64
	Title                                       string
	CompareRows                                 []prestackcore.CompareLogicalRow
	CompareDenseRows, CompareAxisCompatible     bool
	CompareUseLimits                            bool
	CompareLimitMin, CompareLimitMax            float64
	CompareFilterMask                           uint32
	CompareViewFirst, CompareViewLast           int
	CompareAxis                                 prestackcore.SampleAxisCompatibility
	Summary                                     []string
	DeltaEmpty                                  string
	HasCompareBImage, HasCompareDeltaImage      bool
}

var (
	pFigureGetObjectW          = gdi32.NewProc("GetObjectW")
	pFigureCreateFontIndirectW = gdi32.NewProc("CreateFontIndirectW")
	pFigureGDIFlush            = gdi32.NewProc("GdiFlush")
)

func prestackFigurePalette(index int) [256]uint32 {
	indices := make([]byte, 256)
	for i := range indices {
		indices[i] = byte(i)
	}
	bgra := crookedPaletteBGRA(indices, index)
	var colors [256]uint32
	for i := range colors {
		colors[i] = uint32(bgra[4*i+2])<<16 | uint32(bgra[4*i+1])<<8 | uint32(bgra[4*i])
	}
	return colors
}

// CapturePrestackFigurePayload must run on the UI thread after a completed
// render. Busy/resize states are rejected rather than combining live controls
// with an older bitmap. Call PaintPrestackFigurePayload before returning to the
// event loop to freeze the matching reference image.
func CapturePrestackFigurePayload(state *prestackSession, clientWidth, clientHeight int) (prestackFigurePayload, error) {
	var p prestackFigurePayload
	if state == nil || state.dataset == nil {
		return p, errors.New("no Prestack dataset")
	}
	if state.page != 0 && state.page != 3 {
		return p, errors.New("figure export supports only Prestack Gather and Compare")
	}
	if state.loading || state.rendering || state.compareRendering || state.compareBLoading || state.resizeActive || state.resizePending {
		return p, errors.New("wait for Prestack rendering and resizing to finish")
	}
	if len(state.gather.TraceIndices) == 0 || len(state.gather.Positions) != len(state.gather.TraceIndices) {
		return p, errors.New("no complete Prestack gather")
	}
	layout := prestackPanelLayoutForSize(clientWidth, clientHeight, state.page)
	p = prestackFigurePayload{Version: prestackFigurePayloadVersion, Page: state.page,
		ClientWidth: clientWidth, ClientHeight: clientHeight, Scene: prestackLayoutRECT(layout.Scene),
		Content: prestackLayoutRECT(layout.Content), Palette: state.palette, PaletteRGB: prestackFigurePalette(state.palette),
		Display: state.display, WiggleDecimation: state.wiggleDecimation,
		SourceAMetadata: state.dataset.Metadata, Selection: state.selection, Mapping: state.mapping, BMapping: state.compareBMapping,
		Gather: state.gather, ViewFirst: state.viewFirst, ViewLast: state.viewLast,
		Options: prestackFigureOptions{SampleStart: state.sampleFirst, SampleEnd: state.sampleLast,
			AGC: state.agc, ClipPercent: 99, GainPercent: math.Max(0, math.Min(49, state.gain)),
			DisplayMode: segy.DisplayAdaptive, ReadStrategy: segy.ReadStrategySparseMapped, Workers: 2}}
	p.Selection.Keys = append([]prestackcore.GatherKey(nil), state.selection.Keys...)
	p.Gather.Selection.Keys = append([]prestackcore.GatherKey(nil), state.gather.Selection.Keys...)
	p.Gather.TraceIndices = append([]int64(nil), state.gather.TraceIndices...)
	p.Gather.Positions = append([]float64(nil), state.gather.Positions...)
	font := hFont
	if font == 0 {
		font, _, _ = pGetStockObject.Call(DEFAULT_GUI_FONT)
	}
	if n, _, _ := pFigureGetObjectW.Call(font, unsafe.Sizeof(p.Font), uintptr(unsafe.Pointer(&p.Font))); n != unsafe.Sizeof(p.Font) {
		return prestackFigurePayload{}, errors.New("cannot capture the current figure font")
	}
	if state.page == 0 {
		if state.imageWidth < 2 || state.imageHeight < 2 || len(state.indices) != state.imageWidth*state.imageHeight ||
			len(state.bgra) != state.imageWidth*state.imageHeight*4 {
			return prestackFigurePayload{}, errors.New("no completed Gather image")
		}
		p.Options.Width, p.Options.Height = state.imageWidth, state.imageHeight
		logical, positions := prestackDisplayedTracesForState(state)
		p.LogicalTraces, p.LogicalPositions = append([]int64(nil), logical...), append([]float64(nil), positions...)
		p.RenderedTraces = append([]int64(nil), state.renderedTraces...)
		p.RenderedPositions = append([]float64(nil), state.renderedPositions...)
		if state.display == 1 || state.selection.Axis == prestackcore.AxisOffset {
			p.Options.DisplayMode = segy.DisplayNearest
		}
		p.RenderTraces = append([]int64(nil), prestackRenderInputTraces(logical, p.RenderedTraces, p.Options.DisplayMode)...)
		p.Title = prestackGatherFigureTitle(state, p.RenderedPositions)
		p.Crop = RECT{Left: 0, Top: p.Scene.Top - 25, Right: int32(clientWidth), Bottom: p.Scene.Bottom + 28}
	} else {
		if !prestackValidCompareBuffer(state.compareA, state.compareWidth, state.compareHeight, false) ||
			!prestackValidCompareBuffer(state.compareB, state.compareWidth, state.compareHeight, true) ||
			!prestackValidCompareBuffer(state.compareDelta, state.compareWidth, state.compareHeight, true) {
			return prestackFigurePayload{}, errors.New("no completed Compare image")
		}
		p.Options.Width, p.Options.Height = state.compareWidth, state.compareHeight
		p.Panels = prestackComparePanelRectsForContent(p.Content, prestackCompareSummaryHeight)
		p.Crop = p.Content
		p.CompareRows = append([]prestackcore.CompareLogicalRow(nil), state.compareDisplayRows...)
		p.CompareDenseRows, p.CompareAxisCompatible = prestackCompareUniformSides(p.CompareRows), state.compareBAxis.Compatible
		p.CompareAxis = state.compareBAxis
		p.CompareUseLimits, p.CompareLimitMin, p.CompareLimitMax = state.compareUseLimits, state.compareLimitMin, state.compareLimitMax
		p.CompareFilterMask = state.compareFilterMask
		p.CompareViewFirst, p.CompareViewLast = state.compareViewFirst, state.compareViewLast
		p.Summary, p.DeltaEmpty = prestackCompareSummaryLines(*state), prestackCompareDeltaEmptyMessage(*state)
		p.HasCompareBImage, p.HasCompareDeltaImage = len(state.compareB) > 0, len(state.compareDelta) > 0
		p.HasB = state.compareBDataset != nil
		if p.HasB {
			p.SourceBMetadata = state.compareBDataset.Metadata
		}
	}
	if err := p.validate(); err != nil {
		return prestackFigurePayload{}, err
	}
	return p, nil
}

func (p prestackFigurePayload) validate() error {
	if p.Version != prestackFigurePayloadVersion || (p.Page != 0 && p.Page != 3) {
		return errors.New("unsupported Prestack figure version or page")
	}
	if p.ClientWidth < 2 || p.ClientHeight < 2 || p.ClientWidth > 16384 || p.ClientHeight > 16384 || int64(p.ClientWidth)*int64(p.ClientHeight) > prestackFigureMaxCanvasPixels {
		return errors.New("invalid or excessive figure dimensions")
	}
	font := p.Font
	if font.Height == 0 || font.Height < -512 || font.Height > 512 || font.Width < -512 || font.Width > 512 ||
		font.Escapement < -3600 || font.Escapement > 3600 || font.Orientation < -3600 || font.Orientation > 3600 ||
		font.Weight < 0 || font.Weight > 1000 || font.Italic > 1 || font.Underline > 1 || font.StrikeOut > 1 {
		return errors.New("invalid or excessive figure font metrics")
	}
	terminated := false
	for _, ch := range font.FaceName {
		if ch == 0 {
			terminated = true
			break
		}
	}
	if font.FaceName[0] == 0 || !terminated {
		return errors.New("figure font face must be nonempty and NUL-terminated")
	}
	inside := func(r RECT) bool {
		return r.Left >= 0 && r.Top >= 0 && r.Right <= int32(p.ClientWidth) && r.Bottom <= int32(p.ClientHeight) && r.Right > r.Left && r.Bottom > r.Top
	}
	if !inside(p.Crop) || !inside(p.Scene) || !inside(p.Content) {
		return errors.New("invalid figure crop or layout")
	}
	layout := prestackPanelLayoutForSize(p.ClientWidth, p.ClientHeight, p.Page)
	if p.Scene != prestackLayoutRECT(layout.Scene) || p.Content != prestackLayoutRECT(layout.Content) {
		return errors.New("figure layout differs from the captured client dimensions")
	}
	o := p.Options
	if o.Width < 2 || o.Height < 2 || o.Width > 16384 || o.Height > 16384 || int64(o.Width)*int64(o.Height) > 64<<20 ||
		o.SampleStart < 0 || o.SampleEnd < o.SampleStart || o.SampleEnd >= p.SourceAMetadata.SamplesPerTrace ||
		!finiteFloat(o.GainPercent) || o.GainPercent < 0 || o.GainPercent > 49 {
		return errors.New("invalid figure raster or sample options")
	}
	if o.ClipPercent != 99 || o.Workers != 2 || o.ReadStrategy != segy.ReadStrategySparseMapped ||
		(o.DisplayMode != segy.DisplayAdaptive && o.DisplayMode != segy.DisplayNearest) {
		return errors.New("unsupported figure renderer options")
	}
	if !paletteIndexValid(p.Palette) || p.PaletteRGB != prestackFigurePalette(p.Palette) {
		return errors.New("figure palette is unsupported by this renderer")
	}
	if p.SourceAMetadata.SampleIntervalUS <= 0 || p.SourceAMetadata.TraceCount <= 0 || p.SourceAMetadata.SamplesPerTrace <= 0 {
		return errors.New("invalid figure sample interval")
	}
	validTraces := func(traces []int64) bool {
		for _, trace := range traces {
			if trace < 0 || trace >= p.SourceAMetadata.TraceCount {
				return false
			}
		}
		return true
	}
	validPositions := func(positions []float64) bool {
		for _, position := range positions {
			if !finiteFloat(position) {
				return false
			}
		}
		return true
	}
	if len(p.Gather.TraceIndices) == 0 || len(p.Gather.TraceIndices) != len(p.Gather.Positions) ||
		!validTraces(p.Gather.TraceIndices) || !validPositions(p.Gather.Positions) ||
		!validTraces(p.LogicalTraces) || !validTraces(p.RenderedTraces) || !validTraces(p.RenderTraces) ||
		!validPositions(p.LogicalPositions) || !validPositions(p.RenderedPositions) ||
		len(p.LogicalTraces) != len(p.LogicalPositions) || len(p.RenderedTraces) != len(p.RenderedPositions) ||
		p.ViewFirst < 0 || p.ViewLast < p.ViewFirst || p.ViewLast >= len(p.Gather.TraceIndices) {
		return errors.New("invalid Gather trace identities, positions or view range")
	}
	if p.Page == 0 {
		if p.Crop != (RECT{Left: 0, Top: p.Scene.Top - 25, Right: int32(p.ClientWidth), Bottom: p.Scene.Bottom + 28}) {
			return errors.New("Gather figure crop omits or changes the production axes")
		}
		if p.Display < 0 || p.Display > 1 || len(p.RenderTraces) == 0 || len(p.RenderedTraces) != o.Width || len(p.RenderedPositions) != o.Width ||
			len(p.LogicalTraces) != len(p.LogicalPositions) || len(p.LogicalTraces) != p.ViewLast-p.ViewFirst+1 ||
			!validTraces(p.LogicalTraces) || !validTraces(p.RenderedTraces) || !validTraces(p.RenderTraces) ||
			!validPositions(p.LogicalPositions) || !validPositions(p.RenderedPositions) {
			return errors.New("invalid Gather source-column snapshot")
		}
		for i, trace := range p.LogicalTraces {
			if trace != p.Gather.TraceIndices[p.ViewFirst+i] || p.LogicalPositions[i] != p.Gather.Positions[p.ViewFirst+i] {
				return errors.New("logical Gather selection differs from the captured view")
			}
		}
		width, height := minInt(4096, maxInt(2, layout.Scene.width())), minInt(1536, maxInt(2, layout.Scene.height()))
		traces, positions := p.LogicalTraces, p.LogicalPositions
		mode := segy.DisplayAdaptive
		if p.Selection.Axis == prestackcore.AxisOffset {
			traces, positions = prestackOffsetColumns(traces, positions, width)
			mode = segy.DisplayNearest
		} else if p.Selection.Axis != prestackcore.AxisTrace {
			return errors.New("unsupported Gather axis")
		}
		if p.Display == 1 {
			width = prestackWiggleRasterWidth(len(traces), width, p.WiggleDecimation)
			mode = segy.DisplayNearest
		}
		if o.Width != width || o.Height != height || o.DisplayMode != mode {
			return errors.New("Gather raster is stale or differs from production dimensions")
		}
		frameTraces, framePositions := prestackOutputColumns(traces, positions, width)
		for i, trace := range frameTraces {
			if trace != p.RenderedTraces[i] || framePositions[i] != p.RenderedPositions[i] {
				return errors.New("Gather raster-column mapping is inconsistent")
			}
		}
		input := traces
		if mode == segy.DisplayNearest {
			input = frameTraces
		}
		if len(input) != len(p.RenderTraces) {
			return errors.New("Gather rendering sources are inconsistent")
		}
		for i, trace := range input {
			if trace != p.RenderTraces[i] {
				return errors.New("Gather rendering sources are inconsistent")
			}
		}
	} else {
		if p.Crop != p.Content || p.Panels != prestackComparePanelRectsForContent(p.Content, prestackCompareSummaryHeight) {
			return errors.New("Compare figure geometry differs from production layout")
		}
		raster := prestackCompareRasterRect(p.Panels[0])
		if o.Width != int(raster.Right-raster.Left) || o.Height != int(raster.Bottom-raster.Top) || int64(o.Width)*int64(o.Height) > prestackFigureMaxComparePixels {
			return errors.New("Compare raster is stale or exceeds the 4-megapixel-per-panel export budget; reduce the window size")
		}
		if o.DisplayMode != segy.DisplayAdaptive || p.CompareDenseRows != prestackCompareUniformSides(p.CompareRows) {
			return errors.New("inconsistent Compare render policy")
		}
		if len(p.Summary) != 2 {
			return errors.New("invalid Compare summary snapshot")
		}
		for _, panel := range p.Panels {
			if !inside(panel) {
				return errors.New("invalid Compare panel rectangle")
			}
		}
		for _, row := range p.CompareRows {
			if row.HasA && (row.ATrace < 0 || row.ATrace >= p.SourceAMetadata.TraceCount) {
				return errors.New("Compare A trace is outside source file")
			}
			if row.HasB && (!p.HasB || row.BTrace < 0 || row.BTrace >= p.SourceBMetadata.TraceCount) {
				return errors.New("Compare B trace is outside source file")
			}
		}
		if !prestackCompareRangeIsValid(p.CompareUseLimits, p.CompareLimitMin, p.CompareLimitMax) {
			return errors.New("invalid Compare amplitude limits")
		}
	}
	return nil
}

func prestackFigureMetadataMatches(a, b dataset.Metadata) bool {
	return a.FileSize == b.FileSize && a.Endian == b.Endian && a.SampleIntervalUS == b.SampleIntervalUS &&
		a.SamplesPerTrace == b.SamplesPerTrace && a.FormatCode == b.FormatCode && a.BytesPerSample == b.BytesPerSample &&
		a.DataStart == b.DataStart && a.TraceBytes == b.TraceBytes && a.TraceCount == b.TraceCount
}

// RenderPrestackFigure reopens real SEG-Y samples and uses the production
// renderer plus production painters. It creates no HWND and mutates no live
// session; callbacks/auto-indexing/geometry detection are deliberately absent.
func RenderPrestackFigure(ctx context.Context, p prestackFigurePayload, sourceAPath, sourceBPath string) (*image.NRGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	a, err := dataset.NewManager().Open(sourceAPath)
	if err != nil {
		return nil, err
	}
	if !prestackFigureMetadataMatches(p.SourceAMetadata, a.Metadata) {
		return nil, errors.New("figure source A metadata changed")
	}
	state := p.paintState()
	state.dataset = a
	if p.Page == 0 {
		reader, err := a.OpenReader()
		if err != nil {
			return nil, err
		}
		state.indices, state.stats, err = reader.RenderTraceIndices(p.RenderTraces, p.Options.renderOptions())
		reader.Close()
		if err != nil {
			return nil, err
		}
		state.bgra = crookedPaletteBGRA(state.indices, p.Palette)
	} else {
		var b *dataset.SeismicDataset
		if p.HasB {
			b, err = dataset.NewManager().Open(sourceBPath)
			if err != nil {
				return nil, err
			}
			if !prestackFigureMetadataMatches(p.SourceBMetadata, b.Metadata) {
				return nil, errors.New("figure source B metadata changed")
			}
		}
		result := renderPrestackCompareFrame(ctx, prestackCompareRenderRequest{
			token: prestackCompareRenderResult{width: p.Options.Width, height: p.Options.Height, palette: p.Palette, sampleFirst: int64(p.Options.SampleStart), sampleLast: int64(p.Options.SampleEnd)},
			aData: a, bData: b, rows: p.CompareRows, denseRows: p.CompareDenseRows, agc: p.Options.AGC, gain: p.Options.GainPercent,
			useLimits: p.CompareUseLimits, limitMin: p.CompareLimitMin, limitMax: p.CompareLimitMax, axisCompatible: p.CompareAxisCompatible})
		if result.err != nil {
			return nil, result.err
		}
		if result.bErr != nil {
			return nil, result.bErr
		}
		if (len(result.b) > 0) != p.HasCompareBImage || (len(result.delta) > 0) != p.HasCompareDeltaImage {
			return nil, errors.New("replayed Compare panel availability differs from captured frame")
		}
		state.compareA, state.compareB, state.compareDelta = result.a, result.b, result.delta
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return PaintPrestackFigurePayload(p, &state)
}

func (p prestackFigurePayload) paintState() prestackSession {
	return prestackSession{dataset: &dataset.SeismicDataset{Metadata: p.SourceAMetadata}, page: p.Page,
		selection: p.Selection, gather: p.Gather, viewFirst: p.ViewFirst, viewLast: p.ViewLast,
		sampleFirst: p.Options.SampleStart, sampleLast: p.Options.SampleEnd,
		imageWidth: p.Options.Width, imageHeight: p.Options.Height, display: p.Display,
		renderedTraces: p.RenderedTraces, renderedPositions: p.RenderedPositions,
		compareWidth: p.Options.Width, compareHeight: p.Options.Height}
}

// PaintPrestackFigurePayload freezes current committed buffers as the export
// reference. Only pixel buffers are borrowed; all presentation comes from the
// immutable payload, and transient drag/pick overlays are excluded.
func PaintPrestackFigurePayload(p prestackFigurePayload, committed *prestackSession) (*image.NRGBA, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	if committed == nil {
		return nil, errors.New("missing committed figure frame")
	}
	state := p.paintState()
	state.indices, state.bgra = committed.indices, committed.bgra
	state.compareA, state.compareB, state.compareDelta = committed.compareA, committed.compareB, committed.compareDelta
	if p.Page == 0 {
		if len(state.indices) != p.Options.Width*p.Options.Height || len(state.bgra) != p.Options.Width*p.Options.Height*4 {
			return nil, errors.New("Gather reference frame changed")
		}
	} else if !prestackValidCompareBuffer(state.compareA, p.Options.Width, p.Options.Height, false) ||
		!prestackValidCompareBuffer(state.compareB, p.Options.Width, p.Options.Height, true) ||
		!prestackValidCompareBuffer(state.compareDelta, p.Options.Width, p.Options.Height, true) {
		return nil, errors.New("Compare reference frame changed")
	}
	return paintPrestackFigureDIB(p, func(dc uintptr) {
		if p.Page == 0 {
			paintPrestackGatherView(dc, &state, p.Scene, p.Title)
		} else {
			paintPrestackCompareView(dc, &state, p.Content, p.Panels, p.Summary, p.DeltaEmpty)
		}
	})
}

func paintPrestackFigureDIB(p prestackFigurePayload, paint func(uintptr)) (*image.NRGBA, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	dc, _, callErr := pCreateCompatibleDC.Call(0)
	if dc == 0 {
		return nil, fmt.Errorf("create figure DC: %v", callErr)
	}
	defer pDeleteDC.Call(dc)
	bitmapInfo := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(p.ClientWidth), Height: -int32(p.ClientHeight), Planes: 1, BitCount: 32, Compression: BI_RGB}}
	var bits uintptr
	bitmap, _, callErr := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bitmapInfo)), DIB_RGB_COLORS, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == 0 {
		return nil, fmt.Errorf("create figure bitmap: %v", callErr)
	}
	oldBitmap, _, _ := pSelectObject.Call(dc, bitmap)
	defer func() { pSelectObject.Call(dc, oldBitmap); pDeleteObject.Call(bitmap) }()
	font, _, callErr := pFigureCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&p.Font)))
	if font == 0 {
		return nil, fmt.Errorf("create figure font: %v", callErr)
	}
	oldFont, _, _ := pSelectObject.Call(dc, font)
	defer func() { pSelectObject.Call(dc, oldFont); pDeleteObject.Call(font) }()
	canvas := RECT{Right: int32(p.ClientWidth), Bottom: int32(p.ClientHeight)}
	white, _, _ := pGetStockObject.Call(WHITE_BRUSH)
	pFillRect.Call(dc, uintptr(unsafe.Pointer(&canvas)), white)
	pSetBkMode.Call(dc, TRANSPARENT)
	pSetTextColor.Call(dc, rgbRef(0, 0, 0))
	paint(dc)
	pFigureGDIFlush.Call()
	width, height := int(p.Crop.Right-p.Crop.Left), int(p.Crop.Bottom-p.Crop.Top)
	output := image.NewNRGBA(image.Rect(0, 0, width, height))
	source := unsafe.Slice((*byte)(unsafe.Pointer(bits)), p.ClientWidth*p.ClientHeight*4)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			si := ((y+int(p.Crop.Top))*p.ClientWidth + x + int(p.Crop.Left)) * 4
			di := y*output.Stride + x*4
			output.Pix[di], output.Pix[di+1], output.Pix[di+2], output.Pix[di+3] = source[si+2], source[si+1], source[si], 255
		}
	}
	return output, nil
}
