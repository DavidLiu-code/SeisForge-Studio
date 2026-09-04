package segy

import (
	"errors"
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

const (
	coalescedMaximumBlockBytes = int64(8 << 20)
	sparseMaximumBlockBytes    = int64(16 << 20)
	coalescedMaximumGapBytes   = int64(64 << 10)
	coalescedMaximumWorkers    = 4
	coalescedGlobalIOWorkers   = 8
)

var coalescedIOSemaphore = make(chan struct{}, coalescedGlobalIOWorkers)

var coalescedBufferPools [6]sync.Pool

var coalescedBufferSizes = [...]int{64 << 10, 256 << 10, 1 << 20, 4 << 20, 8 << 20, 16 << 20}

func takeCoalescedBuffer(size int) []byte {
	for index, capacity := range coalescedBufferSizes {
		if size <= capacity {
			if cached := coalescedBufferPools[index].Get(); cached != nil {
				return cached.([]byte)[:size]
			}
			return make([]byte, size, capacity)
		}
	}
	return make([]byte, size)
}

func releaseCoalescedBuffer(buffer []byte) {
	capacity := cap(buffer)
	for index, size := range coalescedBufferSizes {
		if capacity == size {
			coalescedBufferPools[index].Put(buffer[:size])
			return
		}
	}
}

type coalescedIOStats struct {
	readCalls int
	readBytes int64
}

type coalescedTraceBlock struct {
	start, end int64
	indices    []int
}

func (s *File) coalescedTraceWindows(traceIndices []int64, sm0, sm1, requestedWorkers int) (map[int64][]float64, coalescedIOStats, error) {
	if s == nil || s.f == nil || len(traceIndices) == 0 {
		return nil, coalescedIOStats{}, errors.New("empty coalesced trace request")
	}
	unique := append([]int64(nil), traceIndices...)
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	write := 0
	for _, trace := range unique {
		if trace < 0 || trace >= s.Info.TraceCount {
			return nil, coalescedIOStats{}, errors.New("trace index out of range")
		}
		if write == 0 || unique[write-1] != trace {
			unique[write] = trace
			write++
		}
	}
	unique = unique[:write]
	bps := s.Info.BytesPerSample
	windowBytes := int64((sm1 - sm0 + 1) * bps)
	maximumGap := coalescedMaximumGapBytes
	maximumOverreadRatio := 0.0
	windowSamples := sm1 - sm0 + 1
	// Inclusive sample windows can contain one extra endpoint at an exact
	// 50% time boundary. Keep that rounding sample in the narrow-window path.
	if windowSamples <= (s.Info.SamplesPerTrace+2)/2 {
		// A narrow time window must not silently read the discarded samples
		// between adjacent traces. Limit merge overhead to 25% of useful bytes
		// (while still permitting one trace header) so cold AOI I/O scales with
		// the selected vertical interval.
		maximumOverreadRatio = 1.6
		if windowSamples <= (s.Info.SamplesPerTrace+4)/4 {
			// At a quarter record, pairs of adjacent trace windows cost about
			// 2.5x their useful bytes. Pairing them halves ReadAt overhead while
			// keeping total cold I/O below the 35% acceptance ceiling.
			maximumOverreadRatio = 2.6
		}
	}
	traceStart := func(trace int64) int64 {
		return s.Info.DataStart + trace*s.Info.TraceBytes + 240 + int64(sm0*bps)
	}
	blocks := make([]coalescedTraceBlock, 0, len(unique))
	for index, trace := range unique {
		start, end := traceStart(trace), traceStart(trace)+windowBytes
		merge := len(blocks) > 0
		if merge {
			last := &blocks[len(blocks)-1]
			span := end - last.start
			merge = start-last.end <= maximumGap && span <= coalescedMaximumBlockBytes
			if merge && maximumOverreadRatio > 0 {
				useful := int64(len(last.indices)+1) * windowBytes
				merge = float64(span) <= float64(useful)*maximumOverreadRatio
			}
		}
		if !merge {
			blocks = append(blocks, coalescedTraceBlock{start: start, end: end, indices: []int{index}})
			continue
		}
		last := &blocks[len(blocks)-1]
		last.end = end
		last.indices = append(last.indices, index)
	}
	traces := make([][]float64, len(unique))
	workers := normalizeRenderWorkers(requestedWorkers, len(blocks))
	if workers > coalescedMaximumWorkers {
		workers = coalescedMaximumWorkers
	}
	jobs := make(chan coalescedTraceBlock)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	var readCalls, readBytes int64
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for block := range jobs {
				errMu.Lock()
				stopped := firstErr != nil
				errMu.Unlock()
				if stopped {
					continue
				}
				raw := takeCoalescedBuffer(int(block.end - block.start))
				coalescedIOSemaphore <- struct{}{}
				_, err := s.f.ReadAt(raw, block.start)
				<-coalescedIOSemaphore
				if err != nil {
					releaseCoalescedBuffer(raw)
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					continue
				}
				atomic.AddInt64(&readCalls, 1)
				atomic.AddInt64(&readBytes, int64(len(raw)))
				for _, index := range block.indices {
					start := traceStart(unique[index]) - block.start
					window := raw[start : start+windowBytes]
					values := make([]float64, sm1-sm0+1)
					for sample := range values {
						values[sample] = decodeSample(window[sample*bps:(sample+1)*bps], s.Info.FormatCode, s.Info.Endian)
					}
					traces[index] = values
				}
				releaseCoalescedBuffer(raw)
			}
		}()
	}
	for _, block := range blocks {
		jobs <- block
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, coalescedIOStats{}, firstErr
	}
	result := make(map[int64][]float64, len(unique))
	for i, trace := range unique {
		result[trace] = traces[i]
	}
	return result, coalescedIOStats{readCalls: int(readCalls), readBytes: readBytes}, nil
}

// sparseCoalescedTraceWindowsOrdered is the exact ReadAt fallback for the
// memory-mapped sparse path. Results retain caller order and never build an
// int64-to-slice map. Bounded overread prevents skipped traces from silently
// turning an output-driven request back into a full-file read.
func (s *File) sparseCoalescedTraceWindowsOrdered(traceIndices []int64, sm0, sm1, requestedWorkers int, sampleOffsets []int) (sparseTraceData, coalescedIOStats, error) {
	if s == nil || s.f == nil || len(traceIndices) == 0 {
		return sparseTraceData{}, coalescedIOStats{}, errors.New("empty sparse coalesced trace request")
	}
	type request struct {
		trace  int64
		output int
	}
	requests := make([]request, len(traceIndices))
	seen := make(map[int64]bool, len(traceIndices))
	for index, trace := range traceIndices {
		if trace < 0 || trace >= s.Info.TraceCount {
			return sparseTraceData{}, coalescedIOStats{}, errors.New("sparse coalesced trace index out of range")
		}
		if seen[trace] {
			return sparseTraceData{}, coalescedIOStats{}, errors.New("sparse coalesced trace request contains a duplicate")
		}
		seen[trace] = true
		requests[index] = request{trace: trace, output: index}
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].trace < requests[j].trace })
	windowSamples := sm1 - sm0 + 1
	windowBytes := int64(windowSamples * s.Info.BytesPerSample)
	if windowSamples < 1 {
		return sparseTraceData{}, coalescedIOStats{}, errors.New("invalid sparse coalesced sample window")
	}
	decodeSamples := windowSamples
	if len(sampleOffsets) > 0 {
		decodeSamples = len(sampleOffsets)
	}
	// Full-record sparse rendering is decode-bound on the target Windows
	// workstation. Permit at most 3.25x logical support bytes so adjacent
	// support traces remain large sequential reads, while still decoding only
	// the traces that can influence output pixels.
	maximumOverreadRatio := 3.25
	if windowSamples <= (s.Info.SamplesPerTrace+2)/2 {
		maximumOverreadRatio = 1.75
	}
	if windowSamples <= (s.Info.SamplesPerTrace+4)/4 {
		maximumOverreadRatio = 1.60
	}
	traceStart := func(trace int64) int64 {
		return s.Info.DataStart + trace*s.Info.TraceBytes + 240 + int64(sm0*s.Info.BytesPerSample)
	}
	blocks := make([]coalescedTraceBlock, 0, len(requests))
	for index, item := range requests {
		start, end := traceStart(item.trace), traceStart(item.trace)+windowBytes
		merge := len(blocks) > 0
		if merge {
			last := &blocks[len(blocks)-1]
			span := end - last.start
			useful := int64(len(last.indices)+1) * windowBytes
			merge = start-last.end <= coalescedMaximumGapBytes && span <= sparseMaximumBlockBytes && float64(span) <= float64(useful)*maximumOverreadRatio
		}
		if !merge {
			blocks = append(blocks, coalescedTraceBlock{start: start, end: end, indices: []int{index}})
			continue
		}
		last := &blocks[len(blocks)-1]
		last.end = end
		last.indices = append(last.indices, index)
	}

	ordered := sparseTraceData{Values: takeSparseFloat64(len(traceIndices) * decodeSamples), Samples: decodeSamples}
	workers := normalizeRenderWorkers(requestedWorkers, len(blocks))
	if workers > coalescedMaximumWorkers {
		workers = coalescedMaximumWorkers
	}
	jobs := make(chan coalescedTraceBlock)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	var readCalls, readBytes int64
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for block := range jobs {
				errMu.Lock()
				stopped := firstErr != nil
				errMu.Unlock()
				if stopped {
					continue
				}
				raw := takeCoalescedBuffer(int(block.end - block.start))
				coalescedIOSemaphore <- struct{}{}
				_, err := s.f.ReadAt(raw, block.start)
				<-coalescedIOSemaphore
				if err != nil {
					releaseCoalescedBuffer(raw)
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					continue
				}
				atomic.AddInt64(&readCalls, 1)
				atomic.AddInt64(&readBytes, int64(len(raw)))
				for _, sortedIndex := range block.indices {
					item := requests[sortedIndex]
					start := traceStart(item.trace) - block.start
					window := raw[start : start+windowBytes]
					values := ordered.trace(item.output)
					decodeSparseSamples(values, window, sampleOffsets, s.Info.FormatCode, s.Info.Endian, s.Info.BytesPerSample)
				}
				releaseCoalescedBuffer(raw)
			}
		}()
	}
	for _, block := range blocks {
		jobs <- block
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		releaseSparseFloat64(ordered.Values)
		return sparseTraceData{}, coalescedIOStats{}, firstErr
	}
	return ordered, coalescedIOStats{readCalls: int(readCalls), readBytes: readBytes}, nil
}

func traceRMS(trace []float64) float64 {
	ss, count := 0.0, 0
	for _, value := range trace {
		if !math.IsNaN(value) && !math.IsInf(value, 0) {
			ss += value * value
			count++
		}
	}
	if count > 0 && ss > 0 {
		return math.Sqrt(ss / float64(count))
	}
	return 1
}

func (s *File) renderColumnsCoalesced(o RenderOptions, traceForX func(x int) int64, sm0, sm1 int) ([]float64, float64, float64, coalescedIOStats, error) {
	columnTraces := make([]int64, o.Width)
	for x := range columnTraces {
		columnTraces[x] = traceForX(x)
	}
	loaded, ioStats, err := s.coalescedTraceWindows(columnTraces, sm0, sm1, o.Workers)
	if err != nil {
		return nil, 0, 0, coalescedIOStats{}, err
	}
	vals := make([]float64, o.Width*o.Height)
	windowSamples := sm1 - sm0 + 1
	sampleForY := make([]int, o.Height)
	for y := range sampleForY {
		if o.Height > 1 && windowSamples > 1 {
			sampleForY[y] = int(math.Round(float64(y) * float64(windowSamples-1) / float64(o.Height-1)))
		}
	}
	rms := make(map[int64]float64)
	if o.AGC {
		for traceIndex, trace := range loaded {
			rms[traceIndex] = traceRMS(trace)
		}
	}
	workers := normalizeRenderWorkers(o.Workers, o.Width)
	mins, maxs := make([]float64, workers), make([]float64, workers)
	for i := range mins {
		mins[i], maxs[i] = math.Inf(1), math.Inf(-1)
	}
	var wg sync.WaitGroup
	var doneCols, lastPct int64
	lastPct = -1
	for worker := 0; worker < workers; worker++ {
		begin, end := worker*o.Width/workers, (worker+1)*o.Width/workers
		wg.Add(1)
		go func(worker, begin, end int) {
			defer wg.Done()
			localMin, localMax := math.Inf(1), math.Inf(-1)
			for x := begin; x < end; x++ {
				traceIndex := columnTraces[x]
				trace := loaded[traceIndex]
				scale := 1.0
				if o.AGC {
					scale = rms[traceIndex]
				}
				for y, sample := range sampleForY {
					value := trace[sample]
					if o.AGC {
						value /= scale
					}
					if math.IsNaN(value) || math.IsInf(value, 0) {
						value = 0
					}
					vals[y*o.Width+x] = value
					if value < localMin {
						localMin = value
					}
					if value > localMax {
						localMax = value
					}
				}
				renderProgressTick(o.Progress, &doneCols, &lastPct, o.Width)
			}
			mins[worker], maxs[worker] = localMin, localMax
		}(worker, begin, end)
	}
	wg.Wait()
	observedMin, observedMax := math.Inf(1), math.Inf(-1)
	for i := range mins {
		observedMin = math.Min(observedMin, mins[i])
		observedMax = math.Max(observedMax, maxs[i])
	}
	if math.IsInf(observedMin, 1) {
		observedMin, observedMax = 0, 0
	}
	if o.Progress != nil {
		o.Progress(o.Width, o.Width)
	}
	return vals, observedMin, observedMax, ioStats, nil
}

func (s *File) renderInterpolatedPositionsCoalesced(traceIndices []int64, positions []float64, positionStart, positionEnd float64, o RenderOptions, sm0, sm1 int) ([]float64, float64, float64, coalescedIOStats, error) {
	loaded, ioStats, err := s.coalescedTraceWindows(traceIndices, sm0, sm1, o.Workers)
	if err != nil {
		return nil, 0, 0, coalescedIOStats{}, err
	}
	traces := make([][]float64, len(traceIndices))
	for i, traceIndex := range traceIndices {
		trace := loaded[traceIndex]
		if o.AGC {
			scale := traceRMS(trace)
			for sample := range trace {
				trace[sample] /= scale
			}
		}
		traces[i] = trace
	}
	srcHeight := sm1 - sm0 + 1
	values := make([]float64, o.Width*o.Height)
	observedMin, observedMax := math.Inf(1), math.Inf(-1)
	for x := 0; x < o.Width; x++ {
		target := positionStart + float64(x)*(positionEnd-positionStart)/float64(o.Width-1)
		right := sort.Search(len(positions), func(i int) bool { return positions[i] >= target })
		if right >= len(positions) {
			right = len(positions) - 1
		}
		left, fractionX := right, 0.0
		if right > 0 {
			left = right - 1
			if span := positions[right] - positions[left]; span > 0 {
				fractionX = (target - positions[left]) / span
			}
		}
		for y := 0; y < o.Height; y++ {
			sourceY := 0.0
			if o.Height > 1 && srcHeight > 1 {
				sourceY = float64(y) * float64(srcHeight-1) / float64(o.Height-1)
			}
			y0, y1, fractionY := int(math.Floor(sourceY)), int(math.Floor(sourceY)), 0.0
			if y0 < srcHeight-1 {
				y1, fractionY = y0+1, sourceY-float64(y0)
			}
			leftValue := traces[left][y0]*(1-fractionY) + traces[left][y1]*fractionY
			rightValue := traces[right][y0]*(1-fractionY) + traces[right][y1]*fractionY
			value := leftValue*(1-fractionX) + rightValue*fractionX
			if math.IsNaN(value) || math.IsInf(value, 0) {
				value = 0
			}
			values[y*o.Width+x] = value
			observedMin, observedMax = math.Min(observedMin, value), math.Max(observedMax, value)
		}
	}
	if math.IsInf(observedMin, 1) {
		observedMin, observedMax = 0, 0
	}
	if o.Progress != nil {
		o.Progress(o.Width, o.Width)
	}
	return values, observedMin, observedMax, ioStats, nil
}
