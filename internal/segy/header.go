package segy

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// TextEncoding identifies the character encoding used by a 3200-byte SEG-Y
// textual header. Unknown requests automatic detection when decoding.
type TextEncoding string

const (
	TextEncodingUnknown     TextEncoding = "unknown"
	TextEncodingASCII       TextEncoding = "ascii"
	TextEncodingEBCDICCP037 TextEncoding = "ebcdic-cp037"
)

// TextHeader is one immutable view of a SEG-Y textual header. Index is zero
// for the primary textual header and one-based for extended headers. Raw is a
// caller-owned copy. Lines always contains forty 80-character records.
type TextHeader struct {
	Index    int
	Extended bool
	Encoding TextEncoding
	Raw      []byte
	Text     string
	Lines    []string
}

// HeaderField is a decoded standard SEG-Y header word. Byte positions are
// 1-based. Binary-header positions use the SEG-Y file positions 3201..3600;
// trace-header positions use 1..240. Raw is the unscaled integer word and
// Value is its user-facing (and, where applicable, scalar-adjusted) value.
type HeaderField struct {
	Key       string
	Name      string
	ByteStart int
	ByteEnd   int
	Raw       int64
	Value     string
	Unit      string
}

// BinaryHeader contains the common SEG-Y Rev 0/1 binary reel-header fields.
// Raw and Fields are newly allocated for every call.
type BinaryHeader struct {
	Raw    []byte
	Fields []HeaderField
	Endian Endian

	JobID                         int32
	LineNumber                    int32
	ReelNumber                    int32
	DataTracesPerEnsemble         int
	AuxiliaryTracesPerEnsemble    int
	SampleIntervalUS              int
	OriginalSampleIntervalUS      int
	SamplesPerTrace               int
	OriginalSamplesPerTrace       int
	FormatCode                    int
	FormatName                    string
	EnsembleFold                  int
	TraceSortingCode              int
	MeasurementSystem             int
	RevisionMajor                 int
	RevisionMinor                 int
	FixedLengthTraceFlag          int
	ExtendedTextHeaderCount       int
	MaximumAdditionalTraceHeaders uint32
	Warnings                      []string
}

// TraceHeader contains common SEG-Y Rev 0/1 fields for one trace. Trace is the
// zero-based file trace index. Coordinate and elevation values have their
// respective SEG-Y scalar applied; the original integer words remain in
// Fields and Raw.
type TraceHeader struct {
	Trace  int64
	Raw    []byte
	Fields []HeaderField

	TraceSequenceLine int32
	TraceSequenceFile int32
	FieldRecord       int32
	TraceInField      int32
	EnergySourcePoint int32
	CDP               int32
	TraceInCDP        int32
	TraceIDCode       int
	Offset            int32

	ElevationScalar   int16
	CoordinateScalar  int16
	CoordinateUnits   int16
	ReceiverElevation float64
	SourceElevation   float64
	SourceDepth       float64
	ReceiverDatum     float64
	SourceDatum       float64
	SourceWaterDepth  float64
	GroupWaterDepth   float64
	SourceX, SourceY  float64
	GroupX, GroupY    float64
	CDPX, CDPY        float64

	DelayMS          int
	SampleCount      int
	SampleIntervalUS int
	Inline           int32
	Crossline        int32
	Shotpoint        float64
	ShotpointScalar  int16
	Warnings         []string
}

// DetectTextEncoding distinguishes ordinary ASCII from EBCDIC code page 037.
// Ties intentionally resolve to ASCII, which is the least surprising result
// for blank and zero-filled legacy headers.
func DetectTextEncoding(raw []byte) TextEncoding {
	if textEncodingScore(raw, TextEncodingEBCDICCP037) > textEncodingScore(raw, TextEncodingASCII) {
		return TextEncodingEBCDICCP037
	}
	return TextEncodingASCII
}

// DecodeTextHeader decodes exactly one 3200-byte textual header. Passing
// TextEncodingUnknown performs automatic detection. The returned text joins
// the forty fixed-width lines with CRLF without trimming their contents.
func DecodeTextHeader(raw []byte, encoding TextEncoding) (string, []string, error) {
	if len(raw) != 3200 {
		return "", nil, fmt.Errorf("SEG-Y textual header must contain 3200 bytes, got %d", len(raw))
	}
	if encoding == TextEncodingUnknown || encoding == "" {
		encoding = DetectTextEncoding(raw)
	}
	if encoding != TextEncodingASCII && encoding != TextEncodingEBCDICCP037 {
		return "", nil, fmt.Errorf("unsupported SEG-Y textual encoding %q", encoding)
	}
	runes := decodeTextRunes(raw, encoding)
	lines := make([]string, 40)
	for line := range lines {
		lines[line] = string(runes[line*80 : (line+1)*80])
	}
	return strings.Join(lines, "\r\n"), lines, nil
}

// Decode re-decodes the preserved raw header using the requested encoding.
// It does not mutate the TextHeader, so UI code can switch encodings repeatedly.
func (h TextHeader) Decode(encoding TextEncoding) (string, []string, error) {
	return DecodeTextHeader(h.Raw, encoding)
}

// ReadTextHeaders returns the primary textual header followed by all extended
// textual headers supported by the current fixed-length SEG-Y layout.
func (s *File) ReadTextHeaders() ([]TextHeader, error) {
	if s == nil || s.f == nil {
		return nil, errors.New("nil SEG-Y reader")
	}
	count := s.Info.ExtendedTextHeaders
	if count < 0 {
		return nil, errors.New("invalid extended textual header count")
	}
	available := int64(0)
	if s.Info.DataStart >= 3600 {
		available = (s.Info.DataStart - 3600) / 3200
	}
	if int64(count) > available {
		return nil, fmt.Errorf("extended textual header count %d exceeds header area", count)
	}
	out := make([]TextHeader, 0, count+1)
	for index := 0; index <= count; index++ {
		offset := int64(0)
		if index > 0 {
			offset = 3600 + int64(index-1)*3200
		}
		raw := make([]byte, 3200)
		if _, err := s.f.ReadAt(raw, offset); err != nil {
			return nil, fmt.Errorf("read textual header %d: %w", index, err)
		}
		encoding := DetectTextEncoding(raw)
		text, lines, err := DecodeTextHeader(raw, encoding)
		if err != nil {
			return nil, err
		}
		out = append(out, TextHeader{Index: index, Extended: index > 0, Encoding: encoding, Raw: raw, Text: text, Lines: lines})
	}
	return out, nil
}

// SampleFormatName returns a stable Chinese display name for a SEG-Y sample
// format code, including codes recognized by current SEG-Y revisions even when
// the Limage amplitude decoder does not implement them.
func SampleFormatName(code int) string {
	switch code {
	case 1:
		return "IBM 32位浮点"
	case 2:
		return "32位有符号整数"
	case 3:
		return "16位有符号整数"
	case 4:
		return "带增益定点数"
	case 5:
		return "IEEE 32位浮点"
	case 6:
		return "IEEE 64位浮点"
	case 7:
		return "24位有符号整数"
	case 8:
		return "8位有符号整数"
	case 9:
		return "64位有符号整数"
	case 10:
		return "32位无符号整数"
	case 11:
		return "16位无符号整数"
	case 12:
		return "64位无符号整数"
	case 15:
		return "24位无符号整数"
	case 16:
		return "8位无符号整数"
	default:
		return "未知格式"
	}
}

// ReadBinaryHeader reads and decodes the 400-byte binary reel header without
// changing any file state.
func (s *File) ReadBinaryHeader() (BinaryHeader, error) {
	if s == nil || s.f == nil {
		return BinaryHeader{}, errors.New("nil SEG-Y reader")
	}
	raw := make([]byte, 400)
	if _, err := s.f.ReadAt(raw, 3200); err != nil {
		return BinaryHeader{}, fmt.Errorf("read SEG-Y binary header: %w", err)
	}
	e := s.Info.Endian
	revision := headerU16(raw, 3501, 3201, e)
	out := BinaryHeader{
		Raw:                           raw,
		Endian:                        e,
		JobID:                         headerI32(raw, 3201, 3201, e),
		LineNumber:                    headerI32(raw, 3205, 3201, e),
		ReelNumber:                    headerI32(raw, 3209, 3201, e),
		DataTracesPerEnsemble:         headerU16(raw, 3213, 3201, e),
		AuxiliaryTracesPerEnsemble:    headerU16(raw, 3215, 3201, e),
		SampleIntervalUS:              headerU16(raw, 3217, 3201, e),
		OriginalSampleIntervalUS:      headerU16(raw, 3219, 3201, e),
		SamplesPerTrace:               headerU16(raw, 3221, 3201, e),
		OriginalSamplesPerTrace:       headerU16(raw, 3223, 3201, e),
		FormatCode:                    headerU16(raw, 3225, 3201, e),
		EnsembleFold:                  headerU16(raw, 3227, 3201, e),
		TraceSortingCode:              headerI16(raw, 3229, 3201, e),
		MeasurementSystem:             headerI16(raw, 3255, 3201, e),
		RevisionMajor:                 revision >> 8,
		RevisionMinor:                 revision & 0xff,
		FixedLengthTraceFlag:          headerU16(raw, 3503, 3201, e),
		ExtendedTextHeaderCount:       headerI16(raw, 3505, 3201, e),
		MaximumAdditionalTraceHeaders: headerU32(raw, 3507, 3201, e),
	}
	out.FormatName = SampleFormatName(out.FormatCode)
	out.Fields = decodeBinaryFields(raw, e)
	if out.FixedLengthTraceFlag == 0 && (out.RevisionMajor > 0 || out.RevisionMinor > 0) {
		out.Warnings = append(out.Warnings, "卷头未声明固定长度道；当前查看器按固定长度布局读取")
	}
	if out.ExtendedTextHeaderCount < 0 {
		out.Warnings = append(out.Warnings, "扩展文本卷头数量为 -1；当前固定长度布局不扫描 EndText 标记")
	}
	if out.MaximumAdditionalTraceHeaders > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("卷头声明最多 %d 个附加道头块；当前查看器按 240 字节基础道头读取", out.MaximumAdditionalTraceHeaders))
	}
	if !sampleFormatDecodable(out.FormatCode) {
		out.Warnings = append(out.Warnings, fmt.Sprintf("样点格式 %d（%s）不能由当前振幅解码器读取", out.FormatCode, out.FormatName))
	}
	if out.SampleIntervalUS != s.Info.SampleIntervalUS || out.SamplesPerTrace != s.Info.SamplesPerTrace || out.FormatCode != s.Info.FormatCode {
		out.Warnings = append(out.Warnings, "当前文件布局与二进制卷头解析结果不一致")
	}
	if s.Info.TraceBytes > 0 && s.Info.FileSize >= s.Info.DataStart {
		if remainder := (s.Info.FileSize - s.Info.DataStart) % s.Info.TraceBytes; remainder != 0 {
			out.Warnings = append(out.Warnings, fmt.Sprintf("文件尾有 %d 个字节不属于完整道", remainder))
		}
	}
	return out, nil
}

// ReadTraceHeader reads and decodes one 240-byte trace header. The method uses
// the same fixed-length trace offsets as all existing Limage render paths.
func (s *File) ReadTraceHeader(trace int64) (TraceHeader, error) {
	if s == nil || s.f == nil {
		return TraceHeader{}, errors.New("nil SEG-Y reader")
	}
	raw, err := s.rawTraceHeader(trace)
	if err != nil {
		return TraceHeader{}, err
	}
	e := s.Info.Endian
	elevationScalar := int16(headerI16(raw, 69, 1, e))
	coordinateScalar := int16(headerI16(raw, 71, 1, e))
	coordinateUnits := int16(headerI16(raw, 89, 1, e))
	shotpointScalar := int16(headerI16(raw, 201, 1, e))
	elevationScale := coordinateScale(elevationScalar)
	coordinateFactor := coordinateScale(coordinateScalar)
	shotpointScale := coordinateScale(shotpointScalar)
	out := TraceHeader{
		Trace:             trace,
		Raw:               raw,
		TraceSequenceLine: headerI32(raw, 1, 1, e),
		TraceSequenceFile: headerI32(raw, 5, 1, e),
		FieldRecord:       headerI32(raw, 9, 1, e),
		TraceInField:      headerI32(raw, 13, 1, e),
		EnergySourcePoint: headerI32(raw, 17, 1, e),
		CDP:               headerI32(raw, 21, 1, e),
		TraceInCDP:        headerI32(raw, 25, 1, e),
		TraceIDCode:       headerI16(raw, 29, 1, e),
		Offset:            headerI32(raw, 37, 1, e),
		ElevationScalar:   elevationScalar,
		CoordinateScalar:  coordinateScalar,
		CoordinateUnits:   coordinateUnits,
		ReceiverElevation: float64(headerI32(raw, 41, 1, e)) * elevationScale,
		SourceElevation:   float64(headerI32(raw, 45, 1, e)) * elevationScale,
		SourceDepth:       float64(headerI32(raw, 49, 1, e)) * elevationScale,
		ReceiverDatum:     float64(headerI32(raw, 53, 1, e)) * elevationScale,
		SourceDatum:       float64(headerI32(raw, 57, 1, e)) * elevationScale,
		SourceWaterDepth:  float64(headerI32(raw, 61, 1, e)) * elevationScale,
		GroupWaterDepth:   float64(headerI32(raw, 65, 1, e)) * elevationScale,
		SourceX:           float64(headerI32(raw, 73, 1, e)) * coordinateFactor,
		SourceY:           float64(headerI32(raw, 77, 1, e)) * coordinateFactor,
		GroupX:            float64(headerI32(raw, 81, 1, e)) * coordinateFactor,
		GroupY:            float64(headerI32(raw, 85, 1, e)) * coordinateFactor,
		DelayMS:           headerI16(raw, 109, 1, e),
		SampleCount:       headerU16(raw, 115, 1, e),
		SampleIntervalUS:  headerU16(raw, 117, 1, e),
		CDPX:              float64(headerI32(raw, 181, 1, e)) * coordinateFactor,
		CDPY:              float64(headerI32(raw, 185, 1, e)) * coordinateFactor,
		Inline:            headerI32(raw, 189, 1, e),
		Crossline:         headerI32(raw, 193, 1, e),
		Shotpoint:         float64(headerI32(raw, 197, 1, e)) * shotpointScale,
		ShotpointScalar:   shotpointScalar,
	}
	out.Fields = decodeTraceFields(raw, e, elevationScale, coordinateFactor, shotpointScale, coordinateUnits)
	if out.SampleCount != 0 && out.SampleCount != s.Info.SamplesPerTrace {
		out.Warnings = append(out.Warnings, fmt.Sprintf("本道样点数 %d 与二进制卷头 %d 不一致；当前查看器使用卷头值", out.SampleCount, s.Info.SamplesPerTrace))
	}
	if out.SampleIntervalUS != 0 && out.SampleIntervalUS != s.Info.SampleIntervalUS {
		out.Warnings = append(out.Warnings, fmt.Sprintf("本道采样间隔 %d us 与二进制卷头 %d us 不一致；当前查看器使用卷头值", out.SampleIntervalUS, s.Info.SampleIntervalUS))
	}
	if coordinateUnits < 0 || coordinateUnits > 4 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("未知坐标单位代码 %d", coordinateUnits))
	}
	return out, nil
}

type headerWordSpec struct {
	key, name string
	start     int
	width     int
	signed    bool
	unit      string
}

var binaryHeaderWords = []headerWordSpec{
	{"job_id", "作业标识", 3201, 4, true, ""},
	{"line_number", "测线号", 3205, 4, true, ""},
	{"reel_number", "卷号", 3209, 4, true, ""},
	{"data_traces_per_ensemble", "每炮/集合数据道数", 3213, 2, false, "道"},
	{"aux_traces_per_ensemble", "每炮/集合辅助道数", 3215, 2, false, "道"},
	{"sample_interval", "采样间隔", 3217, 2, false, "us"},
	{"original_sample_interval", "原始采样间隔", 3219, 2, false, "us"},
	{"samples_per_trace", "每道样点数", 3221, 2, false, "点"},
	{"original_samples_per_trace", "原始每道样点数", 3223, 2, false, "点"},
	{"sample_format", "样点格式代码", 3225, 2, false, ""},
	{"ensemble_fold", "集合覆盖次数", 3227, 2, false, ""},
	{"trace_sorting", "道排序代码", 3229, 2, true, ""},
	{"vertical_sum", "垂直叠加数", 3231, 2, false, ""},
	{"sweep_frequency_start", "扫描起始频率", 3233, 2, false, "Hz"},
	{"sweep_frequency_end", "扫描终止频率", 3235, 2, false, "Hz"},
	{"sweep_length", "扫描长度", 3237, 2, false, "ms"},
	{"sweep_type", "扫描类型", 3239, 2, true, ""},
	{"sweep_trace_number", "扫描道号", 3241, 2, false, ""},
	{"sweep_taper_start", "扫描起始渐变长度", 3243, 2, false, "ms"},
	{"sweep_taper_end", "扫描终止渐变长度", 3245, 2, false, "ms"},
	{"taper_type", "渐变类型", 3247, 2, true, ""},
	{"correlated", "数据相关标志", 3249, 2, true, ""},
	{"binary_gain_recovered", "二进制增益恢复标志", 3251, 2, true, ""},
	{"amplitude_recovery", "振幅恢复方法", 3253, 2, true, ""},
	{"measurement_system", "计量系统", 3255, 2, true, ""},
	{"impulse_polarity", "脉冲极性", 3257, 2, true, ""},
	{"vibratory_polarity", "可控震源极性代码", 3259, 2, true, ""},
	{"segy_revision", "SEG-Y 修订号", 3501, 2, false, ""},
	{"fixed_length_trace", "固定长度道标志", 3503, 2, false, ""},
	{"extended_text_headers", "扩展文本卷头数", 3505, 2, true, "个"},
	{"max_additional_trace_headers", "最大附加道头块数", 3507, 4, false, "个"},
}

var traceHeaderWords = []headerWordSpec{
	{"trace_sequence_line", "测线内道序号", 1, 4, true, ""},
	{"trace_sequence_file", "文件内道序号", 5, 4, true, ""},
	{"field_record", "原始野外记录号", 9, 4, true, ""},
	{"trace_in_field", "野外记录内道号", 13, 4, true, ""},
	{"energy_source_point", "震源点号", 17, 4, true, ""},
	{"cdp", "CDP/集合号", 21, 4, true, ""},
	{"trace_in_cdp", "CDP/集合内道号", 25, 4, true, ""},
	{"trace_id", "道识别代码", 29, 2, true, ""},
	{"vertically_summed_traces", "垂直叠加道数", 31, 2, false, ""},
	{"horizontally_stacked_traces", "水平叠加道数", 33, 2, false, ""},
	{"data_use", "数据用途代码", 35, 2, true, ""},
	{"offset", "炮检距", 37, 4, true, "坐标单位"},
	{"receiver_elevation", "检波点高程", 41, 4, true, "坐标单位"},
	{"source_elevation", "震源点高程", 45, 4, true, "坐标单位"},
	{"source_depth", "震源深度", 49, 4, true, "坐标单位"},
	{"receiver_datum", "检波点基准面高程", 53, 4, true, "坐标单位"},
	{"source_datum", "震源点基准面高程", 57, 4, true, "坐标单位"},
	{"source_water_depth", "震源点水深", 61, 4, true, "坐标单位"},
	{"group_water_depth", "检波点水深", 65, 4, true, "坐标单位"},
	{"elevation_scalar", "高程/深度比例因子", 69, 2, true, ""},
	{"coordinate_scalar", "坐标比例因子", 71, 2, true, ""},
	{"source_x", "震源点 X", 73, 4, true, "坐标单位"},
	{"source_y", "震源点 Y", 77, 4, true, "坐标单位"},
	{"group_x", "检波点 X", 81, 4, true, "坐标单位"},
	{"group_y", "检波点 Y", 85, 4, true, "坐标单位"},
	{"coordinate_units", "坐标单位代码", 89, 2, true, ""},
	{"weathering_velocity", "风化层速度", 91, 2, false, ""},
	{"subweathering_velocity", "风化层下速度", 93, 2, false, ""},
	{"uphole_time_source", "震源点井口时间", 95, 2, false, "ms"},
	{"uphole_time_group", "检波点井口时间", 97, 2, false, "ms"},
	{"source_static", "震源点静校正", 99, 2, true, "ms"},
	{"group_static", "检波点静校正", 101, 2, true, "ms"},
	{"total_static", "总静校正", 103, 2, true, "ms"},
	{"lag_time_a", "延迟时间 A", 105, 2, true, "ms"},
	{"lag_time_b", "延迟时间 B", 107, 2, true, "ms"},
	{"delay_recording_time", "记录延迟时间", 109, 2, true, "ms"},
	{"mute_time_start", "切除起始时间", 111, 2, true, "ms"},
	{"mute_time_end", "切除终止时间", 113, 2, true, "ms"},
	{"samples_in_trace", "本道样点数", 115, 2, false, "点"},
	{"sample_interval", "本道采样间隔", 117, 2, false, "us"},
	{"gain_type", "增益类型", 119, 2, true, ""},
	{"instrument_gain_constant", "仪器增益常数", 121, 2, true, ""},
	{"instrument_initial_gain", "仪器初始增益", 123, 2, true, ""},
	{"correlated", "相关标志", 125, 2, true, ""},
	{"sweep_frequency_start", "扫描起始频率", 127, 2, false, "Hz"},
	{"sweep_frequency_end", "扫描终止频率", 129, 2, false, "Hz"},
	{"sweep_length", "扫描长度", 131, 2, false, "ms"},
	{"sweep_type", "扫描类型", 133, 2, true, ""},
	{"sweep_taper_start", "扫描起始渐变", 135, 2, false, "ms"},
	{"sweep_taper_end", "扫描终止渐变", 137, 2, false, "ms"},
	{"taper_type", "渐变类型", 139, 2, true, ""},
	{"alias_filter_frequency", "陷波前低通频率", 141, 2, false, "Hz"},
	{"alias_filter_slope", "陷波前低通斜率", 143, 2, false, "dB/oct"},
	{"notch_filter_frequency", "陷波频率", 145, 2, false, "Hz"},
	{"notch_filter_slope", "陷波斜率", 147, 2, false, "dB/oct"},
	{"low_cut_frequency", "低截频率", 149, 2, false, "Hz"},
	{"high_cut_frequency", "高截频率", 151, 2, false, "Hz"},
	{"low_cut_slope", "低截斜率", 153, 2, false, "dB/oct"},
	{"high_cut_slope", "高截斜率", 155, 2, false, "dB/oct"},
	{"year", "年", 157, 2, false, ""},
	{"day_of_year", "年内日", 159, 2, false, ""},
	{"hour", "小时", 161, 2, false, ""},
	{"minute", "分钟", 163, 2, false, ""},
	{"second", "秒", 165, 2, false, ""},
	{"time_basis", "时间基准代码", 167, 2, true, ""},
	{"trace_weighting", "道加权因子", 169, 2, true, ""},
	{"geophone_group_roll_1", "滚动开关位置一检波点组号", 171, 2, true, ""},
	{"geophone_group_trace_1", "道一检波点组号", 173, 2, true, ""},
	{"geophone_group_last", "末道检波点组号", 175, 2, true, ""},
	{"gap_size", "间隙大小", 177, 2, true, ""},
	{"over_travel", "超程标志", 179, 2, true, ""},
	{"cdp_x", "CDP/集合 X", 181, 4, true, "坐标单位"},
	{"cdp_y", "CDP/集合 Y", 185, 4, true, "坐标单位"},
	{"inline", "Inline", 189, 4, true, ""},
	{"crossline", "Crossline", 193, 4, true, ""},
	{"shotpoint", "炮点号", 197, 4, true, ""},
	{"shotpoint_scalar", "炮点号比例因子", 201, 2, true, ""},
	{"trace_value_measurement_unit", "道值计量单位代码", 203, 2, true, ""},
	{"transduction_constant_mantissa", "换能常数尾数", 205, 4, true, ""},
	{"transduction_constant_exponent", "换能常数指数", 209, 2, true, ""},
	{"transduction_units", "换能单位代码", 211, 2, true, ""},
	{"device_identifier", "设备/道标识", 213, 2, true, ""},
	{"time_scalar", "时间比例因子", 215, 2, true, ""},
	{"source_type_orientation", "震源类型/方向", 217, 2, true, ""},
	{"source_energy_direction_mantissa", "震源能量方向尾数", 219, 4, true, ""},
	{"source_energy_direction_exponent", "震源能量方向指数", 223, 2, true, ""},
	{"source_measurement_mantissa", "震源测量值尾数", 225, 4, true, ""},
	{"source_measurement_exponent", "震源测量值指数", 229, 2, true, ""},
	{"source_measurement_unit", "震源测量单位代码", 231, 2, true, ""},
}

func decodeBinaryFields(raw []byte, e Endian) []HeaderField {
	fields := decodeHeaderWords(raw, 3201, e, binaryHeaderWords)
	for index := range fields {
		switch fields[index].Key {
		case "sample_format":
			fields[index].Value = fmt.Sprintf("%d（%s）", fields[index].Raw, SampleFormatName(int(fields[index].Raw)))
		case "measurement_system":
			fields[index].Value = measurementSystemName(int(fields[index].Raw))
		case "segy_revision":
			fields[index].Value = fmt.Sprintf("%d.%d", fields[index].Raw>>8, fields[index].Raw&0xff)
		}
	}
	return fields
}

func decodeTraceFields(raw []byte, e Endian, elevationScale, coordinateFactor, shotpointScale float64, units int16) []HeaderField {
	fields := decodeHeaderWords(raw, 1, e, traceHeaderWords)
	coordinateUnit := coordinateUnitName(units)
	for index := range fields {
		field := &fields[index]
		switch field.Key {
		case "receiver_elevation", "source_elevation", "source_depth", "receiver_datum", "source_datum", "source_water_depth", "group_water_depth":
			field.Value = formatScaled(float64(field.Raw) * elevationScale)
		case "source_x", "source_y", "group_x", "group_y", "cdp_x", "cdp_y":
			field.Value = formatScaled(float64(field.Raw) * coordinateFactor)
			field.Unit = coordinateUnit
		case "shotpoint":
			field.Value = formatScaled(float64(field.Raw) * shotpointScale)
		case "coordinate_units":
			field.Value = coordinateUnitName(int16(field.Raw))
		case "trace_id":
			field.Value = traceIDName(int(field.Raw))
		case "time_basis":
			field.Value = timeBasisName(int(field.Raw))
		}
	}
	return fields
}

func decodeHeaderWords(raw []byte, base int, e Endian, specs []headerWordSpec) []HeaderField {
	fields := make([]HeaderField, 0, len(specs))
	for _, spec := range specs {
		var value int64
		if spec.width == 4 {
			if spec.signed {
				value = int64(headerI32(raw, spec.start, base, e))
			} else {
				value = int64(headerU32(raw, spec.start, base, e))
			}
		} else if spec.signed {
			value = int64(headerI16(raw, spec.start, base, e))
		} else {
			value = int64(headerU16(raw, spec.start, base, e))
		}
		fields = append(fields, HeaderField{Key: spec.key, Name: spec.name, ByteStart: spec.start, ByteEnd: spec.start + spec.width - 1, Raw: value, Value: strconv.FormatInt(value, 10), Unit: spec.unit})
	}
	return fields
}

func headerSlice(raw []byte, start, base, width int) []byte {
	offset := start - base
	if offset < 0 || width < 0 || offset+width > len(raw) {
		return nil
	}
	return raw[offset : offset+width]
}

func headerU16(raw []byte, start, base int, e Endian) int {
	b := headerSlice(raw, start, base, 2)
	if len(b) != 2 {
		return 0
	}
	return u16(b, e)
}

func headerI16(raw []byte, start, base int, e Endian) int {
	return int(int16(headerU16(raw, start, base, e)))
}

func headerU32(raw []byte, start, base int, e Endian) uint32 {
	b := headerSlice(raw, start, base, 4)
	if len(b) != 4 {
		return 0
	}
	return u32(b, e)
}

func headerI32(raw []byte, start, base int, e Endian) int32 {
	return int32(headerU32(raw, start, base, e))
}

func formatScaled(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return strconv.FormatFloat(value, 'g', -1, 64)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func measurementSystemName(code int) string {
	switch code {
	case 1:
		return "1（米制）"
	case 2:
		return "2（英尺制）"
	default:
		return strconv.Itoa(code) + "（未指定/未知）"
	}
}

func coordinateUnitName(code int16) string {
	switch code {
	case 1:
		return "长度"
	case 2:
		return "弧秒"
	case 3:
		return "十进制度"
	case 4:
		return "度分秒"
	default:
		return fmt.Sprintf("未指定/未知（%d）", code)
	}
}

func traceIDName(code int) string {
	labels := map[int]string{1: "地震数据", 2: "死道", 3: "空道", 4: "时间中断", 5: "上井时间", 6: "扫描", 7: "计时", 8: "水断", 9: "近场枪签名", 10: "远场枪签名", 11: "地震压力传感器", 12: "多分量垂直分量", 13: "多分量横向分量", 14: "多分量径向分量", 15: "旋转传感器垂直分量", 16: "旋转传感器横向分量", 17: "旋转传感器径向分量", 18: "振动质量", 19: "振动基板", 20: "振动估计地面力", 21: "振动参考", 22: "时差"}
	if label, ok := labels[code]; ok {
		return fmt.Sprintf("%d（%s）", code, label)
	}
	return strconv.Itoa(code)
}

func timeBasisName(code int) string {
	labels := map[int]string{1: "本地时间", 2: "GMT", 3: "其他", 4: "UTC", 5: "GPS"}
	if label, ok := labels[code]; ok {
		return fmt.Sprintf("%d（%s）", code, label)
	}
	return strconv.Itoa(code)
}

func sampleFormatDecodable(code int) bool {
	switch code {
	case 1, 2, 3, 5, 6, 7, 8, 9, 10, 11, 12, 15, 16:
		return true
	default:
		return false
	}
}

func decodeTextRunes(raw []byte, encoding TextEncoding) []rune {
	out := make([]rune, len(raw))
	for index, value := range raw {
		var r rune
		if encoding == TextEncodingEBCDICCP037 {
			r = cp037[value]
		} else {
			r = rune(value)
		}
		if r == 0 || r == '\r' || r == '\n' || r == '\t' {
			r = ' '
		} else if unicode.IsControl(r) || !unicode.IsPrint(r) {
			r = '\ufffd'
		}
		out[index] = r
	}
	return out
}

func textEncodingScore(raw []byte, encoding TextEncoding) int {
	runes := decodeTextRunes(raw, encoding)
	score := 0
	for _, r := range runes {
		switch {
		case r == '\ufffd':
			score -= 8
		case r == ' ':
			score += 3
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			score += 4
		case unicode.IsPrint(r):
			score += 2
		}
	}
	for line := 0; line < 40 && line*80+3 <= len(runes); line++ {
		prefix := string(runes[line*80 : line*80+3])
		if prefix[0] == 'C' && ((prefix[1] == ' ' && prefix[2] >= '0' && prefix[2] <= '9') || (prefix[1] >= '0' && prefix[1] <= '9')) {
			score += 40
		}
	}
	return score
}

// cp037 maps every EBCDIC code page 037 byte to its Unicode code point.
var cp037 = [256]rune{
	0x0000, 0x0001, 0x0002, 0x0003, 0x009c, 0x0009, 0x0086, 0x007f, 0x0097, 0x008d, 0x008e, 0x000b, 0x000c, 0x000d, 0x000e, 0x000f,
	0x0010, 0x0011, 0x0012, 0x0013, 0x009d, 0x0085, 0x0008, 0x0087, 0x0018, 0x0019, 0x0092, 0x008f, 0x001c, 0x001d, 0x001e, 0x001f,
	0x0080, 0x0081, 0x0082, 0x0083, 0x0084, 0x000a, 0x0017, 0x001b, 0x0088, 0x0089, 0x008a, 0x008b, 0x008c, 0x0005, 0x0006, 0x0007,
	0x0090, 0x0091, 0x0016, 0x0093, 0x0094, 0x0095, 0x0096, 0x0004, 0x0098, 0x0099, 0x009a, 0x009b, 0x0014, 0x0015, 0x009e, 0x001a,
	0x0020, 0x00a0, 0x00e2, 0x00e4, 0x00e0, 0x00e1, 0x00e3, 0x00e5, 0x00e7, 0x00f1, 0x00a2, 0x002e, 0x003c, 0x0028, 0x002b, 0x007c,
	0x0026, 0x00e9, 0x00ea, 0x00eb, 0x00e8, 0x00ed, 0x00ee, 0x00ef, 0x00ec, 0x00df, 0x0021, 0x0024, 0x002a, 0x0029, 0x003b, 0x00ac,
	0x002d, 0x002f, 0x00c2, 0x00c4, 0x00c0, 0x00c1, 0x00c3, 0x00c5, 0x00c7, 0x00d1, 0x00a6, 0x002c, 0x0025, 0x005f, 0x003e, 0x003f,
	0x00f8, 0x00c9, 0x00ca, 0x00cb, 0x00c8, 0x00cd, 0x00ce, 0x00cf, 0x00cc, 0x0060, 0x003a, 0x0023, 0x0040, 0x0027, 0x003d, 0x0022,
	0x00d8, 0x0061, 0x0062, 0x0063, 0x0064, 0x0065, 0x0066, 0x0067, 0x0068, 0x0069, 0x00ab, 0x00bb, 0x00f0, 0x00fd, 0x00fe, 0x00b1,
	0x00b0, 0x006a, 0x006b, 0x006c, 0x006d, 0x006e, 0x006f, 0x0070, 0x0071, 0x0072, 0x00aa, 0x00ba, 0x00e6, 0x00b8, 0x00c6, 0x00a4,
	0x00b5, 0x007e, 0x0073, 0x0074, 0x0075, 0x0076, 0x0077, 0x0078, 0x0079, 0x007a, 0x00a1, 0x00bf, 0x00d0, 0x00dd, 0x00de, 0x00ae,
	0x005e, 0x00a3, 0x00a5, 0x00b7, 0x00a9, 0x00a7, 0x00b6, 0x00bc, 0x00bd, 0x00be, 0x005b, 0x005d, 0x00af, 0x00a8, 0x00b4, 0x00d7,
	0x007b, 0x0041, 0x0042, 0x0043, 0x0044, 0x0045, 0x0046, 0x0047, 0x0048, 0x0049, 0x00ad, 0x00f4, 0x00f6, 0x00f2, 0x00f3, 0x00f5,
	0x007d, 0x004a, 0x004b, 0x004c, 0x004d, 0x004e, 0x004f, 0x0050, 0x0051, 0x0052, 0x00b9, 0x00fb, 0x00fc, 0x00f9, 0x00fa, 0x00ff,
	0x005c, 0x00f7, 0x0053, 0x0054, 0x0055, 0x0056, 0x0057, 0x0058, 0x0059, 0x005a, 0x00b2, 0x00d4, 0x00d6, 0x00d2, 0x00d3, 0x00d5,
	0x0030, 0x0031, 0x0032, 0x0033, 0x0034, 0x0035, 0x0036, 0x0037, 0x0038, 0x0039, 0x00b3, 0x00db, 0x00dc, 0x00d9, 0x00da, 0x009f,
}
