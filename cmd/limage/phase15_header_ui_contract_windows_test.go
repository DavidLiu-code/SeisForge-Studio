//go:build windows

package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func phase15TraceAnalysisSource(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve Phase 15 contract-test source path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "trace_analysis_windows.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func phase15FunctionSource(t *testing.T, source, name, nextName string) string {
	t.Helper()
	start := strings.Index(source, "func "+name+"(")
	if start < 0 {
		t.Fatalf("function %s not found", name)
	}
	end := len(source)
	if nextName != "" {
		if relative := strings.Index(source[start+1:], "func "+nextName+"("); relative >= 0 {
			end = start + 1 + relative
		}
	}
	return source[start:end]
}

func TestPhase15HeaderListViewStaticContract(t *testing.T) {
	source := phase15TraceAnalysisSource(t)
	create := phase15FunctionSource(t, source, "createTraceHeaderTable", "traceHeaderTablePositionKeys")
	for _, want := range []string{
		`{"字节位置（1-based）", 122, traceLVCFmtLeft}`,
		`{"字段", 230, traceLVCFmtLeft}`,
		`{"原始值", 150, traceLVCFmtRight}`,
		`{"解释值", 480, traceLVCFmtLeft}`,
		`traceLVSExGridLines | traceLVSExFullRowSelect | traceLVSExDoubleBuffer`,
		`traceLVSNoSortHeader`,
		`"显示原始 Hex"`,
	} {
		if !strings.Contains(create, want) {
			t.Errorf("header ListView contract missing %q", want)
		}
	}
	if strings.Contains(create, "hexExpanded: true") || strings.Contains(create, "hexExpanded = true") {
		t.Error("raw Hex must be collapsed when a header table is first created")
	}

	rebuild := phase15FunctionSource(t, source, "rebuildTraceHeaderTable", "setTraceHeaderTableData")
	pause := strings.Index(rebuild, "pSendMessageW.Call(view.list, traceWMSetRedraw, 0, 0)")
	deleteItems := strings.Index(rebuild, "pSendMessageW.Call(view.list, traceLVMDeleteAllItems, 0, 0)")
	resume := strings.Index(rebuild, "pSendMessageW.Call(view.list, traceWMSetRedraw, 1, 0)")
	if pause < 0 || deleteItems <= pause || resume <= deleteItems {
		t.Fatalf("table rebuild must suspend redraw before replacement and resume afterwards: pause=%d delete=%d resume=%d", pause, deleteItems, resume)
	}

	createUI := phase15FunctionSource(t, source, "createTraceAnalysisUI", "populateTraceAnalysisSourceCombo")
	if !strings.Contains(createUI, `[]string{"文本卷头", "二进制卷头"}`) || !strings.Contains(createUI, "IDTRACE_HEADER_SUBTABS") {
		t.Error("file-header page must retain its Text/Binary inner tabs")
	}
}

func TestPhase15HeaderSummariesKeepWarningsAndConsistencyVisible(t *testing.T) {
	file := &traceAnalysisFileResult{
		target: traceAnalysisTarget{Role: "A", Trace: 6, HasGeometry: true, Inline: 999, Crossline: 88},
		info: segy.Info{
			Path: "example.sgy", FileSize: 123456, TraceCount: 18,
			SamplesPerTrace: 1000, SampleIntervalUS: 2000, FormatCode: 5,
			Endian: segy.Big, DataStart: 3600,
		},
		binaryHeader: segy.BinaryHeader{
			RevisionMajor: 1, RevisionMinor: 0, FixedLengthTraceFlag: 1,
			ExtendedTextHeaderCount: 2, Warnings: []string{"扩展卷头计数异常"},
		},
		traceHeader: segy.TraceHeader{
			TraceSequenceFile: 7, FieldRecord: 660, CDP: 844, Inline: 1220, Crossline: 32,
			SampleCount: 900, SampleIntervalUS: 4000, Warnings: []string{"道头坐标警告"},
		},
		headerError: "文本卷头读取失败",
		waveError:   "测试波形不可用",
	}

	fileSummary := traceAnalysisFileSummary(file)
	for _, want := range []string{"example.sgy", "格式：5", "警告：文本卷头读取失败；扩展卷头计数异常"} {
		if !strings.Contains(fileSummary, want) {
			t.Errorf("file summary missing %q:\n%s", want, fileSummary)
		}
	}
	traceSummary := traceAnalysisTraceSummary(file)
	for _, want := range []string{
		"来源：A", "文件道号：7（1-based）", "当前几何：Inline 1220 / Crossline 32",
		"样点数不一致（道头 900 / 二进制卷头 1000）",
		"采样间隔不一致（道头 4000 / 二进制卷头 2000 us）",
		"警告：道头坐标警告；波形：测试波形不可用",
	} {
		if !strings.Contains(traceSummary, want) {
			t.Errorf("trace summary missing %q:\n%s", want, traceSummary)
		}
	}
	if strings.Contains(traceSummary, "Inline 999 / Crossline 88") {
		t.Fatal("fresh trace-header geometry must take precedence over stale click geometry")
	}
}

func TestPhase15HeaderCopyIncludesSummaryFilteredRowsAndCompleteHex(t *testing.T) {
	fields := []segy.HeaderField{
		{Key: "trace_sequence_file", Name: "文件内道序号", ByteStart: 5, ByteEnd: 8, Raw: 660, Value: "660"},
		{Key: "ordinary", Name: "普通字段", ByteStart: 9, ByteEnd: 12, Raw: 123, Value: "123"},
	}
	raw := make([]byte, 240)
	for index := range raw {
		raw[index] = byte(index)
	}
	view := traceHeaderTableControls{
		rows:    buildTraceHeaderTableRows(fields, traceHeaderFilterKey, traceHeaderImportantKeys),
		raw:     raw,
		rawBase: 1,
	}
	summary := "来源：A\r\n一致性：样点数不一致\r\n警告：测试警告"
	got := traceAnalysisHeaderTableCopyText(summary, "道头字段", "道头原始 Hex（240 字节）", &view)
	for _, want := range []string{
		summary,
		"字节位置（1-based）\t字段\t原始值\t解释值\r\n",
		"5-8\t文件内道序号\t660\t—\r\n",
		"===== 道头原始 Hex（240 字节） =====",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("copied header text missing %q", want)
		}
	}
	if strings.Contains(got, "普通字段") {
		t.Error("copy must contain the current filtered rows, not hidden fields")
	}
	if completeHex := rawHexText(raw, 1); !strings.HasSuffix(got, completeHex) {
		t.Error("copy must include the complete 240-byte raw Hex even while Hex is collapsed")
	}
}

func TestPhase15SelectionRestorationUsesStableFieldKeyWithFallback(t *testing.T) {
	source := phase15TraceAnalysisSource(t)
	rebuild := phase15FunctionSource(t, source, "rebuildTraceHeaderTable", "setTraceHeaderTableData")
	for _, want := range []string{
		"selectedKey, topKey := traceHeaderTablePositionKeys(view)",
		"view.selectedKey = selectedKey",
		"view.topKey = topKey",
		"if row.Key == view.selectedKey",
		"if row.Key == view.topKey",
		"if selectedIndex < 0 && len(rows) > 0",
		"if topIndex < 0 && len(rows) > 0",
	} {
		if !strings.Contains(rebuild, want) {
			t.Errorf("field-position restoration contract missing %q", want)
		}
	}
}

func TestPhase15Format4RetainsTablesSummariesAndFullHexCopy(t *testing.T) {
	path := writePhase9PseudoFixture(t)
	fixture, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	var format [2]byte
	binary.BigEndian.PutUint16(format[:], 4)
	if _, err = fixture.WriteAt(format[:], 3224); err != nil {
		_ = fixture.Close()
		t.Fatal(err)
	}
	if err = fixture.Close(); err != nil {
		t.Fatal(err)
	}

	result := buildTraceAnalysisResult(15, traceAnalysisSelection{Targets: []traceAnalysisTarget{{Role: "A", Path: path, Trace: 0, SampleStart: 0, SampleEnd: 47}}}, false)
	if len(result.files) != 1 {
		t.Fatalf("format-4 result files=%d, want 1", len(result.files))
	}
	file := &result.files[0]
	if len(file.binaryHeader.Fields) == 0 || len(file.traceHeader.Fields) == 0 {
		t.Fatal("format 4 must retain decoded binary- and trace-header table rows")
	}
	if !strings.Contains(traceAnalysisFileSummary(file), "格式：4") || !strings.Contains(traceAnalysisTraceSummary(file), "格式码 4") {
		t.Fatalf("format-4 header-only status is not visible in summaries: file=%q trace=%q", traceAnalysisFileSummary(file), traceAnalysisTraceSummary(file))
	}
	view := traceHeaderTableControls{
		rows:    buildTraceHeaderTableRows(file.traceHeader.Fields, traceHeaderFilterAll, traceHeaderImportantKeys),
		raw:     file.traceHeader.Raw,
		rawBase: 1,
	}
	copyText := traceAnalysisHeaderTableCopyText(traceAnalysisTraceSummary(file), "道头字段", "道头原始 Hex（240 字节）", &view)
	if !strings.HasSuffix(copyText, rawHexText(file.traceHeader.Raw, 1)) {
		t.Error("format-4 trace-header copy lost its full 240-byte Hex payload")
	}
}
