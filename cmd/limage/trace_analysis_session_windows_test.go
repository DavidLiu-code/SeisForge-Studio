//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestTraceScientificLateCompletionKeepsLatestWholeSnapshotAndExport(t *testing.T) {
	dir := t.TempDir()
	a, oldB, newB := filepath.Join(dir, "a.sgy"), filepath.Join(dir, "old-b.sgy"), filepath.Join(dir, "new-b.sgy")
	writeTraceScientificFixture(t, a, 1)
	writeTraceScientificFixture(t, oldB, .5)
	writeTraceScientificFixture(t, newB, -1)
	const panel, window = 0x40101, 0x40102 // test identities, not real HWNDs
	oldPanel, oldA := compareHwnd, compareAPath
	oldWindow, oldUI, oldScope, oldResult := traceAnalysisHwnd, traceAnalysisUI, traceAnalysisWindow, traceAnalysisCurrent
	oldGen, oldManual := atomic.LoadInt64(&traceAnalysisGeneration), traceAnalysisManualGeneration
	oldPending, oldVersion, oldCancel, oldReady := traceAnalysisPending, traceAnalysisRequestedVersion, traceAnalysisCancel, traceAnalysisReady
	oldStyle, oldScene := traceScientificStyle, traceScientificActiveScene
	t.Cleanup(func() {
		compareHwnd, compareAPath = oldPanel, oldA
		traceAnalysisHwnd, traceAnalysisUI, traceAnalysisWindow, traceAnalysisCurrent = oldWindow, oldUI, oldScope, oldResult
		traceAnalysisManualGeneration, traceAnalysisPending, traceAnalysisRequestedVersion, traceAnalysisCancel, traceAnalysisReady = oldManual, oldPending, oldVersion, oldCancel, oldReady
		traceScientificStyle, traceScientificActiveScene = oldStyle, oldScene
		atomic.StoreInt64(&traceAnalysisGeneration, oldGen)
	})
	compareHwnd, compareAPath = panel, a
	traceAnalysisHwnd, traceAnalysisUI, traceAnalysisWindow = window, traceAnalysisControls{}, traceAnalysisScope{}
	traceScientificStyle.paper = true // fixed valid export geometry without a real HWND
	traceScientificStyle.visible = [3]bool{true, true, true}
	traceAnalysisReady = make(map[int64]*traceAnalysisResult)
	traceAnalysisCancel = nil
	build := func(generation int64, trace int64, b, label string, scope traceAnalysisScope) *traceAnalysisResult {
		s := traceAnalysisSelection{Anchor: tracePoststackAnchor{Owner: panel, Trace: trace}, Context: label, CompareGroup: true, Difference: true,
			Targets: []traceAnalysisTarget{{Role: "A", Path: a, Trace: trace, SampleEnd: 350}, {Role: "B", Path: b, Trace: trace, SampleEnd: 350}}}
		r := buildTraceAnalysisResult(generation, freezeTraceAnalysisInScope(s, scope), false)
		r.owner, r.version = window, currentTracePoststackVersion()
		return r
	}
	full, manual := traceAnalysisScope{}, traceAnalysisScope{traceAnalysisManual, 900_000, 1_200_000}
	previous := build(40, 0, oldB, "Previous physical trace", full)
	r1 := build(41, 0, oldB, "Stale physical trace", full)
	r2 := build(42, 1, newB, "Latest physical trace", manual)
	// Simulate replacement B while R1 is pending, then deliver actual mailbox
	// messages in reverse order. The test exercises the real UI commit handler.
	r1.version.B++
	traceAnalysisCurrent, traceAnalysisRequestedVersion = previous, r2.version
	traceAnalysisManualGeneration, traceAnalysisPending = 42, true
	atomic.StoreInt64(&traceAnalysisGeneration, 42)
	traceAnalysisReady[42] = r2
	handleTraceAnalysisReady(42)
	if traceAnalysisCurrent != r2 || traceAnalysisPending || traceAnalysisWindow != manual || traceAnalysisManualGeneration != 0 {
		t.Fatal("latest complete applied time window did not commit atomically")
	}
	validate := func() {
		t.Helper()
		r := traceAnalysisCurrent
		d := r.snapshot
		if len(d.Series) != 3 || r.files[0].target.Trace != 1 || r.files[1].target.Path != newB || d.ReferenceSNR.Pairs != 151 || math.Abs(d.ReferenceSNR.DB-(-6.020599913279624)) > 1e-8 {
			t.Fatalf("mixed trace, B, or SNR: %+v", d.ReferenceSNR)
		}
		for _, s := range d.Series {
			if s.StartUS != 900_000 || s.IntervalUS != 2000 || len(s.Samples) != 151 || s.Spectrum.InputSamples != 151 {
				t.Fatalf("mixed %s waveform and FFT window", s.Role)
			}
		}
		if d.Series[2].Samples[0] != 2*d.Series[0].Samples[0] || !strings.Contains(traceScientificIdentityText(r), "Latest physical trace · 实取 0.9–1.2 s") || !strings.Contains(makeTraceStatsTable(r).snr, "-6.02 dB · 151 对有效样点") {
			t.Fatal("latest residual, title or statistics were not committed together")
		}
	}
	captureSVG := func() []byte {
		t.Helper()
		scene, _, err := traceScientificSceneForExport()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err = scene.svg(&buf); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	validate()
	wantSVG := captureSVG()
	traceAnalysisReady[41] = r1
	handleTraceAnalysisReady(41)
	validate()
	if got := captureSVG(); !bytes.Equal(got, wantSVG) || !bytes.Contains(got, []byte("Latest physical trace")) {
		t.Fatal("late R1 changed the real export scene or source identity")
	}
	ctx, cancel := context.WithCancel(context.Background())
	traceAnalysisCancel = cancel
	traceAnalysisPending = true
	traceAnalysisReady[43] = build(43, 0, oldB, "Never publish after close", full)
	atomic.StoreInt64(&traceAnalysisGeneration, 43)
	traceAnalysisWndProc(window, WM_DESTROY, 0, 0)
	handleTraceAnalysisReady(43)
	if ctx.Err() == nil || traceAnalysisHwnd != 0 || traceAnalysisCurrent != nil || traceAnalysisPending || len(traceAnalysisReady) != 0 {
		t.Fatal("closing the owner failed to cancel, clear or reject its late analysis")
	}
	t.Log("R2 retained physical trace 2, new B, 900–1200 ms, 151 samples/FFT, −6.0206 dB and identical SVG after late R1; WM_DESTROY canceled and drained the owner")
}

func writeTraceScientificFixture(t *testing.T, path string, factor float64) {
	writeTraceScientificFixtureAt(t, path, factor, 800)
}

func writeTraceScientificFixtureAt(t *testing.T, path string, factor float64, startMS uint16) {
	t.Helper()
	const nr, nc, ns = 2, 8, 351
	if err := testsegy.Write(path, testsegy.Options{Rows: nr, Cols: nc, Samples: ns, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nr*nc; i++ {
		base := 3600 + i*(240+ns*4)
		binary.BigEndian.PutUint16(data[base+108:base+110], startMS)
		binary.BigEndian.PutUint16(data[base+114:base+116], ns)
		binary.BigEndian.PutUint16(data[base+116:base+118], 2000)
		for j := 0; j < ns; j++ {
			v := factor * (math.Sin(2*math.Pi*25*.002*float64(j)+.18*float64(i)) + .27*math.Sin(2*math.Pi*70*.002*float64(j)))
			binary.BigEndian.PutUint32(data[base+240+4*j:], math.Float32bits(float32(v)))
		}
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCompareTracePickPreservesClickedPanelPhysicalTime(t *testing.T) {
	dir := t.TempDir()
	aPath, bPath := filepath.Join(dir, "a.sgy"), filepath.Join(dir, "b.sgy")
	writeTraceScientificFixtureAt(t, aPath, 1, 800)
	writeTraceScientificFixtureAt(t, bPath, .5, 850)
	a, err := segy.Open(aPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := segy.Open(bPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	oldTD, oldA, oldB := compareTD, compareAPath, compareBPath
	oldOwner, oldActive, oldBVisible := compareHwnd, compareWorkspaceActive, compareBVisible
	oldPanels := [2]comparePanel{compareA, compareB}
	oldMode, oldLine, oldMin, oldMax := compareMode, compareLineCoord, compareLineXMin, compareLineXMax
	oldFirst, oldLast, oldStep := compareTraceStart, compareTraceEnd, compareTraceStep
	oldSampleFirst, oldSampleLast := compareSampleStart, compareSampleEnd
	t.Cleanup(func() {
		compareTD, compareAPath, compareBPath = oldTD, oldA, oldB
		compareHwnd, compareWorkspaceActive, compareBVisible = oldOwner, oldActive, oldBVisible
		compareA, compareB = oldPanels[0], oldPanels[1]
		compareMode, compareLineCoord, compareLineXMin, compareLineXMax = oldMode, oldLine, oldMin, oldMax
		compareTraceStart, compareTraceEnd, compareTraceStep = oldFirst, oldLast, oldStep
		compareSampleStart, compareSampleEnd = oldSampleFirst, oldSampleLast
	})
	g := compareTraceSelectionTestGeometry([]int64{0, 1, 2, 3, 4, 5})
	compareTD = &compareTimeData{fa: a, fb: b, ga: g, gb: g}
	compareHwnd, compareAPath, compareBPath = 0x40101, aPath, bPath
	compareWorkspaceActive, compareBVisible = true, true
	compareMode, compareLineCoord, compareLineXMin, compareLineXMax = 0, 20, 100, 120
	compareTraceStart, compareTraceEnd, compareTraceStep = 0, 15, 1
	compareSampleStart, compareSampleEnd = 0, 350
	compareA = comparePanel{sampleOriginUS: 800_000, sampleDtUS: 2000}
	compareB = comparePanel{sampleOriginUS: 850_000, sampleDtUS: 2000}

	for _, pick := range []struct {
		panel  int
		wantUS float64
	}{{0, 900_000}, {1, 950_000}} {
		s, err := compareTraceSelectionForWorld(pick.panel, 110, 50)
		if err != nil {
			t.Fatal(err)
		}
		s, ok := resolveTracePoststackAnchor(s.Anchor)
		if !ok || len(s.Targets) != 2 || s.Anchor.Trace != 4 {
			t.Fatalf("wrong physical selection on panel %d: %+v", pick.panel, s)
		}
		r := buildTraceAnalysisResult(1, freezeTraceAnalysisInScope(s, traceAnalysisScope{}), false)
		o := traceScientificOptions(r)
		if len(r.files) != 2 || !o.CursorSet || o.CursorUS != pick.wantUS {
			t.Errorf("panel %d sample 50 mapped to analysis cursor %g µs (set=%v), want %g µs", pick.panel, o.CursorUS, o.CursorSet, pick.wantUS)
		}
	}
}

func TestTraceScientificFixtureAndWindowIdentity(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "Synthetic_A.sgy"), filepath.Join(dir, "Synthetic_B_negative.sgy")
	writeTraceScientificFixture(t, a, 1)
	writeTraceScientificFixture(t, b, -1)
	if out := os.Getenv("SEISFORGE_TRACE_GUI_FIXTURES"); out != "" {
		if err := os.MkdirAll(out, 0700); err != nil {
			t.Fatal(err)
		}
		writeTraceScientificFixture(t, filepath.Join(out, filepath.Base(a)), 1)
		writeTraceScientificFixture(t, filepath.Join(out, filepath.Base(b)), -1)
	}
	anchor := tracePoststackAnchor{Owner: 12}
	s := traceAnalysisSelection{Anchor: anchor, CompareGroup: true, Difference: true, Context: "IL 1000 / XL 2000", Targets: []traceAnalysisTarget{{Role: "A", Path: a, SampleEnd: 350}, {Role: "B", Path: b, SampleEnd: 350}}}
	version := tracePoststackVersion{Owner: 12, A: 1, B: 2, SNRFormula: segyanalysis.ReferenceSNRFormulaVersion}
	result := buildTraceAnalysisResult(41, s, false)
	result.owner, result.version = 33, version
	if !traceAnalysisResultIsCurrent(result, 33, 41, 41, version) || traceAnalysisResultIsCurrent(result, 34, 41, 41, version) || traceAnalysisResultIsCurrent(result, 0, 41, 41, version) || traceAnalysisResultIsCurrent(result, 33, 41, 42, version) {
		t.Fatal("owner, version or completion order not enforced")
	}
	result.snapshot.ReferenceSNR.FormulaVersion = 1
	if traceAnalysisResultIsCurrent(result, 33, 41, 41, version) {
		t.Fatal("snapshot from the old SNR formula was accepted")
	}
	result.snapshot.ReferenceSNR.FormulaVersion = segyanalysis.ReferenceSNRFormulaVersion
	version.B++
	if traceAnalysisResultIsCurrent(result, 33, 41, 41, version) {
		t.Fatal("old B was accepted")
	}
	d := result.snapshot
	if len(d.Series) != 3 || d.Series[0].StartUS != 800000 || d.Series[0].Spectrum.InputSamples != 351 || !strings.Contains(traceScientificIdentityText(result), "0.8–1.5 s") {
		t.Fatalf("actual scope: %s", traceScientificIdentityText(result))
	}
	peak := d.Series[2].Spectrum.DB[d.Series[2].Spectrum.PeakBin]
	if math.Abs(peak-20*math.Log10(2)) > 1e-6 || d.DBRange().Max <= peak {
		t.Fatalf("positive residual peak %g outside %+v", peak, d.DBRange())
	}
	s.Targets[1].PhysicalWindow, s.Targets[1].FirstUS, s.Targets[1].LastUS = true, 1600000, 1700000
	result = buildTraceAnalysisResult(42, s, false)
	if len(result.snapshot.Series) != 1 || !strings.Contains(result.files[1].waveError, "没有重叠") {
		t.Fatalf("missing B time became data: %+v", result.files[1])
	}
}

func TestTraceScientificFrozenFilteredRowAndDisplayEdits(t *testing.T) {
	data := []float64{1, 2, 3}
	row := &viewFilterRows{values: map[int64][]float64{2: data}, first: 7, last: 9}
	s := traceAnalysisSelection{Targets: []traceAnalysisTarget{{Role: "A", Trace: 2, SampleStart: 7, SampleEnd: 9, Filtered: row, IsFiltered: true, Bandpass: poststack.ViewBandpass{}}}}
	frozen := freezeTraceAnalysisSelection(s, false)
	data[0] = 999
	if frozen.Targets[0].Filtered != nil || !reflect.DeepEqual(frozen.Targets[0].FrozenSamples, []float64{1, 2, 3}) {
		t.Fatalf("worker still depends on view row: %+v", frozen.Targets[0])
	}
	prior := segyanalysis.DisplayRange{Min: -.80000000000000004, Max: 1.500000318211}
	fields := [2]string{"-0.8", "1.5"}
	if r, e := traceScientificReadAmp(fields[0], fields[1], fields, prior, false); e != nil || r != prior {
		t.Fatalf("round-trip changed exact values: %+v (%v)", r, e)
	}
	if r, e := traceScientificReadAmp("-", fields[1], fields, prior, true); e == nil || r != prior {
		t.Fatalf("incomplete text changed effective range: %+v (%v)", r, e)
	}
	d := segyanalysis.BuildTraceSnapshot([]segyanalysis.TraceInput{{Role: "A", StartUS: 800000, IntervalUS: 2000, Samples: []float64{1, 2, 3}}}, true)
	r := &traceAnalysisResult{snapshot: &d}
	before := traceScientificPlotData(r, traceScientificPlotOptions{Data: &d})
	after := traceScientificPlotData(r, traceScientificPlotOptions{Data: &d, VisibleRoles: []string{}, WaveGain: 2, FrequencySet: true, FrequencyMinHz: 10, FrequencyMaxHz: 100, TimeInMS: true})
	if before.reference != after.reference || before.amplitude != after.amplitude || d.Series[0].Samples[0] != 1 {
		t.Fatal("display edits changed analysis")
	}
}

func TestTraceScientificInvalidatedGeometryKeepsNoOldRows(t *testing.T) {
	prevHWND, prevPath, prevData := compareHwnd, compareAPath, compareTD
	t.Cleanup(func() { compareHwnd, compareAPath, compareTD = prevHWND, prevPath, prevData })
	compareHwnd, compareAPath, compareTD = 47, "Synthetic_A.sgy", nil
	anchor := tracePoststackAnchor{Owner: 47, SourceA: compareAPath, Role: "A", Trace: 11, Geometry: true, Inline: 1001, Crossline: 2003}
	selection, ok := resolveTracePoststackAnchor(anchor)
	if !ok || len(selection.Targets) != 0 || !strings.Contains(selection.Context, "IL 1001 / XL 2003") || selection.PairNote == "" {
		t.Fatalf("an invalidated exact pick did not create an unavailable snapshot: %+v", selection)
	}
	result := buildTraceAnalysisResult(1, selection, false)
	if result.snapshot == nil || len(result.snapshot.Series) != 0 || len(result.files) != 0 || result.diffNote != selection.PairNote {
		t.Fatalf("unavailable result reuses data or lacks a reason: %+v", result)
	}
}
