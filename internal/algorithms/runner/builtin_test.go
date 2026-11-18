package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/registry"
	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
	"github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi/artifact"
)

func hostTestModule(t *testing.T) registry.Module {
	t.Helper()
	r := registry.New("1.10.4")
	d := fakeDescriptor()
	d.ID = "test.host"
	d.Executable = "host-test"
	if err := r.RegisterBuiltin(d); err != nil {
		t.Fatal(err)
	}
	m, _ := r.Get(d.ID)
	return m
}

func hostCopy(ctx context.Context, _ api.RunRequest, input, output string, progress api.ProgressReporter) (BuiltinExecutionInfo, error) {
	f, err := artifact.OpenContext(ctx, input)
	if err != nil {
		return BuiltinExecutionInfo{}, err
	}
	defer f.Close()
	err = artifact.Write(ctx, output, f.Metadata(), f.TraceMetadata(), func(i int64) ([]float64, []bool, error) {
		if progress != nil {
			progress(api.ProgressEvent{Type: "progress", Value: .5})
		}
		return f.ReadTraceWindow(i, 0, f.SamplesPerTrace())
	})
	return BuiltinExecutionInfo{InputContextFingerprint: strings.Repeat("a", 64), ProcessingScope: &api.AlgorithmProcessingScope{TraceCount: 2, SampleStart: 2, SampleEnd: 4, ContextEnd: 30, SampleIntervalUS: 2000}}, err
}

func TestHostBuiltinUsesCommonValidationRecordsAndCleanup(t *testing.T) {
	m := hostTestModule(t)
	opts := options(t, m)
	opts.AuthorizedFingerprint = "" // public code, not an external EXE
	input := fakeInput(t)
	if _, err := Run(context.Background(), m, request("copy"), input, opts, nil); err == nil {
		t.Fatal("builtin accidentally went through executable runner")
	}
	if _, err := Describe(context.Background(), m, opts); err != nil {
		t.Fatal(err)
	}
	progress := 0
	result, err := RunBuiltin(context.Background(), m, request("copy"), input, opts, hostCopy, func(p api.ProgressEvent) {
		if p.Type == "progress" {
			progress++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if progress != 2 || result.Record.Status != "success" || result.Record.AlgorithmID != "test.host" || result.Record.ProcessingScope == nil || result.Record.InputContextFingerprint == "" || result.Record.OutputFingerprint == "" {
		t.Fatalf("bad record %+v", result.Record)
	}
	data, err := os.ReadFile(result.RecordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record api.AlgorithmRunRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Parameters["mode"] != "copy" || record.ProcessingScope.ContextEnd != 30 {
		t.Fatal("record lost normalized parameters or context")
	}
	for _, forbidden := range []string{"\"samples\"", "\"amplitudes\"", "\"reader\"", "VerifySource"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatal("non-metadata record", forbidden)
		}
	}
	if err := result.Cleanup(); err != nil {
		t.Fatal(err)
	}
	assertNoTasks(t, opts.TempRoot)
}

func TestHostBuiltinRejectsInvalidParametersAndFinalSourceChange(t *testing.T) {
	m := hostTestModule(t)
	input := fakeInput(t)
	opts := options(t, m)
	called := false
	callback := func(ctx context.Context, r api.RunRequest, in, out string, p api.ProgressReporter) (BuiltinExecutionInfo, error) {
		called = true
		info, err := hostCopy(ctx, r, in, out, p)
		info.VerifySource = func() error { return errors.New("source changed") }
		return info, err
	}
	if _, err := RunBuiltin(context.Background(), m, request("invalid-mode"), input, opts, callback, nil); err == nil || called {
		t.Fatal("invalid params ran callback")
	}
	if _, err := RunBuiltin(context.Background(), m, request("copy"), input, opts, callback, nil); err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatal("final source identity not verified", err)
	}
	assertNoTasks(t, opts.TempRoot)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called = false
	if _, err := RunBuiltin(ctx, m, request("copy"), input, opts, callback, nil); !errors.Is(err, context.Canceled) || called {
		t.Fatal("cancelled callback ran", err)
	}
	assertNoTasks(t, opts.TempRoot)
}

func TestFailedRunExposesProvenanceWithoutProvidingPartialResult(t *testing.T) {
	m, input := hostTestModule(t), fakeInput(t)
	opts := options(t, m)
	backendErr := errors.New("exit status 1; configuration rejected")
	result, err := RunBuiltin(context.Background(), m, request("copy"), input, opts,
		func(context.Context, api.RunRequest, string, string, api.ProgressReporter) (BuiltinExecutionInfo, error) {
			return BuiltinExecutionInfo{}, backendErr
		}, nil)
	var recorded *RecordedError
	if result != nil || !errors.Is(err, backendErr) || !errors.As(err, &recorded) || recorded.RecordPath == "" {
		t.Fatalf("bad failure result and record: %+v / %v", result, err)
	}
	data, err := os.ReadFile(recorded.RecordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record api.AlgorithmRunRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Status != "failed" || record.Parameters["mode"] != "copy" || record.Error != backendErr.Error() {
		t.Fatalf("failure details did not round-trip: %+v / %v", record, err)
	}
	assertNoTasks(t, opts.TempRoot)
}
