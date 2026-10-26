//go:build windows

package main

import (
	"context"
	"fmt"
	"math"

	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// prestackCompareLogicalColumn is the endpoint-inclusive screen/logical mapping
// used by the raster and interaction paths. Missing sides keep their slot.
func prestackCompareLogicalColumn(x, width, count int) int {
	if count < 1 || width < 1 {
		return -1
	}
	if width == 1 || count == 1 {
		return 0
	}
	return clampInt(int(math.Round(float64(clampInt(x, 0, width-1))*float64(count-1)/float64(width-1))), 0, count-1)
}

func prestackCompareHasSide(rows []prestackcore.CompareLogicalRow, sideB bool) bool {
	for _, row := range rows {
		if (!sideB && row.HasA) || (sideB && row.HasB) {
			return true
		}
	}
	return false
}

func prestackCompareHasMatchedSlot(rows []prestackcore.CompareLogicalRow, width int) bool {
	for x := 0; x < width; x++ {
		i := prestackCompareLogicalColumn(x, width, len(rows))
		if i >= 0 && rows[i].Classification == prestackcore.CompareRowMatched && rows[i].HasA && rows[i].HasB {
			return true
		}
	}
	return false
}

// Preserve the original Adaptive renderer for complete aligned sides. Mixed
// classifications need explicit slots; interpolating over a missing side would
// fabricate a trace and make the displayed waveform disagree with picking.
func prestackCompareUniformSides(rows []prestackcore.CompareLogicalRow) bool {
	if len(rows) == 0 {
		return false
	}
	for _, row := range rows[1:] {
		if row.HasA != rows[0].HasA || row.HasB != rows[0].HasB {
			return false
		}
	}
	return true
}

// compareSideSupport keeps at most one physical source per screen column. It
// never expands repeated physical traces into the sparse reader's input list.
func prestackCompareSideSupport(rows []prestackcore.CompareLogicalRow, sideB bool, width int) ([]int64, []int) {
	traces := make([]int64, 0, minInt(len(rows), width))
	columns := make([]int, width)
	byTrace := make(map[int64]int)
	for x := range columns {
		columns[x] = -1
		i := prestackCompareLogicalColumn(x, width, len(rows))
		if i < 0 {
			continue
		}
		row := rows[i]
		trace, present := row.ATrace, row.HasA
		if sideB {
			trace, present = row.BTrace, row.HasB
		}
		if !present {
			continue
		}
		column, ok := byTrace[trace]
		if !ok {
			column = len(traces)
			byTrace[trace] = column
			traces = append(traces, trace)
		}
		columns[x] = column
	}
	return traces, columns
}

func renderPrestackCompareSideValues(ctx context.Context, reader *segy.File, rows []prestackcore.CompareLogicalRow, sideB, dense bool, opts segy.RenderOptions) ([]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.Width < 2 || opts.Height < 2 {
		return nil, fmt.Errorf("invalid Compare raster dimensions")
	}
	if dense && prestackCompareHasSide(rows, sideB) {
		if reader == nil {
			return nil, fmt.Errorf("missing Compare reader")
		}
		traces := make([]int64, len(rows))
		for i, row := range rows {
			traces[i] = row.ATrace
			if sideB {
				traces[i] = row.BTrace
			}
		}
		values, _, err := reader.RenderTraceIndicesValues(traces, opts)
		if err == nil {
			err = ctx.Err()
		}
		return values, err
	}
	values := make([]float64, opts.Width*opts.Height)
	for i := range values {
		values[i] = math.NaN() // A missing side is not a real zero-amplitude trace.
	}
	traces, columns := prestackCompareSideSupport(rows, sideB, opts.Width)
	if len(traces) == 0 {
		return values, nil
	}
	if reader == nil {
		return nil, fmt.Errorf("missing Compare reader")
	}
	supportOpts := opts
	supportOpts.Width = maxInt(2, len(traces))
	// Each support pixel is exactly one physical trace. Existing Adaptive
	// vertical resampling and AGC remain in the reader; no new interpolation
	// or full-amplitude volume is introduced by the slot mapping.
	support, _, err := reader.RenderTraceIndicesValues(traces, supportOpts)
	if err != nil {
		return nil, err
	}
	for y := 0; y < opts.Height; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x, column := range columns {
			if column >= 0 {
				values[y*opts.Width+x] = support[y*supportOpts.Width+column]
			}
		}
	}
	return values, nil
}

func prestackCompareDifferenceSlots(a, b []float64, rows []prestackcore.CompareLogicalRow, width, height int) ([]float64, error) {
	if width < 1 || height < 1 || len(a) != width*height || len(b) != len(a) {
		return nil, fmt.Errorf("Compare difference dimensions do not match")
	}
	difference, err := prestackcore.DifferenceValues(a, b)
	if err != nil {
		return nil, err
	}
	for x := 0; x < width; x++ {
		i := prestackCompareLogicalColumn(x, width, len(rows))
		matched := i >= 0 && rows[i].Classification == prestackcore.CompareRowMatched && rows[i].HasA && rows[i].HasB
		if !matched {
			for y := 0; y < height; y++ {
				difference[y*width+x] = math.NaN()
			}
		}
	}
	return difference, nil
}

func prestackCompareValuesBGRA(values []float64, minimum, maximum float64, palette int) []byte {
	pixels := crookedPaletteBGRA(segy.MapAmplitudeValues(values, minimum, maximum), palette)
	for i, value := range values {
		if !finiteFloat(value) {
			pixels[i*4], pixels[i*4+1], pixels[i*4+2], pixels[i*4+3] = 255, 255, 255, 255
		}
	}
	return pixels
}
