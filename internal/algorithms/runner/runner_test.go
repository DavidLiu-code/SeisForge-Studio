package runner

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/registry"
	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
	"github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi/artifact"
)

func fakeDescriptor() api.AlgorithmDescriptor {
	return api.AlgorithmDescriptor{ID: "test.fake", Version: "1.0.0", Name: "Fake test process", ProtocolVersion: api.ProtocolVersion, Executable: "fake.exe", MinimumHostVersion: "1.10.4", InputKinds: []string{"section2d"}, OutputKinds: []string{"section2d"}, SupportsProgress: true, SupportsCancel: true, Parameters: []api.ParameterDescriptor{{ID: "mode", Type: "enum", Default: "copy", Options: []string{"copy", "failure", "hang", "malformed", "corrupt", "escape", "missing-result", "badshape", "change-input", "flood", "child", "locked-child", "files"}}, {ID: "config", Type: "file"}, {ID: "group", Type: "group", Parameters: []api.ParameterDescriptor{{ID: "weights", Type: "file"}}}}}
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "--run" || os.Args[1] == "--describe" || os.Args[1] == "--sleepchild") {
		if err := fakeProcess(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func fakeProcess() error {
	if os.Args[1] == "--sleepchild" {
		if len(os.Args) > 2 {
			release, err := lockChildFile(os.Args[2])
			if err != nil {
				return err
			}
			defer release()
			if err = os.WriteFile(os.Args[2]+".ready", []byte("locked"), 0600); err != nil {
				return err
			}
		}
		time.Sleep(time.Hour)
		return nil
	}
	if os.Args[1] == "--describe" {
		d := fakeDescriptor()
		if len(os.Args) != 2 {
			if len(os.Args) != 4 || os.Args[2] != "--algorithm" || (os.Args[3] != "test.pack_one" && os.Args[3] != "test.pack_two") {
				return errors.New("unexpected describe arguments")
			}
			d.ID = os.Args[3]
		}
		return json.NewEncoder(os.Stdout).Encode(d)
	}
	f := flag.NewFlagSet("fake", flag.ContinueOnError)
	_ = f.Bool("run", false, "")
	requestPath := f.String("request", "", "")
	input := f.String("input", "", "")
	output := f.String("output", "", "")
	if err := f.Parse(os.Args[1:]); err != nil {
		return err
	}
	data, err := os.ReadFile(*requestPath)
	if err != nil {
		return err
	}
	var req api.RunRequest
	if err = json.Unmarshal(data, &req); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(api.ProgressEvent{Type: "started"})
	_ = enc.Encode(api.ProgressEvent{Type: "progress", Value: 0.5, Message: "fake started"})
	mode, _ := req.Parameters["mode"].(string)
	switch mode {
	case "failure":
		return errors.New("intentional fake failure")
	case "hang":
		time.Sleep(time.Hour)
	case "child", "locked-child":
		args := []string{"--sleepchild"}
		if mode == "locked-child" {
			args = append(args, filepath.Join(filepath.Dir(*input), "held.lock"))
		}
		cmd := exec.Command(os.Args[0], args...)
		if err = cmd.Start(); err != nil {
			return err
		}
		if err = os.WriteFile("child.pid", []byte(fmt.Sprint(cmd.Process.Pid)), 0600); err != nil {
			return err
		}
		time.Sleep(time.Hour)
	case "malformed":
		fmt.Fprintln(os.Stdout, "not JSON")
		time.Sleep(time.Hour)
	case "flood":
		fmt.Fprint(os.Stdout, strings.Repeat("x", maxLineBytes+5))
		time.Sleep(time.Hour)
	case "missing-result":
		return nil
	case "corrupt":
		if err = os.WriteFile(*output, []byte("broken"), 0600); err != nil {
			return err
		}
		return enc.Encode(api.ProgressEvent{Type: "result", Status: "ok", Output: *output})
	case "escape":
		return enc.Encode(api.ProgressEvent{Type: "result", Status: "ok", Output: filepath.Join(filepath.Dir(filepath.Dir(*output)), "outside.sfdata")})
	case "badshape":
		meta := artifact.Metadata{DType: "float32", Shape: []uint64{1, 2}, Axes: []string{"trace", "sample"}, SampleIntervalUS: 2000}
		err = artifact.Write(context.Background(), *output, meta, []artifact.TraceMetadata{{TraceID: 2}}, func(int64) ([]float64, []bool, error) { return []float64{1, 2}, nil, nil })
		if err != nil {
			return err
		}
		return enc.Encode(api.ProgressEvent{Type: "result", Status: "ok", Output: *output})
	case "files":
		paths := []string{req.Parameters["config"].(string), req.Parameters["group"].(map[string]any)["weights"].(string)}
		cwd, _ := os.Getwd()
		for _, p := range paths {
			rel, e := filepath.Rel(cwd, p)
			if e != nil || !filepath.IsLocal(rel) {
				return errors.New("file parameter path not staged")
			}
			if _, e = os.ReadFile(p); e != nil {
				return e
			}
		}
	}
	src, err := os.Open(*input)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(*output)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	closeErr := dst.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if mode == "change-input" {
		if err = os.WriteFile(*input, []byte("modified"), 0600); err != nil {
			return err
		}
	}
	_ = enc.Encode(api.ProgressEvent{Type: "progress", Value: 1})
	return enc.Encode(api.ProgressEvent{Type: "result", Status: "ok", Output: *output})
}

func fakeModule(t *testing.T) registry.Module {
	return fakeModuleWithDescriptor(t, fakeDescriptor())
}
func fakeModuleWithDescriptor(t *testing.T, desc api.AlgorithmDescriptor) registry.Module {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = copyInput(context.Background(), exe, filepath.Join(dir, "fake.exe")); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(dir, "fake.exe"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(desc)
	path := filepath.Join(dir, "manifest.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	module, err := registry.LoadManifest(path, "1.10.4")
	if err != nil {
		t.Fatal(err)
	}
	return module
}
func fakeInput(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.sfdata")
	meta := artifact.Metadata{DType: "float64", Shape: []uint64{2, 3}, Axes: []string{"trace", "sample"}, SampleIntervalUS: 2000, TimeOriginUS: 4000, SampleStart: 2}
	traces := []artifact.TraceMetadata{{TraceID: 42, Inline: 1870, Crossline: 184, X: 0, Y: 0, CoordinateValid: true}, {TraceID: 4, Inline: 1870, Crossline: 185, X: 1, Y: 2, CoordinateValid: true}}
	if err := artifact.Write(context.Background(), path, meta, traces, func(i int64) ([]float64, []bool, error) { return []float64{float64(i), -2, 0.5}, nil, nil }); err != nil {
		t.Fatal(err)
	}
	return path
}
func options(t *testing.T, m registry.Module) Options {
	return Options{AuthorizedFingerprint: m.Fingerprint(), TempRoot: t.TempDir(), RecordDirectory: t.TempDir(), Timeout: 10 * time.Second}
}
func request(mode string) api.RunRequest {
	return api.RunRequest{Parameters: map[string]any{"mode": mode}}
}
func assertNoTasks(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("task temp dirs leaked: %v", entries)
	}
}

func TestExternalNoopDiscoveryDescribeRunAndCleanup(t *testing.T) {
	m := fakeModule(t)
	input := fakeInput(t)
	opts := options(t, m)
	if _, err := Describe(context.Background(), m, Options{}); err == nil {
		t.Fatal("describe ran without trust")
	}
	d, err := Describe(context.Background(), m, opts)
	if err != nil || d.ID != "test.fake" {
		t.Fatalf("describe: %v %v", d, err)
	}
	var mu sync.Mutex
	var events []string
	result, err := Run(context.Background(), m, request("copy"), input, opts, func(e api.ProgressEvent) { mu.Lock(); events = append(events, e.Type); mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	if result.Record.Status != "success" || result.Record.InputFingerprint != result.Record.OutputFingerprint || result.RecordPath == "" {
		t.Fatalf("incomplete provenance: %+v", result)
	}
	mu.Lock()
	if len(events) < 2 {
		t.Error("missing progress")
	}
	mu.Unlock()
	a, err := artifact.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := artifact.Open(result.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = artifact.CheckCompatibility(a, b); err != nil {
		t.Fatal(err)
	}
	b.Close()
	if err = result.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err = result.Cleanup(); err != nil {
		t.Fatal(err)
	}
	assertNoTasks(t, opts.TempRoot)
}

func TestPackMembersDescribeAndRunViaSharedExecutable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(root, fakeDescriptor().Executable)
	if err = copyInput(ctx, exe, worker); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(worker, 0700); err != nil {
		t.Fatal(err)
	}
	ids := []string{"test.pack_one", "test.pack_two"}
	names := make([]string, len(ids))
	for i, id := range ids {
		d := fakeDescriptor()
		d.ID = id
		names[i] = id + ".json"
		payload, _ := json.Marshal(d)
		if err = os.WriteFile(filepath.Join(root, names[i]), payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	marker, _ := json.Marshal(map[string]any{"schema": registry.PackSchema, "id": "test.pack", "version": "1.0.0", "manifests": names})
	if err = os.WriteFile(filepath.Join(root, registry.PackManifestName), marker, 0600); err != nil {
		t.Fatal(err)
	}
	r := registry.New("1.10.4")
	if issues, err := r.Scan(ctx, []string{root}); err != nil || len(issues) != 0 || len(r.Modules()) != 2 {
		t.Fatalf("scan: %+v %v", issues, err)
	}
	for _, id := range ids {
		m, _ := r.Get(id)
		if _, err = Describe(ctx, m, Options{}); err == nil {
			t.Fatalf("pack member %s bypassed describe authorization", id)
		}
		d, err := Describe(ctx, m, options(t, m))
		if err != nil || d.ID != id {
			t.Fatalf("pack member %s did not receive its own describe ID: %q %v", id, d.ID, err)
		}
	}
	one, _ := r.Get(ids[0])
	two, _ := r.Get(ids[1])
	input := fakeInput(t)
	if _, err := Run(ctx, two, request("copy"), input, options(t, one), nil); err == nil {
		t.Fatal("authorization of one member implicitly authorized a different member")
	}
	result, err := Run(ctx, two, request("copy"), input, options(t, two), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Cleanup()
	if result.Record.AlgorithmID != ids[1] || result.Record.Status != "success" {
		t.Fatalf("result lost method provenance: %+v", result.Record)
	}
}
func TestInvalidParametersAndChangedTrustNeverLaunch(t *testing.T) {
	m := fakeModule(t)
	opts := options(t, m)
	input := fakeInput(t)
	if _, err := Run(context.Background(), m, request("unknown"), input, opts, nil); err == nil {
		t.Fatal("invalid parameter launched")
	}
	assertNoTasks(t, opts.TempRoot)
	data, err := os.ReadFile(m.ManifestPath())
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, ' ')
	if err = os.WriteFile(m.ManifestPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Run(context.Background(), m, request("copy"), input, opts, nil); err == nil || !strings.Contains(err.Error(), "authorize") {
		t.Fatalf("changed manifest accepted: %v", err)
	}
	assertNoTasks(t, opts.TempRoot)
}

func TestUnsupportedArtifactKindsNeverLaunch(t *testing.T) {
	input := fakeInput(t)
	for _, direction := range []string{"input", "output"} {
		t.Run(direction, func(t *testing.T) {
			desc := fakeDescriptor()
			if direction == "input" {
				desc.InputKinds = []string{"volume3d"}
			} else {
				desc.OutputKinds = []string{"volume3d"}
			}
			m := fakeModuleWithDescriptor(t, desc)
			opts := options(t, m)
			started := false
			result, err := Run(context.Background(), m, request("hang"), input, opts, func(api.ProgressEvent) { started = true })
			if err == nil || !strings.Contains(err.Error(), direction+" kind") || result != nil || started {
				t.Fatalf("incompatible module was not rejected before launch: result=%v err=%v started=%v", result, err, started)
			}
			assertNoTasks(t, opts.TempRoot)
			assertNoTasks(t, opts.RecordDirectory)
		})
	}
	if _, err := artifactKind(artifact.Metadata{Shape: []uint64{1, 2, 3}, Axes: []string{"trace", "channel", "sample"}}); err == nil {
		t.Fatal("unknown axis semantics accepted")
	}
}

func TestExternalVolumeArtifactKind(t *testing.T) {
	desc := fakeDescriptor()
	desc.InputKinds = []string{"section2d", "volume3d"}
	desc.OutputKinds = []string{"section2d", "volume3d"}
	m := fakeModuleWithDescriptor(t, desc)
	input := filepath.Join(t.TempDir(), "volume.sfdata")
	meta := artifact.Metadata{DType: "float64", Shape: []uint64{2, 2, 3}, Axes: []string{"inline", "crossline", "sample"}, SampleIntervalUS: 2000}
	traces := []artifact.TraceMetadata{{TraceID: 1, Inline: 10, Crossline: 20}, {TraceID: 2, Inline: 10, Crossline: 21}, {TraceID: 3, Inline: 11, Crossline: 20}, {TraceID: 4, Inline: 11, Crossline: 21}}
	if err := artifact.Write(context.Background(), input, meta, traces, func(i int64) ([]float64, []bool, error) { return []float64{float64(i), -2, 0.5}, nil, nil }); err != nil {
		t.Fatal(err)
	}
	opts := options(t, m)
	result, err := Run(context.Background(), m, request("copy"), input, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Record.InputFingerprint != result.Record.OutputFingerprint {
		t.Error("volume noop changed input identity")
	}
	if err = result.Cleanup(); err != nil {
		t.Fatal(err)
	}
	assertNoTasks(t, opts.TempRoot)
}
func TestFailuresNeverPublishAndPreserveInput(t *testing.T) {
	m := fakeModule(t)
	input := fakeInput(t)
	before, _ := os.ReadFile(input)
	for _, mode := range []string{"failure", "malformed", "corrupt", "escape", "missing-result", "badshape", "change-input", "flood"} {
		t.Run(mode, func(t *testing.T) {
			opts := options(t, m)
			result, err := Run(context.Background(), m, request(mode), input, opts, nil)
			if err == nil || result != nil {
				t.Fatalf("invalid result accepted: %v", err)
			}
			assertNoTasks(t, opts.TempRoot)
			after, _ := os.ReadFile(input)
			if string(after) != string(before) {
				t.Fatal("source artifact changed")
			}
			records, _ := filepath.Glob(filepath.Join(opts.RecordDirectory, "*.json"))
			if len(records) != 1 {
				t.Fatal("failed run record missing")
			}
		})
	}
}
func TestCancelTimeoutAndChildTree(t *testing.T) {
	m := fakeModule(t)
	input := fakeInput(t)
	t.Run("timeout", func(t *testing.T) {
		opts := options(t, m)
		opts.Timeout = 100 * time.Millisecond
		_, err := Run(context.Background(), m, request("hang"), input, opts, nil)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected timeout: %v", err)
		}
		assertNoTasks(t, opts.TempRoot)
	})
	t.Run("cancel", func(t *testing.T) {
		opts := options(t, m)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := Run(ctx, m, request("hang"), input, opts, func(e api.ProgressEvent) {
			if e.Type == "started" {
				cancel()
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancel: %v", err)
		}
		assertNoTasks(t, opts.TempRoot)
	})
	for _, mode := range []string{"child", "locked-child"} {
		t.Run(mode, func(t *testing.T) {
			opts := options(t, m)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := Run(ctx, m, request(mode), input, opts, nil); done <- err }()
			deadline := time.Now().Add(5 * time.Second)
			pid := 0
			locked := mode != "locked-child"
			for (pid == 0 || !locked) && time.Now().Before(deadline) {
				files, _ := filepath.Glob(filepath.Join(opts.TempRoot, "*", "child.pid"))
				if len(files) > 0 {
					data, _ := os.ReadFile(files[0])
					_, _ = fmt.Sscan(string(data), &pid)
				}
				if mode == "locked-child" {
					ready, _ := filepath.Glob(filepath.Join(opts.TempRoot, "*", "held.lock.ready"))
					locked = len(ready) == 1
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			err := <-done
			if pid == 0 || !locked {
				t.Fatal("child was never started or did not acquire file lock")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			assertProcessExited(t, pid)
			assertNoTasks(t, opts.TempRoot)
		})
	}
}
func TestFileParametersAreTaskLocalAndDetachTransfersOwnership(t *testing.T) {
	m := fakeModule(t)
	opts := options(t, m)
	input := fakeInput(t)
	file := filepath.Join(t.TempDir(), "weights.bin")
	if err := os.WriteFile(file, []byte("metadata fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	req := request("files")
	req.Parameters["config"] = file
	req.Parameters["group"] = map[string]any{"weights": file}
	result, err := Run(context.Background(), m, req, input, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := result.DetachDirectory()
	if dir == "" {
		t.Fatal("missing owned directory")
	}
	if err = result.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(result.OutputPath); err != nil {
		t.Fatal("detach cleaned accepted B")
	}
	if err = os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	assertNoTasks(t, opts.TempRoot)
}
func TestRepeated100RunsLeaveNoTasksOrHandles(t *testing.T) {
	if testing.Short() {
		t.Skip("100-process lifecycle stress")
	}
	// Fix scheduler parallelism so lazily-created per-P GC workers do not make
	// process handle counts depend on the host's (possibly very large) CPU count.
	procs := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(procs)
	m := fakeModule(t)
	input := fakeInput(t)
	opts := options(t, m)
	runtime.GC()
	before := processHandles()
	warmed := -1
	var warmMemory runtime.MemStats
	for i := 0; i < 100; i++ {
		result, err := Run(context.Background(), m, request("copy"), input, opts, nil)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if err = result.Cleanup(); err != nil {
			t.Fatal(err)
		}
		if (i+1)%20 == 0 {
			runtime.GC()
			t.Logf("run %d: handles=%d", i+1, processHandles())
			if i == 39 {
				warmed = processHandles()
				runtime.ReadMemStats(&warmMemory)
			}
		}
	}
	assertNoTasks(t, opts.TempRoot)
	runtime.GC()
	after := processHandles()
	var afterMemory runtime.MemStats
	runtime.ReadMemStats(&afterMemory)
	// The runtime may lazily create worker threads/events during the first
	// executions. Check the steady-state tail, not the cold test-process count.
	if warmed >= 0 && after > warmed+4 {
		t.Fatalf("persistent process handles grew: before=%d warm=%d after=%d", before, warmed, after)
	}
	if warmMemory.HeapAlloc > 0 && afterMemory.HeapAlloc > warmMemory.HeapAlloc+8<<20 {
		t.Fatalf("retained heap grew across repeated runs: warm=%d after=%d", warmMemory.HeapAlloc, afterMemory.HeapAlloc)
	}
	records, _ := filepath.Glob(filepath.Join(opts.RecordDirectory, "*.json"))
	if len(records) != 100 {
		t.Fatalf("run ID collisions: %d records", len(records))
	}
}

// This optional integration entry is algorithm-neutral. Setting a manifest
// alone never authorizes execution: the user must explicitly supply the exact
// combined fingerprint. Private modules and test results remain outside Git.
func TestAuthorizedInstalledModuleIntegration(t *testing.T) {
	manifest := os.Getenv("SEISFORGE_ALGORITHM_TEST_MANIFEST")
	if manifest == "" {
		t.Skip("no explicitly selected external integration module")
	}
	hostVersion := os.Getenv("SEISFORGE_ALGORITHM_TEST_HOST_VERSION")
	if hostVersion == "" {
		hostVersion = "1.10.3"
	}
	r := registry.New(hostVersion)
	issues, err := r.Scan(context.Background(), []string{filepath.Dir(manifest)})
	if err != nil {
		t.Fatal(err)
	}
	modules := r.Modules()
	if len(issues) != 0 || len(modules) != 1 {
		t.Fatalf("passive registry discovery: modules=%d issues=%+v", len(modules), issues)
	}
	m := modules[0]
	t.Logf("registry host=%s module=%s minimum=%s manifest=%s executable=%s fingerprint=%s", hostVersion, m.Descriptor().ID, m.Descriptor().MinimumHostVersion, m.ManifestSHA256(), m.ExecutableSHA256(), m.Fingerprint())
	trusted := os.Getenv("SEISFORGE_ALGORITHM_TEST_FINGERPRINT")
	if trusted != m.Fingerprint() {
		t.Fatalf("explicit integration authorization required; module fingerprint: %s", m.Fingerprint())
	}
	path := filepath.Join(t.TempDir(), "integration.sfdata")
	meta := artifact.Metadata{DType: "float64", Shape: []uint64{15, 128}, Axes: []string{"trace", "sample"}, SampleIntervalUS: 2000}
	traces := make([]artifact.TraceMetadata, 15)
	for i := range traces {
		traces[i] = artifact.TraceMetadata{TraceID: int64(100 + i), Inline: 1870, Crossline: int32(184 + i), X: float64(i) * 25, Y: 0, CoordinateValid: true}
	}
	if err = artifact.Write(context.Background(), path, meta, traces, func(trace int64) ([]float64, []bool, error) {
		values := make([]float64, 128)
		for sample := range values {
			values[sample] = math.Sin(float64(sample)*0.17+float64(trace)*0.2) + 0.1*math.Cos(float64(sample)*1.37+float64(trace)*2.31)
		}
		return values, nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	params := map[string]any{}
	if raw := os.Getenv("SEISFORGE_ALGORITHM_TEST_PARAMS"); raw != "" {
		if err = json.Unmarshal([]byte(raw), &params); err != nil {
			t.Fatal(err)
		}
	}
	opts := options(t, m)
	opts.Timeout = 2 * time.Minute
	opts.AuthorizedFingerprint = trusted
	result, err := Run(context.Background(), m, api.RunRequest{Parameters: params}, path, opts, func(e api.ProgressEvent) {
		if e.Type != "progress" || e.Value == 1 {
			t.Logf("%s %.2f %s", e.Type, e.Value, e.Message)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Cleanup()
	if result.Record.Status != "success" || result.Record.InputFingerprint == "" || result.Record.OutputFingerprint == "" {
		t.Fatalf("invalid result provenance: %+v", result.Record)
	}
	input, err := artifact.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := artifact.Open(result.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err = artifact.CheckCompatibility(input, output); err != nil {
		t.Fatal(err)
	}
	for _, extra := range result.Outputs {
		if extra.Role != "residual" {
			continue
		}
		residual, e := artifact.Open(extra.Path)
		if e != nil {
			t.Fatal(e)
		}
		for tr := int64(0); tr < input.TraceCount(); tr++ {
			a, _, _ := input.ReadTraceWindow(tr, 0, 128)
			b, _, _ := output.ReadTraceWindow(tr, 0, 128)
			r, _, _ := residual.ReadTraceWindow(tr, 0, 128)
			for j := range a {
				if math.Abs((a[j]-b[j])-r[j]) > 1e-6 {
					residual.Close()
					t.Fatalf("residual identity differs at trace %d sample %d", tr, j)
				}
			}
		}
		residual.Close()
	}
}
