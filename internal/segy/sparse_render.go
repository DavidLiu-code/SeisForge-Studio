package segy

import (
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
	"os"
	"sort"
	"sync"
	"time"
)

const (
	sparseFloatMinimumClass = 10 // 1,024 float64 values.
	sparseFloatMaximumClass = 21 // 16 MiB per reusable buffer.
)

var sparseFloatPools [sparseFloatMaximumClass - sparseFloatMinimumClass + 1]sync.Pool

func sparseFloatClass(length int) int {
	if length <= 1<<(sparseFloatMinimumClass) {
		return sparseFloatMinimumClass
	}
	return bits.Len(uint(length - 1))
}

func takeSparseFloat64(length int) []float64 {
	class := sparseFloatClass(length)
	if class < sparseFloatMinimumClass || class > sparseFloatMaximumClass {
		return make([]float64, length)
	}
	pool := &sparseFloatPools[class-sparseFloatMinimumClass]
	if existing := pool.Get(); existing != nil {
		return existing.([]float64)[:length]
	}
	return make([]float64, length, 1<<class)
}

func releaseSparseFloat64(values []float64) {
	if len(values) == 0 {
		return
	}
	class := sparseFloatClass(cap(values))
	if class < sparseFloatMinimumClass || class > sparseFloatMaximumClass || cap(values) != 1<<class {
		return
	}
	sparseFloatPools[class-sparseFloatMinimumClass].Put(values[:cap(values)])
}

func decodeSparseSamples(output []float64, raw []byte, sampleOffsets []int, format int, endian Endian, bytesPerSample int) {
	decodeAt := func(outputIndex, sourceSample int) {
		begin := sourceSample * bytesPerSample
		output[outputIndex] = decodeSample(raw[begin:begin+bytesPerSample], format, endian)
	}
	// IEEE float is the normal post-stack case. Hoisting both format and
	// endian out of the sample loop materially reduces the cold-load CPU cost.
	if format == 5 {
		if endian == Big {
			if len(sampleOffsets) == 0 {
				for index := range output {
					output[index] = float64(math.Float32frombits(binary.BigEndian.Uint32(raw[index*4:])))
				}
			} else {
				for index, sample := range sampleOffsets {
					output[index] = float64(math.Float32frombits(binary.BigEndian.Uint32(raw[sample*4:])))
				}
			}
			return
		}
		if len(sampleOffsets) == 0 {
			for index := range output {
				output[index] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[index*4:])))
			}
		} else {
			for index, sample := range sampleOffsets {
				output[index] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[sample*4:])))
			}
		}
		return
	}
	if format == 2 {
		if endian == Big {
			if len(sampleOffsets) == 0 {
				for index := range output {
					output[index] = float64(int32(binary.BigEndian.Uint32(raw[index*4:])))
				}
			} else {
				for index, sample := range sampleOffsets {
					output[index] = float64(int32(binary.BigEndian.Uint32(raw[sample*4:])))
				}
			}
			return
		}
		if len(sampleOffsets) == 0 {
			for index := range output {
				output[index] = float64(int32(binary.LittleEndian.Uint32(raw[index*4:])))
			}
		} else {
			for index, sample := range sampleOffsets {
				output[index] = float64(int32(binary.LittleEndian.Uint32(raw[sample*4:])))
			}
		}
		return
	}
	if len(sampleOffsets) == 0 {
		for index := range output {
			decodeAt(index, index)
		}
		return
	}
	for index, sample := range sampleOffsets {
		decodeAt(index, sample)
	}
}

type sparseIOStats struct {
	readCalls, inputTraces, supportTraces, fallbacks int
	readBytes, logicalBytes                          int64
	decodeNanos                                      int64
	decodedSamples                                   int64
	mapped                                           bool
}

// sparseTraceData stores all decoded support traces in one compact allocation.
// This avoids one allocation per trace and the pointer-heavy [][]float64 shape
// used by the Phase 11 path.
type sparseTraceData struct {
	Values  []float64
	Samples int
}

func (data sparseTraceData) trace(index int) []float64 {
	start := index * data.Samples
	return data.Values[start : start+data.Samples]
}

// sparseSampleRow describes the source samples used by one output row.
// Top and Bottom index sparseSamplePlan.Samples rather than the SEG-Y trace.
type sparseSampleRow struct {
	Top, Bottom int
	Fraction    float64
}

// sparseSamplePlan is the vertical equivalent of TraceSupportPlan. It keeps
// only source samples that can affect the output raster. AGC deliberately does
// not use this plan because its RMS definition requires the complete selected
// trace window.
type sparseSamplePlan struct {
	Samples []int
	Rows    []sparseSampleRow
}

func buildSparseSamplePlan(sourceHeight, outputHeight int, smooth bool) sparseSamplePlan {
	plan := sparseSamplePlan{Rows: make([]sparseSampleRow, outputHeight)}
	supportBySample := make(map[int]int, outputHeight*2)
	add := func(sample int) int {
		if compact, ok := supportBySample[sample]; ok {
			return compact
		}
		compact := len(plan.Samples)
		supportBySample[sample] = compact
		plan.Samples = append(plan.Samples, sample)
		return compact
	}
	for y := 0; y < outputHeight; y++ {
		sourceY := 0.0
		if outputHeight > 1 && sourceHeight > 1 {
			sourceY = float64(y) * float64(sourceHeight-1) / float64(outputHeight-1)
		}
		if !smooth {
			sample := int(math.Round(sourceY))
			compact := add(sample)
			plan.Rows[y] = sparseSampleRow{Top: compact, Bottom: compact}
			continue
		}
		top := int(math.Floor(sourceY))
		bottom, fraction := top, 0.0
		if top < sourceHeight-1 {
			bottom, fraction = top+1, sourceY-float64(top)
		}
		plan.Rows[y] = sparseSampleRow{Top: add(top), Bottom: add(bottom), Fraction: fraction}
	}
	return plan
}

func sparseRenderWorkers(requested, width int) int {
	workers := normalizeRenderWorkers(requested, width)
	if workers > 4 {
		workers = 4
	}
	return workers
}

func (s *File) mappedTraceRanges(traceIndices []int64, sm0, sm1 int, mappedLength int64) []mappedReadRange {
	page := int64(os.Getpagesize())
	windowBytes := int64((sm1 - sm0 + 1) * s.Info.BytesPerSample)
	ranges := make([]mappedReadRange, 0, len(traceIndices))
	for _, trace := range traceIndices {
		start := s.Info.DataStart + trace*s.Info.TraceBytes + 240 + int64(sm0*s.Info.BytesPerSample)
		end := start + windowBytes
		start = start / page * page
		end = (end + page - 1) / page * page
		if end > mappedLength {
			end = mappedLength
		}
		if start >= 0 && end > start {
			ranges = append(ranges, mappedReadRange{Offset: start, Length: end - start})
		}
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Offset < ranges[j].Offset })
	write := 0
	for _, selected := range ranges {
		if write > 0 {
			previous := &ranges[write-1]
			previousEnd := previous.Offset + previous.Length
			if selected.Offset <= previousEnd {
				if end := selected.Offset + selected.Length; end > previousEnd {
					previous.Length = end - previous.Offset
				}
				continue
			}
		}
		ranges[write] = selected
		write++
	}
	return ranges[:write]
}

func (s *File) sparseTraceWindows(traceIndices []int64, sm0, sm1, requestedWorkers int, preferMapping bool, sampleOffsets []int, stage func(RenderStage)) (sparseTraceData, sparseIOStats, error) {
	if s == nil || s.f == nil || len(traceIndices) == 0 {
		return sparseTraceData{}, sparseIOStats{}, errors.New("empty sparse trace request")
	}
	windowSamples := sm1 - sm0 + 1
	if windowSamples < 1 || sm0 < 0 || sm1 >= s.Info.SamplesPerTrace {
		return sparseTraceData{}, sparseIOStats{}, errors.New("invalid sparse sample window")
	}
	for i, trace := range traceIndices {
		if trace < 0 || trace >= s.Info.TraceCount {
			return sparseTraceData{}, sparseIOStats{}, errors.New("sparse trace index out of range")
		}
		if i > 0 && trace == traceIndices[i-1] {
			return sparseTraceData{}, sparseIOStats{}, errors.New("sparse trace request contains a duplicate")
		}
	}
	decodeSamples := windowSamples
	if len(sampleOffsets) > 0 {
		decodeSamples = len(sampleOffsets)
		previous := -1
		for _, sample := range sampleOffsets {
			if sample < 0 || sample >= windowSamples || sample <= previous {
				return sparseTraceData{}, sparseIOStats{}, errors.New("invalid sparse sample support plan")
			}
			previous = sample
		}
	}
	logicalBytes := int64(len(traceIndices) * windowSamples * s.Info.BytesPerSample)
	decodedSamples := int64(len(traceIndices) * decodeSamples)
	if stage != nil {
		stage(RenderStageRead)
	}
	if mapped, ok := func() ([]byte, bool) {
		if !preferMapping {
			return nil, false
		}
		return s.ensureReadMapping()
	}(); ok {
		started := time.Now()
		platformPrefetchMapped(mapped, s.mappedTraceRanges(traceIndices, sm0, sm1, int64(len(mapped))))
		if stage != nil {
			stage(RenderStageDecodeMap)
		}
		traces := sparseTraceData{Values: takeSparseFloat64(len(traceIndices) * decodeSamples), Samples: decodeSamples}
		workers := sparseRenderWorkers(requestedWorkers, len(traceIndices))
		jobs := make(chan int)
		var wg sync.WaitGroup
		var firstErr error
		var errMu sync.Mutex
		for worker := 0; worker < workers; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for index := range jobs {
					errMu.Lock()
					stopped := firstErr != nil
					errMu.Unlock()
					if stopped {
						continue
					}
					offset := s.Info.DataStart + traceIndices[index]*s.Info.TraceBytes + 240 + int64(sm0*s.Info.BytesPerSample)
					end := offset + int64(windowSamples*s.Info.BytesPerSample)
					if offset < 0 || end > int64(len(mapped)) {
						errMu.Lock()
						if firstErr == nil {
							firstErr = errors.New("mapped trace window exceeds the source file")
						}
						errMu.Unlock()
						continue
					}
					raw := mapped[int(offset):int(end)]
					values := traces.trace(index)
					decodeSparseSamples(values, raw, sampleOffsets, s.Info.FormatCode, s.Info.Endian, s.Info.BytesPerSample)
				}
			}()
		}
		for index := range traceIndices {
			jobs <- index
		}
		close(jobs)
		wg.Wait()
		if firstErr != nil {
			releaseSparseFloat64(traces.Values)
			return sparseTraceData{}, sparseIOStats{}, firstErr
		}
		return traces, sparseIOStats{supportTraces: len(traceIndices), readBytes: logicalBytes, logicalBytes: logicalBytes,
			decodeNanos: time.Since(started).Nanoseconds(), decodedSamples: decodedSamples, mapped: true}, nil
	}

	started := time.Now()
	if stage != nil {
		stage(RenderStageDecodeMap)
	}
	traces, ioStats, err := s.sparseCoalescedTraceWindowsOrdered(traceIndices, sm0, sm1, requestedWorkers, sampleOffsets)
	if err != nil {
		return sparseTraceData{}, sparseIOStats{}, err
	}
	return traces, sparseIOStats{readCalls: ioStats.readCalls, readBytes: ioStats.readBytes, logicalBytes: logicalBytes,
		decodeNanos: time.Since(started).Nanoseconds(), decodedSamples: decodedSamples, supportTraces: len(traceIndices), fallbacks: 1}, nil
}

func normalizeSparseAGC(traces sparseTraceData, enabled bool, requestedWorkers int) {
	traceCount := 0
	if traces.Samples > 0 {
		traceCount = len(traces.Values) / traces.Samples
	}
	if !enabled || traceCount == 0 {
		return
	}
	workers := sparseRenderWorkers(requestedWorkers, traceCount)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		begin, end := worker*traceCount/workers, (worker+1)*traceCount/workers
		wg.Add(1)
		go func(begin, end int) {
			defer wg.Done()
			for index := begin; index < end; index++ {
				trace := traces.trace(index)
				scale := traceRMS(trace)
				for sample := range trace {
					trace[sample] /= scale
				}
			}
		}(begin, end)
	}
	wg.Wait()
}

func (s *File) renderColumnsSparse(o RenderOptions, traceForX func(x int) int64, inputTraceCount, sm0, sm1 int, preferMapping bool) ([]float64, float64, float64, sparseIOStats, error) {
	columns := make([]int, o.Width)
	supportByTrace := make(map[int64]int, o.Width)
	support := make([]int64, 0, o.Width)
	for x := range columns {
		trace := traceForX(x)
		index, ok := supportByTrace[trace]
		if !ok {
			index = len(support)
			supportByTrace[trace] = index
			support = append(support, trace)
		}
		columns[x] = index
	}
	if o.Stage != nil {
		o.Stage(RenderStageSupportPlan)
	}
	windowSamples := sm1 - sm0 + 1
	var samplePlan sparseSamplePlan
	var sampleOffsets []int
	if !o.AGC {
		samplePlan = buildSparseSamplePlan(windowSamples, o.Height, false)
		sampleOffsets = samplePlan.Samples
	}
	traces, stats, err := s.sparseTraceWindows(support, sm0, sm1, o.Workers, preferMapping, sampleOffsets, o.Stage)
	if err != nil {
		return nil, 0, 0, sparseIOStats{}, err
	}
	defer releaseSparseFloat64(traces.Values)
	stats.inputTraces = inputTraceCount
	normalizeSparseAGC(traces, o.AGC, o.Workers)
	sampleForY := make([]int, o.Height)
	if o.AGC {
		for y := range sampleForY {
			if o.Height > 1 && windowSamples > 1 {
				sampleForY[y] = int(math.Round(float64(y) * float64(windowSamples-1) / float64(o.Height-1)))
			}
		}
	} else {
		for y, row := range samplePlan.Rows {
			sampleForY[y] = row.Top
		}
	}
	values := takeSparseFloat64(o.Width * o.Height)
	workers := sparseRenderWorkers(o.Workers, o.Width)
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
				trace := traces.trace(columns[x])
				for y, sample := range sampleForY {
					value := trace[sample]
					if math.IsNaN(value) || math.IsInf(value, 0) {
						value = 0
					}
					values[y*o.Width+x] = value
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
		if mins[i] < observedMin {
			observedMin = mins[i]
		}
		if maxs[i] > observedMax {
			observedMax = maxs[i]
		}
	}
	if math.IsInf(observedMin, 1) {
		observedMin, observedMax = 0, 0
	}
	if o.Progress != nil {
		o.Progress(o.Width, o.Width)
	}
	return values, observedMin, observedMax, stats, nil
}

func (s *File) renderInterpolatedPositionsSparse(traceIndices []int64, positions []float64, positionStart, positionEnd float64, o RenderOptions, sm0, sm1 int, preferMapping bool) ([]float64, float64, float64, sparseIOStats, error) {
	plan, err := BuildTraceSupportPlan(traceIndices, positions, positionStart, positionEnd, o.Width)
	if err != nil {
		return nil, 0, 0, sparseIOStats{}, err
	}
	if o.Stage != nil {
		o.Stage(RenderStageSupportPlan)
	}
	sourceHeight := sm1 - sm0 + 1
	var samplePlan sparseSamplePlan
	var sampleOffsets []int
	if !o.AGC {
		samplePlan = buildSparseSamplePlan(sourceHeight, o.Height, true)
		sampleOffsets = samplePlan.Samples
	}
	traces, stats, err := s.sparseTraceWindows(plan.TraceIndices, sm0, sm1, o.Workers, preferMapping, sampleOffsets, o.Stage)
	if err != nil {
		return nil, 0, 0, sparseIOStats{}, err
	}
	defer releaseSparseFloat64(traces.Values)
	stats.inputTraces = plan.InputTraceCount
	normalizeSparseAGC(traces, o.AGC, o.Workers)
	values := takeSparseFloat64(o.Width * o.Height)
	workers := sparseRenderWorkers(o.Workers, o.Width)
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
				column := plan.Columns[x]
				left, right := traces.trace(column.Left), traces.trace(column.Right)
				for y := 0; y < o.Height; y++ {
					y0, y1, fractionY := 0, 0, 0.0
					if o.AGC {
						sourceY := 0.0
						if o.Height > 1 && sourceHeight > 1 {
							sourceY = float64(y) * float64(sourceHeight-1) / float64(o.Height-1)
						}
						y0, y1 = int(math.Floor(sourceY)), int(math.Floor(sourceY))
						if y0 < sourceHeight-1 {
							y1, fractionY = y0+1, sourceY-float64(y0)
						}
					} else {
						row := samplePlan.Rows[y]
						y0, y1, fractionY = row.Top, row.Bottom, row.Fraction
					}
					leftValue := left[y0]*(1-fractionY) + left[y1]*fractionY
					rightValue := right[y0]*(1-fractionY) + right[y1]*fractionY
					value := leftValue*(1-column.Fraction) + rightValue*column.Fraction
					if math.IsNaN(value) || math.IsInf(value, 0) {
						value = 0
					}
					values[y*o.Width+x] = value
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
		if mins[i] < observedMin {
			observedMin = mins[i]
		}
		if maxs[i] > observedMax {
			observedMax = maxs[i]
		}
	}
	if math.IsInf(observedMin, 1) {
		observedMin, observedMax = 0, 0
	}
	if o.Progress != nil {
		o.Progress(o.Width, o.Width)
	}
	return values, observedMin, observedMax, stats, nil
}
