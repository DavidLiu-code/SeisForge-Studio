//go:build windows

package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// traceHeaderFilter is intentionally UI-independent. The analysis window can
// rebuild a table from the already decoded HeaderField slice without touching
// the SEG-Y reader again.
type traceHeaderFilter int

const (
	traceHeaderFilterAll traceHeaderFilter = iota
	traceHeaderFilterKey
	traceHeaderFilterNonZero
)

// traceHeaderTableRow is the presentation-ready, immutable form of one SEG-Y
// header word. Key is retained for restoring the selected field when the user
// switches traces, files, or filters.
type traceHeaderTableRow struct {
	Key       string
	ByteRange string
	Name      string
	Raw       string
	Explained string
}

var binaryHeaderImportantKeys = map[string]struct{}{
	"job_id":                {},
	"line_number":           {},
	"reel_number":           {},
	"sample_interval":       {},
	"samples_per_trace":     {},
	"sample_format":         {},
	"ensemble_fold":         {},
	"trace_sorting":         {},
	"measurement_system":    {},
	"segy_revision":         {},
	"fixed_length_trace":    {},
	"extended_text_headers": {},
}

var traceHeaderImportantKeys = map[string]struct{}{
	"trace_sequence_line":  {},
	"trace_sequence_file":  {},
	"field_record":         {},
	"cdp":                  {},
	"trace_id":             {},
	"offset":               {},
	"elevation_scalar":     {},
	"coordinate_scalar":    {},
	"coordinate_units":     {},
	"source_x":             {},
	"source_y":             {},
	"group_x":              {},
	"group_y":              {},
	"delay_recording_time": {},
	"samples_in_trace":     {},
	"sample_interval":      {},
	"cdp_x":                {},
	"cdp_y":                {},
	"inline":               {},
	"crossline":            {},
	"shotpoint":            {},
	"shotpoint_scalar":     {},
	"time_scalar":          {},
}

// buildTraceHeaderTableRows keeps the decoder-provided SEG-Y byte order. In
// particular, key filtering never iterates the key set itself, so the UI
// cannot reorder fields nondeterministically.
func buildTraceHeaderTableRows(fields []segy.HeaderField, filter traceHeaderFilter, keySet map[string]struct{}) []traceHeaderTableRow {
	rows := make([]traceHeaderTableRow, 0, len(fields))
	for _, field := range fields {
		switch filter {
		case traceHeaderFilterKey:
			if _, ok := keySet[field.Key]; !ok {
				continue
			}
		case traceHeaderFilterNonZero:
			if field.Raw == 0 {
				continue
			}
		}
		rows = append(rows, traceHeaderTableRow{
			Key:       field.Key,
			ByteRange: traceHeaderByteRange(field.ByteStart, field.ByteEnd),
			Name:      field.Name,
			Raw:       strconv.FormatInt(field.Raw, 10),
			Explained: traceHeaderExplainedValue(field),
		})
	}
	return rows
}

func traceHeaderByteRange(start, end int) string {
	if start == end {
		return strconv.Itoa(start)
	}
	return fmt.Sprintf("%d-%d", start, end)
}

func traceHeaderExplainedValue(field segy.HeaderField) string {
	raw := strconv.FormatInt(field.Raw, 10)
	value := strings.TrimSpace(field.Value)
	if value == "" {
		value = raw
	}
	unit := strings.TrimSpace(field.Unit)
	if unit == "" {
		if value == raw {
			return "—"
		}
		return value
	}
	// Header decoders normally keep Value and Unit separate. Be tolerant of a
	// caller that already supplied the unit so copied tables never say, for
	// example, "2000 us us".
	if value == unit || strings.HasSuffix(value, " "+unit) {
		return value
	}
	return value + " " + unit
}

// traceHeaderRowsTSV produces a clipboard-friendly table that can be pasted
// directly into Excel. Embedded tabs and line breaks are flattened so every
// SEG-Y field remains exactly one row and four columns.
func traceHeaderRowsTSV(rows []traceHeaderTableRow) string {
	var builder strings.Builder
	builder.WriteString("字节位置（1-based）\t字段\t原始值\t解释值\r\n")
	for _, row := range rows {
		builder.WriteString(traceHeaderTSVCell(row.ByteRange))
		builder.WriteByte('\t')
		builder.WriteString(traceHeaderTSVCell(row.Name))
		builder.WriteByte('\t')
		builder.WriteString(traceHeaderTSVCell(row.Raw))
		builder.WriteByte('\t')
		builder.WriteString(traceHeaderTSVCell(row.Explained))
		builder.WriteString("\r\n")
	}
	return builder.String()
}

func traceHeaderTSVCell(value string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\r', '\n':
			return ' '
		default:
			return r
		}
	}, value)
}
