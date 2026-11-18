//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestDisplayDistanceReadsScalarAndDirectionFromTraceHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "distance.sgy")
	coords := []testsegy.TraceCoordinate{
		{X: 1000, Y: 2000}, {X: 1250, Y: 2000}, {X: 1500, Y: 2000},
		{X: 1000, Y: 2400}, {X: 1250, Y: 2400}, {X: 1500, Y: 2400},
	}
	if err := testsegy.Write(path, testsegy.Options{Rows: 2, Cols: 3, RegularGrid: true, Coordinates: coords, CoordinateScalar: -10, CoordinateUnits: 1}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	word := make([]byte, 2)
	binary.BigEndian.PutUint16(word, 2) // binary measurement system: feet
	if _, err := f.WriteAt(word, 3254); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	g := &segy.GeometryIndex{
		TraceCount: 6, InlineValues: []int32{1000, 1001}, CrosslineValues: []int32{2000, 2001, 2002},
		TraceNumbers: []int64{0, 1, 2, 3, 4, 5}, RowOfTrace: []int32{0, 0, 0, 1, 1, 1},
		ColOfTrace: []int32{0, 1, 2, 0, 1, 2}, ValidTraceCount: 6, Poststack: true,
	}
	for _, tc := range []struct {
		orientation int
		line        int32
		want        float64
	}{{0, 1001, 25 * 0.3048}, {1, 2001, 40 * 0.3048}} {
		got := readDisplayDistanceGeometry(context.Background(), path, g, tc.orientation, tc.line, 6, nil)
		if got == nil || math.Abs(got.GridSpacingM()-tc.want) > 1e-12 {
			t.Fatalf("orientation %d spacing %.12g; want %.12g", tc.orientation, got.GridSpacingM(), tc.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if readDisplayDistanceGeometry(ctx, path, g, 1, 2000, 6, nil) != nil {
		t.Fatal("cancelled metadata task accepted a result")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("distance metadata changed the source: %v", err)
	}
}

func TestDisplayDistanceExplicitSpacingHasPriorityAndRequiresUnits(t *testing.T) {
	headers := []segy.TextHeader{{Lines: []string{
		"C19 INLINE SPACING: 50 FT", "C20 CROSSLINE SPACING = 25 M",
		"C21 CROSSLINE SPACING: 42", "C22 INLINE SPACING: -12 m",
	}}}
	if value, ok := displayDeclaredSpacing(headers, 0); !ok || value != 25 {
		t.Fatalf("crossline direction spacing = %v, %t", value, ok)
	}
	if value, ok := displayDeclaredSpacing(headers, 1); !ok || value != 15.24 {
		t.Fatalf("inline direction spacing = %v, %t", value, ok)
	}
	headers[0].Lines = append(headers[0].Lines, "C23 CROSSLINE SPACING: 30 metres")
	if _, ok := displayDeclaredSpacing(headers, 0); ok {
		t.Fatal("conflicting text header spacing should not be used")
	}
	path := filepath.Join(t.TempDir(), "text-spacing.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 2, Cols: 3, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	card := []byte(fmt.Sprintf("%-80s", "C01 CROSSLINE SPACING: 25 M"))
	_, err = f.WriteAt(card, 0)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("write fixture card: %v / %v", err, closeErr)
	}
	g := &segy.GeometryIndex{
		TraceCount: 6, InlineValues: []int32{1000, 1001}, CrosslineValues: []int32{2000, 2001, 2002},
		TraceNumbers: []int64{0, 1, 2, 3, 4, 5}, RowOfTrace: []int32{0, 0, 0, 1, 1, 1},
		ColOfTrace: []int32{0, 1, 2, 0, 1, 2}, ValidTraceCount: 6, Poststack: true,
	}
	var source string
	r := readDisplayDistanceReference(context.Background(), path, g, 0, 1000, 6, nil, &source)
	if r == nil || r.GridSpacingM() != 25 || source != "来自数据（文本卷头方向间距）" {
		t.Fatalf("reopened directional text card: spacing=%v source=%q", r.GridSpacingM(), source)
	}
}

func TestDisplayDistanceCompareKeepsAAnchorAndVoids(t *testing.T) {
	oldWindow, oldMode, oldTD, oldA, oldB := compareHwnd, compareMode, compareTD, compareA, compareB
	const owner = uintptr(0x5391)
	compareHwnd, compareMode = owner, 0
	f := &segy.File{Info: segy.Info{Path: "synthetic-distance.sgy", TraceCount: 6}}
	compareTD = &compareTimeData{fa: f, traceOrderFallback: true}
	compareA.coords, compareB.coords = []int32{1, 2, 3, 4, 5, 6}, []int32{3, 4, 6}
	defer func() {
		displayCloseDistance(owner)
		compareHwnd, compareMode, compareTD, compareA, compareB = oldWindow, oldMode, oldTD, oldA, oldB
	}()
	if err := displaySetDistanceSettings(owner, displayDistanceSettings{Enabled: true, Manual: true, SpacingM: 20}); err != nil {
		t.Fatal(err)
	}
	if f, ok := displayCompareDistanceFraction(0, 3); !ok || f != 0.4 {
		t.Fatalf("B origin in A = %v, %v", f, ok)
	}
	if x, ok := displayCompareNativeAtFraction(1, 0); !ok || x != 3 {
		t.Fatalf("B local left edge = %v, %v", x, ok)
	}
	if displayCompareDistancePixelAllowed(1, 5) || !displayCompareDistancePixelAllowed(1, 4) {
		t.Fatal("distance mode filled an absent B trace")
	}
	compareB.coords = []int32{0, 3, 7}
	bounds, present := displayCompareVisibleDistanceBounds(1)
	if !present || bounds.Min != 1 || bounds.Max != 6 {
		t.Fatalf("B coverage was not clipped to A: %+v, %v", bounds, present)
	}
	compareB.coords = []int32{7, 8}
	if _, present := displayCompareVisibleDistanceBounds(1); present {
		t.Fatal("unrelated B coverage was placed inside A")
	}
	x := displaycore.Axis{ID: "crossline", Data: displaycore.Range{Min: 1, Max: 6}, View: displaycore.Range{Min: 3, Max: 6}}
	x = displayDistanceAxis(owner, x)
	if x.ID != "crossline" || x.Unit != "m" || x.Data.Min != 0 || x.View.Min != 40 || x.View.Max != 100 {
		t.Fatalf("crop reset A origin: %+v", x)
	}
}

func TestDisplayDistanceRawCompareClipsLocalBToItsPhysicalCentres(t *testing.T) {
	oldOwner, oldMode, oldTD, oldA, oldB, oldSF := compareHwnd, compareMode, compareTD, compareA, compareB, sf
	oldAPath, oldPath, oldVisible := compareAPath, compareBPath, compareBVisible
	const owner = uintptr(0x5392)
	compareHwnd, compareMode, compareTD, compareAPath, compareBPath, compareBVisible = owner, 0, nil, "a.sgy", "b.sgy", true
	sf = &segy.File{Info: segy.Info{Path: "a.sgy", TraceCount: 6}}
	compareA = comparePanel{bgra: []byte{1}, stats: segy.RenderStats{TraceStart: 0, TraceEnd: 5, TraceStep: 1}}
	compareB = comparePanel{bgra: []byte{1}, stats: segy.RenderStats{TraceStart: 2, TraceEnd: 5, TraceStep: 2}}
	defer func() {
		displayCloseDistance(owner)
		compareHwnd, compareMode, compareTD, compareA, compareB, sf = oldOwner, oldMode, oldTD, oldA, oldB, oldSF
		compareAPath, compareBPath, compareBVisible = oldAPath, oldPath, oldVisible
	}()
	if err := displaySetDistanceSettings(owner, displayDistanceSettings{Enabled: true, Manual: true, SpacingM: 20}); err != nil {
		t.Fatal(err)
	}
	// A is 0–100 m. B's real stride centres are 40 and 80 m, not 100 m.
	if lo, hi, ok := seismicComparePartial(1, false); !ok || lo != .4 || hi != .8 {
		t.Fatalf("raw B coverage = %g–%g, valid=%t", lo, hi, ok)
	}
	if r := seismicCompareRasterRect(1, RECT{Right: 101, Bottom: 100}); r.Left != 40 || r.Right != 81 {
		t.Fatalf("raw B was stretched beyond its coverage: %+v", r)
	}
}

func TestDisplayDistanceWarpDoesNotChangeSourceRaster(t *testing.T) {
	in := []byte{15, 15, 15, 255, 85, 85, 85, 255, 204, 204, 204, 255}
	before := append([]byte(nil), in...)
	out := displayDistanceWarp(in, 3, 1, func(f float64) (float64, bool) { return f, f != 0.5 })
	if !bytes.Equal(before, in) || !bytes.Equal(out, []byte{15, 15, 15, 255, 255, 255, 255, 255, 204, 204, 204, 255}) {
		t.Fatalf("source %v, projected %v", in, out)
	}
}
