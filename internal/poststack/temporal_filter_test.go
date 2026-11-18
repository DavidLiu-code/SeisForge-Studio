package poststack

import (
	"math"
	"reflect"
	"testing"
)

// A bilinear-transform reference for a Butterworth bandpass: use the
// normalized analog low-pass magnitude, not production SOS or preview data.
func referenceButterworthBandpassAmplitude(hz float64, order int) float64 {
	x := math.Tan(math.Pi * hz / 500)
	lo := math.Tan(math.Pi * 20 / 500)
	hi := math.Tan(math.Pi * 162.5 / 500)
	q := math.Abs((x*x - lo*hi) / ((hi - lo) * x))
	return 1 / math.Sqrt(1+math.Pow(q, float64(2*order)))
}

func TestTemporalFilterIndependentResponseAtKnownTones(t *testing.T) {
	const dtUS, fs, n, start, stop = 2000, 500, 8000, 2000, 6000
	tones := []float64{10, 40, 100}
	wave := make([]float64, n)
	for i := range wave {
		for _, hz := range tones {
			wave[i] += math.Sin(2 * math.Pi * hz * float64(i) / fs)
		}
	}
	// Prewarped frequencies and the normalized analog prototype response give
	// an independent magnitude reference. Chebyshev T_n uses cos in-band
	// and cosh beyond the edge. An order-1 elliptic prototype has no finite
	// zeros and has the same closed form as the order-1 Chebyshev I prototype.
	cheb := func(order int, x float64) float64 {
		if x <= 1 {
			return math.Cos(float64(order) * math.Acos(x))
		}
		return math.Cosh(float64(order) * math.Acosh(x))
	}
	warped := func(f float64) float64 { return math.Tan(math.Pi * f / fs) }
	for _, tc := range []struct {
		name string
		p    ViewBandpass
		amp  func(float64) float64
	}{
		{"Butterworth-bandpass", ViewBandpass{LowHz: 20, HighHz: 162.5, Response: FilterBandpass, Method: FilterButterworth, Order: 4}, func(f float64) float64 {
			return referenceButterworthBandpassAmplitude(f, 4)
		}},
		{"Chebyshev-I-lowpass", ViewBandpass{LowHz: 55, Response: FilterLowpass, Method: FilterChebyshevI, Order: 3, RippleDB: 1.25}, func(f float64) float64 {
			x := cheb(3, warped(f)/warped(55))
			return 1 / math.Sqrt(1+(math.Pow(10, .125)-1)*x*x)
		}},
		{"Chebyshev-II-highpass", ViewBandpass{LowHz: 30, Response: FilterHighpass, Method: FilterChebyshevII, Order: 4, AttenuationDB: 45}, func(f float64) float64 {
			x := cheb(4, warped(f)/warped(30))
			return math.Abs(x) / math.Sqrt(x*x+math.Pow(10, 4.5)-1)
		}},
		{"Elliptic-bandstop", ViewBandpass{LowHz: 30, HighHz: 65, Response: FilterBandstop, Method: FilterElliptic, Order: 1, RippleDB: 1, AttenuationDB: 55}, func(f float64) float64 {
			x, l, h := warped(f), warped(30), warped(65)
			q := (h - l) * x / (l*h - x*x)
			return 1 / math.Sqrt(1+(math.Pow(10, .1)-1)*q*q)
		}},
	} {
		for _, phase := range []FilterPhase{FilterForward, FilterZeroPhase} {
			t.Run(tc.name+"/"+string(phase), func(t *testing.T) {
				p := tc.p
				p.Phase = phase
				got, err := FilterTrace(nil, wave, dtUS, p)
				if err != nil || len(got) != n {
					t.Fatalf("filtered samples = %d, want %d: %v", len(got), n, err)
				}
				for _, hz := range tones {
					s, c := viewTestProjection(got[start:stop], start, hz, fs)
					want := tc.amp(hz)
					if phase == FilterZeroPhase {
						want *= want
						if math.Abs(c) > 2e-8 || math.Abs(s-want) > 2e-8 {
							t.Fatalf("%g Hz: amplitude/phase %g + %gi, want %g + 0i", hz, s, c, want)
						}
					}
					gain := math.Hypot(s, c)
					if math.Abs(gain-want) > 2e-8 {
						t.Fatalf("%g Hz: gain %g, want %g", hz, gain, want)
					}
					t.Logf("%g Hz gain %.10f (reference %.10f), quadrature %.10g", hz, gain, want, c)
				}
			})
		}
	}
}

func TestTemporalFilterStandardBoundaryAndPhase(t *testing.T) {
	wantBoundary := map[FilterMethod]float64{
		FilterButterworth: -10 * math.Log10(2), FilterChebyshevI: -1.25,
		FilterChebyshevII: -54, FilterElliptic: -1.25,
	}
	for _, method := range []FilterMethod{FilterButterworth, FilterChebyshevI, FilterChebyshevII, FilterElliptic} {
		for _, kind := range []FilterResponse{FilterLowpass, FilterHighpass, FilterBandpass, FilterBandstop} {
			for _, n := range []int{1, 3, 4, 8} {
				p := ViewBandpass{LowHz: 40, HighHz: 162.5, Response: kind, Method: method, Order: n, Phase: FilterForward, RippleDB: 1.25, AttenuationDB: 54}
				frequencies := []float64{p.LowHz}
				wantOrder := n
				if kind == FilterBandpass || kind == FilterBandstop {
					frequencies = append(frequencies, p.HighHz)
					wantOrder *= 2
				}
				gotOrder, err := p.ImplementationOrder(2000)
				if err != nil || gotOrder != wantOrder {
					t.Fatalf("%s %s %d: realized order %d, want %d (%v)", method, kind, n, gotOrder, wantOrder, err)
				}
				forward, err := p.ResponseDB(2000, frequencies)
				if err != nil {
					t.Fatal(err)
				}
				p.Phase = FilterZeroPhase
				zero, err := p.ResponseDB(2000, frequencies)
				if err != nil {
					t.Fatal(err)
				}
				for i, value := range forward {
					if math.Abs(value-wantBoundary[method]) > 1e-7 || math.Abs(zero[i]-2*value) > 1e-12 {
						t.Errorf("%s %s %d at %.4g Hz: forward %.9g dB, zero %.9g dB; want %.9g (forward)", method, kind, n, frequencies[i], value, zero[i], wantBoundary[method])
					}
				}
			}
		}
	}
}

func TestTemporalFilterMeasuredResponseAndCroppingPerTrace(t *testing.T) {
	const dtUS, sampleRate, n = 2000, 500, 8000
	wave := make([]float64, n)
	for i := range wave {
		for _, hz := range []float64{15, 80, 190} {
			wave[i] += math.Sin(2 * math.Pi * hz * float64(i) / sampleRate)
		}
	}
	p := ViewBandpass{LowHz: 40, HighHz: 162.5, Response: FilterBandpass, Method: FilterElliptic, Order: 5, Phase: FilterZeroPhase, RippleDB: 1, AttenuationDB: 60}
	freq := []float64{15, 80, 190}
	db, err := p.ResponseDB(dtUS, freq)
	if err != nil {
		t.Fatal(err)
	}
	if db[0] > -50 || db[1] < -2.001 || db[2] > -20 {
		t.Fatalf("unexpected pass/stop response: %v", db)
	}
	got, err := FilterTrace(nil, wave, dtUS, p)
	if err != nil || len(got) != n {
		t.Fatalf("invalid trace result: %v", err)
	}
	for i, hz := range freq {
		s, c := viewTestProjection(got[2000:6000], 2000, hz, sampleRate)
		if want := math.Pow(10, db[i]/20); math.Abs(s-want) > 1e-7 || math.Abs(c) > 1e-7 {
			t.Fatalf("freq %.3g: measured %g + %gi, model %g", hz, s, c, want)
		}
	}
	first, last := 467, 468
	a, err := FilterTraceWindow(nil, wave, dtUS, p, first, last)
	if err != nil || !reflect.DeepEqual(a, got[first:last+1]) {
		t.Fatalf("short crop differs from full processed trace: %v %v", a, err)
	}
	b, err := FilterTrace(nil, make([]float64, n), dtUS, p)
	if err != nil || !reflect.DeepEqual(b, make([]float64, n)) {
		t.Fatalf("independent all-zero neighboring trace changed: %v", err)
	}
}

func TestTemporalFilterExplicitValidationAndReconfiguresResponse(t *testing.T) {
	base := ViewBandpass{LowHz: 60, Response: FilterLowpass, Method: FilterElliptic, Order: 6, Phase: FilterZeroPhase, RippleDB: 1, AttenuationDB: 50}
	freq := []float64{10, 50, 60, 65, 80, 130}
	want, err := base.ResponseDB(2000, freq)
	if err != nil {
		t.Fatal(err)
	}
	for _, tweak := range []func(*ViewBandpass){
		func(p *ViewBandpass) { p.Order = 8 },
		func(p *ViewBandpass) { p.RippleDB = 2 },
		func(p *ViewBandpass) { p.AttenuationDB = 70 },
		func(p *ViewBandpass) { p.Response = FilterHighpass },
		func(p *ViewBandpass) { p.Method = FilterChebyshevI },
	} {
		p := base
		tweak(&p)
		got, err := p.ResponseDB(2000, freq)
		if err != nil || reflect.DeepEqual(got, want) {
			t.Fatalf("ineffective visual/execution parameter change %+v: %v", p, err)
		}
	}
	samples, err := base.MinSamples(2000)
	if err != nil || samples <= 7 {
		t.Fatalf("expected reflection precheck; got %d samples, %v", samples, err)
	}
	if _, err := FilterTrace(nil, make([]float64, samples-1), 2000, base); err == nil {
		t.Fatal("short record accepted")
	}
	if _, err := FilterTrace(nil, make([]float64, samples), 2000, base); err != nil {
		t.Fatalf("minimum approved context rejected: %v", err)
	}
	for _, tweak := range []func(*ViewBandpass){
		func(p *ViewBandpass) { p.LowHz = 250 },
		func(p *ViewBandpass) { p.Order = FilterMaxOrder + 1 },
		func(p *ViewBandpass) { p.RippleDB = math.NaN() },
		func(p *ViewBandpass) { p.RippleDB, p.AttenuationDB = 12, 6 },
		func(p *ViewBandpass) { p.Phase = "unsupported" },
	} {
		p := base
		tweak(&p)
		if err := p.Validate(2000); err == nil {
			t.Fatalf("invalid filter %+v accepted", p)
		}
	}
	params, err := BandpassParameters(map[string]any{
		FilterResponseParameter: string(FilterLowpass), FilterMethodParameter: string(FilterButterworth),
		FilterRippleParameter: "uncommitted draft", FilterAttenuationParameter: math.NaN(), BandpassHighParameter: "hidden error",
	}, 2000)
	if err != nil || params.HighHz != 0 || params.RippleDB != 0 || params.AttenuationDB != 0 {
		t.Fatalf("inapplicable fields affected active filter: %+v %v", params, err)
	}
}
