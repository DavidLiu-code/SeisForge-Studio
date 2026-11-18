package display

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestCompactAmplitudeLabelsKeepDistinctBounds(t *testing.T) {
	for _, tt := range []struct {
		v    float64
		want string
	}{{26641.25435964238, "26641"}, {-20540.483284251517, "-20540"}, {9999.25, "9999.25"}, {10000, "10000"}, {-999999.25, "-999999"}, {1234567, "1.235e+06"}, {-1234567, "-1.235e+06"}, {math.Copysign(0, -1), "0"}, {math.Inf(1), "—"}, {1.23456789e-12, "1.23457e-12"}} {
		if got := FormatAmplitude(tt.v); got != tt.want {
			t.Fatalf("%v: got %q want %q", tt.v, got, tt.want)
		}
	}
	r := Range{Min: 1, Max: math.Nextafter(1, 2)}
	if lo, hi := FormatAmplitudeBounds(r); lo == hi || lo != "1" {
		t.Fatalf("distinct bounds collapsed: %q, %q", lo, hi)
	}
}

func TestAutomaticAmplitudeTicksFollowMagnitudePolicy(t *testing.T) {
	for _, tc := range []struct {
		r    Range
		step float64
		want string
	}{{Range{10000, 20001}, 10000.25, "10000"}, {Range{1000000, 2200000}, 1000000.25, "1e+06"}} {
		p := BuildTickPlan(tc.r, tc.step, TickOptions{Amplitude: true})
		if len(p.Ticks) < 2 || p.Ticks[0].Label != tc.want || p.Ticks[0].Value != tc.step {
			t.Fatalf("amplitude ticks: %+v", p)
		}
	}
	p := BuildTickPlan(Range{10000, 30000}, 10000.25, TickOptions{Amplitude: true, Format: FixedFormat, Digits: 2})
	if len(p.Ticks) < 2 || p.Ticks[0].Label != "10000.25" {
		t.Fatalf("explicit format: %+v", p)
	}
}

func TestTickPlanKeepsGridSeparateFromRangeEnds(t *testing.T) {
	p := BuildTickPlan(Range{184, 2456}, 500, TickOptions{})
	if p.Step != 500 || p.Warning != "" || !reflect.DeepEqual(p.Ticks, []Tick{{500, "500"}, {1000, "1000"}, {1500, "1500"}, {2000, "2000"}}) {
		t.Fatalf("regular grid: %+v", p)
	}
	p = BuildTickPlan(Range{0.1, 0.3}, .1, TickOptions{})
	if len(p.Ticks) != 3 || p.Ticks[2].Label != "0.3" {
		t.Fatalf("rounded endpoint on the grid: %+v", p)
	}
	p = BuildTickPlan(Range{0, 100}, .1, TickOptions{})
	if p.Step != .1 || p.Warning == "" {
		t.Fatalf("manual step silently changed: %+v", p)
	}
}

func TestEqualTickPlanUsesExactBoundsAndRequestedDivisions(t *testing.T) {
	for _, tc := range []struct {
		range_    Range
		n         int
		amplitude bool
		step      float64
		labels    []string
	}{
		{Range{800, 1500}, 4, false, 175, []string{"800", "975", "1150", "1325", "1500"}},
		{Range{-20586, 26684}, 5, true, 9454, []string{"-20586", "-11132", "-1678", "7776", "17230", "26684"}},
		{Range{40, 80}, 1, false, 40, []string{"40", "80"}},
	} {
		p := BuildEqualTickPlan(tc.range_, tc.n, TickOptions{Amplitude: tc.amplitude})
		if len(p.Ticks) != tc.n+1 || p.Step != tc.step || p.Warning != "" || p.Ticks[0].Value != tc.range_.Min || p.Ticks[tc.n].Value != tc.range_.Max {
			t.Fatalf("equal grid %+v", p)
		}
		for i, tick := range p.Ticks {
			wanted := tc.range_.Min + float64(i)*tc.step
			if math.Abs(tick.Value-wanted) > 1e-9 || tick.Label != tc.labels[i] {
				t.Fatalf("tick %d = %+v; want %v / %s", i, tick, wanted, tc.labels[i])
			}
		}
	}
	base := BuildEqualTickPlan(Range{800, 1500}, 0, TickOptions{})
	if len(base.Ticks) != DefaultTickDivisions+1 {
		t.Fatalf("default %+v", base)
	}
	if p := BuildEqualTickPlan(Range{0, 10}, 21, TickOptions{}); p.Warning == "" || len(p.Ticks) != 0 {
		t.Fatalf("bounds %+v", p)
	}
}

func TestEqualTickLabelsKeepGridPositionsAndReportCrowding(t *testing.T) {
	r := Range{800, 1500}
	base := BuildEqualTickPlan(r, 4, TickOptions{})
	s := BuildEqualTickPlan(r, 4, TickOptions{DisplayValue: func(ms float64) float64 { return ms / 1000 }})
	manual := BuildEqualTickPlan(r, 4, TickOptions{Format: ScientificFormat, Digits: 2, Pixels: 25, Measure: func(string) int { return 37 }})
	if s.Step != 175 || manual.Step != 175 || s.Ticks[1].Label != "0.975" || manual.Warning == "" || len(manual.Ticks) != 5 {
		t.Fatalf("base %+v seconds %+v manual %+v", base, s, manual)
	}
	for i, tick := range base.Ticks {
		if tick.Value != s.Ticks[i].Value || tick.Value != manual.Ticks[i].Value {
			t.Fatal("style or units moved tick")
		}
	}
	if p := BuildEqualTickPlan(Range{-0.1, 0.1}, 4, TickOptions{Format: IntegerFormat}); p.Warning == "" || len(p.Ticks) != 5 {
		t.Fatalf("manual duplicate %+v", p)
	}
	if p := BuildEqualTickPlan(Range{10000, 10000.2}, 2, TickOptions{Amplitude: true}); p.Warning != "" || p.Ticks[0].Label == p.Ticks[2].Label {
		t.Fatalf("automatic labels collapsed narrow scale %+v", p)
	}
}

func TestMeasuredTickDensityAndCanonicalTime(t *testing.T) {
	r := Range{400, 800}
	o := TickOptions{Pixels: 480, Gap: 8, Measure: func(string) int { return 30 }}
	base := BuildTickPlan(r, 0, o)
	o.DisplayValue = func(ms float64) float64 { return ms / 1000 }
	seconds := BuildTickPlan(r, 0, o)
	if base.Warning != "" || seconds.Warning != "" || base.Step <= 0 || base.Step != seconds.Step || len(base.Ticks) != len(seconds.Ticks) || base.Ticks[0].Value != seconds.Ticks[0].Value || seconds.Ticks[0].Label != "0.40" {
		t.Fatalf("ms %+v / s %+v", base, seconds)
	}
	o.Pixels = 100
	o.Measure = func(string) int { return 48 }
	narrow := BuildTickPlan(r, 0, o)
	if narrow.Step <= base.Step || narrow.Warning != "" {
		t.Fatalf("auto density did not respond: %+v", narrow)
	}
	manual := BuildTickPlan(r, base.Step, o)
	if manual.Step != base.Step || manual.Warning == "" {
		t.Fatalf("manual density did not report collision: %+v", manual)
	}
}

func TestTickFormatAndLengthCompatibility(t *testing.T) {
	if FixedFormat != 1 || ScientificFormat != 2 || IntegerFormat != 3 {
		t.Fatal("old preference values changed")
	}
	if FormatNumber(-.001, FixedFormat, 2) != "0.00" || FormatNumber(-.1, IntegerFormat, 0) != "0" || FormatNumber(-1230000, ScientificFormat, 2) != "-1.23e+06" {
		t.Fatal("unexpected numeric formats")
	}
	p := BuildTickPlan(Range{-.1, .1}, .1, TickOptions{Format: IntegerFormat})
	if p.Warning == "" {
		t.Fatal("repeated manual labels were accepted")
	}
	a := Axis{ID: "x"}
	if a.MajorTickDIP() != 4 || (ColorMapping{}).MajorTickDIP() != 6 {
		t.Fatal("incorrect legacy/new defaults")
	}
	a.TickLengthSet = true
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded Axis
	if err := json.Unmarshal(b, &reloaded); err != nil || reloaded.MajorTickDIP() != 0 {
		t.Fatalf("explicit zero did not round-trip: %s %v", b, err)
	}
	a.TickLength = math.Inf(1)
	if a.Valid() == nil {
		t.Fatal("non-finite length accepted")
	}
}

func TestDragRangeLimitsAndPreservesTranslationSpan(t *testing.T) {
	bounds, view := Range{-12, 15}, Range{-5, 4}
	for _, tc := range []struct {
		mode  RangeDragMode
		delta float64
		want  Range
	}{{DragWhole, -100, Range{-12, -3}}, {DragWhole, 100, Range{6, 15}}, {DragWhole, 1, Range{-4, 5}}, {DragMinimum, -100, Range{-12, 4}}, {DragMaximum, 100, Range{-5, 15}}} {
		if got := DragRange(bounds, view, tc.mode, tc.delta); got != tc.want {
			t.Fatalf("range drag %+v -> %v", tc, got)
		}
	}
	if got := DragRange(bounds, view, DragMinimum, 100); !got.Valid() || got.Max != view.Max {
		t.Fatalf("endpoints crossed: %v", got)
	}
}
