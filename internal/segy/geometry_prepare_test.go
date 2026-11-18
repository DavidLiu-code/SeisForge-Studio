package segy

import "testing"

func TestGeometryIndexPrepareInitializesDecodedGuard(t *testing.T) {
	idx := &GeometryIndex{
		InlineValues:    []int32{10, 20},
		CrosslineValues: []int32{100, 200},
		TraceNumbers:    []int64{0, 1},
		RowOfTrace:      []int32{0, 1},
		ColOfTrace:      []int32{0, 1},
	}
	// Gob does not serialize unexported fields, so a decoded index starts with
	// a nil preparation guard and must still build its derived lookup safely.
	idx.prepare()
	if idx.prepOnce == nil {
		t.Fatal("prepare did not initialize the guard")
	}
	if len(idx.denseGrid) != 4 {
		t.Fatalf("unexpected derived grid size: got %d want 4", len(idx.denseGrid))
	}
}
