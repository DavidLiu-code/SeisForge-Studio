package prestack

import "testing"

func TestSecondarySortAllRangeUsesPrimaryThenSecondary(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{
		{TraceNumber: 0, CDP: 2, HasCDP: true, SourceID: 10, ReceiverID: 2, Offset: 30, HasOffset: true},
		{TraceNumber: 1, CDP: 1, HasCDP: true, SourceID: 10, ReceiverID: 1, Offset: 20, HasOffset: true},
		{TraceNumber: 2, CDP: 2, HasCDP: true, SourceID: 11, ReceiverID: 2, Offset: 10, HasOffset: true},
		{TraceNumber: 3, CDP: 1, HasCDP: true, SourceID: 11, ReceiverID: 1, Offset: 40, HasOffset: true},
	}}
	allCMP, err := idx.Gather(GatherSelection{Type: GatherCMP, Key: GatherKey{All: true}, Secondary: SecondaryPhysical})
	if err != nil {
		t.Fatal(err)
	}
	assertTraceOrder(t, allCMP.TraceIndices, 1, 3, 0, 2)

	cmpOffset, err := idx.Gather(GatherSelection{Type: GatherCMP, Key: GatherKey{All: true}, Secondary: SecondaryOffset})
	if err != nil {
		t.Fatal(err)
	}
	assertTraceOrder(t, cmpOffset.TraceIndices, 1, 3, 2, 0)

	shotReceiver, err := idx.Gather(GatherSelection{Type: GatherShot, Key: GatherKey{All: true}, Secondary: SecondaryReceiver})
	if err != nil {
		t.Fatal(err)
	}
	assertTraceOrder(t, shotReceiver.TraceIndices, 1, 0, 3, 2)
}

func TestSecondarySortCommonOffsetPrimaryAndMissingLast(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{
		{TraceNumber: 0, Offset: 21, HasOffset: true, CDP: 2, HasCDP: true},
		{TraceNumber: 1, Offset: -1, HasOffset: true, CDP: 1, HasCDP: true},
		{TraceNumber: 2, Offset: 19, HasOffset: true, CDP: 1, HasCDP: true},
		{TraceNumber: 3, CDP: 3, HasCDP: true},
	}}
	result, err := idx.Gather(GatherSelection{Type: GatherOffset, Key: GatherKey{All: true}, OffsetBinSize: 20, Secondary: SecondaryCMP})
	if err != nil {
		t.Fatal(err)
	}
	// Offset bins are the primary groups: -1 first, then 19/21 in the zero
	// bin (ordered by CMP), and finally the trace with missing Offset.
	assertTraceOrder(t, result.TraceIndices, 1, 2, 0, 3)
}

func TestSecondarySortPureComparator(t *testing.T) {
	a := PrestackTraceRecord{TraceNumber: 0, Offset: -2, HasOffset: true, Azimuth: 40, HasAzimuth: true}
	b := PrestackTraceRecord{TraceNumber: 1, Offset: 5, HasOffset: true, Azimuth: 20, HasAzimuth: true}
	if compareSecondary(a, b, SecondaryAbsoluteOffset) >= 0 {
		t.Fatalf("absolute offset comparator did not order records")
	}
	if compareSecondary(a, b, SecondaryAzimuth) <= 0 {
		t.Fatalf("azimuth comparator did not order records")
	}
	missing := PrestackTraceRecord{TraceNumber: 2}
	if compareSecondary(a, missing, SecondaryOffset) >= 0 {
		t.Fatalf("missing offset should sort last")
	}
}

func TestSecondarySortOptionsMatchUserOrder(t *testing.T) {
	want := []SecondarySortMode{SecondaryPhysical, SecondaryOffset, SecondaryAbsoluteOffset,
		SecondaryAzimuth, SecondaryCMP, SecondaryShot, SecondaryReceiver, SecondaryCommonOffset}
	got := SecondarySortOptions()
	if len(got) != len(want) {
		t.Fatalf("secondary option count=%d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("secondary option %d=%v, want %v", i, got[i], want[i])
		}
		if mode, ok := SecondarySortModeFromIndex(i); !ok || mode != want[i] {
			t.Fatalf("secondary index %d=%v/%v, want %v", i, mode, ok, want[i])
		}
	}
	if _, ok := SecondarySortModeFromIndex(len(want)); ok {
		t.Fatal("out-of-range secondary option accepted")
	}
}

func TestSecondarySortNeverReordersRawMode(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{
		{TraceNumber: 0, Offset: 30, HasOffset: true},
		{TraceNumber: 1, Offset: -1, HasOffset: true},
		{TraceNumber: 2, Offset: 10, HasOffset: true},
	}}
	result, err := idx.Gather(GatherSelection{Type: GatherRaw, Secondary: SecondaryOffset})
	if err != nil {
		t.Fatal(err)
	}
	assertTraceOrder(t, result.TraceIndices, 0, 1, 2)
}

func TestGatherMergesMultipleKeysAndDeduplicates(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{
		{TraceNumber: 0, CDP: 10, HasCDP: true, Offset: 30, HasOffset: true},
		{TraceNumber: 1, CDP: 10, HasCDP: true, Offset: 10, HasOffset: true},
		{TraceNumber: 2, CDP: 11, HasCDP: true, Offset: 20, HasOffset: true},
		{TraceNumber: 3, CDP: 12, HasCDP: true, Offset: 40, HasOffset: true},
	}}
	idx.tables[GatherCMP] = gatherTable{
		keys:    []GatherKey{{ID: 10}, {ID: 11}, {ID: 12}},
		starts:  []int{0, 2, 3, 4},
		records: []int{0, 1, 2, 3},
	}
	keys := []GatherKey{{ID: 10}, {ID: 11}}
	result, err := idx.Gather(GatherSelection{Type: GatherCMP, Keys: keys, Key: GatherKey{All: true}, Secondary: SecondaryOffset})
	if err != nil {
		t.Fatal(err)
	}
	assertTraceOrder(t, result.TraceIndices, 1, 0, 2)
	if len(result.Selection.Keys) != 2 || !result.Selection.Key.All {
		t.Fatalf("selection metadata lost: %+v", result.Selection)
	}
	// Overlapping expressions are de-duplicated by physical trace number.
	result, err = idx.Gather(GatherSelection{Type: GatherCMP, Keys: []GatherKey{{ID: 10}, {ID: 10}}, Key: GatherKey{All: true}, Secondary: SecondaryPhysical})
	if err != nil {
		t.Fatal(err)
	}
	assertTraceOrder(t, result.TraceIndices, 0, 1)
}

func assertTraceOrder(t *testing.T, got []int64, want ...int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("trace count=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("trace order=%v, want %v", got, want)
		}
	}
}
