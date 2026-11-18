package segy

import (
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func phase9IBMWord(value float64) uint32 {
	if value == 0 {
		return 0
	}
	sign := uint32(0)
	if value < 0 {
		sign = 0x80000000
		value = -value
	}
	exponent := 64
	for value < 1.0/16.0 {
		value *= 16
		exponent--
	}
	for value >= 1 {
		value /= 16
		exponent++
	}
	mantissa := uint32(math.Round(value * float64(uint32(1)<<24)))
	if mantissa >= 1<<24 {
		mantissa >>= 4
		exponent++
	}
	return sign | uint32(exponent&0x7f)<<24 | mantissa&0x00ffffff
}

func writePhase9FormatFixture(t *testing.T, format int) string {
	t.Helper()
	const traces, samples = 48, 64
	path := filepath.Join(t.TempDir(), "coalesced.sgy")
	header := make([]byte, 3600)
	binary.BigEndian.PutUint16(header[3216:3218], 2000)
	binary.BigEndian.PutUint16(header[3220:3222], samples)
	binary.BigEndian.PutUint16(header[3224:3226], uint16(format))
	bps := bytesPerSample(format)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(header); err != nil {
		t.Fatal(err)
	}
	for trace := 0; trace < traces; trace++ {
		raw := make([]byte, 240+samples*bps)
		for sample := 0; sample < samples; sample++ {
			value := float64((trace-20)*97 + sample*3 - 41)
			offset := 240 + sample*bps
			switch format {
			case 1:
				binary.BigEndian.PutUint32(raw[offset:offset+4], phase9IBMWord(value/17.0))
			case 2:
				binary.BigEndian.PutUint32(raw[offset:offset+4], uint32(int32(value)))
			case 5:
				binary.BigEndian.PutUint32(raw[offset:offset+4], math.Float32bits(float32(value/13.0)))
			default:
				t.Fatalf("unsupported fixture format %d", format)
			}
		}
		if _, err := f.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPhase9ExactOrderSelection(t *testing.T) {
	random := rand.New(rand.NewSource(91))
	values := make([]float64, 513)
	for i := range values {
		values[i] = float64(random.Intn(47)-23) / 7
	}
	want := append([]float64(nil), values...)
	sort.Float64s(want)
	for k := range want {
		got := selectFloat64Order(append([]float64(nil), values...), k)
		if got != want[k] {
			t.Fatalf("order statistic %d changed: got=%g want=%g", k, got, want[k])
		}
	}
}

func TestPhase9CoalescedRenderMatchesLegacyPixels(t *testing.T) {
	for _, format := range []int{1, 2, 5} {
		t.Run(map[int]string{1: "ibm", 2: "int32", 5: "ieee"}[format], func(t *testing.T) {
			file, err := Open(writePhase9FormatFixture(t, format))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			indices, positions := make([]int64, 48), make([]float64, 48)
			for i := range indices {
				indices[i], positions[i] = int64(i), float64(i)+float64(i*i)/37
			}
			for _, options := range []RenderOptions{
				{Width: 37, Height: 51, GainPercent: 0, ClipPercent: 99, SampleStart: 3, SampleEnd: 59, DisplayMode: DisplayNearest, Workers: 2},
				{Width: 73, Height: 79, GainPercent: 17, ClipPercent: 99, AGC: true, SampleStart: 2, SampleEnd: 61, DisplayMode: DisplaySmooth, Workers: 2},
				{Width: 15, Height: 43, GainPercent: 49, ClipPercent: 99, SampleStart: 4, SampleEnd: 55, DisplayMode: DisplayNearest, Workers: 4},
			} {
				legacy, legacyStats, err := file.RenderTracePositions(indices, positions, positions[2], positions[45], options)
				if err != nil {
					t.Fatal(err)
				}
				options.ReadStrategy = ReadStrategyCoalesced
				fast, fastStats, err := file.RenderTracePositions(indices, positions, positions[2], positions[45], options)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(fast, legacy) {
					t.Fatalf("format %d coalesced pixels changed", format)
				}
				if !fastStats.CoalescedIO || fastStats.IOReadCalls <= 0 || fastStats.IOReadCalls >= 44 {
					t.Fatalf("format %d did not reduce ReadAt calls: %+v", format, fastStats)
				}
				if fastStats.IOCoalescedBlocks != fastStats.IOReadCalls {
					t.Fatalf("format %d block/read statistics diverged: %+v", format, fastStats)
				}
				fastStats.IOReadCalls, fastStats.IOCoalescedBlocks, fastStats.IOReadBytes, fastStats.CoalescedIO = 0, 0, 0, false
				if fastStats != legacyStats {
					t.Fatalf("format %d render stats changed:\nlegacy=%+v\n fast=%+v", format, legacyStats, fastStats)
				}
			}
		})
	}
}

func TestPhase11NarrowTimeWindowDoesNotReadDiscardedTraceGaps(t *testing.T) {
	file, err := Open(writePhase9FormatFixture(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	traces := make([]int64, 48)
	for i := range traces {
		traces[i] = int64(i)
	}
	_, fullStats, err := file.coalescedTraceWindows(traces, 0, 63, 4)
	if err != nil {
		t.Fatal(err)
	}
	_, narrowStats, err := file.coalescedTraceWindows(traces, 16, 31, 4)
	if err != nil {
		t.Fatal(err)
	}
	if narrowStats.readBytes*100 > fullStats.readBytes*35 {
		t.Fatalf("25%% time window read too many bytes: narrow=%d full=%d", narrowStats.readBytes, fullStats.readBytes)
	}
}

func TestPhase9GainZeroUsesObservedEndpoints(t *testing.T) {
	values := []float64{-1000, -2, -1, 0, 1, 2, 900}
	_, low, high := mapRenderValues(values, -1000, 900, RenderOptions{GainPercent: 0, ClipPercent: 99})
	if low != -1000 || high != 900 {
		t.Fatalf("gain zero did not use the already observed endpoints: [%g,%g]", low, high)
	}
}
