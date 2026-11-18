package segy

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type testFloatSectionData struct {
	values [][]float64
	closed int
}

func (s *testFloatSectionData) ReadTraceWindow(trace int64, first, last int) ([]float64, error) {
	if s.closed != 0 {
		return nil, os.ErrClosed
	}
	return append([]float64(nil), s.values[trace][first:last+1]...), nil
}
func (s *testFloatSectionData) Close() error { s.closed++; return nil }

func TestFloatSectionPreservesPrecisionGeometryAndSource(t *testing.T) {
	dir := t.TempDir()
	source, valuePath := filepath.Join(dir, "A.sgy"), filepath.Join(dir, "B.sfdata")
	makeExportSynthetic(t, source, 0)
	before, _ := os.ReadFile(source)
	if err := os.WriteFile(valuePath, []byte("only an identity placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	values := &testFloatSectionData{values: [][]float64{{1.0000000000000002, -1e100, 0, 3.141592653589793}, {9, 8, 7, 6}}}
	b, err := OpenFloatSection(FloatSectionOptions{SourcePath: source, ValuePath: valuePath, TraceIndices: []int64{5, 0}, SampleStart: 2, SampleEnd: 5}, values)
	if err != nil {
		t.Fatal(err)
	}
	if b.Info.FormatCode != 6 || b.Info.BytesPerSample != 8 || b.Info.SamplesPerTrace != 4 || b.Info.TraceCount != 2 {
		t.Fatalf("incorrect float facade metadata: %+v", b.Info)
	}
	for i := range values.values {
		got, err := b.ReadTraceWindow(int64(i), 0, 3)
		if err != nil || !reflect.DeepEqual(got, values.values[i]) {
			t.Fatalf("float64 narrowed: %v, %v", got, err)
		}
	}
	h, err := b.ReadTraceHeader(0)
	if err != nil || h.Inline != 101 || h.Crossline != 202 || h.DelayMS != 4 || h.SampleCount != 4 {
		t.Fatalf("source geometry changed: %+v, %v", h, err)
	}
	if _, ok := b.ensureReadMapping(); ok {
		t.Fatal("virtual artifact must not be OS-mapped as SEG-Y")
	}
	// A coalesced read crosses the reel/header/sample boundaries. Every partial
	// read must be identical to the corresponding complete virtual byte range.
	full := make([]byte, int(b.Info.FileSize))
	if _, err := b.f.ReadAt(full, 0); err != nil {
		t.Fatal(err)
	}
	for _, off := range []int64{3199, 3598, 3601, 3839, 3841, 3869, 3891} {
		part := make([]byte, 23)
		if _, err := b.f.ReadAt(part, off); err != nil || !bytes.Equal(part, full[off:off+23]) {
			t.Fatalf("unaligned virtual read %d: %v", off, err)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil || values.closed != 1 {
		t.Fatal("derived provider was not closed exactly once")
	}
	if _, err := b.ReadTraceWindow(0, 0, 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("read after close succeeded: %v", err)
	}
	after, _ := os.ReadFile(source)
	if !bytes.Equal(before, after) {
		t.Fatal("read-only A was modified")
	}
}

func TestFloatSectionFailedOpenDoesNotTakeOwnership(t *testing.T) {
	data := &testFloatSectionData{}
	if _, err := OpenFloatSection(FloatSectionOptions{SourcePath: "missing", TraceIndices: []int64{0}}, data); err == nil || data.closed != 0 {
		t.Fatal("failed open took source ownership")
	}
}
