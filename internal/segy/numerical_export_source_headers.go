package segy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
)

// CanPreserveNumericalExportHeaders restricts header-only reuse to a physical
// SEG-Y whose standard IL/XL words describe its geometry without new metadata.
func (s *File) CanPreserveNumericalExportHeaders(inline, crossline int) bool {
	if s == nil || inline != 189 || crossline != 193 || !NumericalSEGYSuffix(s.Info.Path) {
		return false
	}
	if _, physical := s.f.(*os.File); !physical {
		return false
	}
	m := s.NumericalExportMetadata()
	return !m.TraceOrder && (!m.HasGeometry() || m.InlineByte == inline && m.CrosslineByte == crossline)
}

func numericalByteOrder(endian Endian) binary.ByteOrder {
	if endian == Little {
		return binary.LittleEndian
	}
	return binary.BigEndian
}

func (s *File) preservedNumericalExportHeader(ns, traces int, format ExportSampleFormat) ([]byte, error) {
	info := s.Info
	const extSize = 3200
	if info.ExtendedTextHeaders < 0 || info.DataStart != 3600+int64(info.ExtendedTextHeaders)*extSize ||
		info.TraceBytes <= 240 || info.FileSize < info.DataStart ||
		(info.FileSize-info.DataStart)%info.TraceBytes != 0 ||
		(info.FileSize-info.DataStart)/info.TraceBytes != info.TraceCount {
		return nil, errors.New("cannot preserve an incomplete or unsupported SEG-Y file layout")
	}
	h := make([]byte, int(info.DataStart))
	if _, err := s.f.ReadAt(h, 0); err != nil {
		return nil, err
	}
	bh := h[3200:3600]
	order := numericalByteOrder(info.Endian)
	if int(int16(order.Uint16(bh[304:306]))) != info.ExtendedTextHeaders {
		return nil, errors.New("cannot preserve variable-count SEG-Y textual headers")
	}
	major, minor := int(bh[300]), int(bh[301])
	if info.Endian == Little && major == 0 && (minor == 1 || minor == 2) {
		major, minor = minor, 0 // legacy endian-swapped revision word
	}
	if major > 2 || (major == 2 && minor > 1) {
		return nil, fmt.Errorf("cannot preserve unsupported SEG-Y revision %d.%d", major, minor)
	}
	if major == 2 {
		additional := order.Uint32(bh[306:310])
		if minor >= 1 {
			additional = uint32(order.Uint16(bh[306:308]))
		}
		if additional != 0 || order.Uint32(bh[328:332]) != 0 {
			return nil, errors.New("cannot preserve unsupported SEG-Y trace-header extensions or data trailers")
		}
		if marker := order.Uint32(bh[96:100]); marker != 0 && marker != 0x01020304 {
			return nil, errors.New("source SEG-Y byte-order declaration conflicts with the decoded layout")
		}
		if n := order.Uint32(bh[68:72]); n != 0 && n != uint32(info.SamplesPerTrace) {
			return nil, errors.New("extended SEG-Y sample count conflicts with the physical layout")
		}
		if dt := order.Uint64(bh[72:80]); dt != 0 && math.Float64frombits(dt) != float64(info.SampleIntervalUS) {
			return nil, errors.New("extended SEG-Y sample interval conflicts with the physical layout")
		}
		if n := order.Uint64(bh[312:320]); n != 0 && n != uint64(info.TraceCount) {
			return nil, errors.New("declared SEG-Y trace count conflicts with the physical layout")
		}
		if off := order.Uint64(bh[320:328]); off != 0 && off != uint64(info.DataStart) {
			return nil, errors.New("declared SEG-Y first-trace offset conflicts with the physical layout")
		}
	}

	if int(format) != info.FormatCode {
		order.PutUint16(bh[24:26], uint16(format))
		if major < 2 && format == ExportFormatIEEE64 {
			// Old reserved words must not turn into active Rev 2 overrides.
			clear(bh[60:96])
			clear(bh[306:332])
			order.PutUint32(bh[96:100], 0x01020304)
			bh[300], bh[301] = 2, 1
			order.PutUint16(bh[302:304], 1)
			major = 2
		} else if major == 0 && format == ExportFormatIEEE32 {
			bh[300], bh[301] = 1, 0
			order.PutUint16(bh[302:304], 1)
		}
	}
	if ns != info.SamplesPerTrace {
		order.PutUint16(bh[20:22], uint16(ns))
		if major == 2 && order.Uint32(bh[68:72]) != 0 {
			order.PutUint32(bh[68:72], uint32(ns))
		}
	}
	if major == 2 && int64(traces) != info.TraceCount && order.Uint64(bh[312:320]) != 0 {
		order.PutUint64(bh[312:320], uint64(traces))
	}
	return h, nil
}
