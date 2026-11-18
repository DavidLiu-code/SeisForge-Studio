//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestCompareGeometryOneTraceAndSwapPreserveIdentity(t *testing.T) {
	const ilByte, xlByte = 21, 189
	path := filepath.Join(t.TempDir(), "single.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 1, Samples: 48, RegularGrid: true, InlineByte: ilByte, CrosslineByte: xlByte}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for i, m := range []compareHeaderMapping{{ilByte, xlByte}, {xlByte, ilByte}, {ilByte, xlByte}} {
		g, stats, err := f.BuildPoststackSectionGeometryIndexContextProgress(context.Background(), m.il, m.xl, 1, nil)
		if err != nil || !g.Poststack || g.ValidTraceCount != 1 || stats.CachePath != "" {
			t.Fatalf("measured single trace rejected/cached: %+v, %+v, %v", g, stats, err)
		}
		il, xl, ok := compareGeometryAtTrace(g, 0)
		if !ok || il+xl != 3000 || il != []int32{1000, 2000, 1000}[i] {
			t.Fatalf("swap changed physical identity: %d %d %t", il, xl, ok)
		}
		b, err := poststack.GeometryForTraceSelection("result.sfdata", g, []int64{0})
		if err != nil {
			t.Fatal(err)
		}
		if trace, err := compareExactNavigationPosition(b, il, xl); err != nil || trace != 0 {
			t.Fatalf("B did not inherit the source trace's real geometry: %d %v", trace, err)
		}
		if _, err := compareExactNavigationPosition(b, il, xl+1); err == nil {
			t.Fatal("missing native geometry position treated as a real trace")
		}
	}
	_, _, err = f.BuildGeometryIndexCached(21, 189, 1)
	if err == nil {
		t.Fatal("legacy 3D volume accepted a cached one-trace geometry")
	}
	var values []float64
	_, _, err = segy.RenderTraceDifferencePairsWindowed(f, f, []int64{0}, []int64{0}, segy.RenderOptions{
		Width: 2, Height: 48, SampleStart: 0, SampleEnd: 47, CaptureDisplayValues: func(v []float64) { values = v },
	}, 0)
	if err != nil || len(values) != 96 {
		t.Fatalf("single-pair delta did not render: %d %v", len(values), err)
	}
	for _, v := range values {
		if v != 0 {
			t.Fatalf("identical single traces produced delta %g", v)
		}
	}
	after, _ := os.ReadFile(path)
	if !reflect.DeepEqual(contents, after) {
		t.Fatal("source SEG-Y changed during geometry interpretation")
	}
}

func TestCompareGeometryExactNoncontiguousAndUniquePairing(t *testing.T) {
	g := &segy.GeometryIndex{InlineValues: []int32{10, 30}, CrosslineValues: []int32{-4, 8, 27},
		TraceNumbers: []int64{4, 7, 9}, RowOfTrace: []int32{0, 1, 1}, ColOfTrace: []int32{0, 1, 2}, Poststack: true}
	if n, err := compareExactNavigationPosition(g, 30, 27); n != 9 || err != nil {
		t.Fatalf("failed exact sparse navigation: %d %v", n, err)
	}
	if _, err := compareExactNavigationPosition(g, 20, 8); err == nil || !strings.Contains(err.Error(), "10 / 30") {
		t.Fatalf("missing index selected another line or hid neighbors: %v", err)
	}
	if _, err := compareExactNavigationPosition(g, 10, 27); err == nil {
		t.Fatal("sparse grid treated a hole as valid")
	}
	a, b, c := pairLineTracesByCoord([]int64{2, 3, 4}, []int32{5, 7, 9}, []int64{12}, []int32{7})
	if !reflect.DeepEqual(a, []int64{3}) || !reflect.DeepEqual(b, []int64{12}) || !reflect.DeepEqual(c, []int32{7}) {
		t.Fatalf("one B trace was broadcast or dropped: %v %v %v", a, b, c)
	}
	a, _, _ = pairLineTracesByCoord([]int64{2, 3}, []int32{7, 7}, []int64{12}, []int32{7})
	if len(a) != 0 {
		t.Fatal("ambiguous repeated positions paired")
	}
	only, _ := filterLineRange([]int64{2, 3, 4}, []int32{5, 7, 9}, 7, 7)
	if !reflect.DeepEqual(only, []int64{3}) {
		t.Fatalf("exact equal bounds didn't select one real trace: %v", only)
	}
}

func TestCompareGeometryMappingsAreIndependentAndOpeningMappingIsRetained(t *testing.T) {
	original := compareHeaderMapping{189, 21}
	other := compareHeaderMapping{17, 25}
	var a, b compareGeometrySourceMapping
	saveCompareSourceMapping(&a, "A.sgy", original)
	saveCompareSourceMapping(&b, "B.sgy", other)
	saveCompareSourceMapping(&a, "A.sgy", original.swap())
	if a.opened != original || b.applied != other || a.applied.swap() != original {
		t.Fatalf("swap changed opening or independent mapping: %+v %+v", a, b)
	}
	if _, ok := compareSourceMapping("another.sgy", b); ok {
		t.Fatal("geometry configuration leaked to a different source")
	}
	for _, pair := range [][2]string{{"", "193"}, {"189", "189"}, {"238", "193"}, {"10.2", "193"}} {
		if _, err := parseCompareHeaderMapping(pair[0], pair[1]); err == nil {
			t.Fatalf("accepted malformed mapping %v", pair)
		}
	}
	if _, err := parseCompareHeaderMapping(" 1 ", "237"); err != nil {
		t.Fatalf("valid 1-based boundary rejected: %v", err)
	}
}

func TestCompareGeometryClampsOnlyGeneratedTimeCoverage(t *testing.T) {
	for _, c := range []struct {
		first, last         int
		a, offset, b        int
		wantFirst, wantLast int
		ok                  bool
	}{
		{0, -1, 100, 24, 50, 24, 73, true},
		{60, 65, 100, 24, 50, 60, 65, true},
		{10, 15, 100, 24, 50, 24, 15, false},
		{88, 99, 100, 24, 50, 88, 73, false},
	} {
		lo, hi, ok := compareSourceSampleOverlap(c.first, c.last, c.a, c.offset, c.b)
		if lo != c.wantFirst || hi != c.wantLast || ok != c.ok {
			t.Fatalf("time coverage %+v -> %d..%d (%v)", c, lo, hi, ok)
		}
	}
}

func TestCompareGeometryNavigationNumbersAreSeparateFromHeaderBytes(t *testing.T) {
	il, xl, err := parseCompareNavigationNumbers(" -2003 ", "582144")
	if err != nil || il != -2003 || xl != 582144 {
		t.Fatalf("real line numbers treated as header offsets: %d %d %v", il, xl, err)
	}
	for _, pair := range [][2]string{{"", "1"}, {"1.2", "2"}, {"2147483648", "7"}} {
		if _, _, err := parseCompareNavigationNumbers(pair[0], pair[1]); err == nil {
			t.Errorf("accepted invalid line numbers %v", pair)
		}
	}
}
