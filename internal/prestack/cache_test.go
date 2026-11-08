package prestack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

const cacheTestBypassEnv = "SEISFORGE_PRESTACK_CACHE_BYPASS"

func cacheTestOpen(t *testing.T, path string) *segy.File {
	t.Helper()
	r, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func cacheTestFixture(t *testing.T, options testsegy.Options) (string, *segy.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.sgy")
	if err := testsegy.Write(path, options); err != nil {
		t.Fatal(err)
	}
	return path, cacheTestOpen(t, path)
}

func cacheTestBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func cacheTestWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func cacheTestPlain(t *testing.T, r *segy.File, mapping HeaderMapping) *PrestackIndex {
	t.Helper()
	p, err := BuildIndex(context.Background(), r, mapping, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func cacheTestBuild(t *testing.T, r *segy.File, mapping HeaderMapping, options CacheOptions, hit bool) (*PrestackIndex, IndexBuildStats) {
	t.Helper()
	p, stats, err := BuildIndexCached(context.Background(), r, mapping, options, nil)
	if err != nil || p == nil {
		t.Fatalf("cache build: index=%v stats=%+v err=%v", p != nil, stats, err)
	}
	wantScanned := r.Info.TraceCount
	if hit {
		wantScanned = 0
	}
	// Windows' elapsed clock can legitimately report zero for these tiny
	// fixtures; no wall-clock performance threshold belongs in correctness tests.
	if stats.Hit != hit || stats.ScannedHeaders != wantScanned || stats.Duration < 0 || stats.FingerprintReads <= 0 || stats.FingerprintReads > 192 {
		t.Fatalf("unexpected build statistics: %+v, want hit=%v scanned=%d", stats, hit, wantScanned)
	}
	if !hit && stats.MissReason == "" {
		t.Fatal("cache miss has no diagnostic reason")
	}
	// Success and failure must not transfer ownership of the supplied Reader.
	var header [240]byte
	if err := r.ReadTraceHeaderBytes(0, header[:]); err != nil {
		t.Fatalf("cache closed caller Reader: %v", err)
	}
	if _, err := r.ReadSample(0, 0); err != nil {
		t.Fatalf("cache invalidated caller sample access: %v", err)
	}
	return p, stats
}

func cacheTestNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("cache retained temporary entry %s", entry.Name())
		}
	}
}

func cacheTestEquivalent(t *testing.T, want, got *PrestackIndex) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatal("cache changed index records, warnings, ranges, tables, QC or acquisition metadata")
	}
	for _, kind := range []GatherType{GatherCMP, GatherShot, GatherReceiver, GatherOffset, GatherAzimuth, GatherRaw} {
		keys := want.AvailableGathers(kind)
		if !reflect.DeepEqual(keys, got.AvailableGathers(kind)) {
			t.Fatalf("%s available keys changed", kind)
		}
		selections := []GatherSelection{{Type: kind, Key: GatherKey{All: true}, Secondary: SecondaryPhysical}}
		if kind == GatherRaw {
			selections[0].Key = GatherKey{Raw: true}
		}
		for _, key := range keys {
			selections = append(selections, GatherSelection{Type: kind, Key: key, Secondary: SecondaryOffset})
		}
		if kind != GatherRaw && len(keys) >= 2 {
			selections = append(selections, GatherSelection{Type: kind, Keys: append([]GatherKey(nil), keys[:2]...), Secondary: SecondaryAzimuth})
		}
		for _, selection := range selections {
			a, errA := want.Gather(selection)
			b, errB := got.Gather(selection)
			if errA != nil || errB != nil || !reflect.DeepEqual(a, b) {
				t.Fatalf("%s gather/multikey changed: %v / %v", kind, errA, errB)
			}
			qa, qb := BuildQCReport(want, a), BuildQCReport(got, b)
			ja, errA := MarshalJSONReport(qa)
			jb, errB := MarshalJSONReport(qb)
			if errA != nil || errB != nil || !bytes.Equal(ja, jb) {
				t.Fatalf("%s QC JSON changed", kind)
			}
			ca, errA := MarshalCSVReport(qa)
			cb, errB := MarshalCSVReport(qb)
			if errA != nil || errB != nil || !bytes.Equal(ca, cb) {
				t.Fatalf("%s QC CSV changed", kind)
			}
		}
	}
	for _, kind := range []GatherType{GatherShot, GatherReceiver} {
		points := want.AcquisitionPoints(kind)
		if !reflect.DeepEqual(points, got.AcquisitionPoints(kind)) {
			t.Fatal("geometry points changed")
		}
		for _, point := range points {
			a, okA := want.AcquisitionAssociation(kind, point.Key)
			b, okB := got.AcquisitionAssociation(kind, point.Key)
			if okA != okB || !reflect.DeepEqual(a, b) {
				t.Fatal("geometry association changed")
			}
		}
	}
	for trace := range want.Records {
		a, okA := want.SourceReceiverAssociation(int64(trace))
		b, okB := got.SourceReceiverAssociation(int64(trace))
		if okA != okB || !reflect.DeepEqual(a, b) {
			t.Fatalf("physical trace %d geometry link changed", trace)
		}
	}
}

func cacheTestCompareEquivalent(t *testing.T, plainA, plainB, cachedA, cachedB *PrestackIndex) {
	t.Helper()
	matchA, matchB := MatchRecords(plainA.Records, plainB.Records), MatchRecords(cachedA.Records, cachedB.Records)
	if !reflect.DeepEqual(matchA, matchB) {
		t.Fatal("cache changed Compare matching/classifications")
	}
	filters := CompareFilterSet{Matched: true, AOnly: true, BOnly: true, Ambiguous: true, Invalid: true}
	if !reflect.DeepEqual(FilterCompareRows(matchA, plainA.Records, plainB.Records, filters), FilterCompareRows(matchB, cachedA.Records, cachedB.Records, filters)) {
		t.Fatal("cache changed Compare logical order or physical pairing")
	}
	options := CompareMatchReportOptions{APath: plainA.SourcePath, BPath: plainB.SourcePath, Selection: "all", Primary: "CMP", Secondary: "physical", SampleWindow: SampleWindow{Start: 0, End: 2}, IncludePairs: true}
	options.ARecords, options.BRecords = plainA.Records, plainB.Records
	a := BuildCompareMatchReportWithOptions(matchA, SampleAxisCompatibility{Compatible: true}, options)
	options.ARecords, options.BRecords = cachedA.Records, cachedB.Records
	b := BuildCompareMatchReportWithOptions(matchB, SampleAxisCompatibility{Compatible: true}, options)
	ja, errA := a.JSON()
	jb, errB := b.JSON()
	var ca, cb bytes.Buffer
	errCA, errCB := WriteCompareMatchReportCSV(&ca, a), WriteCompareMatchReportCSV(&cb, b)
	if errA != nil || errB != nil || errCA != nil || errCB != nil || !bytes.Equal(ja, jb) || !bytes.Equal(ca.Bytes(), cb.Bytes()) {
		t.Fatal("cache changed metadata-only Compare JSON/CSV")
	}
}

func TestBuildIndexCachedRealReaderColdHitEquivalence(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	for _, classified := range []bool{false, true} {
		t.Run(fmt.Sprintf("classified_%v", classified), func(t *testing.T) {
			aOptions, bOptions := acceptanceOptions(false, 1, 256, 2000, 10), acceptanceOptions(true, .5, 256, 2000, 10)
			if classified {
				aOptions, bOptions = acceptanceClassifiedOptions()
			}
			aPath, ar := cacheTestFixture(t, aOptions)
			bPath, br := cacheTestFixture(t, bOptions)
			aBefore, bBefore := sha256.Sum256(cacheTestBytes(t, aPath)), sha256.Sum256(cacheTestBytes(t, bPath))
			mapping, options := DefaultHeaderMapping(), CacheOptions{Directory: t.TempDir()}
			plainA, plainB := cacheTestPlain(t, ar, mapping), cacheTestPlain(t, br, mapping)
			coldA, sa := cacheTestBuild(t, ar, mapping, options, false)
			coldB, sb := cacheTestBuild(t, br, mapping, options, false)
			if sa.WriteWarning != "" || sb.WriteWarning != "" || sa.CachePath == sb.CachePath || filepath.Ext(sa.CachePath) != ".pidx" {
				t.Fatalf("cold publication: %+v / %+v", sa, sb)
			}
			warmA, _ := cacheTestBuild(t, cacheTestOpen(t, aPath), mapping, options, true)
			warmB, _ := cacheTestBuild(t, cacheTestOpen(t, bPath), mapping, options, true)
			for _, pair := range [][2]*PrestackIndex{{plainA, coldA}, {plainB, coldB}, {plainA, warmA}, {plainB, warmB}} {
				cacheTestEquivalent(t, pair[0], pair[1])
			}
			cacheTestCompareEquivalent(t, plainA, plainB, warmA, warmB)
			if sha256.Sum256(cacheTestBytes(t, aPath)) != aBefore || sha256.Sum256(cacheTestBytes(t, bPath)) != bBefore {
				t.Fatal("cache changed source SEG-Y bytes")
			}
			cacheTestNoTemps(t, options.Directory)
		})
	}
}

func TestBuildIndexCachedHitUsesBoundedHeaderVerification(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	options := acceptanceOptions(false, 1, 16, 2000, 10)
	options.Cols = 257
	options.SampleValue = nil
	for len(options.TraceHeaders) < options.Cols {
		h := options.TraceHeaders[len(options.TraceHeaders)%60]
		h.SourceID += int32(len(options.TraceHeaders))
		options.TraceHeaders = append(options.TraceHeaders, h)
	}
	path, r := cacheTestFixture(t, options)
	cacheOptions := CacheOptions{Directory: t.TempDir()}
	cacheTestBuild(t, r, DefaultHeaderMapping(), cacheOptions, false)
	_, stats := cacheTestBuild(t, cacheTestOpen(t, path), DefaultHeaderMapping(), cacheOptions, true)
	if stats.FingerprintReads >= r.Info.TraceCount {
		t.Fatalf("hit rescanned the complete source: %+v", stats)
	}
}

func TestBuildIndexCachedPreservesRawMidpointFallback(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	path, r := cacheTestFixture(t, acceptanceOptions(false, 1, 256, 2000, 10))
	_ = r.Close()
	data := cacheTestBytes(t, path)
	binary.BigEndian.PutUint32(data[3600+180:], 1234)
	binary.BigEndian.PutUint32(data[3600+184:], 5678)
	cacheTestWrite(t, path, data)
	r = cacheTestOpen(t, path)
	mapping, options := DefaultHeaderMapping(), CacheOptions{Directory: t.TempDir()}
	var paths []string
	for _, fallback := range []bool{false, true} {
		if fallback {
			mapping.SourceXByte, mapping.SourceYByte, mapping.ReceiverXByte, mapping.ReceiverYByte = 0, 0, 0, 0
		}
		plain := cacheTestPlain(t, r, mapping)
		cold, stats := cacheTestBuild(t, r, mapping, options, false)
		warm, _ := cacheTestBuild(t, r, mapping, options, true)
		paths = append(paths, stats.CachePath)
		cacheTestEquivalent(t, plain, cold)
		cacheTestEquivalent(t, plain, warm)
		if fallback && (warm.Records[0].MidpointX != 1234 || warm.Records[0].MidpointY != 5678 || !warm.Records[0].HasMidpoint) {
			t.Fatal("cached rebuild lost raw CDP XY fallback")
		}
		if !fallback && (warm.Records[0].MidpointX != 0 || warm.Records[0].MidpointY != 0) {
			t.Fatal("raw CDP XY replaced available derived Source/Receiver midpoint")
		}
	}
	if paths[0] == paths[1] {
		t.Fatal("mapping revisions share a cache identity")
	}
}

func TestBuildIndexCachedInvalidation(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	for _, change := range []string{"mapping", "mtime", "size", "header_fingerprint_restored_mtime"} {
		t.Run(change, func(t *testing.T) {
			path, r := cacheTestFixture(t, acceptanceOptions(false, 1, 256, 2000, 10))
			mapping, options := DefaultHeaderMapping(), CacheOptions{Directory: t.TempDir()}
			_, old := cacheTestBuild(t, r, mapping, options, false)
			_ = r.Close()
			st, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "mapping":
				mapping.ReceiverIDByte = 0
			case "mtime":
				if err := os.Chtimes(path, st.ModTime(), st.ModTime().Add(2*time.Second)); err != nil {
					t.Fatal(err)
				}
			case "size":
				data := cacheTestBytes(t, path)
				data = append(data, data[len(data)-(240+256*4):]...)
				cacheTestWrite(t, path, data)
			case "header_fingerprint_restored_mtime":
				data := cacheTestBytes(t, path)
				binary.BigEndian.PutUint32(data[3600+8:], 99999)
				cacheTestWrite(t, path, data)
			}
			if change == "size" || change == "header_fingerprint_restored_mtime" {
				if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			r = cacheTestOpen(t, path)
			got, stats := cacheTestBuild(t, r, mapping, options, false)
			if stats.CachePath == old.CachePath {
				t.Fatal("changed complete identity reused the old cache path")
			}
			cacheTestEquivalent(t, cacheTestPlain(t, r, mapping), got)
			cacheTestBuild(t, r, mapping, options, true)
			cacheTestNoTemps(t, options.Directory)
		})
	}
}

func TestBuildIndexCachedEveryMappingFieldChangesIdentity(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	_, r := cacheTestFixture(t, acceptanceOptions(false, 1, 256, 2000, 10))
	base, options := DefaultHeaderMapping(), CacheOptions{Directory: t.TempDir()}
	_, original := cacheTestBuild(t, r, base, options, false)
	typeOfMapping := reflect.TypeOf(base)
	for field := 0; field < typeOfMapping.NumField(); field++ {
		t.Run(typeOfMapping.Field(field).Name, func(t *testing.T) {
			mapping := base
			value := reflect.ValueOf(&mapping).Elem().Field(field)
			// Keep paired XY/grid fields enabled while moving one word. The
			// fixture's default mapping enables all fields; disabling only one
			// coordinate would be an invalid request, not an identity revision.
			if value.Int() == 1 {
				value.SetInt(5)
			} else {
				value.SetInt(1)
			}
			if err := mapping.Validate(); err != nil {
				t.Fatalf("test mapping revision is invalid: %v", err)
			}
			got, stats := cacheTestBuild(t, r, mapping, options, false)
			if stats.CachePath == original.CachePath {
				t.Fatal("mapping field was omitted from cache identity")
			}
			if !reflect.DeepEqual(cacheTestPlain(t, r, mapping), got) {
				t.Fatal("mapping revision reused another decoded representation")
			}
			cacheTestBuild(t, r, mapping, options, true)
		})
	}
	cacheTestNoTemps(t, options.Directory)
}

func TestBuildIndexCachedCorruptionFallsBackAndRepairs(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	resign := func(b []byte) []byte {
		digest := sha256.Sum256(b[:len(b)-sha256.Size])
		copy(b[len(b)-sha256.Size:], digest[:])
		return b
	}
	for _, kind := range []string{"empty", "truncated", "magic", "payload", "checksum", "trailing", "version", "count_zero_valid_checksum", "count_overflow_valid_checksum", "identity_length_overflow_valid_checksum", "body_length_overflow_valid_checksum"} {
		t.Run(kind, func(t *testing.T) {
			_, r := cacheTestFixture(t, acceptanceOptions(false, 1, 256, 2000, 10))
			mapping, options := DefaultHeaderMapping(), CacheOptions{Directory: t.TempDir()}
			want, old := cacheTestBuild(t, r, mapping, options, false)
			data := cacheTestBytes(t, old.CachePath)
			switch kind {
			case "empty":
				data = nil
			case "truncated":
				data = data[:len(data)/2]
			case "magic":
				data[0] ^= 255
			case "payload":
				data[len(data)/2] ^= 255
			case "checksum":
				data[len(data)-1] ^= 255
			case "trailing":
				data = append(data, 0)
			case "version":
				binary.LittleEndian.PutUint32(data[8:12], math.MaxUint32)
				data = resign(data)
			case "count_zero_valid_checksum":
				binary.LittleEndian.PutUint64(data[20:28], 0)
				data = resign(data)
			case "count_overflow_valid_checksum":
				binary.LittleEndian.PutUint64(data[20:28], math.MaxUint64)
				data = resign(data)
			case "identity_length_overflow_valid_checksum":
				binary.LittleEndian.PutUint32(data[16:20], math.MaxUint32)
				data = resign(data)
			case "body_length_overflow_valid_checksum":
				binary.LittleEndian.PutUint64(data[28:36], math.MaxUint64)
				data = resign(data)
			}
			cacheTestWrite(t, old.CachePath, data)
			got, stats := cacheTestBuild(t, r, mapping, options, false)
			if stats.WriteWarning != "" {
				t.Fatalf("failed to repair cache: %+v", stats)
			}
			cacheTestEquivalent(t, want, got)
			cacheTestBuild(t, r, mapping, options, true)
			cacheTestNoTemps(t, options.Directory)
		})
	}
}

func TestBuildIndexCachedLimitsAndWriteFailuresAreOptional(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	if DefaultCacheMaxFileSize != 1<<30 {
		t.Fatal("default metadata cache ceiling changed from 1 GiB")
	}
	_, r := cacheTestFixture(t, acceptanceOptions(false, 1, 256, 2000, 10))
	mapping := DefaultHeaderMapping()
	want := cacheTestPlain(t, r, mapping)
	t.Run("cache_file_limit_not_source_size", func(t *testing.T) {
		options := CacheOptions{Directory: t.TempDir()}
		_, old := cacheTestBuild(t, r, mapping, options, false)
		before := cacheTestBytes(t, old.CachePath)
		options.MaxFileSize = int64(len(before) - 1)
		got, stats := cacheTestBuild(t, r, mapping, options, false)
		if stats.WriteWarning == "" || !bytes.Equal(before, cacheTestBytes(t, old.CachePath)) {
			t.Fatal("over-limit write replaced a valid entry or lacked warning")
		}
		cacheTestEquivalent(t, want, got)
		cacheTestNoTemps(t, options.Directory)
	})
	t.Run("directory_is_regular_file", func(t *testing.T) {
		blocked := filepath.Join(t.TempDir(), "not-a-directory")
		cacheTestWrite(t, blocked, []byte("preserve me"))
		got, stats := cacheTestBuild(t, r, mapping, CacheOptions{Directory: blocked}, false)
		if stats.WriteWarning == "" || string(cacheTestBytes(t, blocked)) != "preserve me" {
			t.Fatal("unusable cache directory failed source indexing or was modified")
		}
		cacheTestEquivalent(t, want, got)
	})
	t.Run("publication_target_is_directory", func(t *testing.T) {
		options := CacheOptions{Directory: t.TempDir()}
		_, old := cacheTestBuild(t, r, mapping, options, false)
		if err := os.Remove(old.CachePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(old.CachePath, 0700); err != nil {
			t.Fatal(err)
		}
		got, stats := cacheTestBuild(t, r, mapping, options, false)
		if stats.WriteWarning == "" {
			t.Fatal("failed atomic publication has no warning")
		}
		cacheTestEquivalent(t, want, got)
		cacheTestNoTemps(t, options.Directory)
	})
}

func TestBuildIndexCachedBypassDoesNotReadOrWriteCache(t *testing.T) {
	for _, env := range []bool{false, true} {
		t.Run(fmt.Sprintf("environment_%v", env), func(t *testing.T) {
			t.Setenv(cacheTestBypassEnv, "")
			_, r := cacheTestFixture(t, acceptanceOptions(false, 1, 256, 2000, 10))
			mapping, options := DefaultHeaderMapping(), CacheOptions{Directory: t.TempDir()}
			want, old := cacheTestBuild(t, r, mapping, options, false)
			cacheTestWrite(t, old.CachePath, []byte("bad cache must remain untouched"))
			if env {
				t.Setenv(cacheTestBypassEnv, "1")
			} else {
				options.Bypass = true
			}
			got, stats := cacheTestBuild(t, r, mapping, options, false)
			if stats.WriteWarning != "" || string(cacheTestBytes(t, old.CachePath)) != "bad cache must remain untouched" {
				t.Fatal("bypass touched the cache")
			}
			cacheTestEquivalent(t, want, got)
			blocked := filepath.Join(t.TempDir(), "blocked-directory")
			cacheTestWrite(t, blocked, []byte("keep"))
			options.Directory = blocked
			_, stats = cacheTestBuild(t, r, mapping, options, false)
			if stats.WriteWarning != "" {
				t.Fatal("bypass attempted directory I/O")
			}
		})
	}
}

func TestBuildIndexCachedCancellationAndSourceErrorsAreFatal(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	for _, phase := range []string{"before", "scan", "rebuild"} {
		t.Run(phase, func(t *testing.T) {
			_, r := cacheTestFixture(t, acceptanceOptions(false, 1, 256, 2000, 10))
			options := CacheOptions{Directory: t.TempDir()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if phase == "before" {
				cancel()
			}
			called := false
			index, stats, err := BuildIndexCached(ctx, r, DefaultHeaderMapping(), options, func(p IndexProgress) {
				if (phase == "scan" && strings.Contains(p.Stage, "扫描")) || (phase == "rebuild" && p.Scanned == r.Info.TraceCount) {
					called = true
					cancel()
				}
			})
			if index != nil || !errors.Is(err, context.Canceled) || stats.Duration < 0 || (phase != "before" && !called) {
				t.Fatalf("cancelled result: index=%v stats=%+v err=%v callback=%v", index != nil, stats, err, called)
			}
			cacheTestNoTemps(t, options.Directory)
			entries, err := os.ReadDir(options.Directory)
			if err != nil || len(entries) != 0 {
				t.Fatal("cancellation published an entry")
			}
			if _, err := r.ReadSample(0, 0); err != nil {
				t.Fatal("cancellation closed caller Reader")
			}
		})
	}
	t.Run("closed_source_even_with_valid_cache", func(t *testing.T) {
		_, r := cacheTestFixture(t, acceptanceOptions(false, 1, 256, 2000, 10))
		options := CacheOptions{Directory: t.TempDir()}
		_, stats := cacheTestBuild(t, r, DefaultHeaderMapping(), options, false)
		before := cacheTestBytes(t, stats.CachePath)
		_ = r.Close()
		p, _, err := BuildIndexCached(context.Background(), r, DefaultHeaderMapping(), options, nil)
		if err == nil || p != nil || !bytes.Equal(before, cacheTestBytes(t, stats.CachePath)) {
			t.Fatal("source I/O error was hidden as a cache hit/miss")
		}
	})
}

func TestBuildIndexCachedSourceChangeDuringProgressNeverPublishes(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	path, r := cacheTestFixture(t, acceptanceOptions(false, 1, 256, 2000, 10))
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	options := CacheOptions{Directory: t.TempDir()}
	changed := false
	var mutationErr error
	p, _, err := BuildIndexCached(context.Background(), r, DefaultHeaderMapping(), options, func(p IndexProgress) {
		if changed || p.Scanned != r.Info.TraceCount {
			return
		}
		changed = true
		f, openErr := os.OpenFile(path, os.O_RDWR, 0600)
		if openErr != nil {
			mutationErr = openErr
			return
		}
		var sourceID [4]byte
		binary.BigEndian.PutUint32(sourceID[:], 99999)
		_, mutationErr = f.WriteAt(sourceID[:], r.Info.DataStart+8)
		_ = f.Close()
		if mutationErr == nil {
			mutationErr = os.Chtimes(path, st.ModTime(), st.ModTime())
		}
	})
	if mutationErr != nil || !changed || p != nil || !errors.Is(err, segy.ErrSourceChanged) {
		t.Fatalf("source change was not fatal: changed=%v mutation=%v index=%v err=%v", changed, mutationErr, p != nil, err)
	}
	cacheTestNoTemps(t, options.Directory)
	entries, readErr := os.ReadDir(options.Directory)
	if readErr != nil || len(entries) != 0 {
		t.Fatal("source-change failure published a cache")
	}
}

func TestBuildIndexCachedHitStillRejectsCancellationAndSourceErrors(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	for _, failure := range []string{"cancel", "source_changed", "source_closed"} {
		t.Run(failure, func(t *testing.T) {
			path, r := cacheTestFixture(t, acceptanceOptions(false, 1, 256, 2000, 10))
			options := CacheOptions{Directory: t.TempDir()}
			_, old := cacheTestBuild(t, r, DefaultHeaderMapping(), options, false)
			before := cacheTestBytes(t, old.CachePath)
			st, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			called := false
			var callbackErr error
			p, _, err := BuildIndexCached(ctx, r, DefaultHeaderMapping(), options, func(progress IndexProgress) {
				if called || !strings.Contains(progress.Stage, "命中") {
					return
				}
				called = true
				switch failure {
				case "cancel":
					cancel()
				case "source_changed":
					callbackErr = os.Chtimes(path, st.ModTime(), st.ModTime().Add(2*time.Second))
				case "source_closed":
					callbackErr = r.Close()
				}
			})
			if callbackErr != nil || !called || p != nil || err == nil {
				t.Fatalf("cached records hid %s: called=%v callback=%v index=%v err=%v", failure, called, callbackErr, p != nil, err)
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cache hit swallowed cancellation")
			}
			if failure == "source_changed" && !errors.Is(err, segy.ErrSourceChanged) {
				t.Fatal("cache hit swallowed source identity change")
			}
			if !bytes.Equal(before, cacheTestBytes(t, old.CachePath)) {
				t.Fatal("fatal cache-hit failure replaced the existing valid entry")
			}
			cacheTestNoTemps(t, options.Directory)
			if failure != "source_closed" {
				if _, err := r.ReadSample(0, 0); err != nil {
					t.Fatal("cache-hit error closed caller Reader")
				}
			}
		})
	}
}

func TestBuildIndexCachedConcurrentPublication(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	path, r := cacheTestFixture(t, acceptanceOptions(false, 1, 256, 2000, 10))
	want := cacheTestPlain(t, r, DefaultHeaderMapping())
	before := sha256.Sum256(cacheTestBytes(t, path))
	options := CacheOptions{Directory: t.TempDir()}
	type result struct {
		index *PrestackIndex
		stats IndexBuildStats
		err   error
	}
	start, results := make(chan struct{}), make(chan result, 8)
	for range 8 {
		reader := cacheTestOpen(t, path)
		go func() {
			<-start
			p, s, err := BuildIndexCached(context.Background(), reader, DefaultHeaderMapping(), options, nil)
			results <- result{p, s, err}
		}()
	}
	close(start)
	paths := make(map[string]bool)
	var completed []result
	for range 8 {
		completed = append(completed, <-results)
	}
	// Join every worker before a failed assertion can close the test Readers.
	for _, result := range completed {
		if result.err != nil || result.index == nil || result.stats.WriteWarning != "" {
			t.Fatalf("concurrent publication: %+v err=%v", result.stats, result.err)
		}
		paths[result.stats.CachePath] = true
		cacheTestEquivalent(t, want, result.index)
	}
	if len(paths) != 1 {
		t.Fatal("concurrent equal sources published different identities")
	}
	cacheTestBuild(t, r, DefaultHeaderMapping(), options, true)
	cacheTestNoTemps(t, options.Directory)
	entries, err := os.ReadDir(options.Directory)
	if err != nil || len(entries) != 1 || entries[0].IsDir() || filepath.Ext(entries[0].Name()) != ".pidx" {
		t.Fatal("publication did not converge to one complete entry")
	}
	if sha256.Sum256(cacheTestBytes(t, path)) != before {
		t.Fatal("concurrent cache writers modified SEG-Y")
	}
}

func TestBuildIndexCachedDoesNotCacheAmplitudeBody(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	path, r := cacheTestFixture(t, acceptanceOptions(false, 1, 256, 2000, 10))
	options := CacheOptions{Directory: t.TempDir()}
	want, old := cacheTestBuild(t, r, DefaultHeaderMapping(), options, false)
	cacheBefore := cacheTestBytes(t, old.CachePath)
	_ = r.Close()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data := cacheTestBytes(t, path)
	binary.BigEndian.PutUint32(data[3600+240:], math.Float32bits(12345))
	cacheTestWrite(t, path, data)
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	r = cacheTestOpen(t, path)
	got, _ := cacheTestBuild(t, r, DefaultHeaderMapping(), options, true)
	cacheTestEquivalent(t, want, got)
	if sample, err := r.ReadSample(0, 0); err != nil || sample != 12345 {
		t.Fatalf("cache shadowed current source amplitude: sample=%v err=%v", sample, err)
	}
	if !bytes.Equal(cacheBefore, cacheTestBytes(t, old.CachePath)) {
		t.Fatal("sample-body change rewrote metadata cache")
	}
}

func TestBuildIndexCachedPayloadSizeDoesNotScaleWithSamples(t *testing.T) {
	t.Setenv(cacheTestBypassEnv, "")
	path, small := cacheTestFixture(t, acceptanceOptions(false, 1, 16, 2000, 10))
	options := CacheOptions{Directory: t.TempDir()}
	_, smallStats := cacheTestBuild(t, small, DefaultHeaderMapping(), options, false)
	smallCacheSize, smallSourceSize := len(cacheTestBytes(t, smallStats.CachePath)), small.Info.FileSize
	_ = small.Close()
	// Same path and number of trace headers, but 256 times as many samples.
	// The identity and fixed header records have the same encoded lengths.
	if err := testsegy.Write(path, acceptanceOptions(false, 1, 4096, 2000, 10)); err != nil {
		t.Fatal(err)
	}
	large := cacheTestOpen(t, path)
	_, largeStats := cacheTestBuild(t, large, DefaultHeaderMapping(), options, false)
	if large.Info.FileSize <= 16*smallSourceSize || len(cacheTestBytes(t, largeStats.CachePath)) != smallCacheSize {
		t.Fatal("metadata cache payload scales with source amplitude samples")
	}
	cacheTestBuild(t, large, DefaultHeaderMapping(), options, true)
	cacheTestNoTemps(t, options.Directory)
}
