//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"hash/crc32"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

func traceScientificFixture() segyanalysis.TraceSnapshot {
	const n, dt, offset = 128, 2000, 800000
	a, b := make([]float64, n), make([]float64, n)
	for i := range a {
		v := math.Sin(float64(i) * 2 * math.Pi / 16)
		a[i], b[i] = 4*v, v
	}
	return segyanalysis.BuildTraceSnapshot([]segyanalysis.TraceInput{
		{Role: "A", StartUS: offset, IntervalUS: dt, Samples: a},
		{Role: "B", StartUS: offset, IntervalUS: dt, Samples: b},
	}, true)
}

func TestTraceScientificHiddenAKeepsCommonAmplitudeTimeAndDB(t *testing.T) {
	d := traceScientificFixture()
	a := traceScientificPlotData(nil, traceScientificPlotOptions{Data: &d})
	b := traceScientificPlotData(nil, traceScientificPlotOptions{Data: &d, VisibleRoles: []string{"B"}})
	if b.amplitude != a.amplitude || b.time != a.time || b.level != a.level || b.time.Min != 800000 || b.time.Max != 1054000 {
		t.Fatalf("hiding A changed quantitative axes: A+B %+v; B %+v", a, b)
	}
	if len(b.curves) != 1 || b.curves[0].role != "B" || b.reference != a.reference || b.reference <= 0 {
		t.Fatalf("B lost fixed common reference: %+v", b)
	}
	peak := math.Inf(-1)
	for _, v := range b.curves[0].db {
		peak = max(peak, v)
	}
	if math.Abs(peak-20*math.Log10(.25)) > 1e-10 {
		t.Fatalf("B peak = %g dB, want about -12 dB (not its own 0 dB)", peak)
	}
}

func TestTraceScientificSeparateWaveAxesAndDashes(t *testing.T) {
	data := traceScientificFixture()
	s := traceScientificPlotScene(1070, 660, nil, traceScientificPlotOptions{DPI: 96, Data: &data, WaveOnly: true, SideBySide: true, Title: "Selected trace"})
	curves := []traceScientificPath{}
	for _, p := range s.paths {
		if p.clip != nil {
			curves = append(curves, p)
		}
	}
	if len(curves) != 3 {
		t.Fatalf("expected one curve per side-by-side waveform, got %d", len(curves))
	}
	if len(curves[0].stroke.dash) != 0 || len(curves[1].stroke.dash) != 2 || len(curves[2].stroke.dash) != 4 {
		t.Fatalf("scientific line types = %v / %v / %v", curves[0].stroke.dash, curves[1].stroke.dash, curves[2].stroke.dash)
	}
	for i, p := range curves {
		if len(p.runs) == 0 || p.stroke.ink != s.ink([]string{"A", "B", "A-B"}[i]).ink || p.clip.t != curves[0].clip.t || p.clip.b != curves[0].clip.b ||
			math.Abs(p.clip.width()-curves[0].clip.width()) > 1e-9 || i > 0 && curves[i-1].clip.r >= p.clip.l {
			t.Fatalf("panel %d lost aligned bounds, scale, or distinct ink: %+v", i, p)
		}
	}
}

func TestTraceScientificVectorAndPNGShareRenderedCurves(t *testing.T) {
	data := traceScientificFixture()
	s := traceScientificPlotScene(760, 440, nil, traceScientificPlotOptions{DPI: 96, Data: &data, Title: "A & B <check>"})
	svg, pngPath := filepath.Join(t.TempDir(), "trace.svg"), filepath.Join(t.TempDir(), "trace.png")
	if err := s.writeSVG(svg); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(svg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stroke-dasharray", "font-family=\"Arial", "viewBox=\"0 0 760 440\"", "A &amp; B &lt;check&gt;", "<clipPath"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("editable exported SVG lacks %q", want)
		}
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid SVG XML: %v", err)
		}
		if v, ok := tok.(xml.StartElement); ok && v.Name.Local == "image" {
			t.Fatal("SVG contains a raster instead of vectors")
		}
	}
	const dpi = 150
	if err := s.writePNG(pngPath, dpi); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(pngPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b[37:41]) != "pHYs" || binary.BigEndian.Uint32(b[41:45]) != uint32(math.Round(dpi/0.0254)) ||
		binary.BigEndian.Uint32(b[45:49]) != uint32(math.Round(dpi/0.0254)) || b[49] != 1 ||
		crc32.ChecksumIEEE(b[37:50]) != binary.BigEndian.Uint32(b[50:54]) {
		t.Fatalf("physical DPI chunk not present or corrupt: %v", b[33:54])
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	wantWidth, wantHeight := int(math.Round(760*dpi/96.)), int(math.Round(440*dpi/96.))
	if img.Bounds().Dx() != wantWidth || img.Bounds().Dy() != wantHeight {
		t.Fatalf("physical image reserved wrong size: %v want %dx%d", img.Bounds(), wantWidth, wantHeight)
	}
	blue, red, green := 0, 0, 0
	for y := range img.Bounds().Dy() {
		for x := range img.Bounds().Dx() {
			r, g, b, _ := img.At(x, y).RGBA()
			switch {
			case int(b)-int(r) > 22000:
				blue++
			case int(r)-int(g) > 25000:
				red++
			case int(g)-int(r) > 17000:
				green++
			}
		}
	}
	if blue < 100 || red < 100 || green < 100 {
		t.Fatalf("GDI PNG dropped one or more plotted curves: A %d B %d A−B %d", blue, red, green)
	}
}

func TestTraceScientificClipsRunsWithoutBridgingMissingSamples(t *testing.T) {
	r := traceScientificBounds{0, 0, 5, 5}
	out := traceScientificClipRuns([][]traceScientificPoint{
		{{-2, 2}, {2, 2}}, {{3, 3}, {7, 3}},
	}, r)
	if len(out) != 2 || len(out[0]) != 2 || len(out[1]) != 2 ||
		out[0][0] != (traceScientificPoint{0, 2}) || out[0][1] != (traceScientificPoint{2, 2}) ||
		out[1][0] != (traceScientificPoint{3, 3}) || out[1][1] != (traceScientificPoint{5, 3}) {
		t.Fatalf("cross-boundary portion or NaN gap lost: %v", out)
	}
}

func TestTraceScientificRejectsSourceOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "original.svg")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := traceScientificScene{width: 80, height: 60, dpi: 96, readOnlySources: []string{path}}
	if err := s.writeSVG(path); err == nil {
		t.Fatal("export overwrote source")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original" {
		t.Fatalf("source changed: %v %q", err, got)
	}
}

func TestTraceScientificGainChangesOnlyWaveGeometryAndLinearIsAbsolute(t *testing.T) {
	data := traceScientificFixture()
	options := traceScientificPlotOptions{Data: &data, LinearSpectrum: true, WaveWidthPercent: 60}
	plain := traceScientificPlotScene(1040, 610, nil, options)
	options.WaveGain = 1.8
	gain := traceScientificPlotScene(1040, 610, nil, options)
	var first, second []traceScientificPath
	for _, p := range plain.paths {
		if p.clip != nil {
			first = append(first, p)
		}
	}
	for _, p := range gain.paths {
		if p.clip != nil {
			second = append(second, p)
		}
	}
	if len(first) != 6 || len(second) != 6 {
		t.Fatal("three wave and three FFT paths required")
	}
	if first[0].runs[0][1] == second[0].runs[0][1] {
		t.Fatal("gain did not change the wave projection")
	}
	for i := 3; i < len(first); i++ {
		for j, p := range first[i].runs[0] {
			if p != second[i].runs[0][j] {
				t.Fatal("display wave gain changed FFT geometry")
			}
		}
	}
	d := traceScientificPlotData(nil, options)
	if d.linear.Min != 0 || d.linear.Max <= 0 || d.curves[0].linear[8] <= d.curves[1].linear[8] ||
		math.Abs(d.curves[0].linear[8]/d.curves[1].linear[8]-4) > 1e-12 {
		t.Fatal("linear mode didn't keep actual 4:1 physical spectrum magnitude")
	}
}

func TestTraceScientificTimeUnitDoesNotChangeWaveGeometry(t *testing.T) {
	data := traceScientificFixture()
	opts := traceScientificPlotOptions{Data: &data, WaveOnly: true, CursorSet: true, CursorUS: 854321}
	seconds := traceScientificPlotScene(840, 450, nil, opts)
	opts.TimeInMS = true
	milliseconds := traceScientificPlotScene(840, 450, nil, opts)
	for i, s := range []traceScientificScene{seconds, milliseconds} {
		unit, marker := "时间 (s)", "0.854321 s"
		if i == 1 {
			unit, marker = "时间 (ms)", "854.321 ms"
		}
		labels := map[string]bool{}
		for _, txt := range s.text {
			labels[txt.text] = true
		}
		if !labels[unit] || !labels[marker] || !s.WaveAvailable || s.SpectrumAvailable || len(s.WaveBounds) != 1 {
			t.Fatalf("unit or plotted bounds inconsistent: %v", labels)
		}
	}
	projected := func(s traceScientificScene) []float64 {
		for _, p := range s.paths {
			if p.clip != nil {
				result := []float64{}
				for _, row := range p.runs {
					for _, point := range row {
						result = append(result, (point.y-p.clip.t)/p.clip.height())
					}
				}
				return result
			}
		}
		return nil
	}
	a, b := projected(seconds), projected(milliseconds)
	if len(a) < 2 || len(a) != len(b) {
		t.Fatal("unit change dropped samples")
	}
	for j := range a {
		if math.Abs(a[j]-b[j]) > 1e-10 {
			t.Fatal("unit change shifted the physical waveform")
		}
	}
}

func TestTraceScientificSharedAxisEditBecomesNativeViewportAndResets(t *testing.T) {
	saved, controls, hwnd := traceScientificStyle, traceAnalysisUI, traceAnalysisHwnd
	t.Cleanup(func() { traceScientificStyle, traceAnalysisUI, traceAnalysisHwnd = saved, controls, hwnd })
	traceScientificStyle, traceAnalysisUI, traceAnalysisHwnd = traceScientificDisplay{}, traceAnalysisControls{}, 0
	data := traceScientificFixture()
	o := traceScientificPlotOptions{Data: &data, TimeInMS: true, WaveOnly: true, SideBySide: true}
	d := traceScientificPlotData(nil, o)
	time := d.nativeAxes[0].y
	if time.CanonicalUnit() != "s" || time.Unit != "ms" || time.View.Min != .8 || time.DisplayValue(time.View.Min) != 800 {
		t.Fatalf("expected 0.8 s = 800 ms in the shared time axis: %+v", time)
	}
	// The shared settings editor reports canonical seconds even when its
	// input is labeled milliseconds.
	time.View, time.ViewEdited = displaycore.Range{Min: .84, Max: .95}, true
	if err := traceScientificApplyAxisViews(traceScientificWaveKey, false, displaycore.Settings{Axes: []displaycore.Axis{time}}); err != nil {
		t.Fatal(err)
	}
	native := traceScientificStyle.options
	if !native.TimeSet || native.TimeMinUS != 840000 || native.TimeMaxUS != 950000 {
		t.Fatalf("shared edit did not reach the native µs viewport: %+v", native)
	}
	o.TimeSet, o.TimeMinUS, o.TimeMaxUS = native.TimeSet, native.TimeMinUS, native.TimeMaxUS
	u := traceScientificPlotScene(1000, 540, nil, o)
	if !u.WaveAvailable || len(u.axisHits) != 3 {
		t.Fatalf("missing separate waveforms: %+v", u.WaveBounds)
	}
	marker := u.axisHits[1]
	x, y := int((marker.yLabel.l+marker.yLabel.r)/2), int((marker.yLabel.t+marker.yLabel.b)/2)
	if key, axis := u.scientificAxisAt(x, y); key != traceScientificWaveKey || axis != "time" {
		t.Fatalf("B pane axis did not pick shared time editor: %q/%q", key, axis)
	}
	x, y = int((marker.plot.l+marker.plot.r)/2), int((marker.plot.t+marker.plot.b)/2)
	if key, _ := u.scientificAxisAt(x, y); key != "" {
		t.Fatal("the shared settings zone covered the actual waveform")
	}
	o.TimeSet = false
	if reset := traceScientificPlotData(nil, o).nativeAxes[0].y.View; reset.Min != .8 || reset.Max != 1.054 {
		t.Fatalf("native graph reset left stale shared range: %+v", reset)
	}
}

func TestTraceScientificCommittedStylesControlProjectedAxes(t *testing.T) {
	data := traceScientificFixture()
	o := traceScientificPlotOptions{Data: &data, WaveOnly: true}
	base := traceScientificPlotData(nil, o).nativeAxes
	o.nativeAxes, o.axes = base, base
	o.axes[0].y.Title, o.axes[0].y.Unit, o.axes[0].y.Reverse = "记录时间", "ms", true
	o.axes[0].y.TickFontPT, o.axes[0].y.TickLengthSet = 13, true
	s := traceScientificPlotScene(990, 510, nil, o)
	for _, text := range s.text {
		if text.text == "记录时间 (ms)" && text.rotation == 900 {
			for _, path := range s.paths {
				if path.clip != nil && len(path.runs) > 0 && path.runs[0][0].y != path.clip.b {
					t.Fatalf("reversed style changed labels only, not curve projection: %+v", path.runs[0][0])
				}
			}
			return
		}
	}
	t.Fatal("shared label style did not reach the renderer")
}

func TestTraceScientificFrequencyBaseAxisAndDBTail(t *testing.T) {
	data := traceScientificFixture()
	d := traceScientificPlotData(nil, traceScientificPlotOptions{Data: &data})
	if d.frequency != (displaycore.Range{Min: 0, Max: 125}) || d.level.Min >= -80 || d.level.Max <= 0 {
		t.Fatalf("default viewport lost frequency limit or finite tails: %v × %v", d.frequency, d.level)
	}
	s := traceScientificPlotScene(760, 530, nil, traceScientificPlotOptions{Data: &data})
	if s.SpectrumBounds == nil || s.SpectrumBounds.b != s.WaveBounds[0].b || s.SpectrumBounds.t != s.WaveBounds[0].t {
		t.Fatalf("graph pairs are not aligned: %+v", s.WaveBounds)
	}
	for _, label := range s.text {
		if label.text == "125" && label.at.x == s.SpectrumBounds.r && label.at.x+7 < float64(s.width) {
			lower := segyanalysis.BuildTraceSnapshot([]segyanalysis.TraceInput{{Role: "A", IntervalUS: 8000, Samples: data.Series[0].Samples}}, false)
			if got := traceScientificPlotData(nil, traceScientificPlotOptions{Data: &lower}).frequency.Max; got != 62.5 {
				t.Fatalf("Hz beyond the real Nyquist: %g", got)
			}
			return
		}
	}
	t.Fatalf("125 Hz label at right plotted boundary was lost: %+v", s.SpectrumBounds)
}

func TestTraceScientificZeroEnergyHasNoSpectrumFloor(t *testing.T) {
	d := segyanalysis.BuildTraceSnapshot([]segyanalysis.TraceInput{{Role: "A", IntervalUS: 2000, Samples: make([]float64, 32)}}, false)
	for _, linear := range []bool{false, true} {
		s := traceScientificPlotScene(860, 530, nil, traceScientificPlotOptions{Data: &d, SpectrumOnly: true, LinearSpectrum: linear})
		if s.SpectrumAvailable || len(s.paths) == 0 || s.SpectrumBounds == nil {
			t.Fatalf("zero spectrum not represented as empty with axes, linear=%v", linear)
		}
		for _, p := range s.paths {
			if p.clip != nil {
				t.Fatal("an artificial spectrum line was drawn")
			}
		}
	}
}

func TestTraceScientificFullAmplitudeHasRealZeroTickAndPadding(t *testing.T) {
	samples := make([]float64, 1000)
	for i := range samples {
		samples[i] = math.Sin(float64(i) / 13)
	}
	samples[50] = 32
	d := segyanalysis.BuildTraceSnapshot([]segyanalysis.TraceInput{{Role: "A", IntervalUS: 2000, Samples: samples}}, false)
	full := traceScientificPlotScene(1000, 530, nil, traceScientificPlotOptions{Data: &d})
	robust := traceScientificPlotData(nil, traceScientificPlotOptions{Data: &d, RobustWindow: true})
	if full.effectiveAxes[0].x.View.Max <= 32 || robust.amplitude.Max >= 32 {
		t.Fatal("default does not show spikes or P1–P99 failed to remain a separate range")
	}
	for _, tick := range full.axisHits[0].xPlan.Ticks {
		if tick.Value == 0 && tick.Label == "0" {
			return
		}
	}
	t.Fatal("no actual zero tick on a symmetric automatic range")
}

func TestTraceScientificCleanSnapshotAxesAndStyles(t *testing.T) {
	samples := make([]float64, 351)
	neg := make([]float64, len(samples))
	for i := range samples {
		samples[i] = math.Sin(float64(i)*2*math.Pi*.08) + .15*math.Sin(float64(i)*2*math.Pi*.23)
		neg[i] = -samples[i]
	}
	data := segyanalysis.BuildTraceSnapshot([]segyanalysis.TraceInput{
		{Role: "A", StartUS: 800000, IntervalUS: 2000, Samples: samples},
		{Role: "B", StartUS: 800000, IntervalUS: 2000, Samples: neg},
	}, true)
	o := traceScientificPlotOptions{Data: &data, TimeInMS: true, ResearchStyle: true}
	a := traceScientificPlotScene(1070, 620, nil, o)
	if len(a.axisHits) != 2 || a.ink("A").width != 1.8 || a.ink("A").ink == a.ink("B").ink {
		t.Fatalf("unexpected scene style: %+v", a.style)
	}
	p := a.axisHits[0].yPlan
	if p.Ticks[0].Label != "800" || p.Ticks[len(p.Ticks)-1].Label != "1500" {
		t.Fatalf("default time labels: %+v", p.Ticks)
	}
	if a.effectiveAxes[1].y.View.Max <= 6.02 {
		t.Fatalf("positive residual spectrum was clipped: %+v", a.effectiveAxes[1].y.View)
	}
	o.VisibleRoles = []string{"A−B"}
	o.SideBySide, o.HideGrid, o.LineWidthDIP = true, true, 2.25
	b := traceScientificPlotScene(1070, 620, nil, o)
	if a.effectiveAxes != b.effectiveAxes || b.ink("A−B").width != 2.25 {
		t.Fatal("curve visibility or style changed effective data scales")
	}
	for _, path := range b.paths {
		if path.stroke.ink == traceScientificGrid {
			t.Fatal("hidden grid was still drawn")
		}
	}
}

func TestTraceScientificBoxClampsToInitialPanelAndKeepsAppliedSettings(t *testing.T) {
	d := traceScientificFixture()
	baseline := traceScientificPlotOptions{Data: &d, FrequencySet: true, FrequencyMinHz: 10, FrequencyMaxHz: 90, DBSet: true, DBMin: -101, DBMax: 12}
	base := traceScientificPlotData(nil, baseline)
	box := traceScientificBoxDrag{plot: traceScientificBounds{20, 30, 120, 130}, axes: base.axes[0], start: traceScientificPoint{45, 55}, end: traceScientificPoint{220, 325}}
	x, y, ok := box.ranges(5)
	if !ok || x.Max != base.amplitude.Max || y.Max != base.time.Max/1e6 || y.Min <= base.time.Min/1e6 {
		t.Fatalf("box was not constrained by the starting downward-time panel: %v × %v", x, y)
	}
	box.end = traceScientificPoint{46, 56}
	if _, _, ok := box.ranges(5); ok {
		t.Fatal("click was incorrectly accepted as a zoom")
	}
	box.axes, box.end = base.axes[1], traceScientificPoint{90, 85}
	x, y, ok = box.ranges(5)
	if !ok || x.Min <= 10 || x.Max >= 90 || y.Min <= -101 || y.Max >= 12 {
		t.Fatalf("spectrum box was not converted into frequency and dB bounds: %v × %v", x, y)
	}
	z := traceScientificViewport{specX: x, specDB: y}
	live := baseline
	z.apply(&live)
	got := traceScientificPlotData(nil, live)
	if got.frequency != x || got.level != y || got.reference != base.reference || got.time != base.time {
		t.Fatal("zoom changed baseline physics or axes")
	}
	got = traceScientificPlotData(nil, baseline)
	if got.frequency != base.frequency || got.level != base.level {
		t.Fatal("reset changed the applied manual ranges")
	}
}

func TestTraceScientificRasterKeepsFractionalAntialiasCoverage(t *testing.T) {
	s := traceScientificScene{width: 80, height: 60, dpi: 96}
	s.line(5.1, 24.4, 73.2, 26.7, traceScientificStroke{ink: uint32(rgbRef(0, 0, 0)), width: 1.2})
	img, err := s.raster(96)
	if err != nil {
		t.Fatal(err)
	}
	partial := 0
	for y := 20; y < 30; y++ {
		for x := 8; x < 70; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r == g && g == b && r > 0 && r < 65535 {
				partial++
			}
		}
	}
	if partial < 35 {
		t.Fatalf("only %d partially covered pixels; native AA did not receive fractional strokes", partial)
	}
}
