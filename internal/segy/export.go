package segy

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// SectionExportOptions describes a 2-D seismic section export. TraceIndices
// must be ordered in the desired output trace order. SampleStart/SampleEnd are
// inclusive and refer to the source trace samples.
type SectionExportOptions struct {
	TraceIndices           []int64
	SampleStart, SampleEnd int
	Progress               func(done, total int)
	// Context optionally cancels an export between traces. A nil Context keeps
	// the historical, non-cancellable behaviour.
	Context context.Context
}

// These indirections keep the replacement failure path deterministic in
// package tests. Production always uses the os implementations.
var (
	exportRenameFile = os.Rename
	exportRemoveFile = os.Remove
)

func putU16(dst []byte, v uint16, e Endian) {
	if e == Big {
		binary.BigEndian.PutUint16(dst, v)
	} else {
		binary.LittleEndian.PutUint16(dst, v)
	}
}

func putI16(dst []byte, v int16, e Endian) { putU16(dst, uint16(v), e) }

func putF32(dst []byte, v float32, e Endian) {
	bits := math.Float32bits(v)
	if e == Big {
		binary.BigEndian.PutUint32(dst, bits)
	} else {
		binary.LittleEndian.PutUint32(dst, bits)
	}
}

func clampI16(v int) int16 {
	if v < -32768 {
		return -32768
	}
	if v > 32767 {
		return 32767
	}
	return int16(v)
}

// exportHeader returns the original textual/binary/extended headers with only
// the fields that must change for a cropped section updated. This preserves the
// source SEG-Y endian convention and all project-specific metadata.
func (s *File) exportHeader(ns int, formatCode int) ([]byte, error) {
	if s == nil || s.f == nil {
		return nil, errors.New("nil SEG-Y file")
	}
	if s.Info.DataStart < 3600 || s.Info.DataStart > int64(^uint(0)>>1) {
		return nil, errors.New("invalid SEG-Y data start")
	}
	h := make([]byte, int(s.Info.DataStart))
	if _, err := s.f.ReadAt(h, 0); err != nil {
		return nil, err
	}
	bh := h[3200:3600]
	dt := uint16(s.Info.SampleIntervalUS)
	putU16(bh[16:18], dt, s.Info.Endian) // sample interval
	putU16(bh[18:20], dt, s.Info.Endian) // original sample interval
	putU16(bh[20:22], uint16(ns), s.Info.Endian)
	putU16(bh[22:24], uint16(ns), s.Info.Endian)
	putU16(bh[24:26], uint16(formatCode), s.Info.Endian)
	// SEG-Y Rev1 fixed-length trace flag (3503-3504) = 1.
	putU16(bh[302:304], 1, s.Info.Endian)
	return h, nil
}

func (s *File) rawTraceHeader(trace int64) ([]byte, error) {
	if trace < 0 || trace >= s.Info.TraceCount {
		return nil, errors.New("trace index out of range")
	}
	h := make([]byte, 240)
	off := s.Info.DataStart + trace*s.Info.TraceBytes
	if _, err := s.f.ReadAt(h, off); err != nil {
		return nil, err
	}
	return h, nil
}

func (s *File) rawTraceWindow(trace int64, sm0, sm1 int) ([]byte, error) {
	if trace < 0 || trace >= s.Info.TraceCount {
		return nil, errors.New("trace index out of range")
	}
	if sm0 < 0 || sm1 < sm0 || sm1 >= s.Info.SamplesPerTrace {
		return nil, errors.New("sample window out of range")
	}
	n := sm1 - sm0 + 1
	raw := make([]byte, n*s.Info.BytesPerSample)
	off := s.Info.DataStart + trace*s.Info.TraceBytes + 240 + int64(sm0*s.Info.BytesPerSample)
	if _, err := s.f.ReadAt(raw, off); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *File) updateExportTraceHeader(h []byte, ns, sampleStart int) {
	if len(h) < 240 {
		return
	}
	// Trace header bytes 115-116: samples in this trace; 117-118: dt (us).
	putU16(h[114:116], uint16(ns), s.Info.Endian)
	putU16(h[116:118], uint16(s.Info.SampleIntervalUS), s.Info.Endian)
	// Trace header bytes 109-110: delay recording time in ms. Preserve any
	// existing delay and add the cropped sample offset, so exported windows keep
	// their physical time origin in ordinary SEG-Y viewers.
	delay := signed16(h[108:110], s.Info.Endian)
	delay += int(math.Round(float64(sampleStart*s.Info.SampleIntervalUS) / 1000.0))
	putI16(h[108:110], clampI16(delay), s.Info.Endian)
}

func exportContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func cleanExportPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("empty SEG-Y export path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func sameExportPath(source, target string) (bool, error) {
	sourceAbs, err := cleanExportPath(source)
	if err != nil {
		return false, err
	}
	targetAbs, err := cleanExportPath(target)
	if err != nil {
		return false, err
	}
	if sourceAbs == targetAbs || (runtime.GOOS == "windows" && strings.EqualFold(sourceAbs, targetAbs)) {
		return true, nil
	}
	sourceInfo, sourceErr := os.Stat(sourceAbs)
	targetInfo, targetErr := os.Stat(targetAbs)
	if sourceErr == nil && targetErr == nil && os.SameFile(sourceInfo, targetInfo) {
		return true, nil
	}
	if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
		return false, sourceErr
	}
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return false, targetErr
	}
	return false, nil
}

func (s *File) writeExportSection(out *os.File, hdr []byte, traceIndices []int64, sm0, sm1, ns int, o SectionExportOptions) (err error) {
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, out.Close())
		}
	}()

	w := bufio.NewWriterSize(out, 4<<20)
	if _, err = w.Write(hdr); err != nil {
		return err
	}
	for i, tr := range traceIndices {
		if err = exportContextError(o.Context); err != nil {
			return err
		}
		th, readErr := s.rawTraceHeader(tr)
		if readErr != nil {
			return readErr
		}
		s.updateExportTraceHeader(th, ns, sm0)
		raw, readErr := s.rawTraceWindow(tr, sm0, sm1)
		if readErr != nil {
			return readErr
		}
		if _, err = w.Write(th); err != nil {
			return err
		}
		if _, err = w.Write(raw); err != nil {
			return err
		}
		if o.Progress != nil {
			o.Progress(i+1, len(traceIndices))
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
	return err
}

func validateExportSection(path string, source *File, traceIndices []int64, ns, sampleStart int) (err error) {
	check, err := Open(path)
	if err != nil {
		return fmt.Errorf("reopen exported SEG-Y: %w", err)
	}
	defer func() { err = errors.Join(err, check.Close()) }()

	sourceInfo := source.Info
	traceCount := int64(len(traceIndices))
	expectedSize := sourceInfo.DataStart + traceCount*int64(240+ns*sourceInfo.BytesPerSample)
	if check.Info.FileSize != expectedSize {
		return fmt.Errorf("exported SEG-Y length mismatch: got %d, want %d", check.Info.FileSize, expectedSize)
	}
	if check.Info.TraceCount != traceCount || check.Info.SamplesPerTrace != ns ||
		check.Info.SampleIntervalUS != sourceInfo.SampleIntervalUS || check.Info.FormatCode != sourceInfo.FormatCode ||
		check.Info.BytesPerSample != sourceInfo.BytesPerSample || check.Info.Endian != sourceInfo.Endian ||
		check.Info.DataStart != sourceInfo.DataStart || check.Info.ExtendedTextHeaders != sourceInfo.ExtendedTextHeaders {
		return fmt.Errorf("exported SEG-Y header validation failed: got %+v", check.Info)
	}
	bh := make([]byte, 400)
	if _, err = check.f.ReadAt(bh, 3200); err != nil {
		return fmt.Errorf("read exported binary header: %w", err)
	}
	if u16(bh[16:18], sourceInfo.Endian) != sourceInfo.SampleIntervalUS ||
		u16(bh[20:22], sourceInfo.Endian) != ns ||
		u16(bh[24:26], sourceInfo.Endian) != sourceInfo.FormatCode ||
		u16(bh[302:304], sourceInfo.Endian) != 1 {
		return errors.New("exported SEG-Y binary header fields are inconsistent")
	}
	for _, outputTrace := range []int64{0, traceCount - 1} {
		th, readErr := check.rawTraceHeader(outputTrace)
		if readErr != nil {
			return fmt.Errorf("read exported trace header: %w", readErr)
		}
		if u16(th[114:116], sourceInfo.Endian) != ns || u16(th[116:118], sourceInfo.Endian) != sourceInfo.SampleIntervalUS {
			return errors.New("exported SEG-Y trace header fields are inconsistent")
		}
		sourceHeader, readErr := source.rawTraceHeader(traceIndices[outputTrace])
		if readErr != nil {
			return fmt.Errorf("read source trace header for delay validation: %w", readErr)
		}
		expectedDelay := signed16(sourceHeader[108:110], sourceInfo.Endian)
		expectedDelay += int(math.Round(float64(sampleStart*sourceInfo.SampleIntervalUS) / 1000.0))
		expectedDelay = int(clampI16(expectedDelay))
		if got := signed16(th[108:110], sourceInfo.Endian); got != expectedDelay {
			return fmt.Errorf("exported SEG-Y trace delay mismatch: got %d ms, want %d ms", got, expectedDelay)
		}
	}
	return nil
}

func reserveAdjacentExportPath(target, suffix string) (string, error) {
	placeholder, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+suffix+"-*")
	if err != nil {
		return "", err
	}
	name := placeholder.Name()
	closeErr := placeholder.Close()
	removeErr := exportRemoveFile(name)
	if closeErr != nil || (removeErr != nil && !errors.Is(removeErr, os.ErrNotExist)) {
		return "", errors.Join(closeErr, removeErr)
	}
	return name, nil
}

// commitExportSection replaces an existing destination without first deleting
// it. If installing the new file fails, the old destination is restored from
// an adjacent backup before the error is returned.
func commitExportSection(tempPath, target string) error {
	targetInfo, statErr := os.Stat(target)
	if errors.Is(statErr, os.ErrNotExist) {
		return exportRenameFile(tempPath, target)
	}
	if statErr != nil {
		return statErr
	}
	if targetInfo.IsDir() {
		return errors.New("SEG-Y export destination is a directory")
	}

	backup, err := reserveAdjacentExportPath(target, ".backup")
	if err != nil {
		return err
	}
	if err = exportRenameFile(target, backup); err != nil {
		return fmt.Errorf("preserve existing export destination: %w", err)
	}
	if err = exportRenameFile(tempPath, target); err != nil {
		restoreErr := exportRenameFile(backup, target)
		if restoreErr != nil {
			return errors.Join(fmt.Errorf("install exported SEG-Y: %w", err), fmt.Errorf("restore previous destination from %q: %w", backup, restoreErr))
		}
		return fmt.Errorf("install exported SEG-Y (previous destination restored): %w", err)
	}
	if err = exportRemoveFile(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		// The new, validated destination is already installed. Reporting an
		// export failure here would invite a batch caller to repeat a successful
		// job. Keep the uniquely named old backup as a recovery artifact instead.
		return nil
	}
	return nil
}

// ExportSection writes an exact sample-window copy of the selected source
// traces. Sample bytes are copied without decode/re-encode, so IBM/IEEE/integer
// source formats are preserved bit-for-bit while the SEG-Y header is updated to
// the cropped sample count and delay time. The destination is only replaced
// after an adjacent temporary file has been flushed, synced, closed and
// reopened for structural validation.
func (s *File) ExportSection(path string, o SectionExportOptions) (err error) {
	if s == nil || s.f == nil {
		return errors.New("nil SEG-Y file")
	}
	if len(o.TraceIndices) == 0 {
		return errors.New("no traces selected for export")
	}
	sm0, sm1 := o.SampleStart, o.SampleEnd
	if sm0 < 0 {
		sm0 = 0
	}
	if sm1 < 0 || sm1 >= s.Info.SamplesPerTrace {
		sm1 = s.Info.SamplesPerTrace - 1
	}
	if sm0 > sm1 {
		return errors.New("invalid export sample range")
	}
	target, err := cleanExportPath(path)
	if err != nil {
		return err
	}
	same, err := sameExportPath(s.Info.Path, target)
	if err != nil {
		return err
	}
	if !same {
		sourceInfo, sourceErr := s.f.Stat()
		targetInfo, targetErr := os.Stat(target)
		if sourceErr != nil {
			return sourceErr
		}
		if targetErr == nil {
			same = os.SameFile(sourceInfo, targetInfo)
		} else if !errors.Is(targetErr, os.ErrNotExist) {
			return targetErr
		}
	}
	if same {
		return errors.New("SEG-Y export destination must not be the source file")
	}
	if err = exportContextError(o.Context); err != nil {
		return err
	}
	ns := sm1 - sm0 + 1
	hdr, err := s.exportHeader(ns, s.Info.FormatCode)
	if err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".export-*.tmp")
	if err != nil {
		return err
	}
	tempPath := out.Name()
	cleanup := true
	defer func() {
		if cleanup {
			removeErr := exportRemoveFile(tempPath)
			if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("remove incomplete SEG-Y export %q: %w", tempPath, removeErr))
			}
		}
	}()
	if err = s.writeExportSection(out, hdr, o.TraceIndices, sm0, sm1, ns, o); err != nil {
		return err
	}
	if err = exportContextError(o.Context); err != nil {
		return err
	}
	if err = validateExportSection(tempPath, s, o.TraceIndices, ns, sm0); err != nil {
		return err
	}
	if err = exportContextError(o.Context); err != nil {
		return err
	}
	if err = commitExportSection(tempPath, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// ExportDifferenceSection writes A-B as IEEE float32 while retaining A's
// textual and trace headers/geometric metadata. Traces must be coordinate-
// matched and ordered identically.
func ExportDifferenceSection(path string, a, b *File, tracesA, tracesB []int64, sampleStart, sampleEnd int, progress func(done, total int)) error {
	if a == nil || b == nil {
		return errors.New("nil SEG-Y input")
	}
	if len(tracesA) == 0 || len(tracesA) != len(tracesB) {
		return errors.New("A/B trace lists are empty or unmatched")
	}
	if a.Info.SampleIntervalUS != b.Info.SampleIntervalUS {
		return fmt.Errorf("A-B export requires matching sample interval (A=%d us, B=%d us)", a.Info.SampleIntervalUS, b.Info.SampleIntervalUS)
	}
	nsMax := min(a.Info.SamplesPerTrace, b.Info.SamplesPerTrace)
	sm0, sm1 := sampleStart, sampleEnd
	if sm0 < 0 {
		sm0 = 0
	}
	if sm1 < 0 || sm1 >= nsMax {
		sm1 = nsMax - 1
	}
	if sm0 > sm1 {
		return errors.New("invalid A-B sample range")
	}
	ns := sm1 - sm0 + 1
	hdr, err := a.exportHeader(ns, 5) // IEEE float32
	if err != nil {
		return err
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	w := bufio.NewWriterSize(out, 4<<20)
	if _, err = w.Write(hdr); err != nil {
		return err
	}
	raw := make([]byte, ns*4)
	for i := range tracesA {
		th, e := a.rawTraceHeader(tracesA[i])
		if e != nil {
			return e
		}
		a.updateExportTraceHeader(th, ns, sm0)
		va, e := a.readTraceWindow(tracesA[i], sm0, sm1)
		if e != nil {
			return e
		}
		vb, e := b.readTraceWindow(tracesB[i], sm0, sm1)
		if e != nil {
			return e
		}
		for j := 0; j < ns; j++ {
			putF32(raw[j*4:(j+1)*4], float32(va[j]-vb[j]), a.Info.Endian)
		}
		if _, e = w.Write(th); e != nil {
			return e
		}
		if _, e = w.Write(raw); e != nil {
			return e
		}
		if progress != nil {
			progress(i+1, len(tracesA))
		}
	}
	if err = w.Flush(); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}
