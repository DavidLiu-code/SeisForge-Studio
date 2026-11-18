package segy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// StreamFloatSectionOptions writes one processed trace at a time. Samples must
// return exactly SampleCount values in the supplied physical trace order. The
// callback's storage is never retained. Existing export entry points are not
// changed by this processing-only adapter.
type StreamFloatSectionOptions struct {
	TraceIndices  []int64
	SampleStart   int
	SampleCount   int
	Samples       func(outputIndex int, sourceTrace int64) ([]float32, error)
	Context       context.Context
	Progress      func(done, total int)
	BeforePublish func() error // final source-identity/token gate, before no-clobber publication
}

// SourceFileInfo returns identity metadata for the actual open handle, not a
// later path lookup. Callers can detect replacements as well as size/mtime
// changes without opening or reading an amplitude volume.
func (s *File) SourceFileInfo() (os.FileInfo, error) {
	if s == nil || s.f == nil {
		return nil, errors.New("nil SEG-Y file")
	}
	if source, ok := s.f.(interface{ SourceFileInfo() (os.FileInfo, error) }); ok {
		return source.SourceFileInfo()
	}
	return s.f.Stat()
}

// ExportFloatSectionStream emits IEEE float32 samples in a unique temporary
// file, validates it through the normal Reader, and publishes without replacing
// an existing path. Only O(samples per trace) amplitude storage is required.
func (s *File) ExportFloatSectionStream(path string, o StreamFloatSectionOptions) (err error) {
	if s == nil || s.f == nil {
		return errors.New("nil SEG-Y file")
	}
	if len(o.TraceIndices) == 0 || o.Samples == nil {
		return errors.New("processed section needs traces and a sample provider")
	}
	ns := o.SampleCount
	if ns < 1 || ns > 65535 || o.SampleStart < 0 || o.SampleStart > s.Info.SamplesPerTrace || ns > s.Info.SamplesPerTrace-o.SampleStart {
		return errors.New("processed section sample window is invalid")
	}
	shiftUS := int64(o.SampleStart) * int64(s.Info.SampleIntervalUS)
	if shiftUS%1000 != 0 {
		return errors.New("processed window starts at a fractional millisecond; SEG-Y DelayMS cannot preserve the exact time origin")
	}
	for _, trace := range o.TraceIndices {
		if trace < 0 || trace >= s.Info.TraceCount {
			return errors.New("processed section trace is out of range")
		}
	}
	target, err := cleanExportPath(path)
	if err != nil {
		return err
	}
	if same, e := sameExportPath(s.Info.Path, target); e != nil || same {
		if e != nil {
			return e
		}
		return errors.New("processed section cannot replace its source")
	}
	if _, e := os.Lstat(target); e == nil {
		return errors.New("processed section destination already exists")
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if err = exportContextError(o.Context); err != nil {
		return err
	}
	header, err := s.exportHeader(ns, 5)
	if err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(target), ".processed-stream-*.tmp")
	if err != nil {
		return err
	}
	temp := out.Name()
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, out.Close())
		}
		if e := os.Remove(temp); e != nil && !errors.Is(e, os.ErrNotExist) {
			err = errors.Join(err, e)
		}
	}()
	w := bufio.NewWriterSize(out, 256<<10)
	if _, err = w.Write(header); err != nil {
		return err
	}
	raw := make([]byte, ns*4)
	for i, trace := range o.TraceIndices {
		if err = exportContextError(o.Context); err != nil {
			return err
		}
		values, e := o.Samples(i, trace)
		if e != nil {
			return e
		}
		if len(values) != ns {
			return fmt.Errorf("processed trace %d has %d samples, expected %d", i, len(values), ns)
		}
		for j, value := range values {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return fmt.Errorf("processed trace %d sample %d is not finite", i, j)
			}
			putF32(raw[j*4:(j+1)*4], value, s.Info.Endian)
		}
		th, e := s.rawTraceHeader(trace)
		if e != nil {
			return e
		}
		delay := int64(signed16(th[108:110], s.Info.Endian)) + shiftUS/1000
		if delay < math.MinInt16 || delay > math.MaxInt16 {
			return fmt.Errorf("processed trace %d time origin exceeds the SEG-Y DelayMS range", trace)
		}
		s.updateExportTraceHeader(th, ns, o.SampleStart)
		if _, err = w.Write(th); err != nil {
			return err
		}
		if _, err = w.Write(raw); err != nil {
			return err
		}
		if o.Progress != nil {
			o.Progress(i+1, len(o.TraceIndices))
		}
	}
	if err = exportContextError(o.Context); err != nil {
		return err
	}
	if err = w.Flush(); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	err = out.Close()
	closed = true
	if err != nil {
		return err
	}
	if err = validateExportSectionFormat(temp, s, o.TraceIndices, ns, o.SampleStart, 5, 4); err != nil {
		return err
	}
	if err = exportContextError(o.Context); err != nil {
		return err
	}
	if o.BeforePublish != nil {
		if err = o.BeforePublish(); err != nil {
			return err
		}
	}
	if err = exportContextError(o.Context); err != nil {
		return err
	}
	// A hard link installs the completed file atomically and refuses a target
	// created since the preflight check. Unlike os.Rename this is no-clobber on
	// both Unix and Windows. Both names are in the same owned temporary folder.
	return os.Link(temp, target)
}
