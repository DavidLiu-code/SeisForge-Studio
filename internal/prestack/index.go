package prestack

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// BuildIndex scans only base trace headers with at most four reader tasks.
// Progress is delivered serially on the calling goroutine. Cancellation waits
// for all reads to finish before returning, so the caller can safely Close its
// own Reader immediately on return. No partial index is published on failure.
func BuildIndex(ctx context.Context, reader *segy.File, mapping HeaderMapping, progress func(IndexProgress)) (*PrestackIndex, error) {
	if reader == nil {
		return nil, errors.New("nil SEG-Y reader")
	}
	if err := mapping.Validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := reader.Info.TraceCount
	if n < 1 || int64(int(n)) != n {
		return nil, errors.New("invalid trace count")
	}
	p := &PrestackIndex{SourcePath: reader.Info.Path, Mapping: mapping, Records: make([]PrestackTraceRecord, int(n)), BinarySampleCount: reader.Info.SamplesPerTrace, BinarySampleIntervalUS: reader.Info.SampleIntervalUS}
	workers := min(runtime.GOMAXPROCS(0), 4, int(n))
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var next, done atomic.Int64
	var firstErr error
	var errOnce sync.Once
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var raw [240]byte
			for {
				start := next.Add(128) - 128
				if start >= n {
					return
				}
				for trace := start; trace < min(start+128, n); trace++ {
					if workCtx.Err() != nil {
						return
					}
					if err := reader.ReadTraceHeaderBytes(trace, raw[:]); err != nil {
						if errors.Is(err, os.ErrClosed) || errors.Is(err, os.ErrPermission) {
							errOnce.Do(func() { firstErr = fmt.Errorf("read trace header %d: %w", trace+1, err); cancel() })
							return
						}
						// A short/malformed individual header remains in its physical
						// slot.  QC reports it and the other valid traces are usable.
						if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
							p.Records[trace] = PrestackTraceRecord{TraceNumber: trace, HeaderError: err.Error()}
							done.Add(1)
							continue
						}
						errOnce.Do(func() { firstErr = fmt.Errorf("read trace header %d: %w", trace+1, err); cancel() })
						return
					}
					p.Records[trace] = decodeRecord(trace, raw[:], reader.Info, mapping)
					done.Add(1)
				}
			}
		}()
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	if progress != nil {
		progress(IndexProgress{Total: n, Stage: "扫描道头"})
	}
	ticker := time.NewTicker(60 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if progress != nil {
				d := done.Load()
				progress(IndexProgress{Done: d, Scanned: d, Total: n, Stage: "扫描道头"})
			}
		case <-finished:
			if firstErr != nil {
				return nil, firstErr
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if progress != nil {
				progress(IndexProgress{Done: n, Scanned: n, Total: n, Stage: "建立道集"})
			}
			if err := p.buildTables(ctx); err != nil {
				return nil, err
			}
			p.quality = computeQualityStats(p)
			if progress != nil {
				progress(IndexProgress{Done: n, Scanned: n, Total: n, Stage: "完成"})
			}
			return p, nil
		}
	}
}

func word32(raw []byte, b int, e segy.Endian) int32 {
	if b == 0 {
		return 0
	}
	if e == segy.Little {
		return int32(binary.LittleEndian.Uint32(raw[b-1 : b+3]))
	}
	return int32(binary.BigEndian.Uint32(raw[b-1 : b+3]))
}
func word16(raw []byte, b int, e segy.Endian) int16 {
	if b == 0 {
		return 0
	}
	if e == segy.Little {
		return int16(binary.LittleEndian.Uint16(raw[b-1 : b+1]))
	}
	return int16(binary.BigEndian.Uint16(raw[b-1 : b+1]))
}
func scale(v int16) float64 {
	if v > 0 {
		return float64(v)
	}
	if v < 0 {
		return 1 / float64(-int(v))
	}
	return 1
}

func decodeRecord(trace int64, raw []byte, info segy.Info, m HeaderMapping) PrestackTraceRecord {
	e := info.Endian
	scalar := word16(raw, m.ScalarByte, e)
	factor := scale(scalar)
	r := PrestackTraceRecord{TraceNumber: trace, SourceID: word32(raw, m.SourceIDByte, e), ReceiverID: word32(raw, m.ReceiverIDByte, e),
		CDP: word32(raw, m.CDPByte, e), TraceInField: word32(raw, 13, e), Inline: word32(raw, m.InlineByte, e), Crossline: word32(raw, m.CrosslineByte, e),
		SourceX: float64(word32(raw, m.SourceXByte, e)) * factor, SourceY: float64(word32(raw, m.SourceYByte, e)) * factor,
		ReceiverX: float64(word32(raw, m.ReceiverXByte, e)) * factor, ReceiverY: float64(word32(raw, m.ReceiverYByte, e)) * factor,
		HeaderOffset: float64(word32(raw, m.OffsetByte, e)), CoordinateScalar: scalar, CoordinateUnits: word16(raw, m.UnitsByte, e),
		DelayMS: int(word16(raw, 109, e)), SampleIntervalUS: int(uint16(word16(raw, 117, e))), SampleCount: int(uint16(word16(raw, 115, e)))}
	r.HeaderValid = true
	r.HeaderSampleCount, r.HeaderSampleIntervalUS = r.SampleCount, r.SampleIntervalUS
	r.CoordinateScalarValid = !math.IsNaN(factor) && !math.IsInf(factor, 0) && factor > 0
	r.CoordinateScalarError = !r.CoordinateScalarValid
	if r.SampleCount == 0 || r.SampleIntervalUS == 0 {
		r.HeaderValid = false
		r.HeaderError = "道头样点数或采样间隔为零；显示使用卷头参数"
	}
	r.Offset = r.HeaderOffset
	r.HasHeaderOffset = m.OffsetByte > 0
	r.HasOffset = r.HasHeaderOffset
	r.HasCDP = m.CDPByte > 0 && r.CDP != 0
	if r.SampleCount == 0 {
		r.SampleCount = info.SamplesPerTrace
	}
	if r.SampleIntervalUS == 0 {
		r.SampleIntervalUS = info.SampleIntervalUS
	}
	// CDP coordinates are retained as a fallback midpoint, never used to
	// manufacture a missing source/receiver geometry.
	r.MidpointX = float64(word32(raw, m.CDPXByte, e)) * factor
	r.MidpointY = float64(word32(raw, m.CDPYByte, e)) * factor
	return r
}

func (p *PrestackIndex) buildTables(ctx context.Context) error {
	var sourcePresent, receiverPresent, cdpXYPresent, sourceIDs, receiverIDs, cdps bool
	gridCount := 0
	for _, r := range p.Records {
		sourcePresent = sourcePresent || r.SourceX != 0 || r.SourceY != 0
		receiverPresent = receiverPresent || r.ReceiverX != 0 || r.ReceiverY != 0
		cdpXYPresent = cdpXYPresent || r.MidpointX != 0 || r.MidpointY != 0
		sourceIDs = sourceIDs || r.SourceID != 0
		receiverIDs = receiverIDs || r.ReceiverID != 0
		cdps = cdps || r.CDP != 0
		if r.Inline != 0 || r.Crossline != 0 {
			gridCount++
		}
	}
	sourcePresent = sourcePresent && p.Mapping.SourceXByte > 0
	receiverPresent = receiverPresent && p.Mapping.ReceiverXByte > 0
	cdpXYPresent = cdpXYPresent && p.Mapping.CDPXByte > 0
	p.UsesGrid = p.Mapping.InlineByte > 0 && gridCount > 0 && gridCount*10 >= len(p.Records)*9
	if !p.UsesGrid && cdps {
		p.Warnings = append(p.Warnings, "未发现完整 Inline/Crossline，CMP 使用原始 CDP 字段")
	}
	if !p.UsesGrid && !cdps {
		p.Warnings = append(p.Warnings, "CMP 字段为空；请在道头映射中指定 CDP 或 Inline/Crossline")
	}
	if !sourcePresent || !receiverPresent {
		p.Warnings = append(p.Warnings, "Source/Receiver 坐标不完整；不推算缺失的炮检距或方位角")
	}
	// Trace-in-field is a channel number, not a guaranteed global receiver ID.
	// Detect an ID reused for different receiver coordinates and group exact XY
	// instead. With no coordinates this ambiguity must be confirmed by mapping.
	locations := make(map[int32][2]float64)
	conflictingIDs := false
	for i := range p.Records {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		r := &p.Records[i]
		// A coordinate at the origin is still a legal SEG-Y coordinate. Presence
		// is determined by a configured field and finite decoded values rather
		// than by a non-zero test.
		r.HasSource = sourcePresent && finiteXY(r.SourceX, r.SourceY)
		r.HasReceiver = receiverPresent && finiteXY(r.ReceiverX, r.ReceiverY)
		if r.HasSource {
			p.Bounds.add(r.SourceX, r.SourceY)
		}
		if r.HasReceiver {
			p.Bounds.add(r.ReceiverX, r.ReceiverY)
			xy := [2]float64{r.ReceiverX, r.ReceiverY}
			if old, ok := locations[r.ReceiverID]; ok && old != xy {
				conflictingIDs = true
			} else {
				locations[r.ReceiverID] = xy
			}
		}
		if r.HasSource && r.HasReceiver {
			r.MidpointX = (r.SourceX + r.ReceiverX) / 2
			r.MidpointY = (r.SourceY + r.ReceiverY) / 2
			r.HasMidpoint = true
			// Distances retain the coordinate unit declared in the header. We do
			// not project angular coordinates to metres or claim a physical unit.
			dx, dy := r.ReceiverX-r.SourceX, r.ReceiverY-r.SourceY
			r.ComputedOffset = math.Hypot(dx, dy)
			r.HasComputedOffset = true
			if r.ComputedOffset > 0 {
				r.Azimuth = math.Mod(math.Atan2(dx, dy)*180/math.Pi+360, 360)
				r.HasAzimuth = true
			}
		} else {
			r.HasMidpoint = cdpXYPresent && finiteXY(r.MidpointX, r.MidpointY)
		}
		if r.HasMidpoint {
			p.Bounds.add(r.MidpointX, r.MidpointY)
		}
		if p.Mapping.OffsetByte == 0 && r.HasComputedOffset {
			r.Offset = r.ComputedOffset
			r.HasOffset = true
		}
		if r.HasOffset {
			p.OffsetRange.add(r.Offset)
		}
		if r.HasAzimuth {
			p.AzimuthRange.add(r.Azimuth)
		}
	}
	p.ReceiverUsesCoordinates = receiverPresent && (!receiverIDs || conflictingIDs || p.Mapping.ReceiverIDByte == 1 || p.Mapping.ReceiverIDByte == 5)
	if p.ReceiverUsesCoordinates {
		p.Warnings = append(p.Warnings, "Receiver ID 不是稳定的全局检波点标识，按实际 Group XY 组织检波点道集")
	}
	if !receiverPresent && (p.Mapping.ReceiverIDByte == 1 || p.Mapping.ReceiverIDByte == 5 || p.Mapping.ReceiverIDByte == 13) {
		receiverIDs = false
		p.Warnings = append(p.Warnings, "道序/炮内道号不能确认全局检波点身份；Receiver 道集需指定可靠 ID 或坐标")
	}
	// Apply the same stability rule to Source/Shot IDs before constructing the
	// compact Shot table. A reused source ID at different coordinates must not
	// silently merge unrelated shots.
	p.SourceUsesCoordinates = acquisitionIDConflicts(p.Records, GatherShot)
	if p.SourceUsesCoordinates {
		p.Warnings = append(p.Warnings, "Source ID 不是稳定的全局震源标识，按实际 Source XY 组织炮集")
	}
	keyFor := func(kind GatherType, r PrestackTraceRecord) (GatherKey, bool) {
		switch kind {
		case GatherCMP:
			if p.UsesGrid {
				return GatherKey{Inline: r.Inline, Crossline: r.Crossline, Grid: true}, r.Inline != 0 || r.Crossline != 0
			}
			if cdps {
				return GatherKey{ID: r.CDP}, r.CDP != 0
			}
			return GatherKey{Coordinate: true, X: r.MidpointX, Y: r.MidpointY}, r.HasMidpoint
		case GatherShot:
			if p.SourceUsesCoordinates {
				return GatherKey{Coordinate: true, X: r.SourceX, Y: r.SourceY}, r.HasSource
			}
			return GatherKey{ID: r.SourceID}, sourceIDs && r.SourceID != 0
		case GatherReceiver:
			if p.ReceiverUsesCoordinates {
				return GatherKey{Coordinate: true, X: r.ReceiverX, Y: r.ReceiverY}, r.HasReceiver
			}
			return GatherKey{ID: r.ReceiverID}, receiverIDs && r.ReceiverID != 0
		}
		return GatherKey{}, false
	}
	for kind := GatherCMP; kind <= GatherReceiver; kind++ {
		counts := make(map[GatherKey]int)
		for _, r := range p.Records {
			if key, ok := keyFor(kind, r); ok {
				counts[key]++
			}
		}
		table := &p.tables[kind]
		for key := range counts {
			table.keys = append(table.keys, key)
		}
		sort.Slice(table.keys, func(i, j int) bool { return keyLess(table.keys[i], table.keys[j]) })
		table.starts = make([]int, len(table.keys)+1)
		positions := make(map[GatherKey]int, len(counts))
		for i, key := range table.keys {
			table.starts[i+1] = table.starts[i] + counts[key]
			positions[key] = table.starts[i]
		}
		table.records = make([]int, table.starts[len(table.keys)])
		for i, r := range p.Records {
			if i%4096 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if key, ok := keyFor(kind, r); ok {
				at := positions[key]
				table.records[at] = i
				positions[key] = at + 1
			}
		}
	}
	p.buildBins()
	p.buildAcquisition()
	return ctx.Err()
}

func keyLess(a, b GatherKey) bool {
	if a.Grid != b.Grid {
		return !a.Grid
	}
	if a.Coordinate != b.Coordinate {
		return !a.Coordinate
	}
	if a.Coordinate {
		if a.X != b.X {
			return a.X < b.X
		}
		return a.Y < b.Y
	}
	if a.Grid {
		if a.Inline != b.Inline {
			return a.Inline < b.Inline
		}
		return a.Crossline < b.Crossline
	}
	return a.ID < b.ID
}

func (p *PrestackIndex) buildBins() {
	t := p.tables[GatherCMP]
	p.Bins = make([]Bin, len(t.keys))
	p.fold = FoldStatistics{Bins: len(t.keys)}
	for i, key := range t.keys {
		b := Bin{Key: key, Fold: t.starts[i+1] - t.starts[i]}
		xyCount := 0
		for _, record := range t.records[t.starts[i]:t.starts[i+1]] {
			r := p.Records[record]
			if r.HasMidpoint {
				b.X += r.MidpointX
				b.Y += r.MidpointY
				xyCount++
			}
			if r.HasOffset {
				b.OffsetRange.add(r.Offset)
			}
			if r.HasAzimuth {
				b.AzimuthRange.add(r.Azimuth)
			}
		}
		if xyCount > 0 {
			b.X /= float64(xyCount)
			b.Y /= float64(xyCount)
			b.HasCoordinates = true
		}
		p.Bins[i] = b
		p.fold.Traces += b.Fold
		if i == 0 || b.Fold < p.fold.Min {
			p.fold.Min = b.Fold
		}
		if b.Fold > p.fold.Max {
			p.fold.Max = b.Fold
		}
	}
	if p.fold.Bins > 0 {
		p.fold.Mean = float64(p.fold.Traces) / float64(p.fold.Bins)
	}
}

func (p *PrestackIndex) AvailableGathers(kind GatherType) []GatherKey {
	if p == nil || kind > GatherRaw {
		return nil
	}
	if kind == GatherRaw {
		if len(p.Records) == 0 {
			return nil
		}
		return []GatherKey{{Raw: true}}
	}
	if kind == GatherOffset {
		return p.AvailableGathersConfigured(kind, DefaultOffsetBinSize)
	}
	return append([]GatherKey(nil), p.tables[kind].keys...)
}

// AvailableGathersConfigured returns all available gather keys.  Common
// Offset keys are generated from the metadata records using the requested
// bin width; CMP/Shot/Receiver continue to use their compact CSR tables.
func (p *PrestackIndex) AvailableGathersConfigured(kind GatherType, offsetBinSize float64) []GatherKey {
	if p == nil || kind > GatherRaw {
		return nil
	}
	if kind == GatherRaw {
		if len(p.Records) == 0 {
			return nil
		}
		return []GatherKey{{Raw: true}}
	}
	if kind != GatherOffset {
		return append([]GatherKey(nil), p.tables[kind].keys...)
	}
	width := normalizeOffsetBinSize(offsetBinSize)
	indices := make(map[int64]struct{})
	for _, r := range p.Records {
		if !validOffsetRecord(r) {
			continue
		}
		indices[offsetBinIndex(r.Offset, width)] = struct{}{}
	}
	keys := make([]GatherKey, 0, len(indices))
	for index := range indices {
		keys = append(keys, offsetBinKey(index, width))
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].OffsetBinIndex < keys[j].OffsetBinIndex })
	return keys
}

// GatherKeys is a descriptive alias used by non-UI callers. It returns a
// defensive copy in the same way as AvailableGathers.
func (p *PrestackIndex) GatherKeys(kind GatherType) []GatherKey {
	return p.AvailableGathers(kind)
}

// GatherKeysConfigured is the configured-width counterpart to GatherKeys.
// It is provided as a descriptive alias for callers that use the GatherKeys
// naming convention throughout their UI model.
func (p *PrestackIndex) GatherKeysConfigured(kind GatherType, offsetBinSize float64) []GatherKey {
	return p.AvailableGathersConfigured(kind, offsetBinSize)
}

func (p *PrestackIndex) FoldStatistics() FoldStatistics {
	return p.FoldStats()
}

func (p *PrestackIndex) Gather(selection GatherSelection) (GatherResult, error) {
	if p == nil || selection.Type > GatherRaw {
		return GatherResult{}, errors.New("invalid gather type")
	}
	if selection.Sort > SortAzimuth || selection.Axis > AxisOffset {
		return GatherResult{}, errors.New("invalid gather display settings")
	}
	var records []int
	var rawStart, rawEnd int64
	if selection.Type != GatherRaw && selection.Key.All {
		// "全部范围" is a synthetic selection used by the viewer. Keep the
		// requested gather type for labels and sorting, but include every
		// physical record so users can inspect the complete acquisition range
		// without having to choose one CMP/shot/receiver/offset key.
		records = make([]int, len(p.Records))
		for i := range records {
			records[i] = i
		}
	} else if selection.Type == GatherRaw {
		// Raw mode intentionally ignores gather keys and all ordering settings:
		// this is the physical SEG-Y record order, not a derived acquisition
		// order. The synthetic Raw key is accepted but not required so callers
		// can construct a selection directly from text-box range values.
		rawStart, rawEnd, _ = NormalizeRawTraceRange(int64(len(p.Records)), selection.RawTraceStart, selection.RawTraceEnd)
		if rawEnd <= rawStart {
			return GatherResult{}, errors.New("raw trace range is empty")
		}
		records = make([]int, int(rawEnd-rawStart))
		for i := range records {
			records[i] = int(rawStart) + i
		}
		selection.Key = GatherKey{Raw: true}
		selection.Sort = SortPhysical
		selection.Axis = AxisTrace
		selection.RawTraceStart, selection.RawTraceEnd = rawStart, rawEnd
	} else if selection.Type == GatherOffset {
		width := selection.OffsetBinSize
		// A key returned by AvailableGathersConfigured carries its configured
		// edges, so callers that only persist the key can still gather it
		// without separately persisting the width.
		if width <= 0 || math.IsNaN(width) || math.IsInf(width, 0) {
			if selection.Key.OffsetMax > selection.Key.OffsetMin &&
				!math.IsNaN(selection.Key.OffsetMax-selection.Key.OffsetMin) &&
				!math.IsInf(selection.Key.OffsetMax-selection.Key.OffsetMin, 0) {
				width = selection.Key.OffsetMax - selection.Key.OffsetMin
			}
		}
		width = normalizeOffsetBinSize(width)
		selection.OffsetBinSize = width
		if !selection.Key.OffsetBin {
			return GatherResult{}, fmt.Errorf("%s gather %s is unavailable", selection.Type, selection.Key)
		}
		// Match by stable integer bin identity, not by floating display fields.
		// This also lets a caller reconstruct a key from serialized UI values.
		target := selection.Key.OffsetBinIndex
		for i, r := range p.Records {
			if validOffsetRecord(r) && offsetBinIndex(r.Offset, width) == target {
				records = append(records, i)
			}
		}
		if len(records) == 0 {
			return GatherResult{}, fmt.Errorf("%s gather %s is unavailable", selection.Type, selection.Key)
		}
	} else {
		t := p.tables[selection.Type]
		at := sort.Search(len(t.keys), func(i int) bool { return !keyLess(t.keys[i], selection.Key) })
		if at == len(t.keys) || t.keys[at] != selection.Key {
			return GatherResult{}, fmt.Errorf("%s gather %s is unavailable", selection.Type, selection.Key)
		}
		records = append([]int(nil), t.records[t.starts[at]:t.starts[at+1]]...)
	}
	if selection.Sort != SortPhysical {
		sort.SliceStable(records, func(i, j int) bool {
			a, b := p.Records[records[i]], p.Records[records[j]]
			if selection.Sort == SortOffset || selection.Sort == SortAbsoluteOffset {
				if a.HasOffset != b.HasOffset {
					return a.HasOffset
				}
			}
			x, y := a.Offset, b.Offset
			switch selection.Sort {
			case SortAbsoluteOffset:
				x, y = math.Abs(x), math.Abs(y)
			case SortAzimuth:
				if a.HasAzimuth != b.HasAzimuth {
					return a.HasAzimuth
				}
				x, y = a.Azimuth, b.Azimuth
			}
			if x == y {
				return a.TraceNumber < b.TraceNumber
			}
			return x < y
		})
	}
	out := GatherResult{Selection: selection, TraceIndices: make([]int64, len(records)), Positions: make([]float64, len(records)), RawTraceStart: rawStart, RawTraceEnd: rawEnd}
	for i, index := range records {
		r := p.Records[index]
		out.TraceIndices[i] = r.TraceNumber
		out.Positions[i] = float64(i)
		if selection.Axis == AxisOffset {
			out.Positions[i] = r.Offset
		}
		if r.HasOffset {
			out.OffsetRange.add(r.Offset)
		}
		if r.HasAzimuth {
			out.AzimuthRange.add(r.Azimuth)
		}
	}
	return out, nil
}

// RawTraceIndices returns physical trace numbers in file order for a
// zero-based half-open range. It is a lightweight convenience for renderers
// that do not need the metadata ranges produced by Gather.
func (p *PrestackIndex) RawTraceIndices(start, end int64) ([]int64, error) {
	if p == nil {
		return nil, errors.New("nil prestack index")
	}
	first, last, ok := NormalizeRawTraceRange(int64(len(p.Records)), start, end)
	if !ok {
		return nil, errors.New("raw trace range is empty")
	}
	out := make([]int64, int(last-first))
	for i := range out {
		out[i] = first + int64(i)
	}
	return out, nil
}

// RawGather builds the canonical raw-order GatherResult while retaining an
// optional sample window for callers that pass the same immutable selection
// through to their renderer. Sample bounds are not interpreted here because
// this metadata-only package does not own SEG-Y sample decoding.
func (p *PrestackIndex) RawGather(selection RawTraceSelection) (GatherResult, error) {
	return p.Gather(GatherSelection{Type: GatherRaw, Key: GatherKey{Raw: true},
		RawTraceStart: selection.TraceStart, RawTraceEnd: selection.TraceEnd,
		SampleStart: selection.SampleStart, SampleEnd: selection.SampleEnd,
		Sort: SortPhysical, Axis: AxisTrace})
}

// Gather is a nil-safe functional facade for callers that keep indexes behind
// interfaces. The method above remains the primary API.
func Gather(index *PrestackIndex, selection GatherSelection) (GatherResult, error) {
	if index == nil {
		return GatherResult{}, errors.New("nil prestack index")
	}
	return index.Gather(selection)
}

// AdjacentGather navigates available real keys without inventing intermediate
// IDs. It clamps at the first/last key; no wraparound surprise at endpoints.
func (p *PrestackIndex) AdjacentGather(kind GatherType, current GatherKey, delta int) (GatherKey, bool) {
	if p == nil || kind > GatherRaw {
		return GatherKey{}, false
	}
	if kind == GatherRaw {
		if len(p.Records) == 0 {
			return GatherKey{}, false
		}
		// There is exactly one raw-order selection; navigation is intentionally
		// a no-op rather than inventing a neighboring gather.
		return GatherKey{Raw: true}, true
	}
	if kind == GatherOffset {
		return p.AdjacentGatherConfigured(kind, current, delta, DefaultOffsetBinSize)
	}
	keys := p.tables[kind].keys
	if len(keys) == 0 {
		return GatherKey{}, false
	}
	at := sort.Search(len(keys), func(i int) bool { return !keyLess(keys[i], current) })
	at = max(0, min(len(keys)-1, at+delta))
	return keys[at], true
}

// AdjacentGatherConfigured navigates Common Offset bins generated with the
// requested width.  It is intentionally separate from AdjacentGather so old
// callers retain the default 20-unit bin semantics.
func (p *PrestackIndex) AdjacentGatherConfigured(kind GatherType, current GatherKey, delta int, offsetBinSize float64) (GatherKey, bool) {
	if p == nil || kind != GatherOffset {
		return p.AdjacentGather(kind, current, delta)
	}
	keys := p.AvailableGathersConfigured(kind, offsetBinSize)
	if len(keys) == 0 {
		return GatherKey{}, false
	}
	at := sort.Search(len(keys), func(i int) bool { return keys[i].OffsetBinIndex >= current.OffsetBinIndex })
	if at >= len(keys) || keys[at].OffsetBinIndex != current.OffsetBinIndex {
		at = max(0, min(len(keys)-1, at))
	}
	at = max(0, min(len(keys)-1, at+delta))
	return keys[at], true
}

func normalizeOffsetBinSize(width float64) float64 {
	if width <= 0 || math.IsNaN(width) || math.IsInf(width, 0) {
		return DefaultOffsetBinSize
	}
	return width
}

func validOffsetRecord(r PrestackTraceRecord) bool {
	return r.HasOffset && !math.IsNaN(r.Offset) && !math.IsInf(r.Offset, 0)
}

func offsetBinIndex(offset, width float64) int64 {
	return int64(math.Floor(offset/width + 0.5))
}

func offsetBinKey(index int64, width float64) GatherKey {
	center := float64(index) * width
	half := width / 2
	return GatherKey{OffsetBin: true, OffsetBinIndex: index, OffsetCenter: center, OffsetMin: center - half, OffsetMax: center + half}
}
