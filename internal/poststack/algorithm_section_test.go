package poststack

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi/artifact"
)

func algorithmFloat64Fixture(t *testing.T) (string, [][]float64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source-float64.sgy")
	values := [][]float64{{0, 1.0000000000000002, -1e100, math.Pi, 9}, {1, -math.Pi, 1e100, 1e-100, -9}}
	data := make([]byte, 3600+len(values)*(240+len(values[0])*8))
	copy(data, []byte("C 1 Original source; do not alter geometry"))
	binary.BigEndian.PutUint16(data[3216:3218], 2000)
	binary.BigEndian.PutUint16(data[3220:3222], uint16(len(values[0])))
	binary.BigEndian.PutUint16(data[3224:3226], 6)
	for i, trace := range values {
		block := data[3600+i*(240+len(trace)*8):]
		binary.BigEndian.PutUint32(block[0:4], uint32(i+10))
		binary.BigEndian.PutUint32(block[188:192], 1870)
		binary.BigEndian.PutUint32(block[192:196], uint32(i+184))
		binary.BigEndian.PutUint16(block[108:110], 12)
		binary.BigEndian.PutUint16(block[114:116], uint16(len(trace)))
		binary.BigEndian.PutUint16(block[116:118], 2000)
		for j, v := range trace {
			binary.BigEndian.PutUint64(block[240+j*8:], math.Float64bits(v))
		}
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path, values
}

func copyAlgorithmArtifact(t *testing.T, input, output string, alter func(*artifact.Metadata, []artifact.TraceMetadata)) {
	t.Helper()
	f, err := artifact.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, traces := f.Metadata(), f.TraceMetadata()
	if alter != nil {
		alter(&m, traces)
	}
	if err := artifact.Write(context.Background(), output, m, traces, func(i int64) ([]float64, []bool, error) {
		return f.ReadTraceWindow(i, 0, f.SamplesPerTrace())
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAlgorithmSectionCaptureAcceptRenderExportPreservesFloat64(t *testing.T) {
	source, values := algorithmFloat64Fixture(t)
	before, _ := os.ReadFile(source)
	inputPath := filepath.Join(t.TempDir(), "input.sfdata")
	request := SectionRequest{SourcePath: source, TraceIndices: []int64{1, 0}, SampleStart: 1, SampleEnd: 3, AlgorithmID: "example.noop", Direction: "inline"}
	input, err := CaptureAlgorithmSection(context.Background(), request, inputPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.TraceIndices[0] = 0 // caller cannot change the captured host order
	taskDir := t.TempDir()
	output := filepath.Join(taskDir, "output.sfdata")
	copyAlgorithmArtifact(t, inputPath, output, nil)
	result, err := input.AcceptResult(context.Background(), taskDir, output, "")
	if err != nil {
		t.Fatal(err)
	}
	defer result.Cleanup()
	if result.Part1 != "" || result.Part2 != output || result.Metadata.TimeOriginUS != 14000 || !reflect.DeepEqual(result.TraceIndices, []int64{1, 0}) {
		t.Fatalf("result provenance changed: %+v", result)
	}
	b, err := OpenSectionResult(result, output)
	if err != nil {
		t.Fatal(err)
	}
	if b.Info.FormatCode != 6 {
		t.Fatal("artifact was narrowed to float32")
	}
	for logical, physical := range []int64{1, 0} {
		actual, err := b.ReadTraceWindow(int64(logical), 0, 2)
		if err != nil || !reflect.DeepEqual(actual, values[physical][1:4]) {
			t.Fatalf("exact values differ %v: %v", actual, err)
		}
		h, err := b.ReadTraceHeader(int64(logical))
		if err != nil || h.Inline != 1870 || h.Crossline != int32(184+physical) || h.DelayMS != 14 {
			t.Fatalf("A/B geometry or time origin differ: %+v %v", h, err)
		}
	}
	aReader, err := segy.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, strategy := range []segy.RenderReadStrategy{segy.ReadStrategyDefault, segy.ReadStrategyCoalesced, segy.ReadStrategySparseMapped} {
		aOpts := segy.RenderOptions{Width: 2, Height: 3, SampleStart: 1, SampleEnd: 3, ReadStrategy: strategy, DisplayMode: segy.DisplayNearest}
		bOpts := aOpts
		bOpts.SampleStart, bOpts.SampleEnd = 0, 2
		av, _, ae := aReader.RenderTraceIndicesValues([]int64{1, 0}, aOpts)
		bv, _, be := b.RenderTraceIndicesValues([]int64{0, 1}, bOpts)
		if ae != nil || be != nil || !reflect.DeepEqual(av, bv) {
			t.Fatalf("existing renderer changed exact values strategy=%d A=%v B=%v errors=%v/%v", strategy, av, bv, ae, be)
		}
	}
	aReader.Close()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	exported := filepath.Join(t.TempDir(), "B.sgy")
	if err := ExportSectionResult(context.Background(), result, output, exported); err != nil {
		t.Fatal(err)
	}
	reopened, err := segy.Open(exported)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Info.FormatCode != 6 || reopened.Info.Endian != segy.Big || reopened.Info.TraceCount != 2 || reopened.Info.SamplesPerTrace != 3 {
		t.Fatalf("lossy or structurally changed export: %+v", reopened.Info)
	}
	v, err := reopened.ReadTraceWindow(1, 0, 2)
	if err != nil || !reflect.DeepEqual(v, values[0][1:4]) {
		t.Fatalf("export narrowed exact values: %v, %v", v, err)
	}
	reopened.Close()
	if err := ExportSectionResult(context.Background(), result, output, exported); err == nil {
		t.Fatal("existing result was overwritten")
	}
	after, _ := os.ReadFile(source)
	if !bytes.Equal(before, after) {
		t.Fatal("A was modified")
	}
	if err := result.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("accepted artifact leaked")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("A was removed during cleanup")
	}
}

func TestAlgorithmSectionRejectsChangedAxisOrderAndOutsideOutput(t *testing.T) {
	source, _ := algorithmFloat64Fixture(t)
	inputPath := filepath.Join(t.TempDir(), "input.sfdata")
	in, err := CaptureAlgorithmSection(context.Background(), SectionRequest{SourcePath: source, TraceIndices: []int64{0, 1}, SampleStart: 0, SampleEnd: 4}, inputPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, alter := range map[string]func(*artifact.Metadata, []artifact.TraceMetadata){
		"time": func(m *artifact.Metadata, _ []artifact.TraceMetadata) { m.SampleIntervalUS++ },
		"order": func(_ *artifact.Metadata, traces []artifact.TraceMetadata) {
			traces[0], traces[1] = traces[1], traces[0]
		},
		"geometry": func(_ *artifact.Metadata, traces []artifact.TraceMetadata) { traces[0].Inline++ },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "out.sfdata")
			copyAlgorithmArtifact(t, inputPath, out, alter)
			if _, err := in.AcceptResult(context.Background(), dir, out, ""); err == nil {
				t.Fatal("changed axes/order accepted")
			}
			if _, err := os.Stat(out); err != nil {
				t.Fatal("failed acceptance must leave cleanup to runner")
			}
		})
	}
	if _, err := in.AcceptResult(context.Background(), t.TempDir(), inputPath, ""); err == nil {
		t.Fatal("outside output accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CaptureAlgorithmSection(canceled, in.request, filepath.Join(t.TempDir(), "cancel.sfdata"), nil); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled capture accepted")
	}
	if err := os.Chtimes(source, in.identity.ModTime(), in.identity.ModTime().Add(1000000000)); err != nil {
		t.Fatal(err)
	}
	if err := in.SourceUnchanged(); err == nil {
		t.Fatal("changed A accepted")
	}
}

func TestAlgorithmSectionManualGeometryAndFingerprintGate(t *testing.T) {
	source, _ := algorithmFloat64Fixture(t)
	inputPath := filepath.Join(t.TempDir(), "input.sfdata")
	// Byte 1 contains source sequence 10/11, demonstrating an explicit custom
	// Crossline mapping rather than the standard bytes 193-196.
	in, err := CaptureAlgorithmSectionMapped(context.Background(), SectionRequest{SourcePath: source, TraceIndices: []int64{1, 0}, SampleStart: 1, SampleEnd: 3}, inputPath, 189, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := artifact.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	meta, traces := f.Metadata(), f.TraceMetadata()
	f.Close()
	if traces[0].Inline != 1870 || traces[0].Crossline != 11 || traces[1].Crossline != 10 || meta.TimeOriginUS != 12000 || meta.SampleStart != 1 {
		t.Fatalf("manual mapping or exact sample origin lost: %+v %+v", meta, traces)
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "output.sfdata")
	copyAlgorithmArtifact(t, inputPath, output, nil)
	r, err := in.AcceptResult(context.Background(), dir, output, "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cleanup()
	identity, _ := os.Stat(output)
	changed := filepath.Join(t.TempDir(), "changed.sfdata")
	copyAlgorithmArtifact(t, inputPath, changed, func(_ *artifact.Metadata, traces []artifact.TraceMetadata) { traces[0].Crossline++ })
	bytesChanged, _ := os.ReadFile(changed)
	// Keep the same inode, file length and mtime; only the accepted SHA must
	// reject this independently valid but different artifact.
	if err := os.WriteFile(output, bytesChanged, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(output, identity.ModTime(), identity.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSectionResult(r, output); err == nil {
		t.Fatal("changed artifact fingerprint was accepted")
	}
}

func TestAlgorithmSectionOpenReaderRejectsSourceChanges(t *testing.T) {
	source, _ := algorithmFloat64Fixture(t)
	inputPath := filepath.Join(t.TempDir(), "input.sfdata")
	in, err := CaptureAlgorithmSection(context.Background(), SectionRequest{SourcePath: source, TraceIndices: []int64{0}, SampleStart: 0, SampleEnd: 4}, inputPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "output.sfdata")
	copyAlgorithmArtifact(t, inputPath, output, nil)
	r, err := in.AcceptResult(context.Background(), dir, output, "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cleanup()
	b, err := OpenSectionResult(r, output)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := os.Chtimes(source, in.identity.ModTime(), in.identity.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReadTraceWindow(0, 0, 2); err == nil {
		t.Fatal("open reader accepted changed source")
	}
	if _, err := b.ReadTraceHeader(0); err == nil {
		t.Fatal("open reader accepted changed source headers")
	}
}

func TestAlgorithmSectionRetainsFractionalMillisecondOrigin(t *testing.T) {
	source, _ := algorithmFloat64Fixture(t)
	raw, _ := os.ReadFile(source)
	binary.BigEndian.PutUint16(raw[3216:3218], 500)
	for i := 0; i < 2; i++ {
		binary.BigEndian.PutUint16(raw[3600+i*(240+5*8)+116:], 500)
	}
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(t.TempDir(), "input.sfdata")
	in, err := CaptureAlgorithmSection(context.Background(), SectionRequest{SourcePath: source, TraceIndices: []int64{0}, SampleStart: 1, SampleEnd: 3}, inputPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "output.sfdata")
	copyAlgorithmArtifact(t, inputPath, output, nil)
	r, err := in.AcceptResult(context.Background(), dir, output, "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cleanup()
	b, err := OpenSectionResult(r, output)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := b.TraceTimeOriginUS(0)
	b.Close()
	if err != nil || origin != 12500 || r.Metadata.TimeOriginUS != origin {
		t.Fatalf("fractional-ms origin lost: %d %v", origin, err)
	}
	target := filepath.Join(t.TempDir(), "fractional.sgy")
	if err := ExportSectionResult(context.Background(), r, output, target); err == nil {
		t.Fatal("SEG-Y export silently rounded a fractional-ms origin")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected fractional-ms SEG-Y was published")
	}
}

func TestAlgorithmSectionUnmappedTraceOrderPreservesPhysicalIDsAndXY(t *testing.T) {
	source, values := algorithmFloat64Fixture(t)
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		block := raw[3600+i*(240+5*8):]
		binary.BigEndian.PutUint16(block[70:72], uint16(10))
		binary.BigEndian.PutUint32(block[180:184], uint32(100+i))
		binary.BigEndian.PutUint32(block[184:188], uint32(200+i))
	}
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), raw...)
	request := SectionRequest{SourcePath: source, TraceIndices: []int64{1, 0}, SampleStart: 1, SampleEnd: 3, AlgorithmID: "example.noop", Direction: "trace_order"}
	inputPath := filepath.Join(t.TempDir(), "input.sfdata")
	in, err := CaptureAlgorithmSectionMapped(context.Background(), request, inputPath, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := artifact.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	traces := f.TraceMetadata()
	f.Close()
	for i, physical := range []int64{1, 0} {
		got := traces[i]
		if got.TraceID != physical || got.Inline != 0 || got.Crossline != 0 || !got.CoordinateValid || got.X != float64(100+physical)*10 || got.Y != float64(200+physical)*10 {
			t.Fatalf("trace-order metadata is fabricated or changed: %+v", got)
		}
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "output.sfdata")
	copyAlgorithmArtifact(t, inputPath, output, nil)
	r, err := in.AcceptResult(context.Background(), dir, output, "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cleanup()
	b, err := OpenSectionResult(r, output)
	if err != nil {
		t.Fatal(err)
	}
	for i, physical := range []int64{1, 0} {
		got, err := b.ReadTraceWindow(int64(i), 0, 2)
		if err != nil || !reflect.DeepEqual(got, values[physical][1:4]) {
			t.Fatalf("mapped values %v: %v", got, err)
		}
		h, err := b.ReadTraceHeader(int64(i))
		if err != nil || h.Inline != 1870 || h.Crossline != int32(184+physical) {
			t.Fatalf("unconfigured artifact geometry must not rewrite original header: %+v %v", h, err)
		}
	}
	b.Close()
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("A source changed: %v", err)
	}
	for _, test := range []struct {
		direction string
		il, xl    int
	}{{"inline", 0, 0}, {"crossline", 0, 0}, {"trace_order", 0, 193}, {"trace_order", 189, 0}, {"trace_order", 189, 189}} {
		request.Direction = test.direction
		if _, err := CaptureAlgorithmSectionMapped(context.Background(), request, filepath.Join(t.TempDir(), "invalid.sfdata"), test.il, test.xl, nil); err == nil {
			t.Fatalf("invalid mapping accepted: %+v", test)
		}
	}
}
