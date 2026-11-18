//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	pseudocachecore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudocache"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func writePhase9PseudoFixture(t *testing.T) string {
	t.Helper()
	const traces, samples = 24, 48
	path := filepath.Join(t.TempDir(), "phase9-pseudo.sgy")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 3600)
	binary.BigEndian.PutUint16(header[3216:3218], 2000)
	binary.BigEndian.PutUint16(header[3220:3222], samples)
	binary.BigEndian.PutUint16(header[3224:3226], 5)
	if _, err := file.Write(header); err != nil {
		t.Fatal(err)
	}
	for trace := 0; trace < traces; trace++ {
		raw := make([]byte, 240+samples*4)
		binary.BigEndian.PutUint32(raw[20:24], uint32(trace+100))
		binary.BigEndian.PutUint32(raw[180:184], uint32(5000+trace*20))
		binary.BigEndian.PutUint32(raw[184:188], uint32(7000+trace*trace))
		binary.BigEndian.PutUint16(raw[88:90], 1)
		for sample := 0; sample < samples; sample++ {
			value := float32(math.Sin(float64(trace)/3) + math.Cos(float64(sample)/7))
			binary.BigEndian.PutUint32(raw[240+sample*4:244+sample*4], math.Float32bits(value))
		}
		if _, err := file.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPhase9PersistentTextureHitDoesNotOpenSourceReader(t *testing.T) {
	path := writePhase9PseudoFixture(t)
	data, err := dataset.NewManager().Open(path)
	if err != nil {
		t.Fatal(err)
	}
	line := &projectcore.CrookedProjectLine{ID: strings.ToLower(path), Name: "phase9", Path: path, Dataset: data}
	cache, err := pseudocachecore.New(filepath.Join(t.TempDir(), "cache"), 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	job := pseudoLoadJob{windowGen: 11, lineIndex: 0, token: 7, line: line,
		spec: segy.TraceCoordinateSpec{Source: segy.CoordinateEnsemble, XByte: 181, YByte: 185, CDPByte: 21, ScalarByte: 71, UnitsByte: 89},
		size: pseudo3dcore.TextureSize{Width: 96, Height: 80}, gain: 0, clip: 99, displayMode: segy.DisplayAdaptive,
		textureCache: cache}
	atomicStorePhase9RangeGeneration(0)
	first := performPseudoLoad(job)
	if first.err != nil || first.cacheHit || first.cacheWrite == nil || first.ioReadBytes <= 0 {
		t.Fatalf("unexpected cold result: err=%v hit=%v write=%v calls=%d bytes=%d", first.err, first.cacheHit, first.cacheWrite != nil, first.ioReadCalls, first.ioReadBytes)
	}
	if err := cache.Store(first.cacheWrite.key, first.cacheWrite.entry); err != nil {
		t.Fatal(err)
	}
	compatibleCache, err := pseudocachecore.New(filepath.Join(t.TempDir(), "compatible-cache"), 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	explicitJob := job
	explicitJob.spec = segy.DefaultCoordinateSpecs()[0]
	explicitKey, ok := pseudoPersistentCacheKey(explicitJob)
	if !ok {
		t.Fatal("could not build explicit coordinate cache key")
	}
	if err := compatibleCache.Store(explicitKey, first.cacheWrite.entry); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	second := performPseudoLoad(job)
	if second.err != nil || !second.cacheHit || second.ioReadCalls != 0 || second.texture == nil {
		t.Fatalf("unexpected warm result: err=%v hit=%v calls=%d", second.err, second.cacheHit, second.ioReadCalls)
	}
	if !bytes.Equal(first.texture.Indices, second.texture.Indices) {
		t.Fatal("persistent cache changed the indexed seismic texture")
	}
	compatibleJob := job
	compatibleJob.textureCache = compatibleCache
	compatible := performPseudoLoad(compatibleJob)
	if compatible.err != nil || !compatible.cacheHit || !bytes.Equal(first.texture.Indices, compatible.texture.Indices) {
		t.Fatalf("automatic coordinates did not reuse an equivalent explicit-coordinate cache: hit=%v err=%v", compatible.cacheHit, compatible.err)
	}
	baseKey, _ := pseudoPersistentCacheKey(job)
	aoiJob := job
	aoiJob.spatialRange = pseudo3dcore.XYRange{XMin: 5050, XMax: 5300, YMin: 7000, YMax: 7600, Valid: true}
	aoiKey, _ := pseudoPersistentCacheKey(aoiJob)
	if baseKey != aoiKey {
		t.Fatal("AOI unexpectedly participates in the persistent texture cache key")
	}
	gainJob := job
	gainJob.gain = 1
	gainKey, _ := pseudoPersistentCacheKey(gainJob)
	if gainKey == baseKey {
		t.Fatal("gain is missing from the persistent texture cache key")
	}
}

func atomicStorePhase9RangeGeneration(generation int64) {
	// Keep tests independent from whichever Phase test ran immediately before.
	atomic.StoreInt64(&pseudoActiveRangeGen, generation)
}

func TestPhase9LoadingCoalescesPreviewAndQueuesOneFinalRender(t *testing.T) {
	source := phase1FunctionSource(t, "pseudo_windows.go", "handlePseudoLoadResults")
	if strings.Contains(source, "queuePseudoSceneRender(true)") {
		t.Fatal("a full scene render is still queued for each completed line")
	}
	for _, required := range []string{"schedulePseudoProgressRender", "queuePseudoFinalRender", "finalRenderPending"} {
		if !strings.Contains(source, required) {
			t.Fatalf("loading render coalescing is missing %s", required)
		}
	}
}
