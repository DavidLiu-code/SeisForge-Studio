package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func metadata(dtype string, three bool) Metadata {
	m := Metadata{DType: dtype, Shape: []uint64{2, 5}, Axes: []string{"trace", "sample"}, SampleIntervalUS: 2000, TimeOriginUS: -2000, SampleStart: 3}
	if three {
		m.Shape = []uint64{1, 2, 5}
		m.Axes = []string{"inline", "crossline", "sample"}
	}
	return m
}
func geometry() []TraceMetadata {
	return []TraceMetadata{{TraceID: 42, Inline: 1870, Crossline: 21, X: 0, Y: 0, TimeOriginUS: -2000, CoordinateValid: true}, {TraceID: 7, Inline: 1870, Crossline: 22, X: -3.5, Y: 21.25, TimeOriginUS: -2000, CoordinateValid: true}}
}
func samples(i int64) ([]float64, []bool, error) {
	return []float64{1 + float64(i), -2.5, math.Pi, 0, math.NaN()}, []bool{true, true, true, true, false}, nil
}
func TestRoundTripWindowPrecisionAndDefensiveCopies(t *testing.T) {
	for _, dtype := range []string{Float32, Float64} {
		for _, three := range []bool{false, true} {
			t.Run(dtype+map[bool]string{true: "3d", false: "2d"}[three], func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "test.sfdata")
				m := metadata(dtype, three)
				if err := Write(context.Background(), path, m, geometry(), samples); err != nil {
					t.Fatal(err)
				}
				f, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if !reflect.DeepEqual(f.Metadata(), m) || !reflect.DeepEqual(f.TraceMetadata(), geometry()) {
					t.Fatal("metadata roundtrip changed")
				}
				v, valid, err := f.ReadTraceWindow(1, 1, 5)
				if err != nil {
					t.Fatal(err)
				}
				pi := math.Pi
				if dtype == Float32 {
					pi = float64(float32(pi))
				}
				if !reflect.DeepEqual(v, []float64{-2.5, pi, 0, 0}) || !reflect.DeepEqual(valid, []bool{true, true, true, false}) {
					t.Fatalf("values %v mask %v", v, valid)
				}
				if len(f.Fingerprint()) != 64 {
					t.Fatal("missing fingerprint")
				}
				c := f.Metadata()
				c.Shape[0] = 99
				g := f.TraceMetadata()
				g[0].Inline = 0
				if f.Metadata().Shape[0] == 99 || f.TraceMetadata()[0].Inline == 0 {
					t.Fatal("metadata aliased")
				}
				for _, r := range [][3]int64{{-1, 0, 1}, {2, 0, 1}, {0, -1, 1}, {0, 0, 6}, {0, 3, 2}} {
					if _, _, err = f.ReadTraceWindow(r[0], r[1], r[2]); err == nil {
						t.Fatal("bad window accepted")
					}
				}
			})
		}
	}
}
func TestCorruptionTruncationAndUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "good.sfdata")
	if err := Write(context.Background(), path, metadata(Float64, false), geometry(), samples); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"magic", "version", "metadata", "geometry", "mask", "sample", "truncated", "extra", "length", "nonfinite"} {
		t.Run(kind, func(t *testing.T) {
			b := append([]byte(nil), good...)
			ml := int(binary.LittleEndian.Uint32(b[12:16]))
			geom := headerSize + ml
			mask := geom + 2*traceSize
			data := mask + 10
			switch kind {
			case "magic":
				b[0]++
			case "version":
				binary.LittleEndian.PutUint32(b[8:12], 2)
			case "metadata":
				b[headerSize]++
			case "geometry":
				b[geom]++
			case "mask":
				b[mask]++
			case "sample":
				b[data]++
			case "truncated":
				b = b[:len(b)-1]
			case "extra":
				b = append(b, 0)
			case "length":
				binary.LittleEndian.PutUint64(b[48:56], math.MaxUint64)
			case "nonfinite":
				binary.LittleEndian.PutUint64(b[data:data+8], math.Float64bits(math.Inf(1)))
				s := sha256.Sum256(b[data:])
				copy(b[152:184], s[:])
			}
			bad := filepath.Join(t.TempDir(), "bad.sfdata")
			if err := os.WriteFile(bad, b, 0600); err != nil {
				t.Fatal(err)
			}
			f, err := Open(bad)
			if err == nil {
				f.Close()
				t.Fatal("accepted damaged artifact")
			}
		})
	}
}
func TestValidationCancellationAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.sfdata")
	m := metadata(Float64, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Write(ctx, p, m, geometry(), samples); err == nil {
		t.Fatal("canceled write accepted")
	}
	ctx, cancel = context.WithCancel(context.Background())
	if err := Write(ctx, p, m, geometry(), func(i int64) ([]float64, []bool, error) { cancel(); return samples(i) }); err == nil {
		t.Fatal("mid write cancellation accepted")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("temporary file leaked")
	}
	if err := Write(context.Background(), p, m, geometry(), samples); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	if err := Write(context.Background(), p, m, geometry(), samples); err == nil {
		t.Fatal("overwrote target")
	}
	after, _ := os.ReadFile(p)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("target changed")
	}
	bad := m
	bad.Shape = []uint64{math.MaxUint64, 3}
	if Write(context.Background(), filepath.Join(dir, "bad"), bad, geometry(), samples) == nil {
		t.Fatal("overflow shape accepted")
	}
	bad = m
	bad.Axes = []string{"sample", "trace"}
	if Write(context.Background(), filepath.Join(dir, "bad"), bad, geometry(), samples) == nil {
		t.Fatal("bad axes accepted")
	}
	if Write(context.Background(), filepath.Join(dir, "bad"), m, geometry(), func(i int64) ([]float64, []bool, error) { return []float64{1, 2, 3, 4, math.Inf(1)}, nil, nil }) == nil {
		t.Fatal("nonfinite valid input accepted")
	}
}
func TestCompatibilityIncludesGeometryAndMask(t *testing.T) {
	aPath := filepath.Join(t.TempDir(), "a.sfdata")
	m := metadata(Float64, false)
	if err := Write(context.Background(), aPath, m, geometry(), samples); err != nil {
		t.Fatal(err)
	}
	a, err := Open(aPath)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, kind := range []string{"same", "precision", "axis", "geometry", "trace_id", "origin", "mask"} {
		t.Run(kind, func(t *testing.T) {
			mm := metadata(Float64, false)
			g := geometry()
			r := samples
			switch kind {
			case "precision":
				mm.DType = Float32
			case "axis":
				mm.SampleIntervalUS++
			case "geometry":
				g[0].X++
			case "trace_id":
				g[0].TraceID++
			case "origin":
				g[0].TimeOriginUS++
			case "mask":
				r = func(i int64) ([]float64, []bool, error) { v, mask, e := samples(i); mask[0] = false; return v, mask, e }
			}
			p := filepath.Join(t.TempDir(), "b.sfdata")
			if err := Write(context.Background(), p, mm, g, r); err != nil {
				t.Fatal(err)
			}
			b, err := Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			err = CheckCompatibility(a, b)
			if (kind == "same" || kind == "precision") != (err == nil) {
				t.Fatalf("compatibility %s: %v", kind, err)
			}
		})
	}
}

func TestConcurrentAtomicPublicationAndRepeatedClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "published.sfdata")
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- Write(context.Background(), path, metadata(Float64, false), geometry(), samples)
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("publish winners %d, want exactly one", success)
	}
	for i := 0; i < 100; i++ {
		f, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.ReadTraceWindow(0, 0, 5); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left: %v (%v)", entries, err)
	}
	// Windows disallows removing an open reader: this also catches unclosed handles.
	if err := os.Remove(path); err != nil {
		t.Fatalf("reader handle leaked: %v", err)
	}
}

func TestCorruptSemanticBlocksWithCorrectChecksums(t *testing.T) {
	p := filepath.Join(t.TempDir(), "base.sfdata")
	if err := Write(context.Background(), p, metadata(Float64, false), geometry(), samples); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	ml := int(binary.LittleEndian.Uint32(original[12:16]))
	g, mask := headerSize+ml, headerSize+ml+2*traceSize
	for _, kind := range []string{"coordinate_nan", "flag", "reserved", "mask", "invalid_nonzero"} {
		t.Run(kind, func(t *testing.T) {
			b := append([]byte(nil), original...)
			switch kind {
			case "coordinate_nan":
				binary.LittleEndian.PutUint64(b[g+16:g+24], math.Float64bits(math.NaN()))
			case "flag":
				b[g+40] = 2
			case "reserved":
				b[g+47] = 1
			case "mask":
				b[mask] = 2
			case "invalid_nonzero":
				binary.LittleEndian.PutUint64(b[mask+10+4*8:], math.Float64bits(1))
			}
			gs := sha256.Sum256(b[g:mask])
			copy(b[88:120], gs[:])
			ms := sha256.Sum256(b[mask : mask+10])
			copy(b[120:152], ms[:])
			ds := sha256.Sum256(b[mask+10:])
			copy(b[152:184], ds[:])
			path := filepath.Join(t.TempDir(), "bad.sfdata")
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			f, err := Open(path)
			if err == nil {
				f.Close()
				t.Fatal("invalid semantic block accepted")
			}
		})
	}
}
