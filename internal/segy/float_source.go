package segy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
)

// FloatSectionData is a read-only, independently owned window source. Bounds
// are inclusive, just like File.ReadTraceWindow. Implementations must return
// float64 values without narrowing an underlying float64 artifact.
type FloatSectionData interface {
	ReadTraceWindow(trace int64, first, last int) ([]float64, error)
	Close() error
}

// FloatSectionOptions identifies host-owned geometry. The external algorithm
// never supplies source paths or arbitrary SEG-Y headers. TraceIndices map its
// logical rows to the original A physical traces, in exactly that order.
type FloatSectionOptions struct {
	SourcePath, ValuePath  string
	TraceIndices           []int64
	SampleStart, SampleEnd int
}

// TraceTimeOriginUS returns the exact physical time of local sample zero.
// Ordinary SEG-Y retains its DelayMS semantics. Derived artifact sections can
// retain a fractional-millisecond cropped origin that the legacy raw SEG-Y
// header word cannot represent; callers must not infer it from rounded DelayMS.
func (s *File) TraceTimeOriginUS(trace int64) (int64, error) {
	if s == nil || s.f == nil {
		return 0, errors.New("nil SEG-Y reader")
	}
	if source, ok := s.f.(interface{ TraceTimeOriginUS(int64) (int64, error) }); ok {
		return source.TraceTimeOriginUS(trace)
	}
	h, err := s.ReadTraceHeader(trace)
	if err != nil {
		return 0, err
	}
	return int64(h.DelayMS) * 1000, nil
}

// OpenFloatSection explicitly adapts a validated derived window to existing
// read-only SEG-Y rendering APIs. It synthesizes format-6 (IEEE float64) bytes
// on demand and never creates a SEG-Y file. On success it owns data; on failure
// the caller still owns data. Source headers remain in their original endian.
func OpenFloatSection(o FloatSectionOptions, data FloatSectionData) (*File, error) {
	if data == nil || len(o.TraceIndices) == 0 || o.SampleStart < 0 || o.SampleEnd < o.SampleStart {
		return nil, errors.New("invalid float section source or window")
	}
	a, err := Open(o.SourcePath)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = a.Close()
		}
	}()
	if o.SampleEnd >= a.Info.SamplesPerTrace {
		return nil, errors.New("float section window exceeds A")
	}
	info := a.Info
	info.Path = o.ValuePath
	info.SamplesPerTrace = o.SampleEnd - o.SampleStart + 1
	info.TraceCount = int64(len(o.TraceIndices))
	info.FormatCode, info.BytesPerSample = 6, 8
	info.TraceBytes = 240 + int64(info.SamplesPerTrace)*8
	if info.TraceCount > (math.MaxInt64-info.DataStart)/info.TraceBytes {
		return nil, errors.New("float section byte size overflows")
	}
	info.FileSize = info.DataStart + info.TraceCount*info.TraceBytes
	header, err := a.exportHeader(info.SamplesPerTrace, 6)
	if err != nil {
		return nil, err
	}
	for _, trace := range o.TraceIndices {
		if trace < 0 || trace >= a.Info.TraceCount {
			return nil, errors.New("float section physical trace exceeds A")
		}
	}
	identity, err := os.Stat(o.ValuePath)
	if err != nil {
		return nil, err
	}
	sourceIdentity, err := a.SourceFileInfo()
	if err != nil {
		return nil, err
	}
	backing := &floatSectionReader{source: a, data: data, info: info, header: header,
		traces: append([]int64(nil), o.TraceIndices...), sampleStart: o.SampleStart,
		identity: identity, sourceIdentity: sourceIdentity}
	ok = true
	return &File{f: backing, Info: info, mappingDisabled: true}, nil
}

type floatSectionReader struct {
	mu             sync.RWMutex
	closed         bool
	source         *File
	data           FloatSectionData
	info           Info
	header         []byte
	traces         []int64
	sampleStart    int
	identity       os.FileInfo
	sourceIdentity os.FileInfo
}

func (s *floatSectionReader) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.source.Close(), s.data.Close())
}

type floatSectionInfo struct {
	os.FileInfo
	size int64
}

func (s *floatSectionReader) TraceTimeOriginUS(trace int64) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return 0, os.ErrClosed
	}
	if trace < 0 || trace >= int64(len(s.traces)) {
		return 0, errors.New("float section physical trace exceeds A")
	}
	origin, err := s.source.TraceTimeOriginUS(s.traces[trace])
	if err != nil {
		return 0, err
	}
	return origin + int64(s.sampleStart)*int64(s.source.Info.SampleIntervalUS), nil
}

func (s floatSectionInfo) Size() int64 { return s.size }

func (s *floatSectionReader) Stat() (os.FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	return floatSectionInfo{FileInfo: s.identity, size: s.info.FileSize}, nil
}

// SourceFileInfo is the real artifact file identity, not the virtual SEG-Y byte
// length returned by Stat. os.SameFile requires the concrete os.FileInfo from
// the OS; wrapping it makes identity checks always fail on Windows and Unix.
func (s *floatSectionReader) SourceFileInfo() (os.FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	return s.identity, nil
}

// ReadAt handles arbitrary byte boundaries because coalesced reads may span
// headers and several traces. Each iteration materializes at most one requested
// trace window, never the full amplitude volume.
func (s *floatSectionReader) ReadAt(dst []byte, offset int64) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return 0, os.ErrClosed
	}
	if offset < 0 {
		return 0, errors.New("negative float section read offset")
	}
	currentSource, err := os.Stat(s.source.Info.Path)
	if err != nil {
		return 0, err
	}
	if !os.SameFile(s.sourceIdentity, currentSource) || s.sourceIdentity.Size() != currentSource.Size() || !s.sourceIdentity.ModTime().Equal(currentSource.ModTime()) {
		return 0, errors.New("float section source changed after opening")
	}
	n := 0
	for len(dst) > 0 {
		if offset >= s.info.FileSize {
			return n, io.EOF
		}
		var chunk []byte
		var local int64
		if offset < s.info.DataStart {
			chunk, local = s.header, offset
		} else {
			rel := offset - s.info.DataStart
			trace, position := rel/s.info.TraceBytes, rel%s.info.TraceBytes
			if position < 240 {
				var err error
				chunk, err = s.source.rawTraceHeader(s.traces[trace])
				if err != nil {
					return n, err
				}
				s.source.updateExportTraceHeader(chunk, s.info.SamplesPerTrace, s.sampleStart)
				local = position
			} else {
				startByte := position - 240
				first := int(startByte / 8)
				byteCount := min(int64(len(dst)), s.info.TraceBytes-position)
				last := int((startByte + byteCount - 1) / 8)
				values, err := s.data.ReadTraceWindow(trace, first, last)
				if err != nil {
					return n, err
				}
				if len(values) != last-first+1 {
					return n, errors.New("float section provider returned an incorrect sample count")
				}
				chunk = make([]byte, len(values)*8)
				for i, v := range values {
					if math.IsNaN(v) || math.IsInf(v, 0) {
						return n, fmt.Errorf("float section trace %d sample %d is not finite", trace, first+i)
					}
					bits := math.Float64bits(v)
					if s.info.Endian == Big {
						binary.BigEndian.PutUint64(chunk[i*8:], bits)
					} else {
						binary.LittleEndian.PutUint64(chunk[i*8:], bits)
					}
				}
				local = startByte % 8
			}
		}
		count := copy(dst, chunk[local:])
		dst, offset, n = dst[count:], offset+int64(count), n+count
	}
	return n, nil
}
