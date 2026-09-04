//go:build windows

package main

import (
	"os"
	"sort"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func phase12Median(values []time.Duration) time.Duration {
	copyValues := append([]time.Duration(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	return copyValues[len(copyValues)/2]
}

func TestPhase12ProvidedSu36SparseColdLoadComparison(t *testing.T) {
	if os.Getenv("LIMAGE_PHASE12_FULL_ACCEPTANCE") != "1" {
		t.Skip("set LIMAGE_PHASE12_FULL_ACCEPTANCE=1 for Phase 12 sparse performance acceptance")
	}
	root := os.Getenv("SEISFORGE_TEST_CROOKED_PROJECT_DIR")
	if root == "" {
		t.Skip("set SEISFORGE_TEST_CROOKED_PROJECT_DIR to run the external survey acceptance test")
	}
	project, err := projectcore.OpenFolder(dataset.NewManager(), root)
	if err != nil {
		t.Fatal(err)
	}
	if project.ValidLineCount() != 62 {
		t.Fatalf("project has %d valid lines, want 62", project.ValidLineCount())
	}
	legacyDurations, sparseDurations := make([]time.Duration, 0, 5), make([]time.Duration, 0, 5)
	fallbackDurations := make([]time.Duration, 0, 5)
	var sparse phase11AcceptanceMetrics
	for iteration := 0; iteration < 5; iteration++ {
		if iteration%2 == 0 {
			legacy := phase11LoadProvidedTimeWindow(t, project, 1, segy.ReadStrategyCoalesced)
			sparse = phase11LoadProvidedTimeWindow(t, project, 1, segy.ReadStrategySparseMapped)
			legacyDurations, sparseDurations = append(legacyDurations, legacy.duration), append(sparseDurations, sparse.duration)
		} else {
			sparse = phase11LoadProvidedTimeWindow(t, project, 1, segy.ReadStrategySparseMapped)
			legacy := phase11LoadProvidedTimeWindow(t, project, 1, segy.ReadStrategyCoalesced)
			legacyDurations, sparseDurations = append(legacyDurations, legacy.duration), append(sparseDurations, sparse.duration)
		}
		fallback := phase11LoadProvidedTimeWindow(t, project, 1, segy.ReadStrategySparseCoalesced)
		fallbackDurations = append(fallbackDurations, fallback.duration)
	}
	legacyMedian, sparseMedian, fallbackMedian := phase12Median(legacyDurations), phase12Median(sparseDurations), phase12Median(fallbackDurations)
	quarter := phase11LoadProvidedTimeWindow(t, project, .25, segy.ReadStrategySparseMapped)
	t.Logf("Phase12 62-line full range: Phase11 coalesced median=%s samples=%v; mapped sparse median=%s samples=%v; ReadAt sparse median=%s samples=%v; support=%d/%d (%.1f%%), mapped=%d fallback=%d",
		legacyMedian, legacyDurations, sparseMedian, sparseDurations, fallbackMedian, fallbackDurations, sparse.supportTraces, sparse.inputTraces,
		float64(sparse.supportTraces)*100/float64(sparse.inputTraces), sparse.mappedSegments, sparse.fallbackSegments)
	if sparse.inputTraces <= 0 || sparse.supportTraces*100 > sparse.inputTraces*75 {
		t.Fatalf("sparse support plan retained too many traces: %d/%d", sparse.supportTraces, sparse.inputTraces)
	}
	// Recorded immediately before Phase 12 from five Phase 11 runs on this
	// workstation. Keeping the preserved reference separate avoids giving the
	// Phase 11 comparison the new parallel-raster and allocation improvements.
	const phase11ReferenceMedian = 422253600 * time.Nanosecond
	t.Logf("Phase12 target check: recorded Phase11=%s, mapped sparse=%s (%.1f%% change), interleaved Phase11=%s (%.1f%% change), 25%% time window=%s",
		phase11ReferenceMedian, sparseMedian, (1-float64(sparseMedian)/float64(phase11ReferenceMedian))*100,
		legacyMedian, (1-float64(sparseMedian)/float64(legacyMedian))*100, quarter.duration)
}
