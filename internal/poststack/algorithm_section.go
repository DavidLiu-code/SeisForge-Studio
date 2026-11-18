package poststack

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi/artifact"
)

// AlgorithmSectionInput is host-only provenance. Neither the source pathname
// nor its open handle is sent to an external process. The request is a defensive
// copy and the artifact contains only the selected physical traces and samples.
type AlgorithmSectionInput struct {
	InputPath        string
	inputPath        string
	inputFingerprint string
	request          SectionRequest
	metadata         Metadata
	identity         os.FileInfo
}

func (in *AlgorithmSectionInput) SourceUnchanged() error {
	if in == nil || in.identity == nil {
		return errors.New("algorithm source identity is unavailable")
	}
	return checkSectionSource(in.request.SourcePath, in.identity)
}

// CaptureAlgorithmSection writes a float64 artifact one trace at a time. It
// deliberately rejects nonfinite A samples rather than silently replacing them
// with zero or changing the host's valid-mask semantics.
func CaptureAlgorithmSection(ctx context.Context, req SectionRequest, inputPath string, progress func(done, total int)) (*AlgorithmSectionInput, error) {
	return CaptureAlgorithmSectionMapped(ctx, req, inputPath, 189, 193, progress)
}

// CaptureAlgorithmSectionMapped preserves the active host Inline/Crossline
// mapping in artifact geometry, including nonstandard header locations. The
// mapping consists of 1-based signed int32 SEG-Y header words, not plugin UI
// state. A trace_order section may explicitly use 0/0 for unavailable geometry;
// its artifact then preserves real trace IDs and XY without inventing IL/XL.
// The original header bytes are never rewritten.
func CaptureAlgorithmSectionMapped(ctx context.Context, req SectionRequest, inputPath string, inlineByte, crosslineByte int, progress func(done, total int)) (*AlgorithmSectionInput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(req.TraceIndices) == 0 || req.SampleStart < 0 || req.SampleEnd < req.SampleStart {
		return nil, errors.New("algorithm input needs a nonempty trace and sample window")
	}
	unmappedGeometry := req.Direction == "trace_order" && inlineByte == 0 && crosslineByte == 0
	if !unmappedGeometry && (inlineByte < 1 || inlineByte > 237 || crosslineByte < 1 || crosslineByte > 237 || inlineByte == crosslineByte) {
		return nil, errors.New("algorithm source geometry header mapping is invalid")
	}
	req.TraceIndices = append([]int64(nil), req.TraceIndices...)
	a, err := segy.Open(req.SourcePath)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	identity, err := a.SourceFileInfo()
	if err != nil {
		return nil, err
	}
	if err = checkSectionSource(req.SourcePath, identity); err != nil {
		return nil, err
	}
	if req.SampleEnd >= a.Info.SamplesPerTrace {
		return nil, errors.New("algorithm sample window exceeds A")
	}
	shiftUS := int64(req.SampleStart) * int64(a.Info.SampleIntervalUS)
	traces := make([]artifact.TraceMetadata, len(req.TraceIndices))
	for i, trace := range req.TraceIndices {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, e := a.ReadTraceHeader(trace)
		if e != nil {
			return nil, e
		}
		if (h.SampleCount != 0 && h.SampleCount != a.Info.SamplesPerTrace) || (h.SampleIntervalUS != 0 && h.SampleIntervalUS != a.Info.SampleIntervalUS) {
			return nil, fmt.Errorf("A trace %d has an incompatible sample axis", trace)
		}
		x, y := h.CDPX, h.CDPY
		validXY := !math.IsNaN(x) && !math.IsInf(x, 0) && !math.IsNaN(y) && !math.IsInf(y, 0)
		word := func(pos int) int32 {
			if unmappedGeometry {
				return 0
			}
			if a.Info.Endian == segy.Big {
				return int32(binary.BigEndian.Uint32(h.Raw[pos-1 : pos+3]))
			}
			return int32(binary.LittleEndian.Uint32(h.Raw[pos-1 : pos+3]))
		}
		traces[i] = artifact.TraceMetadata{TraceID: trace, Inline: word(inlineByte), Crossline: word(crosslineByte),
			X: x, Y: y, CoordinateValid: validXY, TimeOriginUS: int64(h.DelayMS) * 1000}
	}
	ns := req.SampleEnd - req.SampleStart + 1
	m := artifact.Metadata{DType: "float64", Shape: []uint64{uint64(len(traces)), uint64(ns)}, Axes: []string{"trace", "sample"},
		SampleIntervalUS: int64(a.Info.SampleIntervalUS), TimeOriginUS: traces[0].TimeOriginUS, SampleStart: int64(req.SampleStart)}
	err = artifact.Write(ctx, inputPath, m, traces, func(index int64) ([]float64, []bool, error) {
		if e := ctx.Err(); e != nil {
			return nil, nil, e
		}
		if index%64 == 0 {
			if e := checkSectionSource(req.SourcePath, identity); e != nil {
				return nil, nil, e
			}
		}
		values, e := a.ReadTraceWindow(req.TraceIndices[index], req.SampleStart, req.SampleEnd)
		if e != nil {
			return nil, nil, e
		}
		for j, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, nil, fmt.Errorf("A trace %d sample %d is not finite", req.TraceIndices[index], req.SampleStart+j)
			}
		}
		if progress != nil {
			progress(int(index)+1, len(traces))
		}
		return values, nil, nil
	})
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		_ = os.Remove(inputPath)
		return nil, err
	}
	if err = checkSectionSource(req.SourcePath, identity); err != nil {
		// Write created this new path; do not leave a valid-looking input after
		// the source changes during its final flush.
		_ = os.Remove(inputPath)
		return nil, err
	}
	direction := req.Direction
	if direction == "" {
		direction = "trace_order"
	}
	metadata := Metadata{TraceCount: len(traces), SamplesPerTrace: ns, SampleIntervalUS: a.Info.SampleIntervalUS,
		TimeOriginUS: m.TimeOriginUS + shiftUS, Direction: direction, InlineCount: 1, CrosslineCount: len(traces)}
	if direction == "crossline" {
		metadata.InlineCount, metadata.CrosslineCount = len(traces), 1
	}
	validated, err := artifact.OpenContext(ctx, inputPath)
	if err != nil {
		_ = os.Remove(inputPath)
		return nil, err
	}
	fingerprint := validated.Fingerprint()
	if err := validated.Close(); err != nil {
		_ = os.Remove(inputPath)
		return nil, err
	}
	if err := checkSectionSource(req.SourcePath, identity); err != nil {
		_ = os.Remove(inputPath)
		return nil, err
	}
	return &AlgorithmSectionInput{InputPath: inputPath, inputPath: inputPath, inputFingerprint: fingerprint, request: req, metadata: metadata, identity: identity}, nil
}

func algorithmOutputInDirectory(dir, path string) error {
	if path == "" {
		return errors.New("missing algorithm output")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("algorithm result is outside its owned task directory")
	}
	resolvedDir, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		return err
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return err
	}
	rel, err = filepath.Rel(resolvedDir, resolvedPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("algorithm result symlink escapes its owned task directory")
	}
	return nil
}

// AcceptResult validates the complete result against the captured artifact.
// Ownership is transferred only on success: callers then detach ownedDir from
// their runner result and let SectionResult.Cleanup dispose of it. residual may
// be empty; the host neither invents a private algorithm output nor requires it.
func (in *AlgorithmSectionInput) AcceptResult(ctx context.Context, ownedDir, primary, residual string) (*SectionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := in.SourceUnchanged(); err != nil {
		return nil, err
	}
	// A runner transfers only its own temporary directory, never a source-data
	// folder. Refuse an unsafe ownership transfer even if its output is valid.
	if algorithmOutputInDirectory(ownedDir, in.request.SourcePath) == nil {
		return nil, errors.New("algorithm task ownership cannot include the source SEG-Y")
	}
	if err := algorithmOutputInDirectory(ownedDir, primary); err != nil {
		return nil, err
	}
	if residual != "" {
		if err := algorithmOutputInDirectory(ownedDir, residual); err != nil {
			return nil, err
		}
	}
	input, err := artifact.OpenContext(ctx, in.inputPath)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	if input.Fingerprint() != in.inputFingerprint {
		return nil, errors.New("captured algorithm input changed during processing")
	}
	identities := make(map[string]os.FileInfo)
	fingerprints := make(map[string]string)
	for _, path := range []string{primary, residual} {
		if path == "" {
			continue
		}
		identity, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		output, err := artifact.OpenContext(ctx, path)
		if err != nil {
			return nil, err
		}
		err = artifact.CheckCompatibility(input, output)
		if err == nil {
			// The current Compare raster has no masked-pixel source contract.
			// Reject invalid output samples rather than make up amplitudes.
			for row := int64(0); row < output.TraceCount(); row++ {
				if err = ctx.Err(); err != nil {
					break
				}
				_, valid, e := output.ReadTraceWindow(row, 0, output.SamplesPerTrace())
				if e != nil {
					err = e
					break
				}
				for _, v := range valid {
					if !v {
						err = errors.New("algorithm result contains invalid samples; this Compare source requires complete traces")
						break
					}
				}
				if err != nil {
					break
				}
			}
		}
		fingerprints[path] = output.Fingerprint()
		identities[path] = identity
		// Compare publishes the candidate only after its actual independent
		// reader can be constructed. Reuse the already validated artifact handle
		// (no extra full scan); malformed source headers or unsupported windows
		// must fail the worker before any prior visible B is replaced.
		if err == nil && path == primary {
			req := in.request
			var view *segy.File
			view, err = segy.OpenFloatSection(segy.FloatSectionOptions{SourcePath: req.SourcePath, ValuePath: path,
				TraceIndices: req.TraceIndices, SampleStart: req.SampleStart, SampleEnd: req.SampleEnd}, &algorithmFloatSource{file: output, path: path, identity: identity})
			if err == nil {
				err = view.Close() // closes the artifact too
			} else {
				err = errors.Join(fmt.Errorf("B reader preflight: %w", err), output.Close())
			}
		} else {
			err = errors.Join(err, output.Close())
		}
		if err == nil {
			err = checkSectionSource(path, identity)
		}
		if err != nil {
			return nil, fmt.Errorf("incompatible algorithm output: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := in.SourceUnchanged(); err != nil {
		return nil, err
	}
	req := in.request
	return &SectionResult{Dir: ownedDir, Part1: residual, Part2: primary, SourcePath: req.SourcePath,
		SourceSize: in.identity.Size(), SourceModTimeNS: in.identity.ModTime().UnixNano(),
		SampleStart: req.SampleStart, SampleEnd: req.SampleEnd, TraceIndices: append([]int64(nil), req.TraceIndices...),
		AlgorithmID: req.AlgorithmID, Metadata: in.metadata, ownedDir: ownedDir, sourceIdentity: in.identity,
		artifactIdentities: identities, artifactFingerprints: fingerprints}, nil
}

type algorithmFloatSource struct {
	file     *artifact.File
	path     string
	identity os.FileInfo
}

func (s *algorithmFloatSource) ReadTraceWindow(trace int64, first, last int) ([]float64, error) {
	if err := checkSectionSource(s.path, s.identity); err != nil {
		return nil, fmt.Errorf("algorithm artifact changed: %w", err)
	}
	values, mask, err := s.file.ReadTraceWindow(trace, int64(first), int64(last)+1)
	if err != nil {
		return nil, err
	}
	for _, valid := range mask {
		if !valid {
			return nil, errors.New("invalid sample in algorithm result")
		}
	}
	return values, nil
}
func (s *algorithmFloatSource) Close() error { return s.file.Close() }

// OpenSectionResult is the only generated-B opening path. A normal segy.Open
// does not guess whether arbitrary files are artifacts. Every returned reader
// owns independent A-header and artifact handles and releases both on Close.
func OpenSectionResult(r *SectionResult, path string) (*segy.File, error) {
	if r == nil || path == "" || (path != r.Part1 && path != r.Part2) {
		return nil, errors.New("unknown generated result path")
	}
	if err := r.SourceUnchanged(); err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(path), ".sfdata") {
		return segy.Open(path)
	}
	identity, expectedFingerprint := r.artifactIdentities[path], r.artifactFingerprints[path]
	if identity == nil || expectedFingerprint == "" {
		return nil, errors.New("algorithm artifact has no accepted identity")
	}
	if err := checkSectionSource(path, identity); err != nil {
		return nil, fmt.Errorf("algorithm artifact changed: %w", err)
	}
	a, err := artifact.Open(path)
	if err != nil {
		return nil, err
	}
	if a.Fingerprint() != expectedFingerprint {
		a.Close()
		return nil, errors.New("algorithm artifact fingerprint changed after acceptance")
	}
	m := a.Metadata()
	traces := a.TraceMetadata()
	if a.TraceCount() != int64(len(r.TraceIndices)) || a.SamplesPerTrace() != int64(r.Metadata.SamplesPerTrace) || m.SampleIntervalUS != int64(r.Metadata.SampleIntervalUS) || m.TimeOriginUS+m.SampleStart*m.SampleIntervalUS != r.Metadata.TimeOriginUS || m.SampleStart != int64(r.SampleStart) {
		a.Close()
		return nil, errors.New("algorithm result metadata no longer matches its source window")
	}
	for i, physical := range r.TraceIndices {
		if traces[i].TraceID != physical {
			a.Close()
			return nil, errors.New("algorithm result trace order no longer matches A")
		}
	}
	f, err := segy.OpenFloatSection(segy.FloatSectionOptions{SourcePath: r.SourcePath, ValuePath: path,
		TraceIndices: r.TraceIndices, SampleStart: r.SampleStart, SampleEnd: r.SampleEnd}, &algorithmFloatSource{file: a, path: path, identity: identity})
	if err != nil {
		a.Close()
	}
	return f, err
}

// ExportSectionResult preserves artifact precision using IEEE float64 SEG-Y
// format 6. Publication is no-clobber and source identity is checked again
// immediately before the atomic link. Existing SEG-Y results keep their format.
func ExportSectionResult(ctx context.Context, r *SectionResult, path, target string) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, e := os.Lstat(target); e == nil {
		return errors.New("result export destination already exists")
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	f, err := OpenSectionResult(r, path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if int64(r.SampleStart)*int64(r.Metadata.SampleIntervalUS)%1000 != 0 {
		return errors.New("SEG-Y export cannot preserve a fractional-millisecond cropped origin; artifact remains exact")
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".algorithm-export-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if e := os.Remove(tmpPath); e != nil && !errors.Is(e, os.ErrNotExist) {
			err = errors.Join(err, e)
		}
	}()
	if err := tmp.Close(); err != nil {
		return err
	}
	traces := make([]int64, len(r.TraceIndices))
	for i := range traces {
		traces[i] = int64(i)
	}
	if err = f.ExportSection(tmpPath, segy.SectionExportOptions{TraceIndices: traces, SampleStart: 0, SampleEnd: f.Info.SamplesPerTrace - 1, Context: ctx}); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = r.SourceUnchanged(); err != nil {
		return err
	}
	return os.Link(tmpPath, target)
}
