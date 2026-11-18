package segy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func geometryMemoryFixture(trace int64) *GeometryIndex {
	return &GeometryIndex{
		SourcePath: "memory-only.sgy", TraceCount: 1, ValidTraceCount: 1,
		InlineValues: []int32{1870}, CrosslineValues: []int32{184},
		TraceNumbers: []int64{trace}, RowOfTrace: []int32{0}, ColOfTrace: []int32{0},
	}
}

func geometryMemoryTestKeys(t *testing.T) string {
	t.Helper()
	prefix := t.Name() + "/"
	t.Cleanup(func() {
		geometryMemoryCache.Range(func(key, _ any) bool {
			if strings.HasPrefix(key.(string), prefix) {
				geometryMemoryCache.Delete(key.(string))
			}
			return true
		})
	})
	return prefix
}

func geometryMemoryCount(prefix string) int {
	n := 0
	geometryMemoryCache.Range(func(key, _ any) bool {
		if strings.HasPrefix(key.(string), prefix) {
			n++
		}
		return true
	})
	return n
}

func TestGeometryMemoryCacheBoundedAndKeepsRecentPointer(t *testing.T) {
	prefix := geometryMemoryTestKeys(t)
	first := geometryMemoryFixture(17)
	geometryMemoryCache.Store(prefix+"0", first)
	for i := 1; i < 40; i++ {
		geometryMemoryCache.Store(fmt.Sprintf("%s%d", prefix, i), geometryMemoryFixture(int64(i)))
	}
	// This is a resource policy, not an expectation copied from cache internals.
	if n := geometryMemoryCount(prefix); n > 8 {
		t.Fatalf("retained %d unique geometry indexes; at most 8 are allowed", n)
	}
	if _, ok := geometryMemoryCache.Load(prefix + "0"); ok {
		t.Fatal("oldest unused index was not evicted")
	}
	last, ok := geometryMemoryCache.Load(prefix + "39")
	if !ok || last.(*GeometryIndex).TraceNumbers[0] != 39 {
		t.Fatal("latest reusable index was not retained")
	}
	if trace, ok := first.TraceAt(1870, 184); !ok || trace != 17 {
		t.Fatal("eviction changed the live caller's immutable index")
	}
}

func TestGeometryMemoryCacheReplacementAndConcurrentReaders(t *testing.T) {
	prefix := geometryMemoryTestKeys(t)
	old := geometryMemoryFixture(11)
	replacement := geometryMemoryFixture(12)
	geometryMemoryCache.Store(prefix+"same", old)
	geometryMemoryCache.Store(prefix+"same", replacement)
	got, ok := geometryMemoryCache.Load(prefix + "same")
	if !ok || got != replacement || geometryMemoryCount(prefix) != 1 {
		t.Fatal("same-key Store must atomically replace one entry, not duplicate it")
	}
	var workers sync.WaitGroup
	for w := 0; w < 12; w++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for i := 0; i < 100; i++ {
				key := fmt.Sprintf("%sworker-%d-%d", prefix, worker, i)
				idx := geometryMemoryFixture(int64(worker*100 + i))
				idx.prepare()
				geometryMemoryCache.Store(key, idx)
				if v, hit := geometryMemoryCache.Load(key); hit {
					if trace, valid := v.(*GeometryIndex).TraceAt(1870, 184); !valid || trace != int64(worker*100+i) {
						t.Error("concurrent hit returned changed geometry")
					}
				}
			}
		}(w)
	}
	workers.Wait()
	if n := geometryMemoryCount(prefix); n > 8 {
		t.Fatalf("concurrent Store retained %d indexes; at most 8 are allowed", n)
	}
	if trace, ok := old.TraceAt(1870, 184); !ok || trace != 11 {
		t.Fatal("replacement mutated an existing consumer's index")
	}
}

func TestGeometryMemoryCacheByteBudgetAndLazyPreparation(t *testing.T) {
	cache := newGeometryMemoryCache(8, 1024)
	small := geometryMemoryFixture(9)
	cache.Store("small", small)
	if small.denseGrid != nil {
		t.Fatal("cache admission must not eagerly prepare derived geometry")
	}
	// The trace array alone retains 1600 bytes, despite exposing length one.
	large := geometryMemoryFixture(10)
	large.TraceNumbers = make([]int64, 1, 200)
	large.TraceNumbers[0] = 10
	cache.Store("large", large)
	if _, ok := cache.Load("large"); ok {
		t.Fatal("oversized backing capacity bypassed the 1024-byte budget")
	}
	if retained, ok := cache.Load("small"); !ok || retained != small {
		t.Fatal("oversized entry displaced a reusable small index")
	}
	cache.Store("small", large)
	if _, ok := cache.Load("small"); ok || cache.bytes != 0 {
		t.Fatal("oversized same-key replacement left stale data or byte accounting")
	}

	// Its currently allocated slices fit 2048 bytes, but the future 32×32
	// dense grid needs another 4096 bytes. Admission must reserve that amount
	// without running prepare, or future renders would evade the memory bound.
	grid := geometryMemoryFixture(13)
	grid.InlineValues = make([]int32, 32)
	grid.CrosslineValues = make([]int32, 32)
	cache = newGeometryMemoryCache(8, 2048)
	cache.Store("future-dense", grid)
	if _, ok := cache.Load("future-dense"); ok || grid.denseGrid != nil {
		t.Fatal("future dense storage was not reserved without eager preparation")
	}

	cache = newGeometryMemoryCache(8, 1024)
	for i := 0; i < 10; i++ {
		cache.Store(fmt.Sprintf("entry-%d", i), geometryMemoryFixture(int64(i)))
	}
	// Even their fixed metadata reserve alone is over 256 bytes per entry;
	// four entries cannot fit a 1024-byte budget, independently of LRU order.
	if len(cache.entries) > 3 || cache.bytes > 1024 {
		t.Fatalf("aggregate backing storage exceeded budget: %d entries / %d bytes", len(cache.entries), cache.bytes)
	}
}

func TestGeometryMemoryCacheLRUReplacementAndDeleteAccounting(t *testing.T) {
	cache := newGeometryMemoryCache(2, 4096)
	a, b, c := geometryMemoryFixture(1), geometryMemoryFixture(2), geometryMemoryFixture(3)
	cache.Store("a", a)
	cache.Store("b", b)
	cache.Load("a")
	cache.Store("c", c)
	if _, ok := cache.Load("b"); ok {
		t.Fatal("reuse did not protect the active index from LRU eviction")
	}
	if v, ok := cache.Load("a"); !ok || v != a {
		t.Fatal("LRU eviction changed or discarded the recently reused pointer")
	}
	before := cache.bytes
	cache.Store("a", a)
	if cache.bytes != before || len(cache.entries) != 2 {
		t.Fatal("same-key replacement double-counted retained memory")
	}
	cache.Delete("a")
	cache.Delete("c")
	cache.Delete("already-absent")
	if cache.bytes != 0 || len(cache.entries) != 0 || cache.recency.Len() != 0 {
		t.Fatal("delete left stale recency entries or retained-byte accounting")
	}
}

func TestGeometryMemoryCacheEvictionPreservesDiskCacheAndLiveIndex(t *testing.T) {
	f, cp := geometryContextFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	idx, stats, err := f.BuildGeometryIndexCachedContextProgress(ctx, 189, 193, 2, nil)
	if err != nil || stats.FromCache || !stats.FastRegular {
		t.Fatalf("initial regular geometry build: %+v %v", stats, err)
	}
	original, err := os.ReadFile(cp)
	if err != nil {
		t.Fatal(err)
	}
	prefix := geometryMemoryTestKeys(t)
	for i := 0; i < 9; i++ {
		geometryMemoryCache.Store(fmt.Sprintf("%s%d", prefix, i), geometryMemoryFixture(int64(i)))
	}
	if _, ok := geometryMemoryCache.Load(cp); ok {
		t.Fatal("inactive file index was not evicted")
	}
	if trace, ok := idx.TraceAt(103, 205); !ok || trace != 41 {
		t.Fatal("eviction broke an active reader's exact geometry")
	}
	reloaded, cacheStats, err := f.BuildGeometryIndexCachedContextProgress(ctx, 189, 193, 2, nil)
	if err != nil || !cacheStats.FromCache || reloaded == idx {
		t.Fatalf("evicted geometry did not reload from unchanged .lidx: %+v %v", cacheStats, err)
	}
	if trace, ok := reloaded.TraceAt(103, 205); !ok || trace != 41 {
		t.Fatal("disk-cache reload changed geometry")
	}
	after, err := os.ReadFile(cp)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("memory eviction changed persistent .lidx bytes")
	}
}
