package segy

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
)

// CoordinateSource identifies the standard SEG-Y trace-header words used for
// a survey trajectory. Byte positions remain explicit so non-standard files
// can use the same scanner without exposing the underlying file handle.
type CoordinateSource string

const (
	CoordinateAuto     CoordinateSource = "auto"
	CoordinateEnsemble CoordinateSource = "ensemble"
	CoordinateSourceXY CoordinateSource = "source"
	CoordinateGroup    CoordinateSource = "group"
	CoordinateCustom   CoordinateSource = "custom"
)

// TraceCoordinateSpec uses 1-based SEG-Y trace-header byte positions.
type TraceCoordinateSpec struct {
	Source     CoordinateSource `json:"source"`
	XByte      int              `json:"x_byte"`
	YByte      int              `json:"y_byte"`
	CDPByte    int              `json:"cdp_byte"`
	ScalarByte int              `json:"scalar_byte"`
	UnitsByte  int              `json:"units_byte"`
}

func DefaultCoordinateSpecs() []TraceCoordinateSpec {
	return []TraceCoordinateSpec{
		{Source: CoordinateEnsemble, XByte: 181, YByte: 185, CDPByte: 21, ScalarByte: 71, UnitsByte: 89},
		{Source: CoordinateSourceXY, XByte: 73, YByte: 77, CDPByte: 21, ScalarByte: 71, UnitsByte: 89},
		{Source: CoordinateGroup, XByte: 81, YByte: 85, CDPByte: 21, ScalarByte: 71, UnitsByte: 89},
	}
}

func DefaultCoordinateSpec() TraceCoordinateSpec { return DefaultCoordinateSpecs()[0] }

// NormalizeCoordinateSpec fills omitted standard byte positions and validates
// the resulting 1-based trace-header word locations.
func NormalizeCoordinateSpec(spec TraceCoordinateSpec) (TraceCoordinateSpec, error) {
	spec = spec.normalized()
	if err := spec.Validate(); err != nil {
		return TraceCoordinateSpec{}, err
	}
	return spec, nil
}

func (s TraceCoordinateSpec) normalized() TraceCoordinateSpec {
	if s.Source == "" || s.Source == CoordinateAuto {
		s.Source = CoordinateEnsemble
	}
	if s.XByte == 0 {
		s.XByte = 181
	}
	if s.YByte == 0 {
		s.YByte = 185
	}
	if s.CDPByte == 0 {
		s.CDPByte = 21
	}
	if s.ScalarByte == 0 {
		s.ScalarByte = 71
	}
	if s.UnitsByte == 0 {
		s.UnitsByte = 89
	}
	return s
}

func (s TraceCoordinateSpec) Validate() error {
	s = s.normalized()
	for name, b := range map[string]int{"X": s.XByte, "Y": s.YByte, "CDP": s.CDPByte} {
		if b < 1 || b+3 > 240 {
			return fmt.Errorf("%s trace-header byte must be between 1 and 237", name)
		}
	}
	for name, b := range map[string]int{"coordinate scalar": s.ScalarByte, "coordinate units": s.UnitsByte} {
		if b < 1 || b+1 > 240 {
			return fmt.Errorf("%s trace-header byte must be between 1 and 239", name)
		}
	}
	return nil
}

// TraceCoordinate is metadata only; no seismic amplitudes are retained.
type TraceCoordinate struct {
	Trace  int64
	CDP    int32
	X, Y   float64
	Scalar int16
	Units  int16
	Valid  bool
	HasCDP bool
}

var coordinateHeaderIOSemaphore = make(chan struct{}, 16)

func (s *File) readTraceHeader(trace int64, destination []byte) error {
	coordinateHeaderIOSemaphore <- struct{}{}
	_, err := s.f.ReadAt(destination, s.Info.DataStart+trace*s.Info.TraceBytes)
	<-coordinateHeaderIOSemaphore
	return err
}

func coordinateScale(raw int16) float64 {
	if raw > 0 {
		return float64(raw)
	}
	if raw < 0 {
		return 1 / float64(-int(raw))
	}
	return 1
}

func (s *File) readTraceCoordinate(trace int64, spec TraceCoordinateSpec) (TraceCoordinate, error) {
	if s == nil || s.f == nil {
		return TraceCoordinate{}, errors.New("nil SEG-Y reader")
	}
	spec = spec.normalized()
	if err := spec.Validate(); err != nil {
		return TraceCoordinate{}, err
	}
	if trace < 0 || trace >= s.Info.TraceCount {
		return TraceCoordinate{}, errors.New("trace index out of range")
	}
	var header [240]byte
	if err := s.readTraceHeader(trace, header[:]); err != nil {
		return TraceCoordinate{}, err
	}
	return s.decodeTraceCoordinateHeader(trace, spec, header[:]), nil
}

func (s *File) decodeTraceCoordinateHeader(trace int64, spec TraceCoordinateSpec, header []byte) TraceCoordinate {
	xRaw := int32(u32(header[spec.XByte-1:spec.XByte+3], s.Info.Endian))
	yRaw := int32(u32(header[spec.YByte-1:spec.YByte+3], s.Info.Endian))
	cdp := int32(u32(header[spec.CDPByte-1:spec.CDPByte+3], s.Info.Endian))
	scalar := int16(u16(header[spec.ScalarByte-1:spec.ScalarByte+1], s.Info.Endian))
	units := int16(u16(header[spec.UnitsByte-1:spec.UnitsByte+1], s.Info.Endian))
	factor := coordinateScale(scalar)
	x, y := float64(xRaw)*factor, float64(yRaw)*factor
	valid := (xRaw != 0 || yRaw != 0) && !math.IsNaN(x) && !math.IsNaN(y) && !math.IsInf(x, 0) && !math.IsInf(y, 0)
	return TraceCoordinate{Trace: trace, CDP: cdp, X: x, Y: y, Scalar: scalar, Units: units, Valid: valid, HasCDP: cdp != 0}
}

// ReadTraceCoordinate reads only one 240-byte trace header.
func (s *File) ReadTraceCoordinate(trace int64, spec TraceCoordinateSpec) (TraceCoordinate, error) {
	return s.readTraceCoordinate(trace, spec)
}

// ScanTraceCoordinates scans trace headers concurrently. Progress callbacks
// may run on worker goroutines and must be marshalled by UI callers.
func (s *File) ScanTraceCoordinates(spec TraceCoordinateSpec, workers int, progress func(done, total int)) ([]TraceCoordinate, error) {
	if s == nil {
		return nil, errors.New("nil SEG-Y reader")
	}
	spec = spec.normalized()
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	total := s.Info.TraceCount
	if total <= 0 || total > int64(^uint(0)>>1) {
		return nil, errors.New("invalid trace count")
	}
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > 16 {
		workers = 16
	}
	if int64(workers) > total {
		workers = int(total)
	}
	if workers < 1 {
		workers = 1
	}
	out := make([]TraceCoordinate, int(total))
	jobs := make(chan int64, workers*2)
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	var done int64
	var lastPct int64 = -1
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for trace := range jobs {
				location, err := s.readTraceCoordinate(trace, spec)
				if err != nil {
					errOnce.Do(func() { firstErr = err })
				} else {
					out[int(trace)] = location
				}
				n := atomic.AddInt64(&done, 1)
				if progress != nil {
					pct := n * 100 / total
					for {
						old := atomic.LoadInt64(&lastPct)
						if pct <= old || atomic.CompareAndSwapInt64(&lastPct, old, pct) {
							if pct > old {
								progress(int(n), int(total))
							}
							break
						}
					}
				}
			}
		}()
	}
	for trace := int64(0); trace < total; trace++ {
		jobs <- trace
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if progress != nil && atomic.LoadInt64(&done) == total {
		progress(int(total), int(total))
	}
	return out, nil
}

type CoordinateCandidate struct {
	Spec          TraceCoordinateSpec
	Score         float64
	ValidRatio    float64
	UniqueCount   int
	MedianStep    float64
	P95Step       float64
	TotalDistance float64
	Coordinates   []TraceCoordinate
}

type CoordinateDetectResult struct {
	Spec        TraceCoordinateSpec
	Score       float64
	ValidRatio  float64
	UniqueCount int
	Candidates  []CoordinateCandidate
	Sampled     int
}

func coordinateCandidateStats(spec TraceCoordinateSpec, values []TraceCoordinate) CoordinateCandidate {
	candidate := CoordinateCandidate{Spec: spec, Coordinates: values}
	if len(values) == 0 {
		return candidate
	}
	valid := make([]TraceCoordinate, 0, len(values))
	unique := make(map[[2]uint64]struct{})
	for _, value := range values {
		if !value.Valid {
			continue
		}
		valid = append(valid, value)
		unique[[2]uint64{math.Float64bits(value.X), math.Float64bits(value.Y)}] = struct{}{}
	}
	candidate.ValidRatio = float64(len(valid)) / float64(len(values))
	candidate.UniqueCount = len(unique)
	stepCapacity := 0
	if len(valid) > 1 {
		stepCapacity = len(valid) - 1
	}
	steps := make([]float64, 0, stepCapacity)
	for i := 1; i < len(valid); i++ {
		d := math.Hypot(valid[i].X-valid[i-1].X, valid[i].Y-valid[i-1].Y)
		if d > 0 && !math.IsInf(d, 0) && !math.IsNaN(d) {
			steps = append(steps, d)
			candidate.TotalDistance += d
		}
	}
	if len(steps) > 0 {
		sort.Float64s(steps)
		candidate.MedianStep = steps[len(steps)/2]
		candidate.P95Step = steps[int(math.Round(float64(len(steps)-1)*0.95))]
	}
	uniqueQuality := math.Min(1, float64(candidate.UniqueCount)/32)
	continuity := 0.0
	if candidate.MedianStep > 0 {
		ratio := candidate.P95Step / candidate.MedianStep
		continuity = math.Max(0, math.Min(1, (10-ratio)/9))
	}
	candidate.Score = 100 * (0.65*candidate.ValidRatio + 0.20*uniqueQuality + 0.15*continuity)
	return candidate
}

// DetectCoordinateSpec samples standard coordinate words uniformly through
// the file and selects the strongest usable trajectory. Candidate ordering is
// stable, so equal scores retain Ensemble, Source, Group priority.
func (s *File) DetectCoordinateSpec(maxHeaders int) (CoordinateDetectResult, error) {
	if s == nil {
		return CoordinateDetectResult{}, errors.New("nil SEG-Y reader")
	}
	n := int(s.Info.TraceCount)
	if n <= 0 {
		return CoordinateDetectResult{}, errors.New("SEG-Y contains no traces")
	}
	if maxHeaders <= 0 || maxHeaders > n {
		maxHeaders = n
	}
	result := CoordinateDetectResult{Sampled: maxHeaders}
	traceIndices := make([]int64, maxHeaders)
	headers := make([]byte, maxHeaders*240)
	for i := range traceIndices {
		if maxHeaders > 1 {
			traceIndices[i] = int64(i) * (s.Info.TraceCount - 1) / int64(maxHeaders-1)
		}
	}
	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4
	}
	if workers > maxHeaders {
		workers = maxHeaders
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int, workers*2)
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if err := s.readTraceHeader(traceIndices[index], headers[index*240:(index+1)*240]); err != nil {
					errOnce.Do(func() { firstErr = err })
				}
			}
		}()
	}
	for index := range traceIndices {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return CoordinateDetectResult{}, firstErr
	}
	for _, spec := range DefaultCoordinateSpecs() {
		values := make([]TraceCoordinate, 0, maxHeaders)
		for i := 0; i < maxHeaders; i++ {
			values = append(values, s.decodeTraceCoordinateHeader(traceIndices[i], spec, headers[i*240:(i+1)*240]))
		}
		result.Candidates = append(result.Candidates, coordinateCandidateStats(spec, values))
	}
	best := -1
	for i := range result.Candidates {
		candidate := result.Candidates[i]
		if candidate.UniqueCount < 2 || candidate.ValidRatio <= 0 {
			continue
		}
		if best < 0 || candidate.Score > result.Candidates[best].Score+1e-9 {
			best = i
		}
	}
	if best < 0 {
		return result, errors.New("no usable standard X/Y trace-header coordinates were found")
	}
	chosen := result.Candidates[best]
	result.Spec = chosen.Spec
	result.Score = chosen.Score
	result.ValidRatio = chosen.ValidRatio
	result.UniqueCount = chosen.UniqueCount
	return result, nil
}
