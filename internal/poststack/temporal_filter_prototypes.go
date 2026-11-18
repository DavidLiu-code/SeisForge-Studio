package poststack

import (
	"errors"
	"fmt"
	"math"
)

const (
	FilterMinRippleDB      = 0.01
	FilterMaxRippleDB      = 12
	FilterMinAttenuationDB = 6
	FilterMaxAttenuationDB = 160
)

// lowpassPrototype returns normalized analog poles/zeros and the desired
// magnitude at frequency zero, used to scale the realized digital sections.
// Chebyshev II's critical edge is its stopband attenuation point; Chebyshev I
// and Cauer use the passband ripple boundary. Butterworth uses the half-power
// point. These are all *single-pass* definitions.
func lowpassPrototype(p ViewBandpass) (zeros, poles []complex128, dc float64, err error) {
	n := p.Order
	dc = 1
	passRipple := p.Method == FilterChebyshevI || p.Method == FilterElliptic
	stopAttenuation := p.Method == FilterChebyshevII || p.Method == FilterElliptic
	if passRipple && (!viewFilterFinite(p.RippleDB) || p.RippleDB < FilterMinRippleDB || p.RippleDB > FilterMaxRippleDB) {
		err = fmt.Errorf("通带纹波 Rp 必须为 %g–%g dB", FilterMinRippleDB, float64(FilterMaxRippleDB))
		return
	}
	if stopAttenuation && (!viewFilterFinite(p.AttenuationDB) || p.AttenuationDB < FilterMinAttenuationDB || p.AttenuationDB > FilterMaxAttenuationDB) {
		err = fmt.Errorf("阻带衰减 Rs 必须为 %g–%g dB", float64(FilterMinAttenuationDB), float64(FilterMaxAttenuationDB))
		return
	}
	if p.Method == FilterElliptic && p.AttenuationDB <= p.RippleDB {
		return nil, nil, 0, errors.New("Elliptic 阻带衰减 Rs 必须大于通带纹波 Rp")
	}
	if passRipple && n%2 == 0 {
		dc = math.Pow(10, -p.RippleDB/20)
	}
	ripplePower := func(db float64) float64 { return math.Expm1(db * math.Ln10 / 10) }
	var axisReal, axisImag float64
	switch p.Method {
	case FilterButterworth:
		axisReal, axisImag = 1, 1
	case FilterChebyshevI:
		mu := math.Asinh(1/math.Sqrt(ripplePower(p.RippleDB))) / float64(n)
		axisReal, axisImag = math.Sinh(mu), math.Cosh(mu)
	case FilterChebyshevII:
		mu := math.Asinh(math.Sqrt(ripplePower(p.AttenuationDB))) / float64(n)
		axisReal, axisImag = math.Sinh(mu), math.Cosh(mu)
	case FilterElliptic:
		zeros, poles, err = cauerPrototype(n, ripplePower(p.RippleDB), ripplePower(p.AttenuationDB))
		return
	default:
		return nil, nil, 0, errors.New("未知 IIR 设计方法")
	}
	for i := range n {
		theta := math.Pi * float64(2*i+1-n) / (2 * float64(n))
		s, c := math.Sincos(theta)
		root := complex(-axisReal*c, axisImag*s)
		if p.Method == FilterChebyshevII {
			root = 1 / root
			if s != 0 {
				zeros = append(zeros, complex(0, 1/s))
			}
		}
		poles = append(poles, root)
	}
	return
}

// cauerPrototype uses the elliptic degree equation and Jacobi function
// construction of the Cauer low-pass prototype. For a derivation and the
// frequency edge conventions see SciPy's ellipap references (Lutovac–Tosic–
// Evans and Orfanidis): https://docs.scipy.org/doc/scipy/reference/generated/scipy.signal.ellipap.html .
// The implementation here solves the degree and inverse-sc equations with
// monotone bisection, avoiding a theta-series dependency or complex inverse
// special-function calls.
func cauerPrototype(n int, ripplePower, attenuationPower float64) (zeros, poles []complex128, err error) {
	if n == 1 {
		return nil, []complex128{complex(-1/math.Sqrt(ripplePower), 0)}, nil
	}
	m1 := ripplePower / attenuationPower
	k1 := completeEllipticK(1 - m1)
	degreeTarget := completeEllipticK(m1) / (float64(n) * k1)
	// Solve in the complementary parameter so near-one m is representable.
	lo, hi := 0.0, 1.0
	for range 80 {
		c := (lo + hi) / 2
		ratio := completeEllipticK(1-c) / completeEllipticK(c)
		if ratio < degreeTarget {
			lo = c
		} else {
			hi = c
		}
	}
	complement := (lo + hi) / 2
	m := 1 - complement
	if m <= 0 || complement < 1e-14 {
		return nil, nil, errors.New("Elliptic 参数过于极端，超出稳定 float64 设计范围")
	}
	k := completeEllipticK(complement)
	target := math.Atan(1 / math.Sqrt(ripplePower))
	a, b := 0.0, completeEllipticK(m1)
	for range 64 {
		v := (a + b) / 2
		s, c, _ := jacobiReal(v, m1)
		if math.Atan2(s, c) < target {
			a = v
		} else {
			b = v
		}
	}
	sv, cv, dv := jacobiReal(k*(a+b)/(2*float64(n)*k1), m)
	for j := 1 - n%2; j < n; j += 2 {
		s, c, d := jacobiReal(float64(j)*k/float64(n), complement)
		// 1-(d*sv)^2 written as a sum of nonnegative terms.
		den := cv*cv + m*s*s*sv*sv
		root := complex(-c*d*sv*cv/den, -s*dv/den)
		poles = append(poles, root)
		if j != 0 {
			poles = append(poles, complex(real(root), -imag(root)))
			z := 1 / (math.Sqrt(m) * s)
			zeros = append(zeros, complex(0, z), complex(0, -z))
		}
	}
	return
}

// The arguments are complementary parameters (1 - m), which also avoids
// forming 1 - m in a sensitive near-one design.
func completeEllipticK(complement float64) float64 {
	if complement == 0 {
		return math.Inf(1)
	}
	a, b := 1.0, math.Sqrt(complement)
	for i := 0; i < 32 && math.Abs(a-b) > 2e-16*a; i++ {
		a, b = (a+b)/2, math.Sqrt(a*b)
	}
	return math.Pi / (2 * a)
}

// Real Jacobi sn, cn, dn by AGM descent. All callers use 0 <= u <= K.
func jacobiReal(u, complement float64) (sn, cn, dn float64) {
	a, b, scale := 1.0, math.Sqrt(complement), 1.0
	var ratios [32]float64
	n := 0
	for ; n < len(ratios) && math.Abs(a-b) > 2e-16*a; n++ {
		mean := (a + b) / 2
		ratios[n] = (a - b) / (2 * mean)
		a, b = mean, math.Sqrt(a*b)
		scale *= 2
	}
	phi := scale * a * u
	for i := n - 1; i >= 0; i-- {
		phi = (phi + math.Asin(ratios[i]*math.Sin(phi))) / 2
	}
	sn, cn = math.Sincos(phi)
	return sn, cn, math.Hypot(cn, math.Sqrt(complement)*sn)
}
