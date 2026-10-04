package segy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
)

type Endian int

const (
	Big Endian = iota
	Little
)

type DisplayMode int

const (
	DisplayNearest DisplayMode = iota
	DisplaySmooth
	DisplayAdaptive
)

type RenderReadStrategy int

const (
	ReadStrategyDefault RenderReadStrategy = iota
	ReadStrategyCoalesced
	// ReadStrategySparseMapped plans the exact traces needed by the output
	// raster, prefers a read-only file mapping, and falls back to bounded
	// coalesced ReadAt without changing rendered samples.
	ReadStrategySparseMapped
	// ReadStrategySparseCoalesced forces the exact bounded ReadAt fallback and
	// is primarily used for adaptive selection and regression measurement.
	ReadStrategySparseCoalesced
)

type RenderStage int

const (
	RenderStageSupportPlan RenderStage = iota + 1
	RenderStageRead
	RenderStageDecodeMap
)

type Info struct {
	Path                string
	FileSize            int64
	Endian              Endian
	SampleIntervalUS    int
	SamplesPerTrace     int
	FormatCode          int
	BytesPerSample      int
	ExtendedTextHeaders int
	DataStart           int64
	TraceBytes          int64
	TraceCount          int64
}

type File struct {
	f    *os.File
	Info Info

	mappingMu       sync.Mutex
	mappingData     []byte
	mappingClose    func() error
	mappingTried    bool
	mappingDisabled bool // package tests can force the exact ReadAt fallback.
}

func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.Size() < 3600 {
		f.Close()
		return nil, errors.New("file is too small to be a SEG-Y file")
	}
	hdr := make([]byte, 400)
	if _, err = f.ReadAt(hdr, 3200); err != nil {
		f.Close()
		return nil, err
	}
	endian, dt, ns, format, bps, ok := detectHeader(hdr)
	if !ok {
		f.Close()
		return nil, errors.New("could not identify a plausible SEG-Y binary header")
	}
	ext := signed16(hdr[304:306], endian)
	dataStart := int64(3600)
	if ext > 0 && ext < 10000 {
		dataStart += int64(ext) * 3200
	}
	traceBytes := int64(240 + ns*bps)
	if traceBytes <= 240 || dataStart > st.Size() {
		f.Close()
		return nil, errors.New("invalid trace geometry")
	}
	ntr := (st.Size() - dataStart) / traceBytes
	if ntr < 1 {
		f.Close()
		return nil, errors.New("no complete traces found")
	}
	return &File{f: f, Info: Info{
		Path: path, FileSize: st.Size(), Endian: endian, SampleIntervalUS: dt,
		SamplesPerTrace: ns, FormatCode: format, BytesPerSample: bps,
		ExtendedTextHeaders: max(ext, 0), DataStart: dataStart, TraceBytes: traceBytes, TraceCount: ntr,
	}}, nil
}

func (s *File) Close() error {
	if s == nil {
		return nil
	}
	s.mappingMu.Lock()
	closeMapping := s.mappingClose
	s.mappingData, s.mappingClose = nil, nil
	s.mappingMu.Unlock()
	var mappingErr error
	if closeMapping != nil {
		mappingErr = closeMapping()
	}
	var fileErr error
	if s.f != nil {
		fileErr = s.f.Close()
	}
	return errors.Join(mappingErr, fileErr)
}

func detectHeader(h []byte) (Endian, int, int, int, int, bool) {
	for _, e := range []Endian{Big, Little} {
		dt := u16(h[16:18], e)
		ns := u16(h[20:22], e)
		fc := u16(h[24:26], e)
		bps := bytesPerSample(fc)
		if dt > 0 && dt <= 1000000 && ns > 0 && ns <= 65535 && bps > 0 {
			return e, dt, ns, fc, bps, true
		}
	}
	return Big, 0, 0, 0, 0, false
}

func bytesPerSample(code int) int {
	switch code {
	case 1, 2, 4, 5, 10:
		return 4
	case 3, 11:
		return 2
	case 6, 9, 12:
		return 8
	case 7, 15:
		return 3
	case 8, 16:
		return 1
	default:
		return 0
	}
}

func u16(b []byte, e Endian) int {
	if e == Big {
		return int(binary.BigEndian.Uint16(b))
	}
	return int(binary.LittleEndian.Uint16(b))
}
func signed16(b []byte, e Endian) int { return int(int16(u16(b, e))) }
func u32(b []byte, e Endian) uint32 {
	if e == Big {
		return binary.BigEndian.Uint32(b)
	}
	return binary.LittleEndian.Uint32(b)
}
func u64(b []byte, e Endian) uint64 {
	if e == Big {
		return binary.BigEndian.Uint64(b)
	}
	return binary.LittleEndian.Uint64(b)
}

func (s *File) readTraceWindow(trace int64, sampleStart, sampleEnd int) ([]float64, error) {
	if trace < 0 || trace >= s.Info.TraceCount {
		return nil, errors.New("trace index out of range")
	}
	if sampleStart < 0 || sampleEnd < sampleStart || sampleEnd >= s.Info.SamplesPerTrace {
		return nil, errors.New("sample window out of range")
	}
	n := sampleEnd - sampleStart + 1
	bps := s.Info.BytesPerSample
	raw := make([]byte, n*bps)
	off := s.Info.DataStart + trace*s.Info.TraceBytes + 240 + int64(sampleStart*bps)
	if _, err := s.f.ReadAt(raw, off); err != nil {
		return nil, err
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = decodeSample(raw[i*bps:(i+1)*bps], s.Info.FormatCode, s.Info.Endian)
	}
	return out, nil
}

// ReadTraceWindow reads one trace sample window using 64-bit file offsets.
// The sample bounds are inclusive. It is exported for lightweight QC tools
// such as the Limage spectrum viewer; callers should still subsample traces
// rather than bulk-loading a large volume.
func (s *File) ReadTraceWindow(trace int64, sampleStart, sampleEnd int) ([]float64, error) {
	return s.readTraceWindow(trace, sampleStart, sampleEnd)
}

func (s *File) readTrace(trace int64) ([]float64, error) {
	return s.readTraceWindow(trace, 0, s.Info.SamplesPerTrace-1)
}

func (s *File) ReadSample(trace int64, sample int) (float64, error) {
	if trace < 0 || trace >= s.Info.TraceCount || sample < 0 || sample >= s.Info.SamplesPerTrace {
		return 0, errors.New("sample index out of range")
	}
	off := s.Info.DataStart + trace*s.Info.TraceBytes + 240 + int64(sample*s.Info.BytesPerSample)
	buf := make([]byte, s.Info.BytesPerSample)
	if _, err := s.f.ReadAt(buf, off); err != nil {
		return 0, err
	}
	return decodeSample(buf, s.Info.FormatCode, s.Info.Endian), nil
}

func decodeSample(b []byte, code int, e Endian) float64 {
	switch code {
	case 1:
		return ibmFloat(u32(b, e))
	case 2:
		return float64(int32(u32(b, e)))
	case 3:
		return float64(int16(u16(b, e)))
	case 5:
		return float64(math.Float32frombits(u32(b, e)))
	case 6:
		return math.Float64frombits(u64(b, e))
	case 7:
		var v int32
		if e == Big {
			v = int32(b[0])<<16 | int32(b[1])<<8 | int32(b[2])
		} else {
			v = int32(b[2])<<16 | int32(b[1])<<8 | int32(b[0])
		}
		if v&0x800000 != 0 {
			v |= ^int32(0xffffff)
		}
		return float64(v)
	case 8:
		return float64(int8(b[0]))
	case 9:
		return float64(int64(u64(b, e)))
	case 10:
		return float64(u32(b, e))
	case 11:
		return float64(uint16(u16(b, e)))
	case 12:
		return float64(u64(b, e))
	case 15:
		if e == Big {
			return float64(uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]))
		}
		return float64(uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0]))
	case 16:
		return float64(b[0])
	}
	return math.NaN()
}

func ibmFloat(v uint32) float64 {
	if v == 0 {
		return 0
	}
	sign := 1.0
	if v&0x80000000 != 0 {
		sign = -1
	}
	exp := int((v>>24)&0x7f) - 64
	frac := float64(v&0x00ffffff) / float64(0x01000000)
	return sign * frac * math.Pow(16, float64(exp))
}

// RenderOptions describes the portion of the seismic section to display and
// the amplitude mapping used to convert samples to 8-bit palette indices.
// TraceStart/TraceEnd and SampleStart/SampleEnd are inclusive. Negative End
// values mean "use the last available trace/sample".
type RenderOptions struct {
	Width, Height          int
	AGC                    bool
	ClipPercent            float64
	GainPercent            float64
	UseValueLimits         bool
	MinValue, MaxValue     float64
	TraceStart, TraceEnd   int64
	TraceStep              int64 // 1 reads every trace; N reads every Nth trace.
	SampleStart, SampleEnd int
	// Workers controls trace-window decode concurrency. Zero selects an
	// adaptive default (up to 8 workers). The file is accessed with ReadAt,
	// so independent trace windows can be decoded safely in parallel.
	Workers int
	// DisplayMode controls how zoomed images are resampled. Nearest preserves
	// legacy pixel blocks, Smooth forces bilinear interpolation, and Adaptive
	// automatically switches to bilinear when the visible trace/sample grid is
	// smaller than the target display raster.
	DisplayMode DisplayMode
	// ReadStrategyCoalesced groups nearby trace windows into bounded sequential
	// ReadAt calls. ReadStrategySparseMapped additionally omits traces that
	// cannot affect the output raster and prefers a read-only file mapping. The
	// default retains the legacy per-trace path.
	ReadStrategy RenderReadStrategy
	// Progress is optional and reports completed display columns / total.
	// Callbacks may arrive from worker goroutines; UI callers should marshal
	// them back to their event thread (for example with PostMessage).
	Progress func(done, total int)
	// Stage is optional and reports coarse sparse-render phases. Like Progress,
	// callbacks can arrive from worker goroutines.
	Stage func(RenderStage)
}

type RenderStats struct {
	ObservedMin, ObservedMax float64
	MapMin, MapMax           float64
	TraceStart, TraceEnd     int64
	TraceStep                int64
	SampleStart, SampleEnd   int
	IOReadCalls              int
	IOCoalescedBlocks        int
	IOReadBytes              int64
	CoalescedIO              bool
	SparseIO                 bool
	MappedIO                 bool
	InputTraceCount          int
	SupportTraceCount        int
	IOLogicalBytes           int64
	IODecodeNanos            int64
	DecodedSampleCount       int64
	IOFallbacks              int
}

// RenderWithOptions returns palette indices in the range [0,255]. Index 0
// corresponds to MapMax and index 255 to MapMin, matching the legacy Fimage
// color-table orientation recovered from the original executable.
func normalizeRenderWorkers(requested, width int) int {
	if requested <= 0 {
		requested = runtime.NumCPU()
	}
	// More workers are useful on NVMe/SSD and for IBM-float decoding, but a
	// conservative cap avoids turning a mechanical disk into random-I/O soup.
	if requested > 8 {
		requested = 8
	}
	if requested < 1 {
		requested = 1
	}
	if width > 0 && requested > width {
		requested = width
	}
	return requested
}

func renderProgressTick(cb func(done, total int), done *int64, lastPct *int64, total int) {
	if cb == nil || total <= 0 {
		return
	}
	d := atomic.AddInt64(done, 1)
	pct := d * 100 / int64(total)
	for {
		old := atomic.LoadInt64(lastPct)
		if pct <= old {
			return
		}
		if atomic.CompareAndSwapInt64(lastPct, old, pct) {
			cb(int(d), total)
			return
		}
	}
}

// renderColumnsParallel decodes the requested trace window into a display-sized
// floating-point raster. Each worker owns a contiguous x-range so repeated
// source traces remain adjacent and can be reused locally. os.File.ReadAt is
// safe for concurrent calls; this gives the first-load path real multi-core
// decoding while retaining mostly sequential access within each worker.
func (s *File) renderColumnsParallel(o RenderOptions, traceForX func(x int) int64, sm0, sm1 int) ([]float64, float64, float64, error) {
	vals := make([]float64, o.Width*o.Height)
	windowSamples := sm1 - sm0 + 1
	workers := normalizeRenderWorkers(o.Workers, o.Width)
	mins := make([]float64, workers)
	maxs := make([]float64, workers)
	for i := range mins {
		mins[i] = math.Inf(1)
		maxs[i] = math.Inf(-1)
	}
	var firstErr error
	var errMu sync.Mutex
	var wg sync.WaitGroup
	var doneCols, lastPct int64
	lastPct = -1
	for wi := 0; wi < workers; wi++ {
		x0 := wi * o.Width / workers
		x1 := (wi + 1) * o.Width / workers
		wg.Add(1)
		go func(worker, begin, end int) {
			defer wg.Done()
			lastTrace := int64(-1)
			var trace []float64
			localMin, localMax := math.Inf(1), math.Inf(-1)
			for x := begin; x < end; x++ {
				errMu.Lock()
				stop := firstErr != nil
				errMu.Unlock()
				if stop {
					return
				}
				tr := traceForX(x)
				if tr != lastTrace {
					var err error
					trace, err = s.readTraceWindow(tr, sm0, sm1)
					if err != nil {
						errMu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						errMu.Unlock()
						return
					}
					lastTrace = tr
				}
				rms := 1.0
				if o.AGC {
					var ss float64
					var n int
					for _, v := range trace {
						if !math.IsNaN(v) && !math.IsInf(v, 0) {
							ss += v * v
							n++
						}
					}
					if n > 0 && ss > 0 {
						rms = math.Sqrt(ss / float64(n))
					}
				}
				for y := 0; y < o.Height; y++ {
					j := 0
					if o.Height > 1 && windowSamples > 1 {
						j = int(math.Round(float64(y) * float64(windowSamples-1) / float64(o.Height-1)))
					}
					v := trace[j]
					if o.AGC {
						v /= rms
					}
					if math.IsNaN(v) || math.IsInf(v, 0) {
						v = 0
					}
					vals[y*o.Width+x] = v
					if v < localMin {
						localMin = v
					}
					if v > localMax {
						localMax = v
					}
				}
				renderProgressTick(o.Progress, &doneCols, &lastPct, o.Width)
			}
			mins[worker], maxs[worker] = localMin, localMax
		}(wi, x0, x1)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, 0, 0, firstErr
	}
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
	return vals, observedMin, observedMax, nil
}

func mapRenderValues(vals []float64, observedMin, observedMax float64, o RenderOptions) ([]byte, float64, float64) {
	lo, hi := observedMin, observedMax
	if o.UseValueLimits && o.MaxValue > o.MinValue {
		lo, hi = o.MinValue, o.MaxValue
	} else {
		g := o.GainPercent
		if g < 0 {
			g = 0
		}
		if g > 49 {
			g = 49
		}
		if len(vals) > 0 && g > 0 {
			ordered := append([]float64(nil), vals...)
			n := len(ordered)
			li := int(float64(n) * g / 100.0)
			hiIdx := int(float64(n) * (100.0 - g) / 100.0)
			if li < 0 {
				li = 0
			}
			if li >= n {
				li = n - 1
			}
			if hiIdx < 0 {
				hiIdx = 0
			}
			if hiIdx >= n {
				hiIdx = n - 1
			}
			lo, hi = selectFloat64Order(ordered, li), selectFloat64Order(ordered, hiIdx)
			if hi < lo {
				lo, hi = hi, lo
			}
		}
	}
	if hi <= lo {
		hi = lo + 1
	}
	pix := make([]byte, len(vals))
	den := hi - lo
	for i, v := range vals {
		if v > hi {
			v = hi
		}
		if v < lo {
			v = lo
		}
		idx := int(math.Round(255.0 * (hi - v) / den))
		if idx < 0 {
			idx = 0
		}
		if idx > 255 {
			idx = 255
		}
		pix[i] = byte(idx)
	}
	return pix, lo, hi
}

func selectFloat64Order(values []float64, k int) float64 {
	if len(values) == 0 {
		return 0
	}
	if k < 0 {
		k = 0
	}
	if k >= len(values) {
		k = len(values) - 1
	}
	for _, value := range values {
		if math.IsNaN(value) {
			sort.Float64s(values)
			return values[k]
		}
	}
	lo, hi := 0, len(values)-1
	for lo < hi {
		middle := lo + (hi-lo)/2
		a, b, c := values[lo], values[middle], values[hi]
		if a > b {
			a, b = b, a
		}
		if b > c {
			b, c = c, b
		}
		if a > b {
			b = a
		}
		pivot := b
		less, scan, greater := lo, lo, hi
		for scan <= greater {
			switch {
			case values[scan] < pivot:
				values[less], values[scan] = values[scan], values[less]
				less++
				scan++
			case values[scan] > pivot:
				values[scan], values[greater] = values[greater], values[scan]
				greater--
			default:
				scan++
			}
		}
		if k < less {
			hi = less - 1
		} else if k > greater {
			lo = greater + 1
		} else {
			return values[k]
		}
	}
	return values[lo]
}

func shouldUseSmooth(mode DisplayMode, srcW, srcH, dstW, dstH int) bool {
	switch mode {
	case DisplaySmooth:
		return true
	case DisplayAdaptive:
		if srcW <= 1 || srcH <= 1 {
			return false
		}
		// Use bilinear interpolation when the current visible grid is being
		// enlarged noticeably, which is exactly where nearest-neighbour produces
		// the obvious mosaic/blocky artefacts after zooming.
		upscaleX := srcW < dstW
		upscaleY := srcH < dstH
		if !(upscaleX || upscaleY) {
			return false
		}
		// Keep the slow path only for manageable zoom windows.
		return int64(srcW)*int64(srcH) <= 8_000_000
	default:
		return false
	}
}

func bilinearResampleGrid(src []float64, srcW, srcH, dstW, dstH int) ([]float64, float64, float64) {
	vals := make([]float64, dstW*dstH)
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return vals, 0, 0
	}
	minV, maxV := math.Inf(1), math.Inf(-1)
	for y := 0; y < dstH; y++ {
		sy := 0.0
		if dstH > 1 && srcH > 1 {
			sy = float64(y) * float64(srcH-1) / float64(dstH-1)
		}
		y0 := int(math.Floor(sy))
		if y0 < 0 {
			y0 = 0
		}
		if y0 >= srcH {
			y0 = srcH - 1
		}
		y1 := y0
		fy := 0.0
		if y0 < srcH-1 {
			y1 = y0 + 1
			fy = sy - float64(y0)
		}
		for x := 0; x < dstW; x++ {
			sx := 0.0
			if dstW > 1 && srcW > 1 {
				sx = float64(x) * float64(srcW-1) / float64(dstW-1)
			}
			x0 := int(math.Floor(sx))
			if x0 < 0 {
				x0 = 0
			}
			if x0 >= srcW {
				x0 = srcW - 1
			}
			x1 := x0
			fx := 0.0
			if x0 < srcW-1 {
				x1 = x0 + 1
				fx = sx - float64(x0)
			}
			v00 := src[y0*srcW+x0]
			v10 := src[y0*srcW+x1]
			v01 := src[y1*srcW+x0]
			v11 := src[y1*srcW+x1]
			v0 := v00*(1.0-fx) + v10*fx
			v1 := v01*(1.0-fx) + v11*fx
			v := v0*(1.0-fy) + v1*fy
			vals[y*dstW+x] = v
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}
	}
	if math.IsInf(minV, 1) {
		minV, maxV = 0, 0
	}
	return vals, minV, maxV
}

func (s *File) renderInterpolatedTraces(traceIndices []int64, o RenderOptions, sm0, sm1 int) ([]float64, float64, float64, error) {
	srcW := len(traceIndices)
	srcH := sm1 - sm0 + 1
	src := make([]float64, srcW*srcH)
	for i, tr := range traceIndices {
		trace, err := s.readTraceWindow(tr, sm0, sm1)
		if err != nil {
			return nil, 0, 0, err
		}
		rms := 1.0
		if o.AGC {
			ss := 0.0
			n := 0
			for _, v := range trace {
				if !math.IsNaN(v) && !math.IsInf(v, 0) {
					ss += v * v
					n++
				}
			}
			if n > 0 && ss > 0 {
				rms = math.Sqrt(ss / float64(n))
			}
		}
		base := i
		for j, v := range trace {
			if o.AGC {
				v /= rms
			}
			if math.IsNaN(v) || math.IsInf(v, 0) {
				v = 0
			}
			src[j*srcW+base] = v
		}
	}
	vals, lo, hi := bilinearResampleGrid(src, srcW, srcH, o.Width, o.Height)
	return vals, lo, hi, nil
}

func renderInterpolatedDiffGrid(aTraces, bTraces [][]float64, o RenderOptions) ([]float64, float64, float64) {
	srcW := len(aTraces)
	srcH := 0
	if srcW > 0 {
		srcH = len(aTraces[0])
	}
	src := make([]float64, srcW*srcH)
	for i := 0; i < srcW; i++ {
		rms := 1.0
		if o.AGC {
			ss := 0.0
			n := 0
			for j := 0; j < srcH; j++ {
				d := aTraces[i][j] - bTraces[i][j]
				if !math.IsNaN(d) && !math.IsInf(d, 0) {
					ss += d * d
					n++
				}
			}
			if n > 0 && ss > 0 {
				rms = math.Sqrt(ss / float64(n))
			}
		}
		for j := 0; j < srcH; j++ {
			v := aTraces[i][j] - bTraces[i][j]
			if o.AGC {
				v /= rms
			}
			if math.IsNaN(v) || math.IsInf(v, 0) {
				v = 0
			}
			src[j*srcW+i] = v
		}
	}
	return bilinearResampleGrid(src, srcW, srcH, o.Width, o.Height)
}

func (s *File) RenderWithOptions(o RenderOptions) ([]byte, RenderStats, error) {
	if o.Width < 2 || o.Height < 2 {
		return nil, RenderStats{}, errors.New("invalid image dimensions")
	}
	if o.ClipPercent <= 0 {
		o.ClipPercent = 99
	}
	nt := s.Info.TraceCount
	ns := s.Info.SamplesPerTrace
	tr0, tr1 := o.TraceStart, o.TraceEnd
	if tr0 < 0 {
		tr0 = 0
	}
	if tr1 < 0 || tr1 >= nt {
		tr1 = nt - 1
	}
	if tr0 >= nt || tr0 > tr1 {
		return nil, RenderStats{}, errors.New("invalid trace range")
	}
	step := o.TraceStep
	if step < 1 {
		step = 1
	}
	selectedTraceCount := (tr1-tr0)/step + 1
	actualTr1 := tr0 + (selectedTraceCount-1)*step
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
	traceForX := func(x int) int64 {
		selectedIndex := int64(0)
		if o.Width > 1 && selectedTraceCount > 1 {
			selectedIndex = int64(math.Round(float64(x) * float64(selectedTraceCount-1) / float64(o.Width-1)))
		}
		tr := tr0 + selectedIndex*step
		if tr > actualTr1 {
			tr = actualTr1
		}
		return tr
	}
	var vals []float64
	var observedMin, observedMax float64
	var err error
	if shouldUseSmooth(o.DisplayMode, int(selectedTraceCount), sm1-sm0+1, o.Width, o.Height) {
		traceIndices := make([]int64, int(selectedTraceCount))
		for i := range traceIndices {
			traceIndices[i] = tr0 + int64(i)*step
		}
		vals, observedMin, observedMax, err = s.renderInterpolatedTraces(traceIndices, o, sm0, sm1)
	} else {
		vals, observedMin, observedMax, err = s.renderColumnsParallel(o, traceForX, sm0, sm1)
	}
	if err != nil {
		return nil, RenderStats{}, err
	}
	pix, lo, hi := mapRenderValues(vals, observedMin, observedMax, o)
	st := RenderStats{ObservedMin: observedMin, ObservedMax: observedMax, MapMin: lo, MapMax: hi,
		TraceStart: tr0, TraceEnd: actualTr1, TraceStep: step, SampleStart: sm0, SampleEnd: sm1}
	return pix, st, nil
}

// Render preserves the original prototype API.
func (s *File) Render(width, height int, agc bool, clipPercent float64) ([]byte, error) {
	p, _, err := s.RenderWithOptions(RenderOptions{Width: width, Height: height, AGC: agc, ClipPercent: clipPercent,
		TraceStart: 0, TraceEnd: -1, SampleStart: 0, SampleEnd: -1})
	return p, err
}

func percentile(a []float64, p float64) float64 {
	if len(a) == 0 {
		return 1
	}
	if p <= 0 {
		p = 99
	}
	if p > 100 {
		p = 100
	}
	b := append([]float64(nil), a...)
	sort.Float64s(b)
	i := int(math.Round((p / 100) * float64(len(b)-1)))
	if i < 0 {
		i = 0
	}
	if i >= len(b) {
		i = len(b) - 1
	}
	return b[i]
}

func (i Info) String() string {
	endian := "big-endian"
	if i.Endian == Little {
		endian = "little-endian"
	}
	return fmt.Sprintf("%s\nSize: %d bytes\nTraces: %d\nSamples/trace: %d\nSample interval: %d us\nFormat code: %d (%d bytes/sample)\nByte order: %s\nData start: %d", i.Path, i.FileSize, i.TraceCount, i.SamplesPerTrace, i.SampleIntervalUS, i.FormatCode, i.BytesPerSample, endian, i.DataStart)
}

// RenderTraceIndices renders an arbitrary ordered list of traces. It is used
// by Limage's 3-D Inline/Crossline comparison views, where traces belonging to
// one geometry line are not guaranteed to be contiguous in the SEG-Y file.
func (s *File) RenderTraceIndices(traceIndices []int64, o RenderOptions) ([]byte, RenderStats, error) {
	vals, st, err := s.RenderTraceIndicesValues(traceIndices, o)
	if err != nil {
		return nil, RenderStats{}, err
	}
	pix, lo, hi := mapRenderValues(vals, st.ObservedMin, st.ObservedMax, o)
	st.MapMin, st.MapMax = lo, hi
	return pix, st, nil
}

// RenderTraceIndicesValues renders the same float64 display raster as
// RenderTraceIndices before palette-index mapping. Volume views use it to
// compute one shared normalization range for all three orthogonal slices.
// The returned values already include the requested resampling and AGC rules.
func (s *File) RenderTraceIndicesValues(traceIndices []int64, o RenderOptions) ([]float64, RenderStats, error) {
	if o.Width < 2 || o.Height < 2 {
		return nil, RenderStats{}, errors.New("invalid image dimensions")
	}
	if len(traceIndices) == 0 {
		return nil, RenderStats{}, errors.New("empty trace list")
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
	traceForX := func(x int) int64 {
		i := 0
		if o.Width > 1 && len(traceIndices) > 1 {
			i = int(math.Round(float64(x) * float64(len(traceIndices)-1) / float64(o.Width-1)))
		}
		if i < 0 {
			i = 0
		}
		if i >= len(traceIndices) {
			i = len(traceIndices) - 1
		}
		return traceIndices[i]
	}
	var vals []float64
	var observedMin, observedMax float64
	var err error
	var sparseStats sparseIOStats
	var ioStats coalescedIOStats
	// Keep the sparse read strategy consistent with RenderTracePositions.  The
	// prestack gather renderer supplies physical trace indices (rather than
	// monotonically spaced geometry positions), so it comes through this API.
	// Previously ReadStrategySparseMapped was silently ignored here and the
	// caller fell back to the legacy full-column reader.  Besides wasting I/O,
	// that made it impossible to compare the two paths when diagnosing dark
	// pixels during gather zoom.  The sparse path uses the same interpolation
	// and sample support plans as the crooked renderer and therefore preserves
	// pixel values exactly.
	if o.ReadStrategy == ReadStrategySparseMapped || o.ReadStrategy == ReadStrategySparseCoalesced {
		if shouldUseSmooth(o.DisplayMode, len(traceIndices), sm1-sm0+1, o.Width, o.Height) {
			positions := make([]float64, len(traceIndices))
			for i := range positions {
				positions[i] = float64(i)
			}
			values, minValue, maxValue, stats, sparseErr := s.renderInterpolatedPositionsSparse(traceIndices, positions, 0, float64(len(traceIndices)-1), o, sm0, sm1, o.ReadStrategy == ReadStrategySparseMapped)
			vals, observedMin, observedMax, sparseStats, err = values, minValue, maxValue, stats, sparseErr
		} else {
			values, minValue, maxValue, stats, sparseErr := s.renderColumnsSparse(o, traceForX, len(traceIndices), sm0, sm1, o.ReadStrategy == ReadStrategySparseMapped)
			vals, observedMin, observedMax, sparseStats, err = values, minValue, maxValue, stats, sparseErr
		}
	} else if o.ReadStrategy == ReadStrategyCoalesced {
		// Preserve the existing coalesced implementation for callers that do not
		// need the output-driven sparse support plan.
		vals, observedMin, observedMax, ioStats, err = s.renderColumnsCoalesced(o, traceForX, sm0, sm1)
	} else if shouldUseSmooth(o.DisplayMode, len(traceIndices), sm1-sm0+1, o.Width, o.Height) {
		vals, observedMin, observedMax, err = s.renderInterpolatedTraces(traceIndices, o, sm0, sm1)
	} else {
		vals, observedMin, observedMax, err = s.renderColumnsParallel(o, traceForX, sm0, sm1)
	}
	if err != nil {
		return nil, RenderStats{}, err
	}
	st := RenderStats{ObservedMin: observedMin, ObservedMax: observedMax, SampleStart: sm0, SampleEnd: sm1, TraceStep: 1,
		IOReadCalls: ioStats.readCalls, IOCoalescedBlocks: ioStats.readCalls, IOReadBytes: ioStats.readBytes,
		CoalescedIO: o.ReadStrategy == ReadStrategyCoalesced}
	if o.ReadStrategy == ReadStrategySparseMapped || o.ReadStrategy == ReadStrategySparseCoalesced {
		st.IOReadCalls, st.IOCoalescedBlocks, st.IOReadBytes = sparseStats.readCalls, sparseStats.readCalls, sparseStats.readBytes
		st.CoalescedIO, st.SparseIO, st.MappedIO = sparseStats.fallbacks > 0, true, sparseStats.mapped
		st.InputTraceCount, st.SupportTraceCount = sparseStats.inputTraces, sparseStats.supportTraces
		st.IOLogicalBytes, st.IODecodeNanos, st.DecodedSampleCount, st.IOFallbacks = sparseStats.logicalBytes, sparseStats.decodeNanos, sparseStats.decodedSamples, sparseStats.fallbacks
	}
	st.TraceStart = traceIndices[0]
	st.TraceEnd = traceIndices[len(traceIndices)-1]
	return vals, st, nil
}

// MapAmplitudeValues maps a prepared float64 raster through an explicit
// shared display range. It preserves the exact palette-index orientation and
// rounding used by all existing SEG-Y render entry points.
func MapAmplitudeValues(values []float64, minimum, maximum float64) []byte {
	pixels, _, _ := mapRenderValues(values, minimum, maximum, RenderOptions{
		UseValueLimits: true,
		MinValue:       minimum,
		MaxValue:       maximum,
	})
	return pixels
}

// RenderTraceDifferencePairs renders A-B for two ordered, coordinate-matched
// trace lists. It is intended for processing QC where A and B share the same
// sample interval. The residual display range is forced symmetric about zero
// so positive and negative processing differences remain visually balanced.
func RenderTraceDifferencePairs(a, b *File, tracesA, tracesB []int64, o RenderOptions) ([]byte, RenderStats, error) {
	if a == nil || b == nil {
		return nil, RenderStats{}, errors.New("nil SEG-Y file")
	}
	if len(tracesA) == 0 || len(tracesA) != len(tracesB) {
		return nil, RenderStats{}, errors.New("residual trace pairs are empty or unmatched")
	}
	if a.Info.SampleIntervalUS != b.Info.SampleIntervalUS {
		return nil, RenderStats{}, fmt.Errorf("A-B residual requires matching sample intervals (A=%d us, B=%d us)", a.Info.SampleIntervalUS, b.Info.SampleIntervalUS)
	}
	// A residual is a sample-for-sample operation.  Silently truncating to the
	// shorter record (the former min(...) behavior) shifts the meaning of the
	// last samples and can make an apparently valid delta hide an axis mismatch.
	// Reject the pair instead; callers can still show A and B independently.
	if a.Info.SamplesPerTrace != b.Info.SamplesPerTrace {
		return nil, RenderStats{}, fmt.Errorf("A-B residual requires matching sample counts (A=%d, B=%d)", a.Info.SamplesPerTrace, b.Info.SamplesPerTrace)
	}
	if o.Width < 2 || o.Height < 2 {
		return nil, RenderStats{}, errors.New("invalid image dimensions")
	}
	ns := a.Info.SamplesPerTrace
	sm0, sm1 := o.SampleStart, o.SampleEnd
	if sm0 < 0 {
		sm0 = 0
	}
	if sm1 < 0 || sm1 >= ns {
		sm1 = ns - 1
	}
	if sm0 >= ns || sm0 > sm1 {
		return nil, RenderStats{}, errors.New("invalid residual sample range")
	}
	windowSamples := sm1 - sm0 + 1
	if shouldUseSmooth(o.DisplayMode, len(tracesA), windowSamples, o.Width, o.Height) {
		aGrid := make([][]float64, len(tracesA))
		bGrid := make([][]float64, len(tracesB))
		for i := range tracesA {
			var err error
			aGrid[i], err = a.readTraceWindow(tracesA[i], sm0, sm1)
			if err != nil {
				return nil, RenderStats{}, err
			}
			bGrid[i], err = b.readTraceWindow(tracesB[i], sm0, sm1)
			if err != nil {
				return nil, RenderStats{}, err
			}
		}
		vals, observedMin, observedMax := renderInterpolatedDiffGrid(aGrid, bGrid, o)
		absVals := make([]float64, len(vals))
		for i, v := range vals {
			absVals[i] = math.Abs(v)
		}
		sort.Float64s(absVals)
		g := o.GainPercent
		if g < 0 {
			g = 0
		}
		if g > 49 {
			g = 49
		}
		hiIdx := int(float64(len(absVals)) * (100.0 - g) / 100.0)
		if hiIdx < 0 {
			hiIdx = 0
		}
		if hiIdx >= len(absVals) {
			hiIdx = len(absVals) - 1
		}
		amp := absVals[hiIdx]
		if o.UseValueLimits && o.MaxValue > o.MinValue {
			amp = math.Max(math.Abs(o.MinValue), math.Abs(o.MaxValue))
		}
		if amp <= 0 || math.IsNaN(amp) || math.IsInf(amp, 0) {
			amp = 1
		}
		lo, hi := -amp, amp
		den := hi - lo
		pix := make([]byte, len(vals))
		for i, v := range vals {
			if v < lo {
				v = lo
			}
			if v > hi {
				v = hi
			}
			idx := int(math.Round(255 * (hi - v) / den))
			if idx < 0 {
				idx = 0
			}
			if idx > 255 {
				idx = 255
			}
			pix[i] = byte(idx)
		}
		st := RenderStats{ObservedMin: observedMin, ObservedMax: observedMax, MapMin: lo, MapMax: hi, SampleStart: sm0, SampleEnd: sm1, TraceStep: 1}
		st.TraceStart = tracesA[0]
		st.TraceEnd = tracesA[len(tracesA)-1]
		return pix, st, nil
	}
	vals := make([]float64, o.Width*o.Height)
	observedMin, observedMax := math.Inf(1), math.Inf(-1)
	last := -1
	var ta, tb []float64
	for x := 0; x < o.Width; x++ {
		i := 0
		if o.Width > 1 && len(tracesA) > 1 {
			i = int(math.Round(float64(x) * float64(len(tracesA)-1) / float64(o.Width-1)))
		}
		if i < 0 {
			i = 0
		}
		if i >= len(tracesA) {
			i = len(tracesA) - 1
		}
		if i != last {
			var err error
			ta, err = a.readTraceWindow(tracesA[i], sm0, sm1)
			if err != nil {
				return nil, RenderStats{}, err
			}
			tb, err = b.readTraceWindow(tracesB[i], sm0, sm1)
			if err != nil {
				return nil, RenderStats{}, err
			}
			last = i
		}
		rms := 1.0
		if o.AGC {
			ss := 0.0
			n := 0
			for j := 0; j < windowSamples; j++ {
				d := ta[j] - tb[j]
				if !math.IsNaN(d) && !math.IsInf(d, 0) {
					ss += d * d
					n++
				}
			}
			if n > 0 && ss > 0 {
				rms = math.Sqrt(ss / float64(n))
			}
		}
		for y := 0; y < o.Height; y++ {
			j := 0
			if o.Height > 1 && windowSamples > 1 {
				j = int(math.Round(float64(y) * float64(windowSamples-1) / float64(o.Height-1)))
			}
			v := ta[j] - tb[j]
			if o.AGC {
				v /= rms
			}
			if math.IsNaN(v) || math.IsInf(v, 0) {
				v = 0
			}
			vals[y*o.Width+x] = v
			if v < observedMin {
				observedMin = v
			}
			if v > observedMax {
				observedMax = v
			}
		}
	}
	if math.IsInf(observedMin, 1) {
		observedMin, observedMax = 0, 0
	}
	absVals := make([]float64, len(vals))
	for i, v := range vals {
		absVals[i] = math.Abs(v)
	}
	sort.Float64s(absVals)
	g := o.GainPercent
	if g < 0 {
		g = 0
	}
	if g > 49 {
		g = 49
	}
	// Legacy gain clips g percent at both tails. For |A-B|, the equivalent
	// symmetric upper percentile is 100-g percent.
	hiIdx := int(float64(len(absVals)) * (100.0 - g) / 100.0)
	if hiIdx < 0 {
		hiIdx = 0
	}
	if hiIdx >= len(absVals) {
		hiIdx = len(absVals) - 1
	}
	amp := absVals[hiIdx]
	if o.UseValueLimits && o.MaxValue > o.MinValue {
		amp = math.Max(math.Abs(o.MinValue), math.Abs(o.MaxValue))
	}
	if amp <= 0 || math.IsNaN(amp) || math.IsInf(amp, 0) {
		amp = 1
	}
	lo, hi := -amp, amp
	den := hi - lo
	pix := make([]byte, len(vals))
	for i, v := range vals {
		if v < lo {
			v = lo
		}
		if v > hi {
			v = hi
		}
		idx := int(math.Round(255 * (hi - v) / den))
		if idx < 0 {
			idx = 0
		}
		if idx > 255 {
			idx = 255
		}
		pix[i] = byte(idx)
	}
	st := RenderStats{ObservedMin: observedMin, ObservedMax: observedMax, MapMin: lo, MapMax: hi, SampleStart: sm0, SampleEnd: sm1, TraceStep: 1}
	st.TraceStart = tracesA[0]
	st.TraceEnd = tracesA[len(tracesA)-1]
	return pix, st, nil
}
