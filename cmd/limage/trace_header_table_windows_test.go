//go:build windows

package main

import (
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func TestBuildTraceHeaderTableRowsFormatsRawAndExplainedValues(t *testing.T) {
	fields := []segy.HeaderField{
		{Key: "plain", Name: "普通中文字段", ByteStart: 1, ByteEnd: 4, Raw: -25, Value: "-25"},
		{Key: "unit", Name: "采样间隔", ByteStart: 5, ByteEnd: 6, Raw: 2000, Value: "2000", Unit: "us"},
		{Key: "scaled", Name: "坐标 X", ByteStart: 7, ByteEnd: 10, Raw: 844, Value: "84.4", Unit: "m"},
		{Key: "enum", Name: "道识别", ByteStart: 11, ByteEnd: 12, Raw: 1, Value: "1（地震数据）"},
		{Key: "unit_once", Name: "已有单位", ByteStart: 13, ByteEnd: 14, Raw: 2000, Value: "2000 us", Unit: "us"},
	}
	rows := buildTraceHeaderTableRows(fields, traceHeaderFilterAll, nil)
	if len(rows) != len(fields) {
		t.Fatalf("row count=%d, want %d", len(rows), len(fields))
	}
	if rows[0].ByteRange != "1-4" || rows[0].Name != "普通中文字段" || rows[0].Raw != "-25" || rows[0].Explained != "—" {
		t.Fatalf("plain field formatted incorrectly: %+v", rows[0])
	}
	if rows[1].Explained != "2000 us" {
		t.Fatalf("unit missing from explanation: %+v", rows[1])
	}
	if rows[2].Explained != "84.4 m" {
		t.Fatalf("scaled explanation incorrect: %+v", rows[2])
	}
	if rows[3].Explained != "1（地震数据）" {
		t.Fatalf("enum explanation incorrect: %+v", rows[3])
	}
	if rows[4].Explained != "2000 us" {
		t.Fatalf("unit was duplicated: %+v", rows[4])
	}
}

func TestBuildTraceHeaderTableRowsFiltersWithoutReordering(t *testing.T) {
	fields := []segy.HeaderField{
		{Key: "late_key", Name: "先出现", ByteStart: 41, ByteEnd: 44, Raw: 0, Value: "0"},
		{Key: "ordinary", Name: "中间字段", ByteStart: 45, ByteEnd: 48, Raw: -7, Value: "-7"},
		{Key: "early_key", Name: "后出现", ByteStart: 49, ByteEnd: 52, Raw: 3, Value: "3"},
	}
	keySet := map[string]struct{}{"early_key": {}, "late_key": {}}

	all := buildTraceHeaderTableRows(fields, traceHeaderFilterAll, keySet)
	if len(all) != 3 || all[0].Key != "late_key" || all[2].Key != "early_key" {
		t.Fatalf("all filter changed SEG-Y order: %+v", all)
	}
	key := buildTraceHeaderTableRows(fields, traceHeaderFilterKey, keySet)
	if len(key) != 2 || key[0].Key != "late_key" || key[1].Key != "early_key" {
		t.Fatalf("key filter changed SEG-Y order: %+v", key)
	}
	nonZero := buildTraceHeaderTableRows(fields, traceHeaderFilterNonZero, keySet)
	if len(nonZero) != 2 || nonZero[0].Key != "ordinary" || nonZero[1].Key != "early_key" {
		t.Fatalf("non-zero filter must use Raw != 0: %+v", nonZero)
	}
}

func TestTraceHeaderImportantKeySetsCoverPlannedFields(t *testing.T) {
	for _, key := range []string{"job_id", "line_number", "reel_number", "sample_interval", "samples_per_trace", "sample_format", "ensemble_fold", "trace_sorting", "measurement_system", "segy_revision", "fixed_length_trace", "extended_text_headers"} {
		if _, ok := binaryHeaderImportantKeys[key]; !ok {
			t.Errorf("binary important key %q missing", key)
		}
	}
	for _, key := range []string{"trace_sequence_line", "trace_sequence_file", "field_record", "cdp", "trace_id", "offset", "elevation_scalar", "coordinate_scalar", "coordinate_units", "source_x", "source_y", "group_x", "group_y", "cdp_x", "cdp_y", "delay_recording_time", "samples_in_trace", "sample_interval", "inline", "crossline", "shotpoint", "shotpoint_scalar"} {
		if _, ok := traceHeaderImportantKeys[key]; !ok {
			t.Errorf("trace important key %q missing", key)
		}
	}
}

func TestTraceHeaderRowsTSVHasStableColumnsAndFlattensCells(t *testing.T) {
	rows := []traceHeaderTableRow{{
		Key: "coordinate_scalar", ByteRange: "71-72", Name: "坐标\t比例因子", Raw: "-10", Explained: "-10\r\n倍",
	}}
	got := traceHeaderRowsTSV(rows)
	want := "字节位置（1-based）\t字段\t原始值\t解释值\r\n71-72\t坐标 比例因子\t-10\t-10  倍\r\n"
	if got != want {
		t.Fatalf("unexpected TSV:\n%q\nwant:\n%q", got, want)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\r\n"), "\r\n")
	for _, line := range lines {
		if columns := strings.Count(line, "\t") + 1; columns != 4 {
			t.Fatalf("TSV row has %d columns: %q", columns, line)
		}
	}
}
