package display

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// TickOptions describes the actual drawing space. Position returns a fraction
// of that space; it can, for example, place irregular Crosslines by trace rank.
// Measure returns the size of a label along the axis, in the same pixels.
type TickOptions struct {
	Pixels, Gap, MaxTicks  int
	Measure                func(string) int
	Position, DisplayValue func(float64) float64
	Format                 NumberFormat
	Digits                 int
	Amplitude              bool // use amplitude magnitude conventions in automatic format only
}

type Tick struct {
	Value float64
	Label string
}

type TickPlan struct {
	Step                           float64 // regular interval (target interval for integer axes), in the canonical data unit
	Ticks                          []Tick
	Warning                        string // first diagnostic, for legacy consumers
	ValueError                     string
	FormatWarning                  string
	LayoutWarning                  string
	Digits                         int // effective automatic precision or requested manual precision
	IntegerPositions               bool
	SinglePoint                    bool    // actual single-coordinate axis, not deduplicated integer ticks
	IntegerMinStep, IntegerMaxStep float64 // actual intervals, in displayed units
}

func (p *TickPlan) finishDiagnostics() {
	p.Warning = p.ValueError
	if p.Warning == "" {
		p.Warning = p.FormatWarning
	}
	if p.Warning == "" {
		p.Warning = p.LayoutWarning
	}
}

const (
	DefaultTickDivisions = 5
	MinTickDivisions     = 1
	MaxTickDivisions     = 20
	tickDensityWarning   = "刻度间隔过密或超出当前绘图区；请增大间隔或窗口。"
	tickFormatWarning    = "当前数字格式使不同刻度显示相同；请提高精度或切换格式。"
	tickZeroWarning      = "当前数字格式将非零刻度显示为 0；请提高精度或切换格式。"
	tickIntegerWarning   = "整数精度不足，可选择自动或小数格式。"
	tickNoInteger        = "当前范围内没有可用的整数主刻度；可选择自动或小数格式。"
)

// BuildEqualTickPlan generates endpoints and N subdivisions of the current
// view; unlike the legacy zero-anchored grid, it never inserts other ticks.
// Layout diagnostics do not change the requested division count.
func BuildEqualTickPlan(r Range, divisions int, o TickOptions) TickPlan {
	if divisions == 0 {
		divisions = DefaultTickDivisions
	}
	if divisions < MinTickDivisions || divisions > MaxTickDivisions {
		p := TickPlan{ValueError: "等分数须为 1–20。"}
		p.finishDiagnostics()
		return p
	}
	if !r.Valid() {
		p := TickPlan{ValueError: "没有可用的刻度范围。"}
		p.finishDiagnostics()
		return p
	}
	p := TickPlan{Step: (r.Max - r.Min) / float64(divisions), Ticks: make([]Tick, divisions+1)}
	if !finite(p.Step) {
		p.Step = r.Max/float64(divisions) - r.Min/float64(divisions)
	}
	for i := range p.Ticks {
		p.Ticks[i].Value = r.At(float64(i) / float64(divisions))
		if i > 0 && p.Ticks[i].Value <= p.Ticks[i-1].Value {
			p.ValueError = "当前范围与等分数无法生成可区分的刻度；请减少等分数。"
		}
	}
	if o.DisplayValue == nil {
		o.DisplayValue = func(v float64) float64 { return v }
	}
	if o.Position == nil {
		o.Position = r.Fraction
	}
	return formatAndMeasureTicks(p, o, true)
}

// EqualCoordinateTicks is the seismic coordinate (not amplitude) contract.
// Candidates are rounded in the selected display unit, then converted back to
// real base coordinates for every consumer, including the pixel transform.
// Half values round away from zero, consistently for both signs.
func (a Axis) EqualCoordinateTicks(o TickOptions) TickPlan {
	o.Format, o.Digits, o.DisplayValue = a.Format, a.Digits, a.DisplayValue
	if o.Position == nil {
		o.Position = a.Fraction
	}
	if a.Divisions != 0 && (a.Divisions < MinTickDivisions || a.Divisions > MaxTickDivisions) {
		p := TickPlan{ValueError: "等分数须为 1–20。"}
		p.finishDiagnostics()
		return p
	}
	if a.AllowsPoint && a.View.IsPoint() {
		v := a.DisplayValue(a.View.Min)
		// A single real trace must never be moved to an unrelated integer.
		// If formatting would change its identity, show the precise coordinate.
		label := FormatNumber(v, a.Format, a.Digits)
		if labelValue, err := strconv.ParseFloat(label, 64); err != nil || labelValue != v {
			label = FormatExactNumber(v)
		}
		return TickPlan{Ticks: []Tick{{Value: a.View.Min, Label: label}}, SinglePoint: true}
	}
	if a.Format != IntegerFormat || !a.View.Valid() {
		return BuildEqualTickPlan(a.View, a.Divisions, o)
	}
	n := a.Divisions
	if n == 0 {
		n = DefaultTickDivisions
	}
	base := a.View
	shown := Range{Min: a.DisplayValue(base.Min), Max: a.DisplayValue(base.Max)}
	p := TickPlan{Step: (base.Max - base.Min) / float64(n), IntegerPositions: true}
	if !finite(p.Step) {
		p.Step = base.Max/float64(n) - base.Min/float64(n)
	}
	if !shown.Valid() {
		p.ValueError = "没有可用的刻度范围。"
		p.finishDiagnostics()
		return p
	}
	previous := math.Inf(-1)
	for i := 0; i <= n; i++ {
		v := math.Round(shown.At(float64(i) / float64(n)))
		if v <= previous || v < shown.Min || v > shown.Max {
			continue
		}
		coord := a.BaseValue(v)
		// Preserve exact base bounds when conversion makes a last-bit error.
		if v == shown.Min {
			coord = base.Min
		} else if v == shown.Max {
			coord = base.Max
		}
		if !finite(coord) || coord < base.Min || coord > base.Max || (len(p.Ticks) > 0 && coord <= p.Ticks[len(p.Ticks)-1].Value) {
			continue
		}
		if len(p.Ticks) > 0 {
			step := v - previous
			if len(p.Ticks) == 1 {
				p.IntegerMinStep, p.IntegerMaxStep = step, step
			} else {
				p.IntegerMinStep, p.IntegerMaxStep = min(p.IntegerMinStep, step), max(p.IntegerMaxStep, step)
			}
		}
		p.Ticks = append(p.Ticks, Tick{Value: coord})
		previous = v
	}
	if len(p.Ticks) == 0 {
		p.FormatWarning = tickNoInteger
		p.finishDiagnostics()
		return p
	}
	return formatAndMeasureTicks(p, o, true)
}

// BuildTickPlan never injects view bounds into the zero-anchored main grid.
// Positive manual steps are used verbatim, even if they cannot be displayed.
func BuildTickPlan(r Range, step float64, o TickOptions) TickPlan {
	if !r.Valid() || !finite(step) || step < 0 {
		p := TickPlan{ValueError: "刻度范围或间隔无效。"}
		p.finishDiagnostics()
		return p
	}
	limit := o.MaxTicks
	if limit < 2 {
		limit = 64
	}
	limit = min(64, limit)
	if o.DisplayValue == nil {
		o.DisplayValue = func(v float64) float64 { return v }
	}
	if o.Position == nil {
		o.Position = r.Fraction
	}
	if step > 0 {
		return makeTickPlan(r, step, limit, o)
	}
	// Start fairly dense and increase only when the measured labels collide.
	count := min(limit, 9)
	if o.Pixels > 0 {
		count = min(limit, max(2, min(12, o.Pixels/28+1)))
	}
	span := r.Max - r.Min
	desired := span / float64(max(1, count-1))
	if !finite(desired) {
		desired = r.Max/float64(count) - r.Min/float64(count)
	}
	if desired <= 0 {
		desired = math.SmallestNonzeroFloat64
	}
	step = niceTickStep(desired)
	var best TickPlan
	for attempt := 0; attempt < 32; attempt++ {
		p := makeTickPlan(r, step, limit, o)
		if len(p.Ticks) > 0 || best.Step == 0 {
			best = p
		}
		if p.Warning == "" && len(p.Ticks) > 0 {
			return p
		}
		if p.Warning == "" && len(p.Ticks) == 0 {
			break
		}
		next := niceTickStep(step * 1.01)
		if !finite(next) || next <= step {
			break
		}
		step = next
	}
	if best.Step == 0 {
		best.Step = step
	}
	return best
}

func niceTickStep(v float64) float64 {
	if !finite(v) {
		return math.MaxFloat64
	}
	base := math.Pow10(int(math.Floor(math.Log10(v))))
	if base == 0 {
		return v
	}
	for _, f := range [...]float64{1, 2, 5, 10} {
		candidate := f * base
		if finite(candidate) && candidate >= v*(1-1e-14) {
			return candidate
		}
	}
	return v
}

func makeTickPlan(r Range, step float64, limit int, o TickOptions) TickPlan {
	p := TickPlan{Step: step}
	left, right := r.Min/step, r.Max/step
	if !finite(left) || !finite(right) || right-left > float64(limit) || math.Abs(left) > 1<<53 || math.Abs(right) > 1<<53 {
		p.LayoutWarning = tickDensityWarning
		p.finishDiagnostics()
		return p
	}
	// Nudge only the quotient by one ULP, so e.g. the 0.3 tick survives with
	// a 0.1 step without treating every view bound as a grid point.
	first, last := math.Ceil(math.Nextafter(left, math.Inf(-1))), math.Floor(math.Nextafter(right, math.Inf(1)))
	if last-first >= float64(limit) {
		p.LayoutWarning = tickDensityWarning
		p.finishDiagnostics()
		return p
	}
	for i := 0; i < limit && first+float64(i) <= last; i++ {
		v := (first + float64(i)) * step
		if !finite(v) {
			continue
		}
		if v < r.Min {
			if v < math.Nextafter(r.Min, math.Inf(-1)) {
				continue
			}
			v = r.Min
		}
		if v > r.Max {
			if v > math.Nextafter(r.Max, math.Inf(1)) {
				continue
			}
			v = r.Max
		}
		if len(p.Ticks) > 0 && v <= p.Ticks[len(p.Ticks)-1].Value {
			p.Ticks = nil
			p.LayoutWarning = tickDensityWarning
			p.finishDiagnostics()
			return p
		}
		p.Ticks = append(p.Ticks, Tick{Value: v})
	}
	if len(p.Ticks) == 0 {
		return p
	}
	return formatAndMeasureTicks(p, o, false)
}

func formatAndMeasureTicks(p TickPlan, o TickOptions, equal bool) (result TickPlan) {
	defer func() { result.finishDiagnostics() }()
	shownStep := math.Abs(o.DisplayValue(p.Step) - o.DisplayValue(0))
	if equal && len(p.Ticks) >= 2 {
		last := len(p.Ticks) - 1
		displayedSpan := o.DisplayValue(p.Ticks[last].Value) - o.DisplayValue(p.Ticks[0].Value)
		if finite(displayedSpan) && displayedSpan != 0 {
			shownStep = math.Abs(displayedSpan / float64(last))
		}
	}
	labels := make(map[string]bool, len(p.Ticks))
	precision := automaticTickPrecision(p.Ticks, o.DisplayValue, shownStep)
	equalExtra := 0
	for {
		clear(labels)
		duplicate := false
		roundedZero := false
		for i := range p.Ticks {
			v := o.DisplayValue(p.Ticks[i].Value)
			label := FormatNumber(v, o.Format, max(0, min(12, o.Digits)))
			if o.Format == AutoFormat {
				if equal {
					label = formatEqualTickValue(v, o.Amplitude, equalExtra)
				} else if o.Amplitude && math.Abs(v) >= amplitudeIntegerAt {
					label = formatAmplitude(v, max(4, precision))
				} else {
					label = automaticTickLabel(v, precision)
				}
				if v != 0 && isTickLabelZero(label) {
					label = FormatCompactNumber(v)
				}
			} else if v != 0 && finite(v) && isTickLabelZero(label) {
				roundedZero = true
			}
			duplicate = duplicate || labels[label]
			labels[label] = true
			p.Ticks[i].Label = label
		}
		p.Digits = max(0, min(12, o.Digits))
		if o.Format == AutoFormat {
			p.Digits = precision
			if equal {
				p.Digits = min(17, 6+equalExtra)
			}
		}
		if !duplicate {
			if roundedZero {
				p.FormatWarning = tickZeroWarning
				if o.Format == IntegerFormat {
					p.FormatWarning = tickIntegerWarning
				}
			}
			break
		}
		if o.Format != AutoFormat || !equal && precision >= 17 || equal && equalExtra >= 16 {
			p.FormatWarning = tickFormatWarning
			if o.Format == IntegerFormat {
				p.FormatWarning = tickIntegerWarning
			}
			return p
		}
		precision++
		equalExtra++
	}
	if o.Pixels <= 0 || o.Measure == nil {
		return p
	}
	type extent struct{ lo, hi float64 }
	boxes := make([]extent, 0, len(p.Ticks))
	for _, t := range p.Ticks {
		f := o.Position(t.Value)
		if !finite(f) {
			continue
		}
		half := float64(max(0, o.Measure(t.Label))) / 2
		if half == 0 {
			continue
		}
		center := f * float64(o.Pixels)
		boxes = append(boxes, extent{center - half, center + half})
	}
	sort.Slice(boxes, func(i, j int) bool { return boxes[i].lo < boxes[j].lo })
	for i := 1; i < len(boxes); i++ {
		if boxes[i].lo < boxes[i-1].hi+float64(max(0, o.Gap)) {
			p.LayoutWarning = tickDensityWarning
			if equal {
				p.LayoutWarning = "主图刻度标签可能重叠；可减少等分数、减小字号或放大窗口。"
			}
			break
		}
	}
	return p
}

func isTickLabelZero(s string) bool {
	v, err := strconv.ParseFloat(s, 64)
	return err == nil && v == 0
}

func formatEqualTickValue(v float64, amplitude bool, extra int) string {
	if !finite(v) {
		return "—"
	}
	if v == 0 {
		return "0"
	}
	m := math.Abs(v)
	if amplitude && extra == 0 && m >= amplitudeIntegerAt {
		return formatAmplitude(v, 6)
	}
	if amplitude && m >= amplitudeScientificAt {
		s := strconv.FormatFloat(v, 'e', min(16, 3+extra), 64)
		for i, c := range s {
			if c == 'e' {
				return trimEqualFixed(s[:i]) + s[i:]
			}
		}
		return s
	}
	return formatSignificantNumber(v, min(17, 6+extra))
}

func trimEqualFixed(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '0' {
			continue
		}
		if s[i] == '.' {
			return s[:i]
		}
		return s[:i+1]
	}
	return "0"
}

// TickLabelSummary is a text alternative for a preview with limited width.
// Every skipped interior run is explicitly represented by an ellipsis.
func TickLabelSummary(ticks []Tick) string {
	var labels []string
	if len(ticks) <= 5 {
		for _, t := range ticks {
			labels = append(labels, t.Label)
		}
	} else {
		labels = []string{ticks[0].Label, "…", ticks[len(ticks)/2].Label, "…", ticks[len(ticks)-1].Label}
	}
	return strings.Join(labels, " · ")
}

func automaticTickPrecision(ticks []Tick, shown func(float64) float64, step float64) int {
	m := 0.0
	for _, t := range ticks {
		m = math.Max(m, math.Abs(shown(t.Value)))
	}
	if !(step > 0) || !finite(step) || m == 0 {
		return 6
	}
	// Keep enough significant digits for the step, including a digit when a
	// range is far from zero, without printing binary rounding noise.
	return max(1, min(17, int(math.Floor(math.Log10(m))-math.Floor(math.Log10(step)))+1))
}

func automaticTickLabel(v float64, precision int) string {
	if v == 0 {
		return "0"
	}
	if !finite(v) {
		return "—"
	}
	m := math.Abs(v)
	if m >= 1e-4 && m < 1e7 {
		digits := max(0, precision-1-int(math.Floor(math.Log10(m))))
		return unsignedRoundedZero(strconv.FormatFloat(v, 'f', digits, 64))
	}
	return strconv.FormatFloat(v, 'g', precision, 64)
}

// FormatAmplitude is for compact, read-only amplitude labels. Precision in
// editable limits and in the actual color mapping is independent of this text.
func FormatAmplitude(v float64) string {
	return formatAmplitude(v, 6)
}

const (
	amplitudeIntegerAt    = 1e4
	amplitudeScientificAt = 1e6
)

func formatAmplitude(v float64, digits int) string {
	if !finite(v) {
		return "—"
	}
	if v == 0 {
		return "0"
	}
	switch n := math.Abs(v); {
	case n >= amplitudeScientificAt:
		return strconv.FormatFloat(v, 'g', min(4, digits), 64)
	case n >= amplitudeIntegerAt:
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'g', digits, 64)
}

// FormatAmplitudeBounds increases precision only below the integer threshold
// to distinguish close small bounds. Large-range annotations deliberately
// keep the integer/scientific policy; editing fields retain exact precision.
func FormatAmplitudeBounds(r Range) (lo, hi string) {
	lo, hi = FormatAmplitude(r.Min), FormatAmplitude(r.Max)
	for digits := 7; r.Valid() && lo == hi && math.Max(math.Abs(r.Min), math.Abs(r.Max)) < amplitudeIntegerAt && digits <= 17; digits++ {
		lo, hi = formatAmplitude(r.Min, digits), formatAmplitude(r.Max, digits)
	}
	return lo, hi
}

type RangeDragMode uint8

const (
	DragMinimum RangeDragMode = iota
	DragMaximum
	DragWhole
)

// DragRange returns the nearest allowed result. A whole-range translation
// clamps the movement, not the endpoints independently.
func DragRange(bounds, view Range, mode RangeDragMode, delta float64) Range {
	if !bounds.Valid() || !view.Valid() || view.Min < bounds.Min || view.Max > bounds.Max || !finite(delta) || delta == 0 {
		return view
	}
	result := view
	switch mode {
	case DragMinimum:
		result.Min = math.Max(bounds.Min, math.Min(math.Nextafter(view.Max, math.Inf(-1)), view.Min+delta))
	case DragMaximum:
		result.Max = math.Min(bounds.Max, math.Max(math.Nextafter(view.Min, math.Inf(1)), view.Max+delta))
	case DragWhole:
		movement := math.Max(bounds.Min-view.Min, math.Min(bounds.Max-view.Max, delta))
		result = Range{math.Max(bounds.Min, view.Min+movement), math.Min(bounds.Max, view.Max+movement)}
	}
	if !result.Valid() {
		return view
	}
	return result
}
