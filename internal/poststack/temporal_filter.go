package poststack

import (
	"errors"
	"fmt"
	"math"
	"math/cmplx"
	"sort"
)

type FilterResponse string
type FilterMethod string
type FilterPhase string

const (
	FilterLowpass        FilterResponse = "lowpass"
	FilterHighpass       FilterResponse = "highpass"
	FilterBandpass       FilterResponse = "bandpass"
	FilterBandstop       FilterResponse = "bandstop"
	FilterLegacyBandpass FilterResponse = "legacy_bandpass"

	FilterButterworth FilterMethod = "butterworth"
	FilterChebyshevI  FilterMethod = "chebyshev_i"
	FilterChebyshevII FilterMethod = "chebyshev_ii"
	FilterElliptic    FilterMethod = "elliptic"

	FilterForward   FilterPhase = "forward"
	FilterZeroPhase FilterPhase = "zero_phase"

	FilterMaxOrder = 20
)

func (p ViewBandpass) EffectiveResponse() FilterResponse {
	if p.Response == "" {
		return FilterLegacyBandpass
	}
	return p.Response
}

func (p ViewBandpass) EffectiveMethod() FilterMethod {
	if p.Method == "" {
		return FilterButterworth
	}
	return p.Method
}

func (p ViewBandpass) EffectivePhase() FilterPhase {
	if p.Phase == "" {
		return FilterZeroPhase
	}
	return p.Phase
}

func (p ViewBandpass) DesignOrder() int {
	if p.Order == 0 {
		return 4
	}
	return p.Order
}

// Canonical removes fields not used by a design. Old and explicit legacy
// configurations compare equal, and stale values in hidden fields are ignored.
func (p ViewBandpass) Canonical() ViewBandpass {
	p.Response, p.Method, p.Phase, p.Order = p.EffectiveResponse(), p.EffectiveMethod(), p.EffectivePhase(), p.DesignOrder()
	if p.Response == FilterLowpass || p.Response == FilterHighpass {
		p.HighHz = 0
	}
	if p.Method != FilterChebyshevI && p.Method != FilterElliptic {
		p.RippleDB = 0
	}
	if p.Method != FilterChebyshevII && p.Method != FilterElliptic {
		p.AttenuationDB = 0
	}
	return p
}

func (p ViewBandpass) Summary() string {
	p = p.Canonical()
	corners := fmt.Sprintf("%.6g Hz", p.LowHz)
	if p.Response == FilterBandpass || p.Response == FilterBandstop || p.Response == FilterLegacyBandpass {
		corners = fmt.Sprintf("%.6g–%.6g Hz", p.LowHz, p.HighHz)
	}
	return fmt.Sprintf("%s %s · %s %d 阶 · %s", p.Response.Label(), corners, p.Method.Label(), p.Order, p.Phase.Label())
}

func (r FilterResponse) Label() string {
	switch r {
	case FilterLowpass:
		return "低通"
	case FilterHighpass:
		return "高通"
	case FilterBandpass:
		return "带通"
	case FilterBandstop:
		return "带阻"
	case FilterLegacyBandpass:
		return "兼容带通（高通 + 低通）"
	}
	return string(r)
}

func (m FilterMethod) Label() string {
	switch m {
	case FilterButterworth:
		return "Butterworth"
	case FilterChebyshevI:
		return "Chebyshev I"
	case FilterChebyshevII:
		return "Chebyshev II"
	case FilterElliptic:
		return "Elliptic"
	}
	return string(m)
}

func (p FilterPhase) Label() string {
	if p == FilterForward {
		return "单向"
	}
	if p == FilterZeroPhase {
		return "双向零相位"
	}
	return string(p)
}

// EdgeMeaning explains the single-pass design frequency convention. The
// response preview itself always shows the selected one- or two-pass mode.
func (p ViewBandpass) EdgeMeaning() string {
	if p.EffectiveResponse() == FilterLegacyBandpass {
		return "所示边界为各 Butterworth 分量的单程半功率点，组合响应以曲线为准。"
	}
	switch p.EffectiveMethod() {
	case FilterChebyshevI, FilterElliptic:
		return "所示边界为单程通带纹波边缘 −Rp。"
	case FilterChebyshevII:
		return "所示边界为单程阻带衰减边缘 −Rs。"
	default:
		return "所示边界为单程半功率点（约 −3.01 dB）。"
	}
}

type temporalFilter struct {
	sections            []viewFilterSOS
	pad, minimum, order int
	zeroPhase           bool
}

// MinSamples returns the required real temporal context, not the visible
// output length. A short output can be cropped from a valid input trace.
func (p ViewBandpass) MinSamples(dtUS int) (int, error) {
	d, err := p.design(dtUS)
	return d.minimum, err
}

// ImplementationOrder is the one-pass order after the response transform.
// The two-pass mode executes this design twice in opposite directions.
func (p ViewBandpass) ImplementationOrder(dtUS int) (int, error) {
	d, err := p.design(dtUS)
	return d.order, err
}

// ResponseDB evaluates the actual sections used for execution. Zero-phase
// mode doubles the one-pass dB response; exact zeros are negative infinity.
func (p ViewBandpass) ResponseDB(dtUS int, frequencyHz []float64) ([]float64, error) {
	d, err := p.design(dtUS)
	if err != nil {
		return nil, err
	}
	nyquist := 5e5 / float64(dtUS)
	multiplier := 20.0
	if d.zeroPhase {
		multiplier = 40
	}
	out := make([]float64, len(frequencyHz))
	for i, hz := range frequencyHz {
		if !viewFilterFinite(hz) || hz < 0 || hz > nyquist {
			return nil, fmt.Errorf("response frequency must be within 0–%g Hz", nyquist)
		}
		z := cmplx.Exp(complex(0, -math.Pi*hz/nyquist))
		if hz == 0 {
			z = 1
		} else if hz == nyquist {
			z = -1
		}
		for _, s := range d.sections {
			n := complex(s.b0, 0) + z*(complex(s.b1, 0)+z*complex(s.b2, 0))
			den := 1 + z*(complex(s.a1, 0)+z*complex(s.a2, 0))
			out[i] += multiplier * (math.Log10(cmplx.Abs(n)) - math.Log10(cmplx.Abs(den)))
		}
	}
	return out, nil
}

func (p ViewBandpass) design(dtUS int) (temporalFilter, error) {
	p = p.Canonical()
	if dtUS <= 0 {
		return temporalFilter{}, errors.New("滤波采样间隔必须为正数（微秒）")
	}
	if p.Order < 1 || p.Order > FilterMaxOrder {
		return temporalFilter{}, fmt.Errorf("滤波设计阶数必须为 1–%d", FilterMaxOrder)
	}
	if p.Phase != FilterZeroPhase && p.Phase != FilterForward {
		return temporalFilter{}, errors.New("未知滤波相位方式")
	}
	nyquist := 5e5 / float64(dtUS)
	validCorner := func(hz float64) bool { return viewFilterFinite(hz) && hz > 0 && hz < nyquist }
	switch p.Response {
	case FilterLowpass, FilterHighpass:
		if !validCorner(p.LowHz) {
			return temporalFilter{}, fmt.Errorf("滤波边界必须满足 0 < f < Nyquist（%g Hz）", nyquist)
		}
	case FilterBandpass, FilterBandstop, FilterLegacyBandpass:
		if !validCorner(p.LowHz) || !validCorner(p.HighHz) || p.LowHz >= p.HighHz {
			return temporalFilter{}, fmt.Errorf("滤波边界必须满足 0 < 低频 < 高频 < Nyquist（%g Hz）", nyquist)
		}
	default:
		return temporalFilter{}, errors.New("未知滤波响应类型")
	}
	if p.Response == FilterLegacyBandpass {
		if p.Method != FilterButterworth {
			return temporalFilter{}, errors.New("兼容带通仅支持 Butterworth；其他设计方法请选择标准带通")
		}
		if p.Order == 4 {
			// Preserve existing processing recipes/results to their previous
			// precision, including the 27-point extension.
			s, err := p.sections(dtUS)
			if err != nil {
				return temporalFilter{}, err
			}
			return finishTemporalDesign(s[:], 8, p.Phase), nil
		}
		lo, hi := p, p
		lo.Response, hi.Response, hi.LowHz = FilterHighpass, FilterLowpass, p.HighHz
		left, err := lo.design(dtUS)
		if err != nil {
			return temporalFilter{}, err
		}
		right, err := hi.design(dtUS)
		if err != nil {
			return temporalFilter{}, err
		}
		return finishTemporalDesign(append(left.sections, right.sections...), 2*p.Order, p.Phase), nil
	}
	zeros, poles, gain, err := lowpassPrototype(p)
	if err != nil {
		return temporalFilter{}, err
	}
	lo := math.Tan(math.Pi * p.LowHz / (2 * nyquist))
	hi := math.Tan(math.Pi * p.HighHz / (2 * nyquist))
	mid := math.Sqrt(lo * hi)
	degree := len(poles) - len(zeros)
	ref := complex(1, 0)
	bilinear := func(root complex128) complex128 { return (1 + root) / (1 - root) }
	transform := func(roots []complex128) []complex128 {
		n := len(roots)
		if p.Response == FilterBandpass || p.Response == FilterBandstop {
			n *= 2
		}
		out := make([]complex128, 0, n)
		for _, r := range roots {
			switch p.Response {
			case FilterLowpass:
				out = append(out, bilinear(r*complex(lo, 0)))
			case FilterHighpass:
				out = append(out, bilinear(complex(lo, 0)/r))
			case FilterBandpass, FilterBandstop:
				b := r * complex((hi-lo)/2, 0)
				if p.Response == FilterBandstop {
					b = complex((hi-lo)/2, 0) / r
				}
				m := complex(mid*mid, 0)
				d := cmplx.Sqrt(b*b - m)
				x := b + d
				if cmplx.Abs(b-d) > cmplx.Abs(x) {
					x = b - d
				}
				out = append(out, bilinear(x), bilinear(m/x))
			}
		}
		return out
	}
	zeros, poles = transform(zeros), transform(poles)
	for i := 0; i < degree; i++ {
		switch p.Response {
		case FilterLowpass:
			zeros = append(zeros, -1)
		case FilterHighpass:
			zeros = append(zeros, 1)
		case FilterBandpass:
			zeros = append(zeros, 1, -1)
		case FilterBandstop:
			r := bilinear(complex(0, mid))
			zeros = append(zeros, r, cmplx.Conj(r))
		}
	}
	if p.Response == FilterHighpass {
		ref = -1
	} else if p.Response == FilterBandpass {
		ref = bilinear(complex(0, mid))
	}
	sos, err := designSOS(zeros, poles, ref, gain)
	if err != nil {
		return temporalFilter{}, err
	}
	return finishTemporalDesign(sos, len(poles), p.Phase), nil
}

func finishTemporalDesign(sos []viewFilterSOS, order int, phase FilterPhase) temporalFilter {
	d := temporalFilter{sections: sos, minimum: 2, order: order, zeroPhase: phase == FilterZeroPhase}
	if d.zeroPhase {
		numeratorFirstOrder, denominatorFirstOrder := 0, 0
		for _, s := range sos {
			if s.b2 == 0 {
				numeratorFirstOrder++
			}
			if s.a2 == 0 {
				denominatorFirstOrder++
			}
		}
		d.pad = 3 * (2*len(sos) + 1 - min(numeratorFirstOrder, denominatorFirstOrder))
		d.minimum = d.pad + 1
	} else {
		// The forward pass uses a constant pre-history, not reflected future
		// samples. Require at least one sample beyond the implemented order.
		d.minimum = max(2, order+1)
	}
	return d
}

type filterRootPair struct {
	first, second complex128
	single        bool
}

func pairFilterRoots(roots []complex128) ([]filterRootPair, error) {
	pending := append([]complex128(nil), roots...)
	pairs, realRoots := make([]filterRootPair, 0, (len(roots)+1)/2), make([]complex128, 0, len(roots))
	for len(pending) > 0 {
		x := pending[0]
		pending = pending[1:]
		if !viewFilterFinite(real(x)) || !viewFilterFinite(imag(x)) {
			return nil, errors.New("滤波设计存在非有限根")
		}
		if math.Abs(imag(x)) <= 8e-15 {
			realRoots = append(realRoots, complex(real(x), 0))
			continue
		}
		index, nearest := -1, math.Inf(1)
		for i, y := range pending {
			if imag(x)*imag(y) < 0 {
				if diff := cmplx.Abs(cmplx.Conj(x) - y); diff < nearest {
					index, nearest = i, diff
				}
			}
		}
		if index < 0 || nearest > 1e-9 {
			return nil, errors.New("滤波设计缺少共轭根，无法构造稳定二阶节")
		}
		avg := (x + cmplx.Conj(pending[index])) / 2
		pairs = append(pairs, filterRootPair{first: avg, second: cmplx.Conj(avg)})
		pending = append(pending[:index], pending[index+1:]...)
	}
	sort.Slice(realRoots, func(i, j int) bool { return real(realRoots[i]) < real(realRoots[j]) })
	// Reserve the smallest-magnitude real root as the lone first-order
	// section. Pair opposite signs first (well conditioned for passbands).
	if len(realRoots)%2 != 0 {
		idx := 0
		for i, r := range realRoots {
			if math.Abs(real(r)) < math.Abs(real(realRoots[idx])) {
				idx = i
			}
		}
		pairs = append(pairs, filterRootPair{first: realRoots[idx], single: true})
		realRoots = append(realRoots[:idx], realRoots[idx+1:]...)
	}
	for len(realRoots) > 0 {
		pairs = append(pairs, filterRootPair{first: realRoots[0], second: realRoots[len(realRoots)-1]})
		realRoots = realRoots[1 : len(realRoots)-1]
	}
	return pairs, nil
}

func designSOS(zeros, poles []complex128, ref complex128, gain float64) ([]viewFilterSOS, error) {
	zPairs, err := pairFilterRoots(zeros)
	if err != nil {
		return nil, err
	}
	pPairs, err := pairFilterRoots(poles)
	if err != nil {
		return nil, err
	}
	if len(zPairs) != len(pPairs) || len(pPairs) == 0 {
		return nil, errors.New("滤波设计阶数与零极点不一致")
	}
	radius := func(p filterRootPair) float64 { return max(cmplx.Abs(p.first), cmplx.Abs(p.second)) }
	sort.SliceStable(pPairs, func(i, j int) bool { return radius(pPairs[i]) < radius(pPairs[j]) })
	sos := make([]viewFilterSOS, 0, len(pPairs))
	perSectionGain := math.Pow(gain, 1/float64(len(pPairs)))
	for _, p := range pPairs {
		nearest, index := math.Inf(1), -1
		for i, z := range zPairs {
			if p.single == z.single {
				diff := min(cmplx.Abs(p.first-z.first), cmplx.Abs(p.first-z.second))
				if diff < nearest {
					nearest, index = diff, i
				}
			}
		}
		if index == -1 || radius(p) >= 1 {
			return nil, errors.New("滤波边界或设计参数过于极端，无法构造稳定 float64 二阶节")
		}
		z := zPairs[index]
		zPairs = append(zPairs[:index], zPairs[index+1:]...)
		b := cmplx.Abs((ref - z.first) * (ref - z.second))
		a := cmplx.Abs((ref - p.first) * (ref - p.second))
		k := perSectionGain * a / b
		s := viewFilterSOS{
			b0: k, b1: -k * real(z.first+z.second), b2: k * real(z.first*z.second),
			a1: -real(p.first + p.second), a2: real(p.first * p.second),
		}
		s.dcGain = k * real((1-z.first)*(1-z.second)/(1-p.first)/(1-p.second))
		if !viewFilterFinite(k) || k <= 0 || !viewFilterFinite(s.dcGain) ||
			math.Abs(s.a2) >= 1 || 1+s.a1+s.a2 <= 0 || 1-s.a1+s.a2 <= 0 {
			return nil, errors.New("滤波边界或设计参数过于极端，无法构造稳定 float64 二阶节")
		}
		sos = append(sos, s)
	}
	return sos, nil
}
