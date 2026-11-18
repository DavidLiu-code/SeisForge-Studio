package geometry

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestCrookedGeometryDistanceBoundsAndNearestTrace(t *testing.T) {
	values := []segy.TraceCoordinate{
		{Trace: 0, CDP: 10, X: 0, Y: 0, Valid: true, HasCDP: true},
		{Trace: 1, CDP: 11, X: 3, Y: 4, Valid: true, HasCDP: true},
		{Trace: 3, CDP: 12, X: 6, Y: 8, Valid: true, HasCDP: true},
	}
	geometry, err := NewCrookedLine(values, segy.DefaultCoordinateSpec())
	if err != nil {
		t.Fatal(err)
	}
	if geometry.Kind() != KindCrookedLine || geometry.TraceCount() != 3 || math.Abs(geometry.TotalDistance-10) > 1e-9 {
		t.Fatalf("unexpected crooked geometry: %+v", geometry)
	}
	location, ok := geometry.TraceLocation(1)
	if !ok || !location.HasXY || !location.HasCDP || location.CDP != 11 || location.Distance != 5 {
		t.Fatalf("unexpected trace location: %+v ok=%v", location, ok)
	}
	trace, position, ok := geometry.TraceAtDistance(6)
	if !ok || trace != 1 || position != 1 {
		t.Fatalf("unexpected TraceAtDistance: trace=%d position=%d ok=%v", trace, position, ok)
	}
	trace, position, ok = geometry.NearestTraceXY(5.8, 8.1)
	if !ok || trace != 3 || position != 2 {
		t.Fatalf("unexpected NearestTraceXY: trace=%d position=%d ok=%v", trace, position, ok)
	}
	bounds := geometry.Bounds()
	if !bounds.HasXY || bounds.XMin != 0 || bounds.XMax != 6 || bounds.YMin != 0 || bounds.YMax != 8 || !geometry.CDPMonotonic() {
		t.Fatalf("unexpected bounds/monotonic state: %+v", bounds)
	}
}

func TestCrookedDistanceSamplingReflectsNonUniformSpacing(t *testing.T) {
	geometry, err := NewCrookedLine([]segy.TraceCoordinate{
		{Trace: 0, X: 0, Y: 1, Valid: true},
		{Trace: 1, X: 1, Y: 1, Valid: true},
		{Trace: 2, X: 10, Y: 1, Valid: true},
	}, segy.DefaultCoordinateSpec())
	if err != nil {
		t.Fatal(err)
	}
	if sampled := geometry.SampleDistanceWindow(0, 10, 3); !reflect.DeepEqual(sampled, []int64{0, 1, 2}) {
		t.Fatalf("distance sampling ignored non-uniform positions: %v", sampled)
	}
}

func writeTrajectoryFixture(t *testing.T, curved bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trajectory.sgy")
	coordinates := make([]testsegy.TraceCoordinate, 64)
	for i := range coordinates {
		y := i * 10
		if curved {
			y = i * i
		}
		coordinates[i] = testsegy.TraceCoordinate{X: int32(i * 100), Y: int32(y), CDP: int32(1000 + i)}
	}
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: len(coordinates), Samples: 2, CoordinateScalar: 1, CoordinateUnits: 1, Coordinates: coordinates}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectDistinguishesStraightAndCrookedXY(t *testing.T) {
	straightFile, err := segy.Open(writeTrajectoryFixture(t, false))
	if err != nil {
		t.Fatal(err)
	}
	straight, err := Detect(straightFile, 3000)
	_ = straightFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if straight.Kind != KindLine2D {
		t.Fatalf("straight XY trajectory was not kept in Line2D: %+v", straight)
	}
	curvedFile, err := segy.Open(writeTrajectoryFixture(t, true))
	if err != nil {
		t.Fatal(err)
	}
	curved, err := Detect(curvedFile, 3000)
	_ = curvedFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if curved.Kind != KindCrookedLine || curved.Score < RecommendationScoreMin || curved.Confidence < RecommendationConfidenceMin {
		t.Fatalf("curved XY trajectory was not recommended as Crooked: %+v", curved)
	}
}

func TestBuildCrookedUsesMetadataCache(t *testing.T) {
	path := writeTrajectoryFixture(t, true)
	file, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := BuildCrookedCachedProgress(file, segy.TraceCoordinateSpec{Source: segy.CoordinateAuto}, 2, nil)
	_ = file.Close()
	if err != nil || first.Geometry == nil || first.Stats.FromCache {
		t.Fatalf("unexpected first build: result=%+v err=%v", first, err)
	}
	if !first.Stats.ReusedDetectionHeaders {
		t.Fatal("full coordinate detection was read a second time during the first geometry build")
	}
	file, err = segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildCrookedCachedProgress(file, segy.TraceCoordinateSpec{Source: segy.CoordinateAuto}, 2, nil)
	_ = file.Close()
	if err != nil || second.Geometry == nil || !second.Stats.FromCache || second.Stats.CachePath == "" {
		t.Fatalf("unexpected cached build: result=%+v err=%v", second, err)
	}
	if !second.Stats.ResolvedAutoCache || second.Detection.Sampled != 0 {
		t.Fatal("automatic cached build still scanned coordinate headers")
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstPath, err := crookedCachePath(path, stat.Size(), stat.ModTime().UnixNano(), segy.DefaultCoordinateSpecs()[0])
	if err != nil {
		t.Fatal(err)
	}
	secondPath, err := crookedCachePath(path, stat.Size(), stat.ModTime().UnixNano(), segy.DefaultCoordinateSpecs()[1])
	if err != nil {
		t.Fatal(err)
	}
	if firstPath == secondPath {
		t.Fatal("changing coordinate header bytes did not invalidate the .cidx cache key")
	}
	secondSpec := segy.DefaultCoordinateSpecs()[1]
	saveCrookedCache(secondPath, path, stat.Size(), stat.ModTime().UnixNano(), secondSpec, first.Geometry)
	file, err = segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ambiguous, err := BuildCrookedCachedProgress(file, segy.TraceCoordinateSpec{Source: segy.CoordinateAuto}, 2, nil)
	_ = file.Close()
	if err != nil || !ambiguous.Stats.FromCache || ambiguous.Stats.ResolvedAutoCache || ambiguous.Detection.Sampled == 0 {
		t.Fatalf("multiple standard caches did not fall back to automatic detection: result=%+v err=%v", ambiguous, err)
	}
}

func TestBuildCrookedConcurrentColdCacheUsesUniqueTemporaryFiles(t *testing.T) {
	const (
		traceCount = 2048
		builders   = 8
	)
	// Keep the persistent-cache assertion inside the test sandbox instead of
	// relying on the interactive user's real LocalAppData directory.
	t.Setenv("LocalAppData", t.TempDir())
	path := filepath.Join(t.TempDir(), "concurrent-trajectory.sgy")
	coordinates := make([]testsegy.TraceCoordinate, traceCount)
	for i := range coordinates {
		coordinates[i] = testsegy.TraceCoordinate{
			X:   int32(100000 + i*10),
			Y:   int32(200000 + (i*i)%100000 + i*3),
			CDP: int32(1000 + i),
		}
	}
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: traceCount, Samples: 2, CoordinateScalar: 1, CoordinateUnits: 1, Coordinates: coordinates}); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	spec := segy.DefaultCoordinateSpec()
	cachePath, err := crookedCachePath(path, stat.Size(), stat.ModTime().UnixNano(), spec)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(cachePath)
	for _, temporary := range mustGlob(t, cachePath+".*.tmp") {
		_ = os.Remove(temporary)
	}
	crookedMemoryCache.Delete(cachePath)

	// Hold each build at its first scan progress callback.  Once all builders
	// are inside the cold-cache scan they are released together, guaranteeing
	// concurrent cache writers instead of relying on scheduler timing.
	var scanning sync.WaitGroup
	scanning.Add(builders)
	release := make(chan struct{})
	type outcome struct {
		result CrookedBuildResult
		err    error
	}
	outcomes := make(chan outcome, builders)
	for i := 0; i < builders; i++ {
		go func() {
			file, openErr := segy.Open(path)
			if openErr != nil {
				outcomes <- outcome{err: openErr}
				return
			}
			var firstProgress sync.Once
			result, buildErr := BuildCrookedCachedProgress(file, spec, 2, func(_, _ int) {
				firstProgress.Do(func() {
					scanning.Done()
					<-release
				})
			})
			closeErr := file.Close()
			if buildErr == nil {
				buildErr = closeErr
			}
			outcomes <- outcome{result: result, err: buildErr}
		}()
	}
	scanning.Wait()
	close(release)
	for i := 0; i < builders; i++ {
		outcome := <-outcomes
		if outcome.err != nil || outcome.result.Geometry == nil || outcome.result.Geometry.TraceCount() != traceCount {
			t.Fatalf("concurrent build %d failed: result=%+v err=%v", i, outcome.result, outcome.err)
		}
	}

	if leftovers := mustGlob(t, cachePath+".*.tmp"); len(leftovers) != 0 {
		t.Fatalf("concurrent cache build left temporary files: %v", leftovers)
	}
	if info, err := os.Stat(cachePath); err != nil || info.Size() == 0 {
		t.Fatalf("final cache is missing or empty: info=%v err=%v", info, err)
	}

	// Force the verification through the persisted gob rather than the memory
	// cache populated by the concurrent builders.
	crookedMemoryCache.Delete(cachePath)
	file, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, buildErr := BuildCrookedCachedProgress(file, spec, 2, nil)
	closeErr := file.Close()
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if reopened.Geometry == nil || reopened.Geometry.TraceCount() != traceCount || !reopened.Stats.FromCache {
		t.Fatalf("persisted concurrent cache did not reopen: %+v", reopened)
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return matches
}
