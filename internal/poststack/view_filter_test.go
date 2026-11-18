package poststack

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestViewBandpassValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		dt   int
		p    ViewBandpass
	}{
		{"zero interval", 0, ViewBandpass{LowHz: 5, HighHz: 60}},
		{"negative interval", -2000, ViewBandpass{LowHz: 5, HighHz: 60}},
		{"zero low", 2000, ViewBandpass{LowHz: 0, HighHz: 60}},
		{"negative low", 2000, ViewBandpass{LowHz: -5, HighHz: 60}},
		{"equal", 2000, ViewBandpass{LowHz: 60, HighHz: 60}},
		{"reversed", 2000, ViewBandpass{LowHz: 70, HighHz: 60}},
		{"Nyquist", 2000, ViewBandpass{LowHz: 5, HighHz: 250}},
		{"above Nyquist", 2000, ViewBandpass{LowHz: 5, HighHz: 251}},
		{"NaN low", 2000, ViewBandpass{LowHz: math.NaN(), HighHz: 60}},
		{"Inf high", 2000, ViewBandpass{LowHz: 5, HighHz: math.Inf(1)}},
		{"unrepresentable low corner", 2000, ViewBandpass{LowHz: math.SmallestNonzeroFloat64, HighHz: 60}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.p.Validate(tc.dt); err == nil {
				t.Fatal("accepted invalid bandpass")
			}
		})
	}
	for _, dt := range []int{500, 1000, 2000, 4000} {
		if err := (ViewBandpass{LowHz: 5, HighHz: 60}).Validate(dt); err != nil {
			t.Fatalf("dt %d: %v", dt, err)
		}
	}
}

func TestFilterTracePreservesPassbandAndRejectsOutsideBandWithoutPhaseDelay(t *testing.T) {
	const dtUS, count = 1000, 20000
	p := ViewBandpass{LowHz: 20, HighHz: 100}
	frequencies := []float64{5, 50, 250}
	x := make([]float64, count)
	for i := range x {
		x[i] = 4 // DC must also disappear.
		for _, f := range frequencies {
			x[i] += math.Sin(2 * math.Pi * f * float64(i) / 1000)
		}
	}
	before := append([]float64(nil), x...)
	y, err := FilterTrace(context.Background(), x, dtUS, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(y) != len(x) || !reflect.DeepEqual(x, before) {
		t.Fatal("filter changed input samples or output length")
	}
	// Measure an integer number of periods well away from finite-window edges.
	for _, f := range frequencies {
		sine, cosine := viewTestProjection(y[5000:15000], 5000, f, 1000)
		want := 1 / (1 + math.Pow(math.Tan(math.Pi*p.LowHz/1000)/math.Tan(math.Pi*f/1000), 8)) /
			(1 + math.Pow(math.Tan(math.Pi*f/1000)/math.Tan(math.Pi*p.HighHz/1000), 8))
		if math.Abs(sine-want) > 1e-8 {
			t.Errorf("%g Hz amplitude=%g want %g", f, sine, want)
		}
		if math.Abs(cosine) > 1e-8 {
			t.Errorf("%g Hz phase quadrature=%g, expected zero", f, cosine)
		}
		if f == 50 && sine < .99 {
			t.Errorf("passband attenuated unexpectedly: %g", sine)
		}
		if f != 50 && math.Abs(sine) > .001 {
			t.Errorf("stopband not suppressed: %g Hz %g", f, sine)
		}
	}
}

func TestFilterTraceImpulseIsCenteredAndSymmetric(t *testing.T) {
	x := make([]float64, 4097)
	center := len(x) / 2
	x[center] = 1
	y, err := FilterTrace(nil, x, 2000, ViewBandpass{LowHz: 10, HighHz: 90})
	if err != nil {
		t.Fatal(err)
	}
	maximum := 0
	for i := range y {
		if math.Abs(y[i]) > math.Abs(y[maximum]) {
			maximum = i
		}
	}
	if maximum != center {
		t.Fatalf("impulse peak shifted by %d samples", maximum-center)
	}
	for i := 1; i < 500; i++ {
		if math.Abs(y[center-i]-y[center+i]) > 1e-12 {
			t.Fatalf("impulse asymmetric at %d: %.15g / %.15g", i, y[center-i], y[center+i])
		}
	}
}

func TestFilterTraceRepeatableImmutableAndScaleLinear(t *testing.T) {
	x := make([]float64, 351)
	for i := range x {
		x[i] = math.Sin(float64(i)*.37) + .1*math.Cos(float64(i)*.91)
	}
	p := ViewBandpass{LowHz: 5, HighHz: 60}
	before := append([]float64(nil), x...)
	first, err := FilterTrace(nil, x, 2000, p)
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 100; run++ {
		got, err := FilterTrace(nil, x, 2000, p)
		if err != nil || !reflect.DeepEqual(got, first) || !reflect.DeepEqual(x, before) {
			t.Fatalf("run %d changed data or state: %v", run, err)
		}
	}
	for _, scale := range []float64{-3.25, 1e200, 1e-280} {
		scaled := make([]float64, len(x))
		for i := range scaled {
			scaled[i] = x[i] * scale
		}
		got, err := FilterTrace(nil, scaled, 2000, p)
		if err != nil {
			t.Fatal(err)
		}
		for i, v := range got {
			if math.Abs(v/scale-first[i]) > 1e-11 {
				t.Fatalf("amplitude changed for scale %g at %d: %g / %g", scale, i, v/scale, first[i])
			}
		}
	}
	first[0] = 1000
	if !reflect.DeepEqual(x, before) {
		t.Fatal("output aliases source")
	}
}

func TestFilterTraceEdgesShortInvalidAndConstant(t *testing.T) {
	p := ViewBandpass{LowHz: 5, HighHz: 60}
	for n := 0; n < ViewBandpassMinSamples; n++ {
		if y, err := FilterTrace(nil, make([]float64, n), 2000, p); err == nil || y != nil {
			t.Fatalf("accepted too-short input %d", n)
		}
	}
	for _, constant := range []float64{0, 7, -30, math.MaxFloat64} {
		x := make([]float64, ViewBandpassMinSamples)
		for i := range x {
			x[i] = constant
		}
		y, err := FilterTrace(nil, x, 2000, p)
		if err != nil {
			t.Fatal(err)
		}
		for i, v := range y {
			if v != 0 {
				t.Fatalf("constant input %g has edge transient at %d: %g", constant, i, v)
			}
		}
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		x := make([]float64, 100)
		x[23] = v
		if y, err := FilterTrace(nil, x, 2000, p); err == nil || y != nil || !strings.Contains(err.Error(), "23") {
			t.Fatalf("nonfinite input not rejected with sample location: %v", err)
		}
	}
}

func TestFilterTraceCancellationBeforeAndDuringWork(t *testing.T) {
	x := make([]float64, 65536)
	for i := range x {
		x[i] = math.Sin(float64(i) * .1)
	}
	before := append([]float64(nil), x...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if y, err := FilterTrace(ctx, x, 2000, ViewBandpass{LowHz: 5, HighHz: 60}); !errors.Is(err, context.Canceled) || y != nil {
		t.Fatalf("pre-cancelled task accepted: %v", err)
	}
	// Cancellation occurs during filtering, after validation and input copy;
	// deterministic cancellation avoids a timing-dependent goroutine test.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	counted := &viewTestCancelContext{Context: ctx, cancel: cancel, remaining: 140}
	if y, err := FilterTrace(counted, x, 2000, ViewBandpass{LowHz: 5, HighHz: 60}); !errors.Is(err, context.Canceled) || y != nil {
		t.Fatalf("in-flight task not cancelled: %v", err)
	}
	if !reflect.DeepEqual(x, before) {
		t.Fatal("cancellation changed source")
	}
}

type viewTestCancelContext struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (c *viewTestCancelContext) Err() error {
	c.remaining--
	if c.remaining == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func viewTestProjection(x []float64, start int, hz, rate float64) (sine, cosine float64) {
	for i, v := range x {
		angle := 2 * math.Pi * hz * float64(start+i) / rate
		sine += v * math.Sin(angle)
		cosine += v * math.Cos(angle)
	}
	return 2 * sine / float64(len(x)), 2 * cosine / float64(len(x))
}
