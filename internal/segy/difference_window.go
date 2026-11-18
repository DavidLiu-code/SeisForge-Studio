package segy

import (
	"context"
	"fmt"
	"math"
	"os"
)

// ValidateDifferenceWindow checks metadata only. The strict derived-B path uses
// an explicit source sample offset; the legacy Compare APIs remain unchanged.
func ValidateDifferenceWindow(a, b *File, tracesA, tracesB []int64, sampleStart, sampleEnd, bSampleOffset int) error {
	if a == nil || b == nil {
		return fmt.Errorf("A-B requires both SEG-Y sources")
	}
	if len(tracesA) == 0 || len(tracesA) != len(tracesB) {
		return fmt.Errorf("A-B trace pairs are empty or unmatched")
	}
	if a.Info.SampleIntervalUS <= 0 || a.Info.SampleIntervalUS != b.Info.SampleIntervalUS {
		return fmt.Errorf("A-B sample interval mismatch: A=%d us B=%d us", a.Info.SampleIntervalUS, b.Info.SampleIntervalUS)
	}
	if bSampleOffset < 0 || bSampleOffset >= a.Info.SamplesPerTrace || b.Info.SamplesPerTrace > a.Info.SamplesPerTrace-bSampleOffset {
		return fmt.Errorf("A-B sample count/origin mismatch")
	}
	if sampleStart < 0 || sampleEnd < sampleStart || sampleEnd >= a.Info.SamplesPerTrace || sampleStart < bSampleOffset || sampleEnd-bSampleOffset >= b.Info.SamplesPerTrace {
		return fmt.Errorf("A-B current sample window is not covered by both sources")
	}
	shiftMS := int(math.Round(float64(bSampleOffset) * float64(a.Info.SampleIntervalUS) / 1000))
	_, aExact := a.f.(interface{ TraceTimeOriginUS(int64) (int64, error) })
	_, bExact := b.f.(interface{ TraceTimeOriginUS(int64) (int64, error) })
	for i := range tracesA {
		ha, err := a.rawTraceHeader(tracesA[i])
		if err != nil {
			return err
		}
		hb, err := b.rawTraceHeader(tracesB[i])
		if err != nil {
			return err
		}
		for side, v := range []struct {
			h []byte
			f *File
		}{{ha, a}, {hb, b}} {
			ns := u16(v.h[114:116], v.f.Info.Endian)
			dt := u16(v.h[116:118], v.f.Info.Endian)
			if ns != 0 && ns != v.f.Info.SamplesPerTrace {
				return fmt.Errorf("A-B trace %d side %d sample count differs from volume", i+1, side)
			}
			if dt != 0 && dt != v.f.Info.SampleIntervalUS {
				return fmt.Errorf("A-B trace %d side %d sample interval differs from volume", i+1, side)
			}
		}
		delayA := signed16(ha[108:110], a.Info.Endian)
		delayB := signed16(hb[108:110], b.Info.Endian)
		if aExact || bExact {
			originA, err := a.TraceTimeOriginUS(tracesA[i])
			if err != nil {
				return err
			}
			originB, err := b.TraceTimeOriginUS(tracesB[i])
			if err != nil {
				return err
			}
			if originB-originA != int64(bSampleOffset)*int64(a.Info.SampleIntervalUS) {
				return fmt.Errorf("A-B trace %d Time Origin mismatch: A=%d us B=%d us", i+1, originA, originB)
			}
			continue
		}
		if delayB-delayA != shiftMS {
			return fmt.Errorf("A-B trace %d Delay/Time Origin mismatch: A=%d ms B=%d ms", i+1, delayA, delayB)
		}
	}
	return nil
}

// ExportDifferenceSectionWindowedContext is the processing-workspace export.
// It reads one matched pair at a time and keeps A's geometry/header semantics.
// Ordinary 2D/3D callers retain ExportDifferenceSection's historical behavior.
func ExportDifferenceSectionWindowedContext(ctx context.Context, path string, a, b *File, tracesA, tracesB []int64, sampleStart, sampleEnd, bSampleOffset int, progress func(done, total int)) error {
	tracesA, tracesB = append([]int64(nil), tracesA...), append([]int64(nil), tracesB...)
	if err := ValidateDifferenceWindow(a, b, tracesA, tracesB, sampleStart, sampleEnd, bSampleOffset); err != nil {
		return err
	}
	if same, err := sameExportPath(b.Info.Path, path); err != nil {
		return err
	} else if same {
		return fmt.Errorf("difference destination must not replace B")
	}
	aIdentity, err := a.SourceFileInfo()
	if err != nil {
		return err
	}
	bIdentity, err := b.SourceFileInfo()
	if err != nil {
		return err
	}
	unchanged := func() error {
		for _, source := range []struct {
			file    *File
			initial os.FileInfo
		}{{a, aIdentity}, {b, bIdentity}} {
			current, err := source.file.SourceFileInfo()
			if err != nil {
				return err
			}
			pathInfo, err := os.Stat(source.file.Info.Path)
			if err != nil {
				return err
			}
			if !os.SameFile(source.initial, current) || !os.SameFile(source.initial, pathInfo) || source.initial.Size() != current.Size() || !source.initial.ModTime().Equal(current.ModTime()) {
				return fmt.Errorf("difference source changed during export")
			}
		}
		return nil
	}
	return a.ExportFloatSectionStream(path, StreamFloatSectionOptions{
		TraceIndices: append([]int64(nil), tracesA...), SampleStart: sampleStart, SampleCount: sampleEnd - sampleStart + 1, Context: ctx, Progress: progress, BeforePublish: unchanged,
		Samples: func(i int, trace int64) ([]float32, error) {
			if err := unchanged(); err != nil {
				return nil, err
			}
			av, err := a.ReadTraceWindow(trace, sampleStart, sampleEnd)
			if err != nil {
				return nil, err
			}
			bv, err := b.ReadTraceWindow(tracesB[i], sampleStart-bSampleOffset, sampleEnd-bSampleOffset)
			if err != nil {
				return nil, err
			}
			values := make([]float32, len(av))
			for j := range av {
				v := av[j] - bv[j]
				if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > math.MaxFloat32 {
					return nil, fmt.Errorf("difference value is outside IEEE float32 at trace %d sample %d", i, j)
				}
				values[j] = float32(v)
			}
			return values, unchanged()
		},
	})
}
