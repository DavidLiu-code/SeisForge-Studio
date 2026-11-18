package display

import "math"

// ScientificAxisTicks makes a zero-anchored grid in the displayed unit.
// Converting the step back to the base unit changes real positions rather
// than rounding labels of unrelated, equally spaced endpoints.
func ScientificAxisTicks(a Axis, divisions int, o TickOptions) TickPlan {
	r := plottedUnits(a)
	step := scientificStep(r, divisions, a.Format == IntegerFormat)
	o.Format, o.Digits, o.DisplayValue, o.Position = a.Format, a.Digits, a.DisplayValue, a.Fraction
	o.MaxTicks = 64
	build := func(step float64) TickPlan {
		opt := o
		if a.Format == AutoFormat && step > 0 && finite(step) {
			digits := max(0, int(-math.Floor(math.Log10(step))))
			if digits < 13 {
				f := step * math.Pow10(digits)
				if math.Abs(f-math.Round(f)) > math.Abs(f)*1e-12 {
					digits++
				}
			}
			magnitude := max(math.Abs(r.Min), math.Abs(r.Max))
			if digits > 12 || (magnitude > 0 && magnitude < 1e-4) || magnitude >= 1e6 {
				opt.Format = ScientificFormat
				opt.Digits = max(0, min(12, int(math.Ceil(math.Log10(magnitude)-math.Log10(step)))))
			} else {
				opt.Format, opt.Digits = FixedFormat, digits
			}
		}
		p := BuildTickPlan(a.View, a.BaseValue(step), opt)
		if a.Format == AutoFormat && opt.Format == ScientificFormat {
			for i := range p.Ticks {
				if p.Ticks[i].Value == 0 {
					p.Ticks[i].Label = "0"
				}
			}
		}
		return p
	}
	p := build(step)
	for p.LayoutWarning != "" && step > 0 && o.Pixels > 0 {
		next := nearestScientificStep(step * 1.65)
		if !finite(next) || next <= step {
			break
		}
		candidate := build(next)
		if len(candidate.Ticks) < 2 {
			break
		}
		p, step = candidate, next
	}
	return p
}

// ExpandScientificAxis only expands display geometry, never data limits,
// analysis samples or manually committed view ranges.
func ExpandScientificAxis(a Axis, divisions int) Axis {
	r := plottedUnits(a)
	if !r.Valid() {
		return a
	}
	source := a.View
	step := scientificStep(r, divisions, a.Format == IntegerFormat)
	for i := 0; i < 3 && step > 0 && finite(step); i++ {
		lo := a.BaseValue(math.Floor(math.Nextafter(r.Min/step, math.Inf(1))) * step)
		hi := a.BaseValue(math.Ceil(math.Nextafter(r.Max/step, math.Inf(-1))) * step)
		n := Range{Min: min(lo, source.Min), Max: max(hi, source.Max)}
		if !n.Valid() || !finite(n.Max-n.Min) {
			break
		}
		a.View = n
		next := scientificStep(plottedUnits(a), divisions, a.Format == IntegerFormat)
		if next == step {
			break
		}
		step = next
	}
	return a
}

func plottedUnits(a Axis) Range {
	return Range{Min: a.DisplayValue(a.View.Min), Max: a.DisplayValue(a.View.Max)}
}

func scientificStep(r Range, divisions int, integer bool) float64 {
	if !r.Valid() {
		return 0
	}
	span := r.Max - r.Min
	if !finite(span) {
		return 0
	}
	s := nearestScientificStep(span / float64(max(1, divisions)))
	if integer {
		return max(1, s)
	}
	return s
}

func nearestScientificStep(v float64) float64 {
	if v <= 0 || !finite(v) {
		return v
	}
	base := math.Pow10(int(math.Floor(math.Log10(v))))
	if base <= 0 || !finite(base) {
		return v
	}
	best, score := v, math.Inf(1)
	for _, factor := range [...]float64{1, 2, 2.5, 5, 10} {
		x := base * factor
		if x <= 0 || !finite(x) {
			continue
		}
		if d := math.Abs(math.Log(x / v)); d < score {
			best, score = x, d
		}
	}
	return best
}
