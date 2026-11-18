// Package display contains the shared, view-only display settings and math.
package display

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

type Range struct{ Min, Max float64 }

func finite(v float64) bool   { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func (r Range) Valid() bool   { return finite(r.Min) && finite(r.Max) && r.Min < r.Max }
func (r Range) IsPoint() bool { return finite(r.Min) && r.Min == r.Max }
func (r Range) Expanded() Range {
	if r.Valid() {
		return r
	}
	if !finite(r.Min) || r.Min != r.Max {
		return Range{-1, 1}
	}
	if r.Min == 0 {
		return Range{-1, 1}
	}
	d := math.Max(math.Abs(r.Min)*0.01, math.SmallestNonzeroFloat64)
	a, b := r.Min-d, r.Max+d
	if !finite(a) {
		a = r.Min
	}
	if !finite(b) {
		b = r.Max
	}
	return Range{a, b}
}
func (r Range) Fraction(v float64) float64 {
	if !r.Valid() || !finite(v) {
		return math.NaN()
	}
	if span := r.Max - r.Min; finite(span) {
		return (v - r.Min) / span
	}
	return (v/2 - r.Min/2) / (r.Max/2 - r.Min/2)
}
func (r Range) At(t float64) float64 {
	if t == 0 {
		return r.Min
	}
	if t == 1 {
		return r.Max
	}
	if span := r.Max - r.Min; finite(span) {
		return r.Min + t*span
	}
	return (1-t)*r.Min + t*r.Max
}
func (r Range) Symmetric() Range {
	m := math.Max(math.Abs(r.Min), math.Abs(r.Max))
	return (Range{-m, m}).Expanded()
}

type NumberFormat uint8

const (
	AutoFormat NumberFormat = iota
	FixedFormat
	ScientificFormat
	IntegerFormat // Added last: earlier values are stored in display preferences.
)

// DefaultNumberFormat is chosen by new views, not by renumbering the enum:
// the persisted value 0 remains an explicitly saved automatic format.
const DefaultNumberFormat = IntegerFormat

func UsesFractionalDigits(f NumberFormat) bool {
	return f == FixedFormat || f == ScientificFormat
}

func FormatNumber(v float64, f NumberFormat, digits int) string {
	if !finite(v) {
		return "—"
	}
	if v == 0 {
		v = 0
	}
	switch f {
	case FixedFormat:
		return unsignedRoundedZero(strconv.FormatFloat(v, 'f', digits, 64))
	case ScientificFormat:
		return strconv.FormatFloat(v, 'e', digits, 64)
	case IntegerFormat:
		return unsignedRoundedZero(strconv.FormatFloat(v, 'f', 0, 64))
	}
	return FormatExactNumber(v)
}

// FormatExactNumber is for copying/revealing an underlying value, not for
// compact captions. It round-trips a finite binary64 value without changing it.
func FormatExactNumber(v float64) string {
	if !finite(v) {
		return "—"
	}
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// FormatCompactNumber keeps six significant digits for an editable field's
// initial presentation or a readout. Never parse an untouched presentation
// back into the numeric model: it intentionally is not a round-trip encoding.
func FormatCompactNumber(v float64) string {
	return formatSignificantNumber(v, 6)
}

func formatSignificantNumber(v float64, digits int) string {
	if !finite(v) {
		return "—"
	}
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'g', digits, 64)
}

// ColorbarTitle formats an annotation only. Recognizing units here would
// incorrectly change the meaning of the associated amplitude values.
func ColorbarTitle(title, unit string) string {
	title, unit = strings.TrimSpace(title), strings.TrimSpace(unit)
	if title == "" {
		title = "振幅"
	}
	if unit == "" {
		return title
	}
	return title + "（" + unit + "）"
}

func unsignedRoundedZero(s string) string {
	if len(s) < 2 || s[0] != '-' {
		return s
	}
	for i := 1; i < len(s); i++ {
		if s[i] != '0' && s[i] != '.' {
			return s
		}
	}
	return s[1:]
}

func majorTickLength(value float64, set bool, fallback float64) float64 {
	if !set && value == 0 {
		return fallback
	}
	return value
}

const MaxMajorTickDIP = 48

func validMajorTickLength(v float64) bool { return finite(v) && v >= 0 && v <= MaxMajorTickDIP }
func ValidMajorTickDIP(v float64) bool    { return validMajorTickLength(v) }

type Axis struct {
	ID, Title, Unit, BaseUnit                                             string
	Data, View                                                            Range // Both in BaseUnit; if omitted, BaseUnit is Unit.
	StyleOnly, ViewEdited, Reverse, Down, HideAxis, HideLabels, HideTitle bool
	AllowsPoint                                                           bool    `json:"-"` // a seismic trace axis can select exactly one real coordinate
	Step                                                                  float64 // in the canonical unit
	Format                                                                NumberFormat
	Digits                                                                int
	TickLength                                                            float64 `json:",omitempty"`
	TickLengthSet                                                         bool    `json:",omitempty"`
	Divisions                                                             int     `json:",omitempty"`
	TickFontPT, TitleFontPT                                               float64 `json:",omitempty"`
}

func (a Axis) MajorTickDIP() float64 { return majorTickLength(a.TickLength, a.TickLengthSet, 4) }

func Units(base string) []string {
	switch base {
	case "s", "ms", "µs":
		return []string{"s", "ms", "µs"}
	case "m", "km":
		return []string{"m", "km"}
	}
	return []string{base}
}
func unitScale(unit string) float64 {
	switch unit {
	case "ms":
		return 1e-3
	case "µs":
		return 1e-6
	case "km":
		return 1e3
	}
	return 1
}
func (a Axis) CanonicalUnit() string {
	if a.BaseUnit != "" {
		return a.BaseUnit
	}
	return a.Unit
}
func (a Axis) DisplayValue(v float64) float64 {
	return v * (unitScale(a.CanonicalUnit()) / unitScale(a.Unit))
}
func (a Axis) BaseValue(v float64) float64 {
	return v * (unitScale(a.Unit) / unitScale(a.CanonicalUnit()))
}
func (a Axis) Fraction(v float64) float64 {
	if a.AllowsPoint && a.View.IsPoint() && v == a.View.Min {
		return 0.5 // the sole real coordinate is centered in the display-only span
	}
	f := a.View.Fraction(v)
	if a.Reverse {
		return 1 - f
	}
	return f
}
func (a Axis) ValueAtFraction(f float64) float64 {
	if a.Reverse {
		f = 1 - f
	}
	return a.View.At(f)
}
func (a Axis) Pixel(v float64, start, end int) int {
	return start + int(math.Round(float64(end-start)*a.Fraction(v)))
}
func (a Axis) ValidRange(r Range) bool { return r.Valid() || a.AllowsPoint && r.IsPoint() }

func (a Axis) Valid() error {
	if a.ID == "" || (a.ViewEdited && !a.ValidRange(a.View)) || (a.StyleOnly && (a.ViewEdited || a.Reverse)) || !finite(a.Step) || a.Step < 0 || a.Digits < 0 || a.Digits > 12 || a.Format > IntegerFormat || !validMajorTickLength(a.TickLength) || !validTickPresentation(a.Divisions, a.TickFontPT, a.TitleFontPT) {
		return errors.New("坐标范围或刻度无效")
	}
	for _, u := range Units(a.CanonicalUnit()) {
		if a.Unit == u {
			return nil
		}
	}
	return errors.New("坐标单位不兼容")
}

func Ticks(r Range, step float64, maximum int) []float64 {
	if maximum < 2 {
		return nil
	}
	p := BuildTickPlan(r, step, TickOptions{MaxTicks: maximum})
	out := make([]float64, len(p.Ticks))
	for i, t := range p.Ticks {
		out[i] = t.Value
	}
	return out
}

type RGB struct{ R, G, B byte }
type GradientStop struct {
	Position float64
	Color    RGB
}
type ColorMapping struct {
	Range                            Range
	Stops                            []GradientStop
	Auto, Symmetric, Reverse, Hidden bool
	VisibilitySet                    bool `json:",omitempty"`
	Title, Unit                      string
	Step                             float64
	Format                           NumberFormat
	Digits                           int
	TickLength                       float64     `json:",omitempty"`
	TickLengthSet                    bool        `json:",omitempty"`
	Divisions                        int         `json:",omitempty"`
	TickFontPT, TitleFontPT          float64     `json:",omitempty"`
	RangePolicy                      RangePolicy `json:",omitempty"`
	LowPercentile, HighPercentile    float64     `json:",omitempty"`
}

func (m ColorMapping) MajorTickDIP() float64 {
	return majorTickLength(m.TickLength, m.TickLengthSet, 6)
}

func (m ColorMapping) Valid() error {
	if !m.Range.Valid() || !finite(m.Step) || m.Step < 0 || m.Digits < 0 || m.Digits > 12 || m.Format > IntegerFormat || !validMajorTickLength(m.TickLength) || !validTickPresentation(m.Divisions, m.TickFontPT, m.TitleFontPT) {
		return errors.New("色标范围或刻度无效")
	}
	if err := m.ValidateRangePolicy(); err != nil {
		return err
	}
	if len(m.Stops) < 2 || len(m.Stops) > 512 || m.Stops[0].Position != 0 || m.Stops[len(m.Stops)-1].Position != 1 {
		return errors.New("颜色控制点必须包含两个端点")
	}
	for i, p := range m.Stops {
		if !finite(p.Position) || p.Position < 0 || p.Position > 1 || (i > 0 && p.Position <= m.Stops[i-1].Position) {
			return errors.New("颜色控制点必须有序且不能重合")
		}
	}
	return nil
}
func LegacyStops(get func(index int) RGB) []GradientStop {
	// Legacy raster index 255 means minimum, 0 means maximum. Preserve the full LUT.
	out := make([]GradientStop, 256)
	for i := range out {
		out[i] = GradientStop{float64(i) / 255, get(255 - i)}
	}
	return out
}
func (m ColorMapping) ColorAt(v float64) RGB {
	if !finite(v) || !m.Range.Valid() {
		return RGB{225, 225, 225}
	}
	if v <= m.Range.Min {
		return m.ColorAtPosition(0)
	}
	if v >= m.Range.Max {
		return m.ColorAtPosition(1)
	}
	return m.ColorAtPosition(m.Range.Fraction(v))
}
func (m ColorMapping) ColorAtPosition(t float64) RGB {
	if !finite(t) {
		return RGB{225, 225, 225}
	}
	t = math.Max(0, math.Min(1, t))
	if m.Reverse {
		t = 1 - t
	}
	if len(m.Stops) == 0 {
		g := byte(math.Round(t * 255))
		return RGB{g, g, g}
	}
	lo, hi := 0, len(m.Stops)-1
	for hi-lo > 1 {
		i := (lo + hi) / 2
		if m.Stops[i].Position < t {
			lo = i
		} else {
			hi = i
		}
	}
	a, b := m.Stops[lo], m.Stops[hi]
	if t <= a.Position || lo == hi {
		return a.Color
	}
	if t >= b.Position {
		return b.Color
	}
	f := (t - a.Position) / (b.Position - a.Position)
	mix := func(l, r byte) byte { return byte(math.Round(float64(l) + (float64(r)-float64(l))*f)) }
	return RGB{mix(a.Color.R, b.Color.R), mix(a.Color.G, b.Color.G), mix(a.Color.B, b.Color.B)}
}
func (m ColorMapping) Clone() ColorMapping {
	m.Stops = append([]GradientStop(nil), m.Stops...)
	return m
}

type Settings struct {
	Axes            []Axis
	Color           *ColorMapping
	PlotTitleFontPT float64 `json:",omitempty"`
}

const (
	MinFontPT     = 5
	MaxFontPT     = 36
	DefaultTickPT = 10
)

// A zero in saved settings means to use the font default, not zero-point text.
// Explicit editor values must use ValidEnteredFontPT.
func ValidFontPT(v float64) bool { return v == 0 || ValidEnteredFontPT(v) }

func ValidEnteredFontPT(v float64) bool {
	return finite(v) && v >= MinFontPT && v <= MaxFontPT
}

func validTickPresentation(n int, tick, title float64) bool {
	return n >= 0 && n <= 20 && ValidFontPT(tick) && ValidFontPT(title)
}

func (s Settings) Clone() Settings {
	s.Axes = append([]Axis(nil), s.Axes...)
	if s.Color != nil {
		m := s.Color.Clone()
		s.Color = &m
	}
	return s
}

const sampleLimit = 8192

type Distribution struct {
	Range   Range
	Count   uint64
	Samples []float64
}

func (d *Distribution) add(v float64) {
	if !finite(v) {
		return
	}
	if d.Count == 0 {
		d.Range = Range{v, v}
	} else {
		d.Range.Min = math.Min(d.Range.Min, v)
		d.Range.Max = math.Max(d.Range.Max, v)
	}
	d.Count++
	if len(d.Samples) < sampleLimit {
		d.Samples = append(d.Samples, v)
		return
	}
	// Deterministic reservoir selection, independent of input chunking.
	x := d.Count + 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	x ^= x >> 31
	if i := x % d.Count; i < sampleLimit {
		d.Samples[i] = v
	}
}
func (d Distribution) Bins(r Range, n int) []int {
	if !r.Valid() || n < 1 || n > 1024 {
		return nil
	}
	bins := make([]int, n)
	for _, v := range d.Samples {
		if v < r.Min || v > r.Max {
			continue
		}
		i := min(n-1, int(r.Fraction(v)*float64(n)))
		if i >= 0 {
			bins[i]++
		}
	}
	return bins
}

type Stats struct {
	Raw, Mapped  Distribution
	MappedDomain bool
}

func (s *Stats) Add(v float64)       { s.Raw.add(v) }
func (s *Stats) AddMapped(v float64) { s.MappedDomain = true; s.Mapped.add(v) }
func (s Stats) Mapping() Distribution {
	if s.MappedDomain {
		return s.Mapped
	}
	return s.Raw
}
