package segyanalysis

import (
	"math"
	"strings"
	"testing"
)

func sineTrace(sampleCount, sampleIntervalUS int, frequencyHz, offset float64) []float64 {
	values := make([]float64, sampleCount)
	dt := float64(sampleIntervalUS) * 1e-6
	for index := range values {
		values[index] = offset + math.Sin(2*math.Pi*frequencyHz*float64(index)*dt)
	}
	return values
}

func TestComputeTraceSpectrumUsesEverySampleAndFindsSinePeak(t *testing.T) {
	const sampleIntervalUS = 1000
	values := sineTrace(1000, sampleIntervalUS, 50, 7.25)
	result, err := ComputeTraceSpectrum(values, sampleIntervalUS)
	if err != nil {
		t.Fatal(err)
	}
	if result.InputSamples != 1000 || result.UsedSamples != 1000 || result.NFFT != 1024 {
		t.Fatalf("selected samples were not fully retained: %+v", result)
	}
	if len(result.FrequencyHz) != 513 || len(result.LinearAmplitude) != 513 || len(result.NormalizedDB) != 513 {
		t.Fatalf("unexpected one-sided spectrum lengths: %+v", result)
	}
	if !nearlyEqual(result.NyquistHz, 500, 1e-12) || math.Abs(result.PeakHz-50) > 0.6 {
		t.Fatalf("unexpected frequency scale/peak: nyquist=%g peak=%g", result.NyquistHz, result.PeakHz)
	}
	if math.Abs(result.NormalizedDB[result.PeakBin]) > 1e-10 || result.PeakAmplitude <= 0 {
		t.Fatalf("peak was not normalized to 0 dB: %+v", result)
	}
}

func TestComputeTraceSpectrumAllZeroAndConstantAreDefined(t *testing.T) {
	constant := make([]float64, 64)
	for index := range constant {
		constant[index] = 12.5
	}
	for name, values := range map[string][]float64{
		"zero":     make([]float64, 64),
		"constant": constant,
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ComputeTraceSpectrum(values, 2000)
			if err != nil {
				t.Fatal(err)
			}
			if result.PeakAmplitude != 0 || result.PeakHz != 0 {
				t.Fatalf("flat trace reported a peak: %+v", result)
			}
			for index, amplitude := range result.LinearAmplitude {
				if amplitude != 0 || result.NormalizedDB[index] != DefaultDBFloor {
					t.Fatalf("flat bin %d = amplitude %g, dB %g", index, amplitude, result.NormalizedDB[index])
				}
			}
		})
	}
}

func TestComputeTraceSpectrumRejectsShortNonFiniteAndOversizedInput(t *testing.T) {
	if _, err := ComputeTraceSpectrum(make([]float64, 31), 2000); err == nil {
		t.Fatal("short trace was accepted")
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		values := make([]float64, 64)
		values[27] = value
		if _, err := ComputeTraceSpectrum(values, 2000); err == nil || !strings.Contains(err.Error(), "not finite") {
			t.Fatalf("non-finite value %v was accepted: %v", value, err)
		}
	}
	if _, err := ComputeTraceSpectrum(make([]float64, MaximumNFFT+1), 2000); err == nil {
		t.Fatal("oversized trace was silently truncated")
	}
	if _, err := ComputeTraceSpectrum(make([]float64, 64), 0); err == nil {
		t.Fatal("zero sample interval was accepted")
	}
}

func TestComputeTraceSpectrumRetainsFiniteExtreme(t *testing.T) {
	values := sineTrace(64, 2000, 31.25, 0)
	values[0] = -999999
	result, err := ComputeTraceSpectrum(values, 2000)
	if err != nil {
		t.Fatalf("finite extreme was rejected: %v", err)
	}
	if result.UsedSamples != len(values) || result.PeakAmplitude <= 0 {
		t.Fatalf("finite extreme trace was not analyzed: %+v", result)
	}
}

func TestExtremeFiniteTraceSpectrumKeepsRepresentableSignal(t *testing.T) {
	const n, dtUS, peakBin = 64, 2000, 16
	offset, amplitude := math.Ldexp(1, 1023), math.Ldexp(1, 990)
	phase := [...]float64{0, 1, 0, -1}
	samples := make([]float64, n)
	for i := range samples {
		samples[i] = offset + amplitude*phase[i%len(phase)]
	}
	// The four-sample sequence has an exactly representable DC offset. At
	// the 125 Hz bin only odd samples contribute, all with the same sign
	// after correlation. This closed-form sum does not use the FFT under test.
	want := 0.0
	for i := 1; i < n; i += 2 {
		want += (1 - math.Cos(2*math.Pi*float64(i)/(n-1))) / 2
	}
	want *= amplitude
	got, err := ComputeTraceSpectrum(samples, dtUS)
	if err != nil {
		t.Fatal(err)
	}
	if got.PeakBin != peakBin || got.PeakHz != 125 || !(math.Abs(got.PeakAmplitude/want-1) <= 1e-12) || got.NormalizedDB[peakBin] != 0 {
		t.Errorf("standalone FFT: peak bin %d at %g Hz, amplitude %g, normalized dB %g; want bin %d at 125 Hz, amplitude %g and 0 dB", got.PeakBin, got.PeakHz, got.PeakAmplitude, got.NormalizedDB[peakBin], peakBin, want)
	}
	snap := BuildTraceSnapshot([]TraceInput{{Role: "A", StartUS: 800000, IntervalUS: dtUS, Samples: samples}}, false)
	if series := snap.Series[0]; series.SpectrumError != "" || series.Spectrum.PeakBin != peakBin || !(math.Abs(series.Spectrum.PeakAmplitude/want-1) <= 1e-12) || snap.Reference.Fallback {
		t.Errorf("shared trace snapshot: peak bin %d at %g Hz, amplitude %g, fallback %v, error %q; want amplitude %g", series.Spectrum.PeakBin, series.Spectrum.PeakHz, series.Spectrum.PeakAmplitude, snap.Reference.Fallback, series.SpectrumError, want)
	}
	t.Logf("finite input offset %.10g; expected AC FFT amplitude %.10g at 125 Hz", offset, want)
}

func TestExtremeFiniteSpectrumExplicitlyRejectsUnrepresentableMagnitude(t *testing.T) {
	values := make([]float64, 64)
	for i := range values {
		values[i] = math.MaxFloat64 / 2
		if i%2 != 0 {
			values[i] = -values[i]
		}
	}
	if got, err := ComputeTraceSpectrum(values, 2000); err == nil || !strings.Contains(err.Error(), "magnitude") {
		t.Fatalf("overflowing Nyquist magnitude: peak %g normalized %v; want explicit error, got %v", got.PeakAmplitude, got.NormalizedDB, err)
	}
}

func TestLegacyQCAmplitudeOptionsUseFloorPowerOfTwoAnd4096Cap(t *testing.T) {
	options := LegacyQCAmplitudeOptions()
	result, err := ComputeAmplitudeSpectrum(sineTrace(5000, 2000, 20, 3), 2000, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.InputSamples != 5000 || result.UsedSamples != 4096 || result.NFFT != 4096 || len(result.LinearAmplitude) != 2049 {
		t.Fatalf("legacy 4096 cap changed: %+v", result)
	}
	result, err = ComputeAmplitudeSpectrum(sineTrace(3000, 2000, 20, 3), 2000, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.UsedSamples != 2048 || result.NFFT != 2048 {
		t.Fatalf("legacy floor-power-of-two semantics changed: %+v", result)
	}
}

func TestComputeAmplitudeSpectrumOptionValidation(t *testing.T) {
	values := make([]float64, 64)
	if _, err := ComputeAmplitudeSpectrum(values, 2000, AmplitudeOptions{LengthPolicy: FFTLengthPolicy(99)}); err == nil {
		t.Fatal("invalid FFT policy was accepted")
	}
	if _, err := ComputeAmplitudeSpectrum(values, 2000, AmplitudeOptions{MaxNFFT: MaximumNFFT + 1}); err == nil {
		t.Fatal("unsafe FFT maximum was accepted")
	}
	if _, err := ComputeAmplitudeSpectrum(values, 2000, AmplitudeOptions{LengthPolicy: FFTLengthCeil, MaxNFFT: 32}); err == nil {
		t.Fatal("ceil mode silently discarded samples")
	}
}
