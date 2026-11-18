package prestack

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func matchRecord(trace int64, source, receiver int32, cdp int32, offset float64) PrestackTraceRecord {
	return PrestackTraceRecord{TraceNumber: trace, SourceID: source, ReceiverID: receiver, CDP: cdp,
		Offset: offset, HasCDP: cdp != 0, HasOffset: true, HeaderValid: true}
}

func TestMatchRecordsPreservesAOrderAndDoesNotUsePhysicalTraceNumber(t *testing.T) {
	a := []PrestackTraceRecord{matchRecord(100, 1, 2, 10, -20), matchRecord(101, 1, 3, 10, 20)}
	b := []PrestackTraceRecord{matchRecord(7, 1, 3, 10, 20), matchRecord(8, 1, 2, 10, -20)}
	r, err := MatchTraceRecords(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Strategy != MatchKeySourceReceiverID || len(r.Pairs) != 2 {
		t.Fatalf("result=%+v", r)
	}
	if r.Pairs[0].ATrace != 100 || r.Pairs[0].BTrace != 8 || r.Pairs[1].BTrace != 7 {
		t.Fatalf("A order/physical mapping lost: %+v", r.Pairs)
	}
	if len(r.AOnly)+len(r.BOnly)+len(r.AmbiguousA)+len(r.AmbiguousB)+len(r.InvalidA)+len(r.InvalidB) != 0 {
		t.Fatalf("unexpected unmatched records: %+v", r)
	}
}

func TestMatchGatherResultsUsesSelectionOrder(t *testing.T) {
	aIndex := &PrestackIndex{Records: []PrestackTraceRecord{matchRecord(10, 1, 2, 1, 1), matchRecord(11, 1, 3, 1, 2)}}
	bIndex := &PrestackIndex{Records: []PrestackTraceRecord{matchRecord(20, 1, 3, 1, 2), matchRecord(21, 1, 2, 1, 1)}}
	r, err := MatchGatherResults(aIndex, GatherResult{TraceIndices: []int64{1, 0}}, bIndex, GatherResult{TraceIndices: []int64{0, 1}})
	if err != nil || len(r.Pairs) != 2 || r.Pairs[0].ATrace != 11 || r.Pairs[0].BTrace != 20 {
		t.Fatalf("selection order not preserved: %+v err=%v", r, err)
	}
}

func TestMatchRecordsKeepsPhysicalZeroAfterReordering(t *testing.T) {
	a := []PrestackTraceRecord{matchRecord(3, 1, 3, 1, 2), matchRecord(0, 1, 2, 1, 1)}
	b := []PrestackTraceRecord{matchRecord(8, 1, 3, 1, 2), matchRecord(0, 1, 2, 1, 1)}
	result := MatchRecords(a, b)
	if len(result.Pairs) != 2 || result.Pairs[1].ATrace != 0 || result.Pairs[1].BTrace != 0 {
		t.Fatalf("physical trace zero became a selection ordinal: %+v", result)
	}
	rows := FilterCompareRows(result, a, b, DefaultCompareFilterSet())
	if len(rows) != 2 || rows[1].ATrace != 0 || rows[1].BTrace != 0 {
		t.Fatalf("hover/analysis row lost physical trace zero: %+v", rows)
	}
	report := BuildCompareMatchReportWithOptions(result, SampleAxisCompatibility{}, CompareMatchReportOptions{IncludePairs: true, ARecords: a, BRecords: b})
	if report.Pairs[1].ATrace != 0 || report.Pairs[1].BTrace != 0 || report.Pairs[1].AMetadata.TraceNumber != 0 || report.Pairs[1].BMetadata.TraceNumber != 0 {
		t.Fatalf("report lost physical trace zero: %+v", report.Pairs[1])
	}
}

func TestCompareReportPreservesZeroPhysicalTraceJSON(t *testing.T) {
	for _, classification := range []string{"matched", "a_only", "b_only", "ambiguous_a", "ambiguous_b", "invalid_a", "invalid_b"} {
		t.Run(classification, func(t *testing.T) {
			row := CompareMatchReportRow{Classification: classification, ATrace: 0, BTrace: 0}
			data, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				ATrace *int64 `json:"a_trace"`
				BTrace *int64 `json:"b_trace"`
			}
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			wantA := classification == "matched" || classification == "a_only" || strings.HasSuffix(classification, "_a")
			wantB := classification == "matched" || classification == "b_only" || strings.HasSuffix(classification, "_b")
			if (decoded.ATrace != nil) != wantA || (decoded.BTrace != nil) != wantB {
				t.Fatalf("present physical trace zero or absent side encoded incorrectly: %s", data)
			}
			if (decoded.ATrace != nil && *decoded.ATrace != 0) || (decoded.BTrace != nil && *decoded.BTrace != 0) {
				t.Fatalf("physical trace zero changed in JSON: %s", data)
			}
		})
	}
}

func TestMatchRecordsFallsBackToXYOnIDConflictAndMarksDuplicates(t *testing.T) {
	a := []PrestackTraceRecord{
		{TraceNumber: 1, SourceID: 7, ReceiverID: 8, SourceX: 0, SourceY: 0, ReceiverX: 20, ReceiverY: 0, HasSource: true, HasReceiver: true, HeaderValid: true},
		{TraceNumber: 2, SourceID: 7, ReceiverID: 8, SourceX: 10, SourceY: 0, ReceiverX: 20, ReceiverY: 0, HasSource: true, HasReceiver: true, HeaderValid: true},
	}
	b := []PrestackTraceRecord{
		{TraceNumber: 10, SourceID: 7, ReceiverID: 8, SourceX: 10, SourceY: 0, ReceiverX: 20, ReceiverY: 0, HasSource: true, HasReceiver: true, HeaderValid: true},
		{TraceNumber: 11, SourceID: 7, ReceiverID: 8, SourceX: 0, SourceY: 0, ReceiverX: 20, ReceiverY: 0, HasSource: true, HasReceiver: true, HeaderValid: true},
	}
	r := MatchRecords(a, b)
	if r.Strategy != MatchKeySourceReceiverXY || len(r.Pairs) != 2 {
		t.Fatalf("ID conflict did not use XY: %+v", r)
	}
	dupA := append([]PrestackTraceRecord{a[0], a[0]}, a[1])
	dupB := append([]PrestackTraceRecord{b[0], b[0]}, b[1])
	r = MatchRecords(dupA, dupB)
	if len(r.Pairs) != 0 || len(r.AmbiguousA) != 3 || len(r.AmbiguousB) != 3 {
		t.Fatalf("duplicate key was silently selected: %+v", r)
	}
}

func TestMatchRecordsKeepsStableIDsWhenAnotherIDConflicts(t *testing.T) {
	// SourceID 7 is observed at two positions and must fall back to XY, while
	// the unrelated SourceID 9 remains eligible for the stronger ID key.
	a := []PrestackTraceRecord{
		{TraceNumber: 1, SourceID: 7, ReceiverID: 8, SourceX: 0, SourceY: 0, ReceiverX: 20, ReceiverY: 0, HasSource: true, HasReceiver: true, HeaderValid: true},
		{TraceNumber: 2, SourceID: 7, ReceiverID: 8, SourceX: 10, SourceY: 0, ReceiverX: 20, ReceiverY: 0, HasSource: true, HasReceiver: true, HeaderValid: true},
		{TraceNumber: 3, SourceID: 9, ReceiverID: 90, HeaderValid: true},
	}
	b := []PrestackTraceRecord{
		{TraceNumber: 11, SourceID: 7, ReceiverID: 8, SourceX: 10, SourceY: 0, ReceiverX: 20, ReceiverY: 0, HasSource: true, HasReceiver: true, HeaderValid: true},
		{TraceNumber: 12, SourceID: 7, ReceiverID: 8, SourceX: 0, SourceY: 0, ReceiverX: 20, ReceiverY: 0, HasSource: true, HasReceiver: true, HeaderValid: true},
		{TraceNumber: 13, SourceID: 9, ReceiverID: 90, HeaderValid: true},
	}
	r := MatchRecords(a, b)
	if len(r.Pairs) != 3 || r.Strategy != MatchKeyMixed {
		t.Fatalf("per-ID conflict disabled stable IDs: %+v", r)
	}
	if r.Pairs[2].Strategy != MatchKeySourceReceiverID || r.Pairs[2].ATrace != 3 || r.Pairs[2].BTrace != 13 {
		t.Fatalf("stable ID was not retained: %+v", r.Pairs)
	}
}

func TestMatchRecordsFallbackAndInvalidAccounting(t *testing.T) {
	a := []PrestackTraceRecord{
		matchRecord(1, 0, 0, 12, -5), // CDP + signed Offset fallback
		{TraceNumber: 2, HeaderError: "bad header"},
		{TraceNumber: 3, HeaderValid: true}, // no usable key
	}
	b := []PrestackTraceRecord{matchRecord(22, 0, 0, 12, -5), matchRecord(23, 0, 0, 99, 5)}
	r := MatchRecords(a, b)
	if len(r.Pairs) != 1 || r.Pairs[0].Strategy != MatchKeyCDPOffset {
		t.Fatalf("fallback=%+v", r)
	}
	if len(r.InvalidA) != 2 || len(r.BOnly) != 1 {
		t.Fatalf("invalid/only accounting=%+v", r)
	}
}

func TestMatchRecordsSkipsDuplicateXYWhenCDPOffsetIsUnique(t *testing.T) {
	// These flattened/stacked-style files commonly have zero Source/Receiver
	// IDs and one repeated (0,0) geometry tuple.  The geometry key is therefore
	// non-discriminating, but CDP plus the signed header offset still identifies
	// each trace.  A duplicate stronger key must not prevent trying that later
	// key.
	a := []PrestackTraceRecord{
		{TraceNumber: 1, SourceX: 0, SourceY: 0, ReceiverX: 0, ReceiverY: 0, HasSource: true, HasReceiver: true, CDP: 100, HasCDP: true, Offset: 0, HasOffset: true, HeaderValid: true},
		{TraceNumber: 2, SourceX: 0, SourceY: 0, ReceiverX: 0, ReceiverY: 0, HasSource: true, HasReceiver: true, CDP: 101, HasCDP: true, Offset: 0, HasOffset: true, HeaderValid: true},
	}
	b := []PrestackTraceRecord{
		{TraceNumber: 20, SourceX: 0, SourceY: 0, ReceiverX: 0, ReceiverY: 0, HasSource: true, HasReceiver: true, CDP: 101, HasCDP: true, Offset: 0, HasOffset: true, HeaderValid: true},
		{TraceNumber: 21, SourceX: 0, SourceY: 0, ReceiverX: 0, ReceiverY: 0, HasSource: true, HasReceiver: true, CDP: 100, HasCDP: true, Offset: 0, HasOffset: true, HeaderValid: true},
	}
	r := MatchRecords(a, b)
	if len(r.Pairs) != 2 {
		t.Fatalf("duplicate XY blocked CDP/offset fallback: %+v", r)
	}
	if r.Pairs[0].ATrace != 1 || r.Pairs[0].BTrace != 21 || r.Pairs[1].ATrace != 2 || r.Pairs[1].BTrace != 20 {
		t.Fatalf("A order or CDP pairing lost: %+v", r.Pairs)
	}
	if r.Pairs[0].Strategy != MatchKeyCDPOffset || r.Pairs[1].Strategy != MatchKeyCDPOffset {
		t.Fatalf("unexpected fallback strategy: %+v", r.Pairs)
	}
}

func TestMatchRecordsPhysicalOrderFallbackRequiresEqualKeylessShape(t *testing.T) {
	a := []PrestackTraceRecord{
		{TraceNumber: 4, HeaderValid: true},
		{TraceNumber: 5, HeaderValid: true},
	}
	b := []PrestackTraceRecord{
		{TraceNumber: 40, HeaderValid: true},
		{TraceNumber: 41, HeaderValid: true},
	}
	r := MatchRecords(a, b)
	if r.Strategy != MatchKeyPhysicalTraceOrder || len(r.Pairs) != 2 || len(r.InvalidA) != 0 || len(r.InvalidB) != 0 {
		t.Fatalf("keyless equal-shape selections did not use explicit fallback: %+v", r)
	}
	if r.Pairs[0].ATrace != 4 || r.Pairs[0].BTrace != 40 || r.Pairs[1].ATrace != 5 || r.Pairs[1].BTrace != 41 {
		t.Fatalf("physical-order fallback changed selection order: %+v", r.Pairs)
	}
	if !strings.Contains(r.Reason, "低置信度") {
		t.Fatalf("fallback reason omitted confidence warning: %q", r.Reason)
	}

	unequal := MatchRecords(a, append(b, PrestackTraceRecord{TraceNumber: 42, HeaderValid: true}))
	if unequal.Strategy == MatchKeyPhysicalTraceOrder || len(unequal.Pairs) != 0 || len(unequal.InvalidA) != len(a) || len(unequal.InvalidB) != 3 {
		t.Fatalf("unequal keyless selections unexpectedly fell back: %+v", unequal)
	}
}

func TestSampleAxisCompatibilityAndFloatDifference(t *testing.T) {
	a := SampleAxis{SampleCount: 3, SampleIntervalUS: 2000, DelayMS: 10, HasTimeOrigin: true, TimeOriginMS: 10}
	b := SampleAxis{SampleCount: 3, SampleIntervalUS: 2000, DelayMS: 10, HasTimeOrigin: true, TimeOriginMS: 10}
	if got := CompareSampleAxes(a, b); !got.Compatible {
		t.Fatalf("axes unexpectedly incompatible: %+v", got)
	}
	b.SampleCount = 4
	if got := CompareSampleAxes(a, b); got.Compatible || got.SampleCountEqual {
		t.Fatalf("sample count mismatch not detected: %+v", got)
	}
	if got := CompareSampleAxes(segy.Info{SamplesPerTrace: 3, SampleIntervalUS: 2000}, segy.Info{SamplesPerTrace: 3, SampleIntervalUS: 2000}); !got.Compatible {
		t.Fatalf("Info axes unexpectedly incompatible: %+v", got)
	}
	diff, err := DifferenceValues([]float64{1, 2, 3}, []float64{.5, 1, 1.5})
	if err != nil || !equalFloatSlice(diff, []float64{.5, 1, 1.5}) {
		t.Fatalf("difference=%v err=%v", diff, err)
	}
	if _, err := DifferenceValues([]float64{1}, nil); err == nil {
		t.Fatal("unequal sample vectors were accepted")
	}
	lo, hi := SymmetricValueRange([]float64{-2, 1, math.NaN()})
	if lo != -2 || hi != 2 {
		t.Fatalf("symmetric range=%g..%g", lo, hi)
	}
}

func TestSampleAxisWindowCompatibilityAndNormalization(t *testing.T) {
	a := SampleAxis{SampleCount: 8, SampleIntervalUS: 2000}
	b := SampleAxis{SampleCount: 8, SampleIntervalUS: 2000}
	wa, ok := NormalizeSampleWindow(-4, 99, a.SampleCount)
	if !ok || wa != (SampleWindow{Start: 0, End: 7}) {
		t.Fatalf("window normalization=%+v ok=%v", wa, ok)
	}
	if got := CompareSampleAxesWithWindows(a, b, SampleWindow{Start: 1, End: 4}, SampleWindow{Start: 1, End: 4}); !got.Compatible || !got.SampleWindowEqual {
		t.Fatalf("equal sample windows unexpectedly incompatible: %+v", got)
	}
	got := CompareSampleAxesWithWindows(a, b, SampleWindow{Start: 1, End: 4}, SampleWindow{Start: 1, End: 5})
	if got.Compatible || got.SampleWindowEqual || !strings.Contains(got.Reason, "sample window") {
		t.Fatalf("window mismatch not detected: %+v", got)
	}
	if _, ok := NormalizeSampleWindow(5, 4, 8); ok {
		t.Fatal("reversed sample window accepted")
	}
}

func TestCompareMatchReportIsMetadataOnlyAndStable(t *testing.T) {
	r := MatchRecords([]PrestackTraceRecord{matchRecord(1, 1, 2, 10, -20)}, []PrestackTraceRecord{matchRecord(9, 1, 2, 10, -20)})
	axis := CompareSampleAxes(SampleAxis{SampleCount: 3, SampleIntervalUS: 2000}, SampleAxis{SampleCount: 3, SampleIntervalUS: 2000})
	report := BuildCompareMatchReport(r, axis, true)
	b, err := report.JSON()
	if err != nil || !bytes.Contains(b, []byte(`"schema_version"`)) || bytes.Contains(b, []byte("samples")) {
		t.Fatalf("unexpected JSON report: %s err=%v", b, err)
	}
	var csvOut bytes.Buffer
	if err := WriteCompareMatchReportCSV(&csvOut, report); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(csvOut.Bytes(), []byte("a_trace")) || !bytes.Contains(csvOut.Bytes(), []byte("1")) {
		t.Fatalf("unexpected CSV report: %s", csvOut.String())
	}
	// Mutating the source result after report creation must not alter its pair
	// metadata.
	r.Pairs[0].ATrace = 999
	if report.Pairs[0].ATrace != 1 {
		t.Fatal("report retained an unsafe pair alias")
	}
	if report.SchemaVersion != "1.1" || len(report.Rows) != 1 {
		t.Fatalf("report schema/rows=%s/%d", report.SchemaVersion, len(report.Rows))
	}
	withOptions := BuildCompareMatchReportWithOptions(r, axis, CompareMatchReportOptions{APath: "a.sgy", BPath: "b.sgy", Selection: "CMP 10", SampleWindow: SampleWindow{Start: 2, End: 3}, IncludePairs: true})
	jsonBytes, err := withOptions.JSON()
	if err != nil || !bytes.Contains(jsonBytes, []byte("a.sgy")) || bytes.Contains(jsonBytes, []byte("amplitude")) || bytes.Contains(jsonBytes, []byte("samples")) {
		t.Fatalf("provenance report=%s err=%v", jsonBytes, err)
	}
	if !bytes.Contains(jsonBytes, []byte("sample_window")) || !bytes.Contains(jsonBytes, []byte("matched")) {
		t.Fatalf("report omitted window or summary: %s", jsonBytes)
	}
}

func TestCompareMatchReportIncludesAllClassifications(t *testing.T) {
	result := CompareMatchResult{
		Strategy: MatchKeyMixed,
		Pairs:    []TraceMatchPair{{ATrace: 1, BTrace: 9, Strategy: MatchKeyCDPOffset, Key: TraceMatchKey{Strategy: MatchKeyCDPOffset, CDP: 10, Offset: -20}}},
		AOnly:    []int64{2}, BOnly: []int64{10},
		AmbiguousA: []int64{3}, AmbiguousB: []int64{11},
		InvalidA: []int64{4}, InvalidB: []int64{12},
	}
	report := BuildCompareMatchReportWithOptions(result, SampleAxisCompatibility{}, CompareMatchReportOptions{APath: "a.sgy", BPath: "b.sgy", IncludePairs: true})
	if len(report.Rows) != 7 {
		t.Fatalf("rows=%d, want 7", len(report.Rows))
	}
	var csvOut bytes.Buffer
	if err := WriteCompareMatchReportCSV(&csvOut, report); err != nil {
		t.Fatal(err)
	}
	for _, classification := range []string{"matched", "a_only", "b_only", "ambiguous_a", "ambiguous_b", "invalid_a", "invalid_b"} {
		if !bytes.Contains(csvOut.Bytes(), []byte(classification)) {
			t.Fatalf("CSV omitted classification %q: %s", classification, csvOut.String())
		}
	}
	if bytes.Contains(csvOut.Bytes(), []byte("amplitude")) || bytes.Contains(csvOut.Bytes(), []byte("samples")) {
		t.Fatalf("classification report contains sample data: %s", csvOut.String())
	}
}

func equalFloatSlice(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-12 {
			return false
		}
	}
	return true
}
