package prestack

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func put32(b []byte, pos int, v int32) { binary.BigEndian.PutUint32(b[pos-1:pos+3], uint32(v)) }
func put16(b []byte, pos int, v int16) { binary.BigEndian.PutUint16(b[pos-1:pos+1], uint16(v)) }

func prestackFixture(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/prestack.sgy"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	text := make([]byte, 3200)
	for i := range text {
		text[i] = ' '
	}
	bh := make([]byte, 400)
	put16(bh, 17-0, 2000) // absolute 3217 relative to binary 3201
	put16(bh, 21-0, 4)
	put16(bh, 25-0, 5)
	if _, err = f.Write(text); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(bh); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		h := make([]byte, 240)
		shot := int32(100 + i/4)
		cdp := int32(200 + i/2)
		recv := int32(10 + i%4)
		put32(h, 9, shot)
		put32(h, 13, recv)
		put32(h, 21, cdp)
		put32(h, 37, int32((i%4-1)*100))
		put16(h, 71, 1)
		put16(h, 89, 1)
		put32(h, 73, int32(1000+(i/4)*80))
		put32(h, 77, int32(2000+(i/4)*20))
		put32(h, 81, int32(1010+(i%4)*20))
		put32(h, 85, int32(2000+i*5+i%2))
		put32(h, 181, int32(1005+i*20))
		put32(h, 185, int32(2000+i*5))
		put32(h, 189, int32(1+i/4))
		put32(h, 193, int32(1+i/2))
		put16(h, 115, 4)
		put16(h, 117, 2000)
		if _, err = f.Write(h); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 4; j++ {
			var sample [4]byte
			binary.BigEndian.PutUint32(sample[:], math.Float32bits(float32(i+j)))
			if _, err = f.Write(sample[:]); err != nil {
				t.Fatal(err)
			}
		}
	}
	return path
}

func TestBuildIndexAndGatherTables(t *testing.T) {
	file, err := segy.Open(prestackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	m := DefaultHeaderMapping()
	progress := make([]IndexProgress, 0)
	idx, err := BuildIndex(context.Background(), file, m, func(p IndexProgress) { progress = append(progress, p) })
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Records) != 8 || len(idx.AvailableGathers(GatherShot)) != 2 {
		t.Fatalf("records/shots: %d/%d", len(idx.Records), len(idx.AvailableGathers(GatherShot)))
	}
	if len(idx.AvailableGathers(GatherCMP)) != 4 {
		t.Fatalf("CMP gathers=%d", len(idx.AvailableGathers(GatherCMP)))
	}
	if idx.FoldStats().Max != 2 || idx.FoldStats().Min != 2 {
		t.Fatalf("fold=%+v", idx.FoldStats())
	}
	key := idx.AvailableGathers(GatherCMP)[0]
	result, err := idx.Gather(GatherSelection{Type: GatherCMP, Key: key, Sort: SortOffset, Axis: AxisOffset})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TraceIndices) != 2 || result.Positions[0] > result.Positions[1] {
		t.Fatalf("gather=%+v", result)
	}
	all, err := idx.Gather(GatherSelection{Type: GatherCMP, Key: GatherKey{All: true}, Sort: SortPhysical, Axis: AxisTrace})
	if err != nil || len(all.TraceIndices) != len(idx.Records) {
		t.Fatalf("all-range gather=%+v err=%v", all, err)
	}
	for i, trace := range all.TraceIndices {
		if trace != int64(i) {
			t.Fatalf("all-range order=%v", all.TraceIndices)
		}
	}
	if !idx.Records[0].HasComputedOffset || math.Abs(idx.Records[0].ComputedOffset-math.Hypot(10, 0)) > 1e-9 {
		t.Fatalf("computed offset=%+v", idx.Records[0])
	}
	if len(progress) == 0 || progress[len(progress)-1].Stage != "完成" {
		t.Fatalf("progress=%+v", progress)
	}
}

func TestGatherKeyAndMappingValidation(t *testing.T) {
	if err := (HeaderMapping{SourceXByte: 73}).Validate(); err == nil {
		t.Fatal("expected paired coordinate validation")
	}
	if err := DefaultHeaderMapping().Validate(); err != nil {
		t.Fatal(err)
	}
	if (GatherKey{Grid: true, Inline: 1, Crossline: 2}).String() != "IL 1 / XL 2" {
		t.Fatal("grid key formatting")
	}
	if (GatherKey{All: true}).String() != "全部范围" {
		t.Fatal("all-range key formatting")
	}
}

func TestCommonOffsetGatherConfiguredBins(t *testing.T) {
	file, err := segy.Open(prestackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	idx, err := BuildIndex(context.Background(), file, DefaultHeaderMapping(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// The fixture contains -100, 0, 100 and 200 offsets, two traces each.
	keys := idx.AvailableGathers(GatherOffset)
	if len(keys) != 4 {
		t.Fatalf("default bins=%d, keys=%v", len(keys), keys)
	}
	if keys[0].String() != "Offset -100 ±10" || keys[1].String() != "Offset 0 ±10" {
		t.Fatalf("unexpected default keys: %v", keys)
	}
	selection := GatherSelection{Type: GatherOffset, Key: keys[1], OffsetBinSize: DefaultOffsetBinSize,
		Sort: SortPhysical, Axis: AxisOffset}
	result, err := idx.Gather(selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TraceIndices) != 2 || !result.OffsetRange.Valid || result.OffsetRange.Min != 0 || result.OffsetRange.Max != 0 {
		t.Fatalf("offset bin result=%+v", result)
	}
	if len(result.Positions) != 2 || result.Positions[0] != 0 || result.Positions[1] != 0 {
		t.Fatalf("offset positions=%v", result.Positions)
	}

	// A wider configurable bin groups -100 with zero and 100 with 200 under
	// nearest-centre rounding, while retaining physical trace order.
	wide := idx.AvailableGathersConfigured(GatherOffset, 200)
	if len(wide) != 2 || wide[0].OffsetBinIndex != 0 || wide[1].OffsetBinIndex != 1 {
		t.Fatalf("wide bins=%+v", wide)
	}
	wg, err := idx.Gather(GatherSelection{Type: GatherOffset, Key: wide[0], OffsetBinSize: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(wg.TraceIndices) != 4 || wg.TraceIndices[0] != 0 || wg.TraceIndices[3] != 5 {
		t.Fatalf("wide gather=%v", wg.TraceIndices)
	}
	if wg.OffsetRange.Min != -100 || wg.OffsetRange.Max != 0 {
		t.Fatalf("wide range=%+v", wg.OffsetRange)
	}
	// The key carries its width as edges, so a zero selection width can still
	// gather a configured key correctly.
	inferred, err := idx.Gather(GatherSelection{Type: GatherOffset, Key: wide[1]})
	if err != nil || len(inferred.TraceIndices) != 4 {
		t.Fatalf("inferred width gather=%+v err=%v", inferred, err)
	}

	// Missing offsets never become a fabricated zero bin.
	idx.Records[1].HasOffset = false
	keys = idx.AvailableGathersConfigured(GatherOffset, 20)
	if len(keys) != 4 {
		t.Fatalf("missing offset changed bins unexpectedly: %v", keys)
	}
	zero, err := idx.Gather(GatherSelection{Type: GatherOffset, Key: keys[1], OffsetBinSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(zero.TraceIndices) != 1 || zero.TraceIndices[0] != 5 {
		t.Fatalf("missing-offset gather=%v", zero.TraceIndices)
	}
}

func TestCommonOffsetBinBoundariesAndAdjacent(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{
		{TraceNumber: 0, Offset: -10, HasOffset: true},
		{TraceNumber: 1, Offset: -11, HasOffset: true},
		{TraceNumber: 2, Offset: 10, HasOffset: true},
		{TraceNumber: 3, Offset: 11, HasOffset: true},
		{TraceNumber: 4, Offset: math.NaN(), HasOffset: true},
	}}
	keys := idx.AvailableGathersConfigured(GatherOffset, 20)
	if len(keys) != 3 {
		t.Fatalf("boundary keys=%v", keys)
	}
	// Ties at ±10 round to the zero-centre bin; ±11 fall into ±20 bins.
	if keys[0].OffsetCenter != -20 || keys[1].OffsetCenter != 0 || keys[2].OffsetCenter != 20 {
		t.Fatalf("boundary centers=%v", keys)
	}
	if next, ok := idx.AdjacentGatherConfigured(GatherOffset, keys[0], 1, 20); !ok || next.OffsetCenter != 0 {
		t.Fatalf("adjacent=%v %v", next, ok)
	}
}

func TestRawTraceOrderRange(t *testing.T) {
	file, err := segy.Open(prestackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	idx, err := BuildIndex(context.Background(), file, DefaultHeaderMapping(), nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := idx.AvailableGathers(GatherRaw)
	if len(keys) != 1 || !keys[0].Raw || keys[0].String() != "Raw Trace Order" {
		t.Fatalf("raw keys=%v", keys)
	}
	// A zero range means the complete file; sorting/axis settings must not
	// reorder the physical records in this mode.
	all, err := idx.Gather(GatherSelection{Type: GatherRaw, Sort: SortOffset, Axis: AxisOffset})
	if err != nil || len(all.TraceIndices) != 8 || all.RawTraceStart != 0 || all.RawTraceEnd != 8 {
		t.Fatalf("raw all=%+v err=%v", all, err)
	}
	for i, trace := range all.TraceIndices {
		if trace != int64(i) || all.Positions[i] != float64(i) {
			t.Fatalf("raw order trace=%v positions=%v", all.TraceIndices, all.Positions)
		}
	}
	part, err := idx.RawGather(RawTraceSelection{TraceStart: 6, TraceEnd: 2, SampleStart: 3, SampleEnd: 7})
	if err != nil || len(part.TraceIndices) != 4 || part.RawTraceStart != 2 || part.RawTraceEnd != 6 {
		t.Fatalf("raw reversed=%+v err=%v", part, err)
	}
	for i, trace := range part.TraceIndices {
		if trace != int64(i+2) {
			t.Fatalf("raw reversed order=%v", part.TraceIndices)
		}
	}
	if part.Selection.SampleStart != 3 || part.Selection.SampleEnd != 7 {
		t.Fatalf("sample window not carried: %+v", part.Selection)
	}
	if got, err := idx.RawTraceIndices(-5, 99); err != nil || len(got) != 8 || got[0] != 0 || got[7] != 7 {
		t.Fatalf("raw clamp=%v err=%v", got, err)
	}
}

func TestGatherAllRangeIncludesEveryPhysicalTrace(t *testing.T) {
	file, err := segy.Open(prestackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	idx, err := BuildIndex(context.Background(), file, DefaultHeaderMapping(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []GatherType{GatherCMP, GatherShot, GatherReceiver, GatherOffset} {
		result, err := idx.Gather(GatherSelection{Type: kind, Key: GatherKey{All: true}, OffsetBinSize: DefaultOffsetBinSize})
		if err != nil {
			t.Fatalf("all %s: %v", kind, err)
		}
		if len(result.TraceIndices) != len(idx.Records) {
			t.Fatalf("all %s: got %d traces, want %d", kind, len(result.TraceIndices), len(idx.Records))
		}
		for i, trace := range result.TraceIndices {
			if trace != int64(i) {
				t.Fatalf("all %s reordered physical trace %d to %d", kind, i, trace)
			}
		}
	}
	if (GatherKey{All: true}).String() != "全部范围" {
		t.Fatal("all-range label changed")
	}
}

func TestNormalizeRawTraceRange(t *testing.T) {
	tests := []struct {
		total, start, end  int64
		wantStart, wantEnd int64
		ok                 bool
	}{
		{8, 0, 0, 0, 8, true},
		{8, -4, 3, 0, 3, true},
		{8, 7, 2, 2, 7, true},
		{8, 8, 0, 8, 8, false},
		{0, 0, 0, 0, 0, false},
	}
	for _, tc := range tests {
		a, b, ok := NormalizeRawTraceRange(tc.total, tc.start, tc.end)
		if a != tc.wantStart || b != tc.wantEnd || ok != tc.ok {
			t.Errorf("NormalizeRawTraceRange(%d,%d,%d)=(%d,%d,%v), want (%d,%d,%v)", tc.total, tc.start, tc.end, a, b, ok, tc.wantStart, tc.wantEnd, tc.ok)
		}
	}
}
