package display

import (
	"math"
	"testing"
)

func TestExactStatisticsAndMappedDomain(t *testing.T) {
	var s Stats
	for _, x := range []float64{-2, -1, 0, 1, 3, math.NaN(), math.Inf(1)} {
		s.Add(x)
	}
	for _, x := range []float64{-0.5, 0, 0.25, math.NaN()} {
		s.AddMapped(x)
	}
	if s.Raw.Count != 5 || s.Raw.Range != (Range{-2, 3}) || s.Mapping().Range != (Range{-.5, .25}) {
		t.Fatalf("unexpected domains: %+v", s)
	}
	if bins := s.Raw.Bins(Range{-2, 3}, 5); len(bins) != 5 || bins[0] != 1 || bins[4] != 1 {
		t.Fatalf("bad bins: %v", bins)
	}
	var many Stats
	for i := 0; i < 12000; i++ {
		many.Add(float64(i))
	}
	if len(many.Raw.Samples) != sampleLimit || many.Raw.Range != (Range{0, 11999}) || many.Raw.Count != 12000 {
		t.Fatal("distribution sample was used as an extremum")
	}
}

func TestGradientSaturationAndLegacyDirection(t *testing.T) {
	r := Range{-1, 1}
	legacy := LegacyStops(func(i int) RGB { return RGB{R: byte(i)} })
	m := ColorMapping{Range: r, Stops: legacy}
	if m.ColorAt(-2).R != 255 || m.ColorAt(3).R != 0 || m.ColorAt(0).R != 128 {
		t.Fatal("legacy direction or saturation changed")
	}
	if m.ColorAt(math.NaN()) == m.ColorAt(-2) {
		t.Fatal("invalid value is colored as a valid endpoint")
	}
	d := m.Clone()
	d.Stops[len(d.Stops)-1].Color.B = 55
	d.Reverse = true
	if d.ColorAt(-2).B != 55 || m.ColorAt(3).B != 0 {
		t.Fatal("reverse or deep copy failed")
	}
	duplicate := m.Clone()
	duplicate.Stops[1].Position = 0
	if duplicate.Valid() == nil {
		t.Fatal("duplicate control point accepted")
	}
}

func TestAxisUnitsTicksAndConstantRanges(t *testing.T) {
	a := Axis{ID: "time", BaseUnit: "ms", Unit: "s", View: Range{400, 800}}
	if a.DisplayValue(400) != .4 || a.BaseValue(.4) != 400 || a.ValueAtFraction(.25) != 500 {
		t.Fatal("coordinate units changed data or mouse mapping")
	}
	a.Reverse = true
	if a.Pixel(400, 10, 110) != 110 || a.ValueAtFraction(.25) != 700 {
		t.Fatal("reverse is not invertible")
	}
	a.StyleOnly = true
	if a.Valid() == nil {
		t.Fatal("3D reverse unexpectedly accepted")
	}
	for _, r := range []Range{{0, 0}, {1, 1}, {math.MaxFloat64, math.MaxFloat64}, {-math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64}} {
		if !r.Expanded().Valid() {
			t.Fatalf("cannot draw constant range %v", r)
		}
	}
	for _, r := range []Range{{-math.MaxFloat64, math.MaxFloat64}, {0, 100000}, {1e15, 1e15 + 1000}} {
		ticks := Ticks(r, 0, 9)
		if len(ticks) < 2 || len(ticks) > 9 || ticks[0] < r.Min || ticks[len(ticks)-1] > r.Max {
			t.Fatalf("bounded ticks for %v: %v", r, ticks)
		}
		for i := 1; i < len(ticks); i++ {
			if ticks[i] <= ticks[i-1] {
				t.Fatal("duplicate ticks")
			}
		}
	}
}
