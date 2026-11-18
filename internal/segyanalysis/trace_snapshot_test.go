package segyanalysis

import (
	"math"
	"strings"
	"testing"
)

func TestTraceSnapshotSharedReferenceOnReal351SampleWindow(t *testing.T) {
	const n, startUS, dtUS, frequency = 351, 800000, 2000, 22.0
	a := sineTrace(n, dtUS, frequency, 3)
	for _, tt := range []struct {
		name        string
		b, residual float64
	}{
		{"same", 1, 0},
		{"half", .5, .5},
		{"inverted", -1, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := make([]float64, n)
			for i, amplitude := range a {
				b[i] = tt.b * amplitude
			}
			s := BuildTraceSnapshot([]TraceInput{{"A", startUS, dtUS, a}, {"B", startUS, dtUS, b}}, true)
			if len(s.Series) != 3 || s.ComparisonOverlapSamples != n || s.ResidualNote != "" || s.Reference.Fallback || s.Reference.Role != "A" {
				t.Fatalf("series %d, overlap %d, note %q, reference %+v", len(s.Series), s.ComparisonOverlapSamples, s.ResidualNote, s.Reference)
			}
			bin := s.Series[0].Spectrum.PeakBin
			if peak := s.Series[0].Spectrum.PeakHz; math.Abs(peak-frequency) > 1 {
				t.Errorf("peak frequency %.3f Hz", peak)
			}
			for i, wantRatio := range []float64{1, math.Abs(tt.b), tt.residual} {
				series := s.Series[i]
				spectrum := series.Spectrum
				if series.SpectrumError != "" || spectrum.StartUS != startUS || spectrum.InputSamples != n || spectrum.UsedSamples != n || spectrum.StartUS+int64(spectrum.UsedSamples-1)*dtUS != 1500000 || spectrum.NFFT != 512 || spectrum.NyquistHz != 250 {
					t.Fatalf("%s incorrect FFT domain: error=%q start=%d samples=%d FFT=%d Nyquist=%g", series.Role, series.SpectrumError, spectrum.StartUS, spectrum.InputSamples, spectrum.NFFT, spectrum.NyquistHz)
				}
				if got := spectrum.LinearAmplitude[bin] / s.Reference.Amplitude; math.Abs(got-wantRatio) > 1e-12 {
					t.Errorf("%s linear magnitude ratio %g, want %g", series.Role, got, wantRatio)
				}
				db := spectrum.DB[bin]
				if wantRatio == 0 {
					if !math.IsInf(db, -1) || series.Stats.ZeroCount != n {
						t.Errorf("zero residual has dB=%g, zero samples=%d", db, series.Stats.ZeroCount)
					}
					continue
				}
				if want := 20 * math.Log10(wantRatio); math.Abs(db-want) > 1e-12 {
					t.Errorf("%s peak %g dB, want %g", series.Role, db, want)
				}
			}
			if tt.residual > 1 && s.DBRange().Max < s.Series[2].Spectrum.DB[bin] {
				t.Errorf("positive residual is clipped by automatic display range %+v", s.DBRange())
			}
		})
	}
}

func TestTraceSnapshotOverlapUsesAbsoluteTimeAndLeavesIndividualWaveformsIntact(t *testing.T) {
	a := sineTrace(103, 2000, 30, 1)
	b := append([]float64(nil), a[3:93]...)
	for i := range b {
		b[i] *= .5
	}
	// The A and B input slices index time zero differently; their common real
	// window begins at 800 ms, which is index 3 in A, index 0 in B.
	s := BuildTraceSnapshot([]TraceInput{{"B", 800000, 2000, b}, {"A", 794000, 2000, a}}, true)
	if len(s.Series) != 3 || s.ComparisonOverlapSamples != 90 || s.Series[2].Role != "A-B" {
		t.Fatalf("wrong overlap: series=%d overlap=%d note=%q", len(s.Series), s.ComparisonOverlapSamples, s.ResidualNote)
	}
	if s.Series[1].StartUS != 794000 || s.Series[1].Stats.SampleCount != 103 || s.Series[1].Spectrum.InputSamples != 90 {
		t.Fatalf("A waveform was cropped or A FFT was not aligned: %+v", s.Series[1].Stats)
	}
	for _, series := range s.Series {
		if series.Spectrum.StartUS != 800000 || series.Spectrum.InputSamples != len(b) {
			t.Errorf("%s common FFT start=%d samples=%d", series.Role, series.Spectrum.StartUS, series.Spectrum.InputSamples)
		}
	}
	if s.Series[2].StartUS != 800000 || s.Series[2].Samples[0] != a[3]-b[0] || s.Series[2].Samples[89] != a[92]-b[89] {
		t.Fatal("residual not computed on exact common sample indices")
	}
	b[0] = 999
	if s.Series[0].Samples[0] == 999 {
		t.Fatal("snapshot aliases caller input")
	}
}

func TestTraceSnapshotNoSyntheticResidualWhenWindowsIncompatible(t *testing.T) {
	input := sineTrace(64, 2000, 30, 1)
	for _, tt := range []struct {
		name    string
		startUS int64
		dtUS    int
		note    string
	}{
		{"interval", 800000, 4000, "采样间隔"},
		{"off-grid", 800500, 2000, "未对齐"},
		{"disjoint", 1000000, 2000, "没有共同样点"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := BuildTraceSnapshot([]TraceInput{{"A", 800000, 2000, input}, {"B", tt.startUS, tt.dtUS, input}}, true)
			if len(s.Series) != 2 || s.ComparisonOverlapSamples != 0 || !strings.Contains(s.ResidualNote, tt.note) {
				t.Fatalf("incompatible pair got %d series and residual note %q", len(s.Series), s.ResidualNote)
			}
			for _, series := range s.Series {
				if series.Spectrum.StartUS != series.StartUS || series.Spectrum.InputSamples != len(input) || series.SpectrumError != "" {
					t.Errorf("individual spectrum unavailable on its own window for %s", series.Role)
				}
			}
		})
	}
}

func TestTraceSnapshotFallbackDoesNotNormalizeToB(t *testing.T) {
	b := sineTrace(64, 2000, 32, 1)
	constant := make([]float64, len(b))
	for i := range constant {
		constant[i] = .1 // a naive summed mean leaves a rounding-only spectrum
	}
	for _, inputs := range [][]TraceInput{
		{{"B", 800000, 2000, b}},
		{{"B", 800000, 2000, b}, {"A", 800000, 2000, make([]float64, len(b))}},
		{{"B", 800000, 2000, b}, {"A", 800000, 2000, constant}},
	} {
		s := BuildTraceSnapshot(inputs, true)
		if s.Reference.Amplitude != 1 || !s.Reference.Fallback || s.Reference.Role != "A" {
			t.Fatalf("fallback reference %+v", s.Reference)
		}
		spectrum := s.Series[0].Spectrum
		if got, want := spectrum.DB[spectrum.PeakBin], 20*math.Log10(spectrum.PeakAmplitude); got != want || got <= 0 || s.DBRange().Max < got {
			t.Errorf("B peak %g dB, want %g dB, range %+v", got, want, s.DBRange())
		}
	}
	standalone := BuildTraceSnapshot([]TraceInput{{"B", 800000, 2000, b}}, false)
	spectrum := standalone.Series[0].Spectrum
	if standalone.Reference.Role != "B" || standalone.Reference.Fallback || spectrum.DB[spectrum.PeakBin] != 0 {
		t.Errorf("standalone reference %+v, peak dB %g", standalone.Reference, spectrum.DB[spectrum.PeakBin])
	}
}

func TestTraceSnapshotSharedAmplitudeRangesAndNonfiniteSamples(t *testing.T) {
	a, b := make([]float64, 201), make([]float64, 201)
	for i := range a {
		a[i] = -2 + float64(i)/100
		b[i] = float64(i)/100 + 1
	}
	a[0], b[0], b[1] = -1000, 700, math.NaN()
	s := BuildTraceSnapshot([]TraceInput{{"A", 800000, 2000, a}, {"B", 800000, 2000, b}}, false)
	if got := s.AmplitudeRange(ScaleFull, DisplayRange{}, false); got != (DisplayRange{-1000, 700}) {
		t.Errorf("shared full range %+v", got)
	}
	robust := s.AmplitudeRange(ScaleRobust, DisplayRange{}, false)
	finite := append(append([]float64(nil), a...), b...)
	p01, _ := FinitePercentile(finite, 1)
	p99, _ := FinitePercentile(finite, 99)
	if robust != (DisplayRange{p01, p99}) {
		t.Errorf("shared robust range %+v != [%g,%g]", robust, p01, p99)
	}
	if got := s.AmplitudeRange(ScaleManual, DisplayRange{-4, 12}, true); got != (DisplayRange{-12, 12}) {
		t.Errorf("symmetric manual range %+v", got)
	}
	if got := s.AmplitudeRange(ScaleManual, DisplayRange{4, 2}, false); got != robust {
		t.Errorf("invalid manual range did not fall back: %+v", got)
	}
	if s.Series[1].Stats.NonFiniteCount != 1 || len(s.Series[1].Spectrum.DB) != 0 || s.Series[1].SpectrumError == "" {
		t.Error("nonfinite sample should count in statistics and make its spectrum explicitly unavailable")
	}
}

func TestDisplayRangeFractionHandlesExtremeFiniteValuesAndDBZeros(t *testing.T) {
	r := DisplayRange{-math.MaxFloat64, math.MaxFloat64}
	if r.Fraction(0) != .5 || r.Fraction(-math.MaxFloat64/2) != .25 || r.Fraction(math.MaxFloat64/2) != .75 {
		t.Fatalf("extreme waveform range mapped to %g, %g, %g", r.Fraction(-math.MaxFloat64/2), r.Fraction(0), r.Fraction(math.MaxFloat64/2))
	}
	db := DisplayRange{-80, 10}
	if db.Fraction(math.Inf(-1)) != 0 || db.Fraction(math.Inf(1)) != 1 || !math.IsNaN(db.Fraction(math.NaN())) {
		t.Fatal("dB limits did not clip infinities or preserve NaN gaps")
	}
	if got := percentileSorted([]float64{-math.MaxFloat64, math.MaxFloat64}, 50); got != 0 {
		t.Fatalf("extreme robust percentile: got %g, want 0", got)
	}
}
