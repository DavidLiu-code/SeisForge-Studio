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

	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/registry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/runner"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
	"github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi/artifact"
)

func bandpassModuleFixture(t *testing.T) (string, [][]float64) {
	t.Helper()
	const count, samples = 3, 256
	values := make([][]float64, count)
	for i := range values {
		values[i] = make([]float64, samples)
		for j := range values[i] {
			values[i][j] = float64(i+1) * (math.Sin(2*math.Pi*40*float64(j)*.002) + .4*math.Cos(2*math.Pi*180*float64(j)*.002))
		}
	}
	return writeBandpassFixture(t, values), values
}

func writeBandpassFixture(t *testing.T, values [][]float64) string {
	t.Helper()
	count, samples := len(values), len(values[0])
	path := filepath.Join(t.TempDir(), "base.sgy")
	data := make([]byte, 3600+count*(240+samples*8))
	copy(data, "C 1 bandpass source identity must not change")
	binary.BigEndian.PutUint16(data[3216:3218], 2000)
	binary.BigEndian.PutUint16(data[3220:3222], uint16(samples))
	binary.BigEndian.PutUint16(data[3224:3226], 6)
	for i := range values {
		block := data[3600+i*(240+samples*8):]
		binary.BigEndian.PutUint32(block[188:192], 1870)
		binary.BigEndian.PutUint32(block[192:196], uint32(184+i))
		binary.BigEndian.PutUint16(block[108:110], 800)
		binary.BigEndian.PutUint16(block[114:116], uint16(samples))
		binary.BigEndian.PutUint16(block[116:118], 2000)
		for j, v := range values[i] {
			binary.BigEndian.PutUint64(block[240+j*8:], math.Float64bits(v))
		}
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBandpassModuleFullGeometryIndependentTracesAndRepeat(t *testing.T) {
	const n, center, dtUS = 8000, 4000, 2000
	tones := []float64{10, 40, 100}
	values := [][]float64{make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)}
	values[1][center] = 1 // physical trace 1 has the only impulse
	for i := range values[3] {
		for _, hz := range tones {
			values[3][i] += math.Sin(2 * math.Pi * hz * float64(i) / 500)
		}
	}
	source := writeBandpassFixture(t, values)
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	input := filepath.Join(t.TempDir(), "input.sfdata")
	order := []int64{2, 1, 0, 3} // impulse bracketed by zero traces in requested output order
	req := SectionRequest{SourcePath: source, TraceIndices: order, SampleEnd: n - 1, AlgorithmID: BandpassAlgorithmID, Direction: "inline"}
	in, err := CaptureAlgorithmSection(ctx, req, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	p1 := ViewBandpass{LowHz: 20, HighHz: 162.5, Response: FilterBandpass, Method: FilterButterworth, Order: 4, Phase: FilterZeroPhase}
	p2 := ViewBandpass{LowHz: 60, Response: FilterLowpass, Method: FilterChebyshevI, Order: 3, Phase: FilterForward, RippleDB: 1.25}
	var firstFingerprint string
	var firstRows [][]float64
	for run, p := range []ViewBandpass{p1, p1, p2, p1} {
		dir := t.TempDir()
		outPath := filepath.Join(dir, "output.sfdata")
		if _, err := in.WriteBandpassArtifact(ctx, input, outPath, p, nil); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		out, err := artifact.Open(outPath)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint := out.Fingerprint()
		if run == 0 {
			firstFingerprint = fingerprint
			firstRows = make([][]float64, len(order))
		} else if same := fingerprint == firstFingerprint; same != (p == p1) {
			out.Close()
			t.Fatalf("run %d: expected artifact bit identity if and only if P1, got %v", run, same)
		}
		meta, geometry := out.Metadata(), out.TraceMetadata()
		if !reflect.DeepEqual(meta.Shape, []uint64{4, n}) || meta.SampleIntervalUS != dtUS || meta.TimeOriginUS != 800000 || meta.SampleStart != 0 || len(geometry) != 4 {
			out.Close()
			t.Fatalf("run %d: sample axis/shape changed: %+v", run, meta)
		}
		for row, physical := range order {
			g := geometry[row]
			if g.TraceID != physical || g.Inline != 1870 || g.Crossline != 184+int32(physical) || g.TimeOriginUS != 800000 {
				out.Close()
				t.Fatalf("run %d: geometry of row %d: %+v", run, row, g)
			}
			got, _, err := out.ReadTraceWindow(int64(row), 0, n)
			if err != nil || len(got) != n {
				out.Close()
				t.Fatalf("run %d: row %d: %d samples, %v", run, row, len(got), err)
			}
			if physical == 0 || physical == 2 {
				for k, v := range got {
					if v != 0 {
						out.Close()
						t.Fatalf("run %d: impulse leaked to zero trace %d, sample %d: %g", run, physical, k, v)
					}
				}
			}
			if run == 0 {
				firstRows[row] = got
			} else if p == p1 && !reflect.DeepEqual(firstRows[row], got) {
				out.Close()
				t.Fatalf("run %d: repeated P1 differs at trace %d", run, physical)
			}
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
		if run != 0 {
			continue
		}
		impulse := firstRows[1]
		for i := 1; i < 500; i++ {
			if impulse[center] <= math.Abs(impulse[center+i]) || math.Abs(impulse[center-i]-impulse[center+i]) > 1e-12 {
				t.Fatalf("impulse lost its center/symmetry at offset %d", i)
			}
		}
		for _, hz := range tones {
			s, c := viewTestProjection(firstRows[3][2000:6000], 2000, hz, 500)
			want := math.Pow(referenceButterworthBandpassAmplitude(hz, 4), 2)
			if math.Abs(s-want) > 2e-8 || math.Abs(c) > 2e-8 {
				t.Fatalf("formal B tone %g Hz: %g + %gi; want %g + 0i", hz, s, c, want)
			}
		}
		result, err := in.AcceptResult(ctx, dir, outPath, "")
		if err != nil {
			t.Fatal(err)
		}
		defer result.Cleanup()
		exported := filepath.Join(t.TempDir(), "filtered.sgy")
		if err := ExportSectionResult(ctx, result, outPath, exported); err != nil {
			t.Fatal(err)
		}
		f, err := segy.Open(exported)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if f.Info.FormatCode != 6 || f.Info.SamplesPerTrace != n || f.Info.TraceCount != 4 || f.Info.SampleIntervalUS != dtUS {
			t.Fatal(f.Info)
		}
		for row, physical := range order {
			h, err := f.ReadTraceHeader(int64(row))
			if err != nil || h.Inline != 1870 || h.Crossline != 184+int32(physical) || h.DelayMS != 800 || h.SampleIntervalUS != dtUS || h.SampleCount != n {
				t.Fatalf("reopened SEG-Y geometry/axis row %d: %+v, %v", row, h, err)
			}
			got, err := f.ReadTraceWindow(int64(row), 0, n-1)
			if err != nil || !reflect.DeepEqual(got, firstRows[row]) {
				t.Fatalf("reopened SEG-Y altered output samples at row %d: %v", row, err)
			}
		}
	}
	invalid := p1
	invalid.HighHz = 250
	failedPath := filepath.Join(t.TempDir(), "invalid.sfdata")
	if _, err := in.WriteBandpassArtifact(ctx, input, failedPath, invalid, nil); err == nil {
		t.Fatal("accepted filter edge at Nyquist")
	}
	if _, err := os.Stat(failedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed filter published an output: %v", err)
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("filter changed baseline input: %v", err)
	}
	t.Logf("4×%d, dt=%d µs, t0=800 ms; P1/P1/P2/P1 geometry and samples verified; 162.5 Hz accepted", n, dtUS)
}

func TestBandpassModuleUsesFullBaseContextThenCropsAndPreservesGeometry(t *testing.T) {
	source, values := bandpassModuleFixture(t)
	before, _ := os.ReadFile(source)
	inputPath := filepath.Join(t.TempDir(), "input.sfdata")
	req := SectionRequest{SourcePath: source, TraceIndices: []int64{2, 0}, SampleStart: 100, SampleEnd: 104, AlgorithmID: BandpassAlgorithmID, Direction: "inline"}
	in, err := CaptureAlgorithmSection(context.Background(), req, inputPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := ViewBandpass{LowHz: 10, HighHz: 90}
	var firstFingerprint string
	for run := 0; run < 2; run++ {
		dir := t.TempDir()
		output := filepath.Join(dir, "output.sfdata")
		progress := 0
		info, err := in.WriteBandpassArtifact(context.Background(), inputPath, output, p, func(done, total int) {
			progress = done
			if total != 2 {
				t.Fatal(total)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(info.Fingerprint) != 64 || progress != 2 || info.Scope.ContextStart != 0 || info.Scope.ContextEnd != 255 || info.Scope.SampleStart != 100 || info.Scope.SampleEnd != 104 || info.Scope.SampleIntervalUS != 2000 {
			t.Fatalf("wrong context %+v", info)
		}
		if run == 0 {
			firstFingerprint = info.Fingerprint
		} else if firstFingerprint != info.Fingerprint {
			t.Fatal("context fingerprint unstable")
		}
		out, err := artifact.Open(output)
		if err != nil {
			t.Fatal(err)
		}
		for logical, physical := range req.TraceIndices {
			got, _, err := out.ReadTraceWindow(int64(logical), 0, 5)
			want, e := FilterTraceWindow(context.Background(), values[physical], 2000, p, 100, 104)
			if err != nil || e != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("formal/view differ: %v %v", err, e)
			}
		}
		out.Close()
		result, err := in.AcceptResult(context.Background(), dir, output, "")
		if err != nil {
			t.Fatal(err)
		}
		b, err := OpenSectionResult(result, output)
		if err != nil {
			t.Fatal(err)
		}
		header, err := b.ReadTraceHeader(0)
		if err != nil || header.Inline != 1870 || header.Crossline != 186 || header.DelayMS != 1000 {
			t.Fatalf("geometry/sample origin changed: %+v %v", header, err)
		}
		b.Close()
		exported := filepath.Join(t.TempDir(), "B.sgy")
		if err := ExportSectionResult(context.Background(), result, output, exported); err != nil {
			t.Fatal(err)
		}
		f, err := segy.Open(exported)
		if err != nil {
			t.Fatal(err)
		}
		if f.Info.FormatCode != 6 || f.Info.TraceCount != 2 || f.Info.SamplesPerTrace != 5 {
			t.Fatal(f.Info)
		}
		f.Close()
		result.Cleanup()
	}
	after, _ := os.ReadFile(source)
	if !bytes.Equal(before, after) {
		t.Fatal("formal filtering changed A")
	}
}

func TestBandpassModuleDefaultsValidationCancellationAndSourceChange(t *testing.T) {
	for _, dt := range []int{500, 1000, 2000, 4000} {
		d := BandpassDescriptor(dt)
		if err := api.ValidateDescriptor(d, "1.10.3"); err != nil {
			t.Fatal(err)
		}
		p, err := BandpassParameters(nil, dt)
		if err != nil || p != DefaultBandpass(dt) {
			t.Fatalf("shared defaults differ: %+v %v", p, err)
		}
	}
	for _, params := range []map[string]any{{"low_hz": 0., "high_hz": 90.}, {"low_hz": 90., "high_hz": 10.}, {"low_hz": 10., "high_hz": 250.}, {"unknown": 1.}} {
		if _, err := BandpassParameters(params, 2000); err == nil {
			t.Fatal("accepted invalid module parameters", params)
		}
	}
	source, _ := bandpassModuleFixture(t)
	input := filepath.Join(t.TempDir(), "input.sfdata")
	in, err := CaptureAlgorithmSection(context.Background(), SectionRequest{SourcePath: source, TraceIndices: []int64{0}, SampleStart: 0, SampleEnd: 8, AlgorithmID: BandpassAlgorithmID, Direction: "inline"}, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output := filepath.Join(t.TempDir(), "output.sfdata")
	if _, err := in.WriteBandpassArtifact(ctx, input, output, ViewBandpass{LowHz: 10, HighHz: 90}, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled output published")
	}
	data, _ := os.ReadFile(source)
	data[len(data)-1] ^= 1
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := in.WriteBandpassArtifact(context.Background(), input, output, ViewBandpass{LowHz: 10, HighHz: 90}, nil); err == nil {
		t.Fatal("source mutation accepted")
	}
}

func TestBandpassModuleDoesNotLeaveOutputWhenSourceChangesOnFinalTrace(t *testing.T) {
	source, _ := bandpassModuleFixture(t)
	ctx := context.Background()
	input := filepath.Join(t.TempDir(), "input.sfdata")
	in, err := CaptureAlgorithmSection(ctx, SectionRequest{SourcePath: source, TraceIndices: []int64{0}, SampleEnd: 8, AlgorithmID: BandpassAlgorithmID}, input, nil)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "output.sfdata")
	changed := false
	_, err = in.WriteBandpassArtifact(ctx, input, output, ViewBandpass{LowHz: 10, HighHz: 90}, func(done, total int) {
		if done == total {
			f, err := os.OpenFile(source, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte{0}); err != nil {
				f.Close()
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			changed = true
		}
	})
	if !changed || err == nil {
		t.Fatalf("mutation not injected or accepted: changed=%v, err=%v", changed, err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source mutation left a valid-looking artifact behind: %v", err)
	}
}

func TestFilterTraceWindowDoesNotFilterShortCropOrAccumulate(t *testing.T) {
	_, values := bandpassModuleFixture(t)
	full := values[0]
	p := ViewBandpass{LowHz: 10, HighHz: 90}
	want, err := FilterTrace(nil, full, 2000, p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		got, err := FilterTraceWindow(nil, full, 2000, p, 120, 121)
		if err != nil || !reflect.DeepEqual(got, want[120:122]) {
			t.Fatal("filtered cropped samples or accumulated", err)
		}
	}
	for _, bounds := range [][2]int{{-1, 2}, {5, 4}, {0, len(full)}} {
		if _, err := FilterTraceWindow(nil, full, 2000, p, bounds[0], bounds[1]); err == nil {
			t.Fatal("invalid crop accepted")
		}
	}
}

func TestBandpassFormalRegisteredRunnerAndManagedB(t *testing.T) {
	source, values := bandpassModuleFixture(t)
	inputPath := filepath.Join(t.TempDir(), "input.sfdata")
	in, err := CaptureAlgorithmSection(context.Background(), SectionRequest{SourcePath: source, TraceIndices: []int64{1}, SampleStart: 25, SampleEnd: 225, AlgorithmID: BandpassAlgorithmID, Direction: "inline"}, inputPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := registry.New("1.10.3")
	if err := r.RegisterBuiltin(BandpassDescriptor(2000)); err != nil {
		t.Fatal(err)
	}
	m, _ := r.Get(BandpassAlgorithmID)
	request := api.RunRequest{Parameters: map[string]any{BandpassLowParameter: 10., BandpassHighParameter: 90.}}
	p, err := BandpassParameters(request.Parameters, 2000)
	if err != nil {
		t.Fatal(err)
	}
	run, err := runner.RunBuiltin(context.Background(), m, request, inputPath, runner.Options{TempRoot: t.TempDir(), RecordDirectory: t.TempDir()},
		func(ctx context.Context, request api.RunRequest, localInput, output string, notify api.ProgressReporter) (runner.BuiltinExecutionInfo, error) {
			info, err := in.WriteBandpassArtifact(ctx, localInput, output, p, nil)
			return runner.BuiltinExecutionInfo{InputContextFingerprint: info.Fingerprint, ProcessingScope: &info.Scope, VerifySource: in.SourceUnchanged}, err
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer run.Cleanup()
	if run.Record.AlgorithmID != BandpassAlgorithmID || run.Record.Parameters[BandpassLowParameter] != 10. || run.Record.InputContextFingerprint == "" || run.Record.ProcessingScope.SampleStart != 25 {
		t.Fatalf("formal record lost base identity/parameters/context: %+v", run.Record)
	}
	result, err := in.AcceptResult(context.Background(), run.Directory, run.OutputPath, "")
	if err != nil {
		t.Fatal(err)
	}
	run.DetachDirectory()
	defer result.Cleanup()
	b, err := OpenSectionResult(result, result.Part2)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	got, err := b.ReadTraceWindow(0, 0, 200)
	want, e := FilterTraceWindow(nil, values[1], 2000, p, 25, 225)
	if err != nil || e != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("formal managed B differs from view core", err, e)
	}
}
