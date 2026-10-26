package prestack

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// Opt-in, read-only real-data metadata smoke test. Cache files live in TempDir
// and are removed by the test harness; source samples are never read or copied.
func TestMetadataCacheRealSourceSmoke(t *testing.T) {
	path := os.Getenv("SEISFORGE_PRESTACK_REAL_SEGY")
	if path == "" {
		t.Skip("set SEISFORGE_PRESTACK_REAL_SEGY for read-only real-data metadata smoke")
	}
	t.Setenv("SEISFORGE_PRESTACK_CACHE_BYPASS", "")
	r, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	options := CacheOptions{Directory: t.TempDir()}
	mapping := DefaultHeaderMapping() // explicit mapping, never inferred by cache
	cold, cs, err := BuildIndexCached(context.Background(), r, mapping, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	hit, hs, err := BuildIndexCached(context.Background(), r, mapping, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Hit || !hs.Hit || cs.ScannedHeaders != r.Info.TraceCount || hs.ScannedHeaders != 0 || cs.WriteWarning != "" || hs.WriteWarning != "" {
		t.Fatalf("cache accounting cold=%+v hit=%+v", cs, hs)
	}
	if !reflect.DeepEqual(cold, hit) {
		t.Fatal("real cold/hit metadata or derived index differs")
	}
	t.Logf("read-only PASS traces=%d cold=%s hit=%s fingerprint reads cold/hit=%d/%d cache=%s", len(hit.Records), cs.Duration, hs.Duration, cs.FingerprintReads, hs.FingerprintReads, hs.CachePath)
}
