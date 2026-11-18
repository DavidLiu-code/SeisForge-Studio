package segy

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestQATimeSliceCoalescedWaiterReturnsItsCompletedSlabAfterEviction(t *testing.T) {
	const n = 2
	path := filepath.Join(t.TempDir(), "volume.sgy")
	fixture := qaWireRecord{5, 4, []string{"bf800000", "00000000", "3f800000"}, []float64{-1, 0, 1}}
	if err := os.WriteFile(path, qaWireFile(t, binary.BigEndian, fixture, 0), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c := NewTimeSliceCache(f, &GeometryIndex{TraceNumbers: []int64{0, 1}})
	// Build the real slab through the native reader, then represent the
	// observable waiter state after GetSlice found a shared load and its
	// completed cache block was evicted before the waiter resumed. A closed
	// completion channel makes this ordering deterministic without sleeps.
	slab, err := c.loadBlock(0)
	if err != nil {
		t.Fatal(err)
	}
	signal := make(chan struct{})
	close(signal)
	c.loading[0] = &slabLoad{done: signal, slab: slab}
	if data, ok := c.GetSliceCached(1); ok || data != nil {
		t.Fatalf("fixture should have an evicted cache block, got %v / %t", data, ok)
	}
	got, stats, err := c.GetSlice(1)
	if err != nil || len(got) != n || !reflect.DeepEqual(got, []float32{0, 1}) || !stats.CacheHit || stats.BlockStart != 0 || stats.BlockEnd != 2 {
		t.Fatalf("completed coalesced time-slice returned %v / %+v / %v; expected the actual [0, 1] slice", got, stats, err)
	}
}

func TestQAVolumeOrthogonalCoordinatesAndSamples(t *testing.T) {
	const ilCount, xlCount, ns = 3, 4, 5
	inline := [ilCount]int32{100, 105, 109}
	crossline := [xlCount]int32{201, 205, 213, 221}
	sequence := [...]int{7, 0, 8, 4, 1, 11, 3, 6, 9, 2, 10, 5}
	var gridToTrace [ilCount * xlCount]int64
	const recordSize = 240 + ns*4
	raw := make([]byte, 3600+recordSize*len(sequence))
	end := binary.BigEndian
	end.PutUint16(raw[3216:3218], 2000)
	end.PutUint16(raw[3220:3222], ns)
	end.PutUint16(raw[3224:3226], 2) // signed 32-bit integer, independently encoded
	for physical, grid := range sequence {
		row, col := grid/xlCount, grid%xlCount
		gridToTrace[grid] = int64(physical)
		tr := raw[3600+physical*recordSize:]
		end.PutUint32(tr[0:4], uint32(physical+1))
		end.PutUint32(tr[188:192], uint32(inline[row]))
		end.PutUint32(tr[192:196], uint32(crossline[col]))
		end.PutUint16(tr[108:110], 800)
		end.PutUint16(tr[114:116], ns)
		end.PutUint16(tr[116:118], 2000)
		for k := 0; k < ns; k++ {
			end.PutUint32(tr[240+k*4:], uint32(1000*row+100*col+k))
		}
	}
	path := filepath.Join(t.TempDir(), "shuffled_3x4x5.sgy")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, _, err := f.BuildGeometryIndexCached(189, 193, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(g.InlineValues, inline[:]) || !slices.Equal(g.CrosslineValues, crossline[:]) || len(g.TraceNumbers) != len(sequence) {
		t.Fatalf("unexpected physical geometry: IL=%v XL=%v traces=%v", g.InlineValues, g.CrosslineValues, g.TraceNumbers)
	}
	for r, il := range inline {
		for c, xl := range crossline {
			physical, ok := g.TraceAt(il, xl)
			if !ok || physical != gridToTrace[r*xlCount+c] {
				t.Fatalf("IL/XL=%d/%d maps to %d/%t, expected physical %d", il, xl, physical, ok, gridToTrace[r*xlCount+c])
			}
			if v, err := f.TraceTimeOriginUS(physical); err != nil || v != 800000 {
				t.Fatalf("trace %d start=%d µs, expected 800000: %v", physical, v, err)
			}
		}
	}
	for mode, lines := range [][]int32{inline[:], crossline[:]} {
		for selected, line := range lines {
			traces, coords, found, err := g.LineTraceNumbers(mode, line)
			other := crossline[:]
			if mode == 1 {
				other = inline[:]
			}
			if err != nil || found != line || !slices.Equal(coords, other) {
				t.Fatalf("mode %d line %d geometry: %v / %d / %v", mode, line, coords, found, err)
			}
			got, _, err := f.RenderTraceIndicesValues(traces, RenderOptions{Width: len(other), Height: ns, SampleEnd: -1, DisplayMode: DisplayNearest, ReadStrategy: ReadStrategySparseCoalesced})
			if err != nil || len(got) != len(other)*ns {
				t.Fatalf("mode %d line %d read: %v / %v", mode, line, got, err)
			}
			for k := 0; k < ns; k++ {
				for nav := range other {
					r, c := selected, nav
					if mode == 1 {
						r, c = nav, selected
					}
					if want := float64(1000*r + 100*c + k); got[k*len(other)+nav] != want {
						t.Fatalf("mode %d line %d axis %d sample %d = %g, want %g", mode, line, other[nav], k, got[k*len(other)+nav], want)
					}
				}
			}
		}
	}
	cache := NewTimeSliceCache(f, g)
	for k := 0; k < ns; k++ {
		values, _, err := cache.GetSlice(k)
		if err != nil || len(values) != len(sequence) {
			t.Fatalf("time %d ms read: %v / %v", 800+2*k, values, err)
		}
		for grid, tr := range gridToTrace {
			j := slices.Index(g.TraceNumbers, tr)
			want := float32(1000*(grid/xlCount) + 100*(grid%xlCount) + k)
			if j < 0 || values[j] != want {
				t.Fatalf("time %d ms grid %d: index=%d values=%v, want %g", 800+2*k, grid, j, values, want)
			}
		}
	}
	t.Log("shuffled 3 IL × 4 XL × 5 samples: all cells exact across each orthogonal direction; delay 800 ms and dt 2 ms")
}
