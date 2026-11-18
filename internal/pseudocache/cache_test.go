package pseudocache

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
)

func testEntry(seed byte) Entry {
	indices := make([]byte, 32*24)
	for i := range indices {
		indices[i] = byte(int(seed) + i%31)
	}
	return Entry{
		Geometry: geometrycore.CrookedLineGeometry{
			TraceIndices: []int64{0, 1, 2}, CDP: []int32{10, 11, 12}, HasCDP: []bool{true, true, true},
			X: []float64{1, 2, 3}, Y: []float64{4, 6, 5}, Distance: []float64{0, 2.2, 3.6}, TotalDistance: 3.6,
		},
		Points:       []pseudo3dcore.Point{{X: 1, Y: 4}, {X: 2, Y: 6}, {X: 3, Y: 5}},
		U:            []float64{0, .61, 1},
		TraceIndices: []int64{0, 1, 2}, Positions: []float64{0, 2.2, 3.6},
		PositionStart: 0, PositionEnd: 3.6, Length: 3.6, Multiplier: .1,
		Width: 32, Height: 24, Indices: indices,
	}
}

func testKey(path string, seed int64) CacheKey {
	return CacheKey{SourcePath: path, FileSize: 1000 + seed, ModTimeUnixNano: 2000 + seed, DataStart: 3600,
		TraceBytes: 16624, TraceCount: 3, SamplesPerTrace: 4096, SampleIntervalUS: 1000, FormatCode: 5, BytesPerSample: 4,
		CoordinateSource: "ensemble", XByte: 181, YByte: 185, CDPByte: 21, ScalarByte: 71, UnitsByte: 89,
		NavigationFingerprint: "nav", AlgorithmVersion: 1, Width: 32, Height: 24, GainPercent: 0, ClipPercent: 99,
		DisplayMode: 2, SampleStart: 0, SampleEnd: 4095}
}

func TestCacheRoundTripInvalidationAndCorruption(t *testing.T) {
	cache, err := New(t.TempDir(), DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	key := testKey(filepath.Join("test-fixtures", "line.sgy"), 1)
	want := testEntry(7)
	if err := cache.Store(key, want); err != nil {
		t.Fatal(err)
	}
	got, hit, err := cache.Load(key)
	if err != nil || !hit || !reflect.DeepEqual(got, want) {
		t.Fatalf("cache round trip failed: hit=%v err=%v", hit, err)
	}
	changed := key
	changed.FileSize++
	if _, hit, err := cache.Load(changed); err != nil || hit {
		t.Fatalf("source metadata change did not invalidate cache: hit=%v err=%v", hit, err)
	}
	changed = key
	changed.ModTimeUnixNano++
	if _, hit, err := cache.Load(changed); err != nil || hit {
		t.Fatalf("source modification time did not invalidate cache: hit=%v err=%v", hit, err)
	}
	changed = key
	changed.GainPercent = 12
	if _, hit, err := cache.Load(changed); err != nil || hit {
		t.Fatalf("render parameter change did not invalidate cache: hit=%v err=%v", hit, err)
	}
	path, _, err := cache.pathFor(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := cache.Load(key); err != nil || hit {
		t.Fatalf("corrupt cache was not ignored: hit=%v err=%v", hit, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("corrupt cache file was not deleted")
	}
}

func TestCachePrunesLeastRecentlyUsedAndTemporaryFiles(t *testing.T) {
	cache, err := New(t.TempDir(), DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	keys := []CacheKey{testKey("a.sgy", 1), testKey("b.sgy", 2), testKey("c.sgy", 3)}
	for index, key := range keys {
		if err := cache.Store(key, testEntry(byte(index))); err != nil {
			t.Fatal(err)
		}
		path, _, _ := cache.pathFor(key)
		stamp := time.Now().Add(time.Duration(index-3) * time.Hour)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	firstPath, _, _ := cache.pathFor(keys[0])
	info, err := os.Stat(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, hit, err := cache.Load(keys[0]); err != nil || !hit {
		t.Fatalf("could not refresh cache access time: hit=%v err=%v", hit, err)
	}
	cache.MaxBytes = info.Size() * 2
	temporary := filepath.Join(cache.Root, "abandoned.tmp")
	if err := os.WriteFile(temporary, []byte("temporary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cache.Prune(); err != nil {
		t.Fatal(err)
	}
	secondPath, _, _ := cache.pathFor(keys[1])
	if _, err := os.Stat(secondPath); !os.IsNotExist(err) {
		t.Fatal("least recently used cache entry was not evicted")
	}
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatal("recently accessed cache entry was evicted")
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("abandoned temporary cache file was not removed")
	}
	var total int64
	entries, _ := os.ReadDir(cache.Root)
	for _, entry := range entries {
		info, _ := entry.Info()
		total += info.Size()
	}
	if total > cache.MaxBytes {
		t.Fatalf("cache exceeds limit: %d > %d", total, cache.MaxBytes)
	}
}
