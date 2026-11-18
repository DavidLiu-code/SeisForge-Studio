package display

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestRangePoliciesRetainLegacyMeaningAndExplicitDefaults(t *testing.T) {
	for _, tc := range []struct {
		predecessor string
		want        RangePolicy
	}{
		{`{"Auto":true}`, FullRange}, {`{"Auto":false}`, ManualRange}, {`{}`, ManualRange},
	} {
		var m ColorMapping
		if err := json.Unmarshal([]byte(tc.predecessor), &m); err != nil || m.EffectiveRangePolicy() != tc.want {
			t.Fatalf("legacy %s -> %v policy %v", tc.predecessor, err, m.EffectiveRangePolicy())
		}
		m.SetRangePolicy(PercentileRange)
		if m.LowPercentile != 1 || m.HighPercentile != 99 || !m.Auto {
			t.Fatalf("default mode %+v", m)
		}
		m.LowPercentile, m.HighPercentile = 0, 100
		raw, err := json.Marshal(m)
		if err != nil || json.Unmarshal(raw, &m) != nil || m.EffectiveRangePolicy() != PercentileRange || m.HighPercentile != 100 || m.LowPercentile != 0 {
			t.Fatalf("mode round trip: %s %v", raw, err)
		}
		m.SetRangePolicy(ManualRange)
		if m.Auto || m.EffectiveRangePolicy() != ManualRange {
			t.Fatalf("manual %+v", m)
		}
	}
	for _, p := range []float64{math.NaN(), math.Inf(1), 0, -1, 100.5} {
		if _, _, err := CentralPercentiles(p); err == nil {
			t.Fatalf("invalid keep %v accepted", p)
		}
	}
	for _, tc := range []struct{ keep, lo, hi float64 }{{90, 5, 95}, {95, 2.5, 97.5}, {98, 1, 99}, {99, .5, 99.5}, {100, 0, 100}} {
		lo, hi, err := CentralPercentiles(tc.keep)
		if err != nil || lo != tc.lo || hi != tc.hi {
			t.Fatalf("central percent %g => %g–%g: %v", tc.keep, lo, hi, err)
		}
	}
}

func TestExactMappingPercentileAndSpanDoNotUseHistogramOrRawValues(t *testing.T) {
	var st Stats
	for i := 0; i < 100; i++ {
		st.Add(2 * float64(i))
		st.AddMapped(float64(i))
	}
	st.Add(4e9)
	st.AddMapped(1e4)
	st.AddMapped(math.NaN())
	st.AddMapped(math.Inf(1))
	d := BuildRangeDistribution(st.Mapping())
	if d.Count != 101 || d.Estimated || d.SampleCount != 101 || d.Range != (Range{0, 10000}) {
		t.Fatalf("mapping input distribution %+v", d)
	}
	m := ColorMapping{}
	m.SetRangePolicy(PercentileRange)
	q, err := m.ResolveMappingRange(d)
	if err != nil || q.Range != (Range{1, 99}) || q.Estimated || q.Coverage.Estimated || !q.Coverage.Valid || math.Abs(q.Coverage.Covered-100*99.0/101) > 1e-12 || math.Abs(q.Coverage.LowSaturation-100.0/101) > 1e-12 || math.Abs(q.Coverage.HighSaturation-100.0/101) > 1e-12 {
		t.Fatalf("exact P1–P99 %+v, %v", q, err)
	}
	sp, err := RangeFromPositions(d.Range, 1, 99)
	if err != nil || sp != (Range{100, 9900}) || d.Coverage(sp).Covered != 0 {
		t.Fatalf("numeric span incorrectly treated as percentile: %+v, %v", sp, err)
	}
	if a, b, ok := PositionsForRange(d.Range, Range{-100, 1.01e4}); !ok || a != -1 || b != 101 {
		t.Fatalf("outside positions %g %g %v", a, b, ok)
	}
	m.Symmetric = true
	q, err = m.ResolveMappingRange(d)
	if err != nil || q.Candidate != (Range{1, 99}) || q.Range != (Range{-99, 99}) || q.Coverage.LowSaturation != 0 || math.Abs(q.Coverage.Covered-100*100.0/101) > 1e-12 {
		t.Fatalf("symmetry must extend resolved skewed percentiles before coverage: %+v %v", q, err)
	}
}

func TestQuantilePoolingWeightsUnequalSamplePoolsAndDoesNotModifyInputs(t *testing.T) {
	a := Distribution{Range: Range{-12, 2}, Count: 1e6, Samples: []float64{2, 0, 2}}
	b := Distribution{Range: Range{5, 7}, Count: 3, Samples: []float64{7, 5, 6}}
	d := BuildRangeDistribution(a, b)
	wantBefore := append([]float64(nil), b.Samples...)
	if d.Count != 1e6+3 || d.SampleCount != 6 || !d.Estimated || d.Range != (Range{-12, 7}) || !reflect.DeepEqual(b.Samples, wantBefore) {
		t.Fatalf("pooled metadata %+v", d)
	}
	b.Samples[0] = -999 // verify defensive storage after callers mutate source buffers
	median, ok := d.Quantile(50)
	if !ok || median != 2 {
		t.Fatalf("median %g/%v: small side dominated count", median, ok)
	}
	for _, tc := range []struct{ p, want float64 }{{0, -12}, {100, 7}} {
		v, ok := d.Quantile(tc.p)
		if !ok || v != tc.want {
			t.Fatalf("exact extrema were replaced by reservoir endpoints: P%g=%g", tc.p, v)
		}
	}
	covered := d.Coverage(Range{0, 2})
	if !covered.Valid || !covered.Estimated || math.Abs(covered.HighSaturation-100*3.0/1000003) > 1e-12 || covered.Covered < 99.9 {
		t.Fatalf("unweighted coverage %+v", covered)
	}
	weighted := d.Bins(Range{-12, 7}, 19)
	var sum uint64
	for _, n := range weighted {
		sum += n
	}
	if sum != d.Count {
		t.Fatalf("weighted bins counted %d of %d", sum, d.Count)
	}
	if covered := d.Coverage(d.Range); !covered.Valid || covered.Estimated || covered.Covered != 100 {
		t.Fatalf("exact full count %+v", covered)
	}
}

func TestInterpolatedQuantilesCountRepeatedBoundsAndHandleEmptyConstantZero(t *testing.T) {
	var a, b Stats
	for _, v := range []float64{0, 0, 4, math.NaN()} {
		a.Add(v)
	}
	for _, v := range []float64{4, 8, math.Inf(1)} {
		b.Add(v)
	}
	d := BuildRangeDistribution(a.Mapping(), b.Mapping())
	lo, ok := d.Quantile(37.5)
	if !ok || lo != 2 {
		t.Fatalf("linear quantile %g %v", lo, ok)
	}
	c := d.Coverage(Range{0, 4})
	if !c.Valid || c.Covered != 80 || c.HighSaturation != 20 || c.LowSaturation != 0 {
		t.Fatalf("boundary repeats excluded %+v", c)
	}
	m := ColorMapping{Auto: true, Symmetric: true}
	if _, err := m.ResolveMappingRange(RangeDistribution{}); err != ErrMappingDataUnavailable {
		t.Fatalf("empty mapping success: %v", err)
	}
	var zeros Stats
	zeros.Add(0)
	zeros.Add(math.Copysign(0, -1))
	z := BuildRangeDistribution(zeros.Mapping())
	r, err := m.ResolveMappingRange(z)
	if err != nil || r.Candidate != (Range{0, 0}) || r.Range != (Range{-1, 1}) || !r.Coverage.Valid || r.Coverage.Covered != 100 {
		t.Fatalf("constant zero %+v %v", r, err)
	}
	if _, err = RangeFromPositions(z.Range, 1, 99); err == nil {
		t.Fatal("enabled numeric-span positions for empty interval")
	}
	if sym, err := SymmetricRangeFromEndpoint(-3.25); err != nil || sym != (Range{-3.25, 3.25}) {
		t.Fatalf("single-edge symmetry %v %v", sym, err)
	}
	if _, err := SymmetricRangeFromEndpoint(0); err == nil {
		t.Fatal("explicit zero half width accepted")
	}
}

func TestDisplayCompactAndExactFormattingPreserveNonzeroAndUnits(t *testing.T) {
	v := -7138.39212345678
	if FormatCompactNumber(v) != "-7138.39" || FormatCompactNumber(-v) != "7138.39" || FormatCompactNumber(1427.678199234) != "1427.68" {
		t.Fatal("compact values are not six significant digits")
	}
	for _, value := range []float64{v, math.SmallestNonzeroFloat64, math.MaxFloat64, 0.0002, -0.0002} {
		exact, err := strconv.ParseFloat(FormatExactNumber(value), 64)
		if err != nil || exact != value {
			t.Fatalf("exact readout %g: %g %v", value, exact, err)
		}
		if FormatCompactNumber(value) == "0" {
			t.Fatalf("nonzero readout %g was rounded to zero", value)
		}
	}
	lo := BuildEqualTickPlan(Range{0.0002, 1.0002}, 1, TickOptions{})
	if lo.Ticks[0].Label == "0" || lo.Warning != "" {
		t.Fatalf("equal auto tick rounded tiny endpoint to zero %+v", lo)
	}
	lo = BuildEqualTickPlan(Range{0.0002, 1.0002}, 1, TickOptions{Format: IntegerFormat})
	if lo.Warning == "" || lo.Ticks[0].Label != "0" {
		t.Fatalf("manual format not respected or warning omitted %+v", lo)
	}
	if summary := TickLabelSummary(BuildEqualTickPlan(Range{-9, 9}, 8, TickOptions{}).Ticks); !strings.HasPrefix(summary, "-9") || !strings.HasSuffix(summary, "9") || strings.Count(summary, "…") != 2 {
		t.Fatalf("misleading summary %q", summary)
	}
	for _, tc := range []struct{ title, unit, want string }{{"振幅", "米", "振幅（米）"}, {"振幅", "", "振幅"}, {"", "米", "振幅（米）"}, {"自定义", " 米 ", "自定义（米）"}} {
		if got := ColorbarTitle(tc.title, tc.unit); got != tc.want {
			t.Fatalf("legend title %q -> %q want %q", tc.title, got, tc.want)
		}
	}
}
