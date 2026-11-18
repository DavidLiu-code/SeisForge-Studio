package poststack

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// TraceIndicesForRange returns the zero-based physical trace sequence selected
// by the legacy load dialog. Bounds are inclusive. Invalid/negative bounds are
// normalized to the file, and a non-positive step means every trace. Keeping
// this conversion in the data layer prevents Compare's virtual single-line
// geometry from silently expanding a user-selected section back to the file.
func TraceIndicesForRange(total, start, end, step int64) []int64 {
	if total <= 0 {
		return nil
	}
	if start < 0 {
		start = 0
	}
	if end < 0 || end >= total {
		end = total - 1
	}
	if start >= total || end < start {
		return nil
	}
	if step <= 0 {
		step = 1
	}
	count := (end-start)/step + 1
	if count < 0 || count > total {
		return nil
	}
	out := make([]int64, 0, int(count))
	for i := start; i <= end; {
		out = append(out, i)
		if end-i < step {
			break
		}
		i += step
	}
	return out
}

// GeometryForTraceSelection projects a generated B file back onto the source
// A geometry. The generated file stores selected A traces in compact B order
// (0..N-1), while its spatial coordinates must remain the exact IL/XL values
// from A. This keeps generated results geometrically identical to A and
// avoids falling back to the virtual one-line geometry used for unrelated 2-D
// files.
func GeometryForTraceSelection(path string, source *segy.GeometryIndex, sourceTraceIndices []int64) (*segy.GeometryIndex, error) {
	if source == nil || !source.Poststack || len(source.TraceNumbers) == 0 {
		return nil, errors.New("source geometry is unavailable")
	}
	if len(sourceTraceIndices) == 0 {
		return nil, errors.New("empty source trace selection")
	}
	positions := make(map[int64]int, len(source.TraceNumbers))
	for i, trace := range source.TraceNumbers {
		positions[trace] = i
	}
	rows := make([]int32, len(sourceTraceIndices))
	cols := make([]int32, len(sourceTraceIndices))
	rowRanks, colRanks := make(map[int32]int32), make(map[int32]int32)
	seen := make(map[[2]int32]bool, len(sourceTraceIndices))
	for i, trace := range sourceTraceIndices {
		position, ok := positions[trace]
		if !ok || position >= len(source.RowOfTrace) || position >= len(source.ColOfTrace) {
			return nil, fmt.Errorf("source trace %d has no geometry", trace)
		}
		rows[i] = source.RowOfTrace[position]
		cols[i] = source.ColOfTrace[position]
		if rows[i] < 0 || int(rows[i]) >= len(source.InlineValues) || cols[i] < 0 || int(cols[i]) >= len(source.CrosslineValues) {
			return nil, fmt.Errorf("source trace %d has invalid geometry ranks", trace)
		}
		key := [2]int32{rows[i], cols[i]}
		if seen[key] {
			return nil, fmt.Errorf("source trace %d shares an ambiguous geometry location", trace)
		}
		seen[key] = true
		rowRanks[rows[i]], colRanks[cols[i]] = 0, 0
	}
	// A cropped result owns only its actual rows and columns. Keeping all of
	// A's axis values lets nearest-line selection find an empty B line.
	projectValues := func(sourceValues []int32, ranks map[int32]int32) []int32 {
		old := make([]int32, 0, len(ranks))
		for k := range ranks {
			old = append(old, k)
		}
		sort.Slice(old, func(i, j int) bool { return old[i] < old[j] })
		values := make([]int32, len(old))
		for i, k := range old {
			ranks[k], values[i] = int32(i), sourceValues[k]
		}
		return values
	}
	il, xl := projectValues(source.InlineValues, rowRanks), projectValues(source.CrosslineValues, colRanks)
	for i := range rows {
		rows[i], cols[i] = rowRanks[rows[i]], colRanks[cols[i]]
	}
	return &segy.GeometryIndex{
		SourcePath: path, InlineByte: source.InlineByte, CrosslineByte: source.CrosslineByte,
		TraceCount: int64(len(sourceTraceIndices)), InlineValues: il, CrosslineValues: xl,
		TraceNumbers: sequenceTraceNumbers(len(sourceTraceIndices)), RowOfTrace: rows, ColOfTrace: cols,
		ValidTraceCount: int64(len(sourceTraceIndices)), Poststack: true,
	}, nil
}

func sequenceTraceNumbers(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i)
	}
	return out
}

// TraceOrderSourceGeometry constructs a trace-order view of the original A
// file. Unlike a compact generated B, its Reader still addresses the original
// physical trace numbers, including a nonzero start and skipped traces.
func TraceOrderSourceGeometry(path string, sourceTraceIndices []int64) (*segy.GeometryIndex, error) {
	g, err := TraceOrderGeometry(path, int64(len(sourceTraceIndices)), sourceTraceIndices)
	if err != nil {
		return nil, err
	}
	g.TraceNumbers = append([]int64(nil), sourceTraceIndices...)
	return g, nil
}

// TraceOrderGeometry supplies a virtual, in-memory one-line display index for
// 2D or irregular data. It neither changes source headers nor writes a cache.
// Header byte locations are zero to distinguish it from measured IL/XL data.
//
// For A, nil sourceTraceIndices means physical traces 0..total-1. For a derived
// B, the mapping must contain one original A physical trace per B trace. The
// crossline coordinate is A's one-based physical trace, while TraceNumbers
// remains the index into the actual A/B file. Thus a cropped/reordered B does
// not accidentally pair with the same numeric file index in A.
func TraceOrderGeometry(path string, total int64, sourceTraceIndices []int64) (*segy.GeometryIndex, error) {
	// Match the existing dense-grid limit while bounding metadata allocations
	// for malformed file headers. This is not an amplitude-volume allocation.
	const maxVirtualTraces = 20_000_000
	if total < 1 || total > maxVirtualTraces {
		return nil, fmt.Errorf("virtual trace count must be in 1..%d", maxVirtualTraces)
	}
	if sourceTraceIndices != nil && int64(len(sourceTraceIndices)) != total {
		return nil, errors.New("source trace map length differs from result trace count")
	}
	n := int(total)
	coordinates := make([]int32, n)
	for i := range coordinates {
		trace := int64(i)
		if sourceTraceIndices != nil {
			trace = sourceTraceIndices[i]
		}
		if trace < 0 || trace >= math.MaxInt32 {
			return nil, errors.New("source physical trace cannot be represented by the virtual coordinate axis")
		}
		coordinates[i] = int32(trace + 1)
	}
	sorted := append([]int32(nil), coordinates...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for i := 1; i < n; i++ {
		if sorted[i-1] == sorted[i] {
			return nil, errors.New("virtual geometry cannot assign duplicate source trace coordinates")
		}
	}
	g := &segy.GeometryIndex{SourcePath: path, TraceCount: total, InlineByte: 0, CrosslineByte: 0,
		InlineValues: []int32{1}, CrosslineValues: sorted, TraceNumbers: make([]int64, n),
		RowOfTrace: make([]int32, n), ColOfTrace: make([]int32, n), ValidTraceCount: total, Poststack: true}
	for i, coordinate := range coordinates {
		g.TraceNumbers[i] = int64(i)
		g.ColOfTrace[i] = int32(sort.Search(len(sorted), func(j int) bool { return sorted[j] >= coordinate }))
	}
	return g, nil
}
