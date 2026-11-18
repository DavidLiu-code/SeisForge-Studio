//go:build windows

package main

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func compareSlotReader(t *testing.T) *segy.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compare_slots.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 6, Samples: 4, SampleValue: func(trace, sample int) float32 {
		return float32(trace*100 + sample + 1)
	}}); err != nil {
		t.Fatal(err)
	}
	reader, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return reader
}

func compareMixedSlotRows() []prestackcore.CompareLogicalRow {
	return []prestackcore.CompareLogicalRow{
		{Classification: prestackcore.CompareRowMatched, ATrace: 4, BTrace: 1, HasA: true, HasB: true},
		{Classification: prestackcore.CompareRowAOnly, ATrace: 0, HasA: true},
		{Classification: prestackcore.CompareRowMatched, ATrace: 1, BTrace: 4, HasA: true, HasB: true},
		{Classification: prestackcore.CompareRowBOnly, BTrace: 2, HasB: true},
		{Classification: prestackcore.CompareRowAmbiguous, ATrace: 3, HasA: true},
		{Classification: prestackcore.CompareRowInvalid, BTrace: 5, HasB: true},
	}
}

func TestPrestackCompareMixedSlotsRealReaderPreservesPhysicalMapping(t *testing.T) {
	reader := compareSlotReader(t)
	rows := compareMixedSlotRows()
	if prestackCompareUniformSides(rows) {
		t.Fatal("mixed classifications must not use compact dense sides")
	}
	for _, width := range []int{3, 6, 31} {
		opts := segy.RenderOptions{Width: width, Height: 4, SampleStart: 0, SampleEnd: 3, DisplayMode: segy.DisplayAdaptive, ReadStrategy: segy.ReadStrategySparseMapped}
		a, err := renderPrestackCompareSideValues(context.Background(), reader, rows, false, false, opts)
		if err != nil {
			t.Fatal(err)
		}
		b, err := renderPrestackCompareSideValues(context.Background(), reader, rows, true, false, opts)
		if err != nil {
			t.Fatal(err)
		}
		delta, err := prestackCompareDifferenceSlots(a, b, rows, width, opts.Height)
		if err != nil {
			t.Fatal(err)
		}
		pixels := prestackCompareValuesBGRA(b, 0, 600, 2)
		for y := 0; y < opts.Height; y++ {
			for x := 0; x < width; x++ {
				row := rows[prestackCompareLogicalColumn(x, width, len(rows))]
				i := y*width + x
				for side, raster := range [][]float64{a, b} {
					trace, present := row.ATrace, row.HasA
					if side == 1 {
						trace, present = row.BTrace, row.HasB
					}
					if !present {
						if !math.IsNaN(raster[i]) {
							t.Fatalf("side %d missing slot became trace at %d,%d", side, x, y)
						}
					} else if want := float64(trace*100 + int64(y) + 1); raster[i] != want {
						t.Fatalf("width=%d side=%d column=%d wrong physical trace: value=%v want=%v", width, side, x, raster[i], want)
					}
				}
				if row.Classification == prestackcore.CompareRowMatched {
					if delta[i] != float64((row.ATrace-row.BTrace)*100) {
						t.Fatalf("wrong float Delta at column %d: %v", x, delta[i])
					}
				} else if !math.IsNaN(delta[i]) {
					t.Fatal("non-matched slot received a Delta value")
				}
				if !row.HasB && !reflect.DeepEqual(pixels[i*4:i*4+4], []byte{255, 255, 255, 255}) {
					t.Fatal("missing B side must be blank rather than zero amplitude")
				}
			}
		}
	}
}

func TestPrestackCompareSlotsRetainDenseAdaptiveAndSingleSide(t *testing.T) {
	reader := compareSlotReader(t)
	rows := []prestackcore.CompareLogicalRow{
		{Classification: prestackcore.CompareRowMatched, ATrace: 4, BTrace: 1, HasA: true, HasB: true},
		{Classification: prestackcore.CompareRowMatched, ATrace: 0, BTrace: 2, HasA: true, HasB: true},
		{Classification: prestackcore.CompareRowMatched, ATrace: 3, BTrace: 5, HasA: true, HasB: true},
	}
	opts := segy.RenderOptions{Width: 31, Height: 21, SampleStart: 1, SampleEnd: 2, DisplayMode: segy.DisplayAdaptive, ReadStrategy: segy.ReadStrategySparseMapped}
	if !prestackCompareUniformSides(rows) {
		t.Fatal("complete matched rows lost dense Adaptive path")
	}
	got, err := renderPrestackCompareSideValues(context.Background(), reader, rows, false, true, opts)
	want, _, expectedErr := reader.RenderTraceIndicesValues([]int64{4, 0, 3}, opts)
	if err != nil || expectedErr != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("all-matched rendering changed: %v / %v", err, expectedErr)
	}
	// Selecting only B-only must not repopulate A with the unfiltered A gather.
	rows = []prestackcore.CompareLogicalRow{{Classification: prestackcore.CompareRowBOnly, BTrace: 2, HasB: true}}
	blank, err := renderPrestackCompareSideValues(context.Background(), nil, rows, false, true, opts)
	if err != nil || len(blank) != opts.Width*opts.Height {
		t.Fatalf("blank A side invalid: %v", err)
	}
	for _, value := range blank {
		if !math.IsNaN(value) {
			t.Fatal("B-only selection incorrectly rendered A")
		}
	}
	if prestackCompareHasMatchedSlot(rows, opts.Width) {
		t.Fatal("B-only selection must not render Delta")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := renderPrestackCompareSideValues(ctx, reader, rows, true, true, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled slot render proceeded: %v", err)
	}
}

func TestPrestackCompareSupportBoundedAndDeterministic(t *testing.T) {
	rows := make([]prestackcore.CompareLogicalRow, 68432)
	for i := range rows {
		rows[i] = prestackcore.CompareLogicalRow{ATrace: int64(i), BTrace: int64(len(rows) - 1 - i), HasA: true, HasB: i%3 != 0}
	}
	const width = 1274
	for _, sideB := range []bool{false, true} {
		traces, columns := prestackCompareSideSupport(rows, sideB, width)
		if len(traces) > width || len(columns) != width {
			t.Fatalf("read support escaped screen bound: traces=%d columns=%d", len(traces), len(columns))
		}
		seen := make(map[int64]bool)
		for _, trace := range traces {
			if seen[trace] {
				t.Fatal("duplicate physical trace sent to sparse renderer")
			}
			seen[trace] = true
		}
		again, againColumns := prestackCompareSideSupport(rows, sideB, width)
		if !reflect.DeepEqual(traces, again) || !reflect.DeepEqual(columns, againColumns) {
			t.Fatal("slot support is not deterministic")
		}
	}
	if prestackCompareLogicalColumn(0, width, len(rows)) != 0 || prestackCompareLogicalColumn(width-1, width, len(rows)) != len(rows)-1 {
		t.Fatal("first/last physical support was lost")
	}
}
