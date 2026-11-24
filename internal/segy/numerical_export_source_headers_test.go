package segy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const headerTestSamples = 4

func writeNumericalIBM32HeaderFixture(t *testing.T, path string, order binary.ByteOrder, extended bool) []byte {
	t.Helper()
	reel := make([]byte, 3600)
	copy(reel[:3200], bytes.Repeat([]byte("Synthetic primary header; "), 120))
	order.PutUint16(reel[3216:], 2000)
	order.PutUint16(reel[3218:], 3000) // original acquisition fields are not output dimensions
	order.PutUint16(reel[3220:], headerTestSamples)
	order.PutUint16(reel[3222:], 8)
	order.PutUint16(reel[3224:], 1) // IBM 32-bit
	reel[3500] = 1
	if extended {
		order.PutUint16(reel[3504:], 1)
		reel = append(reel, bytes.Repeat([]byte{' '}, 3200)...)
		copy(reel[3600:], "C 1 Synthetic source extension")
	}
	data := append([]byte(nil), reel...)
	for i := 0; i < 3; i++ {
		trace := make([]byte, 240+headerTestSamples*4)
		order.PutUint32(trace[:], uint32(17+i)) // distinct physical trace identities
		order.PutUint16(trace[108:], 800)       // base Delay ms
		// 0 means the original trace uses its binary-header ns and dt.
		order.PutUint32(trace[188:], uint32(184+i))
		order.PutUint32(trace[192:], 446)
		for j, v := range []uint32{0x41100000, 0xc1200000, 0x41300000, 0xc1400000} {
			order.PutUint32(trace[240+j*4:], v) // [1, -2, 3, -4] in IBM base-16
		}
		data = append(data, trace...)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestNumericalExportSourceHeadersFullAndCrop(t *testing.T) {
	for _, tc := range []struct {
		name     string
		order    binary.ByteOrder
		extended bool
	}{
		{"big-endian-plain", binary.BigEndian, false},
		{"little-endian-extended", binary.LittleEndian, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "source.sgy")
			input := writeNumericalIBM32HeaderFixture(t, src, tc.order, tc.extended)
			a, err := Open(src)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if !a.CanPreserveNumericalExportHeaders(189, 193) || a.CanPreserveNumericalExportHeaders(189, 21) {
				t.Fatal("only the standard geometry mapping may omit metadata")
			}
			n := int(a.Info.DataStart)
			full := filepath.Join(dir, "full.sgy")
			o := NumericalSectionExportOptions{
				Traces: []NumericalExportTrace{{0, 0}, {1, 0}, {2, 0}}, SampleCount: headerTestSamples,
				GeometryInlineByte: 189, GeometryCrosslineByte: 193, PreserveSourceHeaders: true,
				ReadValues: func(i int) ([]float64, error) {
					return []float64{2, -3, float64(7 + i), -4}, nil
				},
			}
			if err := a.ExportNumericalSection(nil, full, o); err != nil {
				t.Fatal(err)
			}
			result, err := os.ReadFile(full)
			if err != nil || len(result) != len(input) {
				t.Fatalf("full output size %d, want %d: %v", len(result), len(input), err)
			}
			if !bytes.Equal(result[:n], input[:n]) {
				t.Fatal("full output changed the source reel headers")
			}
			for i := 0; i < 3; i++ {
				offset := n + i*(240+headerTestSamples*4)
				if !bytes.Equal(result[offset:offset+240], input[offset:offset+240]) {
					t.Fatalf("full output changed trace header %d", i)
				}
				if v := tc.order.Uint32(result[offset+240:]); v != 0x41200000 {
					t.Fatalf("IBM result word %#x, want %#x", v, uint32(0x41200000))
				}
			}

			crop := filepath.Join(dir, "crop.sgy")
			o.Traces, o.SampleCount = []NumericalExportTrace{{1, 1}, {2, 1}}, 2
			o.ReadValues = func(i int) ([]float64, error) {
				return []float64{-3, float64(8 + i)}, nil
			}
			if err := a.ExportNumericalSection(nil, crop, o); err != nil {
				t.Fatal(err)
			}
			b, err := Open(crop)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if b.Info.TraceCount != 2 || b.Info.FileSize != int64(n+2*(240+2*4)) ||
				b.Info.DataStart != int64(n) || b.Info.FormatCode != 1 ||
				b.Info.SamplesPerTrace != 2 || b.Info.SampleIntervalUS != 2000 {
				t.Fatalf("wrong cropped layout: %+v", b.Info)
			}
			cropped, err := os.ReadFile(crop)
			if err != nil {
				t.Fatal(err)
			}
			wantHeaders := append([]byte(nil), input[:n]...)
			tc.order.PutUint16(wantHeaders[3220:], 2)
			if !bytes.Equal(cropped[:n], wantHeaders) {
				t.Fatal("crop changed source reel beyond current sample count")
			}
			for i := range o.Traces {
				head, err := b.ReadTraceHeader(int64(i))
				values, readErr := b.ReadTraceWindow(int64(i), 0, 1)
				if err != nil || readErr != nil || head.Inline != int32(185+i) || head.Crossline != 446 ||
					head.DelayMS != 802 || !reflect.DeepEqual(values, []float64{-3, float64(8 + i)}) {
					t.Fatalf("wrong cropped trace %d: %+v %v %v %v", i, head, values, err, readErr)
				}
			}
			if stored, err := os.ReadFile(src); err != nil || !bytes.Equal(stored, input) {
				t.Fatal("numerical export modified the input")
			}
		})
	}
}

func TestNumericalExportFormatsAndIBMNaNFallback(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request ExportSampleFormat
		source  int
		code    int
		wantErr bool
	}{
		{"inherited-ibm-with-nan", ExportFormatSource, 0, 5, false},
		{"explicit-ibm-with-nan", ExportFormatIBM32, 0, 0, true},
		{"explicit-ieee32", ExportFormatIEEE32, 0, 5, false},
		{"explicit-ieee64", ExportFormatIEEE64, 0, 6, false},
		{"unsupported-source-falls-back", ExportFormatSource, 65537, 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src, dst := filepath.Join(dir, "source.sgy"), filepath.Join(dir, "export.sgy")
			writeNumericalIBM32HeaderFixture(t, src, binary.BigEndian, true)
			a, err := Open(src)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			var progress []int
			o := NumericalSectionExportOptions{
				Traces: []NumericalExportTrace{{0, 0}, {1, 0}}, SampleCount: 1,
				GeometryInlineByte: 189, GeometryCrosslineByte: 193, PreserveSourceHeaders: true,
				SampleFormat: tc.request, SourceFormatCode: tc.source,
				ReadValues: func(i int) ([]float64, error) {
					if i == 0 {
						return []float64{2}, nil
					}
					return []float64{math.NaN()}, nil
				},
				Progress: func(done, total int) { progress = append(progress, done) },
			}
			err = a.ExportNumericalSection(nil, dst, o)
			tmp, _ := filepath.Glob(filepath.Join(dir, ".*.tmp"))
			if len(tmp) != 0 {
				t.Fatalf("export leaked temporary file: %v", tmp)
			}
			if tc.wantErr {
				if _, statErr := os.Stat(dst); !errors.Is(err, errIBM32NonFinite) || !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("expected clean IBM32 rejection, got %v, stat %v", err, statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := Open(dst)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			bps := 4
			if tc.code == 6 {
				bps = 8
			}
			first, readErr := b.ReadSample(0, 0)
			second, readErr2 := b.ReadSample(1, 0)
			if b.Info.FormatCode != tc.code || b.Info.ExtendedTextHeaders != 1 ||
				b.Info.FileSize != int64(6800+2*(240+bps)) || first != 2 || !math.IsNaN(second) ||
				readErr != nil || readErr2 != nil || !reflect.DeepEqual(progress, []int{1, 2}) {
				t.Fatalf("wrong result: %+v samples %g/%g errors %v/%v progress %v", b.Info, first, second, readErr, readErr2, progress)
			}
		})
	}
}

func TestNumericalExportPreservedRev2UpdatesDeclaredDimensions(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "source.sgy"), filepath.Join(dir, "export.sgy")
	order := binary.BigEndian
	input := writeNumericalIBM32HeaderFixture(t, src, order, true)
	bh := input[3200:3600]
	bh[300], bh[301] = 2, 1
	order.PutUint32(bh[68:], headerTestSamples)
	order.PutUint64(bh[72:], math.Float64bits(2000))
	order.PutUint32(bh[96:], 0x01020304)
	order.PutUint64(bh[312:], 3)
	order.PutUint64(bh[320:], 6800)
	if err := os.WriteFile(src, input, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	err = a.ExportNumericalSection(nil, dst, NumericalSectionExportOptions{
		Traces: []NumericalExportTrace{{1, 1}}, SampleCount: 2,
		GeometryInlineByte: 189, GeometryCrosslineByte: 193, PreserveSourceHeaders: true,
		ReadValues: func(int) ([]float64, error) { return []float64{-2, 3}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte(nil), input[:6800]...)
	order.PutUint16(want[3220:], 2)
	order.PutUint32(want[3268:], 2)
	order.PutUint64(want[3512:], 1)
	if len(got) != 6800+240+2*4 || !bytes.Equal(got[:6800], want) {
		t.Fatal("export changed source reel outside the Rev 2 current sample/trace counts")
	}
}
