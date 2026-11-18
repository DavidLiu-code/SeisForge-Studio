//go:build windows

package main

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func legacySpectrumFFTRadix2(values []complex128) {
	n := len(values)
	position := 0
	for index := 1; index < n; index++ {
		bit := n >> 1
		for ; position&bit != 0; bit >>= 1 {
			position ^= bit
		}
		position ^= bit
		if index < position {
			values[index], values[position] = values[position], values[index]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		angle := -2 * math.Pi / float64(length)
		step := complex(math.Cos(angle), math.Sin(angle))
		for begin := 0; begin < n; begin += length {
			rotation := complex(1.0, 0)
			half := length >> 1
			for offset := 0; offset < half; offset++ {
				even := values[begin+offset]
				odd := values[begin+offset+half] * rotation
				values[begin+offset] = even + odd
				values[begin+offset+half] = even - odd
				rotation *= step
			}
		}
	}
}

func TestSpectrumBaseCurrentShareAnalysisAndAmplitudeCalibration(t *testing.T) {
	const ns = 256
	base := make([]float64, ns)
	for i := range base {
		base[i] = 2 * math.Sin(2*math.Pi*32*float64(i)*0.002)
	}
	var calls [2][][3]int64
	reader := func(side int, gain float64) func(int64, int, int) ([]float64, error) {
		return func(tr int64, first, last int) ([]float64, error) {
			calls[side] = append(calls[side], [3]int64{tr, int64(first), int64(last)})
			x := append([]float64(nil), base[first:last+1]...)
			for i := range x {
				x[i] *= gain
			}
			return x, nil
		}
	}
	row := viewFilterRows{traces: []int64{10, 2, 7}, ns: ns, dt: 2000, first: 0, last: ns - 1}
	curves, linear, err := spectrumLinearPair(context.Background(), row, reader(0, 1), reader(1, 0.5))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls[0], calls[1]) {
		t.Fatalf("baseline/current used different traces/window: %v / %v", calls[0], calls[1])
	}
	ref, peak := 0.0, 0
	for i, v := range linear[0] {
		if v > ref {
			ref, peak = v, i
		}
	}
	calibrateSpectrum([]*spectrumCurve{&curves[0], &curves[1]}, [][]float64{linear[0], linear[1]}, ref)
	if math.Abs(curves[0].DB[peak]) > 1e-10 || math.Abs(curves[1].DB[peak]-20*math.Log10(0.5)) > 1e-10 {
		t.Fatalf("attenuation hidden by normalization: %g / %g", curves[0].DB[peak], curves[1].DB[peak])
	}
	if curves[0].NFFT != curves[1].NFFT || !reflect.DeepEqual(curves[0].Freq, curves[1].Freq) {
		t.Fatal("different FFT calibration")
	}
}

func TestSpectrumAnalysisRejectsShortWindowReadFailuresAndCancellation(t *testing.T) {
	read := func(_ int64, first, last int) ([]float64, error) { return make([]float64, last-first+1), nil }
	row := viewFilterRows{traces: []int64{0}, ns: 64, dt: 2000, first: 0, last: 12}
	if _, _, err := spectrumLinearPair(context.Background(), row, read, read); err == nil {
		t.Fatal("short spectral window accepted")
	}
	row.last = 63
	errRead := errors.New("failed current view")
	bad := func(int64, int, int) ([]float64, error) { return nil, errRead }
	if _, _, err := spectrumLinearPair(context.Background(), row, read, bad); !errors.Is(err, errRead) {
		t.Fatalf("different subsets silently averaged: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := spectrumLinearPair(ctx, row, read, read); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled analysis: %v", err)
	}
}

func TestSpectrumPairUsesFiniteMeanOfExtremeMagnitudes(t *testing.T) {
	const ns = 64
	const amplitude = 0x1p1019
	row := viewFilterRows{traces: []int64{10, 20}, ns: ns, dt: 2000, first: 0, last: ns - 1}
	read := func(gain float64) func(int64, int, int) ([]float64, error) {
		return func(_ int64, first, last int) ([]float64, error) {
			values := make([]float64, last-first+1)
			for i := range values {
				values[i] = amplitude * gain
				if (first+i)%2 != 0 {
					values[i] = -values[i]
				}
			}
			return values, nil
		}
	}
	curves, linear, err := spectrumLinearPair(context.Background(), row, read(1), read(0.5))
	if err != nil {
		t.Fatal(err)
	}
	// The alternating samples have zero mean and put their exact amplitude
	// at Nyquist after Hann coherent-gain normalization. Each raw FFT is
	// finite; summing two raw magnitudes before averaging would overflow.
	peak := ns / 2
	if curves[0].Freq[peak] != 250 || curves[0].TraceCount != 2 ||
		math.Abs(linear[0][peak]/amplitude-1) > 1e-12 || math.Abs(linear[1][peak]/amplitude-0.5) > 1e-12 {
		t.Fatalf("incorrect amplitude calibration at Nyquist: frequency=%g traces=%d A=%g B=%g", curves[0].Freq[peak], curves[0].TraceCount, linear[0][peak], linear[1][peak])
	}
	calibrateSpectrum([]*spectrumCurve{&curves[0], &curves[1]}, [][]float64{linear[0], linear[1]}, linear[0][peak])
	if curves[0].DB[peak] != 0 || math.Abs(curves[1].DB[peak]+6.020599913279624) > 1e-10 {
		t.Fatalf("incorrect shared-reference dB: A=%g B=%g", curves[0].DB[peak], curves[1].DB[peak])
	}
	t.Logf("calibrated Nyquist A=%g B=%g, B %.12f dB relative to A", linear[0][peak], linear[1][peak], curves[1].DB[peak])
}

func TestSpectrumBReadFailureRetainsAAndLabelsReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spectrum-A.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 4, Samples: 256}); err != nil {
		t.Fatal(err)
	}
	f, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := f.SourceFileInfo()
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	row := viewFilterRows{path: path, traces: []int64{0, 1}, ns: 256, dt: 2000, first: 0, last: 255}
	b := row
	b.path = filepath.Join(t.TempDir(), "missing-B.sgy")
	r := spectrumRequest{ctx: context.Background(), sources: [2]spectrumSource{{row: row, identity: info}, {row: b, identity: info}}}
	d, err := computePoststackSpectrum(r)
	if err != nil || len(d.A.Freq) == 0 || d.HasB || d.BError == "" {
		t.Fatalf("B failure lost A/error: %+v / %v", d, err)
	}
}

func TestSpectrumRatioReadsCommittedCurrentViewAndChecksRealHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ratio-A.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 2, Samples: 256}); err != nil {
		t.Fatal(err)
	}
	f, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.SourceFileInfo()
	if err != nil {
		t.Fatal(err)
	}
	origin, err := f.TraceTimeOriginUS(0)
	if err != nil {
		t.Fatal(err)
	}
	row := viewFilterRows{path: path, traces: []int64{0, 1}, ns: 256, dt: f.Info.SampleIntervalUS, first: 0, last: 255, originUS: origin}
	filtered := row
	filtered.values = make(map[int64][]float64)
	for _, tr := range row.traces {
		x, err := f.ReadTraceWindow(tr, 0, 255)
		if err != nil {
			t.Fatal(err)
		}
		for i := range x {
			x[i] *= 0.5
		}
		filtered.values[tr] = x
	}
	source := spectrumSource{row: row, identity: info, keys: [][2]int32{{1, 1}, {1, 2}}}
	r := spectrumRequest{ctx: context.Background(), sources: [2]spectrumSource{source, source}}
	r.sources[1].view = &filtered
	d, err := computePoststackSpectrum(r)
	if err != nil || !d.HasB || len(d.DiffDB) == 0 || d.RatioReason != "" {
		t.Fatalf("ratio not generated: %+v / %v", d, err)
	}
	peak := 1
	for i, v := range d.A.DB {
		if i > 0 && v > d.A.DB[peak] {
			peak = i
		}
	}
	if math.Abs(d.DiffDB[peak]-20*math.Log10(2)) > 1e-9 {
		t.Fatalf("used B base instead of its current view: %v", d.DiffDB[peak])
	}
	// Metadata equality alone is insufficient: verify the selected trace headers.
	r.sources[0].row.originUS++
	r.sources[1].row.originUS++
	d, err = computePoststackSpectrum(r)
	if err != nil || !d.HasB || len(d.A.Freq) == 0 || len(d.B.Freq) == 0 || len(d.DiffDB) != 0 || d.RatioReason == "" {
		t.Fatalf("incompatible real header accepted or independent spectra lost: %+v / %v", d, err)
	}
}

func TestSpectrumResultRejectsChangedSideParametersSourceAndRange(t *testing.T) {
	k := viewFilterKey{owner: 1, a: "A", b: "B", first: 1, last: 128, bVersion: 2}
	settings := [2]spectrumSettingKey{{Enabled: true, Params: poststack.ViewBandpass{LowHz: 10, HighHz: 100}}, {}}
	r := &spectrumResult{id: 4, request: spectrumRequest{key: k, settings: settings}}
	if !spectrumResultCurrent(r, 4, k, settings) {
		t.Fatal("current rejected")
	}
	if spectrumResultCurrent(r, 5, k, settings) {
		t.Fatal("old worker accepted")
	}
	for _, change := range []func(*viewFilterKey){func(k *viewFilterKey) { k.owner++ }, func(k *viewFilterKey) { k.first++ }, func(k *viewFilterKey) { k.bVersion++ }, func(k *viewFilterKey) { k.render++ }, func(k *viewFilterKey) { k.geometry++ }, func(k *viewFilterKey) { k.a = "new A" }} {
		next := k
		change(&next)
		if spectrumResultCurrent(r, 4, next, settings) {
			t.Fatal("stale source/range accepted")
		}
	}
	next := settings
	next[1] = spectrumSettingKey{Enabled: true, Params: poststack.ViewBandpass{LowHz: 20, HighHz: 90}}
	if spectrumResultCurrent(r, 4, k, next) {
		t.Fatal("old B spectrum accepted after separate B apply")
	}
}

func TestSpectrumIntegratedControlsFitAtDPIAndMinimumSizes(t *testing.T) {
	for _, dpi := range []int{96, 144, 192} {
		for _, size := range [][2]int{{560, 300}, {660, 360}, {560, 540}, {660, 600}, {920, 720}, {1400, 900}} {
			w, h := size[0]*dpi/96, size[1]*dpi/96
			l := spectrumLayout(w, h, dpi)
			ranges := []RECT{l.Object, l.A, l.B, l.Both, l.Scope, l.LowLabel, l.Low, l.HighLabel, l.High, l.Method, l.StateA, l.StateB, l.Status, l.Apply, l.Restore, l.Close, l.Base, l.Current, l.Ratio, l.ResponsePreview}
			ranges = append(ranges, l.DesignLabels[:]...)
			ranges = append(ranges, l.DesignFields[:]...)
			if l.Plot != (RECT{}) {
				ranges = append(ranges, l.Plot, l.Legend)
			}
			for _, r := range ranges {
				if r.Left < 0 || r.Top < 0 || r.Right > int32(w) || r.Bottom > int32(h) || r.Right <= r.Left || r.Bottom <= r.Top {
					t.Fatalf("out of client %v DPI %d: %+v", size, dpi, r)
				}
			}
			for i, a := range ranges {
				for j, b := range ranges {
					if i < j && a.Left < b.Right && b.Left < a.Right && a.Top < b.Bottom && b.Top < a.Bottom {
						t.Fatalf("overlap size %v DPI %d controls %d/%d: %+v %+v", size, dpi, i, j, a, b)
					}
				}
			}
		}
	}
}

func TestSpectrumCurrentRatioUsesMatchingPairsAndPhysicalWindow(t *testing.T) {
	const samples = 256
	a := spectrumSource{row: viewFilterRows{traces: []int64{10, 20, 30}, first: 100, last: 355, ns: 400, dt: 2000}, keys: [][2]int32{{1870, 1}, {1870, 2}, {1870, 3}}}
	b := spectrumSource{row: viewFilterRows{traces: []int64{2, 1, 3}, first: 0, last: 255, ns: 256, dt: 2000, originUS: 200000}, keys: [][2]int32{{1870, 2}, {1870, 1}, {1870, 9}}}
	var calls [2][]int64
	read := func(side int, gain float64) func(int64, int, int) ([]float64, error) {
		return func(tr int64, first, last int) ([]float64, error) {
			calls[side] = append(calls[side], tr)
			wantFirst := 100
			if side == 1 {
				wantFirst = 0
			}
			if first != wantFirst || last-first+1 != samples {
				t.Fatalf("wrong actual-time FFT window: side=%d %d..%d", side, first, last)
			}
			if tr == 30 || (side == 1 && tr == 3) {
				t.Fatal("unmatched trace entered the ratio")
			}
			x := make([]float64, samples)
			for i := range x {
				x[i] = gain * math.Sin(2*math.Pi*float64(i)*32/samples)
			}
			return x, nil
		}
	}
	sources := [2]spectrumSource{a, b}
	for _, gain := range []float64{1, 0.5} {
		calls = [2][]int64{}
		got, err := spectrumRatioLinear(context.Background(), sources, [2]func(int64, int, int) ([]float64, error){read(0, 1), read(1, gain)})
		if err != nil {
			t.Fatal(err)
		}
		if got.TraceCount != 2 || got.NFFT != samples || math.Abs(got.DB[32]-20*math.Log10(1/gain)) > 1e-10 {
			t.Fatalf("incorrect current ratio: %+v", got)
		}
		if !reflect.DeepEqual(calls[0], []int64{10, 20}) || !reflect.DeepEqual(calls[1], []int64{1, 2}) {
			t.Fatalf("reordered B pairing wrong: %v", calls)
		}
	}
	for name, change := range map[string]func(*[2]spectrumSource){
		"dt":                 func(s *[2]spectrumSource) { s[1].row.dt++ },
		"origin":             func(s *[2]spectrumSource) { s[1].row.originUS++ },
		"window":             func(s *[2]spectrumSource) { s[1].row.last-- },
		"missing geometry":   func(s *[2]spectrumSource) { s[1].keys = nil },
		"different line":     func(s *[2]spectrumSource) { s[1].keys = [][2]int32{{1, 2}, {1, 1}, {1, 9}} },
		"duplicate geometry": func(s *[2]spectrumSource) { s[1].keys = [][2]int32{{1870, 1}, {1870, 1}, {1870, 9}} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := sources
			change(&bad)
			if _, err := spectrumRatioPairs(bad); err == nil {
				t.Fatal("incompatible ratio accepted")
			}
		})
	}
}

func TestSpectrumGeometryKeysUseActualCoordinates(t *testing.T) {
	g := &segy.GeometryIndex{InlineValues: []int32{1870}, CrosslineValues: []int32{184, 185}, TraceNumbers: []int64{12, 4}, RowOfTrace: []int32{0, 0}, ColOfTrace: []int32{1, 0}}
	if got := spectrumGeometryKeys(g, []int64{4, 12}); !reflect.DeepEqual(got, [][2]int32{{1870, 184}, {1870, 185}}) {
		t.Fatalf("wrong actual keys: %v", got)
	}
	if got := spectrumGeometryKeys(g, []int64{99}); got != nil {
		t.Fatal("missing physical trace invented geometry")
	}
}

func TestSpectrumDraftAndObjectEditsDoNotStartFiltering(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	oldH, oldUI, oldDraft, oldData := spectrumHwnd, spectrumUI, spectrumDraft, spectrumData
	defer func() { spectrumHwnd, spectrumUI, spectrumDraft, spectrumData = oldH, oldUI, oldDraft, oldData }()
	h := createCtrl(0, "STATIC", "spectrum draft fixture", WS_OVERLAPPEDWINDOW, 0, 0, 940, 760, 0)
	if h == 0 {
		t.Fatal("native spectrum fixture not created")
	}
	defer pDestroyWindow.Call(h)
	spectrumHwnd = h
	makePoststackSpectrumControls(h)
	beforeFilter, beforeSpectrum := viewFilter.id, spectrumTask.id
	beforeSettings := spectrumSettings()
	setText(spectrumUI.low, "13.5")
	setText(spectrumUI.high, "88")
	poststackSpectrumCommand(spectrumLowID | uintptr(0x0300)<<16)
	poststackSpectrumCommand(spectrumTargetA)
	if spectrumDraft.low != "13.5" || spectrumDraft.high != "88" || spectrumDraft.target != viewFilterA {
		t.Fatalf("draft lost: %+v", spectrumDraft)
	}
	if viewFilter.id != beforeFilter || spectrumTask.id != beforeSpectrum || spectrumSettings() != beforeSettings {
		t.Fatal("editing draft/target submitted or changed applied filter")
	}
}

func TestSpectrumTimeSliceRetainsAnalysisButDisablesTimeFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slice-A.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 2, Samples: 128}); err != nil {
		t.Fatal(err)
	}
	f, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g := &segy.GeometryIndex{InlineValues: []int32{1}, CrosslineValues: []int32{1, 2}, TraceNumbers: []int64{0, 1}, RowOfTrace: []int32{0, 0}, ColOfTrace: []int32{0, 1}}
	oldTD, oldPath, oldMode, oldWorkspace := compareTD, compareAPath, compareMode, compareWorkspaceActive
	oldFirst, oldLast := compareSampleStart, compareSampleEnd
	defer func() {
		compareTD, compareAPath, compareMode, compareWorkspaceActive = oldTD, oldPath, oldMode, oldWorkspace
		compareSampleStart, compareSampleEnd = oldFirst, oldLast
	}()
	compareTD, compareAPath, compareMode, compareWorkspaceActive = &compareTimeData{fa: f, ga: g, bounds: g.Bounds()}, path, 2, false
	compareSampleStart, compareSampleEnd = 0, 127
	r, err := capturePoststackSpectrumRequest()
	if err != nil {
		t.Fatal(err)
	}
	d, err := computePoststackSpectrum(r)
	if err != nil || len(d.A.Freq) == 0 || d.A.TraceCount != 2 {
		t.Fatalf("time-slice spectrum regressed: %+v %v", d.A, err)
	}
	if _, dt := poststackViewFilterSideScope(0); dt != 0 {
		t.Fatal("time-slice filtering should remain disabled")
	}
}

func legacyMeanSpectrumReference(file *segy.File, traces []int64, sampleStart, sampleEnd int) (spectrumCurve, error) {
	if sampleStart < 0 {
		sampleStart = 0
	}
	if sampleEnd < 0 || sampleEnd >= file.Info.SamplesPerTrace {
		sampleEnd = file.Info.SamplesPerTrace - 1
	}
	nfft := nextPow2LE(sampleEnd - sampleStart + 1)
	sampleEnd = sampleStart + nfft - 1
	traces = evenlySampleTraces(traces, 96)
	accumulated := make([]float64, nfft/2+1)
	used := 0
	for _, trace := range traces {
		samples, err := file.ReadTraceWindow(trace, sampleStart, sampleEnd)
		if err != nil {
			continue
		}
		mean := 0.0
		for _, value := range samples {
			mean += value
		}
		mean /= float64(len(samples))
		fft := make([]complex128, nfft)
		for index, value := range samples {
			window := 0.5 - 0.5*math.Cos(2*math.Pi*float64(index)/float64(nfft-1))
			fft[index] = complex((value-mean)*window, 0)
		}
		legacySpectrumFFTRadix2(fft)
		for bin := range accumulated {
			accumulated[bin] += math.Hypot(real(fft[bin]), imag(fft[bin]))
		}
		used++
	}
	maximum := 0.0
	peakBin := 1
	for bin := 1; bin < len(accumulated); bin++ {
		accumulated[bin] /= float64(used)
		if accumulated[bin] > maximum {
			maximum = accumulated[bin]
			peakBin = bin
		}
	}
	if maximum <= 0 {
		maximum = 1
	}
	frequency := make([]float64, len(accumulated))
	db := make([]float64, len(accumulated))
	dt := float64(file.Info.SampleIntervalUS) * 1e-6
	for bin, value := range accumulated {
		frequency[bin] = float64(bin) / (float64(nfft) * dt)
		ratio := value / maximum
		if ratio < 1e-6 {
			ratio = 1e-6
		}
		db[bin] = 20 * math.Log10(ratio)
		if db[bin] < -80 {
			db[bin] = -80
		}
	}
	return spectrumCurve{
		Freq: frequency, DB: db,
		PeakHz:     float64(peakBin) / (float64(nfft) * dt),
		TraceCount: used, NFFT: nfft,
	}, nil
}

func TestMeanSpectrumRefactorPreservesLegacyCurveAndLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mean-spectrum.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 120, Samples: 5000}); err != nil {
		t.Fatal(err)
	}
	file, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	traces := make([]int64, 120)
	for index := range traces {
		traces[index] = int64(index)
	}
	const sampleStart, sampleEnd = 37, 4800
	want, err := legacyMeanSpectrumReference(file, traces, sampleStart, sampleEnd)
	if err != nil {
		t.Fatal(err)
	}
	got, err := meanSpectrum(file, traces, sampleStart, sampleEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mean-spectrum refactor changed the legacy curve:\n got=%+v\nwant=%+v", got, want)
	}
	if got.TraceCount != 96 || got.NFFT != 4096 {
		t.Fatalf("legacy trace/NFFT limits changed: %+v", got)
	}
	if nyquist := got.Freq[len(got.Freq)-1]; nyquist != 250 {
		t.Fatalf("physical Nyquist changed: %g", nyquist)
	}
}
