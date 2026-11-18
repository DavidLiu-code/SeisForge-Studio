package segyanalysis

import "math"

// ReferenceSNRState distinguishes physical zeros from missing or invalid pairs.
type ReferenceSNRState uint8

const (
	SNRUnavailable ReferenceSNRState = iota
	SNRFinite
	SNRPerfect
	SNRZeroReference
	SNRBothZero
)

// ReferenceSNRFormulaVersion identifies the default numerator and mask.
// Version 1 used noisy A as the reference; its cached dB is not reusable.
const ReferenceSNRFormulaVersion uint8 = 2

// ReferenceSNR uses clean reference B and the same finite sample pairs for both
// B's reference energy and A−B's error energy. DB is meaningful for SNRFinite,
// SNRPerfect (+Inf), and SNRZeroReference (−Inf).
type ReferenceSNR struct {
	DB             float64
	Pairs          int64
	State          ReferenceSNRState
	FormulaVersion uint8
}

// ReferenceSNRAccumulator is an allocation-free, streaming energy ratio.
// Its zero value is ready for use; one instance can span many aligned traces.
// It has no dependency on FFT, display gain, normalization or crop settings.
type ReferenceSNRAccumulator struct {
	reference, error scaledBinaryEnergy
	pairs            int64
}

// Each nonzero component is m*2^e, where 0.5 <= |m| < 1. The stored squared
// sum is relative to the largest exponent, so neither squaring tiny/huge
// values nor summing many large values overflows or erases physical zeros.
type scaledBinaryEnergy struct {
	exponent int
	squares  float64
}

func (s *scaledBinaryEnergy) add(mantissa float64, exponent int) {
	if mantissa == 0 {
		return
	}
	if s.squares == 0 {
		s.exponent = exponent
	} else if exponent > s.exponent {
		s.squares = math.Ldexp(s.squares, 2*(s.exponent-exponent))
		s.exponent = exponent
	}
	s.squares += math.Ldexp(mantissa*mantissa, 2*(exponent-s.exponent))
}

// Add accumulates an already geometry/time-matched pair; missing samples must
// not be submitted (or must be NaN). A valid zero is still a counted pair.
func (s *ReferenceSNRAccumulator) Add(a, b float64) bool {
	if !finiteDisplayValue(a) || !finiteDisplayValue(b) {
		return false
	}
	s.pairs++
	m, e := math.Frexp(b)
	s.reference.add(m, e)
	d := a - b
	if math.IsInf(d, 0) {
		// Opposite-sign extremes can exceed float64 as an amplitude but their
		// energy ratio is still valid. Halve only on actual overflow, so a
		// small difference between nearby values is never lost to scaling.
		m, e = math.Frexp(a/2 - b/2)
		e++
	} else {
		m, e = math.Frexp(d)
	}
	s.error.add(m, e)
	return true
}

// AddAligned intersects physical samples exactly, without rounding,
// resampling, or padding. The caller verifies that the two input traces are
// geometry counterparts. A nonempty result explains incompatible time axes.
func (s *ReferenceSNRAccumulator) AddAligned(a, b TraceInput) string {
	a, b, reason := CompareTraceWindows(a, b)
	if reason != "" {
		return reason
	}
	for i, v := range a.Samples {
		s.Add(v, b.Samples[i])
	}
	return ""
}

func (s ReferenceSNRAccumulator) Result() ReferenceSNR {
	r := ReferenceSNR{Pairs: s.pairs, DB: math.NaN(), FormulaVersion: ReferenceSNRFormulaVersion}
	if s.pairs == 0 {
		return r
	}
	b, e := s.reference, s.error
	switch {
	case b.squares == 0 && e.squares == 0:
		r.State = SNRBothZero
	case b.squares == 0:
		r.State, r.DB = SNRZeroReference, math.Inf(-1)
	case e.squares == 0:
		r.State, r.DB = SNRPerfect, math.Inf(1)
	default:
		r.State = SNRFinite
		r.DB = 10 * (math.Log10(b.squares/e.squares) + 2*float64(b.exponent-e.exponent)*math.Log10(2))
	}
	return r
}
