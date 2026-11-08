package prestack

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func syntheticHeader(source, receiver, cdp, offset, sx, sy, rx, ry int32) testsegy.TraceHeader {
	return testsegy.TraceHeader{
		SourceID: source, ReceiverID: receiver, CDP: cdp, Offset: offset,
		SourceX: sx, SourceY: sy, ReceiverX: rx, ReceiverY: ry,
		Scalar: 1, Units: 1, SampleCount: 4, SampleIntervalUS: 2000,
	}
}

func buildSyntheticIndex(t *testing.T, name string, samples, interval int, headers []testsegy.TraceHeader) *PrestackIndex {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".sgy")
	if err := testsegy.Write(path, testsegy.Options{
		Cols: len(headers), Samples: samples, SampleIntervalUS: interval,
		TraceHeaders: headers,
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	index, err := BuildIndex(context.Background(), reader, DefaultHeaderMapping(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func TestSyntheticReaderReopenMatchesReorderedPhysicalTraces(t *testing.T) {
	a := buildSyntheticIndex(t, "a", 4, 2000, []testsegy.TraceHeader{
		syntheticHeader(100, 1, 10, -20, 0, 0, 20, 0),
		syntheticHeader(100, 2, 10, 20, 0, 0, 40, 0),
		syntheticHeader(101, 1, 20, -40, 100, 0, 120, 0),
	})
	b := buildSyntheticIndex(t, "b", 4, 2000, []testsegy.TraceHeader{
		syntheticHeader(101, 1, 20, -40, 100, 0, 120, 0),
		syntheticHeader(100, 2, 10, 20, 0, 0, 40, 0),
		syntheticHeader(100, 1, 10, -20, 0, 0, 20, 0),
	})
	result := MatchRecords(a.Records, b.Records)
	if result.Strategy != MatchKeyMixed || len(result.Pairs) != 3 {
		t.Fatalf("unexpected match result: %+v", result)
	}
	if got := []int64{result.Pairs[0].BTrace, result.Pairs[1].BTrace, result.Pairs[2].BTrace}; !reflect.DeepEqual(got, []int64{2, 1, 0}) {
		t.Fatalf("A order did not map to reordered B traces: %v", got)
	}
	for _, record := range append(append([]PrestackTraceRecord{}, a.Records...), b.Records...) {
		if !record.HeaderValid || !record.HasSource || !record.HasReceiver || !record.HasOffset {
			t.Fatalf("reader did not preserve header metadata: %+v", record)
		}
	}
}

func TestSyntheticReaderSelectionKindsAndRepeatability(t *testing.T) {
	headers := []testsegy.TraceHeader{
		syntheticHeader(100, 1, 10, -20, 0, 0, 20, 0),
		syntheticHeader(100, 2, 10, 0, 0, 0, 0, 20),
		syntheticHeader(101, 1, 20, 20, 100, 0, 120, 0),
		syntheticHeader(101, 2, 20, 40, 100, 0, 100, 20),
	}
	first := buildSyntheticIndex(t, "selection-a", 4, 2000, headers)
	second := buildSyntheticIndex(t, "selection-b", 4, 2000, headers)
	for _, kind := range []GatherType{GatherCMP, GatherShot, GatherReceiver, GatherOffset, GatherAzimuth, GatherRaw} {
		one, err := first.Gather(GatherSelection{Type: kind, Key: GatherKey{All: kind != GatherRaw}, Secondary: SecondaryPhysical, Sort: SortPhysical})
		if kind == GatherRaw {
			one, err = first.Gather(GatherSelection{Type: GatherRaw, Key: GatherKey{Raw: true}, Secondary: SecondaryPhysical})
		}
		if err != nil || len(one.TraceIndices) != len(headers) {
			t.Fatalf("kind %s all-range gather=%+v err=%v", kind, one, err)
		}
		if keys := first.AvailableGathers(kind); len(keys) == 0 {
			t.Fatalf("kind %s did not produce any keys", kind)
		}
	}
	if !reflect.DeepEqual(first.Records, second.Records) {
		t.Fatal("reopening identical synthetic metadata produced different records")
	}
	if !reflect.DeepEqual(first.AvailableGathers(GatherOffset), second.AvailableGathers(GatherOffset)) {
		t.Fatal("offset key ordering is not deterministic")
	}
}

func TestSyntheticReaderSampleAxisMismatchDisablesDelta(t *testing.T) {
	a := buildSyntheticIndex(t, "axis-a", 4, 2000, []testsegy.TraceHeader{syntheticHeader(1, 1, 1, 0, 0, 0, 10, 0)})
	b := buildSyntheticIndex(t, "axis-b", 5, 4000, []testsegy.TraceHeader{syntheticHeader(1, 1, 1, 0, 0, 0, 10, 0)})
	axis := CompareSampleAxes(
		SampleAxis{SampleCount: a.BinarySampleCount, SampleIntervalUS: a.BinarySampleIntervalUS},
		SampleAxis{SampleCount: b.BinarySampleCount, SampleIntervalUS: b.BinarySampleIntervalUS},
	)
	if axis.Compatible || axis.SampleCountEqual || axis.SampleIntervalEqual {
		t.Fatalf("incompatible synthetic axes were accepted: %+v", axis)
	}
}

func TestSyntheticReaderInvalidHeaderIsRetainedAndReported(t *testing.T) {
	valid := syntheticHeader(1, 1, 1, 0, 0, 0, 10, 0)
	invalid := testsegy.TraceHeader{} // zero sample axis and missing metadata
	idx := buildSyntheticIndex(t, "invalid", 4, 2000, []testsegy.TraceHeader{valid, invalid})
	if len(idx.Records) != 2 || idx.Records[1].HeaderValid {
		t.Fatalf("invalid physical slot was not retained: %+v", idx.Records)
	}
	stats := idx.QualityStats()
	if stats.TotalTraces != 2 || stats.InvalidHeaderTraces == 0 || stats.MissingCDP == 0 || stats.MissingSource == 0 || stats.MissingReceiver == 0 {
		t.Fatalf("invalid header was not reflected in QC: %+v", stats)
	}
}

func TestSyntheticReaderIDConflictFallsBackToXY(t *testing.T) {
	a := buildSyntheticIndex(t, "conflict-a", 4, 2000, []testsegy.TraceHeader{
		syntheticHeader(7, 8, 1, 10, 0, 0, 20, 0),
		syntheticHeader(7, 8, 2, 20, 100, 0, 120, 0),
	})
	b := buildSyntheticIndex(t, "conflict-b", 4, 2000, []testsegy.TraceHeader{
		syntheticHeader(7, 8, 2, 20, 100, 0, 120, 0),
		syntheticHeader(7, 8, 1, 10, 0, 0, 20, 0),
	})
	result := MatchRecords(a.Records, b.Records)
	if result.Strategy != MatchKeySourceReceiverXY || len(result.Pairs) != 2 {
		t.Fatalf("synthetic ID conflict did not fall back to XY: %+v", result)
	}
}
