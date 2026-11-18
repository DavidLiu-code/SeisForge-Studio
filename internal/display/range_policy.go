package display

import (
	"errors"
	"math"
	"sort"
)

// RangePolicy determines where the mapping bounds come from. Zero is the old
// saved format: the Auto flag chooses full data or manually entered bounds.
type RangePolicy uint8

const (
	LegacyRange RangePolicy = iota
	FullRange
	PercentileRange
	ManualRange
)

const DefaultCentralPercent = 98.0

var (
	ErrMappingDataUnavailable = errors.New("当前映射输入尚无有效样点或统计未完成")
	ErrPercentiles            = errors.New("百分位须满足 0 ≤ 低百分位 < 高百分位 ≤ 100")
	ErrRangePositions         = errors.New("位置须满足 0 ≤ 起点 < 终点 ≤ 100，且参考数值范围不能为常数")
)

func (m ColorMapping) EffectiveRangePolicy() RangePolicy {
	if m.RangePolicy != LegacyRange {
		return m.RangePolicy
	}
	if m.Auto {
		return FullRange
	}
	return ManualRange
}

func (m *ColorMapping) SetRangePolicy(mode RangePolicy) {
	m.RangePolicy = mode
	if mode != LegacyRange {
		// Old render paths can recognize automatic mappings until they migrate
		// to EffectiveRangePolicy. Never infer percentile mode from this flag.
		m.Auto = mode == FullRange || mode == PercentileRange
	}
	if mode == PercentileRange && m.LowPercentile == 0 && m.HighPercentile == 0 {
		m.LowPercentile, m.HighPercentile, _ = CentralPercentiles(DefaultCentralPercent)
	}
}

func (m ColorMapping) ValidateRangePolicy() error {
	switch m.EffectiveRangePolicy() {
	case FullRange, ManualRange:
		return nil
	case PercentileRange:
		if ValidPercentiles(m.LowPercentile, m.HighPercentile) {
			return nil
		}
		return ErrPercentiles
	default:
		return errors.New("未知的色标范围方式")
	}
}

func ValidPercentiles(low, high float64) bool {
	return finite(low) && finite(high) && low >= 0 && low < high && high <= 100
}

func CentralPercentiles(keep float64) (low, high float64, err error) {
	if !finite(keep) || keep <= 0 || keep > 100 {
		return 0, 0, errors.New("中央保留比例须大于 0 且不超过 100%")
	}
	low = (100 - keep) / 2
	high = 100 - low
	if !ValidPercentiles(low, high) {
		return 0, 0, ErrPercentiles
	}
	return low, high, nil
}

// RangeFromPositions converts percentages of the full numeric span, not
// percentiles of samples. A constant or absent reference is not invertible.
func RangeFromPositions(reference Range, low, high float64) (Range, error) {
	if !reference.Valid() || !ValidPercentiles(low, high) {
		return Range{}, ErrRangePositions
	}
	r := Range{reference.At(low / 100), reference.At(high / 100)}
	if !r.Valid() {
		return Range{}, ErrRangePositions
	}
	return r, nil
}

// PositionsForRange reports unbounded percentages so a wider manual range can
// be described without clipping or changing its actual endpoints.
func PositionsForRange(reference, bounds Range) (low, high float64, ok bool) {
	if !reference.Valid() || !bounds.Valid() {
		return 0, 0, false
	}
	low, high = 100*reference.Fraction(bounds.Min), 100*reference.Fraction(bounds.Max)
	return low, high, finite(low) && finite(high)
}

// SymmetricRangeFromEndpoint is used for deliberate single-edge edits. A zero
// half-width is invalid input; automatic constant-zero data is handled by the
// mapping resolver instead, which expands its graphical range only.
func SymmetricRangeFromEndpoint(v float64) (Range, error) {
	m := math.Abs(v)
	if !finite(m) || m == 0 {
		return Range{}, errors.New("零对称半幅必须为有限正数")
	}
	return Range{-m, m}, nil
}

type weightedSample struct {
	value   float64
	through uint64 // cumulative number of represented samples at this value
}

// RangeDistribution is an immutable rank cache from one or more statistical
// regions. Range and Count are exact. A reservoir entry represents its share
// of that region's valid input count, not an equal vote per region.
type RangeDistribution struct {
	Range       Range
	Count       uint64
	SampleCount int
	Estimated   bool
	samples     []weightedSample
	represented uint64
}

func BuildRangeDistribution(parts ...Distribution) RangeDistribution {
	type entry struct {
		value  float64
		weight uint64
	}
	var out RangeDistribution
	var entries []entry
	for _, p := range parts {
		if p.Count == 0 {
			continue
		}
		if !finite(p.Range.Min) || !finite(p.Range.Max) || p.Range.Min > p.Range.Max || p.Count > ^uint64(0)-out.Count {
			return RangeDistribution{}
		}
		if out.Count == 0 {
			out.Range = p.Range
		} else {
			out.Range.Min = min(out.Range.Min, p.Range.Min)
			out.Range.Max = max(out.Range.Max, p.Range.Max)
		}
		out.Count += p.Count
		start := len(entries)
		for _, v := range p.Samples {
			if uint64(len(entries)-start) == p.Count {
				break
			}
			if finite(v) && v >= p.Range.Min && v <= p.Range.Max {
				entries = append(entries, entry{value: v})
			}
		}
		n := len(entries) - start
		out.Estimated = out.Estimated || uint64(n) != p.Count
		if n == 0 {
			continue
		}
		out.represented += p.Count
		base, remainder := p.Count/uint64(n), p.Count%uint64(n)
		for i := range n {
			entries[start+i].weight = base
			if uint64(i) < remainder {
				entries[start+i].weight++
			}
		}
	}
	out.SampleCount = len(entries)
	sort.Slice(entries, func(i, j int) bool { return entries[i].value < entries[j].value })
	out.samples = make([]weightedSample, 0, len(entries))
	var cumulative uint64
	for _, e := range entries {
		cumulative += e.weight
		if n := len(out.samples); n > 0 && out.samples[n-1].value == e.value {
			out.samples[n-1].through = cumulative
		} else {
			out.samples = append(out.samples, weightedSample{e.value, cumulative})
		}
	}
	return out
}

func (d RangeDistribution) hasSampleRanks() bool {
	return d.Count > 0 && d.represented == d.Count && len(d.samples) > 0
}

// valueAtRank uses exact boundary extrema even when the reservoir has omitted
// the first or last value. Equal repeated samples remain in the rank count.
func (d RangeDistribution) valueAtRank(rank uint64) float64 {
	if rank == 0 {
		return d.Range.Min
	}
	if rank >= d.Count-1 {
		return d.Range.Max
	}
	i := sort.Search(len(d.samples), func(i int) bool { return d.samples[i].through > rank })
	return d.samples[i].value
}

func (d RangeDistribution) Quantile(p float64) (float64, bool) {
	if d.Count == 0 || !finite(p) || p < 0 || p > 100 {
		return 0, false
	}
	if p == 0 {
		return d.Range.Min, true
	}
	if p == 100 {
		return d.Range.Max, true
	}
	if !d.hasSampleRanks() {
		return 0, false
	}
	rank := float64(d.Count-1) * p / 100
	if rank >= float64(d.Count-1) {
		return d.Range.Max, true
	}
	i := uint64(math.Floor(rank))
	f := rank - float64(i)
	a := d.valueAtRank(i)
	if f == 0 {
		return a, true
	}
	b := d.valueAtRank(i + 1)
	return (Range{a, b}).At(f), true
}

func (d RangeDistribution) PercentileBounds(low, high float64) (Range, bool) {
	if !ValidPercentiles(low, high) {
		return Range{}, false
	}
	a, oka := d.Quantile(low)
	b, okb := d.Quantile(high)
	return Range{a, b}, oka && okb
}

// RangeCoverage quantities are percentages, not fractions. Bound-equal
// samples are covered. Estimated describes the pool, not the thresholds' mode.
type RangeCoverage struct {
	LowSaturation, HighSaturation, Covered float64
	Estimated, Valid                       bool
}

func (d RangeDistribution) before(index int) uint64 {
	if index == 0 {
		return 0
	}
	return d.samples[index-1].through
}

func (d RangeDistribution) Coverage(bounds Range) RangeCoverage {
	c := RangeCoverage{Estimated: d.Estimated}
	if d.Count == 0 || !finite(bounds.Min) || !finite(bounds.Max) || bounds.Min > bounds.Max {
		return c
	}
	if bounds.Min <= d.Range.Min && bounds.Max >= d.Range.Max {
		c.Covered, c.Valid = 100, true
		c.Estimated = false // Exact extrema prove this interval contains every sample.
		return c
	}
	if !d.hasSampleRanks() {
		return c
	}
	lo := sort.Search(len(d.samples), func(i int) bool { return d.samples[i].value >= bounds.Min })
	hi := sort.Search(len(d.samples), func(i int) bool { return d.samples[i].value > bounds.Max })
	a, b := d.before(lo), d.Count-d.before(hi)
	c.LowSaturation = 100 * (float64(a) / float64(d.Count))
	c.HighSaturation = 100 * (float64(b) / float64(d.Count))
	c.Covered = 100 * (float64(d.Count-a-b) / float64(d.Count))
	c.Valid = true
	return c
}

// Bins uses the same population weighting as the quantiles and coverage. It
// is only for plotting the distribution, never a source of percentile values.
func (d RangeDistribution) Bins(bounds Range, n int) []uint64 {
	if !bounds.Valid() || n < 1 || n > 1024 || !d.hasSampleRanks() {
		return nil
	}
	bins := make([]uint64, n)
	var prev uint64
	for _, s := range d.samples {
		if s.value >= bounds.Min && s.value <= bounds.Max {
			i := min(n-1, max(0, int(bounds.Fraction(s.value)*float64(n))))
			bins[i] += s.through - prev
		}
		prev = s.through
	}
	return bins
}

type RangeResolution struct {
	Range     Range // final graphical range: candidate plus symmetry or constant expansion
	Candidate Range
	Policy    RangePolicy
	Coverage  RangeCoverage
	Estimated bool // whether interpolated percentile thresholds are estimates
}

func (m ColorMapping) ResolveMappingRange(d RangeDistribution) (RangeResolution, error) {
	res := RangeResolution{Policy: m.EffectiveRangePolicy()}
	if err := m.ValidateRangePolicy(); err != nil {
		return res, err
	}
	switch res.Policy {
	case ManualRange:
		if !m.Range.Valid() {
			return res, errors.New("色标最小值必须小于最大值")
		}
		res.Candidate = m.Range
	case FullRange:
		if d.Count == 0 {
			return res, ErrMappingDataUnavailable
		}
		res.Candidate = d.Range
	case PercentileRange:
		r, ok := d.PercentileBounds(m.LowPercentile, m.HighPercentile)
		if !ok {
			return res, ErrMappingDataUnavailable
		}
		res.Candidate = r
		res.Estimated = d.Estimated && (m.LowPercentile > 0 || m.HighPercentile < 100)
	}
	res.Range = res.Candidate
	if m.Symmetric {
		res.Range = res.Candidate.Symmetric()
	} else if !res.Range.Valid() {
		res.Range = res.Range.Expanded()
	}
	res.Coverage = d.Coverage(res.Range)
	return res, nil
}
