package segy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// NumericalExportTrace refers to one actual source trace and its first
// selected sample, independently of data decimation, panel size and labeling.
type NumericalExportTrace struct {
	Trace int64
	First int
}

// NumericalSectionExportOptions describes a fixed-ns, fixed-dt IEEE float64
// SEG-Y. ReadValues supplies one already-selected *numerical* row and must
// not use a palette, display gain, or reapply an already-committed filter.
type NumericalSectionExportOptions struct {
	Traces                                    []NumericalExportTrace
	SampleCount                               int
	ReadValues                                func(row int) ([]float64, error)
	CheckCurrent                              func() error
	Progress                                  func(done, total int)
	Provenance, OriginalStem                  string
	GeometryInlineByte, GeometryCrosslineByte int
	TraceOrder                                bool
	ReplaceExisting                           os.FileInfo // nil is always atomic no-overwrite
}

func (s *File) numericalExportHeader(ns int, provenance string, m NumericalExportMetadata) ([]byte, error) {
	h, err := s.exportHeader(ns, 6)
	if err != nil {
		return h, err
	}
	if s.Info.ExtendedTextHeaders >= 9998 {
		return nil, errors.New("cannot add SEG-Y processing provenance: too many extended headers")
	}
	// Preserve the original volume and extended headers; add an ASCII
	// processing record instead of overwriting a conventional C-line.
	extension, err := numericalMetadataBlock(m, provenance)
	if err != nil {
		return nil, err
	}
	putU16(h[3504:3506], uint16(len(h[3600:])/3200+1), s.Info.Endian)
	return append(h, extension...), nil
}

func verifyNumericalExportTarget(path string, approved, source os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && approved == nil {
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("confirmed SEG-Y destination was moved or removed; select it again")
		}
		return err
	}
	if approved == nil {
		return fmt.Errorf("SEG-Y destination already exists; select a new filename: %w", os.ErrExist)
	}
	if !current.Mode().IsRegular() || !approved.Mode().IsRegular() || !os.SameFile(current, approved) ||
		current.Size() != approved.Size() || !current.ModTime().Equal(approved.ModTime()) {
		return errors.New("confirmed SEG-Y destination changed; select it again")
	}
	if source != nil && os.SameFile(current, source) {
		return errors.New("cannot replace the source SEG-Y")
	}
	return nil
}

// ExportNumericalSection verifies exact cropped Delay, fixed-axis headers,
// byte layout and content by reopening the completed adjacent temporary file.
// It never replaces the source. An existing output may only be replaced when
// its previously approved, unchanged identity is supplied. Non-finite values are emitted as
// IEEE NaN (missing), not removed, zero-filled, or used as valid difference.
func (s *File) ExportNumericalSection(ctx context.Context, path string, o NumericalSectionExportOptions) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.f == nil {
		return errors.New("SEG-Y source is not available")
	}
	ns, dt := o.SampleCount, s.Info.SampleIntervalUS
	if len(o.Traces) == 0 || ns < 1 || ns > 65535 || dt < 1 || dt > 65535 || o.ReadValues == nil {
		return errors.New("invalid numerical SEG-Y dimensions, sample rate, or source")
	}
	target, err := cleanExportPath(path)
	if err != nil {
		return err
	}
	source, err := s.f.Stat()
	if err != nil {
		return err
	}
	if err := verifyNumericalExportTarget(target, o.ReplaceExisting, source); err != nil {
		return err
	}
	checkCurrent := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if o.CheckCurrent != nil {
			return o.CheckCurrent()
		}
		return nil
	}
	if err = checkCurrent(); err != nil {
		return err
	}
	m := NumericalExportMetadata{OriginalStem: numericalSafeStem(o.OriginalStem), InlineByte: o.GeometryInlineByte,
		CrosslineByte: o.GeometryCrosslineByte, TraceOrder: o.TraceOrder}
	if m.OriginalStem == "" {
		m.OriginalStem = s.NumericalOriginalStem()
	}
	if !m.HasGeometry() && (m.InlineByte != 0 || m.CrosslineByte != 0) {
		return errors.New("invalid numerical SEG-Y Inline/Crossline header mapping")
	}
	header, err := s.numericalExportHeader(ns, o.Provenance, m)
	if err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".numerical-*.tmp")
	if err != nil {
		return err
	}
	temp := out.Name()
	defer func() {
		if e := os.Remove(temp); e != nil && !errors.Is(e, os.ErrNotExist) {
			err = errors.Join(err, e)
		}
	}()
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, out.Close())
		}
	}()
	sha := sha256.New()
	w := bufio.NewWriterSize(io.MultiWriter(out, sha), 1<<20)
	if _, err = w.Write(header); err != nil {
		return err
	}
	raw := make([]byte, ns*8)
	order := binary.ByteOrder(binary.BigEndian)
	if s.Info.Endian != Big {
		order = binary.LittleEndian
	}
	geometryHash := sha256.New()
	var geoBytes [8]byte
	for i, row := range o.Traces {
		if err = checkCurrent(); err != nil {
			return err
		}
		if row.First < 0 || row.First > s.Info.SamplesPerTrace-ns {
			return fmt.Errorf("trace %d sample selection exceeds source coverage", row.Trace+1)
		}
		th, e := s.rawTraceHeader(row.Trace)
		if e != nil {
			return e
		}
		if count := u16(th[114:116], s.Info.Endian); count != 0 && row.First+ns > count {
			return fmt.Errorf("trace %d has only %d samples", row.Trace+1, count)
		}
		if interval := u16(th[116:118], s.Info.Endian); interval != 0 && interval != dt {
			return fmt.Errorf("trace %d uses %d µs, not the SEG-Y export interval %d µs", row.Trace+1, interval, dt)
		}
		t0, e := s.TraceTimeOriginUS(row.Trace)
		if e != nil {
			return e
		}
		t0 += int64(row.First) * int64(dt)
		if t0%1000 != 0 || t0/1000 < math.MinInt16 || t0/1000 > math.MaxInt16 {
			return fmt.Errorf("trace %d start %g ms cannot be represented exactly by SEG-Y Delay", row.Trace+1, float64(t0)/1000)
		}
		if m.HasGeometry() {
			copyNumericalGeometryWords(geoBytes[:], th, m)
			_, _ = geometryHash.Write(geoBytes[:])
		}
		putU16(th[114:116], uint16(ns), s.Info.Endian)
		putU16(th[116:118], uint16(dt), s.Info.Endian)
		putI16(th[108:110], int16(t0/1000), s.Info.Endian)
		if m.HasGeometry() {
			var after [8]byte
			copyNumericalGeometryWords(after[:], th, m)
			if after != geoBytes {
				return fmt.Errorf("trace %d has a geometry word overlapping a changed sample-axis header; export will not alter geometry", row.Trace+1)
			}
		}
		values, e := o.ReadValues(i)
		if e != nil {
			return e
		}
		if len(values) != ns {
			return fmt.Errorf("trace %d returned %d samples; expected %d", row.Trace+1, len(values), ns)
		}
		for j, v := range values {
			if math.IsInf(v, 0) || math.IsNaN(v) {
				v = math.NaN()
			}
			order.PutUint64(raw[j*8:j*8+8], math.Float64bits(v))
		}
		if _, err = w.Write(th); err != nil {
			return err
		}
		if _, err = w.Write(raw); err != nil {
			return err
		}
		if o.Progress != nil {
			o.Progress(i+1, len(o.Traces))
		}
	}
	if err = w.Flush(); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	closeErr := out.Close()
	closed = true
	if closeErr != nil {
		return closeErr
	}
	reopened, err := Open(temp)
	if err != nil {
		return fmt.Errorf("reopen numerical SEG-Y: %w", err)
	}
	info := reopened.Info
	expectedSize := int64(len(header)) + int64(len(o.Traces))*int64(240+ns*8)
	if info.TraceCount != int64(len(o.Traces)) || info.SamplesPerTrace != ns || info.SampleIntervalUS != dt ||
		info.FormatCode != 6 || info.Endian != s.Info.Endian || info.DataStart != int64(len(header)) || info.FileSize != expectedSize {
		return errors.Join(errors.New("reopened numerical SEG-Y header or byte size does not match the export plan"), reopened.Close())
	}
	if reopened.NumericalExportMetadata() != m {
		return errors.Join(errors.New("reopened numerical SEG-Y geometry metadata does not match the export plan"), reopened.Close())
	}
	if err = reopened.Close(); err != nil {
		return err
	}
	f, err := os.Open(temp)
	if err != nil {
		return err
	}
	check := sha256.New()
	buff := make([]byte, 1<<20)
	var reopenGeometryHash = sha256.New()
	var nextTraceOffset = int64(len(header))
	var offset int64
	// A buffered Reader keeps the raw verification streaming while allowing
	// geometry words split across a chunk to be read exactly by ReadAt below.
	var rawHeader [240]byte
	for {
		if err = checkCurrent(); err != nil {
			return errors.Join(err, f.Close())
		}
		n, readErr := f.Read(buff)
		if n > 0 {
			_, _ = check.Write(buff[:n])
			for m.HasGeometry() && nextTraceOffset < offset+int64(n) && nextTraceOffset < expectedSize {
				var th []byte
				if nextTraceOffset+240 <= offset+int64(n) {
					start := nextTraceOffset - offset
					th = buff[start : start+240]
				} else {
					if _, e := f.ReadAt(rawHeader[:], nextTraceOffset); e != nil {
						return errors.Join(e, f.Close())
					}
					th = rawHeader[:]
				}
				copyNumericalGeometryWords(geoBytes[:], th, m)
				_, _ = reopenGeometryHash.Write(geoBytes[:])
				nextTraceOffset += info.TraceBytes
			}
			offset += int64(n)
		}
		if readErr != nil {
			if readErr != io.EOF {
				return errors.Join(readErr, f.Close())
			}
			break
		}
	}
	if err = f.Close(); err != nil {
		return err
	}
	if !bytes.Equal(sha.Sum(nil), check.Sum(nil)) {
		return errors.New("reopened numerical SEG-Y differs from the written trace content")
	}
	if m.HasGeometry() && !bytes.Equal(geometryHash.Sum(nil), reopenGeometryHash.Sum(nil)) {
		return errors.New("reopened numerical SEG-Y geometry differs from the selected source trace headers")
	}
	if err = checkCurrent(); err != nil {
		return err
	}
	if err = verifyNumericalExportTarget(target, o.ReplaceExisting, source); err != nil {
		return err
	}
	if o.ReplaceExisting == nil {
		err = os.Link(temp, target)
	} else {
		err = os.Rename(temp, target)
	}
	if err != nil {
		return fmt.Errorf("publish numerical SEG-Y: %w", err)
	}
	return nil
}

func copyNumericalGeometryWords(dst, header []byte, m NumericalExportMetadata) {
	copy(dst[:4], header[m.InlineByte-1:m.InlineByte+3])
	copy(dst[4:], header[m.CrosslineByte-1:m.CrosslineByte+3])
}

// ExportTimeIndices includes both requested endpoints if they lie on the
// physical sampling grid. Correct only machine-scale error near an integer
// index (e.g. 799.9999999999999 ms); never round display labels into the axis.
func ExportTimeIndices(firstUS, lastUS float64, originUS int64, dt, ns int) (first, last int, ok bool) {
	if math.IsNaN(firstUS) || math.IsNaN(lastUS) || math.IsInf(firstUS, 0) || math.IsInf(lastUS, 0) ||
		firstUS > lastUS || ns <= 0 || dt <= 0 {
		return 0, -1, false
	}
	index := func(us float64) float64 {
		q := (us - float64(originUS)) / float64(dt)
		r := math.Round(q)
		epsilon := 8 * (math.Nextafter(1, 2) - 1) * max(1, math.Abs(q), math.Abs(us)/float64(dt), math.Abs(float64(originUS))/float64(dt))
		if math.Abs(q-r) <= epsilon {
			return r
		}
		return q
	}
	lo, hi := max(0, math.Ceil(index(firstUS))), min(float64(ns-1), math.Floor(index(lastUS)))
	if lo > hi {
		return 0, -1, false
	}
	return int(lo), int(hi), true
}

// NumericalSEGYSuffix is used by callers to prevent a data export from being
// accidentally presented as a figure or another numeric representation.
func NumericalSEGYSuffix(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".sgy") || strings.EqualFold(filepath.Ext(path), ".segy")
}
