package poststack

import (
	"context"
	"errors"
	"fmt"
	"math"
)

// ViewBandpass is the shared temporal filter configuration. The historical
// name is retained for existing view state and task metadata. A low/high-only
// configuration uses the original fourth-order highpass/lowpass cascade.
type ViewBandpass struct {
	LowHz, HighHz           float64
	Response                FilterResponse
	Method                  FilterMethod
	Order                   int
	Phase                   FilterPhase
	RippleDB, AttenuationDB float64
}

// ViewBandpassMinSamples is the minimum trace/window length required by the
// 27-sample odd-reflection padding (3 * (2 * four SOS sections + 1)). Very short
// windows are rejected rather than silently changing the filter or padding.
const ViewBandpassMinSamples = 28

const viewBandpassPad = ViewBandpassMinSamples - 1

// DefaultBandpass preserves the existing, sample-rate-aware editable preset.
// The caller must still validate it and explicitly apply/run the filter.
func DefaultBandpass(dtUS int) ViewBandpass {
	if dtUS <= 0 {
		return ViewBandpass{}
	}
	nyquist := 500000 / float64(dtUS)
	return ViewBandpass{LowHz: nyquist * 0.08, HighHz: nyquist * 0.65}
}

// FilterTraceWindow uses all supplied real temporal context before returning
// the inclusive requested window. It never pads a missing B window with A or
// zero samples, changes sample positions, or filters an already derived view.
func FilterTraceWindow(ctx context.Context, full []float64, dtUS int, p ViewBandpass, first, last int) ([]float64, error) {
	filter, err := NewTraceWindowFilter(dtUS, p)
	if err != nil {
		return nil, err
	}
	return filter(ctx, full, first, last)
}

// NewTraceWindowFilter compiles immutable sections once for all selected
// traces with this interval. Each call owns its scratch buffer; it is safe to
// invoke concurrently for independent traces.
func NewTraceWindowFilter(dtUS int, p ViewBandpass) (func(context.Context, []float64, int, int) ([]float64, error), error) {
	design, err := p.design(dtUS)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, full []float64, first, last int) ([]float64, error) {
		if first < 0 || last < first || last >= len(full) {
			return nil, errors.New("filter output window is outside the real source context")
		}
		if ctx == nil {
			ctx = context.Background()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		filtered, err := design.filterTrace(ctx, full)
		if err != nil {
			return nil, err
		}
		if first == 0 && last == len(full)-1 {
			return filtered, nil
		}
		return append([]float64(nil), filtered[first:last+1]...), nil
	}, nil
}

func (p ViewBandpass) Validate(dtUS int) error {
	_, err := p.design(dtUS)
	return err
}

// FilterTrace filters the real samples supplied by the caller, not palette
// bytes or screen-decimated columns. It returns a new slice of exactly the same
// length and never mutates input. The caller selects the logical trace/window;
// this function does not open Readers, alter sample axes, or write source data.
//
// Endpoint treatment uses odd reflection and constant-input steady-state
// initialization of every SOS in both directions. As with all finite-window
// forward/backward IIR filters, samples near the window ends depend on this
// boundary assumption; zero phase does not imply absence of edge transients.
// Coefficients use the bilinear-transform LPF/HPF formulae described in
// https://www.w3.org/TR/audio-eq-cookbook/ and Butterworth pole-pair Q factors.
// The padding/forward-backward convention follows
// https://docs.scipy.org/doc/scipy/reference/generated/scipy.signal.sosfiltfilt.html .
// This is a standalone Go implementation and has no Python/SciPy dependency.
func FilterTrace(ctx context.Context, input []float64, dtUS int, p ViewBandpass) ([]float64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	design, err := p.design(dtUS)
	if err != nil {
		return nil, err
	}
	return design.filterTrace(ctx, input)
}

func (design temporalFilter) filterTrace(ctx context.Context, input []float64) ([]float64, error) {
	pad := design.pad
	if len(input) < design.minimum {
		return nil, fmt.Errorf("filter requires at least %d real context samples; got %d", design.minimum, len(input))
	}
	if len(input) > int(^uint(0)>>1)-2*pad {
		return nil, errors.New("filter padded sample count overflows")
	}
	// Normalize only for numerical headroom during reflection and recurrence,
	// then restore the exact scale. This is not amplitude normalization or AGC.
	scale := 0.0
	for i, x := range input {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if !viewFilterFinite(x) {
			return nil, fmt.Errorf("filter input sample %d is not finite", i)
		}
		scale = math.Max(scale, math.Abs(x))
	}
	if scale == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return make([]float64, len(input)), nil
	}
	work := make([]float64, len(input)+2*pad)
	for i, x := range input {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		work[pad+i] = x / scale
	}
	first, last := input[0]/scale, input[len(input)-1]/scale
	for i := 0; i < pad; i++ {
		work[i] = 2*first - input[pad-i]/scale
		work[pad+len(input)+i] = 2*last - input[len(input)-2-i]/scale
	}
	if err := viewFilterPass(ctx, work, design.sections, false); err != nil {
		return nil, err
	}
	if design.zeroPhase {
		if err := viewFilterPass(ctx, work, design.sections, true); err != nil {
			return nil, err
		}
	}
	output := make([]float64, len(input))
	for i := range output {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		output[i] = work[pad+i] * scale
		if !viewFilterFinite(output[i]) {
			return nil, fmt.Errorf("filter output sample %d exceeds finite float64 range", i)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return output, nil
}

type viewFilterSOS struct {
	b0, b1, b2 float64
	a1, a2     float64
	dcGain     float64
}

func (p ViewBandpass) sections(dtUS int) ([4]viewFilterSOS, error) {
	var sos [4]viewFilterSOS
	for i := range sos {
		// The two stable conjugate pole pairs of a fourth-order Butterworth
		// prototype have these Q factors, ordered low-Q first for headroom.
		q := 1 / (2 * math.Cos(float64(2*(i%2)+1)*math.Pi/8))
		cutoff := p.LowHz
		if i >= 2 {
			cutoff = p.HighHz
		}
		w := 2 * math.Pi * cutoff * float64(dtUS) / 1e6
		sin, cos := math.Sincos(w)
		alpha := sin / (2 * q)
		a0 := 1 + alpha
		section := viewFilterSOS{a1: -2 * cos / a0, a2: (1 - alpha) / a0}
		if i < 2 {
			// cos(w/2)^2 avoids the cancellation of (1+cos(w))/2.
			half := math.Cos(w / 2)
			section.b0 = half * half / a0
			section.b1 = -2 * section.b0
		} else {
			half := math.Sin(w / 2)
			section.b0 = half * half / a0
			section.b1 = 2 * section.b0
			section.dcGain = 1
		}
		section.b2 = section.b0
		// Jury stability conditions, also rejecting numerically collapsed
		// corner frequencies whose poles round onto the unit circle.
		if !viewFilterFinite(section.b0) || !viewFilterFinite(section.a1) || !viewFilterFinite(section.a2) ||
			math.Abs(section.a2) >= 1 || 1+section.a1+section.a2 <= 0 || 1-section.a1+section.a2 <= 0 {
			return sos, errors.New("bandpass cutoff is too close to zero or Nyquist for stable float64 coefficients")
		}
		sos[i] = section
	}
	return sos, nil
}

func viewFilterPass(ctx context.Context, values []float64, sos []viewFilterSOS, backward bool) error {
	first := 0
	if backward {
		first = len(values) - 1
	}
	for _, s := range sos {
		x0 := values[first]
		z1 := (s.dcGain - s.b0) * x0
		z2 := (s.b2 - s.a2*s.dcGain) * x0
		for step := range values {
			if step&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			i := step
			if backward {
				i = len(values) - 1 - step
			}
			x := values[i]
			y := s.b0*x + z1
			z1 = s.b1*x - s.a1*y + z2
			z2 = s.b2*x - s.a2*y
			if !viewFilterFinite(y) || !viewFilterFinite(z1) || !viewFilterFinite(z2) {
				return errors.New("filter numerical state exceeds finite float64 range")
			}
			values[i] = y
		}
	}
	return nil
}

func viewFilterFinite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
