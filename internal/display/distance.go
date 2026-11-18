package display

import (
	"math"
	"sort"
)

const DefaultDistanceSpacingM = 20

// DistanceReference relates original trace centres to metres measured from the
// first centre of the complete reference line. Cropping never creates a new
// reference. A positive grid step identifies physical holes in the lattice.
type DistanceReference struct {
	native, metres []float64
	min, max, step float64
	spacing        float64
}

// RegularDistance uses a confirmed native grid index, not its display ordinal.
// The ordinary physical trace sequence uses native indices 1..TraceCount.
func RegularDistance(minimum, maximum, step, spacingM float64) *DistanceReference {
	if !(Range{minimum, maximum}).Valid() || !finite(step) || step <= 0 || !finite(spacingM) || spacingM <= 0 {
		return nil
	}
	if n := (maximum - minimum) / step; !finite(n*spacingM) || n < 1 {
		return nil
	}
	return &DistanceReference{min: minimum, max: maximum, step: step, spacing: spacingM}
}

// IrregularDistance accepts coordinates measured in metres, in increasing
// native order. Duplicate centres and non-finite input are not usable axes.
func IrregularDistance(native, metres []float64) *DistanceReference {
	if len(native) < 2 || len(native) != len(metres) || metres[0] != 0 {
		return nil
	}
	for i := range native {
		if !finite(native[i]) || !finite(metres[i]) || i > 0 && (native[i] <= native[i-1] || metres[i] <= metres[i-1]) {
			return nil
		}
	}
	return &DistanceReference{native: append([]float64(nil), native...), metres: append([]float64(nil), metres...), min: native[0], max: native[len(native)-1]}
}

// RankDistance assumes a fixed spacing along the original complete trace
// order without claiming that native identifiers reveal missing grid slots.
func RankDistance(native []float64, spacingM float64) *DistanceReference {
	if !finite(spacingM) || spacingM <= 0 {
		return nil
	}
	metres := make([]float64, len(native))
	for i := range metres {
		metres[i] = float64(i) * spacingM
	}
	d := IrregularDistance(native, metres)
	if d != nil {
		d.spacing = spacingM
	}
	return d
}

func distanceInterpolate(x, y []float64, v float64) float64 {
	i := sort.Search(len(x), func(i int) bool { return x[i] >= v })
	i = min(max(i, 1), len(x)-1)
	return y[i-1] + (v-x[i-1])/(x[i]-x[i-1])*(y[i]-y[i-1])
}

func (d *DistanceReference) Metres(native float64) (float64, bool) {
	if d == nil || !finite(native) || native < d.min || native > d.max {
		return 0, false
	}
	if d.step > 0 {
		return (native - d.min) / d.step * d.spacing, true
	}
	return distanceInterpolate(d.native, d.metres, native), true
}

func (d *DistanceReference) Native(metres float64) (float64, bool) {
	if d == nil || !finite(metres) || metres < 0 {
		return 0, false
	}
	hi, _ := d.Metres(d.max)
	if metres > hi {
		return 0, false
	}
	if metres == hi {
		return d.max, true
	}
	if d.step > 0 {
		return d.min + metres/d.spacing*d.step, true
	}
	return distanceInterpolate(d.metres, d.native, metres), true
}

// KnownGridStep returns a step supported by the present values. When known
// deltas are whole multiples of the smallest observed delta, larger observed
// deltas represent absent slots; arbitrary identifiers do not prove a grid.
func KnownGridStep(ids []int32) float64 {
	if len(ids) < 2 {
		return 0
	}
	step := int64(ids[1]) - int64(ids[0])
	for i := 2; i < len(ids); i++ {
		step = min(step, int64(ids[i])-int64(ids[i-1]))
	}
	if step <= 0 {
		return 0
	}
	for i := 1; i < len(ids); i++ {
		if (int64(ids[i])-int64(ids[i-1]))%step != 0 {
			return 0
		}
	}
	return float64(step)
}

// GridSpacingM also reports an explicitly assumed rank spacing; it is zero
// only for continuously measured irregular geometry.
func (d *DistanceReference) GridSpacingM() float64 {
	if d == nil {
		return 0
	}
	return d.spacing
}

func DistanceUnit(viewM Range) string {
	if math.Max(math.Abs(viewM.Min), math.Abs(viewM.Max)) > 1000 {
		return "km"
	}
	return "m"
}
