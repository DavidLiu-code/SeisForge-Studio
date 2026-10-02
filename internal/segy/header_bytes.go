package segy

import "errors"

// ReadTraceHeaderBytes reads exactly one 240-byte base trace header into a
// caller-owned buffer. It shares the fixed-length layout of ReadTraceHeader,
// but deliberately skips display strings and field allocations for metadata
// indexes. It never reads amplitude samples. The caller owns the Reader and
// must not Close it until concurrent reads have completed.
func (s *File) ReadTraceHeaderBytes(trace int64, destination []byte) error {
	if s == nil || s.f == nil {
		return errors.New("nil SEG-Y reader")
	}
	if len(destination) != 240 {
		return errors.New("trace-header buffer must contain exactly 240 bytes")
	}
	if trace < 0 || trace >= s.Info.TraceCount {
		return errors.New("trace index out of range")
	}
	return s.readTraceHeader(trace, destination)
}
