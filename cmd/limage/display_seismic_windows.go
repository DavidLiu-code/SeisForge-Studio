//go:build windows

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"unsafe"

	display "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const mainDisplayKey = "seismic-2d"

var mainDisplayValues []float64
var mainDisplayOriginMS float64
var mainDisplayIdentity string

var compareFixedColorScale [3]struct {
	a, b         string
	manual       bool
	gain, lo, hi float64
	rangeValue   display.Range
}

func seismicDisplayColor(st segy.RenderStats, pal [256]rgb, normalized bool) display.ColorMapping {
	lo, hi := st.MapMin, st.MapMax
	if !seismicFinite(lo) || !seismicFinite(hi) || hi <= lo {
		lo, hi = -1, 1
	}
	title := "振幅"
	if normalized {
		title = "归一化显示值"
	}
	return display.ColorMapping{Range: display.Range{Min: lo, Max: hi}, Title: title, Format: display.DefaultNumberFormat, Hidden: true, Stops: display.LegacyStops(func(i int) display.RGB {
		p := pal[i]
		return display.RGB{R: p.r, G: p.g, B: p.b}
	})}
}

func seismicFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func seismicMappingDomain(normalized bool) string {
	if normalized {
		return "agc-rms"
	}
	return "amplitude"
}

func seismicCurrentMappingOwner(owner uintptr, key, identity, domain string) uintptr {
	o := displayOwners[owner]
	if o == nil {
		return 0
	}
	if s := o.surfaces[key]; s == nil || s.s.Identity != identity || s.s.ColorDomain != domain {
		return 0
	}
	return owner
}

func seismicColors(owner uintptr, key string, values []float64, indices []byte, palette [256]rgb) []byte {
	pixels := make([]byte, len(indices)*4)
	if len(values) != len(indices) || !displayRenderColor(owner, key, values, pixels) {
		for i, j := range indices {
			p := palette[j]
			if i < len(values) && !seismicFinite(values[i]) {
				p = rgb{255, 255, 255}
			}
			pixels[i*4], pixels[i*4+1], pixels[i*4+2] = p.b, p.g, p.r
		}
	}
	return pixels
}

func seismicByteIndices(values []float64, st segy.RenderStats) []byte {
	return segy.MapAmplitudeValues(values, st.MapMin, st.MapMax)
}

func seismicCurrentColor(owner uintptr, key string, fallback display.ColorMapping) display.ColorMapping {
	// Layout is measured before surface registration. Never carry the prior
	// dataset's visible legend into the first frame of a replacement dataset.
	if owner == hwnd && key == mainDisplayKey && !seismicSettingIdentityMatches(owner, key, mainDisplayIdentity) {
		return fallback
	}
	if owner == compareHwnd {
		for panel := range 3 {
			if key == seismicCompareKey(panel) && !seismicSettingIdentityMatches(owner, key, seismicCompareIdentity(panel)) {
				return fallback
			}
		}
	}
	return poststackLiveColorRange(displayEffectiveLegend(owner, key, fallback), fallback)
}

func seismicReplacePalette(owner uintptr, key string, pal [256]rgb) {
	o := displayOwners[owner]
	if o == nil {
		return
	}
	e := o.surfaces[key]
	if e == nil || e.saved == nil || e.saved.Color == nil {
		return
	}
	e.saved.Color.Stops = display.LegacyStops(func(i int) display.RGB { p := pal[i]; return display.RGB{R: p.r, G: p.g, B: p.b} })
	e.saved.Color.Reverse = false
}

func seismicUseCompareToolbarPalette(index int) string {
	key := seismicCompareKey(0)
	linked, follow := displayCompareOptions(compareHwnd, key)
	if o := displayOwners[compareHwnd]; o != nil {
		for panel := range 3 {
			k := seismicCompareKey(panel)
			e := o.surfaces[k]
			if e == nil || e.s.Color == nil || e.saved != nil ||
				(panel == 1 && linked) || (panel == 2 && follow) {
				continue
			}
			// Freeze already displayed, independent panels which haven't needed
			// an explicit mapping yet, before replacing the legacy fallback LUT.
			s := displayDefaults(e.s)
			e.saved = &s
		}
	}
	comparePaletteIndex = index
	seismicReplacePalette(compareHwnd, key, compareCurrentPalette())
	if !compareWorkspaceActive {
		return "当前剖面"
	}
	if linked && follow {
		return "一致"
	}
	return "独立 · A"
}

func seismicStatsAddTrace(stats *display.Stats, values []float64, normalized bool) {
	rms := 1.0
	if normalized {
		sum, n := 0.0, 0
		for _, v := range values {
			if seismicFinite(v) {
				sum += v * v
				n++
			}
		}
		if sum > 0 && n > 0 {
			rms = math.Sqrt(sum / float64(n))
		}
	}
	for _, v := range values {
		stats.Add(v)
		if normalized {
			stats.AddMapped(v / rms)
		}
	}
}

type seismicRowsForStats struct {
	path             string
	first, last      int
	traces           []int64
	start, end, step int64 // contiguous/ranked fallback if traces is nil
	normalized       bool
}

func (r seismicRowsForStats) walk(ctx context.Context, f *segy.File, add func([]float64)) error {
	read := func(tr int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		v, err := f.ReadTraceWindow(tr, r.first, r.last)
		if err == nil {
			add(v)
		}
		return err
	}
	if r.traces != nil {
		for _, t := range r.traces {
			if err := read(t); err != nil {
				return err
			}
		}
	} else {
		step := maxInt64(r.step, 1)
		for t := r.start; t <= r.end; t += step {
			if err := read(t); err != nil {
				return err
			}
			if r.end-t < step {
				break
			}
		}
	}
	return nil
}

func (r seismicRowsForStats) reader(open func(string) (*segy.File, error)) func(context.Context) (display.Stats, error) {
	return func(ctx context.Context) (display.Stats, error) {
		var stats display.Stats
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		f, err := open(r.path)
		if err != nil {
			return stats, err
		}
		defer f.Close()
		err = r.walk(ctx, f, func(v []float64) { seismicStatsAddTrace(&stats, v, r.normalized) })
		return stats, err
	}
}

func seismicAxis(id, title, unit string, dv, vv display.Range, down bool) display.Axis {
	return display.Axis{ID: id, Title: title, Unit: unit, BaseUnit: unit, Data: dv, View: vv, Down: down, StyleOnly: dv.Min >= dv.Max}
}

func seismicAxisRange(lo, hi float64) display.Range {
	if hi < lo {
		lo, hi = hi, lo
	}
	return display.Range{Min: lo, Max: hi}
}

func seismicAxisBounds(a, b int) display.Range { return seismicAxisRange(float64(a), float64(b)) }

func seismicMainEqualTicks() bool { return sf != nil }

func seismicCompareEqualTicks() bool { return compareMode != 2 }

func seismicPlotTitleFont(owner uintptr, key string) float64 {
	s, _ := displayCommitted(owner, key)
	return s.PlotTitleFontPT
}

func seismicMainAxes() (display.Axis, display.Axis) {
	x, y := seismicMainNativeAxes()
	x.Format, y.Format = display.DefaultNumberFormat, display.DefaultNumberFormat
	if displayDistanceSettingsFor(hwnd).Enabled {
		if view := displayMainDistanceNativeBounds(); x.ValidRange(view) {
			x.View = view
		}
		x = displayDistanceAxis(hwnd, x)
	}
	return x, y
}

func seismicMainNativeAxes() (display.Axis, display.Axis) {
	st := lastStats
	x := seismicAxis("trace", "物理道", "", seismicAxisRange(float64(originTraceStart+1), float64(originTraceEnd+1)), seismicAxisRange(float64(st.TraceStart+1), float64(st.TraceEnd+1)), false)
	y := seismicAxis("time", "时间", "ms", seismicAxisBounds(0, 0), seismicAxisBounds(0, 0), true)
	y.Unit = "s" // legacy 2D labels use seconds; canonical samples remain ms
	if sf == nil {
		return x, y
	}
	x.AllowsPoint = sf.Info.TraceCount > 0
	if !originValid {
		x.Data = seismicAxisRange(1, float64(sf.Info.TraceCount))
		x.StyleOnly = x.Data.Min >= x.Data.Max
	}
	dt := float64(sf.Info.SampleIntervalUS) / 1000
	first, last := 0, sf.Info.SamplesPerTrace-1
	if originValid {
		first, last = originSampleStart, originSampleEnd
	}
	y.Data = seismicAxisRange(mainDisplayOriginMS+float64(first)*dt, mainDisplayOriginMS+float64(last)*dt)
	y.View = seismicAxisRange(mainDisplayOriginMS+float64(st.SampleStart)*dt, mainDisplayOriginMS+float64(st.SampleEnd)*dt)
	y.StyleOnly = y.Data.Min >= y.Data.Max
	return x, y
}

func seismicMainReverse() (bool, bool) {
	if sf == nil || !seismicSettingIdentityMatches(hwnd, mainDisplayKey, mainDisplayIdentity) {
		return false, false
	}
	return displayAxisFor(hwnd, mainDisplayKey, display.Axis{ID: "trace"}).Reverse, displayAxisFor(hwnd, mainDisplayKey, display.Axis{ID: "time", Unit: "ms"}).Reverse
}

func seismicApplyMain(s display.Settings) error {
	if sf == nil {
		return fmt.Errorf("当前数据已关闭")
	}
	x, y := seismicMainAxes()
	if err := seismicValidateView(s, x, y); err != nil {
		return err
	}
	oldT0, oldT1, oldS0, oldS1 := traceStart, traceEnd, sampleStart, sampleEnd
	for _, a := range s.Axes {
		if a.ViewEdited && a.ID == x.ID && a.View != x.View {
			r, err := seismicNativeRange(hwnd, a)
			if err != nil {
				return err
			}
			traceStart, traceEnd = int64(math.Ceil(r.Min-1e-9))-1, int64(math.Floor(r.Max+1e-9))-1
		}
		if a.ViewEdited && a.ID == y.ID && a.View != y.View {
			dt := float64(sf.Info.SampleIntervalUS) / 1000
			if dt <= 0 {
				traceStart, traceEnd = oldT0, oldT1
				return fmt.Errorf("没有可用的时间采样间隔")
			}
			sampleStart, sampleEnd = int(math.Ceil((a.View.Min-mainDisplayOriginMS)/dt-1e-9)), int(math.Floor((a.View.Max-mainDisplayOriginMS)/dt+1e-9))
		}
	}
	if traceEnd < traceStart || sampleEnd < sampleStart {
		traceStart, traceEnd, sampleStart, sampleEnd = oldT0, oldT1, oldS0, oldS1
		return fmt.Errorf("当前范围内没有有效道或样点")
	}
	if traceStart != oldT0 || traceEnd != oldT1 || sampleStart != oldS0 || sampleEnd != oldS1 {
		if !rerender() {
			traceStart, traceEnd, sampleStart, sampleEnd = oldT0, oldT1, oldS0, oldS1
			return fmt.Errorf("无法绘制所选范围")
		}
	} else {
		applyPalette()
	}
	pInvalidateRect.Call(hwnd, 0, 0)
	return nil
}

func seismicNativeRange(owner uintptr, a display.Axis) (display.Range, error) {
	if !displayAxisIsDistance(a) {
		return a.View, nil
	}
	lo, lok := displayDistanceToNative(owner, a, a.View.Min)
	hi, hok := displayDistanceToNative(owner, a, a.View.Max)
	r := display.Range{Min: lo, Max: hi}
	if !lok || !hok || !a.ValidRange(r) {
		return display.Range{}, fmt.Errorf("当前距离范围不能对应到有效的剖面道号")
	}
	return r, nil
}

func seismicValidateView(s display.Settings, x, y display.Axis) error {
	for _, a := range s.Axes {
		if !a.ViewEdited {
			continue
		}
		d := x.Data
		if a.ID == y.ID {
			d = y.Data
		} else if a.ID != x.ID {
			continue
		}
		if !a.ValidRange(a.View) || !a.ValidRange(d) || a.View.Min < d.Min || a.View.Max > d.Max {
			return fmt.Errorf("%s：显示范围必须在数据范围内（%.8g … %.8g）", a.Title, a.DisplayValue(d.Min), a.DisplayValue(d.Max))
		}
	}
	return nil
}

func seismicRegisterMain(r RECT) {
	if sf == nil {
		return
	}
	x, y := seismicMainAxes()
	c := seismicDisplayColor(lastStats, currentPalette(), agc)
	layout := displayTranslateLayout(seismicMainLayout(), r)
	reader := seismicRowsForStats{path: sf.Info.Path, start: lastStats.TraceStart, end: lastStats.TraceEnd, step: lastStats.TraceStep,
		first: lastStats.SampleStart, last: lastStats.SampleEnd, normalized: agc}
	same := seismicSettingIdentityMatches(hwnd, mainDisplayKey, mainDisplayIdentity)
	domain := seismicMappingDomain(agc)
	sameDomain := seismicCurrentMappingOwner(hwnd, mainDisplayKey, mainDisplayIdentity, domain) != 0
	displayRegister(hwnd, mainDisplayKey, displaySurface{Title: "二维剖面", Identity: mainDisplayIdentity, LiveViewport: true, equalTicks: seismicMainEqualTicks(), distance: displayDistanceCanUse(hwnd),
		StatsVersion: fmt.Sprintf("%d:%d:%d:%d:%d:%t", reader.start, reader.end, reader.step, reader.first, reader.last, agc),
		ColorDomain:  domain, Axes: []display.Axis{x, y}, Color: &c, Regions: layout.Regions, Plot: r, ColorRegion: layout.ColorRegion, AxisTicks: layout.AxisTicks, ColorTicks: layout.ColorTicks, ReadStats: reader.reader(segy.Open), Apply: seismicApplyMain})
	if _, ok := displayCommitted(hwnd, mainDisplayKey); ok && (!same || !sameDomain) {
		bgra = seismicColors(hwnd, mainDisplayKey, mainDisplayValues, indices, currentPalette())
	}
}

func seismicMainDecorate(dc uintptr, r RECT) {
	if sf == nil {
		return
	}
	x, y := seismicMainAxes()
	x, y = displayAxisFor(hwnd, mainDisplayKey, x), displayAxisFor(hwnd, mainDisplayKey, y)
	p := displayTranslateLayout(seismicMainLayout(), r)
	color := seismicCurrentColor(hwnd, mainDisplayKey, seismicDisplayColor(lastStats, currentPalette(), agc))
	if p.Compare {
		displayDrawMeasuredCompareTitle(dc, p)
		displayDrawMeasuredCompareAxes(dc, p, x, y, nil)
		displayDrawMeasuredCompareColorbar(dc, p, color)
		return
	}
	displayDrawAxesPlanned(dc, r, x, y, p.Metrics, p.AxisTicks[x.ID], p.AxisTicks[y.ID], nil)
	displayDrawColorbarPlanned(dc, p.ColorRegion, color, p.Metrics, p.ColorTicks)
}

func seismicBar(r RECT) RECT {
	return RECT{Left: r.Right + 7, Right: r.Right + 76, Top: r.Top + 6, Bottom: r.Bottom - 6}
}

func seismicAxisRegions(r RECT, x, y display.Axis) map[string]RECT {
	m := displayAxisRegions(r, x.ID, y.ID)
	// Match the y-axis heading's actual position above the left-hand scale.
	// Nothing from either hit region overlaps the data image.
	a := m[y.ID]
	a.Top -= 20
	m[y.ID] = a
	return m
}

func seismicPlot(dc uintptr, r RECT, pixels []byte, w, h int, rx, ry bool) {
	if len(pixels) < w*h*4 || w < 1 || h < 1 {
		return
	}
	ww, hh := rectWH(r)
	if ww < 1 || hh < 1 {
		return
	}
	x, y := int(r.Left), int(r.Top)
	if rx {
		x += ww
		ww = -ww
	}
	if ry {
		y += hh
		hh = -hh
	}
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32, Compression: BI_RGB, SizeImage: uint32(len(pixels))}}
	pSetStretchBltMode.Call(dc, HALFTONE)
	pStretchDIBits.Call(dc, uintptr(x), uintptr(y), uintptr(ww), uintptr(hh), 0, 0, uintptr(w), uintptr(h), uintptr(unsafe.Pointer(&pixels[0])), uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, SRCCOPY)
}

func seismicAxisTimeText(ms float64, axis display.Axis) string {
	switch axis.Unit {
	case "s", "ms", "µs":
		if axis.Format == display.AutoFormat && seismicFinite(ms) {
			value := math.Round(ms*10) / 10
			if axis.Unit == "ms" {
				return display.FormatNumber(value, display.FixedFormat, 1) + " ms"
			}
			n := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(axis.DisplayValue(value), 'f', 4, 64), "0"), ".")
			return n + " " + axis.Unit
		}
		return display.FormatNumber(axis.DisplayValue(ms), axis.Format, axis.Digits) + " " + axis.Unit
	}
	return seismicDefaultTimeText(ms)
}

func seismicDefaultTimeText(ms float64) string {
	return display.FormatNumber(ms, display.FixedFormat, 1) + " ms"
}

// BMP uses the same GDI scene compositor, including its axis and colorbar.
// The seismic sample/SEG-Y export paths do not pass through this function.
func saveSeismicDisplayBMP(path string, w, h int, paint func(uintptr)) error {
	if w < 1 || h < 1 || int64(w)*int64(h) > 32_000_000 {
		return fmt.Errorf("图像尺寸无效")
	}
	dc, _, _ := pCreateCompatibleDC.Call(0)
	if dc == 0 {
		return fmt.Errorf("无法创建图像画布")
	}
	defer pDeleteDC.Call(dc)
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32, Compression: BI_RGB}}
	var bits uintptr
	bmp, _, _ := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == 0 {
		if bmp != 0 {
			pDeleteObject.Call(bmp)
		}
		return fmt.Errorf("无法创建图像位图")
	}
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(dc, bmp)
	defer pSelectObject.Call(dc, old)
	if hFont != 0 {
		pSelectObject.Call(dc, hFont)
	}
	pSetBkMode.Call(dc, TRANSPARENT)
	pSetTextColor.Call(dc, rgbRef(0, 0, 0))
	wh, _, _ := pGetStockObject.Call(WHITE_BRUSH)
	rect := RECT{Right: int32(w), Bottom: int32(h)}
	pFillRect.Call(dc, uintptr(unsafe.Pointer(&rect)), wh)
	paint(dc)
	gdi32.NewProc("GdiFlush").Call()
	stride := (w*3 + 3) &^ 3
	out := make([]byte, 54+stride*h)
	out[0], out[1] = 'B', 'M'
	for off, v := range map[int]uint32{2: uint32(len(out)), 10: 54, 14: 40, 18: uint32(w), 22: uint32(h), 34: uint32(stride * h)} {
		binary.LittleEndian.PutUint32(out[off:], v)
	}
	binary.LittleEndian.PutUint16(out[26:], 1)
	binary.LittleEndian.PutUint16(out[28:], 24)
	raw := unsafe.Slice((*byte)(unsafe.Pointer(bits)), w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			from := ((h-y-1)*w + x) * 4
			to := 54 + y*stride + x*3
			copy(out[to:to+3], raw[from:from+3])
		}
	}
	return os.WriteFile(path, out, 0644)
}

func seismicCompareKey(panel int) string { return fmt.Sprintf("poststack-%d-%d", compareMode, panel) }

func seismicComparePanel(panel int) *comparePanel {
	switch panel {
	case 0:
		return &compareA
	case 1:
		return &compareB
	case 2:
		return &compareD
	}
	return nil
}

func seismicSettingIdentityMatches(owner uintptr, key, id string) bool {
	o := displayOwners[owner]
	if o == nil {
		return false
	}
	e := o.surfaces[key]
	return e != nil && e.s.Identity == id
}

func seismicSetPanelTime(p *comparePanel, f *segy.File, traces []int64) {
	if len(traces) == 0 || f == nil {
		return
	}
	origin, err := f.TraceTimeOriginUS(traces[0])
	if err != nil {
		return
	}
	p.sampleOriginUS, p.sampleDtUS = origin, f.Info.SampleIntervalUS
}

func seismicSetSliceValues(p *comparePanel, v []float32, mask []bool, lo, hi float64) {
	p.displayValues = seismicSliceNumeric(v, mask)
	p.stats.MapMin, p.stats.MapMax = lo, hi
	p.indices = seismicByteIndices(p.displayValues, p.stats)
}

func seismicCompareAxes(panel int) (display.Axis, display.Axis) {
	x, y := seismicCompareNativeAxes(panel)
	if seismicCompareEqualTicks() {
		x.Format, y.Format = display.DefaultNumberFormat, display.DefaultNumberFormat
		// Compare shares the same physical distance viewport, also when one
		// panel's actual samples cover only a subset of that viewport.
		if displayDistanceSettingsFor(compareHwnd).Enabled {
			if panel > 0 {
				ax, _ := seismicCompareNativeAxes(0)
				if ax.ID == x.ID {
					x.Data, x.View, x.StyleOnly, x.AllowsPoint = ax.Data, ax.View, ax.StyleOnly, ax.AllowsPoint
				}
			}
			if compareTD == nil {
				if bounds := displayCompareDistanceNativeBounds(0); x.ValidRange(bounds) {
					x.View = bounds
				}
			}
		}
		x = displayDistanceAxis(compareHwnd, x)
	}
	return x, y
}

func seismicCompareNativeAxes(panel int) (display.Axis, display.Axis) {
	d := compareTD
	if d != nil && compareMode == 2 {
		b, data := d.bounds, d.fullBounds
		if data.CrosslineMax <= data.CrosslineMin || data.InlineMax <= data.InlineMin {
			data = d.ga.Bounds()
		}
		return seismicAxis("crossline", "Crossline", "", seismicAxisRange(float64(data.CrosslineMin), float64(data.CrosslineMax)), seismicAxisRange(float64(b.CrosslineMin), float64(b.CrosslineMax)), false),
			seismicAxis("inline", "Inline", "", seismicAxisRange(float64(data.InlineMin), float64(data.InlineMax)), seismicAxisRange(float64(b.InlineMin), float64(b.InlineMax)), true)
	}
	if d == nil {
		p := seismicComparePanel(panel)
		if p == nil {
			p = &compareA
		}
		if sf == nil {
			return seismicAxis("trace", "物理道", "", display.Range{}, display.Range{}, false), seismicAxis("time", "时间", "ms", display.Range{}, display.Range{}, true)
		}
		dt := float64(sf.Info.SampleIntervalUS) / 1000
		ts, te := compareOriginTraceStart, compareOriginTraceEnd
		if te < ts {
			ts, te = 0, sf.Info.TraceCount-1
		}
		s0, s1 := seismicCompareSampleBounds(sf)
		x := seismicAxis("trace", "物理道", "", seismicAxisRange(float64(ts+1), float64(te+1)), seismicAxisRange(float64(p.stats.TraceStart+1), float64(p.stats.TraceEnd+1)), false)
		x.AllowsPoint = len(p.bgra) > 0 || len(p.indices) > 0
		if panel > 0 {
			if _, _, ok := seismicComparePartial(panel, false); ok {
				a, _ := seismicCompareNativeAxes(0)
				x.Data, x.View, x.StyleOnly, x.AllowsPoint = a.Data, a.View, a.StyleOnly, a.AllowsPoint
			}
		}
		y := seismicAxis("time", "时间", "ms", seismicAxisRange(float64(s0)*dt, float64(s1)*dt), seismicAxisRange(float64(p.stats.SampleStart)*dt, float64(p.stats.SampleEnd)*dt), true)
		y.AllowsPoint = x.AllowsPoint && dt > 0
		return x, y
	}
	g, f := d.ga, d.fa
	if panel == 1 && d.fb != nil && d.gb != nil {
		g, f = d.gb, d.fb
	}
	axisID, axisTitle := "crossline", "Crossline"
	if compareMode == 1 {
		axisID, axisTitle = "inline", "Inline"
	}
	if d.traceOrderFallback {
		axisID, axisTitle = "trace", "物理道"
	}
	x0, x1 := viewFilterPanelRange(panel)
	if p := seismicComparePanel(panel); p != nil && len(p.coords) > 0 {
		x0, x1 = float64(p.coords[0]), float64(p.coords[len(p.coords)-1])
	}
	dataX := seismicAxisRange(x0, x1)
	if g != nil && f != nil {
		tr, co, _, err := g.LineTraceNumbers(compareMode, compareLineCoord)
		if err == nil {
			start, end, step := compareOriginTraceStart, compareOriginTraceEnd, maxInt64(1, compareOriginTraceStep)
			if end < start || end >= f.Info.TraceCount || generatedPoststackTraceOrder(f.Info.Path) != nil {
				start, end, step = 0, f.Info.TraceCount-1, 1
			}
			tr, co = filterPhysicalTraceRange(tr, co, start, end, step)
			if len(co) > 0 {
				dataX = seismicAxisRange(float64(co[0]), float64(co[len(co)-1]))
			}
		}
	}
	s0, s1 := seismicPanelTimeWindow(panel)
	t0, t1 := seismicCompareTimeMS(panel, float64(s0)), seismicCompareTimeMS(panel, float64(s1))
	dataY := seismicAxisRange(t0, t1)
	if f != nil {
		lb, ub := seismicCompareSampleBounds(d.fa)
		if panel == 1 && d.fa != nil && (f != d.fa) {
			// The comparison viewport is linked in A's physical time. The B
			// panel advertises its own valid limits, but never pads absent data.
			offset := 0
			if frame := committedViewFilter(); frame != nil {
				offset = frame.rows[panel].offset
			}
			lb, ub = offset, offset+f.Info.SamplesPerTrace-1
		}
		dataY = seismicAxisRange(seismicCompareTimeMS(panel, float64(lb)), seismicCompareTimeMS(panel, float64(ub)))
	}
	xAxis := seismicAxis(axisID, axisTitle, "", dataX, seismicAxisRange(x0, x1), false)
	if p := seismicComparePanel(panel); p != nil {
		xAxis.AllowsPoint = len(p.coords) > 0 && (len(p.bgra) > 0 || len(p.indices) > 0)
	}
	yAxis := seismicAxis("time", "时间", "ms", dataY, seismicAxisRange(t0, t1), true)
	yAxis.AllowsPoint = xAxis.AllowsPoint && f != nil && f.Info.SampleIntervalUS > 0
	if panel > 0 {
		ax, ay := seismicCompareNativeAxes(0)
		if _, _, ok := seismicComparePartial(panel, false); ok {
			xAxis.View, xAxis.Data = ax.View, ax.Data
			xAxis.StyleOnly, xAxis.AllowsPoint = ax.StyleOnly, ax.AllowsPoint
		}
		if _, _, ok := seismicComparePartial(panel, true); ok {
			yAxis.View, yAxis.Data = ay.View, ay.Data
			yAxis.StyleOnly, yAxis.AllowsPoint = ay.StyleOnly, ay.AllowsPoint
		}
	}
	return xAxis, yAxis
}

func seismicCompareSampleBounds(f *segy.File) (int, int) {
	if f == nil || f.Info.SamplesPerTrace <= 0 {
		return 0, 0
	}
	a, b := compareOriginSampleStart, compareOriginSampleEnd
	if a < 0 || b <= a || b >= f.Info.SamplesPerTrace {
		a, b = 0, f.Info.SamplesPerTrace-1
	}
	return a, b
}

func seismicCompareTimeMS(panel int, y float64) float64 {
	if t, ok := viewFilterTimeMS(panel, y); ok {
		return t
	}
	p := seismicComparePanel(panel)
	if p != nil && p.viewAxes && p.stats.SampleEnd >= p.stats.SampleStart {
		f := float64(p.stats.SampleEnd - p.stats.SampleStart)
		if f > 0 {
			return p.filterT0 + (y-float64(p.stats.SampleStart))*(p.filterT1-p.filterT0)/f
		}
		return p.filterT0
	}
	if p != nil && p.sampleDtUS > 0 {
		return (float64(p.sampleOriginUS) + y*float64(p.sampleDtUS)) / 1000
	}
	dt := compareDtAUS
	if dt <= 0 && compareTD != nil && compareTD.fa != nil {
		dt = compareTD.fa.Info.SampleIntervalUS
	}
	if dt <= 0 && sf != nil {
		dt = sf.Info.SampleIntervalUS
	}
	return y * float64(dt) / 1000
}

func seismicCompareReverse(panel int) (bool, bool) {
	// Direction is display-only. The linked physical ranges remain the
	// same even when one panel chooses a different screen orientation.
	x, y := seismicCompareEffectiveAxes(panel)
	return x.Reverse, y.Reverse
}

func seismicCompareFollowsAxes(panel int) bool {
	if !compareWorkspaceActive || !seismicCompareEqualTicks() || panel == 0 {
		return false
	}
	if o := displayOwners[compareHwnd]; o != nil {
		if local := o.surfaces[seismicCompareKey(panel)]; local != nil &&
			(local.compareAIdentity == "" || local.compareAIdentity == seismicCompareIdentity(0)) {
			return local.bAxesFollow
		}
	}
	// A new panel is measured before it is registered/painted.
	return true
}

func seismicCompareEffectiveAxes(panel int) (display.Axis, display.Axis) {
	x, y := seismicCompareAxes(panel)
	if seismicCompareFollowsAxes(panel) {
		ax, ay := seismicCompareEffectiveAxes(0)
		axes := displayFollowAxisStyle([]display.Axis{x, y}, []display.Axis{ax, ay})
		return axes[0], axes[1]
	}
	key := seismicCompareKey(panel)
	if seismicSettingIdentityMatches(compareHwnd, key, seismicCompareIdentity(panel)) {
		x, y = displayAxisFor(compareHwnd, key, x), displayAxisFor(compareHwnd, key, y)
	}
	return x, y
}

func seismicApplyCompare(panel int, s display.Settings) error {
	x, y := seismicCompareAxes(panel)
	if err := seismicValidateView(s, x, y); err != nil {
		return err
	}
	snap, ok := currentCompareViewSnapshot()
	if !ok {
		return fmt.Errorf("当前剖面不存在")
	}
	changed := false
	for _, axis := range s.Axes {
		if !axis.ViewEdited {
			continue
		}
		if axis.ID == x.ID && axis.View != x.View {
			changed = true
			if compareMode == 2 {
				snap.bounds.CrosslineMin, snap.bounds.CrosslineMax = int32(math.Ceil(axis.View.Min)), int32(math.Floor(axis.View.Max))
			} else {
				r, err := seismicNativeRange(compareHwnd, axis)
				if err != nil {
					return err
				}
				if r.IsPoint() {
					snap.lineXMin, snap.lineXMax = r.Min, r.Max
				} else {
					snap.lineXMin, snap.lineXMax = r.Min-1e-9, r.Max+1e-9
				}
			}
		}
		if axis.ID == y.ID && axis.View != y.View {
			changed = true
			if compareMode == 2 {
				snap.bounds.InlineMin, snap.bounds.InlineMax = int32(math.Ceil(axis.View.Min)), int32(math.Floor(axis.View.Max))
			} else {
				dt := compareDtAUS
				if compareTD != nil && compareTD.fa != nil {
					dt = compareTD.fa.Info.SampleIntervalUS
				}
				if dt <= 0 {
					return fmt.Errorf("没有可用的时间采样间隔")
				}
				origin := seismicCompareTimeMS(0, 0)
				snap.sampleStart = int(math.Ceil((axis.View.Min-origin)*1000/float64(dt) - 1e-9))
				snap.sampleEnd = int(math.Floor((axis.View.Max-origin)*1000/float64(dt) + 1e-9))
			}
		}
	}
	if changed {
		if compareMode == 2 {
			if snap.bounds.InlineMax <= snap.bounds.InlineMin || snap.bounds.CrosslineMax <= snap.bounds.CrosslineMin {
				return fmt.Errorf("所选区域至少需要两条线")
			}
		} else if snap.sampleEnd < snap.sampleStart {
			return fmt.Errorf("所选时间没有有效样点")
		}
		if compareMode != 2 && compareTD != nil && compareTD.ga != nil && snap.lineXMax >= snap.lineXMin {
			t, c, _, err := compareTD.ga.LineTraceNumbers(compareMode, compareLineCoord)
			if err != nil {
				return err
			}
			start, end, step := compareTraceRangeForGeometry(compareTD.fa)
			t, c = filterPhysicalTraceRange(t, c, start, end, step)
			t, _ = filterLineRange(t, c, snap.lineXMin, snap.lineXMax)
			if len(t) == 0 {
				return fmt.Errorf("所选范围没有有效道")
			}
		}
		if compareTD == nil {
			if snap.lineXMax >= snap.lineXMin {
				first, last := int64(math.Ceil(snap.lineXMin))-1, int64(math.Floor(snap.lineXMax))-1
				if first < 0 || last < first {
					return fmt.Errorf("所选范围没有有效道")
				}
				compareTraceStart, compareTraceEnd = first, last
			}
			compareSampleStart, compareSampleEnd = snap.sampleStart, snap.sampleEnd
			renderCompareSection()
		} else if !applyCompareViewSnapshot(snap) {
			return fmt.Errorf("无法绘制所选区域")
		}
	}
	seismicRefreshCompareColors()
	invalidateCompareBase()
	return nil
}

func seismicCompareIdentity(panel int) string {
	a := fmt.Sprintf("%d:%d:%s", atomic.LoadInt64(&poststackEpoch), viewFilter.active[0].BaseVersion, compareAPath)
	b := fmt.Sprintf("%d:%s", viewFilter.active[1].BaseVersion, compareBPath)
	switch panel {
	case 0:
		return a
	case 1:
		return b
	default:
		return a + "/" + b
	}
}

func seismicCompareCompareGroup() *displayCompareBinding {
	if !compareWorkspaceActive {
		return nil
	}
	readyB := compareActiveB() && (len(compareB.bgra) != 0 || len(compareB.indices) != 0)
	return &displayCompareBinding{
		Keys:    [3]string{seismicCompareKey(0), seismicCompareKey(1), seismicCompareKey(2)},
		CanLink: readyB && (!agc || compareMode == 2), Linked: true, FollowA: true, PoststackProfiles: true,
	}
}

func seismicCompareColorLinks() (bool, bool) {
	b := seismicCompareCompareGroup()
	if b == nil || !b.CanLink {
		return false, false
	}
	if o := displayOwners[compareHwnd]; o != nil {
		if v, ok := o.links[b.Keys]; ok {
			return v.Linked, v.FollowA
		}
	}
	return b.Linked, b.FollowA
}

func seismicCompareFollow() bool {
	_, follow := seismicCompareColorLinks()
	return follow
}

func seismicCompareDefaultColor(panel int, p comparePanel) display.ColorMapping {
	c := seismicDisplayColor(p.stats, compareCurrentPalette(), agc && panel < 2 && compareMode != 2)
	if !seismicCompareEqualTicks() {
		c.Format = display.AutoFormat // Preserve the Time Slice rendering contract.
	}
	if b := seismicCompareCompareGroup(); b != nil && b.CanLink && panel == 0 {
		aID, bID := seismicCompareIdentity(0), seismicCompareIdentity(1)
		fixed := &compareFixedColorScale[clampInt(compareMode, 0, 2)]
		gain := compareMappingGain()
		if fixed.a != aID || fixed.b != bID || fixed.manual != useLimits || fixed.gain != gain || useLimits && (fixed.lo != limitMin || fixed.hi != limitMax) || !fixed.rangeValue.Valid() {
			fixed.a, fixed.b, fixed.manual, fixed.gain, fixed.lo, fixed.hi = aID, bID, useLimits, gain, limitMin, limitMax
			fixed.rangeValue = seismicDisplayColor(compareA.stats, compareCurrentPalette(), false).Range
		}
		c.Range = fixed.rangeValue
	}
	return c
}

func seismicSliceNumeric(v []float32, mask []bool) []float64 {
	a := make([]float64, len(v))
	for i, x := range v {
		if i < len(mask) && mask[i] {
			a[i] = float64(x)
		} else {
			a[i] = math.NaN()
		}
	}
	return a
}

func seismicCompareColorsForPanel(panel int, p *comparePanel) {
	if len(p.indices) == 0 {
		return
	}
	indices := p.indices
	key := seismicCompareKey(panel)
	linked, follow := seismicCompareColorLinks()
	followsA := panel == 1 && linked || panel == 2 && follow
	_, explicit := displayCommitted(compareHwnd, key)
	owner := seismicCurrentMappingOwner(compareHwnd, key, seismicCompareIdentity(panel), seismicMappingDomain(agc && panel < 2 && compareMode != 2))
	if owner == 0 {
		explicit = false
	}
	if explicit && followsA {
		if a, active := displayCommitted(compareHwnd, seismicCompareKey(0)); !active || a.Color == nil {
			owner = 0
			explicit = false
		}
	}
	if explicit && len(p.displayValues) == len(indices) {
		if s, ok := displayCommitted(owner, key); ok && s.Color != nil {
			fresh := seismicCompareDefaultColor(panel, *p)
			if followsA {
				fresh = seismicCompareDefaultColor(0, compareA)
			}
			c := poststackLiveColorRange(*s.Color, fresh)
			pixels := make([]byte, len(indices)*4)
			if displayRenderColorMapping(&c, p.displayValues, pixels) {
				p.bgra = pixels
				return
			}
		}
	}
	if !explicit && len(p.displayValues) == len(indices) {
		c := seismicCompareDefaultColor(panel, *p)
		if followsA {
			c = seismicCompareDefaultColor(0, compareA)
		}
		if c.Range.Min != p.stats.MapMin || c.Range.Max != p.stats.MapMax {
			indices = segy.MapAmplitudeValues(p.displayValues, c.Range.Min, c.Range.Max)
		}
	}
	p.bgra = seismicColors(owner, key, p.displayValues, indices, compareCurrentPalette())
}

func seismicRefreshCompareColors() {
	for i := range 3 {
		seismicCompareColorsForPanel(i, seismicComparePanel(i))
	}
}

func seismicPanelColor(panel int, p comparePanel) display.ColorMapping {
	linked, follow := seismicCompareColorLinks()
	if panel == 1 && linked || panel == 2 && follow {
		local := seismicCurrentColor(compareHwnd, seismicCompareKey(panel), seismicCompareDefaultColor(panel, p))
		c := seismicCurrentColor(compareHwnd, seismicCompareKey(0), seismicCompareDefaultColor(0, compareA))
		displayCopyLegendStyle(&c, &local)
		return c
	}
	return seismicCurrentColor(compareHwnd, seismicCompareKey(panel), seismicCompareDefaultColor(panel, p))
}

func seismicComparePlotTitle(panel int) string {
	if panel < 0 || panel > 2 {
		return ""
	}
	prefix := [3]string{"A", "B", "A−B"}[panel]
	if compareTD == nil || compareTD.traceOrderFallback {
		return prefix + " · 物理道剖面"
	}
	orientation := "Inline"
	if compareMode == 1 {
		orientation = "Crossline"
	}
	line := compareLineCoord
	if p := seismicComparePanel(panel); p != nil {
		parts := strings.Fields(p.title)
		n := len(parts)
		if n >= 2 && parts[n-2] == orientation {
			if v, err := strconv.ParseInt(parts[n-1], 10, 32); err == nil {
				line = int32(v)
			}
		}
	}
	return fmt.Sprintf("%s · %s %d", prefix, orientation, line)
}

func seismicCopyTickStyle(dst *display.Settings, src display.Settings, color bool, axis int) {
	dst.PlotTitleFontPT = src.PlotTitleFontPT
	if color && dst.Color != nil && src.Color != nil {
		d, s := dst.Color, src.Color
		d.Divisions, d.TickLength, d.TickLengthSet = s.Divisions, s.TickLength, s.TickLengthSet
		d.Format, d.Digits = s.Format, s.Digits
		d.TickFontPT, d.TitleFontPT = s.TickFontPT, s.TitleFontPT
	}
	if !color && axis >= 0 && axis < len(src.Axes) && axis < len(dst.Axes) {
		d, s := &dst.Axes[axis], src.Axes[axis]
		d.Divisions, d.TickLength, d.TickLengthSet = s.Divisions, s.TickLength, s.TickLengthSet
		d.Format, d.Digits = s.Format, s.Digits
		d.TickFontPT, d.TitleFontPT = s.TickFontPT, s.TitleFontPT
	}
}

func seismicCompareApplyPanelStyle(src display.Settings, color bool, axis int) error {
	o := displayOwners[compareHwnd]
	if o == nil {
		return fmt.Errorf("对比视图已关闭")
	}
	var pending [3]*display.Settings
	for panel := range 3 {
		key := seismicCompareKey(panel)
		e := o.surfaces[key]
		if e == nil || e.order == 0 {
			continue
		}
		dst := displayDefaults(e.s)
		if e.saved != nil {
			dst = e.saved.Clone()
		}
		seismicCopyTickStyle(&dst, src, color, axis)
		if !display.ValidFontPT(dst.PlotTitleFontPT) {
			return fmt.Errorf("%s：图标题字号必须在 %d–%d pt 之间", seismicComparePlotTitle(panel), display.MinFontPT, display.MaxFontPT)
		}
		if color && dst.Color != nil {
			if err := dst.Color.Valid(); err != nil {
				return fmt.Errorf("%s：%w", seismicComparePlotTitle(panel), err)
			}
		} else if !color && axis >= 0 && axis < len(dst.Axes) {
			if err := dst.Axes[axis].Valid(); err != nil {
				return fmt.Errorf("%s：%w", seismicComparePlotTitle(panel), err)
			}
		}
		pending[panel] = &dst
	}
	for panel, dst := range pending {
		if dst == nil {
			continue
		}
		key := seismicCompareKey(panel)
		e := o.surfaces[key]
		e.saved = dst
		displayPersist(key, *dst)
	}
	displayCompareLayoutCache.valid = false
	displayInvalidateProfile(compareHwnd)
	return nil
}

func seismicRegisterCompare(r RECT, p comparePanel, panel int) comparePanel {
	x, y := seismicCompareAxes(panel)
	key := seismicCompareKey(panel)
	layout := seismicCompareLayout(panel, r)
	c := seismicCompareDefaultColor(panel, p)
	reader, version := seismicCompareStatsReader(panel, p)
	same := seismicSettingIdentityMatches(compareHwnd, key, seismicCompareIdentity(panel))
	domain := seismicMappingDomain(agc && panel < 2 && compareMode != 2)
	sameDomain := seismicCurrentMappingOwner(compareHwnd, key, seismicCompareIdentity(panel), domain) != 0
	var applyPanelStyle func(display.Settings, bool, int) error
	if seismicCompareEqualTicks() {
		applyPanelStyle = seismicCompareApplyPanelStyle
	}
	displayRegister(compareHwnd, key, displaySurface{Title: p.title, Identity: seismicCompareIdentity(panel), StatsVersion: version, LiveViewport: true,
		ColorDomain: domain, Axes: []display.Axis{x, y}, Color: &c, Regions: layout.Regions, Plot: r, ColorRegion: layout.ColorRegion, AxisTicks: layout.AxisTicks, ColorTicks: layout.ColorTicks, ReadStats: reader,
		equalTicks: seismicCompareEqualTicks(), distance: seismicCompareEqualTicks() && displayDistanceCanUse(compareHwnd), applyPanelStyle: applyPanelStyle,
		Compare: seismicCompareCompareGroup(), Apply: func(s display.Settings) error { return seismicApplyCompare(panel, s) }})
	if _, applied := displayCommitted(compareHwnd, key); applied && (!same || !sameDomain) {
		seismicCompareColorsForPanel(panel, seismicComparePanel(panel))
		p.bgra = seismicComparePanel(panel).bgra
	}
	return p
}

// Compact comparison retains Y ticks for each panel, but a shared Y title
// occupies just one gutter. An independently named, hidden or differently
// scaled axis keeps its own title. This is presentation, not an edit to the axes.
func seismicCompareCompactYAxis(panel int, y display.Axis) display.Axis {
	if panel == 0 || !compareWorkspaceActive {
		return y
	}
	_, a := seismicCompareEffectiveAxes(0)
	if !a.HideAxis && !a.HideTitle && !y.HideAxis && displayAxisTitle(a) == displayAxisTitle(y) &&
		a.BaseUnit == y.BaseUnit && a.Reverse == y.Reverse && a.View == y.View {
		y.HideTitle = true
	}
	return y
}

func seismicCompareDecorate(dc uintptr, r RECT, p comparePanel, panel int) {
	x, y := seismicCompareEffectiveAxes(panel)
	key := seismicCompareKey(panel)
	layout := seismicCompareLayout(panel, r)
	pos := seismicSurfacePosition(compareHwnd, key, x.View)
	color := seismicPanelColor(panel, p)
	if layout.Compare {
		y = seismicCompareCompactYAxis(panel, y)
		displayDrawMeasuredCompareTitle(dc, layout)
		displayDrawMeasuredCompareAxes(dc, layout, x, y, pos)
		if poststackWaveActive() {
			drawPoststackWaveLegend(dc, layout, color)
		} else {
			displayDrawMeasuredCompareColorbar(dc, layout, color)
		}
		return
	}
	displayDrawAxesPlanned(dc, r, x, y, layout.Metrics, layout.AxisTicks[x.ID], layout.AxisTicks[y.ID], pos)
	displayDrawColorbarPlanned(dc, layout.ColorRegion, color, layout.Metrics, layout.ColorTicks)
}

// Compare reserves the full A viewport in all line panels. An output B or
// residual which covers only a proper subset occupies only those coordinates;
// the rest stays blank. Ordinary time slices already have geometry masks.
func seismicComparePartial(panel int, vertical bool) (lo, hi float64, ok bool) {
	if panel == 0 || compareMode == 2 || !compareActiveB() {
		return 0, 1, false
	}
	b := seismicComparePanel(panel)
	if b == nil || len(b.bgra) == 0 || len(compareA.bgra) == 0 {
		return 0, 1, false
	}
	if !vertical && displayDistanceReference(compareHwnd) != nil {
		bounds, present := displayCompareVisibleDistanceBounds(panel)
		if !present {
			return 0, 1, false
		}
		if displayCompareDistanceNativeBounds(0).IsPoint() {
			return 0, 1, false // A's single trace is already the entire display span
		}
		if bounds.IsPoint() && len(compareA.coords) > 1 {
			fraction := func(v float64) (float64, bool) {
				return displayCompareDistanceFraction(0, v)
			}
			lo, hi, ok := seismicSingleTraceCell(compareA.coords, bounds.Min, fraction)
			// Where metadata proves a regular physical lattice, restrict even
			// the display-only cell to half a real grid increment on each side.
			if s := displayDistances[compareHwnd]; ok && s != nil && s.gridStep > 0 && hi > lo {
				if p, valid := fraction(bounds.Min - s.gridStep/2); valid {
					lo = max(lo, p)
				}
				if p, valid := fraction(bounds.Max + s.gridStep/2); valid {
					hi = min(hi, p)
				}
			}
			return lo, hi, ok
		}
		lo, lok := displayCompareDistanceFraction(0, bounds.Min)
		hi, hok := displayCompareDistanceFraction(0, bounds.Max)
		return lo, hi, lok && hok && hi >= lo
	}
	if compareTD == nil {
		if !vertical && b.stats.TraceStart == b.stats.TraceEnd && compareA.stats.TraceStart < compareA.stats.TraceEnd {
			first, last := compareA.stats.TraceStart, compareA.stats.TraceEnd
			trace := b.stats.TraceStart
			step := maxInt64(1, compareA.stats.TraceStep)
			if trace >= first && trace <= last && (trace-first)%step == 0 {
				span := float64(last - first)
				mid, half := float64(trace-first)/span, float64(step)/(2*span)
				return math.Max(0, mid-half), math.Min(1, mid+half), true
			}
		}
		return 0, 1, false
	}
	if vertical {
		a0, a1 := seismicPanelTimeWindow(0)
		b0, b1 := seismicPanelTimeWindow(panel)
		al, ah := seismicCompareTimeMS(0, float64(a0)), seismicCompareTimeMS(0, float64(a1))
		bl, bh := seismicCompareTimeMS(panel, float64(b0)), seismicCompareTimeMS(panel, float64(b1))
		if ah <= al || bl < al-1e-6 || bh > ah+1e-6 || bh < bl {
			return 0, 1, false
		}
		return math.Max(0, (bl-al)/(ah-al)), math.Min(1, (bh-al)/(ah-al)), true
	}
	if len(compareA.coords) < 2 || len(b.coords) < 1 {
		return 0, 1, false
	}
	if len(b.coords) == 1 {
		return seismicSingleTraceCell(compareA.coords, float64(b.coords[0]), func(v float64) (float64, bool) {
			return seismicAcrossFraction(compareA.coords, v)
		})
	}
	lo, lok := seismicAcrossFraction(compareA.coords, float64(b.coords[0]))
	hi, hok := seismicAcrossFraction(compareA.coords, float64(b.coords[len(b.coords)-1]))
	return lo, hi, lok && hok && hi >= lo
}

// A one-trace raster occupies only its display cell, without synthesizing
// adjacent traces or changing the real coordinate used by picking/export.
func seismicSingleTraceCell(coords []int32, v float64, fraction func(float64) (float64, bool)) (float64, float64, bool) {
	i := sort.Search(len(coords), func(i int) bool { return float64(coords[i]) >= v })
	if i == len(coords) || float64(coords[i]) != v {
		// Standalone B remains visible at its actual fractional position, but
		// a nonmatching point does not acquire the width of an A trace cell.
		mid, ok := fraction(v)
		return mid, mid, ok
	}
	mid, ok := fraction(v)
	if !ok {
		return 0, 1, false
	}
	lo, hi := 0.0, 1.0
	if i > 0 {
		if prev, ok := fraction(float64(coords[i-1])); ok {
			lo = (prev + mid) / 2
		}
	}
	if i+1 < len(coords) {
		if next, ok := fraction(float64(coords[i+1])); ok {
			hi = (next + mid) / 2
		}
	}
	return lo, hi, hi >= lo
}

func seismicCompareRasterRect(panel int, r RECT) RECT {
	rx, ry := seismicCompareReverse(panel)
	if lo, hi, ok := seismicComparePartial(panel, false); ok {
		if rx {
			lo, hi = 1-hi, 1-lo
		}
		w := maxInt(1, int(r.Right-r.Left)-1)
		left, right := r.Left+int32(math.Round(lo*float64(w))), r.Left+int32(math.Round(hi*float64(w)))+1
		r.Left, r.Right = min(left, r.Right-2), min(r.Right, max(left+2, right))
	}
	if lo, hi, ok := seismicComparePartial(panel, true); ok {
		if ry {
			lo, hi = 1-hi, 1-lo
		}
		h := maxInt(1, int(r.Bottom-r.Top)-1)
		top, bottom := r.Top+int32(math.Round(lo*float64(h))), r.Top+int32(math.Round(hi*float64(h)))+1
		r.Top, r.Bottom = min(top, r.Bottom-2), min(r.Bottom, max(top+2, bottom))
	}
	return r
}

func seismicDrawLineAxes(dc uintptr, r RECT, x, y display.Axis, coords []int32) {
	m := displayMetrics(dc, displayDCDPI(dc))
	position := func(v float64) float64 {
		f, ok := seismicAcrossFraction(coords, v)
		if !ok {
			return math.NaN()
		}
		return f
	}
	xp := displayAxisTickPlan(m, x, int(r.Right-r.Left), true, position)
	yp := displayAxisTickPlan(m, y, int(r.Bottom-r.Top), false, nil)
	displayDrawAxesPlanned(dc, r, x, y, m, xp, yp, position)
}

func seismicPanelTimeWindow(panel int) (int, int) {
	if s := committedViewFilter(); s != nil && panel >= 0 && panel < len(s.rows) && len(s.rows[panel].traces) > 0 {
		return viewFilterDisplayedSamplesForPanel(panel)
	}
	if p := seismicComparePanel(panel); p != nil && p.sampleDtUS > 0 {
		return p.stats.SampleStart, p.stats.SampleEnd
	}
	return compareSampleStart, compareSampleEnd
}

func seismicPanelCanonicalSample(panel int, sample float64) float64 {
	if committedViewFilter() != nil {
		return viewFilterAWorldSample(panel, sample)
	}
	if panel == 1 && compareA.sampleDtUS > 0 && compareB.sampleDtUS > 0 {
		return (float64(compareB.sampleOriginUS-compareA.sampleOriginUS) + sample*float64(compareB.sampleDtUS)) / float64(compareA.sampleDtUS)
	}
	return sample
}

func seismicPanelLocalSample(panel int, sample float64) float64 {
	if committedViewFilter() != nil {
		return viewFilterLocalPanelSample(panel, sample)
	}
	if panel == 1 && compareA.sampleDtUS > 0 && compareB.sampleDtUS > 0 {
		return (float64(compareA.sampleOriginUS-compareB.sampleOriginUS) + sample*float64(compareA.sampleDtUS)) / float64(compareB.sampleDtUS)
	}
	return sample
}

func seismicCompareStatsReader(panel int, p comparePanel) (func(context.Context) (display.Stats, error), string) {
	d := compareTD
	k := viewFilterCurrentKey()
	if snap := committedViewFilter(); snap != nil && len(snap.rows[panel].traces) > 0 {
		row := snap.rows[panel]
		normalized := agc && panel < 2
		return func(ctx context.Context) (display.Stats, error) {
			var st display.Stats
			for _, t := range row.traces {
				if err := ctx.Err(); err != nil {
					return st, err
				}
				seismicStatsAddTrace(&st, row.values[t], normalized)
			}
			return st, nil
		}, fmt.Sprintf("filter:%p:%t", snap, normalized)
	}
	if d != nil && compareMode == 2 {
		return seismicTimeSliceStatsReader(panel, d), fmt.Sprintf("slice:%d:%d:%d:%v", k.dataset, d.sampleA, d.sampleB, d.bounds)
	}
	path := compareAPath
	if panel == 1 {
		path = compareBPath
	}
	st := p.stats
	r := seismicRowsForStats{path: path, start: st.TraceStart, end: st.TraceEnd, step: st.TraceStep, first: st.SampleStart, last: st.SampleEnd, normalized: agc && panel < 2}
	if d != nil {
		ro := func(f *segy.File, g *segy.GeometryIndex) ([]int64, []int32) {
			if g == nil || f == nil {
				return nil, nil
			}
			tr, co, _, err := g.LineTraceNumbers(compareMode, compareLineCoord)
			if err != nil {
				return nil, nil
			}
			a, b, step := compareTraceRangeForGeometry(f)
			if b < a {
				return nil, nil
			}
			tr, co = filterPhysicalTraceRange(tr, co, a, b, step)
			return filterLineRange(tr, co, compareLineXMin, compareLineXMax)
		}
		if panel == 2 {
			at, ac := ro(d.fa, d.ga)
			bt, bc := ro(d.fb, d.gb)
			at, bt, _ = pairLineTracesByCoord(at, ac, bt, bc)
			bpath := compareBPath
			offset, _ := generatedPoststackSourceStart(bpath)
			return func(ctx context.Context) (display.Stats, error) {
				var st display.Stats
				a, err := openPoststackCompareSource(r.path)
				if err != nil {
					return st, err
				}
				defer a.Close()
				b, err := openPoststackCompareSource(bpath)
				if err != nil {
					return st, err
				}
				defer b.Close()
				for i, t := range at {
					if err := ctx.Err(); err != nil {
						return st, err
					}
					av, err := a.ReadTraceWindow(t, r.first, r.last)
					if err != nil {
						return st, err
					}
					bv, err := b.ReadTraceWindow(bt[i], r.first-offset, r.last-offset)
					if err != nil {
						return st, err
					}
					for j := range av {
						av[j] -= bv[j]
					}
					seismicStatsAddTrace(&st, av, false)
				}
				return st, nil
			}, fmt.Sprintf("residual:%d:%d:%d:%d:%x:%x:%d:%d", k.dataset, k.geometry, k.mode, k.line, k.x0, k.x1, r.first, r.last)
		}
		if panel == 1 {
			r.traces, _ = ro(d.fb, d.gb)
		} else {
			r.traces, _ = ro(d.fa, d.ga)
		}
		if r.traces == nil {
			r.traces = []int64{}
		} // empty, not a contiguous fallback
	}
	return r.reader(openPoststackCompareSource), fmt.Sprintf("line:%d:%d:%d:%d:%x:%x:%d:%d:%d:%d:%t", k.dataset, k.geometry, k.mode, k.line, k.x0, k.x1, r.start, r.end, r.first, r.last, r.normalized)
}

func seismicTimeSliceStatsReader(panel int, d *compareTimeData) func(context.Context) (display.Stats, error) {
	ga, gb := d.ga, d.gb
	va, vb := d.currentValsA, d.currentValsB
	bounds := d.bounds
	// Published slices are immutable for a paint generation (see updateTimeSliceImages).
	// Reading the slice avoids an extra disk pass across the complete volume.
	return func(ctx context.Context) (display.Stats, error) {
		var out display.Stats
		g, values := ga, va
		if panel == 1 {
			g, values = gb, vb
		}
		if g == nil {
			return out, fmt.Errorf("几何尚未就绪")
		}
		var paired map[[2]int32]float32
		if panel == 2 {
			paired = make(map[[2]int32]float32)
			if gb == nil {
				return out, nil
			}
			for i, v := range vb {
				if err := ctx.Err(); err != nil {
					return out, err
				}
				if p, valid := seismicSliceBin(gb, i, bounds); valid {
					if _, exists := paired[p]; !exists {
						paired[p] = v
					}
				}
			}
		}
		seen := make(map[[2]int32]struct{})
		for i, v32 := range values {
			if e := ctx.Err(); e != nil {
				return out, e
			}
			p, valid := seismicSliceBin(g, i, bounds)
			if !valid {
				continue
			}
			if _, exists := seen[p]; exists {
				continue
			}
			seen[p] = struct{}{}
			v := float64(v32)
			if panel == 2 {
				bv, ok := paired[p]
				if !ok {
					continue
				}
				v -= float64(bv)
			}
			out.Add(v)
		}
		return out, nil
	}
}

func seismicSliceBin(g *segy.GeometryIndex, i int, b segy.SliceBounds) ([2]int32, bool) {
	if i >= len(g.RowOfTrace) || i >= len(g.ColOfTrace) {
		return [2]int32{}, false
	}
	r, c := int(g.RowOfTrace[i]), int(g.ColOfTrace[i])
	if r < 0 || c < 0 || r >= len(g.InlineValues) || c >= len(g.CrosslineValues) {
		return [2]int32{}, false
	}
	il, xl := g.InlineValues[r], g.CrosslineValues[c]
	return [2]int32{il, xl}, il >= b.InlineMin && il <= b.InlineMax && xl >= b.CrosslineMin && xl <= b.CrosslineMax
}

// Form the difference on original matched bins, not between independently
// sampled screen pixels (their nearest coordinate can differ for sparse B).
func seismicTimeSliceDifference(ga, gb *segy.GeometryIndex, va, vb []float32, bounds segy.SliceBounds) []float32 {
	matched := make(map[[2]int32]float32)
	for i, v := range vb {
		if c, ok := seismicSliceBin(gb, i, bounds); ok {
			if _, seen := matched[c]; !seen {
				matched[c] = v
			}
		}
	}
	diff := make([]float32, len(va))
	for i, v := range va {
		c, ok := seismicSliceBin(ga, i, bounds)
		b, has := matched[c]
		if ok && has {
			diff[i] = v - b
		} else {
			diff[i] = float32(math.NaN())
		}
	}
	return diff
}

// Interpolate the physical coordinate across the SAME ordered trace ranks as
// the existing line raster. A large spacing gap is not an extra trace.
func seismicAcrossAt(coords []int32, f float64) float64 {
	if len(coords) == 0 {
		return math.NaN()
	}
	if len(coords) == 1 {
		return float64(coords[0])
	}
	rank := math.Max(0, math.Min(1, f)) * float64(len(coords)-1)
	i := int(rank)
	if i >= len(coords)-1 {
		return float64(coords[len(coords)-1])
	}
	return float64(coords[i]) + (rank-float64(i))*(float64(coords[i+1])-float64(coords[i]))
}

func seismicAcrossFraction(coords []int32, v float64) (float64, bool) {
	if len(coords) == 0 || v < float64(coords[0]) || v > float64(coords[len(coords)-1]) {
		return 0, false
	}
	if len(coords) == 1 {
		return 0.5, true
	}
	i := sort.Search(len(coords), func(i int) bool { return float64(coords[i]) >= v })
	if i <= 0 {
		return 0, true
	}
	if i >= len(coords) {
		return 1, true
	}
	a, b := float64(coords[i-1]), float64(coords[i])
	if b <= a {
		return float64(i) / float64(len(coords)-1), true
	}
	return (float64(i-1) + (v-a)/(b-a)) / float64(len(coords)-1), true
}

func saveSeismicCompareBMP(path string) error {
	if len(compareA.bgra) == 0 {
		return fmt.Errorf("没有可导出的地震图像")
	}
	panels := comparePanelRectsAll()
	if len(panels) == 0 {
		return fmt.Errorf("图像尺寸无效")
	}
	w, h := clientSize(compareHwnd)
	top := int(seismicCompareLayouts()[0].Outer.Top)
	return saveSeismicDisplayBMP(path, w, h-top, func(dc uintptr) {
		for i, r := range panels {
			p := seismicComparePanel(i)
			if p == nil || len(p.bgra) == 0 {
				continue
			}
			r.Top -= int32(top)
			r.Bottom -= int32(top)
			paintSeismicComparePanel(dc, r, *p, i, false)
		}
		paintCompareCommittedAnnotationsOffset(dc, -top)
	})
}
