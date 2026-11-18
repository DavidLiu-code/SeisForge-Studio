package prestack

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func TestQualityStatsMetadataAndReports(t *testing.T) {
	reader, err := segy.Open(prestackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	index, err := BuildIndex(context.Background(), reader, DefaultHeaderMapping(), nil)
	if err != nil {
		t.Fatal(err)
	}
	stats := index.QualityStats()
	if stats.TotalTraces != 8 || stats.IndexedTraces != 8 || stats.ValidHeaderTraces != 8 || stats.InvalidHeaderTraces != 0 {
		t.Fatalf("file counters=%+v", stats)
	}
	if stats.MissingCDP != 0 || stats.MissingOffset != 0 || stats.CoordinateScalarErrors != 0 || stats.SampleCountInconsistent != 0 || stats.SampleIntervalInconsistent != 0 {
		t.Fatalf("unexpected QC issues=%+v", stats)
	}
	if stats.CMPCount != 4 || stats.ShotCount != 2 || stats.CommonOffsetBinCount != 4 || stats.Fold.Min != 2 || stats.Fold.Max != 2 {
		t.Fatalf("gather counters=%+v", stats)
	}
	if len(stats.OffsetDistribution.Bins) != 32 || len(stats.FoldDistribution.Bins) != 1 || histogramCount(stats.OffsetDistribution) != 8 {
		t.Fatalf("distributions=%+v/%+v", stats.OffsetDistribution, stats.FoldDistribution)
	}
	gather, err := index.Gather(GatherSelection{Type: GatherCMP, Key: index.AvailableGathers(GatherCMP)[0]})
	if err != nil {
		t.Fatal(err)
	}
	report := BuildQCReport(index, gather)
	if report.CurrentGather.PhysicalTraceCount != 2 || currentHistogramCount(report.Quality.OffsetDistribution) != 2 || currentHistogramCount(report.Quality.AzimuthDistribution) != 2 {
		t.Fatalf("current gather=%+v report=%+v", report.CurrentGather, report.Quality)
	}
	if currentHistogramCount(index.QualityStats().OffsetDistribution) != 0 {
		t.Fatal("report overlay modified immutable index distribution")
	}
	data, err := MarshalJSONReport(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded QCReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != "1" || decoded.Quality.TotalTraces != 8 || decoded.Quality.OffsetRange.Min != -100 || decoded.Quality.OffsetRange.Max != 200 {
		t.Fatalf("JSON round trip=%+v\n%s", decoded, data)
	}
	data, err = MarshalCSVReport(report)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 30 || strings.Join(rows[0], ",") != "section,field,value" {
		t.Fatalf("CSV rows=%d header=%v", len(rows), rows[0])
	}
	for _, ext := range []string{"csv", "json"} {
		path := t.TempDir() + "/report." + ext
		if err := report.ExportQCReport(path); err != nil {
			t.Fatal(err)
		}
		if st, err := os.Stat(path); err != nil || st.Size() == 0 {
			t.Fatalf("export %s: %v", path, err)
		}
	}
}

func TestBuildQCReportAllRangeIsNotOneFold(t *testing.T) {
	index := &PrestackIndex{SourcePath: "synthetic.sgy", Records: []PrestackTraceRecord{
		{TraceNumber: 0, HeaderValid: true, HasCDP: true, CDP: 1, HasOffset: true, Offset: -10},
		{TraceNumber: 1, HeaderValid: true, HasCDP: true, CDP: 1, HasOffset: true, Offset: 10},
	}}
	// QualityStats is intentionally lazy for synthetic indexes. The all-range
	// report must still identify the selection without manufacturing a Fold of 2.
	all, err := index.Gather(GatherSelection{Type: GatherCMP, Key: GatherKey{All: true}})
	if err != nil {
		t.Fatal(err)
	}
	report := BuildQCReport(index, all)
	if !report.CurrentGather.All || report.CurrentGather.PhysicalTraceCount != 2 || report.CurrentGather.Fold != 0 {
		t.Fatalf("all-range summary=%+v", report.CurrentGather)
	}
	if currentHistogramCount(report.Quality.FoldDistribution) != 0 {
		t.Fatalf("all-range fold overlay should be empty: %+v", report.Quality.FoldDistribution)
	}
}

func TestQualityStatsZerosScalarsAndMissingHeaders(t *testing.T) {
	index := &PrestackIndex{Mapping: DefaultHeaderMapping(), BinarySampleCount: 4, BinarySampleIntervalUS: 2000, Records: []PrestackTraceRecord{
		{TraceNumber: 0, HeaderValid: true, SourceID: 1, ReceiverID: 2, HasCDP: true, HasOffset: true, Offset: 0, CoordinateScalar: 0, CoordinateScalarValid: true, HeaderSampleCount: 4, HeaderSampleIntervalUS: 2000, SampleCount: 4, SampleIntervalUS: 2000},
		{TraceNumber: 1, HeaderValid: false, HeaderError: "invalid sample fields", CoordinateScalar: -100, CoordinateScalarValid: true, HeaderSampleCount: 8, HeaderSampleIntervalUS: 1000, SampleCount: 8, SampleIntervalUS: 1000},
		{TraceNumber: 2, HeaderValid: false, HeaderError: "short header", CoordinateScalarError: true},
	}}
	stats := index.QualityStats()
	if stats.ValidHeaderTraces != 1 || stats.InvalidHeaderTraces != 2 || stats.MissingOffset != 2 || stats.MissingCDP != 2 || stats.SampleCountInconsistent != 2 || stats.SampleIntervalInconsistent != 2 || stats.CoordinateScalarErrors != 1 {
		t.Fatalf("quality counters=%+v", stats)
	}
	if stats.OffsetDistribution.Bins[0].Min != 0 || stats.OffsetDistribution.Bins[0].Count != 1 {
		t.Fatalf("zero offset must remain valid: %+v", stats.OffsetDistribution)
	}
	for _, scalar := range []int16{0, 1, 10, -1, -100, math.MinInt16} {
		raw := make([]byte, 240)
		put16(raw, 71, scalar)
		put16(raw, 115, 4)
		put16(raw, 117, 2000)
		r := decodeRecord(0, raw, segy.Info{SamplesPerTrace: 4, SampleIntervalUS: 2000}, DefaultHeaderMapping())
		if !r.CoordinateScalarValid || r.CoordinateScalarError || !r.HeaderValid {
			t.Fatalf("legal scalar %d rejected: %+v", scalar, r)
		}
	}
}

func TestBuildIndexContinuesAfterIsolatedShortHeader(t *testing.T) {
	path := prestackFixture(t)
	reader, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	// Keep reader.Info from the valid binary header, then remove only the last
	// header.  BuildIndex must retain the physical record and report it rather
	// than fail the entire independent workspace.
	if err := os.Truncate(path, reader.Info.DataStart+7*reader.Info.TraceBytes+100); err != nil {
		t.Fatal(err)
	}
	index, err := BuildIndex(context.Background(), reader, DefaultHeaderMapping(), nil)
	if err != nil {
		t.Fatal(err)
	}
	stats := index.QualityStats()
	if len(index.Records) != 8 || index.Records[7].TraceNumber != 7 || index.Records[7].HeaderError == "" || stats.InvalidHeaderTraces != 1 || stats.ValidHeaderTraces != 7 {
		t.Fatalf("short header not retained: %+v", stats)
	}
	all, err := index.Gather(GatherSelection{Type: GatherCMP, Key: GatherKey{All: true}})
	if err != nil || len(all.TraceIndices) != 8 || all.TraceIndices[7] != 7 {
		t.Fatalf("physical order changed: %v %v", all.TraceIndices, err)
	}
}

func histogramCount(d Distribution) int {
	n := 0
	for _, b := range d.Bins {
		n += b.Count
	}
	return n
}
func currentHistogramCount(d Distribution) int {
	n := 0
	for _, b := range d.Bins {
		n += b.CurrentCount
	}
	return n
}
