package segy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// ExportDifferenceSectionFloat64WindowedContext is the explicit artifact-B
// export path. It retains float64 precision, A's headers and original trace
// order. Legacy SEG-Y difference APIs continue to produce float32 unchanged.
// It never replaces existing files and reads at most one requested trace window.
func ExportDifferenceSectionFloat64WindowedContext(ctx context.Context, path string, a, b *File, tracesA, tracesB []int64, sampleStart, sampleEnd, bSampleOffset int, progress func(done, total int)) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = ValidateDifferenceWindow(a, b, tracesA, tracesB, sampleStart, sampleEnd, bSampleOffset); err != nil {
		return err
	}
	if int64(sampleStart)*int64(a.Info.SampleIntervalUS)%1000 != 0 {
		return errors.New("SEG-Y export cannot preserve a fractional-millisecond cropped origin; artifact remains exact")
	}
	if _, e := os.Lstat(path); e == nil {
		return errors.New("difference export destination already exists")
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	identities := make([]os.FileInfo, 2)
	for i, source := range []*File{a, b} {
		identities[i], err = source.SourceFileInfo()
		if err != nil {
			return err
		}
	}
	unchanged := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for i, source := range []*File{a, b} {
			current, err := os.Stat(source.Info.Path)
			if err != nil {
				return err
			}
			initial := identities[i]
			if !os.SameFile(initial, current) || initial.Size() != current.Size() || !initial.ModTime().Equal(current.ModTime()) {
				return errors.New("difference source changed during export")
			}
		}
		return nil
	}
	if err = unchanged(); err != nil {
		return err
	}
	provider := &differenceFloat64Source{a: a, b: b, aTraces: append([]int64(nil), tracesA...), bTraces: append([]int64(nil), tracesB...),
		sampleStart: sampleStart, bOffset: bSampleOffset, unchanged: unchanged}
	view, err := OpenFloatSection(FloatSectionOptions{SourcePath: a.Info.Path, ValuePath: b.Info.Path,
		TraceIndices: tracesA, SampleStart: sampleStart, SampleEnd: sampleEnd}, provider)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, view.Close()) }()
	temp, err := os.CreateTemp(filepath.Dir(path), ".algorithm-difference-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		if e := os.Remove(tempPath); e != nil && !errors.Is(e, os.ErrNotExist) {
			err = errors.Join(err, e)
		}
	}()
	if err = temp.Close(); err != nil {
		return err
	}
	logical := make([]int64, len(tracesA))
	for i := range logical {
		logical[i] = int64(i)
	}
	if err = view.ExportSection(tempPath, SectionExportOptions{TraceIndices: logical, SampleStart: 0,
		SampleEnd: sampleEnd - sampleStart, Context: ctx, Progress: progress}); err != nil {
		return err
	}
	if err = unchanged(); err != nil {
		return err
	}
	return os.Link(tempPath, path)
}

type differenceFloat64Source struct {
	a, b                 *File // independently owned by the caller
	aTraces, bTraces     []int64
	sampleStart, bOffset int
	unchanged            func() error
}

func (s *differenceFloat64Source) Close() error { return nil }

func (s *differenceFloat64Source) ReadTraceWindow(trace int64, first, last int) ([]float64, error) {
	if err := s.unchanged(); err != nil {
		return nil, err
	}
	av, err := s.a.ReadTraceWindow(s.aTraces[trace], s.sampleStart+first, s.sampleStart+last)
	if err != nil {
		return nil, err
	}
	bv, err := s.b.ReadTraceWindow(s.bTraces[trace], s.sampleStart+first-s.bOffset, s.sampleStart+last-s.bOffset)
	if err != nil {
		return nil, err
	}
	if len(av) != len(bv) {
		return nil, errors.New("difference trace windows have different lengths")
	}
	for i := range av {
		av[i] -= bv[i]
		if math.IsNaN(av[i]) || math.IsInf(av[i], 0) {
			return nil, fmt.Errorf("difference is not finite at trace %d sample %d", trace, s.sampleStart+first+i)
		}
	}
	return av, s.unchanged()
}
