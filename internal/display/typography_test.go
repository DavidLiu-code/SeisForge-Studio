package display

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDisplayNumberFormatMissingDiffersFromSavedAuto(t *testing.T) {
	var s Settings
	err := json.Unmarshal([]byte(`{"Axes":[{"ID":"missing"},{"ID":"auto","Format":0},{"ID":"fixed","Format":1,"Digits":4}],"Color":{}}`), &s)
	if err != nil || s.Axes[0].Format != IntegerFormat || s.Color.Format != IntegerFormat || s.Axes[1].Format != AutoFormat ||
		s.Axes[2].Format != FixedFormat || s.Axes[2].Digits != 4 {
		t.Fatalf("missing and explicitly saved formats decoded alike: %+v, %v", s, err)
	}
	if err := json.Unmarshal([]byte(`{"Color":{"Format":0}}`), &s); err != nil || s.Color.Format != AutoFormat {
		t.Fatalf("explicit saved colorbar Auto: %+v, %v", s, err)
	}
}

func TestIntegerAmplitudeLabelsKeepEqualNumericPositionsAndSeparateDiagnostics(t *testing.T) {
	r := Range{184, 2456}
	p := BuildEqualTickPlan(r, 5, TickOptions{Format: DefaultNumberFormat})
	labels := []string{"184", "638", "1093", "1547", "2002", "2456"}
	if p.Step != 454.4 || len(p.Ticks) != 6 || p.Warning != "" {
		t.Fatalf("equal plan %+v", p)
	}
	for i, want := range labels {
		where := r.At(float64(i) / 5)
		if p.Ticks[i].Value != where || p.Ticks[i].Label != want {
			t.Fatalf("tick %d %+v; want %v, %s", i, p.Ticks[i], where, want)
		}
	}
	p = BuildEqualTickPlan(r, 8, TickOptions{Format: DefaultNumberFormat})
	if p.Step != 284 || len(p.Ticks) != 9 || !ValidEnteredFontPT(5) || ValidEnteredFontPT(4) || !ValidFontPT(0) || ValidEnteredFontPT(0) {
		t.Fatalf("8-tick or font-bound configuration rejected: %+v", p)
	}
	p = BuildEqualTickPlan(Range{-0.2, 0.2}, 5, TickOptions{Format: IntegerFormat, Pixels: 20, Measure: func(string) int { return 30 }})
	if !strings.Contains(p.FormatWarning, "整数精度不足") || p.LayoutWarning != "" || p.ValueError != "" || p.Ticks[0].Label != "0" {
		t.Fatalf("integer precision was labeled as a crowding or validation error: %+v", p)
	}
	p = BuildEqualTickPlan(r, 5, TickOptions{Format: IntegerFormat, Pixels: 12, Measure: func(string) int { return 30 }})
	if p.LayoutWarning == "" || p.FormatWarning != "" || p.ValueError != "" || len(p.Ticks) != 6 {
		t.Fatalf("crowding changed real ticks or format: %+v", p)
	}
}

func TestEqualIntegerCoordinateTicksUseRealRoundedPositions(t *testing.T) {
	axis := Axis{ID: "crossline", View: Range{184, 2456}, Divisions: 5, Format: IntegerFormat}
	plan := axis.EqualCoordinateTicks(TickOptions{})
	want := []Tick{{184, "184"}, {638, "638"}, {1093, "1093"}, {1547, "1547"}, {2002, "2002"}, {2456, "2456"}}
	if plan.Step != 454.4 || plan.IntegerMinStep != 454 || plan.IntegerMaxStep != 455 || plan.Warning != "" || !plan.IntegerPositions || len(plan.Ticks) != len(want) {
		t.Fatalf("real positions/intervals: %+v", plan)
	}
	for i, tick := range want {
		if plan.Ticks[i] != tick {
			t.Fatalf("tick %d = %+v; want %+v", i, plan.Ticks[i], tick)
		}
	}
	if x, fractional := axis.Pixel(plan.Ticks[1].Value, 0, 22720), axis.Pixel(axis.View.At(.2), 0, 22720); x != 4540 || x == fractional {
		t.Fatalf("tick pixel used nominal coordinate: %d, %d", x, fractional)
	}
	tests := []struct {
		r      Range
		n      int
		coords []float64
	}{
		{Range{0, 3}, 5, []float64{0, 1, 2, 3}},
		{Range{.2, 3.7}, 5, []float64{1, 2, 3}},
		{Range{-3, 3}, 4, []float64{-3, -2, 0, 2, 3}},
		{Range{.15, .25}, 5, nil},
	}
	for _, tc := range tests {
		axis.View, axis.Divisions = tc.r, tc.n
		p := axis.EqualCoordinateTicks(TickOptions{})
		if len(p.Ticks) != len(tc.coords) || p.ValueError != "" || (len(tc.coords) == 0) != (p.FormatWarning != "") || axis.View != tc.r {
			t.Fatalf("exact view %v, division %v generated %+v", tc.r, tc.n, p)
		}
		for i, v := range tc.coords {
			if p.Ticks[i].Value != v {
				t.Fatalf("trace coordinate %d %v != %v", i, p.Ticks[i].Value, v)
			}
		}
	}
}

func TestEqualCoordinateTicksUseDisplayUnitAndSupportExactSingleTrace(t *testing.T) {
	a := Axis{ID: "distance", View: Range{200, 3700}, Unit: "km", BaseUnit: "m", Divisions: 5, Format: IntegerFormat}
	p := a.EqualCoordinateTicks(TickOptions{})
	if len(p.Ticks) != 3 || p.Ticks[0] != (Tick{1000, "1"}) || p.Ticks[2] != (Tick{3000, "3"}) || p.IntegerMinStep != 1 {
		t.Fatalf("km ticks did not map to actual metre coordinates: %+v", p)
	}
	a.View, a.AllowsPoint, a.ViewEdited = Range{1250, 1250}, true, true
	p = a.EqualCoordinateTicks(TickOptions{})
	if a.Valid() != nil || len(p.Ticks) != 1 || p.Ticks[0] != (Tick{1250, "1.25"}) || !p.SinglePoint || p.Warning != "" || a.Pixel(p.Ticks[0].Value, 0, 200) != 100 {
		t.Fatalf("single fractional trace lost its true position: %+v (%v)", p, a.Valid())
	}
	a.Divisions = 21
	if p = a.EqualCoordinateTicks(TickOptions{}); !strings.Contains(p.Warning, "1–20") || len(p.Ticks) != 0 {
		t.Fatalf("invalid division on a point was not diagnosed: %+v", p)
	}
}
