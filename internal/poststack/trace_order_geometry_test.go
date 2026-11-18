package poststack

import (
	"errors"
	"math"
	"os"
	"reflect"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func TestTraceIndicesForRangeHonorsInclusiveEndAndStep(t *testing.T) {
	if got := TraceIndicesForRange(10, 2, 5, 1); !reflect.DeepEqual(got, []int64{2, 3, 4, 5}) {
		t.Fatalf("selected range=%v", got)
	}
	if got := TraceIndicesForRange(10, 2, 5, 2); !reflect.DeepEqual(got, []int64{2, 4}) {
		t.Fatalf("selected stepped range=%v", got)
	}
	if got := TraceIndicesForRange(10, 2, 2, 1); !reflect.DeepEqual(got, []int64{2}) {
		t.Fatalf("single trace was expanded: %v", got)
	}
	if got := TraceIndicesForRange(10, 8, 99, 1); !reflect.DeepEqual(got, []int64{8, 9}) {
		t.Fatalf("end was not clamped: %v", got)
	}
}

func TestTraceOrderGeometryPreservesRealFileIndices(t *testing.T) {
	a, err := TraceOrderGeometry("A.sgy", 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	mapping := []int64{6, 2, 4}
	b, err := TraceOrderGeometry("B.sgy", 3, mapping)
	if err != nil {
		t.Fatal(err)
	}
	mapping[0] = 0
	traces, coords, line, err := b.LineTraceNumbers(0, 1)
	if err != nil || line != 1 || !reflect.DeepEqual(traces, []int64{1, 2, 0}) || !reflect.DeepEqual(coords, []int32{3, 5, 7}) {
		t.Fatalf("unexpected virtual B line: %v %v %d %v", traces, coords, line, err)
	}
	for i, coordinate := range coords {
		at, ok := a.TraceAt(1, coordinate)
		bt, bok := b.TraceAt(1, coordinate)
		if !ok || !bok || at != int64(coordinate-1) || bt != traces[i] {
			t.Fatalf("wrong A/B mapping at %d: %d/%d", coordinate, at, bt)
		}
	}
	if a.InlineByte != 0 || a.CrosslineByte != 0 || b.InlineByte != 0 || b.CrosslineByte != 0 {
		t.Fatal("virtual geometry must not claim real header byte mappings")
	}
	if b.Rows() != 1 || b.Cols() != 3 || !b.Poststack || b.DuplicateCount != 0 {
		t.Fatal("unexpected virtual topology")
	}
}

func TestTraceOrderSourceGeometryKeepsSelectedPhysicalReaderIndices(t *testing.T) {
	selected := TraceIndicesForRange(40, 10, 21, 2)
	a, err := TraceOrderSourceGeometry("A.sgy", selected)
	if err != nil {
		t.Fatal(err)
	}
	b, err := TraceOrderGeometry("B.sfdata", int64(len(selected)), selected)
	if err != nil {
		t.Fatal(err)
	}
	selected[0] = 99
	traces, coords, _, err := a.LineTraceNumbers(0, 1)
	if err != nil || !reflect.DeepEqual(traces, []int64{10, 12, 14, 16, 18, 20}) || !reflect.DeepEqual(coords, []int32{11, 13, 15, 17, 19, 21}) {
		t.Fatalf("A reader indices no longer equal source physical traces: %v %v %v", traces, coords, err)
	}
	for row, coordinate := range coords {
		at, aok := a.TraceAt(1, coordinate)
		bt, bok := b.TraceAt(1, coordinate)
		if !aok || !bok || at != traces[row] || bt != int64(row) {
			t.Fatalf("A/B pairing at %d reads wrong original/compact trace: %d/%d", coordinate, at, bt)
		}
	}
	if _, err := TraceOrderSourceGeometry("A.sgy", nil); err == nil {
		t.Fatal("empty source selection accepted")
	}
}

func TestGeometryForTraceSelectionPreservesSourceCoordinates(t *testing.T) {
	source := &segy.GeometryIndex{
		SourcePath: "A.sgy", TraceCount: 6, InlineByte: 189, CrosslineByte: 193,
		InlineValues: []int32{1870}, CrosslineValues: []int32{184, 185, 186, 187, 188, 189},
		TraceNumbers: []int64{0, 1, 2, 3, 4, 5}, RowOfTrace: []int32{0, 0, 0, 0, 0, 0},
		ColOfTrace: []int32{0, 1, 2, 3, 4, 5}, ValidTraceCount: 6, Poststack: true,
	}
	b, err := GeometryForTraceSelection("B.sgy", source, []int64{4, 1, 5})
	if err != nil {
		t.Fatal(err)
	}
	traces, coords, actual, err := b.LineTraceNumbers(0, 1870)
	if err != nil || actual != 1870 || !reflect.DeepEqual(traces, []int64{1, 0, 2}) || !reflect.DeepEqual(coords, []int32{185, 188, 189}) {
		t.Fatalf("B geometry changed: traces=%v coords=%v actual=%d err=%v", traces, coords, actual, err)
	}
}

func TestTraceOrderGeometryRejectsAmbiguousOrOversizedMaps(t *testing.T) {
	for _, tc := range []struct {
		total int64
		ids   []int64
	}{
		{0, nil}, {-1, nil}, {20_000_001, nil}, {2, []int64{1}},
		{2, []int64{1, 1}}, {1, []int64{-1}}, {1, []int64{math.MaxInt32}},
	} {
		if _, err := TraceOrderGeometry("unchanged.sgy", tc.total, tc.ids); err == nil {
			t.Fatalf("accepted invalid map %+v", tc)
		}
	}
}

func TestSectionCleanupCanRetryAfterLockedReader(t *testing.T) {
	dir := t.TempDir()
	r := &SectionResult{ownedDir: dir}
	locked := errors.New("file is still held by a Reader")
	calls := 0
	if err := r.cleanupWith(func(path string) error {
		calls++
		if path != dir {
			t.Fatal("cleanup target changed")
		}
		return locked
	}); !errors.Is(err, locked) {
		t.Fatalf("first cleanup: %v", err)
	}
	if r.ownedDir != dir {
		t.Fatal("failed cleanup discarded the owned directory")
	}
	if err := r.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retry did not remove the directory")
	}
	if err := r.cleanupWith(func(string) error { calls++; return nil }); err != nil || calls != 1 {
		t.Fatal("successful cleanup must be idempotent")
	}
}
