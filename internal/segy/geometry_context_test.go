package segy

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func geometryContextFixture(t *testing.T, irregular bool) (*File, string) {
	t.Helper()
	cache := filepath.Join(t.TempDir(), "cache")
	t.Setenv("LOCALAPPDATA", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	heads := make([][]byte, 120)
	for i := range heads {
		r, c := i/12, i%12
		if irregular {
			// Interleave both axes to force the exhaustive scanner while
			// preserving all unique geometry coordinates.
			r, c = i%10, (i/10+i%10)%12
		}
		heads[i] = make([]byte, 240)
		binary.BigEndian.PutUint32(heads[i][188:192], uint32(100+r))
		binary.BigEndian.PutUint32(heads[i][192:196], uint32(200+c))
	}
	path := writeHeaderFixture(t, Big, makeASCIITextHeader("C 1 GEOMETRY CONTEXT"), nil, nil, heads, 4)
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	st, err := f.SourceFileInfo()
	if err != nil {
		t.Fatal(err)
	}
	cp, err := geometryCachePath(path, st, 189, 193)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { geometryMemoryCache.Delete(cp) })
	return f, cp
}

func assertGeometryNotPublished(t *testing.T, cachePath string) {
	t.Helper()
	if _, ok := geometryMemoryCache.Load(cachePath); ok {
		t.Fatal("canceled index reached memory cache")
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled index published cache: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(cachePath))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache staging leak: %v %v", entries, err)
	}
}

func TestGeometryContextCanceledBeforeBuild(t *testing.T) {
	f, cp := geometryContextFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	idx, _, err := f.BuildGeometryIndexCachedContextProgress(ctx, 189, 193, 2, nil)
	if idx != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled build: %v %v", idx, err)
	}
	assertGeometryNotPublished(t, cp)
}

func TestGeometryContextCancellationJoinsWorkersAndSkipsPublication(t *testing.T) {
	for _, irregular := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular-fast-path", true: "full-scan"}[irregular], func(t *testing.T) {
			f, cp := geometryContextFixture(t, irregular)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var once sync.Once
			var calls atomic.Int64
			idx, _, err := f.BuildGeometryIndexCachedContextProgress(ctx, 189, 193, 4, func(done, total int) {
				calls.Add(1)
				once.Do(cancel)
			})
			if idx != nil || !errors.Is(err, context.Canceled) || calls.Load() == 0 {
				t.Fatalf("mid-build cancellation: %v %v callbacks=%d", idx, err, calls.Load())
			}
			assertGeometryNotPublished(t, cp)
			// All scanner goroutines are joined before returning, so closing
			// the caller-owned Reader is safe immediately after cancellation.
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGeometryContextCacheAndLegacyResultsAgree(t *testing.T) {
	for _, irregular := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular", true: "irregular"}[irregular], func(t *testing.T) {
			f, cp := geometryContextFixture(t, irregular)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			idx, stats, err := f.BuildGeometryIndexCachedContextProgress(ctx, 189, 193, 3, nil)
			if err != nil || idx == nil || stats.FastRegular == irregular {
				t.Fatalf("context build: %v %+v", err, stats)
			}
			original, err := os.ReadFile(cp)
			if err != nil {
				t.Fatal(err)
			}
			geometryMemoryCache.Delete(cp)
			legacy, legacyStats, err := f.BuildGeometryIndexCachedProgress(189, 193, 1, nil)
			if err != nil || !legacyStats.FromCache {
				t.Fatalf("legacy cache read: %v %+v", err, legacyStats)
			}
			if !reflect.DeepEqual(idx.TraceNumbers, legacy.TraceNumbers) || !reflect.DeepEqual(idx.RowOfTrace, legacy.RowOfTrace) || !reflect.DeepEqual(idx.ColOfTrace, legacy.ColOfTrace) || !reflect.DeepEqual(idx.denseGrid, legacy.denseGrid) {
				t.Fatal("context API changed geometry semantics")
			}
			geometryMemoryCache.Delete(cp)
			cancelCtx, stop := context.WithCancel(context.Background())
			got, _, err := f.BuildGeometryIndexCachedContextProgress(cancelCtx, 189, 193, 1, func(int, int) { stop() })
			if got != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled cache load: %v %v", got, err)
			}
			if _, ok := geometryMemoryCache.Load(cp); ok {
				t.Fatal("canceled disk-cache load inserted memory index")
			}
			unchanged, err := os.ReadFile(cp)
			if err != nil || !reflect.DeepEqual(original, unchanged) {
				t.Fatal("canceled cache load changed pre-existing cache")
			}
		})
	}
}

func TestGeometryContextCanceledCacheSaveLeavesNoTemporaryFile(t *testing.T) {
	_, cp := geometryContextFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := saveGeometryCacheContext(ctx, cp, &GeometryIndex{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cache cancellation: %v", err)
	}
	assertGeometryNotPublished(t, cp)
}

type geometryCancelOnCheck struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *geometryCancelOnCheck) Err() error {
	c.checks++
	if c.checks >= 2 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestGeometryContextCancelDuringCacheEncoding(t *testing.T) {
	_, cp := geometryContextFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checked := &geometryCancelOnCheck{Context: ctx, cancel: cancel}
	idx := &GeometryIndex{TraceNumbers: make([]int64, 10000)}
	if err := saveGeometryCacheContext(checked, cp, idx); !errors.Is(err, context.Canceled) {
		t.Fatalf("encoding was not canceled: %v", err)
	}
	assertGeometryNotPublished(t, cp)
}
