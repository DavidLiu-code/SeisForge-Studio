//go:build windows

package main

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf16"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

// Ranges are canonical µs, sample amplitude, Hz and dB respectively. A range
// is only applied when its Set flag is true. One scene is painted on screen,
// rasterized for PNG/clipboard or serialized into editable vector SVG.
type traceScientificPlotOptions struct {
	DPI                                                          int
	SideBySide, FullWindow, RobustWindow, WaveOnly, SpectrumOnly bool
	LinearSpectrum, TimeInMS                                     bool
	WaveHeightPercent, WaveWidthPercent                          int
	TimeMinUS, TimeMaxUS, AmpMin, AmpMax                         float64
	FrequencyMinHz, FrequencyMaxHz, DBMin, DBMax                 float64
	LinearMin, LinearMax                                         float64
	TimeSet, AmpSet, FrequencySet, DBSet, LinearSet              bool
	CursorUS, SpectrumReference, WaveGain                        float64
	CursorSet                                                    bool
	Title                                                        string
	HideSceneTitle                                               bool // live toolbar already shows complete analysis identity
	LineWidthDIP                                                 float64
	ResearchStyle, HideGrid                                      bool
	CurveColor                                                   [3]uint32
	CurveColorSet                                                [3]bool
	CurvePattern                                                 [3]int // 0 default, 1 solid, 2 dashed, 3 dash-dot
	Data                                                         *segyanalysis.TraceSnapshot
	VisibleRoles                                                 []string // nil means every available curve
	nativeAxes, axes                                             [2]traceScientificAxes
}

type traceScientificPoint struct{ x, y float64 }
type traceScientificBounds struct{ l, t, r, b float64 }

func (r traceScientificBounds) width() float64  { return r.r - r.l }
func (r traceScientificBounds) height() float64 { return r.b - r.t }

type traceScientificStroke struct {
	ink   uint32
	width float64
	dash  []float64
}

type traceScientificPath struct {
	runs   [][]traceScientificPoint
	stroke traceScientificStroke
	clip   *traceScientificBounds
}

type traceScientificText struct {
	text     string
	at       traceScientificPoint // x is anchor; y is the common text baseline
	pt       float64
	align    int // -1 start, 0 center, +1 end
	rotation int // counter-clockwise GDI angle in tenths of a degree
	ink      uint32
}

type traceScientificScene struct {
	width, height                    int
	dpi                              int
	paths                            []traceScientificPath
	text                             []traceScientificText
	readOnlySources                  []string
	WaveAvailable, SpectrumAvailable bool
	WaveBounds                       []traceScientificBounds
	SpectrumBounds                   *traceScientificBounds
	axisHits                         []traceScientificAxisHit
	linearSpectrum                   bool
	style                            traceScientificPlotOptions
	effectiveAxes                    [2]traceScientificAxes
}

var traceScientificInk = uint32(rgbRef(25, 39, 54))
var traceScientificGrid = uint32(rgbRef(223, 230, 235))
var traceScientificZero = uint32(rgbRef(156, 169, 178))
var traceScientificMarker = uint32(rgbRef(161, 99, 36))

const traceScientificDefaultLineWidth = 1.8

func (s *traceScientificScene) dip(v float64) float64 { return v * float64(s.dpi) / 96 }

func traceScientificRole(role string) string {
	switch strings.TrimSpace(role) {
	case "Delta", "Δ", "A−B", "A-B", "差值", "差":
		return "A−B"
	default:
		return strings.TrimSpace(role)
	}
}

func (s *traceScientificScene) ink(role string) traceScientificStroke {
	index := 0
	switch traceScientificRole(role) {
	case "B":
		index = 1
	case "A−B":
		index = 2
	}
	color := [...]uint32{uint32(rgbRef(31, 81, 171)), uint32(rgbRef(185, 52, 38)), uint32(rgbRef(26, 119, 73))}[index]
	if s.style.ResearchStyle {
		color = [...]uint32{uint32(rgbRef(0x00, 0x72, 0xB2)), uint32(rgbRef(0xD5, 0x5E, 0x00)), uint32(rgbRef(0xCC, 0x79, 0xA7))}[index]
	}
	if s.style.CurveColorSet[index] {
		color = s.style.CurveColor[index]
	}
	pattern := s.style.CurvePattern[index]
	if pattern == 0 {
		pattern = index + 1
	}
	var dash []float64
	switch pattern {
	case 2:
		dash = []float64{s.dip(6), s.dip(3)}
	case 3:
		dash = []float64{s.dip(7), s.dip(3), s.dip(1.4), s.dip(3)}
	}
	width := s.style.LineWidthDIP
	if !traceScientificFinite(width) || width < .5 || width > 4 {
		width = traceScientificDefaultLineWidth
	}
	return traceScientificStroke{color, s.dip(width), dash}
}

func (s *traceScientificScene) line(x1, y1, x2, y2 float64, p traceScientificStroke) {
	s.paths = append(s.paths, traceScientificPath{runs: [][]traceScientificPoint{{{x1, y1}, {x2, y2}}}, stroke: p})
}

func (s *traceScientificScene) label(value string, x, y float64, align int, pt float64, ink uint32) {
	if value != "" {
		s.text = append(s.text, traceScientificText{text: value, at: traceScientificPoint{x, y}, pt: pt, align: align, ink: ink})
	}
}

func traceScientificLabelWidth(text string, em float64) float64 {
	var n float64
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r) || r >= 0x3000 && r < 0x3100 || r >= 0xff00:
			n++
		case r == '1' || r == '.' || r == ' ':
			n += .43
		default:
			n += .67
		}
	}
	return n * em
}

func traceScientificFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func traceScientificRange(lo, hi, fallbackLo, fallbackHi float64) displaycore.Range {
	if traceScientificFinite(lo) && traceScientificFinite(hi) && lo < hi && traceScientificFinite(hi-lo) {
		return displaycore.Range{Min: lo, Max: hi}
	}
	return displaycore.Range{Min: fallbackLo, Max: fallbackHi}
}

type traceScientificCurve struct {
	role                  string
	firstUS               float64
	dtUS                  int
	samples               []float64
	frequency, db, linear []float64
}

type traceScientificData struct {
	curves                                    []traceScientificCurve
	time, amplitude, frequency, level, linear displaycore.Range
	cursorUS, reference, waveGain             float64
	cursorSet, linearSpectrum                 bool
	referenceRole                             string
	referenceFallback                         bool
	nativeAxes, axes                          [2]traceScientificAxes
}

func traceScientificPlotData(r *traceAnalysisResult, opt traceScientificPlotOptions) traceScientificData {
	if opt.Data == nil && r != nil {
		opt.Data = r.snapshot
	}
	d := traceScientificData{reference: opt.SpectrumReference, cursorUS: opt.CursorUS, cursorSet: opt.CursorSet, linearSpectrum: opt.LinearSpectrum, waveGain: opt.WaveGain}
	if !traceScientificFinite(d.waveGain) || d.waveGain <= 0 {
		d.waveGain = 1
	}
	if opt.Data != nil {
		for _, v := range opt.Data.Series {
			d.curves = append(d.curves, traceScientificCurve{traceScientificRole(v.Role), float64(v.StartUS), v.IntervalUS, v.Samples, v.Spectrum.FrequencyHz, v.Spectrum.DB, v.Spectrum.LinearAmplitude})
		}
		d.reference = opt.Data.Reference.Amplitude
		d.referenceRole, d.referenceFallback = opt.Data.Reference.Role, opt.Data.Reference.Fallback
		scale := segyanalysis.ScaleFull
		if opt.RobustWindow && !opt.FullWindow {
			scale = segyanalysis.ScaleRobust
		}
		a := opt.Data.AmplitudeRange(scale, segyanalysis.DisplayRange{}, true)
		if scale == segyanalysis.ScaleFull {
			a = traceScientificPaddedAmplitude(a)
		}
		d.amplitude = traceScientificRange(a.Min, a.Max, -1, 1)
	}
	if r != nil && (opt.Data == nil || !d.cursorSet) {
		if opt.Data == nil {
			if d.reference <= 0 || !traceScientificFinite(d.reference) {
				for _, v := range r.series {
					if traceScientificRole(v.role) == "A" {
						d.reference = v.spectrum.PeakAmplitude
						break
					}
				}
				if len(r.series) == 1 && d.reference <= 0 {
					d.reference = r.series[0].spectrum.PeakAmplitude
				}
			}
			if d.reference <= 0 || !traceScientificFinite(d.reference) {
				d.reference = 1
			}
			for _, v := range r.series {
				db := make([]float64, len(v.spectrum.LinearAmplitude))
				for j, magnitude := range v.spectrum.LinearAmplitude {
					db[j] = 20 * (math.Log10(magnitude) - math.Log10(d.reference))
				}
				d.curves = append(d.curves, traceScientificCurve{traceScientificRole(v.role), v.timeUS(v.sampleStart), v.dtUS, v.samples, v.spectrum.FrequencyHz, db, v.spectrum.LinearAmplitude})
				peak := v.stats.PeakAbs
				if opt.RobustWindow && !opt.FullWindow {
					abs := max(math.Abs(v.stats.P01), math.Abs(v.stats.P99))
					if abs > 0 {
						peak = abs
					}
				}
				if traceScientificFinite(peak) && peak > d.amplitude.Max {
					d.amplitude = displaycore.Range{Min: -peak, Max: peak}
				}
			}
		}
		if !d.cursorSet && len(r.series) > 0 && r.series[0].dtUS > 0 && r.series[0].markerSample >= 0 {
			d.cursorSet, d.cursorUS = true, r.series[0].timeUS(r.series[0].markerSample)
		}
	}
	tlo, thi, ny := math.Inf(1), math.Inf(-1), 0.0
	step := 1
	for _, c := range d.curves {
		if len(c.samples) != 0 && c.dtUS > 0 {
			tlo, thi = min(tlo, c.firstUS), max(thi, c.firstUS+float64(len(c.samples)-1)*float64(c.dtUS))
			step = max(step, c.dtUS)
		}
		if len(c.frequency) != 0 {
			ny = max(ny, c.frequency[len(c.frequency)-1])
		} else if c.dtUS > 0 {
			ny = max(ny, 500000/float64(c.dtUS))
		}
	}
	if tlo == thi && traceScientificFinite(tlo) {
		tlo, thi = tlo-float64(step)/2, thi+float64(step)/2
	}
	d.time = traceScientificRange(tlo, thi, 0, 1000)
	d.frequency = traceScientificRange(0, min(125, ny), 0, 125)
	d.amplitude = traceScientificRange(d.amplitude.Min, d.amplitude.Max, -1, 1)
	if opt.TimeSet {
		d.time = traceScientificRange(opt.TimeMinUS, opt.TimeMaxUS, d.time.Min, d.time.Max)
	}
	if opt.AmpSet {
		d.amplitude = traceScientificRange(opt.AmpMin, opt.AmpMax, d.amplitude.Min, d.amplitude.Max)
	}
	if opt.FrequencySet {
		d.frequency = traceScientificRange(opt.FrequencyMinHz, opt.FrequencyMaxHz, d.frequency.Min, d.frequency.Max)
	}
	d.level, d.linear = traceScientificSpectrumExtent(d.curves, d.frequency)
	if opt.DBSet {
		d.level = traceScientificRange(opt.DBMin, opt.DBMax, d.level.Min, d.level.Max)
	}
	if opt.LinearSet && opt.LinearMin >= 0 {
		d.linear = traceScientificRange(opt.LinearMin, opt.LinearMax, d.linear.Min, d.linear.Max)
	}
	d.nativeAxes, d.axes = opt.nativeAxes, opt.axes
	if !d.nativeAxes[0].valid() || !d.nativeAxes[1].valid() {
		d.nativeAxes = traceScientificNativeAxes(d, opt)
	}
	if !d.axes[0].valid() || !d.axes[1].valid() {
		d.axes = d.nativeAxes
	}
	if opt.VisibleRoles != nil {
		allowed := make(map[string]bool, len(opt.VisibleRoles))
		for _, r := range opt.VisibleRoles {
			allowed[traceScientificRole(r)] = true
		}
		curves := d.curves[:0]
		for _, c := range d.curves {
			if allowed[c.role] {
				curves = append(curves, c)
			}
		}
		d.curves = curves
	}
	return d
}

func traceScientificPaddedAmplitude(r segyanalysis.DisplayRange) segyanalysis.DisplayRange {
	peak := max(math.Abs(r.Min), math.Abs(r.Max))
	if padded := peak * 1.04; padded > peak && traceScientificFinite(2*padded) {
		return segyanalysis.DisplayRange{Min: -padded, Max: padded}
	}
	return r
}

// Derive display bounds from every available curve before visibility filtering.
// Reference FFT data, including bins outside this view, remains untouched.
func traceScientificSpectrumExtent(curves []traceScientificCurve, hz displaycore.Range) (displaycore.Range, displaycore.Range) {
	lo, hi, peak := math.Inf(1), math.Inf(-1), 0.0
	for _, c := range curves {
		for i, f := range c.frequency {
			if f < hz.Min || f > hz.Max || !traceScientificFinite(f) {
				continue
			}
			if i < len(c.db) && traceScientificFinite(c.db[i]) {
				lo, hi = min(lo, c.db[i]), max(hi, c.db[i])
			}
			if i < len(c.linear) && traceScientificFinite(c.linear[i]) {
				peak = max(peak, c.linear[i])
			}
		}
	}
	if traceScientificFinite(lo) && traceScientificFinite(hi) {
		margin := max(1, (hi-lo)*.04)
		lo, hi = lo-margin, hi+margin
	}
	return traceScientificRange(lo, hi, -1, 1), traceScientificRange(0, peak*1.04, 0, max(1, peak))
}

func traceScientificMajorDivisions(pixels, em float64, horizontal bool) int {
	spacing := 4.3 * em
	if horizontal {
		spacing = 5.0 * em
	}
	return max(2, min(7, int(pixels/max(1, spacing))))
}

func traceScientificAxisTicks(a displaycore.Axis, pixels float64, em float64, horizontal, amplitude bool) displaycore.TickPlan {
	options := displaycore.TickOptions{Amplitude: amplitude, Pixels: int(pixels), Gap: int(em / 2),
		Format: a.Format, Digits: a.Digits, DisplayValue: a.DisplayValue, Position: a.Fraction}
	if horizontal {
		options.Measure = func(s string) int { return int(math.Ceil(traceScientificLabelWidth(s, em))) }
	} else {
		options.Measure = func(string) int { return int(math.Ceil(em)) }
	}
	if a.Step > 0 {
		options.MaxTicks = 64
		return displaycore.BuildTickPlan(a.View, a.Step, options)
	}
	if a.Divisions != 0 {
		options.MaxTicks = a.Divisions + 1
		if a.Format == displaycore.IntegerFormat {
			return a.EqualCoordinateTicks(options)
		}
		return displaycore.BuildEqualTickPlan(a.View, a.Divisions, options)
	}
	divisions := traceScientificMajorDivisions(pixels, em, horizontal)
	if a.ID == "frequency" && a.View.Min == 0 && a.View.Max == 125 && divisions >= 5 {
		divisions = 5 // standard overview: 0/25/50/75/100/125 Hz
	}
	return displaycore.ScientificAxisTicks(a, divisions, options)
}

func traceScientificFraction(r displaycore.Range, v float64) float64 {
	// Preserve lines crossing the visible range while avoiding infinities in
	// the drawing representation when a very narrow manual range is chosen.
	span := r.Max - r.Min
	if math.IsInf(span, 0) {
		return max(-1e6, min(1e6, (v/2-r.Min/2)/(r.Max/2-r.Min/2)))
	}
	return max(-1e6, min(1e6, (v-r.Min)/span))
}

func traceScientificAt(rect traceScientificBounds, a traceScientificAxes, x0, y0 float64) traceScientificPoint {
	fx, fy := traceScientificFraction(a.x.View, x0), traceScientificFraction(a.y.View, y0)
	if a.x.Reverse {
		fx = 1 - fx
	}
	if a.y.Down == a.y.Reverse {
		fy = 1 - fy
	}
	return traceScientificPoint{rect.l + fx*rect.width(), rect.t + fy*rect.height()}
}

func traceScientificFontPT(a displaycore.Axis, title bool) float64 {
	if title {
		if a.TitleFontPT > 0 {
			return a.TitleFontPT
		}
		return 10
	}
	if a.TickFontPT > 0 {
		return a.TickFontPT
	}
	return 9
}

func (s *traceScientificScene) frame(rect traceScientificBounds, a, native traceScientificAxes, key string, waveAmp, spectrumAmp bool, gain float64) {
	x, y := a.x, a.y
	xFont, yFont := traceScientificFontPT(x, false), traceScientificFontPT(y, false)
	xTitleFont, yTitleFont := traceScientificFontPT(x, true), traceScientificFontPT(y, true)
	xEm, yEm := s.dip(xFont*96/72), s.dip(yFont*96/72)
	xTicks := traceScientificAxisTicks(x, rect.width(), xEm, true, waveAmp)
	yTicks := traceScientificAxisTicks(y, rect.height(), yEm, false, spectrumAmp)
	xExt, yExt := s.dip(x.MajorTickDIP()), s.dip(y.MajorTickDIP())
	g := traceScientificStroke{ink: traceScientificGrid, width: s.dip(.7)}
	stroke := traceScientificStroke{ink: traceScientificInk, width: s.dip(.85)}
	if !x.HideAxis {
		for _, t := range xTicks.Ticks {
			p := traceScientificAt(rect, a, t.Value, y.View.Min)
			if !s.style.HideGrid {
				s.line(p.x, rect.t, p.x, rect.b, g)
			}
			if xExt > 0 {
				s.line(p.x, rect.b, p.x, rect.b+xExt, stroke)
			}
			if !x.HideLabels {
				s.label(t.Label, p.x, rect.b+xExt+xEm+s.dip(2), 0, xFont, traceScientificInk)
			}
		}
		s.line(rect.l, rect.b, rect.r, rect.b, stroke)
		if !x.HideTitle {
			title := displayAxisTitle(x)
			if waveAmp && gain != 1 {
				title += fmt.Sprintf("（显示 ×%.4g）", gain)
			}
			baseline := rect.b + xExt + s.dip(xTitleFont*96/72+6)
			if !x.HideLabels {
				baseline += xEm + s.dip(2)
			}
			s.label(title, (rect.l+rect.r)/2, baseline, 0, xTitleFont, traceScientificInk)
		}
	}
	yLabelWidth := 0.0
	if !y.HideAxis {
		for _, t := range yTicks.Ticks {
			p := traceScientificAt(rect, a, x.View.Min, t.Value)
			if !s.style.HideGrid {
				s.line(rect.l, p.y, rect.r, p.y, g)
			}
			if yExt > 0 {
				s.line(rect.l-yExt, p.y, rect.l, p.y, stroke)
			}
			if !y.HideLabels {
				s.label(t.Label, rect.l-yExt-s.dip(4), p.y+yEm/3, 1, yFont, traceScientificInk)
				yLabelWidth = max(yLabelWidth, traceScientificLabelWidth(t.Label, yEm))
			}
		}
		s.line(rect.l, rect.t, rect.l, rect.b, stroke)
		if !y.HideTitle {
			s.text = append(s.text, traceScientificText{text: displayAxisTitle(y), at: traceScientificPoint{rect.l - yExt - yLabelWidth - s.dip(11), (rect.t + rect.b) / 2}, pt: yTitleFont, align: 0, rotation: 900, ink: traceScientificInk})
		}
	}
	xBand := max(s.dip(18), s.scientificAxisBottom(x))
	yBand := max(s.dip(18), yExt+yLabelWidth+s.dip(yTitleFont*96/72+15))
	s.axisHits = append(s.axisHits, traceScientificAxisHit{key, native, rect,
		traceScientificBounds{rect.l, rect.b, rect.r, rect.b + xBand},
		traceScientificBounds{max(0, rect.l-yBand), rect.t, rect.l, rect.b}, xTicks, yTicks})
}

func (s *traceScientificScene) scientificAxisBottom(a displaycore.Axis) float64 {
	space := s.dip(4)
	if a.HideAxis {
		return space
	}
	space += s.dip(a.MajorTickDIP())
	if !a.HideLabels {
		space += s.dip(traceScientificFontPT(a, false)*96/72 + 3)
	}
	if !a.HideTitle {
		space += s.dip(traceScientificFontPT(a, true)*96/72 + 5)
	}
	return space
}

func (s *traceScientificScene) plot(rect traceScientificBounds, d traceScientificData, curves []traceScientificCurve, wave bool, title string, emptyWindow bool) {
	if rect.width() < s.dip(44) || rect.height() < s.dip(35) {
		return
	}
	s.label(title, (rect.l+rect.r)/2, rect.t-s.dip(10), 0, 11, traceScientificInk)
	if emptyWindow {
		key, axes := traceScientificSpecKey, 1
		note := "时间窗内无样点，未计算频谱"
		if wave {
			key, axes = traceScientificWaveKey, 0
			note = "所选时间窗内没有真实样点"
		}
		s.frame(rect, d.axes[axes], d.nativeAxes[axes], key, wave, !wave && d.linearSpectrum, 1)
		s.label(note, (rect.l+rect.r)/2, (rect.t+rect.b)/2, 0, 10, traceScientificInk)
		return
	}
	if len(curves) == 0 {
		s.label("没有选中的数据曲线", (rect.l+rect.r)/2, (rect.t+rect.b)/2, 0, 10, traceScientificInk)
		return
	}
	displayed := 0
	if wave {
		s.WaveBounds = append(s.WaveBounds, rect)
		a := d.axes[0]
		s.frame(rect, a, d.nativeAxes[0], traceScientificWaveKey, true, false, d.waveGain)
		if a.x.View.Min <= 0 && a.x.View.Max >= 0 {
			p := traceScientificAt(rect, a, 0, a.y.View.Min)
			s.line(p.x, rect.t, p.x, rect.b, traceScientificStroke{ink: traceScientificZero, width: s.dip(.9)})
		}
		cursor := d.cursorUS / 1e6
		if d.cursorSet && traceScientificFinite(cursor) && cursor >= a.y.View.Min && cursor <= a.y.View.Max {
			p := traceScientificAt(rect, a, a.x.View.Min, cursor)
			s.line(rect.l, p.y, rect.r, p.y, traceScientificStroke{traceScientificMarker, s.dip(.8), []float64{s.dip(3), s.dip(3)}})
			baseline := max(rect.t+s.dip(13), p.y-s.dip(4))
			s.label(displaycore.FormatCompactNumber(a.y.DisplayValue(cursor))+" "+a.y.Unit, rect.r-s.dip(4), baseline, 1, 8, traceScientificMarker)
		}
		hasSample, nonzero := false, false
		for _, c := range curves {
			if c.dtUS <= 0 {
				continue
			}
			runs := traceScientificRuns(len(c.samples), func(k int) (traceScientificPoint, bool) {
				amp, t := c.samples[k], (c.firstUS+float64(k)*float64(c.dtUS))/1e6
				ok := traceScientificFinite(amp) && traceScientificFinite(t)
				if ok && t >= a.y.View.Min && t <= a.y.View.Max {
					hasSample, nonzero = true, nonzero || amp != 0
				}
				return traceScientificAt(rect, a, amp*d.waveGain, t), ok
			})
			visible := traceScientificClipRuns(runs, rect)
			if len(visible) != 0 {
				displayed++
				s.paths = append(s.paths, traceScientificPath{runs: visible, stroke: s.ink(c.role), clip: &rect})
			}
		}
		s.WaveAvailable = s.WaveAvailable || displayed != 0
		if displayed != 0 && hasSample && !nonzero {
			s.label("当前窗波形为零", rect.r-s.dip(8), rect.t+s.dip(16), 1, 9, traceScientificInk)
		}
	} else {
		s.SpectrumBounds = &rect
		a := d.axes[1]
		s.frame(rect, a, d.nativeAxes[1], traceScientificSpecKey, false, d.linearSpectrum, 1)
		for _, c := range curves {
			values := c.db
			if d.linearSpectrum {
				if c.spectrumZero() {
					continue
				}
				values = c.linear
			}
			runs := traceScientificRuns(min(len(c.frequency), len(values)), func(k int) (traceScientificPoint, bool) {
				f, v := c.frequency[k], values[k]
				if !traceScientificFinite(f) || !traceScientificFinite(v) {
					return traceScientificPoint{}, false
				}
				return traceScientificAt(rect, a, f, v), true
			})
			visible := traceScientificClipRuns(runs, rect)
			if len(visible) != 0 {
				displayed++
				s.paths = append(s.paths, traceScientificPath{runs: visible, stroke: s.ink(c.role), clip: &rect})
			}
		}
		s.SpectrumAvailable = displayed != 0
	}
	if displayed == 0 {
		text := "当前频率窗无有效谱值或谱能量为零"
		if wave {
			text = "没有有效波形样点"
		}
		s.label(text, (rect.l+rect.r)/2, (rect.t+rect.b)/2, 0, 10, traceScientificInk)
	}
}

func (c traceScientificCurve) spectrumZero() bool {
	for _, v := range c.linear {
		if v > 0 && traceScientificFinite(v) {
			return false
		}
	}
	return true
}

func traceScientificRuns(n int, value func(int) (traceScientificPoint, bool)) [][]traceScientificPoint {
	var result [][]traceScientificPoint
	var run []traceScientificPoint
	for k := 0; k < n; k++ {
		p, ok := value(k)
		if !ok {
			if len(run) > 0 {
				result = append(result, run)
				run = nil
			}
			continue
		}
		run = append(run, p)
	}
	if len(run) > 0 {
		result = append(result, run)
	}
	return result
}

func traceScientificInside(p traceScientificPoint, r traceScientificBounds) bool {
	return p.x >= r.l && p.x <= r.r && p.y >= r.t && p.y <= r.b
}

func traceScientificClipSegment(a, b traceScientificPoint, r traceScientificBounds) (traceScientificPoint, traceScientificPoint, bool) {
	if traceScientificInside(a, r) && traceScientificInside(b, r) {
		return a, b, true
	}
	lo, hi := 0.0, 1.0
	for _, axis := range [][4]float64{{a.x, b.x, r.l, r.r}, {a.y, b.y, r.t, r.b}} {
		start, end, low, high := axis[0], axis[1], axis[2], axis[3]
		if start == end {
			if start < low || start > high {
				return a, b, false
			}
			continue
		}
		t0, t1 := (low-start)/(end-start), (high-start)/(end-start)
		lo, hi = max(lo, min(t0, t1)), min(hi, max(t0, t1))
		if lo > hi {
			return a, b, false
		}
	}
	at := func(t float64) traceScientificPoint {
		return traceScientificPoint{max(r.l, min(r.r, a.x+(b.x-a.x)*t)), max(r.t, min(r.b, a.y+(b.y-a.y)*t))}
	}
	return at(lo), at(hi), true
}

func traceScientificClipRuns(runs [][]traceScientificPoint, r traceScientificBounds) [][]traceScientificPoint {
	var visible [][]traceScientificPoint
	for _, run := range runs {
		if len(run) == 1 && traceScientificInside(run[0], r) {
			visible = append(visible, run)
			continue
		}
		var line []traceScientificPoint
		flush := func() {
			if len(line) > 0 {
				visible = append(visible, line)
				line = nil
			}
		}
		for i := 1; i < len(run); i++ {
			a, b, ok := traceScientificClipSegment(run[i-1], run[i], r)
			if !ok {
				flush()
				continue
			}
			if len(line) > 0 && line[len(line)-1] != a {
				flush()
			}
			if len(line) == 0 {
				line = append(line, a)
			}
			if a != b {
				line = append(line, b)
			}
		}
		flush()
	}
	return visible
}

func traceScientificPlotScene(width, height int, r *traceAnalysisResult, opt traceScientificPlotOptions) traceScientificScene {
	s := traceScientificScene{width: width, height: height, dpi: opt.DPI, linearSpectrum: opt.LinearSpectrum, style: opt}
	if s.dpi <= 0 {
		s.dpi = 96
	}
	if width < 1 || height < 1 {
		return s
	}
	// Measure the real Arial/CJK font runs at the target DPI. Screen, raster and
	// vector output therefore reserve space for the actual right-most label.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	measureDC, _, _ := pCreateCompatibleDC.Call(0)
	if measureDC != 0 {
		defer pDeleteDC.Call(measureDC)
	}
	metrics := map[float64]displayPlotFontMetrics{}
	fonts := map[float64]displayPlotFontPair{}
	measure := func(label string, pt float64) float64 {
		if measureDC == 0 {
			return traceScientificLabelWidth(label, s.dip(pt*96/72))
		}
		m, ok := metrics[pt]
		if !ok {
			f := displayNewPlotFonts(pt, s.dpi, 0)
			fonts[pt] = f
			m = displayPlotFontMeasure(measureDC, s.dpi, pt, f)
			metrics[pt] = m
		}
		return float64(m.Measure(label).Width)
	}
	defer func() {
		for _, f := range fonts {
			f.close()
		}
	}()
	if r != nil {
		for _, target := range r.selection.Targets {
			if target.Path != "" {
				s.readOnlySources = append(s.readOnlySources, target.Path)
			}
		}
	}
	d := traceScientificPlotData(r, opt)
	title := opt.Title
	if title == "" && r != nil {
		title = r.selection.Context
	}
	inset := s.dip(12)
	y := s.dip(19)
	if title != "" && !opt.HideSceneTitle {
		s.label(title, float64(width)/2, y, 0, 11, traceScientificInk)
		y += s.dip(20)
	}
	// Color and line style remain distinguishable in grayscale print.
	legendW := s.dip(87)
	legendX := max(inset, (float64(width)-legendW*float64(len(d.curves)))/2)
	for i, c := range d.curves {
		x := legendX + float64(i)*legendW
		s.line(x, y-s.dip(4), x+s.dip(31), y-s.dip(4), s.ink(c.role))
		s.label(c.role, x+s.dip(37), y, -1, 9, traceScientificInk)
	}
	y += s.dip(24)
	emptyWindow := r != nil && r.manualWindowHasNoSamples()
	if len(d.curves) == 0 && !emptyWindow {
		s.label("未选择有效曲线", float64(width)/2, float64(height)/2, 0, 10, traceScientificInk)
	}
	wave, spectrum := !opt.SpectrumOnly, !opt.WaveOnly
	waveBottom, spectrumBottom := s.scientificAxisBottom(d.axes[0].x), s.scientificAxisBottom(d.axes[1].x)
	panelBottom := float64(height) - max(waveBottom, spectrumBottom)
	if wave && !spectrum {
		panelBottom = float64(height) - waveBottom
	} else if spectrum && !wave {
		panelBottom = float64(height) - spectrumBottom
	}
	// Two independent graph areas. At narrow widths stack, while separation
	// into one waveform plot per selected role is independent of that choice.
	var wRect, fRect traceScientificBounds
	waveMinimumWidth := s.dip(200)
	if opt.SideBySide && len(d.curves) > 1 {
		waveMinimumWidth = max(waveMinimumWidth, s.dip(122)*float64(len(d.curves)))
	}
	spectrumMinimumWidth := s.dip(205)
	if wave && spectrum && float64(width) < max(s.dip(650), waveMinimumWidth+spectrumMinimumWidth+4*inset) {
		p := opt.WaveHeightPercent
		if p < 25 || p > 75 {
			p = 51
		}
		mid := y + (panelBottom-y)*float64(p)/100
		wRect = traceScientificBounds{inset, y, float64(width) - inset, mid - waveBottom}
		fRect = traceScientificBounds{inset, mid + s.dip(23), float64(width) - inset, panelBottom}
	} else if wave && spectrum {
		// With separate waveforms, grant more width to their shared plot group.
		f := .50
		if opt.WaveWidthPercent >= 25 && opt.WaveWidthPercent <= 75 {
			f = float64(opt.WaveWidthPercent) / 100
		} else if opt.SideBySide && len(d.curves) > 1 {
			f = .66
		}
		mid := max(waveMinimumWidth+2*inset, min(float64(width)-spectrumMinimumWidth-2*inset, float64(width)*f))
		wRect = traceScientificBounds{inset, y, mid - s.dip(3), panelBottom}
		fRect = traceScientificBounds{mid + s.dip(3), y, float64(width) - inset, panelBottom}
	} else {
		wRect = traceScientificBounds{inset, y, float64(width) - inset, panelBottom}
		fRect = wRect
	}
	expand := func(a *displaycore.Axis, native *displaycore.Axis, size float64, horizontal bool) {
		if a.Step != 0 || a.Divisions != 0 {
			return // explicitly requested locations take priority
		}
		em := s.dip(traceScientificFontPT(*a, false) * 96 / 72)
		*a = displaycore.ExpandScientificAxis(*a, traceScientificMajorDivisions(size, em, horizontal))
		native.View = a.View
	}
	if wave && !opt.AmpSet {
		// Use the entire waveform group, not the count of checked roles. A
		// visibility change must not rescale any surviving waveform.
		groupWidth := float64(width)
		if spectrum {
			groupWidth *= .5
		}
		expand(&d.axes[0].x, &d.nativeAxes[0].x, groupWidth-s.dip(60), true)
	}
	if spectrum && ((!d.linearSpectrum && !opt.DBSet) || (d.linearSpectrum && !opt.LinearSet)) {
		expand(&d.axes[1].y, &d.nativeAxes[1].y, float64(height)-s.dip(105), false)
	}
	s.effectiveAxes = d.axes
	makePlot := func(box traceScientificBounds, a traceScientificAxes, waveAmp bool) traceScientificBounds {
		left, right := s.dip(9), s.dip(12)
		xEm, yEm := s.dip(traceScientificFontPT(a.x, false)*96/72), s.dip(traceScientificFontPT(a.y, false)*96/72)
		if !a.y.HideAxis {
			extension := s.dip(a.y.MajorTickDIP() + 4)
			if !a.y.HideLabels {
				for _, tick := range traceScientificAxisTicks(a.y, box.height(), yEm, false, !waveAmp && d.linearSpectrum).Ticks {
					left = max(left, measure(tick.Label, traceScientificFontPT(a.y, false))+extension)
				}
			} else {
				left += extension
			}
			if !a.y.HideTitle {
				left += s.dip(traceScientificFontPT(a.y, true)*96/72 + 12)
			}
		}
		if ticks := traceScientificAxisTicks(a.x, box.width()-left-right, xEm, true, waveAmp).Ticks; !a.x.HideAxis && !a.x.HideLabels && len(ticks) > 0 {
			l, r := ticks[0], ticks[len(ticks)-1]
			if a.x.Reverse {
				l, r = r, l
			}
			left = max(left, measure(l.Label, traceScientificFontPT(a.x, false))/2+s.dip(5))
			right = max(right, measure(r.Label, traceScientificFontPT(a.x, false))/2+s.dip(5))
		}
		return traceScientificBounds{box.l + left, box.t, box.r - right, box.b}
	}
	if wave {
		if opt.SideBySide && len(d.curves) > 1 {
			size := wRect.width() / float64(len(d.curves))
			for i, c := range d.curves {
				box := traceScientificBounds{wRect.l + size*float64(i), wRect.t, wRect.l + size*float64(i+1), wRect.b}
				s.plot(makePlot(box, d.axes[0], true), d, []traceScientificCurve{c}, true, "波形 "+c.role, false)
			}
		} else {
			s.plot(makePlot(wRect, d.axes[0], true), d, d.curves, true, "单道波形", emptyWindow)
		}
	}
	if spectrum {
		caption := "幅度谱 (dB · 同一参考)"
		if d.linearSpectrum {
			caption = "幅度谱（线性）"
		} else if d.referenceFallback {
			caption = "幅度谱 (dB · 0 = 1 FFT 单位)"
		} else if d.referenceRole != "" {
			caption = "幅度谱 (dB · 0 = " + d.referenceRole + " 峰值)"
		}
		s.plot(makePlot(fRect, d.axes[1], false), d, d.curves, false, caption, emptyWindow)
	}
	return s
}

var traceScientificPolyline = gdi32.NewProc("Polyline")
var traceScientificPen = gdi32.NewProc("ExtCreatePen")

func (s traceScientificScene) paint(hdc uintptr, x, y int) {
	s.paintAt(hdc, x, y, 1)
}

func (s traceScientificScene) paintAt(hdc uintptr, x, y int, scale float64) {
	if hdc == 0 || s.width < 1 || s.height < 1 || scale <= 0 {
		return
	}
	pixel := func(offset int, v float64) int32 {
		return int32(max(-1e9, min(1e9, float64(offset)+math.Round(v*scale))))
	}
	fill := RECT{int32(x), int32(y), pixel(x, float64(s.width)), pixel(y, float64(s.height))}
	white, _, _ := pGetStockObject.Call(WHITE_BRUSH)
	pFillRect.Call(hdc, uintptr(unsafe.Pointer(&fill)), white)
	pSetBkMode.Call(hdc, TRANSPARENT)
	type penKey struct {
		ink   uint32
		width uint32
		style string
	}
	type logBrush struct {
		Style, Color uint32
		Hatch        uintptr
	}
	pens := make(map[penKey]uintptr)
	defer func() {
		for _, pen := range pens {
			pDeleteObject.Call(pen)
		}
	}()
	choosePen := func(p traceScientificStroke) uintptr {
		w := uint32(max(1, int(math.Round(p.width*scale))))
		key := penKey{p.ink, w, fmt.Sprint(p.dash)}
		if pen := pens[key]; pen != 0 {
			return pen
		}
		var ptr uintptr
		dash := make([]uint32, len(p.dash))
		for j := range dash {
			dash[j] = uint32(max(1, int(math.Round(p.dash[j]*scale))))
		}
		style := uintptr(0x10000 | 0x200) // PS_GEOMETRIC | PS_ENDCAP_FLAT
		if len(dash) != 0 {
			style |= 7 // PS_USERSTYLE
			ptr = uintptr(unsafe.Pointer(&dash[0]))
		}
		l := logBrush{Color: p.ink}
		pen, _, _ := traceScientificPen.Call(style, uintptr(w), uintptr(unsafe.Pointer(&l)), uintptr(len(dash)), ptr)
		runtime.KeepAlive(dash)
		if pen == 0 {
			pen, _, _ = pCreatePen.Call(PS_SOLID, uintptr(w), uintptr(p.ink))
		}
		if pen != 0 {
			pens[key] = pen
		}
		return pen
	}
	// Limit all scenes to their declared viewport, including long titles.
	state, _, _ := pSaveDC.Call(hdc)
	if state != 0 {
		defer pRestoreDC.Call(hdc, state)
		pIntersectClipRect.Call(hdc, uintptr(fill.Left), uintptr(fill.Top), uintptr(fill.Right), uintptr(fill.Bottom))
	}
	if !s.drawAntialiased(hdc, x, y, scale) {
		for _, p := range s.paths {
			pen := choosePen(p.stroke)
			if pen == 0 {
				continue
			}
			old, _, _ := pSelectObject.Call(hdc, pen)
			clipState := uintptr(0)
			if p.clip != nil {
				cr := p.clip
				clipState, _, _ = pSaveDC.Call(hdc)
				if clipState != 0 {
					pIntersectClipRect.Call(hdc, uintptr(pixel(x, cr.l)), uintptr(pixel(y, cr.t)), uintptr(pixel(x, cr.r)+1), uintptr(pixel(y, cr.b)+1))
				}
			}
			for _, run := range p.runs {
				if len(run) == 1 {
					px, py := pixel(x, run[0].x), pixel(y, run[0].y)
					drawLine(hdc, int(px)-1, int(py), int(px)+2, int(py))
				} else if len(run) > 1 {
					points := make([]POINT, len(run))
					for i, p := range run {
						points[i] = POINT{pixel(x, p.x), pixel(y, p.y)}
					}
					traceScientificPolyline.Call(hdc, uintptr(unsafe.Pointer(&points[0])), uintptr(len(points)))
				}
			}
			if clipState != 0 {
				pRestoreDC.Call(hdc, clipState)
			}
			pSelectObject.Call(hdc, old)
		}
	}
	type fontsKey struct {
		pt float64
		r  int
	}
	fonts := make(map[fontsKey]displayPlotFontPair)
	defer func() {
		for _, f := range fonts {
			f.close()
		}
	}()
	dpi := max(24, int(math.Round(float64(s.dpi)*scale)))
	for _, t := range s.text {
		key := fontsKey{t.pt, t.rotation}
		f, ok := fonts[key]
		if !ok {
			f = displayNewPlotFonts(t.pt, dpi, t.rotation)
			fonts[key] = f
		}
		m := displayPlotFontMeasure(hdc, dpi, t.pt, f)
		w := m.Measure(t.text).Width
		px, py := int(pixel(x, t.at.x)), int(pixel(y, t.at.y))
		offset := (t.align + 1) * w / 2
		if t.rotation == 900 {
			py += offset
		} else {
			px -= offset
		}
		pSetTextColor.Call(hdc, uintptr(t.ink))
		traceScientificPaintText(hdc, f, t.text, px, py, t.rotation, m)
	}
}

var traceScientificTextAlign = gdi32.NewProc("SetTextAlign")
var traceScientificTextOut = gdi32.NewProc("TextOutW")

func traceScientificPaintText(dc uintptr, f displayPlotFontPair, text string, x, y, rotation int, metric displayPlotFontMetrics) {
	restoreAlignment, _, _ := traceScientificTextAlign.Call(dc, 24 /* TA_LEFT | TA_BASELINE */)
	defer traceScientificTextAlign.Call(dc, restoreAlignment)
	f.runs(text, func(run string, font uintptr) {
		restore := displaySelectPlotFont(dc, font)
		// Input labels have no embedded NULs. Count actual UTF-16 code units
		// for Chinese paths and filenames containing supplementary characters.
		units := utf16.Encode([]rune(run))
		if len(units) == 0 {
			restore()
			return
		}
		traceScientificTextOut.Call(dc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(&units[0])), uintptr(len(units)))
		w := displayPlotTextExtent(dc, units, metric.Ascent).Width
		restore()
		if rotation == 900 {
			y -= w
		} else {
			x += w
		}
	})
}
