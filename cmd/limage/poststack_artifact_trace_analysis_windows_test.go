//go:build windows

package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func TestArtifactTraceAnalysisRetainsOriginalSampleAxisAndZeroCopyDifference(t *testing.T) {
	r := pluginArtifactResult(t) // B rows correspond to A physical 2,0; samples 1..4.
	generatedPoststackMu.Lock()
	oldResults := generatedPoststackResults
	generatedPoststackResults = []*generatedPoststackResult{r}
	generatedPoststackMu.Unlock()
	oldFirst, oldLast, oldMode := compareSampleStart, compareSampleEnd, compareMode
	t.Cleanup(func() {
		generatedPoststackMu.Lock()
		generatedPoststackResults = oldResults
		generatedPoststackMu.Unlock()
		compareSampleStart, compareSampleEnd, compareMode = oldFirst, oldLast, oldMode
	})
	a, err := segy.Open(r.Section.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := openPoststackCompareSource(r.Section.Part2)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	compareSampleStart, compareSampleEnd, compareMode = 1, 4, 0
	ta, err := compareAnalysisTarget("A", a, 0, 3, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	tb, err := compareAnalysisTarget("B", b, 1, 3, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ta.SourceSampleOffset != 0 || tb.SourceSampleOffset != 1 || tb.SampleStart != 0 || tb.SampleEnd != 3 || tb.MarkerSample != 2 {
		t.Fatalf("target must retain local read coordinates plus immutable source offset: A=%+v B=%+v", ta, tb)
	}
	selection := traceAnalysisSelection{Difference: true, Targets: []traceAnalysisTarget{ta, tb}}
	// A subsequent UI change must not alter the worker's captured sample axis.
	compareSampleStart, compareSampleEnd, compareMode = 5, 7, 1
	result := buildTraceAnalysisResult(3, selection, false)
	if len(result.series) != 3 || result.diffNote != "" {
		t.Fatalf("cropped Copy B was falsely rejected: series=%d reason=%s", len(result.series), result.diffNote)
	}
	for _, file := range result.files {
		if file.waveError != "" || file.sampleStart != 1 || file.sampleEnd != 4 || len(file.samples) != 4 {
			t.Fatalf("local B index leaked into A-relative analysis: %+v", file)
		}
	}
	for _, series := range result.series {
		if series.sampleStart != 1 || series.markerSample != 3 {
			t.Fatalf("waveform/marker time axis shifted: %+v", series)
		}
	}
	if !reflect.DeepEqual(result.series[0].samples, result.series[1].samples) || result.series[2].stats.PeakAbs != 0 {
		t.Fatal("Copy waveform or difference was misaligned")
	}
	dt := result.series[0].dtUS
	segmentsA := buildTraceWaveformSegments(result.series[0], 0, float64(7*dt), 80)
	segmentsB := buildTraceWaveformSegments(result.series[1], 0, float64(7*dt), 80)
	if !reflect.DeepEqual(segmentsA, segmentsB) {
		t.Fatal("Copy A/B draw at different time coordinates")
	}
	fullB := buildTraceAnalysisResult(4, traceAnalysisSelection{Targets: []traceAnalysisTarget{tb}}, true)
	if len(fullB.series) != 1 || fullB.files[0].sampleStart != 1 || fullB.files[0].sampleEnd != 4 || fullB.series[0].markerSample != 3 {
		t.Fatal("full B view must mean all available artifact samples on A's axis")
	}
	fullAB := buildTraceAnalysisResult(5, selection, true)
	if len(fullAB.series) != 2 || !strings.Contains(fullAB.diffNote, "样点窗不一致") {
		t.Fatal("full A cannot be subtracted from a cropped full B")
	}
}

func TestArtifactTraceAnalysisSubwindowAndMissingOffsetAreNotSilentlyAligned(t *testing.T) {
	r := pluginArtifactResult(t)
	generatedPoststackMu.Lock()
	old := generatedPoststackResults
	generatedPoststackResults = []*generatedPoststackResult{r}
	generatedPoststackMu.Unlock()
	t.Cleanup(func() {
		generatedPoststackMu.Lock()
		generatedPoststackResults = old
		generatedPoststackMu.Unlock()
	})
	a := traceAnalysisTarget{Role: "A", Path: r.Section.SourcePath, Trace: 2, SampleStart: 2, SampleEnd: 3, MarkerSample: 3}
	b := traceAnalysisTarget{Role: "B", Path: r.Section.Part2, Trace: 0, SampleStart: 1, SampleEnd: 2, MarkerSample: 2, SourceSampleOffset: 1}
	selection := traceAnalysisSelection{Difference: true, Targets: []traceAnalysisTarget{a, b}}
	result := buildTraceAnalysisResult(1, selection, false)
	if result.diffNote != "" || len(result.series) != 3 || result.files[1].sampleStart != 2 || result.files[1].sampleEnd != 3 || result.series[2].stats.PeakAbs != 0 {
		t.Fatalf("artifact subwindow failed: %s", result.diffNote)
	}
	selection.Targets[1].SourceSampleOffset = 0
	result = buildTraceAnalysisResult(2, selection, false)
	if len(result.series) != 2 || !strings.Contains(result.diffNote, "样点窗不一致") {
		t.Fatal("different A-relative windows were subtracted")
	}
	selection.Targets[1].SourceSampleOffset = -1
	result = buildTraceAnalysisResult(3, selection, false)
	if result.files[1].waveError == "" || len(result.files[1].samples) != 0 {
		t.Fatal("negative source offset accepted")
	}
}
