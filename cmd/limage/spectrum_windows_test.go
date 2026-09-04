//go:build windows

package main

import (
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func legacySpectrumFFTRadix2(values []complex128) {
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

func legacyMeanSpectrumReference(file *segy.File, traces []int64, sampleStart, sampleEnd int) (spectrumCurve, error) {
	if sampleStart < 0 {
		sampleStart = 0
	}
	if sampleEnd < 0 || sampleEnd >= file.Info.SamplesPerTrace {
		sampleEnd = file.Info.SamplesPerTrace - 1
	}
	nfft := nextPow2LE(sampleEnd - sampleStart + 1)
	sampleEnd = sampleStart + nfft - 1
	traces = evenlySampleTraces(traces, 96)
	accumulated := make([]float64, nfft/2+1)
	used := 0
	for _, trace := range traces {
		samples, err := file.ReadTraceWindow(trace, sampleStart, sampleEnd)
		if err != nil {
			continue
		}
		mean := 0.0
		for _, value := range samples {
			mean += value
		}
		mean /= float64(len(samples))
		fft := make([]complex128, nfft)
		for index, value := range samples {
			window := 0.5 - 0.5*math.Cos(2*math.Pi*float64(index)/float64(nfft-1))
			fft[index] = complex((value-mean)*window, 0)
		}
		legacySpectrumFFTRadix2(fft)
		for bin := range accumulated {
			accumulated[bin] += math.Hypot(real(fft[bin]), imag(fft[bin]))
		}
		used++
	}
	maximum := 0.0
	peakBin := 1
	for bin := 1; bin < len(accumulated); bin++ {
		accumulated[bin] /= float64(used)
		if accumulated[bin] > maximum {
			maximum = accumulated[bin]
			peakBin = bin
		}
	}
	if maximum <= 0 {
		maximum = 1
	}
	frequency := make([]float64, len(accumulated))
	db := make([]float64, len(accumulated))
	dt := float64(file.Info.SampleIntervalUS) * 1e-6
	for bin, value := range accumulated {
		frequency[bin] = float64(bin) / (float64(nfft) * dt)
		ratio := value / maximum
		if ratio < 1e-6 {
			ratio = 1e-6
		}
		db[bin] = 20 * math.Log10(ratio)
		if db[bin] < -80 {
			db[bin] = -80
		}
	}
	return spectrumCurve{
		Freq: frequency, DB: db,
		PeakHz:     float64(peakBin) / (float64(nfft) * dt),
		TraceCount: used, NFFT: nfft,
	}, nil
}

func TestMeanSpectrumRefactorPreservesLegacyCurveAndLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mean-spectrum.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 120, Samples: 5000}); err != nil {
		t.Fatal(err)
	}
	file, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	traces := make([]int64, 120)
	for index := range traces {
		traces[index] = int64(index)
	}
	const sampleStart, sampleEnd = 37, 4800
	want, err := legacyMeanSpectrumReference(file, traces, sampleStart, sampleEnd)
	if err != nil {
		t.Fatal(err)
	}
	got, err := meanSpectrum(file, traces, sampleStart, sampleEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mean-spectrum refactor changed the legacy curve:\n got=%+v\nwant=%+v", got, want)
	}
	if got.TraceCount != 96 || got.NFFT != 4096 {
		t.Fatalf("legacy trace/NFFT limits changed: %+v", got)
	}
	if nyquist := got.Freq[len(got.Freq)-1]; nyquist != 250 {
		t.Fatalf("physical Nyquist changed: %g", nyquist)
	}
}
