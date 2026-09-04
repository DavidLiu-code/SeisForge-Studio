package segyanalysis

import (
	"errors"
	"fmt"
	"math"
)

const (
	// MaximumNFFT bounds the memory and CPU cost of a single-trace analysis.
	// SEG-Y's ordinary two-byte sample count cannot exceed 65,535, so 65,536
	// bins can contain every sample without truncation.
	MaximumNFFT = 65536
	// MinimumTraceSpectrumSamples prevents misleading spectra from very short
	// trace windows.
	MinimumTraceSpectrumSamples = 32
	// DefaultDBFloor is the displayed normalized-amplitude floor.
	DefaultDBFloor = -80.0
)

// FFTLengthPolicy determines whether an input window is zero-padded upward or
// truncated downward to a radix-2 FFT length.
type FFTLengthPolicy int

const (
	// FFTLengthCeil preserves every input sample and pads with zeroes.
	FFTLengthCeil FFTLengthPolicy = iota
	// FFTLengthFloor uses the first power-of-two-sized portion of the input.
	// This matches the historical Limage average-spectrum estimator.
	FFTLengthFloor
)

// AmplitudeOptions controls the low-level linear amplitude spectrum. The
// result is deliberately not normalized so callers can average linear spectra
// before converting them to dB.
type AmplitudeOptions struct {
	LengthPolicy   FFTLengthPolicy
	MaxNFFT        int
	MinimumSamples int
	RemoveMean     bool
	ApplyHann      bool
}

// LegacyQCAmplitudeOptions reproduces the old average-spectrum preparation:
// use the first floor-power-of-two window, cap it at 4096, remove its mean and
// apply a Hann window.
func LegacyQCAmplitudeOptions() AmplitudeOptions {
	return AmplitudeOptions{
		LengthPolicy:   FFTLengthFloor,
		MaxNFFT:        4096,
		MinimumSamples: MinimumTraceSpectrumSamples,
		RemoveMean:     true,
		ApplyHann:      true,
	}
}

// AmplitudeResult is a one-sided linear amplitude spectrum including DC and
// Nyquist. FrequencyHz and LinearAmplitude always have NFFT/2+1 elements.
type AmplitudeResult struct {
	FrequencyHz     []float64
	LinearAmplitude []float64
	InputSamples    int
	UsedSamples     int
	NFFT            int
	NyquistHz       float64
}

// Spectrum is the single-trace presentation result. NormalizedDB is relative
// to the strongest non-DC bin and is clamped to DBFloor.
type Spectrum struct {
	FrequencyHz     []float64
	LinearAmplitude []float64
	NormalizedDB    []float64
	InputSamples    int
	UsedSamples     int
	NFFT            int
	NyquistHz       float64
	PeakBin         int
	PeakHz          float64
	PeakAmplitude   float64
	DBFloor         float64
}

// ComputeTraceSpectrum calculates a full selected-trace spectrum. Every input
// sample participates; a non-power-of-two length is zero-padded upward. Input
// longer than MaximumNFFT is rejected rather than silently truncated.
func ComputeTraceSpectrum(samples []float64, sampleIntervalUS int) (Spectrum, error) {
	if len(samples) < MinimumTraceSpectrumSamples {
		return Spectrum{}, fmt.Errorf("trace spectrum requires at least %d samples", MinimumTraceSpectrumSamples)
	}
	for index, value := range samples {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return Spectrum{}, fmt.Errorf("trace spectrum sample %d is not finite", index)
		}
	}
	linear, err := ComputeAmplitudeSpectrum(samples, sampleIntervalUS, AmplitudeOptions{
		LengthPolicy:   FFTLengthCeil,
		MaxNFFT:        MaximumNFFT,
		MinimumSamples: MinimumTraceSpectrumSamples,
		RemoveMean:     true,
		ApplyHann:      true,
	})
	if err != nil {
		return Spectrum{}, err
	}

	result := Spectrum{
		FrequencyHz:     linear.FrequencyHz,
		LinearAmplitude: linear.LinearAmplitude,
		NormalizedDB:    make([]float64, len(linear.LinearAmplitude)),
		InputSamples:    linear.InputSamples,
		UsedSamples:     linear.UsedSamples,
		NFFT:            linear.NFFT,
		NyquistHz:       linear.NyquistHz,
		DBFloor:         DefaultDBFloor,
	}
	for bin := 1; bin < len(linear.LinearAmplitude); bin++ {
		if linear.LinearAmplitude[bin] > result.PeakAmplitude {
			result.PeakAmplitude = linear.LinearAmplitude[bin]
			result.PeakBin = bin
		}
	}
	if result.PeakAmplitude <= 0 {
		for index := range result.NormalizedDB {
			result.NormalizedDB[index] = result.DBFloor
		}
		return result, nil
	}
	result.PeakHz = result.FrequencyHz[result.PeakBin]
	minimumRatio := math.Pow(10, result.DBFloor/20)
	for index, amplitude := range linear.LinearAmplitude {
		ratio := amplitude / result.PeakAmplitude
		if ratio < minimumRatio {
			ratio = minimumRatio
		}
		db := 20 * math.Log10(ratio)
		if db < result.DBFloor {
			db = result.DBFloor
		}
		result.NormalizedDB[index] = db
	}
	return result, nil
}

// ComputeAmplitudeSpectrum returns a one-sided raw FFT magnitude spectrum.
// In floor mode only the first UsedSamples values are examined, matching the
// legacy estimator's explicit input-window truncation. In ceil mode all input
// samples are retained and zero-padded.
func ComputeAmplitudeSpectrum(samples []float64, sampleIntervalUS int, options AmplitudeOptions) (AmplitudeResult, error) {
	if sampleIntervalUS <= 0 {
		return AmplitudeResult{}, errors.New("sample interval must be positive")
	}
	if options.LengthPolicy != FFTLengthCeil && options.LengthPolicy != FFTLengthFloor {
		return AmplitudeResult{}, errors.New("unsupported FFT length policy")
	}
	maxNFFT := options.MaxNFFT
	if maxNFFT <= 0 {
		maxNFFT = MaximumNFFT
	}
	if maxNFFT > MaximumNFFT {
		return AmplitudeResult{}, fmt.Errorf("maximum FFT length exceeds %d", MaximumNFFT)
	}
	minimum := options.MinimumSamples
	if minimum <= 0 {
		minimum = 1
	}

	nfft, used, err := plannedFFTLength(len(samples), maxNFFT, options.LengthPolicy)
	if err != nil {
		return AmplitudeResult{}, err
	}
	if used < minimum {
		return AmplitudeResult{}, fmt.Errorf("amplitude spectrum requires at least %d samples", minimum)
	}
	for index, value := range samples[:used] {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return AmplitudeResult{}, fmt.Errorf("amplitude spectrum sample %d is not finite", index)
		}
	}

	mean := 0.0
	if options.RemoveMean {
		// Keep the historical summation order so the existing average-spectrum
		// viewer can adopt this helper without changing its curve.
		for _, value := range samples[:used] {
			mean += value
		}
		mean /= float64(used)
	}
	values := make([]complex128, nfft)
	for index, value := range samples[:used] {
		window := 1.0
		if options.ApplyHann && used > 1 {
			window = 0.5 - 0.5*math.Cos(2*math.Pi*float64(index)/float64(used-1))
		}
		values[index] = complex((value-mean)*window, 0)
	}
	fftRadix2(values)

	binCount := nfft/2 + 1
	result := AmplitudeResult{
		FrequencyHz:     make([]float64, binCount),
		LinearAmplitude: make([]float64, binCount),
		InputSamples:    len(samples),
		UsedSamples:     used,
		NFFT:            nfft,
		NyquistHz:       1e6 / (2 * float64(sampleIntervalUS)),
	}
	frequencyStep := 1e6 / (float64(nfft) * float64(sampleIntervalUS))
	for bin := 0; bin < binCount; bin++ {
		result.FrequencyHz[bin] = float64(bin) * frequencyStep
		result.LinearAmplitude[bin] = math.Hypot(real(values[bin]), imag(values[bin]))
	}
	return result, nil
}

func plannedFFTLength(sampleCount, maxNFFT int, policy FFTLengthPolicy) (nfft, used int, err error) {
	if sampleCount < 1 {
		return 0, 0, errors.New("amplitude spectrum requires samples")
	}
	if maxNFFT < 1 {
		return 0, 0, errors.New("maximum FFT length must be positive")
	}
	if policy == FFTLengthCeil {
		if sampleCount > maxNFFT {
			return 0, 0, fmt.Errorf("%d samples exceed maximum FFT length %d", sampleCount, maxNFFT)
		}
		nfft = 1
		for nfft < sampleCount {
			nfft <<= 1
		}
		if nfft > maxNFFT {
			return 0, 0, fmt.Errorf("next power-of-two FFT length %d exceeds maximum %d", nfft, maxNFFT)
		}
		return nfft, sampleCount, nil
	}
	available := sampleCount
	if available > maxNFFT {
		available = maxNFFT
	}
	nfft = 1
	for nfft<<1 <= available {
		nfft <<= 1
	}
	return nfft, nfft, nil
}

func fftRadix2(values []complex128) {
	n := len(values)
	position := 0
	for index := 1; index < n; index++ {
		bit := n >> 1
		for ; position&bit != 0; bit >>= 1 {
			position ^= bit
		}
		position ^= bit
		if index < position {
			values[index], values[position] = values[position], values[index]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		angle := -2 * math.Pi / float64(length)
		step := complex(math.Cos(angle), math.Sin(angle))
		for begin := 0; begin < n; begin += length {
			rotation := complex(1.0, 0)
			half := length >> 1
			for offset := 0; offset < half; offset++ {
				even := values[begin+offset]
				odd := values[begin+offset+half] * rotation
				values[begin+offset] = even + odd
				values[begin+offset+half] = even - odd
				rotation *= step
			}
		}
	}
}
