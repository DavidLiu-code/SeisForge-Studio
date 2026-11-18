package poststack

import (
	"context"
	"errors"
	"math"
	"testing"
)

func testInput() Input {
	return Input{Metadata: Metadata{TraceCount: 2, SamplesPerTrace: 3, SampleIntervalUS: 2000, Direction: "inline"}, Samples: [][]float32{{1, -2, 3}, {4, 5, -6}}}
}

func TestRegistryAndCopy(t *testing.T) {
	r := NewRegistry()
	specs := r.Specs()
	if len(specs) != 2 || specs[0].ID != "copy" || specs[1].ID != "gain" {
		t.Fatalf("unexpected specs: %#v", specs)
	}
	a, ok := r.Get("copy")
	if !ok {
		t.Fatal("copy not registered")
	}
	in := testInput()
	out, err := a.Run(context.Background(), in, Params{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	in.Samples[0][0] = 99
	if out.Samples[0][0] != 1 || out.Algorithm != "copy" {
		t.Fatalf("copy mutated or missing provenance: %#v", out)
	}
}

func TestGainAndValidation(t *testing.T) {
	a, _ := NewRegistry().Get("gain")
	out, err := a.Run(context.Background(), testInput(), Params{Values: map[string]float64{"factor": 1.5}}, nil)
	if err != nil || out.Samples[0][1] != -3 || out.Samples[1][2] != -9 {
		t.Fatalf("gain result: %#v %v", out, err)
	}
	for _, factor := range []float64{-1, 11, math.NaN(), math.Inf(1)} {
		if err := a.Validate(testInput().Metadata, Params{Values: map[string]float64{"factor": factor}}); err == nil {
			t.Fatalf("factor %v should fail", factor)
		}
	}
}

func TestLegacyRegistryDoesNotExposePrivateAlgorithmStubs(t *testing.T) {
	for _, id := range []string{"cadzow_residual", "radon", "unknown"} {
		if _, ok := NewRegistry().Get(id); ok {
			t.Fatalf("private or unknown algorithm %q must be explicitly installed via manifest", id)
		}
	}
}

func TestInputRejectsInvalidSamples(t *testing.T) {
	in := testInput()
	in.Samples[0][0] = float32(math.NaN())
	if err := in.Validate(); err == nil {
		t.Fatal("expected NaN rejection")
	}
	in = testInput()
	in.Samples = in.Samples[:1]
	if err := in.Validate(); err == nil {
		t.Fatal("expected trace count rejection")
	}
}

func TestGainRejectsFloat32Overflow(t *testing.T) {
	in := testInput()
	in.Samples[0][0] = math.MaxFloat32
	a, _ := NewRegistry().Get("gain")
	if _, err := a.Run(context.Background(), in, Params{Values: map[string]float64{"factor": 10}}, nil); err == nil {
		t.Fatal("overflow published")
	}
	if in.Samples[0][0] != math.MaxFloat32 {
		t.Fatal("A changed")
	}
}

func TestAlgorithmsCancellationAtProgressDoesNotPublish(t *testing.T) {
	for _, id := range []string{"copy", "gain"} {
		t.Run(id, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a, _ := NewRegistry().Get(id)
			out, err := a.Run(ctx, testInput(), Params{}, func(Progress) { cancel() })
			if !errors.Is(err, context.Canceled) || len(out.Samples) != 0 {
				t.Fatalf("%#v %v", out, err)
			}
		})
	}
}

func TestSampleAndGeometryCompatibility(t *testing.T) {
	a := testInput().Metadata
	if err := CheckWindowCompatibility(a, a, SampleWindow{1, 3}, SampleWindow{1, 3}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Metadata){
		"count":     func(b *Metadata) { b.SamplesPerTrace++ },
		"interval":  func(b *Metadata) { b.SampleIntervalUS++ },
		"origin":    func(b *Metadata) { b.TimeOriginUS++ },
		"trace":     func(b *Metadata) { b.TraceCount++ },
		"direction": func(b *Metadata) { b.Direction = "crossline" },
		"geometry":  func(b *Metadata) { b.InlineCount++ },
	} {
		t.Run(name, func(t *testing.T) {
			b := a
			mutate(&b)
			if CheckCompatibility(a, b) == nil {
				t.Fatal("incompatible metadata accepted")
			}
		})
	}
	if CheckWindowCompatibility(a, a, SampleWindow{0, 2}, SampleWindow{1, 3}) == nil {
		t.Fatal("unequal windows accepted")
	}
	for _, w := range []SampleWindow{{-1, 2}, {0, 4}, {1, 1}} {
		if w.Validate(a) == nil {
			t.Fatalf("invalid window %v", w)
		}
	}
}
