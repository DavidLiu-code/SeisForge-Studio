//go:build windows

package main

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

func TestTraceScientificAbsoluteSecondsPreserveMicroseconds(t *testing.T) {
	for _, us := range []int64{math.MinInt64, -1_300_017, -1, 0, 800_001, 1_500_000, math.MaxInt64} {
		x, err := parseTraceAnalysisSeconds(formatTraceAnalysisSeconds(us))
		if err != nil || x != us {
			t.Fatalf("%d became %d: %v", us, x, err)
		}
	}
	for _, input := range []string{"", "-", "0.8000001", "NaN", "9223372036854.775808"} {
		if _, err := parseTraceAnalysisSeconds(input); err == nil {
			t.Fatalf("accepted invalid seconds %q", input)
		}
	}
	if i, j, ok := traceAnalysisSampleIndices(-12000, -10001, -6001, 2000, 10); !ok || i != 1 || j != 2 {
		t.Fatalf("lost signed/time lattice semantics: %d %d %v", i, j, ok)
	}
	if _, _, ok := traceAnalysisSampleIndices(math.MinInt64, math.MaxInt64-1, math.MaxInt64, 2000, 10); ok {
		t.Fatal("remote range wrapped into trace samples")
	}
}

func TestTraceScientificManualScopeCommitsOnlyUsableResult(t *testing.T) {
	a := filepath.Join(t.TempDir(), "trace.sgy")
	writeTraceScientificFixture(t, a, 1)
	selection := traceAnalysisSelection{Targets: []traceAnalysisTarget{{Role: "A", Path: a, Trace: 0, SampleEnd: 350}}}
	initial := buildTraceAnalysisResult(1, freezeTraceAnalysisInScope(selection, traceAnalysisScope{}), false)
	if lo, hi, ok := traceAnalysisActualWindow(initial); !ok || lo != 800_000 || hi != 1_500_000 {
		t.Fatalf("fixture original window = %d..%d (valid %t)", lo, hi, ok)
	}
	oldH, oldUI, oldWindow, oldResult := traceAnalysisHwnd, traceAnalysisUI, traceAnalysisWindow, traceAnalysisCurrent
	oldGen, oldManual := atomic.LoadInt64(&traceAnalysisGeneration), traceAnalysisManualGeneration
	oldPending, oldVersion, oldCancel := traceAnalysisPending, traceAnalysisRequestedVersion, traceAnalysisCancel
	oldReady := traceAnalysisReady
	oldStyle := traceScientificStyle
	t.Cleanup(func() {
		traceAnalysisHwnd, traceAnalysisUI, traceAnalysisWindow, traceAnalysisCurrent = oldH, oldUI, oldWindow, oldResult
		traceAnalysisManualGeneration, traceAnalysisPending, traceAnalysisRequestedVersion, traceAnalysisCancel = oldManual, oldPending, oldVersion, oldCancel
		traceAnalysisReady = oldReady
		traceScientificStyle = oldStyle
		atomic.StoreInt64(&traceAnalysisGeneration, oldGen)
	})
	// The completion path checks owner identity; no desktop window is needed.
	traceAnalysisHwnd, traceAnalysisUI, traceAnalysisWindow, traceAnalysisCurrent = 1, traceAnalysisControls{}, traceAnalysisScope{}, initial
	traceAnalysisRequestedVersion = tracePoststackVersion{SNRFormula: segyanalysis.ReferenceSNRFormulaVersion}
	traceAnalysisReady = make(map[int64]*traceAnalysisResult)
	traceAnalysisCancel = nil
	stage := func(generation int64, src traceAnalysisSelection, draft traceAnalysisScope) {
		r := buildTraceAnalysisResult(generation, freezeTraceAnalysisInScope(src, draft), false)
		r.owner, r.version = traceAnalysisHwnd, traceAnalysisRequestedVersion
		traceAnalysisReady[generation] = r
		traceAnalysisManualGeneration, traceAnalysisPending = generation, true
		atomic.StoreInt64(&traceAnalysisGeneration, generation)
	}
	outside := traceAnalysisScope{traceAnalysisManual, 2_000_000, 3_000_000}
	bad := traceAnalysisSelection{Targets: []traceAnalysisTarget{{Role: "A", Path: a + ".missing"}}}
	stage(7, bad, outside)
	handleTraceAnalysisReady(7)
	if traceAnalysisCurrent != initial || traceAnalysisWindow.mode != traceAnalysisFollow || traceAnalysisPending {
		t.Fatal("read failure replaced the committed analysis")
	}
	valid := traceAnalysisScope{traceAnalysisManual, 900_000, 1_200_000}
	stage(8, selection, valid)
	traceAnalysisAbortManualRequest()
	handleTraceAnalysisReady(8)
	if traceAnalysisCurrent != initial || traceAnalysisWindow.mode != traceAnalysisFollow || traceAnalysisPending {
		t.Fatal("cancelled manual draft replaced the committed analysis")
	}
	traceScientificStyle.options.TimeSet, traceScientificStyle.options.AmpSet = true, true
	traceScientificStyle.zoom.waveY = displaycore.Range{Min: .9, Max: 1}
	traceScientificStyle.zoom.specX = displaycore.Range{Min: 1, Max: 10}
	stage(10, selection, valid)
	handleTraceAnalysisReady(10)
	lo, hi, ok := traceAnalysisActualWindow(traceAnalysisCurrent)
	if traceAnalysisWindow != valid || !ok || lo != 900_000 || hi != 1_200_000 || len(traceAnalysisCurrent.snapshot.Series[0].Samples) != 151 || traceAnalysisPending {
		t.Fatalf("0.9–1.2 s result: committed=%+v actual=%d..%d", traceAnalysisWindow, lo, hi)
	}
	if traceScientificStyle.options.TimeSet || traceScientificStyle.zoom.waveY.Valid() ||
		!traceScientificStyle.options.AmpSet || !traceScientificStyle.zoom.specX.Valid() {
		t.Fatal("committing the requested time window did not reset only the previous time viewport")
	}
	stage(11, selection, outside)
	handleTraceAnalysisReady(11)
	if traceAnalysisWindow != outside || !traceAnalysisCurrent.manualWindowHasNoSamples() || traceAnalysisPending {
		t.Fatal("valid empty interval did not replace the old view with a new empty result")
	}
	opt := traceScientificOptions(traceAnalysisCurrent)
	scene := traceScientificPlotScene(1000, 540, traceAnalysisCurrent, opt)
	y := scene.effectiveAxes[0].y.View
	if y.Min != 2 || y.Max != 3 || len(scene.axisHits) != 2 || scene.WaveAvailable || scene.SpectrumAvailable {
		t.Fatalf("empty interval does not show an empty 2–3 s axis: view=%+v axes=%d", y, len(scene.axisHits))
	}
	for _, path := range scene.paths {
		if path.clip != nil {
			t.Fatal("empty interval fabricated a data path")
		}
	}
}

func TestTraceScientificManualWindowShowsBlankOutsideRealSamples(t *testing.T) {
	a := filepath.Join(t.TempDir(), "trace.sgy")
	writeTraceScientificFixture(t, a, 1)
	selection := traceAnalysisSelection{Targets: []traceAnalysisTarget{{Role: "A", Path: a, Trace: 0, SampleEnd: 350, MarkerSample: -1}}}
	oldStyle := traceScientificStyle
	t.Cleanup(func() { traceScientificStyle = oldStyle })
	traceScientificStyle.options = traceScientificPlotOptions{TimeInMS: true}
	traceScientificStyle.zoom = traceScientificViewport{}
	traceScientificStyle.visible = [3]bool{true}
	baseline := buildTraceAnalysisResult(1, freezeTraceAnalysisInScope(selection, traceAnalysisScope{mode: traceAnalysisFull}), true)
	scope := traceAnalysisScope{traceAnalysisManual, 600_000, 1_700_000}
	result := buildTraceAnalysisResult(2, freezeTraceAnalysisInScope(selection, scope), false)
	if v := result.snapshot.Series[0]; len(v.Samples) != 351 || v.StartUS != 800_000 || v.IntervalUS != 2000 ||
		!reflect.DeepEqual(v, baseline.snapshot.Series[0]) {
		t.Fatal("extending the window changed real samples, statistics or FFT")
	}
	opt := traceScientificOptions(result)
	scene := traceScientificPlotScene(1250, 620, result, opt)
	axis := scene.effectiveAxes[0].y
	if axis.View.Min != .6 || axis.View.Max != 1.7 || axis.Data.Min != .8 || axis.Data.Max != 1.5 || len(scene.WaveBounds) != 1 {
		t.Fatalf("requested and actual ranges not kept separate: axis=%+v", axis)
	}
	wave := scene.WaveBounds[0]
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, path := range scene.paths {
		if path.clip == nil || *path.clip != wave {
			continue
		}
		for _, run := range path.runs {
			for _, pt := range run {
				lo, hi = min(lo, pt.y), max(hi, pt.y)
			}
		}
	}
	if wantLo, wantHi := wave.t+wave.height()*(.8-.6)/(1.7-.6), wave.t+wave.height()*(1.5-.6)/(1.7-.6); math.Abs(lo-wantLo) > 1e-6 || math.Abs(hi-wantHi) > 1e-6 {
		t.Fatalf("waveform covers %.4f–%.4f px, expected %.4f–%.4f; no wave should enter unsampled intervals", lo, hi, wantLo, wantHi)
	}
}

func TestTraceScientificManualAnalysisUsesRealFilteredOverlap(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.sgy"), filepath.Join(dir, "b.sgy")
	writeTraceScientificFixture(t, a, 1)
	writeTraceScientificFixture(t, b, 1)
	contents, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint16(contents[3600+108:3600+110], 1200)
	if err = os.WriteFile(b, contents, 0600); err != nil {
		t.Fatal(err)
	}
	legacy := traceAnalysisSelection{Anchor: tracePoststackAnchor{Owner: 12}, CompareGroup: true, Difference: true, Context: "IL 1000 / XL 2000", Targets: []traceAnalysisTarget{
		{Role: "A", Path: a, Trace: 0, SampleStart: 3, SampleEnd: 7, WindowUnavailable: true, IsFiltered: true, Bandpass: poststack.ViewBandpass{LowHz: 10, HighHz: 90}, FrozenSamples: []float64{999, 999, 999}},
		{Role: "B", Path: b, Trace: 0, SampleStart: 5, SampleEnd: 6},
	}}
	scope := traceAnalysisScope{mode: traceAnalysisManual, firstUS: 900001, lastUS: 1401999}
	r := buildTraceAnalysisResult(7, freezeTraceAnalysisInScope(legacy, scope), false)
	if len(r.snapshot.Series) != 3 || r.snapshot.ComparisonOverlapSamples != 101 || r.snapshot.ComparisonStartUS != 1_200_000 || r.snapshot.Series[0].StartUS != 902_000 || len(r.snapshot.Series[0].Samples) != 250 {
		t.Fatalf("manual filtered windows did not follow each real trace and overlap: %+v %v", r.selection.Window, r.diffNote)
	}
	if !strings.Contains(traceScientificIdentityText(r), "实取 0.902–1.4 s · 手动 0.900001–1.401999 s") {
		t.Fatalf("requested and actual times were conflated: %s", traceScientificIdentityText(r))
	}
	source, err := openPoststackCompareSource(a)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	full, err := source.ReadTraceWindow(0, 0, 350)
	if err != nil {
		t.Fatal(err)
	}
	want, err := poststack.FilterTraceWindow(context.Background(), full, 2000, legacy.Targets[0].Bandpass, 51, 300)
	if err != nil || !reflect.DeepEqual(want, r.snapshot.Series[0].Samples) {
		t.Fatal("manual trace was resampled or did not preserve full genuine filter context")
	}
	if !legacy.Targets[0].WindowUnavailable || legacy.Targets[0].FrozenSamples[0] != 999 {
		t.Fatal("freezing committed manual time altered prior scope")
	}
	outside := traceAnalysisScope{traceAnalysisManual, 20_000_000, 20_500_000}
	if x := buildTraceAnalysisResult(8, freezeTraceAnalysisInScope(legacy, outside), false); len(x.snapshot.Series) != 0 || !strings.Contains(x.files[0].waveError, "没有真实样点") {
		t.Fatal("out-of-coverage request fabricated samples")
	}
}
