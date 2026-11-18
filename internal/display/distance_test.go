package display

import "testing"

func TestDistanceCentresGridVoidsAndUnits(t *testing.T) {
	d := RegularDistance(1, 6, 1, 20)
	for i, want := range []float64{0, 20, 40, 60, 80, 100} {
		actual, ok := d.Metres(float64(i + 1))
		if !ok || actual != want {
			t.Fatalf("centre %d = %v, %v; want %v", i+1, actual, ok, want)
		}
	}
	ids := []int32{101, 103, 107}
	g := RegularDistance(101, 107, KnownGridStep(ids), 20)
	for i, want := range []float64{0, 20, 60} {
		n := float64(ids[i])
		actual, ok := g.Metres(n)
		back, inverseOK := g.Native(actual)
		if !ok || actual != want || !inverseOK || back != n {
			t.Fatalf("%v -> %v -> %v", n, actual, back)
		}
	}
	if KnownGridStep([]int32{11, 13, 16}) != 0 || DistanceUnit(Range{0, 1000}) != "m" || DistanceUnit(Range{0, 1000.0001}) != "km" {
		t.Fatal("irregular IDs or m/km threshold changed")
	}
}

func TestIrregularDistanceInverse(t *testing.T) {
	ids, m := []float64{10, 12, 20}, []float64{0, 17, 61}
	d := IrregularDistance(ids, m)
	ids[0], m[1] = 0, 99
	got, ok := d.Metres(16)
	back, ok2 := d.Native(39)
	if !ok || !ok2 || got != 39 || back != 16 || d.GridSpacingM() != 0 {
		t.Fatalf("interpolation %v, inverse %v", got, back)
	}
	if IrregularDistance([]float64{1, 2}, []float64{0, 0}) != nil {
		t.Fatal("duplicate centres cannot support inverse coordinates")
	}
}
