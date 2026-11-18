package display

import (
	"slices"
	"testing"
)

func TestScientificTicksUseDisplayedUnitAndRealGrid(t *testing.T) {
	a := Axis{ID: "time", Unit: "ms", BaseUnit: "s", View: Range{Min: .8, Max: 1.5}}
	p := ScientificAxisTicks(a, 7, TickOptions{})
	want := []string{"800", "900", "1000", "1100", "1200", "1300", "1400", "1500"}
	labels := make([]string, 0, len(p.Ticks))
	for _, tick := range p.Ticks {
		labels = append(labels, tick.Label)
	}
	if !slices.Equal(labels, want) || a.DisplayValue(p.Step) != 100 {
		t.Fatalf("ms plans: %#v, labels %v", p, labels)
	}
	a.Unit = "s"
	q := ScientificAxisTicks(a, 7, TickOptions{})
	if q.Ticks[0].Label != "0.8" || q.Ticks[len(q.Ticks)-1].Label != "1.5" || q.Ticks[2].Value != p.Ticks[2].Value {
		t.Fatalf("seconds changed data positions: %#v, ms %#v", q, p)
	}
	frequency := Axis{ID: "frequency", View: Range{Min: 0, Max: 125}}
	f := ScientificAxisTicks(frequency, 5, TickOptions{})
	if len(f.Ticks) != 6 || f.Step != 25 || f.Ticks[5].Label != "125" {
		t.Fatalf("frequency locator: %#v", f)
	}
}

func TestScientificLimitsCoverFiniteDBWithARealZero(t *testing.T) {
	a := Axis{ID: "level", Unit: "dB", View: Range{Min: -142.98, Max: 6.02}, Data: Range{Min: -142.98, Max: 6.02}}
	b := ExpandScientificAxis(a, 7)
	p := ScientificAxisTicks(b, 7, TickOptions{})
	if b.Data != a.Data || b.View.Min > a.View.Min || b.View.Max < a.View.Max || p.Step != 25 ||
		!slices.Contains(p.Ticks, Tick{Value: 0, Label: "0"}) || p.Ticks[len(p.Ticks)-1].Value <= 6.02 {
		t.Fatalf("dB limits %#v; plan %#v", b, p)
	}
	tiny := Axis{ID: "amplitude", View: Range{Min: -3e-12, Max: 3e-12}}
	q := ScientificAxisTicks(tiny, 6, TickOptions{Amplitude: true})
	if len(q.Ticks) != 7 || q.Ticks[2].Label == "0" || q.Ticks[4].Label == "0" {
		t.Fatalf("tiny nonzero ticks: %#v", q)
	}
}
