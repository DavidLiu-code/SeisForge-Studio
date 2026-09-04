//go:build windows

package main

import (
	"encoding/binary"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func TestBuildTraceWaveformSegmentsConnectsZoomedSamples(t *testing.T) {
	curve := traceAnalysisSeries{
		sampleStart: 100,
		dtUS:        2000,
		samples:     []float64{-2, -1, 0.5, 3, 1},
	}
	segments := buildTraceWaveformSegments(curve, 200000, 208000, 41)
	connectors, envelopes := 0, 0
	for _, segment := range segments {
		if segment.envelope {
			envelopes++
			continue
		}
		connectors++
		if segment.row0 == segment.row1 {
			t.Fatalf("zoomed connector collapsed to a disconnected row: %+v", segment)
		}
	}
	if connectors != len(curve.samples)-1 || envelopes != len(curve.samples) {
		t.Fatalf("zoomed waveform lost continuity: connectors=%d envelopes=%d", connectors, envelopes)
	}
}

func TestBuildTraceWaveformSegmentsPreservesDenseEnvelope(t *testing.T) {
	curve := traceAnalysisSeries{dtUS: 1000, samples: make([]float64, 101)}
	for index := range curve.samples {
		curve.samples[index] = math.Sin(float64(index) * 0.2)
	}
	curve.samples[49] = -73
	curve.samples[50] = 91
	segments := buildTraceWaveformSegments(curve, 0, 100000, 5)
	foundMin, foundMax := false, false
	for _, segment := range segments {
		if !segment.envelope {
			continue
		}
		foundMin = foundMin || segment.value0 == -73
		foundMax = foundMax || segment.value1 == 91
	}
	if !foundMin || !foundMax {
		t.Fatalf("per-pixel envelope lost a sub-pixel spike: min=%v max=%v", foundMin, foundMax)
	}
}

func TestBuildTraceWaveformSegmentsDoesNotBridgeNonFiniteGap(t *testing.T) {
	curve := traceAnalysisSeries{
		dtUS:    1000,
		samples: []float64{0, 1, math.NaN(), 2, 3},
	}
	segments := buildTraceWaveformSegments(curve, 0, 4000, 41)
	connectors := make([]traceWaveformSegment, 0, 2)
	for _, segment := range segments {
		if !segment.envelope {
			connectors = append(connectors, segment)
		}
	}
	if len(connectors) != 2 {
		t.Fatalf("non-finite gap was bridged: connectors=%+v", connectors)
	}
	if connectors[0].value0 != 0 || connectors[0].value1 != 1 || connectors[1].value0 != 2 || connectors[1].value1 != 3 {
		t.Fatalf("unexpected finite-run connectors: %+v", connectors)
	}
}

func TestBuildTraceWaveformSegmentsKeepsSingleZeroVisible(t *testing.T) {
	curve := traceAnalysisSeries{sampleStart: 8, dtUS: 2000, samples: []float64{0}}
	segments := buildTraceWaveformSegments(curve, 15000, 17000, 20)
	if len(segments) != 1 || !segments[0].envelope || segments[0].value0 != 0 || segments[0].value1 != 0 {
		t.Fatalf("single zero sample must remain drawable: %+v", segments)
	}
	if got := buildTraceWaveformSegments(curve, 17000, 15000, 20); got != nil {
		t.Fatalf("invalid time window should not produce segments: %+v", got)
	}
}

func TestBuildTraceAnalysisResultReadsOnlySelectedWindowAndDifference(t *testing.T) {
	path := writePhase9PseudoFixture(t)
	selection := traceAnalysisSelection{Difference: true, Context: "test", Targets: []traceAnalysisTarget{
		{Role: "A", Path: path, Trace: 3, SampleStart: 5, SampleEnd: 36, MarkerSample: 12},
		{Role: "B", Path: path, Trace: 3, SampleStart: 5, SampleEnd: 36, MarkerSample: 12},
	}}
	result := buildTraceAnalysisResult(7, selection, false)
	if result.generation != 7 || len(result.files) != 2 || len(result.series) != 3 || result.diffNote != "" {
		t.Fatalf("unexpected analysis result: files=%d series=%d note=%q", len(result.files), len(result.series), result.diffNote)
	}
	for _, file := range result.files {
		if file.sampleStart != 5 || file.sampleEnd != 36 || len(file.samples) != 32 || file.stats.SampleCount != 32 || file.spectrum.NFFT != 32 {
			t.Fatalf("selected sample window changed: start=%d end=%d samples=%d stats=%d nfft=%d", file.sampleStart, file.sampleEnd, len(file.samples), file.stats.SampleCount, file.spectrum.NFFT)
		}
		if len(file.binaryHeader.Raw) != 400 || len(file.traceHeader.Raw) != 240 || len(file.textHeaders) != 1 {
			t.Fatalf("header payload missing: binary=%d trace=%d text=%d", len(file.binaryHeader.Raw), len(file.traceHeader.Raw), len(file.textHeaders))
		}
	}
	difference := result.series[2]
	if difference.role != "A-B" || difference.stats.PeakAbs != 0 || difference.stats.ZeroCount != len(difference.samples) {
		t.Fatalf("identical A/B traces did not produce an exact zero difference: %+v", difference.stats)
	}

	full := buildTraceAnalysisResult(8, traceAnalysisSelection{Targets: selection.Targets[:1]}, true)
	if len(full.files) != 1 || full.files[0].sampleStart != 0 || full.files[0].sampleEnd != 47 || len(full.files[0].samples) != 48 {
		t.Fatalf("full-trace selection did not use all samples: %+v", full.files)
	}
}

func TestBuildTraceAnalysisResultKeepsABWhenDifferenceIsIncompatible(t *testing.T) {
	pathA := writePhase9PseudoFixture(t)
	pathB := writePhase9PseudoFixture(t)
	file, err := os.OpenFile(pathB, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var interval [2]byte
	binary.BigEndian.PutUint16(interval[:], 4000)
	if _, err = file.WriteAt(interval[:], 3216); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	result := buildTraceAnalysisResult(1, traceAnalysisSelection{Difference: true, Targets: []traceAnalysisTarget{
		{Role: "A", Path: pathA, Trace: 0, SampleStart: 0, SampleEnd: 47},
		{Role: "B", Path: pathB, Trace: 0, SampleStart: 0, SampleEnd: 47},
	}}, false)
	if len(result.series) != 2 || !strings.Contains(result.diffNote, "采样间隔不一致") {
		t.Fatalf("incompatible A/B should keep both source curves without A-B: series=%d note=%q", len(result.series), result.diffNote)
	}
}

func TestTraceAnalysisFormat4IsHeaderOnly(t *testing.T) {
	path := writePhase9PseudoFixture(t)
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var format [2]byte
	binary.BigEndian.PutUint16(format[:], 4)
	if _, err = file.WriteAt(format[:], 3224); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	result := buildTraceAnalysisResult(1, traceAnalysisSelection{Targets: []traceAnalysisTarget{{Role: "A", Path: path, Trace: 0, SampleStart: 0, SampleEnd: 47}}}, false)
	if len(result.files) != 1 || len(result.files[0].binaryHeader.Raw) != 400 || len(result.files[0].traceHeader.Raw) != 240 {
		t.Fatal("format 4 did not retain read-only header access")
	}
	if len(result.files[0].samples) != 0 || !strings.Contains(result.files[0].waveError, "格式码 4") {
		t.Fatalf("format 4 was not reported as header-only: samples=%d error=%q", len(result.files[0].samples), result.files[0].waveError)
	}
}

func TestVolumeFullViewSelectionUsesActualGeometryTrace(t *testing.T) {
	path := writePhase9PseudoFixture(t)
	reader, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	oldF, oldG := volumeF, volumeG
	oldMin, oldMax := volumeSampleMin, volumeSampleMax
	oldCompare, oldSide := volumeCompareMode, volumeActiveSide
	defer func() {
		volumeF, volumeG = oldF, oldG
		volumeSampleMin, volumeSampleMax = oldMin, oldMax
		volumeCompareMode, volumeActiveSide = oldCompare, oldSide
	}()
	volumeF = reader
	volumeG = compareTraceSelectionTestGeometry([]int64{0, 1, 2, 3, 4, 5})
	volumeSampleMin, volumeSampleMax = 5, 30
	volumeCompareMode, volumeActiveSide = true, 1
	selection, err := volumeTraceAnalysisSelectionForWorld(0, 19.2, 112.0, 18.4)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Targets) != 1 {
		t.Fatalf("unexpected target count: %+v", selection)
	}
	target := selection.Targets[0]
	if target.Role != "B" || target.Trace != 4 || target.Inline != 20 || target.Crossline != 110 || target.MarkerSample != 18 || target.SampleStart != 5 || target.SampleEnd != 30 {
		t.Fatalf("Full View did not map to the real geometry trace: %+v", target)
	}
}
