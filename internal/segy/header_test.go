package segy

import (
	"encoding/binary"
	"math"
	"os"
	"strings"
	"testing"
)

func makeASCIITextHeader(firstLine string) []byte {
	raw := make([]byte, 3200)
	for index := range raw {
		raw[index] = ' '
	}
	copy(raw[:80], []byte(firstLine))
	return raw
}

func makeCP037TextHeader(firstLine string) []byte {
	reverse := make(map[rune]byte, len(cp037))
	for value, decoded := range cp037 {
		if _, exists := reverse[decoded]; !exists {
			reverse[decoded] = byte(value)
		}
	}
	raw := make([]byte, 3200)
	for index := range raw {
		raw[index] = reverse[' ']
	}
	for index, value := range []rune(firstLine) {
		if index >= 80 {
			break
		}
		raw[index] = reverse[value]
	}
	return raw
}

func putHeaderU16(raw []byte, absolute, base int, value uint16, endian Endian) {
	offset := absolute - base
	if endian == Big {
		binary.BigEndian.PutUint16(raw[offset:offset+2], value)
	} else {
		binary.LittleEndian.PutUint16(raw[offset:offset+2], value)
	}
}

func putHeaderI16(raw []byte, absolute, base int, value int16, endian Endian) {
	putHeaderU16(raw, absolute, base, uint16(value), endian)
}

func putHeaderI32(raw []byte, absolute, base int, value int32, endian Endian) {
	offset := absolute - base
	if endian == Big {
		binary.BigEndian.PutUint32(raw[offset:offset+4], uint32(value))
	} else {
		binary.LittleEndian.PutUint32(raw[offset:offset+4], uint32(value))
	}
}

func putHeaderU32(raw []byte, absolute, base int, value uint32, endian Endian) {
	offset := absolute - base
	if endian == Big {
		binary.BigEndian.PutUint32(raw[offset:offset+4], value)
	} else {
		binary.LittleEndian.PutUint32(raw[offset:offset+4], value)
	}
}

func writeHeaderFixture(t *testing.T, endian Endian, primary []byte, extended [][]byte, binaryMutator func([]byte), traceHeaders [][]byte, samples int) string {
	t.Helper()
	if len(primary) != 3200 {
		t.Fatalf("primary textual header length=%d", len(primary))
	}
	if samples < 1 {
		samples = 8
	}
	binaryHeader := make([]byte, 400)
	putHeaderU16(binaryHeader, 3217, 3201, 2000, endian)
	putHeaderU16(binaryHeader, 3219, 3201, 2000, endian)
	putHeaderU16(binaryHeader, 3221, 3201, uint16(samples), endian)
	putHeaderU16(binaryHeader, 3223, 3201, uint16(samples), endian)
	putHeaderU16(binaryHeader, 3225, 3201, 5, endian)
	putHeaderU16(binaryHeader, 3503, 3201, 1, endian)
	putHeaderI16(binaryHeader, 3505, 3201, int16(len(extended)), endian)
	if binaryMutator != nil {
		binaryMutator(binaryHeader)
	}
	if len(traceHeaders) == 0 {
		traceHeaders = [][]byte{make([]byte, 240)}
	}
	data := make([]byte, 0, 3600+len(extended)*3200+len(traceHeaders)*(240+samples*4))
	data = append(data, primary...)
	data = append(data, binaryHeader...)
	for _, text := range extended {
		if len(text) != 3200 {
			t.Fatalf("extended textual header length=%d", len(text))
		}
		data = append(data, text...)
	}
	for _, header := range traceHeaders {
		if len(header) != 240 {
			t.Fatalf("trace header length=%d", len(header))
		}
		data = append(data, header...)
		data = append(data, make([]byte, samples*4)...)
	}
	path := t.TempDir() + "/headers.sgy"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fieldByKey(t *testing.T, fields []HeaderField, key string) HeaderField {
	t.Helper()
	for _, field := range fields {
		if field.Key == key {
			return field
		}
	}
	t.Fatalf("header field %q not found", key)
	return HeaderField{}
}

func TestReadTextHeadersDetectsASCIIAndCP037AndCanRedecode(t *testing.T) {
	primary := makeCP037TextHeader("C 1 EBCDIC PRIMARY HEADER")
	extended := makeASCIITextHeader("C 1 ASCII EXTENDED HEADER")
	path := writeHeaderFixture(t, Big, primary, [][]byte{extended}, nil, nil, 8)
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	headers, err := file.ReadTextHeaders()
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 2 || headers[0].Extended || !headers[1].Extended || headers[0].Index != 0 || headers[1].Index != 1 {
		t.Fatalf("unexpected textual headers: %+v", headers)
	}
	if headers[0].Encoding != TextEncodingEBCDICCP037 || headers[1].Encoding != TextEncodingASCII {
		t.Fatalf("encoding detection failed: %q / %q", headers[0].Encoding, headers[1].Encoding)
	}
	if len(headers[0].Lines) != 40 || len([]rune(headers[0].Lines[0])) != 80 || !strings.HasPrefix(headers[0].Lines[0], "C 1 EBCDIC PRIMARY HEADER") {
		t.Fatalf("bad EBCDIC decode: %q", headers[0].Lines[0])
	}
	if !strings.HasPrefix(headers[1].Lines[0], "C 1 ASCII EXTENDED HEADER") {
		t.Fatalf("bad ASCII decode: %q", headers[1].Lines[0])
	}

	firstText, firstLines, err := headers[0].Decode(TextEncodingEBCDICCP037)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = headers[0].Decode(TextEncodingASCII); err != nil {
		t.Fatal(err)
	}
	secondText, secondLines, err := headers[0].Decode(TextEncodingUnknown)
	if err != nil {
		t.Fatal(err)
	}
	if firstText != secondText || firstLines[0] != secondLines[0] || headers[0].Text != firstText {
		t.Fatal("repeated text-header decoding changed the preserved view")
	}

	headers[0].Raw[0] = 0
	headersAgain, err := file.ReadTextHeaders()
	if err != nil {
		t.Fatal(err)
	}
	if headersAgain[0].Raw[0] != primary[0] {
		t.Fatal("textual Raw did not return an independent copy")
	}
}

func TestDecodeTextHeaderRejectsInvalidLengthAndEncoding(t *testing.T) {
	if _, _, err := DecodeTextHeader(make([]byte, 3199), TextEncodingASCII); err == nil {
		t.Fatal("short textual header accepted")
	}
	if _, _, err := DecodeTextHeader(make([]byte, 3200), TextEncoding("utf-8")); err == nil {
		t.Fatal("unsupported textual encoding accepted")
	}
}

func TestReadBinaryHeaderCommonRev1FieldsAndRawCopy(t *testing.T) {
	path := writeHeaderFixture(t, Big, makeASCIITextHeader("C 1 BINARY HEADER"), nil, func(raw []byte) {
		putHeaderI32(raw, 3201, 3201, 123456, Big)
		putHeaderI32(raw, 3205, 3201, -77, Big)
		putHeaderI32(raw, 3209, 3201, 9, Big)
		putHeaderU16(raw, 3213, 3201, 48, Big)
		putHeaderU16(raw, 3215, 3201, 2, Big)
		putHeaderU16(raw, 3217, 3201, 2000, Big)
		putHeaderU16(raw, 3219, 3201, 4000, Big)
		putHeaderU16(raw, 3221, 3201, 16, Big)
		putHeaderU16(raw, 3223, 3201, 32, Big)
		putHeaderU16(raw, 3225, 3201, 5, Big)
		putHeaderU16(raw, 3227, 3201, 24, Big)
		putHeaderI16(raw, 3229, 3201, 4, Big)
		putHeaderI16(raw, 3255, 3201, 1, Big)
		putHeaderU16(raw, 3501, 3201, 0x0100, Big)
		putHeaderU16(raw, 3503, 3201, 1, Big)
	}, nil, 16)
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	header, err := file.ReadBinaryHeader()
	if err != nil {
		t.Fatal(err)
	}
	if header.JobID != 123456 || header.LineNumber != -77 || header.ReelNumber != 9 || header.DataTracesPerEnsemble != 48 || header.AuxiliaryTracesPerEnsemble != 2 {
		t.Fatalf("identity/count fields mismatch: %+v", header)
	}
	if header.SampleIntervalUS != 2000 || header.OriginalSampleIntervalUS != 4000 || header.SamplesPerTrace != 16 || header.OriginalSamplesPerTrace != 32 {
		t.Fatalf("sampling fields mismatch: %+v", header)
	}
	if header.FormatCode != 5 || header.FormatName != "IEEE 32位浮点" || header.EnsembleFold != 24 || header.TraceSortingCode != 4 || header.MeasurementSystem != 1 {
		t.Fatalf("format/sort fields mismatch: %+v", header)
	}
	if header.RevisionMajor != 1 || header.RevisionMinor != 0 || header.FixedLengthTraceFlag != 1 || header.ExtendedTextHeaderCount != 0 || header.MaximumAdditionalTraceHeaders != 0 {
		t.Fatalf("revision fields mismatch: %+v", header)
	}
	if len(header.Warnings) != 0 {
		t.Fatalf("unexpected binary-header warnings: %v", header.Warnings)
	}
	format := fieldByKey(t, header.Fields, "sample_format")
	if format.Name != "样点格式代码" || format.ByteStart != 3225 || format.ByteEnd != 3226 || format.Raw != 5 || !strings.Contains(format.Value, "IEEE 32位浮点") {
		t.Fatalf("bad format field: %+v", format)
	}
	interval := fieldByKey(t, header.Fields, "sample_interval")
	if interval.Raw != 2000 || interval.Unit != "us" {
		t.Fatalf("bad interval field: %+v", interval)
	}
	header.Raw[16] = 0
	header.Fields[0].Value = "changed"
	again, err := file.ReadBinaryHeader()
	if err != nil {
		t.Fatal(err)
	}
	if again.SampleIntervalUS != 2000 || again.Raw[16] == 0 || again.Fields[0].Value == "changed" {
		t.Fatal("binary Raw/Fields did not return independent copies")
	}
}

func TestReadBinaryHeaderLittleEndianAndWarnings(t *testing.T) {
	path := writeHeaderFixture(t, Little, makeASCIITextHeader("C 1 LITTLE ENDIAN"), nil, func(raw []byte) {
		putHeaderU16(raw, 3501, 3201, 0x0100, Little)
		putHeaderU16(raw, 3503, 3201, 0, Little)
		putHeaderU32(raw, 3507, 3201, 2, Little)
	}, nil, 16)
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	header, err := file.ReadBinaryHeader()
	if err != nil {
		t.Fatal(err)
	}
	if header.Endian != Little || header.SampleIntervalUS != 2000 || header.SamplesPerTrace != 16 || header.RevisionMajor != 1 {
		t.Fatalf("little-endian binary header mismatch: %+v", header)
	}
	warnings := strings.Join(header.Warnings, " | ")
	if !strings.Contains(warnings, "固定长度") || !strings.Contains(warnings, "附加道头") {
		t.Fatalf("layout warnings missing: %v", header.Warnings)
	}
}

func TestReadTraceHeaderAppliesScalarsAndReportsMismatch(t *testing.T) {
	const samples = 16
	trace := make([]byte, 240)
	putHeaderI32(trace, 1, 1, 101, Big)
	putHeaderI32(trace, 5, 1, 202, Big)
	putHeaderI32(trace, 9, 1, 303, Big)
	putHeaderI32(trace, 13, 1, 4, Big)
	putHeaderI32(trace, 17, 1, 505, Big)
	putHeaderI32(trace, 21, 1, 606, Big)
	putHeaderI32(trace, 25, 1, 7, Big)
	putHeaderI16(trace, 29, 1, 1, Big)
	putHeaderI32(trace, 37, 1, -250, Big)
	putHeaderI32(trace, 41, 1, 1250, Big)
	putHeaderI32(trace, 45, 1, -500, Big)
	putHeaderI32(trace, 49, 1, 200, Big)
	putHeaderI16(trace, 69, 1, -10, Big)
	putHeaderI16(trace, 71, 1, 10, Big)
	putHeaderI32(trace, 73, 1, 100, Big)
	putHeaderI32(trace, 77, 1, 200, Big)
	putHeaderI32(trace, 81, 1, 300, Big)
	putHeaderI32(trace, 85, 1, 400, Big)
	putHeaderI16(trace, 89, 1, 1, Big)
	putHeaderI16(trace, 109, 1, 125, Big)
	putHeaderU16(trace, 115, 1, 15, Big)
	putHeaderU16(trace, 117, 1, 4000, Big)
	putHeaderI32(trace, 181, 1, 500, Big)
	putHeaderI32(trace, 185, 1, 600, Big)
	putHeaderI32(trace, 189, 1, 1220, Big)
	putHeaderI32(trace, 193, 1, 71, Big)
	putHeaderI32(trace, 197, 1, 1234, Big)
	putHeaderI16(trace, 201, 1, -10, Big)
	path := writeHeaderFixture(t, Big, makeASCIITextHeader("C 1 TRACE HEADER"), nil, nil, [][]byte{trace}, samples)
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	header, err := file.ReadTraceHeader(0)
	if err != nil {
		t.Fatal(err)
	}
	if header.TraceSequenceLine != 101 || header.TraceSequenceFile != 202 || header.FieldRecord != 303 || header.TraceInField != 4 || header.EnergySourcePoint != 505 || header.CDP != 606 || header.TraceInCDP != 7 || header.TraceIDCode != 1 || header.Offset != -250 {
		t.Fatalf("trace identity mismatch: %+v", header)
	}
	if header.ElevationScalar != -10 || header.ReceiverElevation != 125 || header.SourceElevation != -50 || header.SourceDepth != 20 {
		t.Fatalf("elevation scaling mismatch: %+v", header)
	}
	if header.CoordinateScalar != 10 || header.SourceX != 1000 || header.SourceY != 2000 || header.GroupX != 3000 || header.GroupY != 4000 || header.CDPX != 5000 || header.CDPY != 6000 {
		t.Fatalf("coordinate scaling mismatch: %+v", header)
	}
	if header.Inline != 1220 || header.Crossline != 71 || header.DelayMS != 125 || header.SampleCount != 15 || header.SampleIntervalUS != 4000 || math.Abs(header.Shotpoint-123.4) > 1e-9 {
		t.Fatalf("trace sampling/geometry mismatch: %+v", header)
	}
	warnings := strings.Join(header.Warnings, " | ")
	if !strings.Contains(warnings, "样点数") || !strings.Contains(warnings, "采样间隔") {
		t.Fatalf("trace mismatch warnings missing: %v", header.Warnings)
	}
	field := fieldByKey(t, header.Fields, "source_x")
	if field.Name != "震源点 X" || field.ByteStart != 73 || field.ByteEnd != 76 || field.Raw != 100 || field.Value != "1000" || field.Unit != "长度" {
		t.Fatalf("scaled source-X field mismatch: %+v", field)
	}
	shotpoint := fieldByKey(t, header.Fields, "shotpoint")
	if shotpoint.Raw != 1234 || shotpoint.Value != "123.4" {
		t.Fatalf("scaled shotpoint field mismatch: %+v", shotpoint)
	}
	header.Raw[3] = 0
	header.Fields[0].Value = "changed"
	again, err := file.ReadTraceHeader(0)
	if err != nil {
		t.Fatal(err)
	}
	if again.Raw[3] != 101 || again.Fields[0].Value == "changed" {
		t.Fatal("trace Raw/Fields did not return independent copies")
	}
	if _, err = file.ReadTraceHeader(1); err == nil {
		t.Fatal("out-of-range trace header accepted")
	}
}

func TestReadTraceHeaderPositiveNegativeAndZeroScalars(t *testing.T) {
	tests := []struct {
		scalar int16
		raw    int32
		want   float64
	}{
		{scalar: 10, raw: 123, want: 1230},
		{scalar: -10, raw: 123, want: 12.3},
		{scalar: 0, raw: 123, want: 123},
	}
	for _, test := range tests {
		trace := make([]byte, 240)
		putHeaderI16(trace, 69, 1, test.scalar, Big)
		putHeaderI16(trace, 71, 1, test.scalar, Big)
		putHeaderI32(trace, 41, 1, test.raw, Big)
		putHeaderI32(trace, 73, 1, test.raw, Big)
		path := writeHeaderFixture(t, Big, makeASCIITextHeader("C 1 SCALAR"), nil, nil, [][]byte{trace}, 4)
		file, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		header, err := file.ReadTraceHeader(0)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(header.ReceiverElevation-test.want) > 1e-9 || math.Abs(header.SourceX-test.want) > 1e-9 {
			t.Fatalf("scalar %d: elevation=%g sourceX=%g want=%g", test.scalar, header.ReceiverElevation, header.SourceX, test.want)
		}
	}
}

func TestHeaderAPIRemainsAvailableForUnsupportedFormat4(t *testing.T) {
	trace := make([]byte, 240)
	putHeaderI32(trace, 21, 1, 8801, Big)
	putHeaderI32(trace, 189, 1, 1210, Big)
	putHeaderI32(trace, 193, 1, 44, Big)
	path := writeHeaderFixture(t, Big, makeASCIITextHeader("C 1 FORMAT 4 HEADER ONLY"), nil, func(raw []byte) {
		putHeaderU16(raw, 3225, 3201, 4, Big)
	}, [][]byte{trace}, 8)
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	binaryHeader, err := file.ReadBinaryHeader()
	if err != nil {
		t.Fatal(err)
	}
	if binaryHeader.FormatCode != 4 || binaryHeader.FormatName != "带增益定点数" {
		t.Fatalf("format-4 header mismatch: %+v", binaryHeader)
	}
	if !strings.Contains(strings.Join(binaryHeader.Warnings, " | "), "不能由当前振幅解码器读取") {
		t.Fatalf("format-4 decoder warning missing: %v", binaryHeader.Warnings)
	}
	textHeaders, err := file.ReadTextHeaders()
	if err != nil || len(textHeaders) != 1 || !strings.HasPrefix(textHeaders[0].Lines[0], "C 1 FORMAT 4 HEADER ONLY") {
		t.Fatalf("format-4 textual header unavailable: headers=%d err=%v", len(textHeaders), err)
	}
	traceHeader, err := file.ReadTraceHeader(0)
	if err != nil {
		t.Fatal(err)
	}
	if traceHeader.CDP != 8801 || traceHeader.Inline != 1210 || traceHeader.Crossline != 44 {
		t.Fatalf("format-4 trace header unavailable: %+v", traceHeader)
	}
}

func TestLittleEndianExtendedTextHeaderAndInvalidCount(t *testing.T) {
	primary := makeASCIITextHeader("C 1 LITTLE PRIMARY")
	extended := makeCP037TextHeader("C 1 LITTLE EBCDIC EXTENDED")
	path := writeHeaderFixture(t, Little, primary, [][]byte{extended}, func(raw []byte) {
		putHeaderU16(raw, 3501, 3201, 0x0100, Little)
	}, nil, 8)
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	binaryHeader, err := file.ReadBinaryHeader()
	if err != nil {
		t.Fatal(err)
	}
	if binaryHeader.Endian != Little || binaryHeader.ExtendedTextHeaderCount != 1 {
		t.Fatalf("little-endian extended count mismatch: %+v", binaryHeader)
	}
	textHeaders, err := file.ReadTextHeaders()
	if err != nil {
		t.Fatal(err)
	}
	if len(textHeaders) != 2 || textHeaders[1].Encoding != TextEncodingEBCDICCP037 || !strings.HasPrefix(textHeaders[1].Lines[0], "C 1 LITTLE EBCDIC EXTENDED") {
		t.Fatalf("little-endian extended text mismatch: %+v", textHeaders)
	}

	// A corrupted or externally altered count must fail before allocation or
	// an out-of-range file read. The underlying file remains untouched.
	file.Info.ExtendedTextHeaders = 2
	if _, err = file.ReadTextHeaders(); err == nil || !strings.Contains(err.Error(), "exceeds header area") {
		t.Fatalf("invalid extended textual count was not rejected: %v", err)
	}
}

func TestNegativeExtendedTextCountIsReportedWithoutChangingLayout(t *testing.T) {
	path := writeHeaderFixture(t, Big, makeASCIITextHeader("C 1 NEGATIVE EXT COUNT"), nil, func(raw []byte) {
		putHeaderU16(raw, 3501, 3201, 0x0100, Big)
		putHeaderI16(raw, 3505, 3201, -1, Big)
	}, nil, 8)
	file, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if file.Info.ExtendedTextHeaders != 0 || file.Info.DataStart != 3600 {
		t.Fatalf("legacy fixed-length layout changed: %+v", file.Info)
	}
	binaryHeader, err := file.ReadBinaryHeader()
	if err != nil {
		t.Fatal(err)
	}
	if binaryHeader.ExtendedTextHeaderCount != -1 || !strings.Contains(strings.Join(binaryHeader.Warnings, " | "), "EndText") {
		t.Fatalf("negative extended-count warning missing: %+v", binaryHeader)
	}
	textHeaders, err := file.ReadTextHeaders()
	if err != nil || len(textHeaders) != 1 {
		t.Fatalf("primary header should remain readable: count=%d err=%v", len(textHeaders), err)
	}
}
