package segy

import (
	"errors"
	"math"
	"sort"
)

// RenderTracePositions renders traces whose horizontal coordinates are not
// uniformly spaced. positions must be finite and monotonically increasing;
// positionStart/End select the visible coordinate window.
func (s *File) RenderTracePositions(traceIndices []int64, positions []float64, positionStart, positionEnd float64, o RenderOptions) ([]byte, RenderStats, error) {
	if o.Width < 2 || o.Height < 2 {
		return nil, RenderStats{}, errors.New("invalid image dimensions")
	}
	if len(traceIndices) < 2 || len(traceIndices) != len(positions) {
		return nil, RenderStats{}, errors.New("trace positions are empty or unmatched")
	}
	for i, position := range positions {
		if math.IsNaN(position) || math.IsInf(position, 0) || (i > 0 && position < positions[i-1]) {
			return nil, RenderStats{}, errors.New("trace positions must be finite and monotonically increasing")
		}
	}
	if positionStart < positions[0] {
		positionStart = positions[0]
	}
	if positionEnd > positions[len(positions)-1] || positionEnd <= positionStart {
		positionEnd = positions[len(positions)-1]
	}
	if positionEnd <= positionStart {
		return nil, RenderStats{}, errors.New("trace position range has zero width")
	}
	if o.ClipPercent <= 0 {
		o.ClipPercent = 99
	}
	ns := s.Info.SamplesPerTrace
	sm0, sm1 := o.SampleStart, o.SampleEnd
	if sm0 < 0 {
		sm0 = 0
	}
	if sm1 < 0 || sm1 >= ns {
		sm1 = ns - 1
	}
	if sm0 >= ns || sm0 > sm1 {
		return nil, RenderStats{}, errors.New("invalid sample range")
	}
	nearest := func(target float64) int {
		i := sort.Search(len(positions), func(i int) bool { return positions[i] >= target })
		if i >= len(positions) {
			return len(positions) - 1
		}
		if i > 0 && target-positions[i-1] <= positions[i]-target {
			return i - 1
		}
		return i
	}
	traceForX := func(x int) int64 {
		target := positionStart
		if o.Width > 1 {
			target += float64(x) * (positionEnd - positionStart) / float64(o.Width-1)
		}
		return traceIndices[nearest(target)]
	}
	visibleStart, visibleEnd := nearest(positionStart), nearest(positionEnd)
	if visibleEnd < visibleStart {
		visibleStart, visibleEnd = visibleEnd, visibleStart
	}
	visibleCount := visibleEnd - visibleStart + 1
	var values []float64
	var observedMin, observedMax float64
	var ioStats coalescedIOStats
	var sparseStats sparseIOStats
	var err error
	if shouldUseSmooth(o.DisplayMode, visibleCount, sm1-sm0+1, o.Width, o.Height) {
		readStart, readEnd := visibleStart, visibleEnd
		if readStart > 0 {
			readStart--
		}
		if readEnd < len(traceIndices)-1 {
			readEnd++
		}
		if o.ReadStrategy == ReadStrategySparseMapped || o.ReadStrategy == ReadStrategySparseCoalesced {
			values, observedMin, observedMax, sparseStats, err = s.renderInterpolatedPositionsSparse(traceIndices[readStart:readEnd+1], positions[readStart:readEnd+1], positionStart, positionEnd, o, sm0, sm1, o.ReadStrategy == ReadStrategySparseMapped)
		} else if o.ReadStrategy == ReadStrategyCoalesced {
			values, observedMin, observedMax, ioStats, err = s.renderInterpolatedPositionsCoalesced(traceIndices[readStart:readEnd+1], positions[readStart:readEnd+1], positionStart, positionEnd, o, sm0, sm1)
		} else {
			values, observedMin, observedMax, err = s.renderInterpolatedPositions(traceIndices[readStart:readEnd+1], positions[readStart:readEnd+1], positionStart, positionEnd, o, sm0, sm1)
		}
	} else {
		if o.ReadStrategy == ReadStrategySparseMapped || o.ReadStrategy == ReadStrategySparseCoalesced {
			values, observedMin, observedMax, sparseStats, err = s.renderColumnsSparse(o, traceForX, visibleCount, sm0, sm1, o.ReadStrategy == ReadStrategySparseMapped)
		} else if o.ReadStrategy == ReadStrategyCoalesced {
			values, observedMin, observedMax, ioStats, err = s.renderColumnsCoalesced(o, traceForX, sm0, sm1)
		} else {
			values, observedMin, observedMax, err = s.renderColumnsParallel(o, traceForX, sm0, sm1)
		}
	}
	if err != nil {
		return nil, RenderStats{}, err
	}
	if o.ReadStrategy == ReadStrategySparseMapped || o.ReadStrategy == ReadStrategySparseCoalesced {
		defer releaseSparseFloat64(values)
	}
	pixels, lo, hi := mapRenderValues(values, observedMin, observedMax, o)
	stats := RenderStats{ObservedMin: observedMin, ObservedMax: observedMax, MapMin: lo, MapMax: hi, TraceStart: traceIndices[visibleStart], TraceEnd: traceIndices[visibleEnd], TraceStep: 1, SampleStart: sm0, SampleEnd: sm1,
		IOReadCalls: ioStats.readCalls, IOCoalescedBlocks: ioStats.readCalls, IOReadBytes: ioStats.readBytes, CoalescedIO: o.ReadStrategy == ReadStrategyCoalesced}
	if o.ReadStrategy == ReadStrategySparseMapped || o.ReadStrategy == ReadStrategySparseCoalesced {
		stats.IOReadCalls, stats.IOCoalescedBlocks, stats.IOReadBytes = sparseStats.readCalls, sparseStats.readCalls, sparseStats.readBytes
		stats.CoalescedIO, stats.SparseIO, stats.MappedIO = sparseStats.fallbacks > 0, true, sparseStats.mapped
		stats.InputTraceCount, stats.SupportTraceCount = sparseStats.inputTraces, sparseStats.supportTraces
		stats.IOLogicalBytes, stats.IODecodeNanos, stats.DecodedSampleCount, stats.IOFallbacks = sparseStats.logicalBytes, sparseStats.decodeNanos, sparseStats.decodedSamples, sparseStats.fallbacks
	}
	return pixels, stats, nil
}

func (s *File) renderInterpolatedPositions(traceIndices []int64, positions []float64, positionStart, positionEnd float64, o RenderOptions, sm0, sm1 int) ([]float64, float64, float64, error) {
	srcHeight := sm1 - sm0 + 1
	traces := make([][]float64, len(traceIndices))
	for i, traceIndex := range traceIndices {
		trace, err := s.readTraceWindow(traceIndex, sm0, sm1)
		if err != nil {
			return nil, 0, 0, err
		}
		if o.AGC {
			ss, count := 0.0, 0
			for _, value := range trace {
				if !math.IsNaN(value) && !math.IsInf(value, 0) {
					ss += value * value
					count++
				}
			}
			if count > 0 && ss > 0 {
				rms := math.Sqrt(ss / float64(count))
				for j := range trace {
					trace[j] /= rms
				}
			}
		}
		traces[i] = trace
	}
	values := make([]float64, o.Width*o.Height)
	observedMin, observedMax := math.Inf(1), math.Inf(-1)
	for x := 0; x < o.Width; x++ {
		target := positionStart + float64(x)*(positionEnd-positionStart)/float64(o.Width-1)
		right := sort.Search(len(positions), func(i int) bool { return positions[i] >= target })
		if right >= len(positions) {
			right = len(positions) - 1
		}
		left := right
		fractionX := 0.0
		if right > 0 {
			left = right - 1
			span := positions[right] - positions[left]
			if span > 0 {
				fractionX = (target - positions[left]) / span
			}
		}
		for y := 0; y < o.Height; y++ {
			sourceY := 0.0
			if o.Height > 1 && srcHeight > 1 {
				sourceY = float64(y) * float64(srcHeight-1) / float64(o.Height-1)
			}
			y0 := int(math.Floor(sourceY))
			y1 := y0
			fractionY := 0.0
			if y0 < srcHeight-1 {
				y1 = y0 + 1
				fractionY = sourceY - float64(y0)
			}
			leftValue := traces[left][y0]*(1-fractionY) + traces[left][y1]*fractionY
			rightValue := traces[right][y0]*(1-fractionY) + traces[right][y1]*fractionY
			value := leftValue*(1-fractionX) + rightValue*fractionX
			if math.IsNaN(value) || math.IsInf(value, 0) {
				value = 0
			}
			values[y*o.Width+x] = value
			observedMin = math.Min(observedMin, value)
			observedMax = math.Max(observedMax, value)
		}
	}
	if math.IsInf(observedMin, 1) {
		observedMin, observedMax = 0, 0
	}
	if o.Progress != nil {
		o.Progress(o.Width, o.Width)
	}
	return values, observedMin, observedMax, nil
}
