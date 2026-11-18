package segy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestNumericalLineTracesKeepDistinctPhysicalTracesAtDuplicateGeometry(t *testing.T) {
	g := &GeometryIndex{InlineValues: []int32{100}, CrosslineValues: []int32{9, 11}, TraceNumbers: []int64{0, 1, 2},
		RowOfTrace: []int32{0, 0, 0}, ColOfTrace: []int32{0, 0, 1}, DuplicateCount: 1}
	render, _, _, err := g.LineTraceNumbers(0, 100)
	if err != nil || !reflect.DeepEqual(render, []int64{0, 2}) {
		t.Fatalf("render representatives: %v %v", render, err)
	}
	got, c, _, err := g.NumericLineTraceNumbers(0, 100)
	if err != nil || !reflect.DeepEqual(got, []int64{0, 1, 2}) || !reflect.DeepEqual(c, []int32{9, 9, 11}) {
		t.Fatalf("numeric traces dropped or reordered: %v %v %v", got, c, err)
	}
}

func TestNumericalExportVerifySingleSampleNaNPrecisionAndCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "A.sgy")
	makeExportSynthetic(t, path, 0)
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	opt := NumericalSectionExportOptions{Traces: []NumericalExportTrace{{4, 5}, {1, 5}}, SampleCount: 1,
		Provenance: "A-B; baseline; A=A.sgy; B=B.sgy",
		ReadValues: func(i int) ([]float64, error) {
			return [][]float64{{1.0000000000000002}, {math.Inf(1)}}[i], nil
		}}
	out := filepath.Join(dir, "out.sgy")
	if err = a.ExportNumericalSection(context.Background(), out, opt); err != nil {
		t.Fatal(err)
	}
	b, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Info.SamplesPerTrace != 1 || b.Info.TraceCount != 2 || b.Info.FormatCode != 6 || b.Info.ExtendedTextHeaders != a.Info.ExtendedTextHeaders+1 {
		t.Fatalf("wrong reopened numeric export dimensions: %+v", b.Info)
	}
	for i, row := range opt.Traces {
		src, e := a.ReadTraceHeader(row.Trace)
		dst, e2 := b.ReadTraceHeader(int64(i))
		val, e3 := b.ReadSample(int64(i), 0)
		if e != nil || e2 != nil || e3 != nil || src.Inline != dst.Inline || src.Crossline != dst.Crossline ||
			dst.DelayMS != src.DelayMS+5*a.Info.SampleIntervalUS/1000 ||
			(i == 0 && val != 1.0000000000000002) || (i == 1 && !math.IsNaN(val)) {
			t.Fatalf("row %d got %g / %+v: %v %v %v", i, val, dst, e, e2, e3)
		}
	}
	if err = a.ExportNumericalSection(nil, out, opt); !errors.Is(err, os.ErrExist) {
		t.Fatalf("unexpected no-overwrite error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opt.Progress = func(done, total int) { cancel() }
	cancelled := filepath.Join(dir, "cancel.sgy")
	if err = a.ExportNumericalSection(ctx, cancelled, opt); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation failed: %v", err)
	}
	if _, err = os.Stat(cancelled); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled file was published")
	}
	if tmp, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(tmp) != 0 {
		t.Fatalf("incomplete export leaked: %v", tmp)
	}
}

func makeNumericalCustomGeometry(t *testing.T, path string, legacy bool) {
	t.Helper()
	const ns, nt = 351, 2273
	header := make([]byte, 3600)
	binary.BigEndian.PutUint16(header[3216:], 2000)
	binary.BigEndian.PutUint16(header[3220:], ns)
	binary.BigEndian.PutUint16(header[3224:], 5)
	if legacy {
		var descriptions = []string{
			"A; scope 0; filters baseline; A=源文件_real.sgy; B=.; file version 1/0; dt=2000 us",
			"A; scope 1; filters baseline; A=源文件_real_A_view_visible.sgy; B=.; file version 1/0; dt=2000 us",
		}
		binary.BigEndian.PutUint16(header[3504:], uint16(len(descriptions)))
		for _, details := range descriptions {
			block := bytes.Repeat([]byte(" "), 3200)
			raw := numericalLegacyTag + strconv.QuoteToASCII(details)
			for i := 0; len(raw) != 0; i++ {
				n := min(76, len(raw))
				copy(block[i*80:], fmt.Sprintf("C%2d %s", i+1, raw[:n]))
				raw = raw[n:]
			}
			header = append(header, block...)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(header); err != nil {
		t.Fatal(err)
	}
	for tr := 0; tr < nt; tr++ {
		row := make([]byte, 240+ns*4)
		binary.BigEndian.PutUint32(row[0:], uint32(tr+2)) // trace sequence, emphatically not XL
		binary.BigEndian.PutUint32(row[20:], uint32(tr+184))
		binary.BigEndian.PutUint16(row[108:], 800)
		binary.BigEndian.PutUint16(row[114:], ns)
		binary.BigEndian.PutUint16(row[116:], 2000)
		binary.BigEndian.PutUint32(row[188:], 1870)
		if _, err := f.Write(row); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNumericalExportRestoresCustomGeometryAndOriginalStem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy_A_view_visible_A_view_manual.sgy")
	makeNumericalCustomGeometry(t, path, true)
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.NumericalOriginalStem() != "源文件_real" {
		t.Fatalf("legacy root: %q", a.NumericalOriginalStem())
	}
	legacyMapping, err := a.DetectPoststackSectionGeometryBytes(3072)
	if err != nil || legacyMapping.InlineByte != 189 || legacyMapping.CrosslineByte != 21 {
		t.Fatalf("legacy IL/XL: %d/%d, error %v; %+v", legacyMapping.InlineByte, legacyMapping.CrosslineByte, err, legacyMapping.Candidates)
	}
	traces := make([]NumericalExportTrace, int(a.Info.TraceCount))
	for i := range traces {
		traces[i] = NumericalExportTrace{Trace: int64(i), First: 50}
	}
	out := filepath.Join(dir, "new_export.sgy")
	o := NumericalSectionExportOptions{Traces: traces, SampleCount: 301, GeometryInlineByte: 189, GeometryCrosslineByte: 21,
		ReadValues: func(i int) ([]float64, error) { return a.ReadTraceWindow(int64(i), 50, 350) }}
	if err := a.ExportNumericalSection(nil, out, o); err != nil {
		t.Fatal(err)
	}
	b, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.NumericalOriginalStem() != "源文件_real" || b.Info.SamplesPerTrace != 301 || b.Info.SampleIntervalUS != 2000 || b.Info.TraceCount != 2273 {
		t.Fatalf("root=%q, reopened info=%+v", b.NumericalOriginalStem(), b.Info)
	}
	m, err := b.DetectPoststackSectionGeometryBytes(2)
	if err != nil || m.InlineByte != 189 || m.CrosslineByte != 21 || m.LabelsHeuristic {
		t.Fatalf("explicit mapping was not stored: %+v, %v", m, err)
	}
	g, _, err := b.BuildPoststackSectionGeometryIndexContextProgress(context.Background(), m.InlineByte, m.CrosslineByte, 0, nil)
	if err != nil || len(g.CrosslineValues) != 2273 || g.CrosslineValues[0] != 184 || g.CrosslineValues[len(g.CrosslineValues)-1] != 2456 {
		t.Fatalf("reopened line did not recover 184–2456: %v", err)
	}
	for i := range traces {
		original, e := a.rawTraceHeader(int64(i))
		actual, e2 := b.rawTraceHeader(int64(i))
		if e != nil || e2 != nil || traceI32(actual[0:4], Big) != int32(i+2) ||
			traceI32(actual[20:24], Big) != int32(i+184) || traceI32(actual[188:192], Big) != 1870 ||
			u16(actual[114:116], Big) != 301 || u16(actual[116:118], Big) != 2000 || u16(actual[108:110], Big) != 900 {
			t.Fatalf("trace %d lost source identity, geometry or sampling", i)
		}
		for _, keep := range [][2]int{{0, 108}, {110, 114}, {118, 240}} {
			if !bytes.Equal(actual[keep[0]:keep[1]], original[keep[0]:keep[1]]) {
				t.Fatalf("trace %d non-sampling headers changed at %v", i, keep)
			}
		}
	}
}

func TestNumericalExportVerifyActualLegacyGeometry(t *testing.T) {
	path := os.Getenv("SEISFORGE_REAL_LEGACY_EXPORT_TEST")
	if path == "" {
		t.Skip("optional real legacy SEG-Y path not provided")
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old, oldErr := f.DetectGeometryBytes(3072)
	m, err := f.DetectPoststackSectionGeometryBytes(3072)
	t.Logf("old detector returned %d/%d (%v), new returned %d/%d (%v); recovered stem %q", old.InlineByte, old.CrosslineByte, oldErr, m.InlineByte, m.CrosslineByte, err, f.NumericalOriginalStem())
	if err != nil || m.InlineByte != 189 || m.CrosslineByte != 21 || f.Info.TraceCount != 2273 {
		t.Fatal("real legacy IL/XL not restored")
	}
	first, err := f.rawTraceHeader(0)
	last, err2 := f.rawTraceHeader(f.Info.TraceCount - 1)
	if err != nil || err2 != nil || traceI32(first[20:24], f.Info.Endian) != 184 || traceI32(last[20:24], f.Info.Endian) != 2456 {
		t.Fatalf("real source header bounds did not match: %v %v", err, err2)
	}
	if original := os.Getenv("SEISFORGE_REAL_LEGACY_EXPORT_SOURCE_TEST"); original != "" {
		a, err := Open(original)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		if a.Info.TraceCount != f.Info.TraceCount || a.Info.SampleIntervalUS != f.Info.SampleIntervalUS {
			t.Fatal("trace count or dt changed in the legacy export")
		}
		for i := int64(0); i < a.Info.TraceCount; i++ {
			src, e1 := a.rawTraceHeader(i)
			dst, e2 := f.rawTraceHeader(i)
			if e1 != nil || e2 != nil || !bytes.Equal(src[0:4], dst[0:4]) || !bytes.Equal(src[20:24], dst[20:24]) || !bytes.Equal(src[188:192], dst[188:192]) {
				t.Fatalf("legacy trace %d changed source identity or geometry: %v %v", i+1, e1, e2)
			}
		}
		t.Logf("compared all %d source/output trace sequence, IL and XL words; dt %d µs, output ns %d",
			a.Info.TraceCount, f.Info.SampleIntervalUS, f.Info.SamplesPerTrace)
	}
}

func TestNumericalExportConfirmedTargetAndSourceProtection(t *testing.T) {
	dir := t.TempDir()
	path, target := filepath.Join(dir, "A.sgy"), filepath.Join(dir, "output.sgy")
	makeExportSynthetic(t, path, 0)
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	o := NumericalSectionExportOptions{Traces: []NumericalExportTrace{{0, 0}}, SampleCount: 1,
		ReadValues: func(i int) ([]float64, error) { return []float64{9}, nil }}
	if err := os.WriteFile(target, []byte("old output"), 0600); err != nil {
		t.Fatal(err)
	}
	o.ReplaceExisting, _ = os.Lstat(target)
	if err := os.WriteFile(target, []byte("changed after prompt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.ExportNumericalSection(nil, target, o); err == nil {
		t.Fatal("replaced a changed destination")
	}
	o.ReplaceExisting, _ = os.Lstat(target)
	if err := a.ExportNumericalSection(nil, target, o); err != nil {
		t.Fatal(err)
	}
	out, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if sample, err := out.ReadSample(0, 0); err != nil || sample != 9 {
		t.Fatalf("replacement result %g %v", sample, err)
	}
	alias := filepath.Join(dir, "alias.sgy")
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	o.ReplaceExisting, _ = os.Lstat(alias)
	if err := a.ExportNumericalSection(nil, alias, o); err == nil {
		t.Fatal("allowed replacement of an alias of the source")
	}
}

func TestNumericalExportTimeIncludesExactEndpointsOnly(t *testing.T) {
	for _, v := range []struct {
		lo, hi      float64
		first, last int
		ok          bool
	}{
		{math.Nextafter(800000, math.Inf(1)), math.Nextafter(1500000, math.Inf(-1)), 0, 350, true},
		{1086000, 1176000, 143, 188, true},
		{1086000, 1086000, 143, 143, true},
		{1086000.01, 1087999.99, 0, -1, false},
		{1100, 700000, 0, -1, false},
	} {
		lo, hi, ok := ExportTimeIndices(v.lo, v.hi, 800000, 2000, 351)
		if lo != v.first || hi != v.last || ok != v.ok {
			t.Errorf("%+v got %d–%d %t", v, lo, hi, ok)
		}
	}
}
