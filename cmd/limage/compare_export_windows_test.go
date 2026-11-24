//go:build windows

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi/artifact"
)

func writeNumericalCoverageFixture(t *testing.T, path string, traces, samples, crosslineFirst, delay int, factor float64) {
	t.Helper()
	writeNumericalRegressionFixture(t, path, traces, samples, delay, func(i int) (int, int) {
		return crosslineFirst + i, i + 1
	}, func(i, j int) float64 {
		if i == 0 {
			return 0 // legitimate zero trace, not missing
		}
		b := float64(j+delay/2) * .002
		return factor * (math.Sin(2*math.Pi*40*b) + .2*math.Sin(2*math.Pi*190*b)) * (float64(i%7) + 1)
	})
}

func writeNumericalRegressionFixture(t *testing.T, path string, traces, samples, delay int, geometry func(int) (xl, id int), sample func(int, int) float64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	main := make([]byte, 3600)
	binary.BigEndian.PutUint16(main[3216:], 2000)
	binary.BigEndian.PutUint16(main[3220:], uint16(samples))
	binary.BigEndian.PutUint16(main[3224:], 5)
	if _, err := f.Write(main); err != nil {
		t.Fatal(err)
	}
	row := make([]byte, 240+samples*4)
	for i := 0; i < traces; i++ {
		xl, id := geometry(i)
		binary.BigEndian.PutUint32(row[0:], uint32(id))
		binary.BigEndian.PutUint32(row[4:], uint32(id))
		binary.BigEndian.PutUint32(row[188:], 1870)
		binary.BigEndian.PutUint32(row[192:], uint32(xl))
		binary.BigEndian.PutUint16(row[108:], uint16(delay))
		binary.BigEndian.PutUint16(row[114:], uint16(samples))
		binary.BigEndian.PutUint16(row[116:], 2000)
		for j := 0; j < samples; j++ {
			binary.BigEndian.PutUint32(row[240+j*4:], math.Float32bits(float32(sample(i, j))))
		}
		if _, err := f.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func numericalExportFixtureSource(t *testing.T, path string) compareExportSource {
	t.Helper()
	f, err := openPoststackCompareSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, _, err := f.BuildPoststackSectionGeometryIndexContextProgress(context.Background(), 189, 193, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.SourceFileInfo()
	if err != nil {
		t.Fatal(err)
	}
	return compareExportSource{path: path, identity: id, geometry: g, originalStem: f.NumericalOriginalStem(),
		physical: [3]int64{0, f.Info.TraceCount - 1, 1},
		dt:       f.Info.SampleIntervalUS, samples: f.Info.SamplesPerTrace, sourceOrder: generatedPoststackTraceOrder(path), generated: compareExportGeneratedSection(path)}
}

func TestCompareNumericalExportFormatSelector(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d := numericalExportDialog{h: makeSettingsTestWindow(t, "STATIC", 0), initializing: true,
		scopeOptions: []int{compareExportVisible, compareExportLine, compareExportAll}}
	d.createControls(false)
	for i, want := range []segy.ExportSampleFormat{segy.ExportFormatSource, segy.ExportFormatIBM32, segy.ExportFormatIEEE32, segy.ExportFormatIEEE64} {
		if i > 0 {
			pSendMessageW.Call(d.format, CB_SETCURSEL, uintptr(i), 0)
		}
		q, err := d.currentRequest()
		if err != nil || q.format != want || q.kind != compareExportA || q.scope != compareExportVisible || !q.effective {
			t.Fatalf("sampling format choice %d changed another option: %+v, %v", i, q, err)
		}
	}
}

func TestCompareNumericalExportDefaultFilteredPreservesSourceHeaders(t *testing.T) {
	const nTr, nS, headerSize, originalTraceBytes = 3, 6, 3600, 240 + 6*4
	ctx, dir := context.Background(), t.TempDir()
	source := filepath.Join(dir, "input.sgy")
	read := func(path string) []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	writeNumericalRegressionFixture(t, source, nTr, nS, 800,
		func(i int) (int, int) { return []int{184, 184, 186}[i], 50 + i }, // duplicate geometry is a separate physical row
		func(_, _ int) float64 { return 0 })
	before := read(source)
	copy(before, "C 1 SOURCE TEXT MUST STAY EXACT")
	binary.BigEndian.PutUint16(before[3224:], 1) // IBM 32-bit, all valid zero amplitudes
	if err := os.WriteFile(source, before, 0600); err != nil {
		t.Fatal(err)
	}
	c := compareExportCapture{key: viewFilterKey{line: 1870}, actualLine: 1870, hasGeometry: true,
		axis: "Inline", axisUnits: "Crossline", sources: [2]compareExportSource{numericalExportFixtureSource(t, source)},
		spatial: compareExportBounds{184, 186, 800000, 810000}, frame: &viewFilterSnapshot{}}
	c.settings[0].Enabled = true
	c.frame.rows[0] = viewFilterRows{first: 0, last: nS - 1, values: map[int64][]float64{
		0: {1, 2, 3, 4, 5, 6}, 1: {-1, -2, -3, -4, -5, -6}, 2: {7, 8, 9, 10, 11, 12},
	}}
	for _, tc := range []struct {
		name        string
		format      segy.ExportSampleFormat
		wire, width int
		label       string
	}{
		{"default", segy.ExportFormatSource, 1, 4, "IBM 32 位"},
		{"ibm32", segy.ExportFormatIBM32, 1, 4, "IBM 32 位"},
		{"ieee32", segy.ExportFormatIEEE32, 5, 4, "IEEE 32 位"},
		{"ieee64", segy.ExportFormatIEEE64, 6, 8, "IEEE 64 位"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := planNumericalCompareExport(ctx, c, compareExportSelection{kind: compareExportA, scope: compareExportVisible, effective: true, format: tc.format})
			if want := int64(headerSize + nTr*(240+nS*tc.width)); err != nil || !p.preserveSourceHeaders || p.size != want ||
				!strings.Contains(p.summary, tc.label) || !strings.Contains(p.summary, "不新增扩展文本卷头") {
				t.Fatalf("wrong plan, size %d expected %d: %s (%v)", p.size, want, p.summary, err)
			}
			path := filepath.Join(dir, tc.name+".sgy")
			if err = executeNumericalCompareExport(ctx, path, p, nil); err != nil {
				t.Fatal(err)
			}
			raw := read(path)
			if int64(len(raw)) != p.size || !bytes.Equal(raw[:3200], before[:3200]) || tc.wire == 1 && !bytes.Equal(raw[:headerSize], before[:headerSize]) {
				t.Fatal("export changed the expected size or original reel header")
			}
			out, err := segy.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			if i := out.Info; i.FormatCode != tc.wire || i.ExtendedTextHeaders != 0 || i.DataStart != headerSize || i.TraceCount != nTr || i.SamplesPerTrace != nS || i.SampleIntervalUS != 2000 {
				t.Fatalf("wrong output layout: %+v", i)
			}
			for i := range nTr {
				a, b := headerSize+i*originalTraceBytes, headerSize+i*(240+nS*tc.width)
				if !bytes.Equal(raw[b:b+240], before[a:a+240]) {
					t.Fatalf("physical trace header %d changed", i)
				}
				values, err := out.ReadTraceWindow(int64(i), 0, nS-1)
				if err != nil || !reflect.DeepEqual(values, c.frame.rows[0].values[int64(i)]) {
					t.Fatalf("row %d not read from committed filtered samples: %v (%v)", i, values, err)
				}
			}
		})
	}
	if !bytes.Equal(before, read(source)) {
		t.Fatal("source was modified by export")
	}
}

func TestCompareNumericalExportRegressionExactFormulaAndRenderIndependence(t *testing.T) {
	const nTrace, nSample = 2273, 351
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "formula.sgy")
	writeNumericalRegressionFixture(t, source, nTrace, nSample, 800,
		func(i int) (int, int) { return 184 + i, 10000 + i },
		func(i, k int) float64 { return float64(1000*i + k) })
	f, err := segy.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h := createCtrl(0, "STATIC", "Numerical export regression", WS_OVERLAPPEDWINDOW, 0, 0, 1250, 600, 0)
	if h == 0 {
		t.Fatal("native temporary owner creation failed")
	}
	defer pDestroyWindow.Call(h)
	previousOwner, previousData, previousPath, previousMode, previousLine, previousWorkspace := compareHwnd, compareTD, compareAPath, compareMode, compareLineCoord, compareWorkspaceActive
	previousXMin, previousXMax := compareLineXMin, compareLineXMax
	previousT0, previousT1, previousStep, previousS0, previousS1 := compareTraceStart, compareTraceEnd, compareTraceStep, compareSampleStart, compareSampleEnd
	previousActive, previousFrame, previousWave := viewFilter.active, viewFilter.frame, poststackWaveDisplay
	previousRasterGeneration, previousGain := atomic.LoadInt64(&compareSliceGen), gainPercent
	defer func() {
		compareHwnd, compareTD, compareAPath, compareMode, compareLineCoord, compareWorkspaceActive = previousOwner, previousData, previousPath, previousMode, previousLine, previousWorkspace
		compareLineXMin, compareLineXMax = previousXMin, previousXMax
		compareTraceStart, compareTraceEnd, compareTraceStep, compareSampleStart, compareSampleEnd = previousT0, previousT1, previousStep, previousS0, previousS1
		viewFilter.active, viewFilter.frame, poststackWaveDisplay, gainPercent = previousActive, previousFrame, previousWave, previousGain
		atomic.StoreInt64(&compareSliceGen, previousRasterGeneration)
		displayCompareLayoutCache.valid = false
	}()
	geometry := numericalExportFixtureSource(t, source).geometry
	compareHwnd, compareTD, compareAPath, compareMode, compareLineCoord, compareWorkspaceActive = h, &compareTimeData{fa: f, ga: geometry}, source, 0, 1870, false
	compareLineXMin, compareLineXMax = 184, 2456
	compareTraceStart, compareTraceEnd, compareTraceStep, compareSampleStart, compareSampleEnd = 0, nTrace-1, 1, 0, nSample-1
	viewFilter.active, viewFilter.frame = [2]SideFilterState{}, nil
	gainPercent = 0
	poststackWaveDisplay = poststackWaveDefaultStyle()
	displayCompareLayoutCache.valid = false
	beforeStyle := viewFilterCurrentStyle()
	c, err := captureNumericalCompareExport()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []int{compareExportB, compareExportDifference} {
		if _, err := planNumericalCompareExport(ctx, c, compareExportSelection{kind: kind, scope: compareExportVisible, effective: true}); err == nil {
			t.Fatalf("missing B unexpectedly produced %s", compareExportNames[kind])
		}
	}

	check := func(c compareExportCapture, scope int, bounds compareExportBounds, i0, k0, nTr, nS int, effective bool, leaf string) {
		t.Helper()
		q := compareExportSelection{kind: compareExportA, scope: scope, manual: bounds, effective: effective, format: segy.ExportFormatIEEE64}
		p, err := planNumericalCompareExport(ctx, c, q)
		if err != nil {
			t.Fatal(err)
		}
		delay := 800 + 2*k0
		if len(p.rows) != nTr || p.samples != nS || p.dt != 2000 || p.first != int32(184+i0) || p.last != int32(184+i0+nTr-1) ||
			p.t0 != int64(delay*1000) || p.t1 != int64((delay+2*(nS-1))*1000) {
			t.Fatalf("wrong requested/actual scope: %s", p.summary)
		}
		file := filepath.Join(dir, leaf)
		if err := executeNumericalCompareExport(ctx, file, p, nil); err != nil {
			t.Fatal(err)
		}
		got, err := segy.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer got.Close()
		if got.Info.TraceCount != int64(nTr) || got.Info.SamplesPerTrace != nS || got.Info.FormatCode != 6 || got.Info.SampleIntervalUS != 2000 {
			t.Fatalf("wrong reopened dimensions/encoding: %+v", got.Info)
		}
		for i := 0; i < nTr; i++ {
			original := i0 + i
			header, err := got.ReadTraceHeader(int64(i))
			if err != nil || header.TraceSequenceLine != int32(10000+original) || header.TraceSequenceFile != int32(10000+original) ||
				header.Inline != 1870 || header.Crossline != int32(184+original) || header.DelayMS != delay ||
				header.SampleIntervalUS != 2000 || header.SampleCount != nS {
				t.Fatalf("row %d readback header: %+v; %v", i, header, err)
			}
			actual, err := got.ReadTraceWindow(int64(i), 0, nS-1)
			if err != nil || len(actual) != nS {
				t.Fatalf("row %d readback: %v, n=%d", i, err, len(actual))
			}
			for k, value := range actual {
				if want := float64(1000*original + k0 + k); value != want {
					t.Fatalf("row %d sample %d: got %g, want exactly %g", i, k, value, want)
				}
			}
		}
		t.Logf("%s: exact IEEE64 reread %d×%d, IL1870, XL%d–%d, t₀=%d ms dt=2 ms, endpoints %.0f/%.0f",
			leaf, nTr, nS, 184+i0, 184+i0+nTr-1, delay, float64(1000*i0+k0), float64(1000*(i0+nTr-1)+k0+nS-1))
	}
	check(c, compareExportVisible, compareExportBounds{}, 0, 0, nTrace, nSample, true, "default.sgy")
	poststackWaveDisplay.Mode, poststackWaveDisplay.Density, poststackWaveDisplay.Stride = poststackPlotWiggleVA, poststackWaveDensityStride, 37
	gainPercent = 17
	pMoveWindow.Call(h, 0, 0, 815, 520, 0)
	atomic.AddInt64(&compareSliceGen, 1)
	displayCompareLayoutCache.valid = false
	afterStyle := viewFilterCurrentStyle()
	if beforeStyle.clientSize == afterStyle.clientSize || !afterStyle.wave.active() || afterStyle.wave.Stride != 37 || beforeStyle.gain == afterStyle.gain {
		t.Fatalf("test did not change client size, wiggle stride and display gain: %+v -> %+v", beforeStyle, afterStyle)
	}
	restyled, err := captureNumericalCompareExport()
	if err != nil || restyled.key != c.key {
		t.Fatalf("display-only changes invalidated the numeric data identity: %v", err)
	}
	check(restyled, compareExportLine, compareExportBounds{}, 0, 0, nTrace, nSample, false, "styled-full.sgy")
	check(restyled, compareExportManual, compareExportBounds{1120, 1322, 1086000, 1176000}, 936, 143, 203, 46, false, "cropped.sgy")
}

func TestCompareNumericalExportRegressionSparseReorderedAndMissingB(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	xl := []int32{184, 191, 225, 901}
	source := filepath.Join(dir, "A.sgy")
	writeNumericalRegressionFixture(t, source, len(xl), 9, 800,
		func(i int) (int, int) { return int(xl[i]), 100 + i },
		func(i, k int) float64 { return float64(1000*i + k) })
	capture := compareExportCapture{key: viewFilterKey{line: 1870}, actualLine: 1870, hasGeometry: true,
		axis: "Inline", axisUnits: "Crossline", spatial: compareExportBounds{184, 901, 800000, 816000},
		sources: [2]compareExportSource{numericalExportFixtureSource(t, source)}}

	for _, tc := range []struct {
		name          string
		order, shared []int
	}{
		{"reordered", []int{3, 1, 0, 2}, []int{0, 1, 2, 3}},
		{"missing-middle", []int{3, 1, 0}, []int{0, 1, 3}},
		{"missing-edge", []int{1, 0, 2}, []int{0, 1, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := filepath.Join(dir, tc.name+"-B.sgy")
			writeNumericalRegressionFixture(t, b, len(tc.order), 5, 804,
				func(i int) (int, int) { return int(xl[tc.order[i]]), 900 + tc.order[i] },
				func(i, k int) float64 { return float64(100*tc.order[i] - 2*k) })
			c := capture
			c.sources[1] = numericalExportFixtureSource(t, b)
			p, err := planNumericalCompareExport(ctx, c, compareExportSelection{kind: compareExportDifference, scope: compareExportVisible})
			if err != nil || len(p.rows) != len(tc.shared) || p.samples != 5 || p.t0 != 804000 || p.t1 != 812000 ||
				p.first != xl[tc.shared[0]] || p.last != xl[tc.shared[len(tc.shared)-1]] {
				t.Fatalf("incorrect pairing or common coverage: %s; %v", p.summary, err)
			}
			path := filepath.Join(dir, tc.name+"-difference.sgy")
			if err := executeNumericalCompareExport(ctx, path, p, nil); err != nil {
				t.Fatal(err)
			}
			got, err := segy.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Close()
			if got.Info.TraceCount != int64(len(tc.shared)) {
				t.Fatalf("difference output added missing physical rows: %+v", got.Info)
			}
			for i, id := range tc.shared {
				h, err := got.ReadTraceHeader(int64(i))
				if err != nil || h.Inline != 1870 || h.Crossline != xl[id] || h.TraceSequenceLine != int32(100+id) ||
					h.DelayMS != 804 || h.SampleCount != 5 || h.SampleIntervalUS != 2000 {
					t.Fatalf("lost physical A identity/common sampling at output row %d: %+v %v", i, h, err)
				}
				values, err := got.ReadTraceWindow(int64(i), 0, 4)
				if err != nil || len(values) != 5 {
					t.Fatalf("read row %d: %v %v", i, values, err)
				}
				for k, actual := range values {
					// A starts two samples before B. Pair by XL, not file order.
					if want := float64(900*id + 2 + 3*k); actual != want {
						t.Fatalf("XL%d sample %d: %.0f != %.0f", xl[id], k, actual, want)
					}
				}
			}
			t.Logf("B physical order %v; %d actual paired rows, XL%d–%d at 804–812 ms, exact A−B", tc.order, len(tc.shared), p.first, p.last)
		})
	}
}

func TestCompareNumericalExportRegressionAutoNameReload(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	a := filepath.Join(dir, "survey_view_visible.sgy")
	writeNumericalRegressionFixture(t, a, 4, 8, 800,
		func(i int) (int, int) { return 184 + i, 100 + i },
		func(i, k int) float64 { return float64(1000*i + k) })
	capture := func(path string) compareExportCapture {
		return compareExportCapture{key: viewFilterKey{line: 1870}, actualLine: 1870, hasGeometry: true,
			axis: "Inline", axisUnits: "Crossline", sources: [2]compareExportSource{numericalExportFixtureSource(t, path)}}
	}
	q := compareExportSelection{kind: compareExportA, scope: compareExportManual, manual: compareExportBounds{184, 187, 800000, 814000}}
	p, err := planNumericalCompareExport(ctx, capture(a), q)
	if err != nil {
		t.Fatal(err)
	}
	if err := executeNumericalCompareExport(ctx, a, p, nil); err == nil {
		t.Fatal("A source path accepted as a destination")
	}
	base, err := numericalExportBasePath(dir, p)
	if err != nil || filepath.Base(base) != "survey_view_visible_A_XL184-187_T800-814ms.sgy" {
		t.Fatalf("wrong base auto name: %q %v", base, err)
	}
	if err := executeNumericalCompareExport(ctx, base, p, nil); err != nil {
		t.Fatal(err)
	}
	if err := executeNumericalCompareExport(ctx, base, p, nil); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("existing destination silently overwritten/unrecognized: %v", err)
	}
	again, err := availableNumericalExportPath(base)
	if err != nil || !strings.HasSuffix(again, "_001.sgy") {
		t.Fatalf("incorrect next automatic name: %q %v", again, err)
	}
	if err := executeNumericalCompareExport(ctx, again, p, nil); err != nil {
		t.Fatal(err)
	}
	custom, err := resolveNumericalExportPath("kept-explicit-name", dir)
	if err != nil || filepath.Base(custom) != "kept-explicit-name.sgy" {
		t.Fatalf("custom path changed: %q %v", custom, err)
	}
	if err := executeNumericalCompareExport(ctx, custom, p, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{again, custom} {
		q.manual = compareExportBounds{185, 186, 804000, 810000}
		p2, err := planNumericalCompareExport(ctx, capture(path), q)
		if err != nil || !strings.Contains(p2.summary, "Crossline 185–186 · 804–810 ms") {
			t.Fatalf("reload wrong scope: %v, %s", err, p2.summary)
		}
		next, err := numericalExportBasePath(dir, p2)
		if err != nil || filepath.Base(next) != "survey_view_visible_A_XL185-186_T804-810ms.sgy" {
			t.Fatalf("reload accumulated earlier source/scope suffix or lost origin: %q %v", next, err)
		}
		next, err = availableNumericalExportPath(next)
		if err != nil {
			t.Fatal(err)
		}
		if err := executeNumericalCompareExport(ctx, next, p2, nil); err != nil {
			t.Fatal(err)
		}
		f, err := segy.Open(next)
		if err != nil {
			t.Fatal(err)
		}
		values, err := f.ReadTraceWindow(1, 0, 3)
		f.Close()
		if !reflect.DeepEqual(values, []float64{2002, 2003, 2004, 2005}) || err != nil {
			t.Fatalf("updated name/summary does not match reopened contents: %v %v", values, err)
		}
		t.Logf("reloaded %s -> %s; correct new scope, second trace samples %v", filepath.Base(path), filepath.Base(next), values)
	}
	if _, err := os.Stat(filepath.Join(dir, "kept-explicit-name.sgy")); err != nil {
		t.Fatalf("custom original was lost: %v", err)
	}
}

func TestCompareNumericalExportRegressionRealReadOnly(t *testing.T) {
	path := os.Getenv("SEISFORGE_REAL_LEGACY_EXPORT_SOURCE_TEST")
	if path == "" {
		t.Skip("optional real SEG-Y source not supplied")
	}
	sourceHash := func() string {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			t.Fatal(err)
		}
		return string(h.Sum(nil))
	}
	originalHash := sourceHash()
	f, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx := context.Background()
	id, err := f.SourceFileInfo()
	if err != nil {
		t.Fatal(err)
	}
	g, _, err := f.BuildPoststackSectionGeometryIndexContextProgress(ctx, 189, 21, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := compareExportCapture{key: viewFilterKey{line: 1870}, actualLine: 1870, hasGeometry: true, axis: "Inline", axisUnits: "Crossline",
		sources: [2]compareExportSource{{path: path, geometry: g, originalStem: f.NumericalOriginalStem(), identity: id,
			physical: [3]int64{0, 2272, 1}, dt: f.Info.SampleIntervalUS, samples: f.Info.SamplesPerTrace}}}
	p, err := planNumericalCompareExport(ctx, c, compareExportSelection{kind: compareExportA, scope: compareExportLine, format: segy.ExportFormatIEEE64})
	if err != nil || len(p.rows) != 2273 || p.samples != 351 || p.first != 184 || p.last != 2456 || p.dt != 2000 || p.t0 != 800000 || p.t1 != 1500000 {
		t.Fatalf("unexpected real input coverage: %s; %v", p.summary, err)
	}
	out := filepath.Join(t.TempDir(), "real-read-only-reread.sgy")
	if err := executeNumericalCompareExport(ctx, out, p, nil); err != nil {
		t.Fatal(err)
	}
	reopened, err := segy.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Info.TraceCount != 2273 || reopened.Info.SamplesPerTrace != 351 || reopened.Info.FormatCode != 6 {
		t.Fatalf("real source reopened metadata: %+v", reopened.Info)
	}
	hdr := make([]byte, 240)
	for i := int64(0); i < 2273; i++ {
		if err := f.ReadTraceHeaderBytes(i, hdr); err != nil {
			t.Fatal(err)
		}
		h, err := reopened.ReadTraceHeader(i)
		if err != nil || int32(binary.BigEndian.Uint32(h.Raw[20:24])) != int32(184+i) || h.Inline != 1870 ||
			h.DelayMS != 800 || h.SampleCount != 351 || h.SampleIntervalUS != 2000 || !reflect.DeepEqual(h.Raw[:8], hdr[:8]) {
			t.Fatalf("real geometry/sequence row %d: %+v %v", i, h, err)
		}
		want, e1 := f.ReadTraceWindow(i, 0, 350)
		got, e2 := reopened.ReadTraceWindow(i, 0, 350)
		if e1 != nil || e2 != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("real decoded source -> IEEE64 -> reopen changed sample values for row %d: %v/%v", i, e1, e2)
		}
	}
	if sourceHash() != originalHash {
		t.Fatal("real source SHA-256 changed")
	}
	t.Logf("real SEG-Y read-only source SHA-256 %x; all 2273×351 decoded amplitudes and headers preserved in new IEEE64 output", originalHash)
}

func TestCompareNumericalExportFullFormalCoverageAndExactCrop(t *testing.T) {
	const traces, ns = 2273, 351
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "A.sgy")
	writeNumericalCoverageFixture(t, source, traces, ns, 184, 800, 1)
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	table := numericalExportFixtureSource(t, source)
	sourceIDs, coords, line, err := table.geometry.NumericLineTraceNumbers(0, 1870)
	if err != nil || len(sourceIDs) != traces || coords[0] != 184 || coords[traces-1] != 2456 || line != 1870 {
		t.Fatalf("source -> physical scope %d/%d line=%d: %v", len(sourceIDs), traces, line, err)
	}
	inputPath := filepath.Join(t.TempDir(), "input.sfdata")
	owned := t.TempDir()
	output := filepath.Join(owned, "output.sfdata")
	input, err := poststack.CaptureAlgorithmSection(ctx, poststack.SectionRequest{SourcePath: source,
		TraceIndices: sourceIDs, SampleStart: 0, SampleEnd: ns - 1, Direction: "inline", AlgorithmID: poststack.BandpassAlgorithmID}, inputPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	af, err := artifact.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if af.TraceCount() != traces || af.SamplesPerTrace() != ns {
		t.Fatalf("physical scope -> algorithm input: %d×%d", af.TraceCount(), af.SamplesPerTrace())
	}
	af.Close()
	params := poststack.DefaultBandpass(2000)
	if _, err = input.WriteBandpassArtifact(ctx, inputPath, output, params, nil); err != nil {
		t.Fatal(err)
	}
	result, err := input.AcceptResult(ctx, owned, output, "")
	if err != nil {
		t.Fatal(err)
	}
	defer result.Cleanup()
	if !reflect.DeepEqual(result.TraceIndices, sourceIDs) || result.SampleStart != 0 || result.SampleEnd != ns-1 {
		t.Fatal("algorithm artifact changes trace identities/order or sample range")
	}

	oldResults := generatedPoststackResults
	generatedPoststackResults = []*generatedPoststackResult{{Section: result}}
	defer func() { generatedPoststackResults = oldResults }()
	fb, err := openPoststackCompareSource(output)
	if err != nil {
		t.Fatal(err)
	}
	defer fb.Close()
	if fb.Info.TraceCount != traces || fb.Info.SamplesPerTrace != ns || fb.Info.SampleIntervalUS != 2000 {
		t.Fatalf("algorithm output -> B dimensions: %+v", fb.Info)
	}
	c := compareExportCapture{key: viewFilterKey{line: 1870}, actualLine: 1870, hasGeometry: true,
		axis: "Inline", axisUnits: "Crossline", spatial: compareExportBounds{184, 2456, 800000, 1500000},
		sources: [2]compareExportSource{table, numericalExportFixtureSource(t, output)}}

	check := func(kind, scope int, bounds compareExportBounds, expectTraces, expectSamples, expectT0, firstPhysical int, leaf string) {
		t.Helper()
		plan, err := planNumericalCompareExport(ctx, c, compareExportSelection{kind: kind, scope: scope, effective: true, manual: bounds, format: segy.ExportFormatIEEE64})
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.rows) != expectTraces || plan.samples != expectSamples || plan.t0 != int64(expectT0)*1000 || plan.dt != 2000 {
			t.Fatalf("B -> export plan: %s", plan.summary)
		}
		path := filepath.Join(dir, leaf)
		if err = executeNumericalCompareExport(ctx, path, plan, nil); err != nil {
			t.Fatal(err)
		}
		got, err := segy.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer got.Close()
		if got.Info.TraceCount != int64(expectTraces) || got.Info.SamplesPerTrace != expectSamples ||
			got.Info.SampleIntervalUS != 2000 || got.Info.FormatCode != 6 {
			t.Fatalf("write -> read dimensions: %+v", got.Info)
		}
		mapping, err := got.DetectPoststackSectionGeometryBytes(2)
		if err != nil || mapping.InlineByte != 189 || mapping.CrosslineByte != 193 || mapping.LabelsHeuristic {
			t.Fatalf("Compare export did not record its source header mapping for deterministic reopen: %+v %v", mapping, err)
		}
		for i := 0; i < expectTraces; i++ {
			trace := int64(i + firstPhysical)
			header, err := got.ReadTraceHeader(int64(i))
			if err != nil || header.DelayMS != expectT0 || header.Inline != 1870 || header.Crossline != int32(184+trace) ||
				header.SampleCount != expectSamples || header.SampleIntervalUS != 2000 {
				t.Fatalf("row %d missing physical identity or axis: %+v (%v)", i, header, err)
			}
			values, err := got.ReadTraceWindow(int64(i), 0, expectSamples-1)
			aFirst := (expectT0 - 800) / 2
			want, err2 := fb.ReadTraceWindow(trace, aFirst, aFirst+expectSamples-1)
			if kind == compareExportA {
				replayA, openErr := segy.Open(source)
				if openErr != nil {
					t.Fatal(openErr)
				}
				want, err2 = replayA.ReadTraceWindow(trace, aFirst, aFirst+expectSamples-1)
				replayA.Close()
			}
			if err != nil || err2 != nil || !reflect.DeepEqual(values, want) {
				t.Fatalf("row %d samples differ after reopening: %v/%v", i, err, err2)
			}
		}
		t.Logf("source %dx%d -> selected %dx%d -> algorithm input/output %dx%d -> selected and reopened %dx%d, t0=%d ms dt=2 ms, physical [%d..%d]",
			traces, ns, traces, ns, traces, ns, expectTraces, expectSamples, expectT0, firstPhysical, firstPhysical+expectTraces-1)
	}
	check(compareExportB, compareExportAll, compareExportBounds{}, traces, ns, 800, 0, "formal-all.sgy")
	check(compareExportB, compareExportManual, compareExportBounds{1120, 1322, 1086000, 1176000}, 203, 46, 1086, 936, "formal-crop.sgy")
	// A genuinely local formal result must not be advertised as a complete
	// processed line. All available B is precisely the artifact's own scope.
	localInputPath := filepath.Join(t.TempDir(), "local-input.sfdata")
	localDir := t.TempDir()
	localPath := filepath.Join(localDir, "local-output.sfdata")
	localInput, err := poststack.CaptureAlgorithmSection(ctx, poststack.SectionRequest{SourcePath: source,
		TraceIndices: sourceIDs[936:1139], SampleStart: 143, SampleEnd: 188, Direction: "inline", AlgorithmID: poststack.BandpassAlgorithmID}, localInputPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = localInput.WriteBandpassArtifact(ctx, localInputPath, localPath, params, nil); err != nil {
		t.Fatal(err)
	}
	local, err := localInput.AcceptResult(ctx, localDir, localPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer local.Cleanup()
	generatedPoststackResults = append(generatedPoststackResults, &generatedPoststackResult{Section: local})
	c.sources[1] = numericalExportFixtureSource(t, localPath)
	if _, err = planNumericalCompareExport(ctx, c, compareExportSelection{kind: compareExportB, scope: compareExportLine, effective: true}); err == nil || !strings.Contains(err.Error(), "请先处理完整范围") {
		t.Fatalf("a cropped B was treated as a full processed line: %v", err)
	}
	p, err := planNumericalCompareExport(ctx, c, compareExportSelection{kind: compareExportB, scope: compareExportAll, effective: true})
	if err != nil || len(p.rows) != 203 || p.samples != 46 || p.t0 != 1086000 || p.t1 != 1176000 ||
		p.sourceFormatCode != 5 || p.preserveSourceHeaders || !strings.Contains(p.summary, "IEEE 32 位") {
		t.Fatalf("the local artifact's true coverage changed: %s %v", p.summary, err)
	}
	currentInfo, err := os.Stat(source)
	if err != nil || !os.SameFile(sourceInfo, currentInfo) || !currentInfo.ModTime().Equal(sourceInfo.ModTime()) || currentInfo.Size() != sourceInfo.Size() {
		t.Fatal("export changed A", err)
	}
}

func TestCompareNumericalExportLocalAndCommittedStage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "A.sgy"), filepath.Join(dir, "local-B.sgy")
	writeNumericalRegressionFixture(t, a, 8, 351, 800,
		func(i int) (int, int) { return 184 + i, 100 + i },
		func(i, k int) float64 { return float64(200*i + k) })
	writeNumericalRegressionFixture(t, b, 3, 46, 1086,
		func(i int) (int, int) { return 186 + i, 900 + i },
		func(i, k int) float64 { return float64(100*(i+1) - 4*k) })
	c := compareExportCapture{key: viewFilterKey{line: 1870}, actualLine: 1870, hasGeometry: true,
		axis: "Inline", axisUnits: "Crossline", spatial: compareExportBounds{184, 191, 800000, 1500000},
		sources: [2]compareExportSource{numericalExportFixtureSource(t, a), numericalExportFixtureSource(t, b)}}
	rect := compareExportBounds{187, 188, 1088000, 1090000}
	c.rectangle = &rect
	c.settings[1].Enabled = true
	c.frame = &viewFilterSnapshot{}
	c.frame.rows[1] = viewFilterRows{first: 0, last: 3, values: map[int64][]float64{1: {105, 19, 22, 107}, 2: {109, 23, 29, 111}}}
	if _, err := planNumericalCompareExport(ctx, c, compareExportSelection{kind: compareExportB, scope: compareExportAll, effective: true}); err == nil || !strings.Contains(err.Error(), "不覆盖") {
		t.Fatalf("uncovered cached result allowed or silently reprocessed: %v", err)
	}
	wantA, wantB0, wantB1 := [2][2]float64{{744, 745}, {944, 945}}, [2][2]float64{{196, 192}, {296, 292}}, [2][2]float64{{19, 22}, {23, 29}}
	for _, mode := range []struct {
		name      string
		kind      int
		effective bool
		want      [2][2]float64
	}{
		{"base-A", compareExportA, false, wantA},
		{"effective-A", compareExportA, true, wantA},
		{"base-B", compareExportB, false, wantB0},
		{"effective-B", compareExportB, true, wantB1},
		{"base-difference", compareExportDifference, false, [2][2]float64{{548, 553}, {648, 653}}},
		{"effective-difference", compareExportDifference, true, [2][2]float64{{725, 723}, {921, 916}}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			p, err := planNumericalCompareExport(ctx, c, compareExportSelection{kind: mode.kind, scope: compareExportRectangle, effective: mode.effective, format: segy.ExportFormatIEEE64})
			if err != nil || len(p.rows) != 2 || p.samples != 2 || p.t0 != 1088000 || p.t1 != 1090000 {
				t.Fatalf("stage changed actual crop: %s %v", p.summary, err)
			}
			output := filepath.Join(dir, mode.name+".sgy")
			if err := executeNumericalCompareExport(ctx, output, p, nil); err != nil {
				t.Fatal(err)
			}
			out, err := segy.Open(output)
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			for i, want := range mode.want {
				h, err := out.ReadTraceHeader(int64(i))
				if err != nil || h.Inline != 1870 || h.Crossline != int32(187+i) || h.DelayMS != 1088 {
					t.Fatalf("stage changed selected identity: %+v %v", h, err)
				}
				actual, err := out.ReadTraceWindow(int64(i), 0, 1)
				if err != nil || !reflect.DeepEqual(actual, want[:]) {
					t.Fatalf("row %d: got %v, want exactly %v: %v", i, actual, want, err)
				}
			}
			t.Logf("exact IEEE64 stage readback at XL187–188, 1088–1090 ms: %v", mode.want)
		})
	}
	p, err := planNumericalCompareExport(ctx, c, compareExportSelection{kind: compareExportB, scope: compareExportVisible})
	if err != nil || len(p.rows) != 3 || p.samples != 46 || p.t0 != 1086000 {
		t.Fatalf("explicit local B intersection not reported: %s %v", p.summary, err)
	}
}
