package poststack

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// SectionRequest is an immutable job snapshot. Indices are physical, zero
// based, and kept in the caller's logical order. Sample bounds are inclusive.
// ResidualPath is used only by the generic residual subtraction compatibility
// operation; this function never invokes or translates a private algorithm.
type SectionRequest struct {
	SourcePath             string
	TempDir                string // optional parent for this job's unique private directory
	TraceIndices           []int64
	SampleStart, SampleEnd int
	AlgorithmID            string
	Direction              string // inline, crossline or trace_order; empty defaults to trace_order
	Gain                   float64
	ResidualPath           string
}

// SectionResult owns a private temporary directory. It is not a public export.
// No Reader, samples or GUI objects are retained. Cleanup never follows the
// exported Dir field, so modifying metadata cannot redirect deletion.
type SectionResult struct {
	Dir, Part1, Part2      string
	SourcePath             string
	SourceSize             int64
	SourceModTimeNS        int64
	SampleStart, SampleEnd int
	TraceIndices           []int64
	AlgorithmID            string
	Metadata               Metadata
	Gain                   float64
	ownedDir               string
	sourceIdentity         os.FileInfo
	// Accepted plugin artifacts carry immutable host-side identity/fingerprint
	// gates. They are never serialized into a project or sent to the plugin.
	artifactIdentities   map[string]os.FileInfo
	artifactFingerprints map[string]string
	cleanupMu            sync.Mutex
}

func (r *SectionResult) Cleanup() error {
	return r.cleanupWith(os.RemoveAll)
}

func (r *SectionResult) cleanupWith(remove func(string) error) error {
	if r == nil {
		return nil
	}
	r.cleanupMu.Lock()
	defer r.cleanupMu.Unlock()
	if r.ownedDir == "" {
		return nil
	}
	if err := remove(r.ownedDir); err != nil {
		return err
	}
	r.ownedDir = ""
	return nil
}

// SourceUnchanged must also be checked when a UI accepts a completed result.
func (r *SectionResult) SourceUnchanged() error {
	if r == nil || r.sourceIdentity == nil {
		return errors.New("processing source identity is unavailable")
	}
	return checkSectionSource(r.SourcePath, r.sourceIdentity)
}

func checkSectionSource(path string, before os.FileInfo) error {
	after, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("processing source unavailable: %w", err)
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return errors.New("processing source changed during generation")
	}
	return nil
}

// ProcessSection processes the selected traces with bounded per-trace sample
// storage. Both artifacts are private until their owning session accepts the
// result. Failure/cancellation removes only this job's unique directory.
func ProcessSection(ctx context.Context, req SectionRequest, progress func(done, total int)) (result *SectionResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	req.TraceIndices = append([]int64(nil), req.TraceIndices...)
	if req.AlgorithmID != "copy" && req.AlgorithmID != "gain" && req.AlgorithmID != "residual" {
		return nil, fmt.Errorf("unsupported section algorithm %q", req.AlgorithmID)
	}
	if req.AlgorithmID == "gain" && (math.IsNaN(req.Gain) || math.IsInf(req.Gain, 0) || req.Gain < 0 || req.Gain > 10) {
		return nil, errors.New("gain must be finite and in 0..10")
	}
	if len(req.TraceIndices) == 0 || req.SampleStart < 0 || req.SampleEnd < req.SampleStart {
		return nil, errors.New("empty traces or invalid processing sample window")
	}
	fa, err := segy.Open(req.SourcePath)
	if err != nil {
		return nil, err
	}
	defer fa.Close()
	identity, err := fa.SourceFileInfo()
	if err != nil {
		return nil, err
	}
	if err = checkSectionSource(req.SourcePath, identity); err != nil {
		return nil, err
	}
	if req.SampleEnd >= fa.Info.SamplesPerTrace {
		return nil, errors.New("processing sample window exceeds source")
	}
	shiftUS := int64(req.SampleStart) * int64(fa.Info.SampleIntervalUS)
	if shiftUS%1000 != 0 {
		return nil, errors.New("processing window starts at a fractional millisecond; SEG-Y DelayMS cannot preserve the exact time origin")
	}
	for _, trace := range req.TraceIndices {
		if trace < 0 || trace >= fa.Info.TraceCount {
			return nil, errors.New("processing trace index exceeds source")
		}
		h, e := fa.ReadTraceHeader(trace)
		if e != nil {
			return nil, e
		}
		if (h.SampleCount != 0 && h.SampleCount != fa.Info.SamplesPerTrace) || (h.SampleIntervalUS != 0 && h.SampleIntervalUS != fa.Info.SampleIntervalUS) {
			return nil, fmt.Errorf("source trace %d has an inconsistent sample axis", trace)
		}
		delay := int64(h.DelayMS) + shiftUS/1000
		if delay < math.MinInt16 || delay > math.MaxInt16 {
			return nil, fmt.Errorf("source trace %d cropped time origin exceeds the SEG-Y DelayMS range", trace)
		}
	}
	ns := req.SampleEnd - req.SampleStart + 1
	firstHeader, err := fa.ReadTraceHeader(req.TraceIndices[0])
	if err != nil {
		return nil, err
	}
	direction := req.Direction
	if direction == "" {
		direction = "trace_order"
	}
	metadata := Metadata{TraceCount: len(req.TraceIndices), SamplesPerTrace: ns,
		SampleIntervalUS: fa.Info.SampleIntervalUS, TimeOriginUS: int64(firstHeader.DelayMS)*1000 + shiftUS,
		Direction: direction}
	if direction == "crossline" {
		metadata.InlineCount = len(req.TraceIndices)
		metadata.CrosslineCount = 1
	} else {
		metadata.InlineCount = 1
		metadata.CrosslineCount = len(req.TraceIndices)
	}
	var residual *segy.File
	var residualIdentity os.FileInfo
	if req.AlgorithmID == "residual" {
		if req.ResidualPath == "" {
			return nil, errors.New("residual input is not configured")
		}
		residual, err = segy.Open(req.ResidualPath)
		if err != nil {
			return nil, fmt.Errorf("open residual input: %w", err)
		}
		defer residual.Close()
		residualIdentity, err = residual.SourceFileInfo()
		if err != nil {
			return nil, err
		}
		if err = validateSectionFile(ctx, residual, fa, req.TraceIndices, req.SampleStart, ns, fa.Info.FormatCode, fa.Info.BytesPerSample); err != nil {
			return nil, fmt.Errorf("invalid residual input: %w", err)
		}
	}
	dir, err := os.MkdirTemp(req.TempDir, "seisforge-poststack-result-")
	if err != nil {
		return nil, err
	}
	r := &SectionResult{Dir: dir, Part1: filepath.Join(dir, "Part1.sgy"), Part2: filepath.Join(dir, "Part2.sgy"),
		SourcePath: req.SourcePath, SourceSize: identity.Size(), SourceModTimeNS: identity.ModTime().UnixNano(),
		SampleStart: req.SampleStart, SampleEnd: req.SampleEnd, TraceIndices: append([]int64(nil), req.TraceIndices...),
		AlgorithmID: req.AlgorithmID, Metadata: metadata, Gain: req.Gain, ownedDir: dir, sourceIdentity: identity}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Cleanup())
		}
	}()
	for part, output := range []string{r.Part1, r.Part2} {
		part := part
		err = fa.ExportFloatSectionStream(output, segy.StreamFloatSectionOptions{
			TraceIndices: req.TraceIndices, SampleStart: req.SampleStart, SampleCount: ns, Context: ctx,
			BeforePublish: func() error {
				if e := r.SourceUnchanged(); e != nil {
					return e
				}
				if residual != nil {
					return checkSectionSource(req.ResidualPath, residualIdentity)
				}
				return nil
			},
			Samples: func(i int, trace int64) ([]float32, error) {
				if e := ctx.Err(); e != nil {
					return nil, e
				}
				a, e := fa.ReadTraceWindow(trace, req.SampleStart, req.SampleEnd)
				if e != nil {
					return nil, e
				}
				var rv []float64
				if residual != nil {
					rv, e = residual.ReadTraceWindow(int64(i), 0, ns-1)
					if e != nil {
						return nil, e
					}
				}
				out := make([]float32, ns)
				for j, av := range a {
					if math.IsNaN(av) || math.IsInf(av, 0) {
						return nil, fmt.Errorf("source trace %d sample %d is non-finite", trace, req.SampleStart+j)
					}
					b, delta := av, 0.0
					if req.AlgorithmID == "gain" {
						b = av * req.Gain
						delta = av - b
					} else if residual != nil {
						delta = rv[j]
						b = av - delta
					}
					v := delta
					if part == 1 {
						v = b
					}
					if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > math.MaxFloat32 {
						return nil, fmt.Errorf("processed trace %d sample %d cannot be represented as finite float32", trace, j)
					}
					out[j] = float32(v)
				}
				return out, nil
			},
			Progress: func(done, total int) {
				if progress != nil {
					progress(part*total+done, 2*total)
				}
			},
		})
		if err != nil {
			return nil, err
		}
		if err = r.SourceUnchanged(); err != nil {
			return nil, err
		}
		if err = reopenSection(ctx, output, fa, req.TraceIndices, req.SampleStart, ns); err != nil {
			return nil, err
		}
	}
	if residual != nil {
		if err = checkSectionSource(req.ResidualPath, residualIdentity); err != nil {
			return nil, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = r.SourceUnchanged(); err != nil {
		return nil, err
	}
	return r, nil
}

func reopenSection(ctx context.Context, path string, source *segy.File, traces []int64, start, count int) (err error) {
	f, err := segy.Open(path)
	if err != nil {
		return fmt.Errorf("reopen processed section: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return validateSectionFile(ctx, f, source, traces, start, count, 5, 4)
}

func validateSectionFile(ctx context.Context, f, source *segy.File, traces []int64, start, count, format, bps int) error {
	i, a := f.Info, source.Info
	if i.TraceCount != int64(len(traces)) || i.SamplesPerTrace != count || i.SampleIntervalUS != a.SampleIntervalUS || i.Endian != a.Endian || i.FormatCode != format || i.BytesPerSample != bps || i.DataStart != a.DataStart || i.FileSize != a.DataStart+int64(len(traces))*int64(240+count*bps) {
		return errors.New("processed section count, sample axis, format, endian or length mismatch")
	}
	for n, trace := range traces {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := f.ReadTraceHeader(int64(n))
		if err != nil {
			return err
		}
		ah, err := source.ReadTraceHeader(trace)
		if err != nil {
			return err
		}
		shiftUS := int64(start) * int64(a.SampleIntervalUS)
		if shiftUS%1000 != 0 {
			return errors.New("cropped time origin is not an exact millisecond")
		}
		delay := int64(ah.DelayMS) + shiftUS/1000
		if delay < math.MinInt16 || delay > math.MaxInt16 {
			return errors.New("cropped trace delay cannot be represented without losing the time origin")
		}
		if int64(h.DelayMS) != delay || h.SampleCount != count || h.SampleIntervalUS != a.SampleIntervalUS {
			return fmt.Errorf("processed trace %d sample axis or time origin mismatch", n)
		}
		values, err := f.ReadTraceWindow(int64(n), 0, count-1)
		if err != nil {
			return err
		}
		for _, v := range values {
			if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > math.MaxFloat32 {
				return fmt.Errorf("processed trace %d contains invalid values", n)
			}
		}
	}
	return nil
}
