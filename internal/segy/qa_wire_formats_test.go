package segy

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The wire bytes and expected values are fixed reference pairs, rather than
// using any production encoder, decoder or bytes-per-sample implementation.
type qaWireRecord struct {
	code, width int
	words       []string
	values      []float64
}

func qaWireFile(t *testing.T, order binary.ByteOrder, f qaWireRecord, ext int16) []byte {
	t.Helper()
	const ns, dt = 3, 2000
	raw := bytes.Repeat([]byte{' '}, 3200)
	copy(raw, "C 1 SEISFORGE QA WIRE FORMAT FIXTURE")
	bin := make([]byte, 400)
	order.PutUint16(bin[16:18], dt)
	order.PutUint16(bin[20:22], ns)
	order.PutUint16(bin[24:26], uint16(f.code))
	order.PutUint16(bin[302:304], 1)
	order.PutUint16(bin[304:306], uint16(ext))
	raw = append(raw, bin...)
	if ext == 1 {
		extra := bytes.Repeat([]byte{' '}, 3200)
		copy(extra, "C 1 SEISFORGE QA EXTENDED TEXT")
		raw = append(raw, extra...)
	}
	for tr := 0; tr < 2; tr++ {
		header := make([]byte, 240)
		order.PutUint32(header[0:4], uint32(17+tr))
		order.PutUint32(header[20:24], uint32(184+7*tr))
		order.PutUint16(header[108:110], uint16(800+4*tr))
		order.PutUint16(header[114:116], ns)
		order.PutUint16(header[116:118], dt)
		order.PutUint32(header[188:192], 1870)
		order.PutUint32(header[192:196], uint32(901-7*tr))
		raw = append(raw, header...)
		for k := 0; k < ns; k++ {
			j := (k + tr) % ns
			word, err := hex.DecodeString(f.words[j])
			if err != nil || len(word) != f.width {
				t.Fatalf("bad QA reference word %q: %v", f.words[j], err)
			}
			if order == binary.LittleEndian {
				slices.Reverse(word)
			}
			raw = append(raw, word...)
		}
	}
	return raw
}

func TestQASEGYWireFormatsBothEndians(t *testing.T) {
	formats := []qaWireRecord{
		{1, 4, []string{"C1200000", "00000000", "40800000"}, []float64{-2, 0, .5}},
		{2, 4, []string{"80000000", "00000000", "7fffffff"}, []float64{-2147483648, 0, 2147483647}},
		{3, 2, []string{"8000", "0000", "7fff"}, []float64{-32768, 0, 32767}},
		{5, 4, []string{"bfc00000", "80000000", "40200000"}, []float64{-1.5, math.Copysign(0, -1), 2.5}},
		{6, 8, []string{"bff8000000000000", "8000000000000000", "3ff0000000000001"}, []float64{-1.5, math.Copysign(0, -1), 1.0000000000000002}},
		{7, 3, []string{"800000", "000000", "7fffff"}, []float64{-8388608, 0, 8388607}},
		{8, 1, []string{"80", "00", "7f"}, []float64{-128, 0, 127}},
		{9, 8, []string{"ffe0000000000000", "0000000000000000", "001fffffffffffff"}, []float64{-9007199254740992, 0, 9007199254740991}},
		{10, 4, []string{"00000000", "80000000", "ffffffff"}, []float64{0, 2147483648, 4294967295}},
		{11, 2, []string{"0000", "8000", "ffff"}, []float64{0, 32768, 65535}},
		{12, 8, []string{"0000000000000000", "001fffffffffffff", "0020000000000000"}, []float64{0, 9007199254740991, 9007199254740992}},
		{15, 3, []string{"000000", "800000", "ffffff"}, []float64{0, 8388608, 16777215}},
		{16, 1, []string{"00", "80", "ff"}, []float64{0, 128, 255}},
	}
	for _, order := range []binary.ByteOrder{binary.BigEndian, binary.LittleEndian} {
		for _, spec := range formats {
			t.Run(fmt.Sprintf("%s/code%d", order, spec.code), func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "地震 输入")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, strings.Repeat("wide-", 22)+"数据 档案.segy")
				wire := qaWireFile(t, order, spec, 1)
				if err := os.WriteFile(path, wire, 0600); err != nil {
					t.Fatal(err)
				}
				f, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				wantEndian := Big
				if order == binary.LittleEndian {
					wantEndian = Little
				}
				if i := f.Info; i.Endian != wantEndian || i.TraceCount != 2 || i.SamplesPerTrace != 3 || i.SampleIntervalUS != 2000 || i.DataStart != 6800 || i.FormatCode != spec.code || i.BytesPerSample != spec.width {
					t.Fatalf("incorrect binary metadata: %+v", i)
				}
				texts, err := f.ReadTextHeaders()
				if err != nil || len(texts) != 2 || !strings.HasPrefix(texts[1].Lines[0], "C 1 SEISFORGE QA EXTENDED TEXT") {
					t.Fatalf("extended textual header not read: %d headers, %v", len(texts), err)
				}
				for tr := 0; tr < 2; tr++ {
					h, err := f.ReadTraceHeader(int64(tr))
					if err != nil || h.Inline != 1870 || h.Crossline != int32(901-7*tr) || h.CDP != int32(184+7*tr) || h.DelayMS != 800+4*tr {
						t.Fatalf("trace %d unexpected geometry: standard %d/%d customXL=%d delay=%d: %v", tr, h.Inline, h.Crossline, h.CDP, h.DelayMS, err)
					}
					values, err := f.ReadTraceWindow(int64(tr), 0, 2)
					if err != nil || len(values) != 3 {
						t.Fatalf("trace %d read failed: %v / %v", tr, values, err)
					}
					for k, v := range values {
						want := spec.values[(k+tr)%3]
						single, err := f.ReadSample(int64(tr), k)
						if err != nil || math.Float64bits(v) != math.Float64bits(want) || math.Float64bits(single) != math.Float64bits(want) {
							t.Fatalf("trace %d sample %d got %g / %g, want %g: %v", tr, k, v, single, want, err)
						}
					}
				}
			})
		}
	}
}

func TestQASEGYShortAndUnsupportedInput(t *testing.T) {
	f := qaWireRecord{5, 4, []string{"bf800000", "00000000", "3f800000"}, []float64{-1, 0, 1}}
	valid := qaWireFile(t, binary.BigEndian, f, 0)
	unknown := qaWireFile(t, binary.BigEndian, qaWireRecord{14, 4, f.words, nil}, 0)
	impossibleExt := qaWireFile(t, binary.BigEndian, f, 10000)
	for _, tt := range []struct {
		name string
		data []byte
		msg  string
	}{
		{"empty", nil, "too small"},
		{"short-header", valid[:3599], "too small"},
		{"incomplete-first-trace", valid[:3600+240+11], "no complete traces"},
		{"unknown-format-14", unknown, "binary header"},
		{"declared-extensions-beyond-end", impossibleExt, "header"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tt.name+".sgy")
			if err := os.WriteFile(path, tt.data, 0600); err != nil {
				t.Fatal(err)
			}
			r, err := Open(path)
			if r != nil {
				r.Close()
			}
			if err == nil || !strings.Contains(err.Error(), tt.msg) {
				t.Fatalf("expected a clear %q rejection, got %v", tt.msg, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "format4.sgy")
	headerOnly := qaWireFile(t, binary.BigEndian, qaWireRecord{4, 4, f.words, nil}, 0)
	if err := os.WriteFile(path, headerOnly, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if h, err := r.ReadTraceHeader(1); err != nil || h.Inline != 1870 {
		t.Fatalf("known header-only format must still expose its headers: %+v / %v", h, err)
	}
	if v, err := r.ReadSample(0, 1); err == nil || !strings.Contains(err.Error(), "format 4") {
		t.Errorf("format 4 sample: expected unsupported error, got %g / %v", v, err)
	}
	if values, err := r.ReadTraceWindow(0, 0, 2); err == nil || !strings.Contains(err.Error(), "format 4") {
		t.Errorf("format 4 window: expected unsupported error, got %v / %v", values, err)
	}
	if p, err := r.Render(4, 5, false, 99); err == nil || !strings.Contains(err.Error(), "format 4") {
		t.Errorf("format 4 render: expected rejection rather than synthetic pixels, got %v / %v", p, err)
	}
	for _, strategy := range []RenderReadStrategy{ReadStrategyDefault, ReadStrategyCoalesced, ReadStrategySparseMapped} {
		opts := RenderOptions{Width: 4, Height: 5, SampleEnd: -1, ReadStrategy: strategy}
		if p, _, err := r.RenderTraceIndices([]int64{0, 1}, opts); err == nil || !strings.Contains(err.Error(), "format 4") {
			t.Errorf("format 4 indexed render strategy %d: got %v / %v", strategy, p, err)
		}
		if p, _, err := r.RenderTracePositions([]int64{0, 1}, []float64{184, 191}, 184, 191, opts); err == nil || !strings.Contains(err.Error(), "format 4") {
			t.Errorf("format 4 nonuniform render strategy %d: got %v / %v", strategy, p, err)
		}
	}
	cache := NewTimeSliceCache(r, &GeometryIndex{TraceNumbers: []int64{0, 1}})
	if v, _, err := cache.GetSlice(1); err == nil || !strings.Contains(err.Error(), "format 4") {
		t.Errorf("format 4 time slice: got %v / %v", v, err)
	}
}
