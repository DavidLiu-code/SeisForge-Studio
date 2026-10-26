//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestPrestackCachedABWorkersUseFinalIndependentMappings(t *testing.T) {
	t.Setenv("SEISFORGE_PRESTACK_CACHE_BYPASS", "")
	opts := prestackcore.CacheOptions{Directory: t.TempDir()}
	aPath, bPath := compareResourceFixture(t, "cache-A.sgy"), compareResourceFixture(t, "cache-B.sgy")
	a, err := dataset.NewManager().Open(aPath)
	if err != nil {
		t.Fatal(err)
	}
	mapping := prestackcore.DefaultHeaderMapping()
	aCold := &prestackIndexResult{}
	buildPrestackAIndex(context.Background(), a, mapping, false, opts, nil, aCold)
	if aCold.err != nil || aCold.index == nil || aCold.cacheStats.Hit || aCold.cacheStats.ScannedHeaders != 60 {
		t.Fatalf("cold A: index=%v stats=%+v err=%v", aCold.index != nil, aCold.cacheStats, aCold.err)
	}
	aWarm := &prestackIndexResult{}
	buildPrestackAIndex(context.Background(), a, mapping, false, opts, nil, aWarm)
	if aWarm.err != nil || !aWarm.cacheStats.Hit || aWarm.cacheStats.ScannedHeaders != 0 || !reflect.DeepEqual(aCold.index.Records, aWarm.index.Records) {
		t.Fatalf("warm A changed records or scanned headers: stats=%+v err=%v", aWarm.cacheStats, aWarm.err)
	}
	bCold := &prestackCompareBResult{path: bPath, mapping: mapping}
	buildPrestackCompareBIndexCached(context.Background(), bCold, opts, nil)
	bWarm := &prestackCompareBResult{path: bPath, mapping: mapping}
	buildPrestackCompareBIndexCached(context.Background(), bWarm, opts, nil)
	if bCold.err != nil || bWarm.err != nil || bCold.cacheStats.Hit || !bWarm.cacheStats.Hit || bWarm.cacheStats.ScannedHeaders != 0 || !reflect.DeepEqual(bCold.index.Records, bWarm.index.Records) {
		t.Fatalf("B cache integration: cold=%+v warm=%+v errs=%v/%v", bCold.cacheStats, bWarm.cacheStats, bCold.err, bWarm.err)
	}
	if aCold.cacheStats.CachePath == bCold.cacheStats.CachePath {
		t.Fatal("A and B used the same cache identity")
	}
	changed := mapping
	changed.SourceIDByte = 17
	bRemapped := &prestackCompareBResult{path: bPath, mapping: changed}
	buildPrestackCompareBIndexCached(context.Background(), bRemapped, opts, nil)
	if bRemapped.err != nil || bRemapped.cacheStats.Hit || bRemapped.index.Mapping != changed || bRemapped.cacheStats.CachePath == bCold.cacheStats.CachePath {
		t.Fatalf("B remapping reused old mapping identity: stats=%+v err=%v", bRemapped.cacheStats, bRemapped.err)
	}
	if aWarm.index.Mapping != mapping || !reflect.DeepEqual(aCold.index.Records, aWarm.index.Records) {
		t.Fatal("B remapping modified A's cached index")
	}
	for _, path := range []string{aPath, bPath} {
		if err := compareResourceExclusiveRead(path); err != nil {
			t.Fatalf("cache worker retained Reader for %s: %v", path, err)
		}
	}
}

func TestPrestackCacheLookupWaitsForAutomaticMappingConfirmation(t *testing.T) {
	t.Setenv("SEISFORGE_PRESTACK_CACHE_BYPASS", "")
	path := filepath.Join(t.TempDir(), "unmapped.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 2, Cols: 2, Samples: 8}); err != nil {
		t.Fatal(err)
	}
	data, err := dataset.NewManager().Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(t.TempDir(), "must-not-be-created")
	opts := prestackcore.CacheOptions{Directory: cacheDir}
	progressCalls := 0
	progress := func(prestackcore.IndexProgress) { progressCalls++ }
	a := &prestackIndexResult{}
	buildPrestackAIndex(context.Background(), data, prestackcore.DefaultHeaderMapping(), true, opts, progress, a)
	b := &prestackCompareBResult{path: path, mapping: prestackcore.DefaultHeaderMapping(), detected: true}
	buildPrestackCompareBIndexCached(context.Background(), b, opts, progress)
	if a.err != nil || b.err != nil || !a.detection.NeedsConfirmation || !b.detection.NeedsConfirmation || a.index != nil || b.index != nil {
		t.Fatalf("unconfirmed mapping unexpectedly indexed: A=%+v B=%+v", a, b)
	}
	if progressCalls != 0 || a.cacheStats.CachePath != "" || b.cacheStats.CachePath != "" {
		t.Fatal("cache was consulted before the final mapping was confirmed")
	}
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed mapping touched cache directory: %v", err)
	}
	if err := compareResourceExclusiveRead(path); err != nil {
		t.Fatalf("mapping confirmation path retained Reader: %v", err)
	}
}

func TestPrestackCacheProgressIsGatedByIndependentAsyncIdentity(t *testing.T) {
	state, token := compareBRetryFixture()
	p := &prestackCacheProgress{generation: token.generation, bToken: token, progress: prestackcore.IndexProgress{Stage: "缓存命中", Done: 60, Total: 60}}
	if !prestackCacheProgressMatches(p, state, 17, prestackAsyncToken{}, true) {
		t.Fatal("current B progress rejected")
	}
	for name, change := range map[string]func(*prestackSession){
		"closed":       func(s *prestackSession) { s.compareBLoading = false },
		"owner token":  func(s *prestackSession) { s.ownerToken++ },
		"workspace":    func(s *prestackSession) { s.workspaceGeneration++ },
		"A dataset":    func(s *prestackSession) { s.datasetGeneration++ },
		"A mapping":    func(s *prestackSession) { s.mappingGeneration++ },
		"B dataset":    func(s *prestackSession) { s.compareBDatasetGeneration++ },
		"B mapping":    func(s *prestackSession) { s.compareBMappingGeneration++ },
		"selection":    func(s *prestackSession) { s.selectionGeneration++ },
		"compare mode": func(s *prestackSession) { s.compareModeGeneration++ },
		"B generation": func(s *prestackSession) { s.compareBGeneration++ },
	} {
		t.Run(name, func(t *testing.T) {
			s := state
			change(&s)
			if prestackCacheProgressMatches(p, s, 17, prestackAsyncToken{}, true) {
				t.Fatal("stale B cache progress could update UI")
			}
		})
	}
	if prestackCacheProgressMatches(p, state, 18, prestackAsyncToken{}, true) {
		t.Fatal("wrong HWND accepted B cache progress")
	}
	aToken := prestackAsyncToken{owner: 17, ownerToken: 2, workspaceGeneration: 3, datasetGeneration: 4}
	a := &prestackCacheProgress{generation: 22, aToken: aToken}
	state.loading, state.indexGeneration = true, 22
	if !prestackCacheProgressMatches(a, state, 17, aToken, false) {
		t.Fatal("current A progress rejected")
	}
	state.indexGeneration++
	if prestackCacheProgressMatches(a, state, 17, aToken, false) {
		t.Fatal("old A cache progress accepted after index cancellation")
	}
}

func TestPrestackCacheStatusDistinguishesHitsMissesAndWarnings(t *testing.T) {
	if got := prestackIndexCacheSummary(prestackcore.IndexBuildStats{Hit: true}); got != "缓存命中 | 加载完成" {
		t.Fatal(got)
	}
	if got := prestackIndexCacheSummary(prestackcore.IndexBuildStats{MissReason: "cache bypass requested"}); got != "绕过缓存，扫描道头 | 加载完成" {
		t.Fatal(got)
	}
	if got := prestackIndexCacheSummary(prestackcore.IndexBuildStats{WriteWarning: "read-only directory"}); !strings.Contains(got, "缓存失效，扫描道头") || !strings.Contains(got, "加载完成") || !strings.Contains(got, "read-only directory") {
		t.Fatal(got)
	}
	if got := prestackCacheProgressText("B", prestackcore.IndexProgress{Done: 30, Total: 60, Stage: "缓存失效，扫描道头"}); got != "B 缓存失效，扫描道头 50%（仅道头元数据）" {
		t.Fatal(got)
	}
	if got := prestackCacheProgressText("A", prestackcore.IndexProgress{Done: 60, Total: 60, Stage: "加载完成"}); strings.Contains(got, "加载完成") || !strings.Contains(got, "正在校验道头索引") {
		t.Fatalf("unvalidated worker progress claimed completion: %s", got)
	}
	if prestackCacheProgressPercent(prestackcore.IndexProgress{Done: 200, Total: 60}) != 100 || prestackCacheProgressPercent(prestackcore.IndexProgress{Done: -1, Total: 60}) != 0 {
		t.Fatal("progress escaped native progress-bar range")
	}
}

func TestPrestackCacheProgressCloseBDoesNotClearAOrConsumeNewerB(t *testing.T) {
	savedState, savedUI, savedHwnd := prestackState, prestackUI, prestackHwnd
	prestackDeliveryMu.Lock()
	savedAProgress, savedBProgress := prestackPendingCacheProgressA, prestackPendingCacheProgressB
	savedIndex, savedRender := prestackPendingIndex, prestackPendingRender
	savedB, savedCompareRender := prestackPendingCompareB, prestackPendingCompareRender
	savedIndexGen, savedRenderGen := prestackDeliveryIndexGeneration, prestackDeliveryRenderGeneration
	savedBGen, savedCompareRenderGen := prestackDeliveryCompareBGeneration, prestackDeliveryCompareRenderGeneration
	prestackDeliveryMu.Unlock()
	t.Cleanup(func() {
		prestackState, prestackUI, prestackHwnd = savedState, savedUI, savedHwnd
		prestackDeliveryMu.Lock()
		prestackPendingCacheProgressA, prestackPendingCacheProgressB = savedAProgress, savedBProgress
		prestackPendingIndex, prestackPendingRender = savedIndex, savedRender
		prestackPendingCompareB, prestackPendingCompareRender = savedB, savedCompareRender
		prestackDeliveryIndexGeneration, prestackDeliveryRenderGeneration = savedIndexGen, savedRenderGen
		prestackDeliveryCompareBGeneration, prestackDeliveryCompareRenderGeneration = savedBGen, savedCompareRenderGen
		prestackDeliveryMu.Unlock()
	})
	prestackHwnd, prestackUI = 0, prestackControls{}
	prestackState = prestackSession{compareBGeneration: 7, indexGeneration: 8,
		indexCacheStats:    prestackcore.IndexBuildStats{Hit: true, CachePath: "A-cache"},
		compareBCacheStats: prestackcore.IndexBuildStats{Hit: true, CachePath: "B-cache"}}
	aProgress := &prestackCacheProgress{generation: 8}
	bProgress := &prestackCacheProgress{generation: 7}
	prestackDeliveryMu.Lock()
	prestackPendingCacheProgressA, prestackPendingCacheProgressB = aProgress, bProgress
	prestackPendingCompareB = nil
	prestackDeliveryMu.Unlock()
	receivePrestackCacheProgress(6, true)
	receivePrestackCompareB(6)
	prestackDeliveryMu.Lock()
	preserved := prestackPendingCacheProgressB == bProgress
	prestackDeliveryMu.Unlock()
	if !preserved {
		t.Fatal("old B message consumed a newer request's cache progress")
	}
	cancelPrestackCompareB()
	prestackDeliveryMu.Lock()
	bCleared, aRetained := prestackPendingCacheProgressB == nil, prestackPendingCacheProgressA == aProgress
	prestackDeliveryMu.Unlock()
	if !bCleared || !aRetained || prestackState.indexCacheStats.CachePath != "A-cache" || prestackState.compareBCacheStats != (prestackcore.IndexBuildStats{}) {
		t.Fatal("closing B retained its cache state or altered A cache state")
	}
	cancelPrestackJobs()
	prestackDeliveryMu.Lock()
	allCleared := prestackPendingCacheProgressA == nil && prestackPendingCacheProgressB == nil
	prestackDeliveryMu.Unlock()
	if !allCleared {
		t.Fatal("workspace cancellation retained pending cache progress")
	}
}

func TestPrestackCachedWorkerRejectsSourceChangedBeforeReaderOpen(t *testing.T) {
	t.Setenv("SEISFORGE_PRESTACK_CACHE_BYPASS", "")
	for _, change := range []string{"mtime", "size", "sample count", "sample interval"} {
		t.Run(change, func(t *testing.T) {
			path := compareResourceFixture(t, "changed-before-worker.sgy")
			data, err := dataset.NewManager().Open(path)
			if err != nil {
				t.Fatal(err)
			}
			original := data.Metadata
			switch change {
			case "mtime":
				stamp := original.ModifiedAt.Add(2 * time.Second)
				if err := os.Chtimes(path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			case "size":
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := f.Write([]byte{0})
				closeErr := f.Close()
				if writeErr != nil || closeErr != nil {
					t.Fatalf("fixture append: %v / %v", writeErr, closeErr)
				}
			case "sample count", "sample interval":
				opts := testsegy.Options{Rows: 5, Cols: 12, Samples: 32, SampleIntervalUS: 2000}
				if change == "sample count" {
					opts.Samples = 16
				} else {
					opts.SampleIntervalUS = 4000
				}
				if err := testsegy.Write(path, opts); err != nil {
					t.Fatal(err)
				}
			}
			cacheDir := filepath.Join(t.TempDir(), "not-published")
			result := &prestackIndexResult{}
			progress := 0
			buildPrestackAIndex(context.Background(), data, prestackcore.DefaultHeaderMapping(), true,
				prestackcore.CacheOptions{Directory: cacheDir}, func(prestackcore.IndexProgress) { progress++ }, result)
			if !errors.Is(result.err, segy.ErrSourceChanged) || result.index != nil || result.detected || progress != 0 || result.cacheStats.CachePath != "" {
				t.Fatalf("stale Dataset published or reached detection/cache: result=%+v progress=%d", result, progress)
			}
			if data.Metadata != original {
				t.Fatal("failed index work rewrote A Dataset metadata")
			}
			// B uses this same production opener after its independent Manager
			// snapshot, guarding the smaller Manager.Open→OpenReader interval.
			reader, err := openPrestackDatasetReader(data)
			if reader != nil {
				_ = reader.Close()
				t.Fatal("stale Dataset returned a usable Reader")
			}
			if !errors.Is(err, segy.ErrSourceChanged) {
				t.Fatalf("shared A/B opener did not reject stale metadata: %v", err)
			}
			if err := compareResourceExclusiveRead(path); err != nil {
				t.Fatalf("rejected Dataset retained Reader: %v", err)
			}
			if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
				t.Fatalf("stale Dataset touched cache: %v", err)
			}
		})
	}
}
