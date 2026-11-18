//go:build windows

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

func TestCompareDataInfoAlignsRegionalAndSingleTraceByAbsoluteTime(t *testing.T) {
	ctx := context.Background()
	oldData, oldFirst, oldLast, oldStep := compareTD, compareTraceStart, compareTraceEnd, compareTraceStep
	t.Cleanup(func() {
		compareTD, compareTraceStart, compareTraceEnd, compareTraceStep = oldData, oldFirst, oldLast, oldStep
	})
	compareTraceStart, compareTraceEnd, compareTraceStep = 0, -1, 1
	const il, xl int32 = 1000, 2000 // trace 0 in the shared 2×8 fixture
	type fixture struct {
		reader   *segy.File
		geometry *segy.GeometryIndex
	}
	open := func(t *testing.T, originMS, intervalUS int, amp float64) fixture {
		t.Helper()
		path := filepath.Join(t.TempDir(), "trace.sgy")
		writeTraceScientificFixture(t, path, 1)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		binary.BigEndian.PutUint16(data[3600+108:], uint16(originMS))
		binary.BigEndian.PutUint16(data[3600+116:], uint16(intervalUS))
		for k := range 351 {
			// The same absolute sample has A=2B although each file has a different local index.
			v := amp * float64(1+(originMS-750)/2+k)
			binary.BigEndian.PutUint32(data[3600+240+4*k:], math.Float32bits(float32(v)))
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		f, err := segy.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		g, _, err := f.BuildGeometryIndexCachedContextProgress(ctx, 189, 193, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := g.TraceAt(il, xl); !ok || got != 0 {
			t.Fatalf("fixture geometry: trace=%d valid=%v", got, ok)
		}
		return fixture{f, g}
	}
	a := open(t, 800, 2000, 2)
	for _, tc := range []struct{ originMS, firstMS, pairs int }{{750, 900, 151}, {850, 900, 151}, {1100, 1100, 51}} {
		t.Run(fmt.Sprintf("B origin %d ms", tc.originMS), func(t *testing.T) {
			b := open(t, tc.originMS, 2000, 1)
			compareTD = &compareTimeData{fa: a.reader, fb: b.reader, ga: a.geometry, gb: b.geometry}
			key := compareInfoKey{view: viewFilterKey{b: b.reader.Info.Path, line: il, first: 50, last: 200,
				x0: math.Float64bits(float64(xl)), x1: math.Float64bits(float64(xl))}}
			region, err := computeCompareInfo(ctx, captureCompareInfo(key))
			want := fmt.Sprintf("1 道 · %d 个有效样点 · 实取 %d–1200 ms", tc.pairs, tc.firstMS)
			if err != nil || !strings.Contains(region.detail, want) || !strings.Contains(region.caption, "0.00 dB") {
				t.Fatalf("regional capture clipped B by A indices: want %q and 0 dB, got %q / %q (%v)", want, region.detail, region.caption, err)
			}
			sel := traceAnalysisSelection{Anchor: tracePoststackAnchor{Owner: 1}, CompareGroup: true, Difference: true,
				Targets: []traceAnalysisTarget{{Role: "A", Path: a.reader.Info.Path}, {Role: "B", Path: b.reader.Info.Path}}}
			scope := traceAnalysisScope{traceAnalysisManual, 900_000, 1_200_000}
			single := buildTraceAnalysisResult(1, freezeTraceAnalysisInScope(sel, scope), false).snapshot
			if single.ReferenceSNR.State != segyanalysis.SNRFinite || single.ReferenceSNR.Pairs != int64(tc.pairs) || math.Abs(single.ReferenceSNR.DB) > 1e-8 || single.ComparisonStartUS != int64(tc.firstMS)*1000 || single.ComparisonOverlapSamples != tc.pairs || region.caption != referenceSNRLabel+"："+ReferenceSNRText(single.ReferenceSNR, "") {
				t.Fatalf("single trace and regional reference differ: %+v / %q", single.ReferenceSNR, region.caption)
			}
			t.Logf("%s; %s; single-trace result agrees to 1e-8 dB", want, region.caption)
		})
	}
	t.Run("B declared trace clock differs from its binary header", func(t *testing.T) {
		b := open(t, 800, 3000, 1)
		if b.reader.Info.SampleIntervalUS != 2000 {
			t.Fatalf("fixture binary dt = %d; expected 2000 µs", b.reader.Info.SampleIntervalUS)
		}
		compareTD = &compareTimeData{fa: a.reader, fb: b.reader, ga: a.geometry, gb: b.geometry}
		key := compareInfoKey{view: viewFilterKey{b: b.reader.Info.Path, line: il, first: 50, last: 200,
			x0: math.Float64bits(float64(xl)), x1: math.Float64bits(float64(xl))}}
		region, err := computeCompareInfo(ctx, captureCompareInfo(key))
		if err != nil || !strings.Contains(region.caption, "2000 / 3000 μs") {
			t.Fatalf("regional mismatch not reported: %q (%v)", region.caption, err)
		}
		sel := traceAnalysisSelection{Anchor: tracePoststackAnchor{Owner: 1}, CompareGroup: true, Difference: true,
			Targets: []traceAnalysisTarget{{Role: "A", Path: a.reader.Info.Path}, {Role: "B", Path: b.reader.Info.Path}}}
		scope := traceAnalysisScope{traceAnalysisManual, 900_000, 1_200_000}
		single := buildTraceAnalysisResult(1, freezeTraceAnalysisInScope(sel, scope), false)
		d := single.snapshot
		// B's 3-ms lattice includes 902..1199 ms: 100 samples. Retain each
		// waveform but do not construct a difference or SNR between clocks.
		if len(d.Series) != 2 || d.ReferenceSNR.State != segyanalysis.SNRUnavailable || d.ReferenceSNR.Pairs != 0 ||
			d.Series[1].StartUS != 902_000 || d.Series[1].IntervalUS != 3000 || len(d.Series[1].Samples) != 100 || !strings.Contains(single.diffNote, "2000 / 3000 μs") {
			t.Fatalf("single trace hid the B clock mismatch: %+v, %q", d.ReferenceSNR, single.diffNote)
		}
		t.Log("A 2-ms and B 3-ms series retained; both entries reject comparison without resampling")
	})
}

func TestCompareDataInfoSharesSingleTraceReferenceAndHonorsRealCoverage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "Regional_A.sgy"), filepath.Join(dir, "Regional_B_half.sgy")
	writeTraceScientificFixture(t, a, 1)
	writeTraceScientificFixture(t, b, .5)
	var sources [2]compareInfoSource
	for i, path := range [2]string{a, b} {
		f, err := segy.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		g, _, err := f.BuildGeometryIndexCachedContextProgress(ctx, 189, 193, 1, nil)
		identity, idErr := f.SourceFileInfo()
		_ = f.Close()
		if err != nil || idErr != nil {
			t.Fatalf("fixture geometry/identity: %v %v", err, idErr)
		}
		sources[i] = compareInfoSource{path: path, geometry: g, identity: identity, physical: [3]int64{0, 15, 1}, window: [2]int{0, 350}}
	}
	const il, xl int32 = 1001, 2003
	const want = "共同配对：1 道 · 351 个有效样点"
	r := compareInfoRequest{geometry: true, sources: sources, key: compareInfoKey{view: viewFilterKey{b: b, line: il, x0: math.Float64bits(float64(xl)), x1: math.Float64bits(float64(xl))}}}
	got, err := computeCompareInfo(ctx, r)
	if err != nil || !strings.Contains(got.detail, want) || !strings.Contains(got.detail, "实取 800–1500 ms") {
		t.Fatalf("scoped trace not used: %+v (%v)", got, err)
	}
	trace, ok := sources[0].geometry.TraceAt(il, xl)
	if !ok {
		t.Fatal("fixture trace missing")
	}
	selection := traceAnalysisSelection{Anchor: tracePoststackAnchor{Owner: 1}, CompareGroup: true, Difference: true,
		Targets: []traceAnalysisTarget{{Role: "A", Path: a, Trace: trace, SampleEnd: 350}, {Role: "B", Path: b, Trace: trace, SampleEnd: 350}}}
	single := buildTraceAnalysisResult(1, selection, false).snapshot.ReferenceSNR
	if single.State != segyanalysis.SNRFinite || math.Abs(single.DB) > 1e-6 || got.caption != referenceSNRLabel+"："+ReferenceSNRText(single, "") {
		t.Fatalf("regional SNR differs from the same selected A/B samples: %s (%+v)", got.caption, single)
	}
	r.sources[1].window = [2]int{50, 99}
	got, err = computeCompareInfo(ctx, r)
	if err != nil || !strings.Contains(got.detail, "1 道 · 50 个有效样点 · 实取 900–998 ms") {
		t.Fatalf("local B was expanded or shifted: %+v (%v)", got, err)
	}
	r.geometry = false
	got, err = computeCompareInfo(ctx, r)
	if err != nil || !strings.Contains(got.caption, "缺少可核实的共同几何") || !strings.Contains(got.detail, "0 道 · 0 个有效样点") {
		t.Fatalf("unverified geometry was used for SNR: %+v (%v)", got, err)
	}
}
