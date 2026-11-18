package prestack

import (
	"bytes"
	"strings"
	"testing"
)

func TestFilterCompareRowsKeepsAOrderAndCategories(t *testing.T) {
	a := []PrestackTraceRecord{{TraceNumber: 10}, {TraceNumber: 11}, {TraceNumber: 12}}
	b := []PrestackTraceRecord{{TraceNumber: 20}, {TraceNumber: 21}, {TraceNumber: 22}}
	r := CompareMatchResult{
		Pairs: []TraceMatchPair{{ATrace: 10, BTrace: 21, AIndex: 0, BIndex: 1}},
		AOnly: []int64{11}, BOnly: []int64{20}, AmbiguousA: []int64{12}, InvalidB: []int64{22},
	}
	rows := FilterCompareRows(r, a, b, DefaultCompareFilterSet())
	if len(rows) != 2 || rows[0].Classification != CompareRowMatched || rows[1].Classification != CompareRowAOnly {
		t.Fatalf("default rows=%+v", rows)
	}
	all := CompareFilterSet{Matched: true, AOnly: true, BOnly: true, Ambiguous: true, Invalid: true}
	rows = FilterCompareRows(r, a, b, all)
	if len(rows) != 5 {
		t.Fatalf("all rows=%d want 5: %+v", len(rows), rows)
	}
	if rows[0].ATrace != 10 || rows[1].ATrace != 11 || rows[2].ATrace != 12 || rows[3].BTrace != 20 || rows[4].BTrace != 22 {
		t.Fatalf("row order lost: %+v", rows)
	}
}

func TestCompareReportIncludesHeaderMetadataAndNoSamples(t *testing.T) {
	a := []PrestackTraceRecord{{TraceNumber: 4, SourceID: 7, ReceiverID: 8, CDP: 9, SourceX: 1, SourceY: 2, ReceiverX: 20, ReceiverY: 3, HeaderOffset: -20, ComputedOffset: 20, Offset: -20, Azimuth: 90, HasSource: true, HasReceiver: true, HasCDP: true, HasOffset: true, HasAzimuth: true, HeaderValid: true}}
	b := []PrestackTraceRecord{{TraceNumber: 40, SourceID: 7, ReceiverID: 8, CDP: 9, SourceX: 1, SourceY: 2, ReceiverX: 20, ReceiverY: 3, HeaderOffset: -20, ComputedOffset: 20, Offset: -20, Azimuth: 90, HasSource: true, HasReceiver: true, HasCDP: true, HasOffset: true, HasAzimuth: true, HeaderValid: true}}
	r := MatchRecords(a, b)
	report := BuildCompareMatchReportWithOptions(r, SampleAxisCompatibility{Compatible: true}, CompareMatchReportOptions{APath: "a.sgy", BPath: "b.sgy", Primary: "CMP", Secondary: "Offset", ARecords: a, BRecords: b, IncludePairs: true})
	data, err := report.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a_metadata", "source_x", "header_offset", "primary", "secondary"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("JSON omitted %q: %s", want, data)
		}
	}
	if bytes.Contains(data, []byte("amplitude")) || bytes.Contains(data, []byte("samples")) {
		t.Fatalf("report contains sample data: %s", data)
	}
	var csvOut bytes.Buffer
	if err := WriteCompareMatchReportCSV(&csvOut, report); err != nil {
		t.Fatal(err)
	}
	text := csvOut.String()
	for _, want := range []string{"a_source_x", "a_header_offset", "b_computed_offset", "matched"} {
		if !strings.Contains(text, want) {
			t.Fatalf("CSV omitted %q: %s", want, text)
		}
	}
}
