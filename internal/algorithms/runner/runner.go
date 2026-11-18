// Package runner executes explicitly authorized external algorithm modules.
// Process-tree containment is lifecycle management, NOT a security sandbox.
package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/provenance"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/registry"
	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
	"github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi/artifact"
)

const DefaultTimeout = 30 * time.Minute
const DescribeTimeout = 5 * time.Second
const maxLineBytes = 64 << 10
const maxOutputBytes = 8 << 20
const maxLogBytes = 1 << 20

type Options struct {
	AuthorizedFingerprint string
	Timeout               time.Duration
	TempRoot              string
	RecordDirectory       string
}

type Result struct {
	Directory                 string
	OutputPath                string
	Outputs                   []api.AlgorithmOutput
	Record                    api.AlgorithmRunRecord
	RecordPath, RecordWarning string
	mu                        sync.Mutex
	owned                     string
}

// RecordedError makes a failure's saved record reachable without returning a
// partial Result. It preserves the underlying error for timeout/cancel checks.
type RecordedError struct {
	Err        error
	RecordPath string
}

func (e *RecordedError) Error() string { return e.Err.Error() }
func (e *RecordedError) Unwrap() error { return e.Err }

func (r *Result) Cleanup() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.owned == "" {
		return nil
	}
	err := os.RemoveAll(r.owned)
	if err == nil {
		r.owned = ""
	}
	return err
}

// DetachDirectory transfers only the successfully created task directory to the
// B-result owner. The receiver must release all Readers before removing it.
func (r *Result) DetachDirectory() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	dir := r.owned
	r.owned = ""
	return dir
}

type ExternalProcessAlgorithm struct {
	Module  registry.Module
	Options Options
}

func (a ExternalProcessAlgorithm) Run(ctx context.Context, request api.RunRequest, input string, report api.ProgressReporter) (*Result, error) {
	return Run(ctx, a.Module, request, input, a.Options, report)
}

func authorize(m registry.Module, opts Options) error {
	if m.IsBuiltin() {
		return m.Verify()
	}
	if opts.AuthorizedFingerprint == "" || opts.AuthorizedFingerprint != m.Fingerprint() {
		return errors.New("algorithm execution requires explicit authorization of this module path and fingerprint")
	}
	return m.Verify()
}

func newRunID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

// Run validates parameters before starting any process. Input and result
// artifacts are streamed and validated, not materialized as an amplitude cube.
// ProgressReporter should return promptly (the host posts UI messages).
func Run(ctx context.Context, m registry.Module, request api.RunRequest, inputPath string, opts Options, report api.ProgressReporter) (*Result, error) {
	return run(ctx, m, request, inputPath, opts, nil, report)
}

// BuiltinExecution is a host-owned capability, not a manifest option. It writes
// the requested artifact and returns metadata describing any temporal context.
type BuiltinExecution func(context.Context, api.RunRequest, string, string, api.ProgressReporter) (BuiltinExecutionInfo, error)
type BuiltinExecutionInfo struct {
	InputContextFingerprint string
	ProcessingScope         *api.AlgorithmProcessingScope
	// Host-only final source check, never serialized or sent to an executable.
	VerifySource func() error
}

// RunBuiltin keeps the same normalized parameters, cancellation, result
// validation, no-clobber artifacts and provenance as external modules. It is
// never an unknown-ID fallback and cannot run an external module in-process.
func RunBuiltin(ctx context.Context, m registry.Module, request api.RunRequest, inputPath string, opts Options, execute BuiltinExecution, report api.ProgressReporter) (*Result, error) {
	return run(ctx, m, request, inputPath, opts, execute, report)
}

func run(ctx context.Context, m registry.Module, request api.RunRequest, inputPath string, opts Options, execute BuiltinExecution, report api.ProgressReporter) (result *Result, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if m.IsBuiltin() != (execute != nil) {
		return nil, errors.New("builtin algorithm requires its explicit host executor; external modules cannot use it")
	}
	if err = authorize(m, opts); err != nil {
		return nil, err
	}
	desc := m.Descriptor()
	if request.ProtocolVersion != "" && request.ProtocolVersion != api.ProtocolVersion {
		return nil, errors.New("unsupported run request protocol")
	}
	if request.AlgorithmID != "" && request.AlgorithmID != desc.ID || request.AlgorithmVersion != "" && request.AlgorithmVersion != desc.Version {
		return nil, errors.New("run request algorithm differs from authorized module")
	}
	request.Parameters, err = api.NormalizeParameters(desc.Parameters, request.Parameters)
	if err != nil {
		return nil, err
	}
	// Host-generated run IDs avoid caller path injection and accidental record reuse.
	request.RunID, err = newRunID()
	if err != nil {
		return nil, err
	}
	request.ProtocolVersion = api.ProtocolVersion
	request.AlgorithmID = desc.ID
	request.AlgorithmVersion = desc.Version
	input, err := artifact.OpenContext(ctx, inputPath)
	if err != nil {
		return nil, fmt.Errorf("input artifact: %w", err)
	}
	defer input.Close()
	inputKind, err := artifactKind(input.Metadata())
	if err != nil {
		return nil, err
	}
	if err = supportsKind(desc.InputKinds, inputKind, "input"); err != nil {
		return nil, err
	}
	// The host's first result contract preserves the input shape and axes. A
	// module that cannot produce this kind is incompatible before it executes,
	// even if it advertises support for reading the input.
	if err = supportsKind(desc.OutputKinds, inputKind, "output"); err != nil {
		return nil, err
	}
	if request.InputFingerprint != "" && request.InputFingerprint != input.Fingerprint() {
		return nil, errors.New("input fingerprint changed")
	}
	request.InputFingerprint = input.Fingerprint()
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	record := api.AlgorithmRunRecord{Schema: api.RunRecordSchema, RunID: request.RunID, AlgorithmID: desc.ID, AlgorithmVersion: desc.Version, ProtocolVersion: api.ProtocolVersion, Parameters: request.Parameters, ManifestSHA256: m.ManifestSHA256(), ExecutableSHA256: m.ExecutableSHA256(), InputFingerprint: input.Fingerprint(), StartedAt: time.Now().UTC(), Status: "failed"}
	defer func() {
		record.FinishedAt = time.Now().UTC()
		record.DurationMS = record.FinishedAt.Sub(record.StartedAt).Milliseconds()
		if err != nil {
			record.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				record.Status = "cancelled"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				record.Status = "timeout"
			}
		}
		path, saveErr := provenance.Save(opts.RecordDirectory, record)
		if err != nil && path != "" && saveErr == nil {
			err = &RecordedError{Err: err, RecordPath: path}
		}
		if result != nil {
			result.Record = record
			result.RecordPath = path
			if saveErr != nil {
				result.RecordWarning = saveErr.Error()
			}
		}
	}()
	dir, err := os.MkdirTemp(opts.TempRoot, "seisforge-algorithm-")
	if err != nil {
		return nil, err
	}
	owned := true
	defer func() {
		if owned {
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("remove algorithm task directory %s: %w", dir, cleanupErr))
			}
		}
	}()
	localInput := filepath.Join(dir, "input.sfdata")
	localOutput := filepath.Join(dir, "output.sfdata")
	if err = copyInput(ctx, inputPath, localInput); err != nil {
		return nil, err
	}
	// Copy validation detects a changed source while copying, including a file
	// replaced at the original path after the initial read-only handle opened.
	copied, err := artifact.OpenContext(ctx, localInput)
	if err != nil {
		return nil, fmt.Errorf("copied input: %w", err)
	}
	if copied.Fingerprint() != request.InputFingerprint {
		copied.Close()
		return nil, errors.New("input changed during copy")
	}
	copied.Close()
	request.Parameters, err = stageFileParameters(ctx, desc.Parameters, request.Parameters, dir)
	if err != nil {
		return nil, err
	}
	requestBytes, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return nil, err
	}
	requestPath := filepath.Join(dir, "request.json")
	if err = os.WriteFile(requestPath, requestBytes, 0600); err != nil {
		return nil, err
	}
	if err = authorize(m, opts); err != nil {
		return nil, err
	}
	var events api.ProgressEvent
	var verifySource func() error
	if execute != nil {
		if report != nil {
			report(api.ProgressEvent{Type: "started", Message: "内置算法处理开始"})
		}
		var info BuiltinExecutionInfo
		info, err = execute(ctx, request, localInput, localOutput, report)
		if err == nil {
			record.InputContextFingerprint, record.ProcessingScope = info.InputContextFingerprint, info.ProcessingScope
			verifySource = info.VerifySource
			events = api.ProgressEvent{Type: "result", Status: "ok", Output: localOutput}
		}
	} else {
		events, err = runProcess(ctx, m, dir, []string{"--run", "--request", requestPath, "--input", localInput, "--output", localOutput}, report)
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if events.Status != "ok" {
		if reason := strings.TrimSpace(events.Message); reason != "" {
			return nil, fmt.Errorf("algorithm did not report success: %s; %s", events.Status, reason)
		}
		return nil, fmt.Errorf("algorithm did not report success: %s", events.Status)
	}
	path, err := resolveOutput(dir, events.Output)
	if err != nil {
		return nil, err
	}
	if path != localOutput {
		return nil, errors.New("algorithm primary output must equal the requested output.sfdata")
	}
	// A module may alter its local copy; it never receives the original source
	// path. Reject such changes so output provenance remains meaningful.
	checkInput, err := artifact.OpenContext(ctx, localInput)
	if err != nil {
		return nil, fmt.Errorf("input was altered: %w", err)
	}
	if checkInput.Fingerprint() != request.InputFingerprint {
		checkInput.Close()
		return nil, errors.New("algorithm modified input artifact")
	}
	checkInput.Close()
	original, err := artifact.OpenContext(ctx, inputPath)
	if err != nil {
		return nil, fmt.Errorf("source input changed: %w", err)
	}
	if original.Fingerprint() != request.InputFingerprint {
		original.Close()
		return nil, errors.New("source input changed during execution")
	}
	original.Close()
	outputs := []api.AlgorithmOutput{{Path: path, Role: "primary"}}
	seen := map[string]bool{path: true}
	for _, extra := range events.Outputs {
		p, e := resolveOutput(dir, extra.Path)
		if e != nil {
			return nil, e
		}
		if seen[p] {
			if p == path && (extra.Role == "primary" || extra.Role == "") {
				continue
			}
			return nil, errors.New("duplicate algorithm output path")
		}
		if extra.Role == "" || extra.Role == "primary" {
			return nil, errors.New("additional output needs a non-primary role")
		}
		seen[p] = true
		extra.Path = p
		outputs = append(outputs, extra)
	}
	for i := range outputs {
		out, e := artifact.OpenContext(ctx, outputs[i].Path)
		if e != nil {
			return nil, fmt.Errorf("output artifact: %w", e)
		}
		kind, kindErr := artifactKind(out.Metadata())
		if kindErr == nil {
			kindErr = supportsKind(desc.OutputKinds, kind, "output")
		}
		if kindErr != nil {
			out.Close()
			return nil, kindErr
		}
		e = artifact.CheckCompatibility(input, out)
		fingerprint := out.Fingerprint()
		out.Close()
		if e != nil {
			return nil, fmt.Errorf("output layout differs from input: %w", e)
		}
		if outputs[i].Fingerprint != "" && outputs[i].Fingerprint != fingerprint {
			return nil, errors.New("reported output fingerprint differs from artifact")
		}
		outputs[i].Fingerprint = fingerprint
	}
	if err = authorize(m, opts); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if verifySource != nil {
		if err = verifySource(); err != nil {
			return nil, fmt.Errorf("source changed before result publication: %w", err)
		}
	}
	record.Status = "success"
	record.OutputFingerprint = outputs[0].Fingerprint
	record.Outputs = append([]api.AlgorithmOutput(nil), outputs...)
	result = &Result{Directory: dir, OutputPath: path, Outputs: outputs, owned: dir}
	owned = false
	return result, nil
}

func artifactKind(meta artifact.Metadata) (string, error) {
	if len(meta.Shape) == 2 && len(meta.Axes) == 2 && meta.Axes[0] == "trace" && meta.Axes[1] == "sample" {
		return "section2d", nil
	}
	if len(meta.Shape) == 3 && len(meta.Axes) == 3 && meta.Axes[0] == "inline" && meta.Axes[1] == "crossline" && meta.Axes[2] == "sample" {
		return "volume3d", nil
	}
	return "", errors.New("artifact dimensions and axes do not identify a supported algorithm data kind")
}

func supportsKind(kinds []string, kind, direction string) error {
	for _, candidate := range kinds {
		if candidate == kind {
			return nil
		}
	}
	return fmt.Errorf("algorithm manifest does not support %s kind %q", direction, kind)
}

func copyInput(ctx context.Context, from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	buf := make([]byte, 128<<10)
	for {
		if err = ctx.Err(); err != nil {
			break
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, err = dst.Write(buf[:n]); err != nil {
				break
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			err = readErr
			break
		}
	}
	if err == nil {
		err = dst.Sync()
	}
	closeErr := dst.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// File parameters are explicit user-selected dependencies. Copy them into the
// owned task so the protocol never exposes their original local paths. Groups
// are traversed using the same validated descriptor, not ad-hoc algorithm IDs.
func stageFileParameters(ctx context.Context, schema []api.ParameterDescriptor, values map[string]any, dir string) (map[string]any, error) {
	out := make(map[string]any, len(values))
	for _, p := range schema {
		value, ok := values[p.ID]
		if !ok {
			continue
		}
		switch p.Type {
		case "group":
			group, err := stageFileParameters(ctx, p.Parameters, value.(map[string]any), filepath.Join(dir, p.ID))
			if err != nil {
				return nil, err
			}
			out[p.ID] = group
		case "file":
			path := value.(string)
			if strings.TrimSpace(path) == "" {
				out[p.ID] = ""
				continue
			}
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("file parameter %s must name a readable regular file", p.ID)
			}
			localDir := filepath.Join(dir, "parameters")
			if err = os.MkdirAll(localDir, 0700); err != nil {
				return nil, err
			}
			target := filepath.Join(localDir, p.ID+filepath.Ext(filepath.Base(path)))
			if err = copyInput(ctx, path, target); err != nil {
				return nil, fmt.Errorf("file parameter %s: %w", p.ID, err)
			}
			out[p.ID] = target
		default:
			out[p.ID] = value
		}
	}
	return out, nil
}

func resolveOutput(dir, path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", errors.New("algorithm did not provide an output path")
	}
	if !filepath.IsAbs(path) {
		if !filepath.IsLocal(path) {
			return "", errors.New("algorithm output escapes task directory")
		}
		path = filepath.Join(dir, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(canonicalDir, resolved)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", errors.New("algorithm output escapes task directory")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("algorithm output is not a regular file")
	}
	return filepath.Clean(abs), nil
}

type boundedWriter struct {
	data  []byte
	limit int
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - len(w.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		w.data = append(w.data, p...)
	}
	return n, nil
}

type eventWriter struct {
	pending []byte
	total   int
	err     error
	result  *api.ProgressEvent
	started bool
	report  func(api.ProgressEvent)
	cancel  context.CancelFunc
}

func (w *eventWriter) fail(err error) {
	if w.err == nil {
		w.err = err
		w.cancel()
	}
}
func (w *eventWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.total += n
	if w.total > maxOutputBytes {
		w.fail(errors.New("algorithm stdout exceeds safety limit"))
	}
	if w.err != nil {
		return n, nil
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			i = len(p)
		}
		if len(w.pending)+i > maxLineBytes {
			w.fail(errors.New("algorithm progress line exceeds safety limit"))
			return n, nil
		}
		w.pending = append(w.pending, p[:i]...)
		p = p[i:]
		if len(p) > 0 {
			w.line()
			w.pending = w.pending[:0]
			p = p[1:]
			if w.err != nil {
				return n, nil
			}
		}
	}
	return n, nil
}
func (w *eventWriter) line() {
	line := bytes.TrimSpace(w.pending)
	if len(line) == 0 {
		return
	}
	var e api.ProgressEvent
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		w.fail(fmt.Errorf("invalid algorithm JSONL: %w", err))
		return
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		w.fail(errors.New("trailing JSONL data"))
		return
	}
	switch e.Type {
	case "started":
		if w.started {
			w.fail(errors.New("duplicate started event"))
			return
		}
		w.started = true
	case "progress":
		if math.IsNaN(e.Value) || math.IsInf(e.Value, 0) || e.Value < 0 || e.Value > 1 {
			w.fail(errors.New("invalid progress value"))
			return
		}
	case "log":
	case "result":
		if w.result != nil {
			w.fail(errors.New("multiple result events"))
			return
		}
		w.result = &e
	default:
		w.fail(fmt.Errorf("unknown algorithm event type %q", e.Type))
		return
	}
	if w.report != nil {
		w.report(e)
	}
}

func runProcess(ctx context.Context, m registry.Module, dir string, args []string, report api.ProgressReporter) (event api.ProgressEvent, err error) {
	var empty api.ProgressEvent
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.ExecutablePath(), args...)
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	// Drain process output independently from UI speed. Progress delivery is
	// bounded and coalesced when the UI cannot keep up; stdout is never blocked.
	var dispatch chan api.ProgressEvent
	var dispatched chan struct{}
	if report != nil {
		dispatch = make(chan api.ProgressEvent, 32)
		dispatched = make(chan struct{})
		go func() {
			defer close(dispatched)
			for e := range dispatch {
				report(e)
			}
		}()
		defer func() { close(dispatch); <-dispatched }()
	}
	out := &eventWriter{cancel: cancel}
	if dispatch != nil {
		out.report = func(e api.ProgressEvent) {
			select {
			case dispatch <- e:
			default:
			}
		}
	}
	stderr := &boundedWriter{limit: maxLogBytes}
	cmd.Stdout = out
	cmd.Stderr = stderr
	afterStart, cleanup, err := prepareAlgorithmProcess(cmd)
	if err != nil {
		return empty, err
	}
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("stop algorithm process tree: %w", cleanupErr))
		}
	}()
	if err = cmd.Start(); err != nil {
		return empty, fmt.Errorf("start algorithm: %w", err)
	}
	if err = afterStart(); err != nil {
		_ = cmd.Cancel()
		_ = cmd.Wait()
		return empty, fmt.Errorf("contain algorithm process: %w", err)
	}
	err = cmd.Wait()
	if len(out.pending) > 0 && out.err == nil {
		out.line()
	}
	if out.err != nil {
		return empty, out.err
	}
	if ctx.Err() != nil {
		return empty, ctx.Err()
	}
	if err != nil {
		return empty, fmt.Errorf("algorithm process failed: %w; %s", err, strings.TrimSpace(string(stderr.data)))
	}
	if out.result == nil {
		return empty, errors.New("algorithm exited without a result event")
	}
	return *out.result, nil
}

// Describe is also executable code and requires exactly the same explicit
// authorization and process-tree containment as Run.
func Describe(ctx context.Context, m registry.Module, opts Options) (description api.AlgorithmDescriptor, err error) {
	var empty api.AlgorithmDescriptor
	if err := authorize(m, opts); err != nil {
		return empty, err
	}
	if m.IsBuiltin() {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		return m.Descriptor(), nil
	}
	timeout := opts.Timeout
	if timeout <= 0 || timeout > DescribeTimeout {
		timeout = DescribeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	desc := m.Descriptor()
	args := []string{"--describe"}
	if m.PackManifestPath() != "" {
		args = append(args, "--algorithm", desc.ID)
	}
	cmd := exec.CommandContext(ctx, m.ExecutablePath(), args...)
	cmd.Dir = m.Directory()
	cmd.WaitDelay = 2 * time.Second
	out := &boundedWriter{limit: registry.MaxManifestBytes + 1}
	stderr := &boundedWriter{limit: maxLogBytes}
	cmd.Stdout = out
	cmd.Stderr = stderr
	afterStart, cleanup, err := prepareAlgorithmProcess(cmd)
	if err != nil {
		return empty, err
	}
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("stop algorithm describe process tree: %w", cleanupErr))
		}
	}()
	if err = cmd.Start(); err != nil {
		return empty, err
	}
	if err = afterStart(); err != nil {
		_ = cmd.Cancel()
		_ = cmd.Wait()
		return empty, err
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return empty, ctx.Err()
	}
	if err != nil {
		return empty, fmt.Errorf("describe failed: %w; %s", err, string(stderr.data))
	}
	if len(out.data) > registry.MaxManifestBytes {
		return empty, errors.New("describe response exceeds size limit")
	}
	reported, err := api.ParseManifest(out.data, desc.MinimumHostVersion)
	if err != nil {
		return empty, err
	}
	declared, _ := json.Marshal(desc)
	actual, _ := json.Marshal(reported)
	if !bytes.Equal(declared, actual) {
		return empty, errors.New("describe response differs from the authorized manifest")
	}
	if err = authorize(m, opts); err != nil {
		return empty, err
	}
	return reported, nil
}
