package segyanalysis

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// TraceInput is a decoded single-trace window. StartUS is the physical time
// of Samples[0], including both the trace's recording delay and window offset.
type TraceInput struct {
	Role       string
	StartUS    int64
	IntervalUS int
	Samples    []float64
}

// TraceSpectrum stores unmodified linear FFT magnitudes and their dB values
// relative to the snapshot's shared reference. Zero magnitude is -Inf dB; a
// display floor, if needed, belongs to the renderer rather than these data.
type TraceSpectrum struct {
	AmplitudeResult
	StartUS       int64
	IntervalUS    int
	DB            []float64
	PeakBin       int
	PeakHz        float64
	PeakAmplitude float64
}

// TraceSeries keeps the individual waveform and statistics even when its
// comparison spectrum is computed on a shorter, common physical window.
type TraceSeries struct {
	TraceInput
	Stats         TraceStats
	Spectrum      TraceSpectrum
	SpectrumError string
}

// SpectrumReference is the non-DC peak of A for a comparison, or that of the
// single trace for standalone analysis. Fallback is one raw FFT magnitude
// unit; never silently normalize to B or the residual when A is absent/zero.
type SpectrumReference struct {
	Amplitude float64
	Fallback  bool
	Role      string
}

// TraceSnapshot is a read-only, UI-independent analysis of one selection.
type TraceSnapshot struct {
	Series                   []TraceSeries
	Reference                SpectrumReference
	ReferenceSNR             ReferenceSNR
	ResidualNote             string
	Comparison               bool
	ComparisonOverlapSamples int
	ComparisonStartUS        int64
	ComparisonIntervalUS     int
	fullRange, robustRange   DisplayRange
	dbRange                  DisplayRange
}

// BuildTraceSnapshot copies the decoded inputs, so a worker can pass the
// resulting read-only snapshot to a UI without sharing mutable input buffers.
// In a comparison, compatible A and B spectra are measured on the exact
// same real time samples as the residual. The plotted waveforms/statistics
// remain on their full individual windows. Incompatible individual traces
// still have their own analysis, but there is no interpolated residual.
func BuildTraceSnapshot(inputs []TraceInput, comparison bool) TraceSnapshot {
	var snr ReferenceSNRAccumulator
	out := TraceSnapshot{Comparison: comparison, Series: make([]TraceSeries, 0, len(inputs)+1)}
	aIndex, bIndex := -1, -1
	for _, in := range inputs {
		if len(in.Samples) == 0 {
			continue
		}
		in.Samples = append([]float64(nil), in.Samples...)
		i := len(out.Series)
		out.Series = append(out.Series, TraceSeries{TraceInput: in, Stats: ComputeTraceStats(in.Samples)})
		switch {
		case aIndex < 0 && strings.EqualFold(in.Role, "A"):
			aIndex = i
		case bIndex < 0 && strings.EqualFold(in.Role, "B"):
			bIndex = i
		}
	}

	windows := make([]TraceInput, len(out.Series), len(out.Series)+1)
	for i := range out.Series {
		windows[i] = out.Series[i].TraceInput
	}
	if comparison {
		if aIndex < 0 || bIndex < 0 {
			out.ResidualNote = "A−B 不可计算：A 或 B 波形不可用"
		} else {
			aw, bw, note := CompareTraceWindows(windows[aIndex], windows[bIndex])
			out.ResidualNote = note
			if note == "" {
				windows[aIndex], windows[bIndex] = aw, bw
				out.ComparisonOverlapSamples = len(aw.Samples)
				out.ComparisonStartUS, out.ComparisonIntervalUS = aw.StartUS, aw.IntervalUS
				residual := TraceInput{Role: "A-B", StartUS: aw.StartUS, IntervalUS: aw.IntervalUS, Samples: make([]float64, len(aw.Samples))}
				for i := range residual.Samples {
					if snr.Add(aw.Samples[i], bw.Samples[i]) {
						residual.Samples[i] = aw.Samples[i] - bw.Samples[i]
					} else {
						residual.Samples[i] = math.NaN()
					}
				}
				out.Series = append(out.Series, TraceSeries{TraceInput: residual, Stats: ComputeTraceStats(residual.Samples)})
				windows = append(windows, residual)
			}
		}
	}
	out.ReferenceSNR = snr.Result()
	for i := range out.Series {
		out.Series[i].Spectrum, out.Series[i].SpectrumError = makeTraceSnapshotSpectrum(windows[i])
	}
	refIndex, refRole := aIndex, "A"
	if !comparison {
		refIndex, refRole = -1, ""
		if len(out.Series) > 0 {
			refIndex, refRole = 0, out.Series[0].Role
		}
	}
	out.Reference = SpectrumReference{Amplitude: 1, Fallback: true, Role: refRole}
	if refIndex >= 0 {
		peak := out.Series[refIndex].Spectrum.PeakAmplitude
		if peak > 0 && finiteDisplayValue(peak) {
			out.Reference.Amplitude, out.Reference.Fallback = peak, false
		}
	}
	logReference := math.Log10(out.Reference.Amplitude)
	for i := range out.Series {
		spectrum := &out.Series[i].Spectrum
		if len(spectrum.LinearAmplitude) == 0 {
			continue
		}
		spectrum.DB = make([]float64, len(spectrum.LinearAmplitude))
		for j, amplitude := range spectrum.LinearAmplitude {
			// Subtract logarithms rather than divide: the ratio of finite
			// amplitudes may overflow or underflow when the dB value is finite.
			spectrum.DB[j] = 20 * (math.Log10(amplitude) - logReference)
		}
	}
	out.fullRange, out.robustRange = traceAmplitudeRanges(out.Series)
	out.dbRange = traceDBRange(out.Series)
	return out
}

// CompareTraceWindows intersects two physical trace windows on their existing
// time grid. The output windows borrow the original samples; no data is moved.
func CompareTraceWindows(a, b TraceInput) (TraceInput, TraceInput, string) {
	if a.IntervalUS <= 0 || b.IntervalUS <= 0 || a.IntervalUS != b.IntervalUS {
		return TraceInput{}, TraceInput{}, fmt.Sprintf("A−B 不可计算：采样间隔无效或不一致（%d / %d μs）", a.IntervalUS, b.IntervalUS)
	}
	// Subtract with unsigned arithmetic after ordering the times. This also
	// handles a comparison whose physical origins straddle zero.
	start := max(a.StartUS, b.StartUS)
	dt := uint64(a.IntervalUS)
	aDelta, bDelta := uint64(start)-uint64(a.StartUS), uint64(start)-uint64(b.StartUS)
	i, j := aDelta/dt, bDelta/dt
	if i >= uint64(len(a.Samples)) || j >= uint64(len(b.Samples)) {
		return TraceInput{}, TraceInput{}, "A−B 不可计算：当前时间窗没有共同样点"
	}
	if aDelta%dt != 0 || bDelta%dt != 0 {
		return TraceInput{}, TraceInput{}, "A−B 不可计算：A、B 的真实时间样点网格未对齐"
	}
	n := min(len(a.Samples)-int(i), len(b.Samples)-int(j))
	a.StartUS, b.StartUS = start, start
	a.Samples, b.Samples = a.Samples[int(i):int(i)+n], b.Samples[int(j):int(j)+n]
	return a, b, ""
}

func makeTraceSnapshotSpectrum(window TraceInput) (TraceSpectrum, string) {
	result := TraceSpectrum{AmplitudeResult: AmplitudeResult{InputSamples: len(window.Samples)}, StartUS: window.StartUS, IntervalUS: window.IntervalUS}
	linear, err := ComputeAmplitudeSpectrum(window.Samples, window.IntervalUS, AmplitudeOptions{
		LengthPolicy:   FFTLengthCeil,
		MaxNFFT:        MaximumNFFT,
		MinimumSamples: MinimumTraceSpectrumSamples,
		RemoveMean:     true,
		ApplyHann:      true,
	})
	if err != nil {
		return result, err.Error()
	}
	for i, amplitude := range linear.LinearAmplitude {
		if i != 0 && amplitude > result.PeakAmplitude {
			result.PeakBin, result.PeakHz, result.PeakAmplitude = i, linear.FrequencyHz[i], amplitude
		}
	}
	result.AmplitudeResult = linear
	return result, ""
}

// AmplitudeScaleMode selects the manual or automatic common waveform scale.
type AmplitudeScaleMode int

const (
	ScaleRobust AmplitudeScaleMode = iota // P01..P99, as in legacy trace view.
	ScaleFull                             // Min..max, including extremes.
	ScaleManual                           // Caller-provided physical amplitudes.
)

// DisplayRange is a finite increasing interval in physical amplitude or dB.
type DisplayRange struct{ Min, Max float64 }

func (r DisplayRange) Valid() bool {
	return finiteDisplayValue(r.Min) && finiteDisplayValue(r.Max) && r.Min < r.Max
}

// Fraction converts a plotted value to a clamped [0,1] coordinate. It keeps
// finite extreme amplitudes drawable even when subtracting the range limits
// directly would overflow. NaN is still a gap; infinities clip to an edge.
func (r DisplayRange) Fraction(value float64) float64 {
	if !r.Valid() || math.IsNaN(value) {
		return math.NaN()
	}
	if value <= r.Min {
		return 0
	}
	if value >= r.Max {
		return 1
	}
	span := r.Max - r.Min
	if math.IsInf(span, 1) {
		return (value/2 - r.Min/2) / (r.Max/2 - r.Min/2)
	}
	return (value - r.Min) / span
}

// AmplitudeRange uses one shared scale for every curve. With symmetric set,
// the magnitude of the largest endpoint becomes both the positive and
// negative bound. Invalid manual bounds defensively fall back to robust auto.
func (s TraceSnapshot) AmplitudeRange(mode AmplitudeScaleMode, manual DisplayRange, symmetric bool) DisplayRange {
	r := s.robustRange
	if mode == ScaleFull {
		r = s.fullRange
	} else if mode == ScaleManual && manual.Valid() {
		r = manual
	}
	if !r.Valid() {
		r = DisplayRange{-1, 1}
	}
	if symmetric {
		peak := math.Max(math.Abs(r.Min), math.Abs(r.Max))
		r = DisplayRange{-peak, peak}
	}
	return r
}

func traceAmplitudeRanges(series []TraceSeries) (full, robust DisplayRange) {
	n := 0
	for _, s := range series {
		n += s.Stats.FiniteCount
	}
	if n == 0 {
		return DisplayRange{-1, 1}, DisplayRange{-1, 1}
	}
	values := make([]float64, 0, n)
	for _, s := range series {
		for _, v := range s.Samples {
			if finiteDisplayValue(v) {
				values = append(values, v)
			}
		}
	}
	sort.Float64s(values)
	full = DisplayRange{values[0], values[len(values)-1]}
	if !full.Valid() {
		switch {
		case full.Min < 0:
			full.Max = 0
		case full.Max > 0:
			full.Min = 0
		default:
			full = DisplayRange{-1, 1}
		}
	}
	robust = DisplayRange{percentileSorted(values, 1), percentileSorted(values, 99)}
	if !robust.Valid() {
		robust = full
	}
	return full, robust
}

// DBRange returns finite automatic plot limits. Positive values remain
// visible when B/the residual exceed A; an identically zero trace does not
// expand the scale. Negative infinity can be drawn on the lower edge.
func (s TraceSnapshot) DBRange() DisplayRange {
	if s.dbRange.Valid() {
		return s.dbRange
	}
	return DisplayRange{DefaultDBFloor, 0}
}

func traceDBRange(series []TraceSeries) DisplayRange {
	r := DisplayRange{DefaultDBFloor, 0}
	for _, s := range series {
		peak := math.Inf(-1)
		for _, db := range s.Spectrum.DB {
			if finiteDisplayValue(db) && db > peak {
				peak = db
			}
		}
		if finiteDisplayValue(peak) {
			r.Min = math.Min(r.Min, 10*math.Floor(peak/10)-10)
			r.Max = math.Max(r.Max, 10*math.Ceil(peak/10))
		}
	}
	return r
}

func finiteDisplayValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
