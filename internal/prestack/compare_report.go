package prestack

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// CompareMatchReport is a versioned, metadata-only summary of an A/B match.
// It intentionally contains no sample arrays or amplitude values. Paths,
// selection text and the sample window are provenance metadata only.
type CompareMatchReport struct {
	SchemaVersion string                   `json:"schema_version"`
	APath         string                   `json:"a_path,omitempty"`
	BPath         string                   `json:"b_path,omitempty"`
	Selection     string                   `json:"selection,omitempty"`
	Primary       string                   `json:"primary,omitempty"`
	Secondary     string                   `json:"secondary,omitempty"`
	SampleWindow  SampleWindow             `json:"sample_window"`
	Strategy      MatchKeyStrategy         `json:"strategy"`
	Matched       int                      `json:"matched"`
	AOnly         int                      `json:"a_only"`
	BOnly         int                      `json:"b_only"`
	AmbiguousA    int                      `json:"ambiguous_a"`
	AmbiguousB    int                      `json:"ambiguous_b"`
	InvalidA      int                      `json:"invalid_a"`
	InvalidB      int                      `json:"invalid_b"`
	Axis          SampleAxisCompatibility  `json:"sample_axis"`
	Reason        string                   `json:"reason,omitempty"`
	Pairs         []CompareMatchReportPair `json:"pairs,omitempty"`
	Rows          []CompareMatchReportRow  `json:"rows,omitempty"`
}

// CompareMatchReportPair contains only physical trace numbers and the
// explainable metadata key used for a matched pair.
type CompareMatchReportPair struct {
	ATrace    int64                 `json:"a_trace"`
	BTrace    int64                 `json:"b_trace"`
	Strategy  MatchKeyStrategy      `json:"strategy"`
	Key       TraceMatchKey         `json:"key"`
	AMetadata *CompareTraceMetadata `json:"a_metadata,omitempty"`
	BMetadata *CompareTraceMetadata `json:"b_metadata,omitempty"`
}

// CompareTraceMetadata is a header-only snapshot attached to report rows. It
// deliberately contains no sample values, amplitudes, or reader state.
type CompareTraceMetadata struct {
	TraceNumber    int64   `json:"trace_number"`
	SourceID       int32   `json:"source_id,omitempty"`
	ReceiverID     int32   `json:"receiver_id,omitempty"`
	CDP            int32   `json:"cdp,omitempty"`
	Inline         int32   `json:"inline,omitempty"`
	Crossline      int32   `json:"crossline,omitempty"`
	SourceX        float64 `json:"source_x,omitempty"`
	SourceY        float64 `json:"source_y,omitempty"`
	ReceiverX      float64 `json:"receiver_x,omitempty"`
	ReceiverY      float64 `json:"receiver_y,omitempty"`
	MidpointX      float64 `json:"midpoint_x,omitempty"`
	MidpointY      float64 `json:"midpoint_y,omitempty"`
	HeaderOffset   float64 `json:"header_offset,omitempty"`
	ComputedOffset float64 `json:"computed_offset,omitempty"`
	Offset         float64 `json:"offset,omitempty"`
	Azimuth        float64 `json:"azimuth,omitempty"`
	HasSource      bool    `json:"has_source,omitempty"`
	HasReceiver    bool    `json:"has_receiver,omitempty"`
	HasCDP         bool    `json:"has_cdp,omitempty"`
	HasOffset      bool    `json:"has_offset,omitempty"`
	HasAzimuth     bool    `json:"has_azimuth,omitempty"`
}

// CompareMatchReportRow is a metadata-only classification row. A row may
// describe a matched pair or one side of an unmatched/ambiguous/invalid
// classification. No samples or amplitudes are ever included.
type CompareMatchReportRow struct {
	Classification string                `json:"classification"`
	Category       string                `json:"category"`
	ATrace         int64                 `json:"a_trace,omitempty"`
	BTrace         int64                 `json:"b_trace,omitempty"`
	Strategy       MatchKeyStrategy      `json:"strategy"`
	Key            *TraceMatchKey        `json:"key,omitempty"`
	AMetadata      *CompareTraceMetadata `json:"a_metadata,omitempty"`
	BMetadata      *CompareTraceMetadata `json:"b_metadata,omitempty"`
}

// CompareMatchReportOptions supplies provenance fields that are not part of
// the match algorithm itself.
type CompareMatchReportOptions struct {
	APath        string
	BPath        string
	Selection    string
	Primary      string
	Secondary    string
	SampleWindow SampleWindow
	IncludePairs bool
	ARecords     []PrestackTraceRecord
	BRecords     []PrestackTraceRecord
}

func reportRecordMap(records []PrestackTraceRecord) map[int64]PrestackTraceRecord {
	if len(records) == 0 {
		return nil
	}
	out := make(map[int64]PrestackTraceRecord, len(records))
	for i, record := range records {
		out[traceNumber(record, i)] = record
	}
	return out
}

func compareTraceMetadata(record PrestackTraceRecord) CompareTraceMetadata {
	return CompareTraceMetadata{
		TraceNumber: record.TraceNumber,
		SourceID:    record.SourceID, ReceiverID: record.ReceiverID, CDP: record.CDP,
		Inline: record.Inline, Crossline: record.Crossline,
		SourceX: record.SourceX, SourceY: record.SourceY,
		ReceiverX: record.ReceiverX, ReceiverY: record.ReceiverY,
		MidpointX: record.MidpointX, MidpointY: record.MidpointY,
		HeaderOffset: record.HeaderOffset, ComputedOffset: record.ComputedOffset,
		Offset: record.Offset, Azimuth: record.Azimuth,
		HasSource: record.HasSource, HasReceiver: record.HasReceiver,
		HasCDP: record.HasCDP, HasOffset: record.HasOffset, HasAzimuth: record.HasAzimuth,
	}
}

// BuildCompareMatchReport creates a defensive report snapshot. IncludePairs
// controls whether the legacy Pairs field is populated; classification rows
// still retain all unmatched metadata so the summary remains auditable.
func BuildCompareMatchReport(result CompareMatchResult, axis SampleAxisCompatibility, includePairs bool) CompareMatchReport {
	return BuildCompareMatchReportWithOptions(result, axis, CompareMatchReportOptions{IncludePairs: includePairs})
}

// BuildCompareMatchReportWithOptions creates schema 1.1 metadata and never
// aliases any caller-owned slices or keys.
func BuildCompareMatchReportWithOptions(result CompareMatchResult, axis SampleAxisCompatibility, options CompareMatchReportOptions) CompareMatchReport {
	r := CompareMatchReport{
		SchemaVersion: "1.1",
		APath:         options.APath, BPath: options.BPath, Selection: options.Selection,
		Primary: options.Primary, Secondary: options.Secondary,
		SampleWindow: options.SampleWindow,
		Strategy:     result.Strategy,
		Matched:      len(result.Pairs),
		AOnly:        len(result.AOnly), BOnly: len(result.BOnly),
		AmbiguousA: len(result.AmbiguousA), AmbiguousB: len(result.AmbiguousB),
		InvalidA: len(result.InvalidA), InvalidB: len(result.InvalidB),
		Axis: axis, Reason: result.Reason,
	}
	aByTrace := reportRecordMap(options.ARecords)
	bByTrace := reportRecordMap(options.BRecords)
	metadataAt := func(records []PrestackTraceRecord, byTrace map[int64]PrestackTraceRecord, trace int64, position int) *CompareTraceMetadata {
		if position >= 0 && position < len(records) {
			m := compareTraceMetadata(records[position])
			return &m
		}
		if record, ok := byTrace[trace]; ok {
			m := compareTraceMetadata(record)
			return &m
		}
		return nil
	}
	if options.IncludePairs {
		r.Pairs = make([]CompareMatchReportPair, len(result.Pairs))
	}
	rows := make([]CompareMatchReportRow, 0, len(result.Pairs)+len(result.AOnly)+len(result.BOnly)+len(result.AmbiguousA)+len(result.AmbiguousB)+len(result.InvalidA)+len(result.InvalidB))
	for i, p := range result.Pairs {
		aMetadata := metadataAt(options.ARecords, aByTrace, p.ATrace, p.AIndex)
		bMetadata := metadataAt(options.BRecords, bByTrace, p.BTrace, p.BIndex)
		if options.IncludePairs {
			r.Pairs[i] = CompareMatchReportPair{ATrace: p.ATrace, BTrace: p.BTrace, Strategy: p.Strategy, Key: p.Key, AMetadata: aMetadata, BMetadata: bMetadata}
		}
		key := p.Key
		rows = append(rows, CompareMatchReportRow{Classification: "matched", Category: "matched", ATrace: p.ATrace, BTrace: p.BTrace, Strategy: p.Strategy, Key: &key, AMetadata: aMetadata, BMetadata: bMetadata})
	}
	appendTraceRows := func(classification, category string, traces []int64, records []PrestackTraceRecord, byTrace map[int64]PrestackTraceRecord, isA bool) {
		for _, trace := range traces {
			row := CompareMatchReportRow{Classification: classification, Category: category, ATrace: trace, Strategy: MatchKeyInvalid}
			metadata := metadataAt(records, byTrace, trace, -1)
			if isA {
				row.AMetadata = metadata
			} else {
				row.ATrace = 0
				row.BTrace = trace
				row.BMetadata = metadata
			}
			rows = append(rows, row)
		}
	}
	appendTraceRows("a_only", "a_only", result.AOnly, options.ARecords, aByTrace, true)
	appendTraceRows("b_only", "b_only", result.BOnly, options.BRecords, bByTrace, false)
	appendTraceRows("ambiguous_a", "ambiguous", result.AmbiguousA, options.ARecords, aByTrace, true)
	appendTraceRows("ambiguous_b", "ambiguous", result.AmbiguousB, options.BRecords, bByTrace, false)
	appendTraceRows("invalid_a", "invalid", result.InvalidA, options.ARecords, aByTrace, true)
	appendTraceRows("invalid_b", "invalid", result.InvalidB, options.BRecords, bByTrace, false)
	r.Rows = rows
	return r
}

func (r CompareMatchReport) MarshalJSON() ([]byte, error) {
	type alias CompareMatchReport
	return json.Marshal(alias(r))
}

func (r CompareMatchReport) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

func WriteCompareMatchReportJSON(w io.Writer, r CompareMatchReport) error {
	if w == nil {
		return fmt.Errorf("nil report writer")
	}
	b, err := r.JSON()
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// WriteCompareMatchReportCSV writes a fixed metadata-only header and one row
// per classification. It never writes sample arrays or amplitudes.
func WriteCompareMatchReportCSV(w io.Writer, r CompareMatchReport) error {
	if w == nil {
		return fmt.Errorf("nil report writer")
	}
	c := csv.NewWriter(w)
	header := []string{"schema_version", "a_path", "b_path", "selection", "primary", "secondary", "sample_window_start", "sample_window_end", "strategy", "matched", "a_only", "b_only", "ambiguous_a", "ambiguous_b", "invalid_a", "invalid_b", "sample_axis_compatible", "axis_reason", "reason", "classification", "category", "a_trace", "b_trace", "pair_strategy", "key_strategy", "source_id", "receiver_id", "cdp", "offset", "key_source_x", "key_source_y", "key_receiver_x", "key_receiver_y", "a_trace_number", "a_source_id", "a_receiver_id", "a_cdp", "a_inline", "a_crossline", "a_source_x", "a_source_y", "a_receiver_x", "a_receiver_y", "a_midpoint_x", "a_midpoint_y", "a_header_offset", "a_computed_offset", "a_offset", "a_azimuth", "b_trace_number", "b_source_id", "b_receiver_id", "b_cdp", "b_inline", "b_crossline", "b_source_x", "b_source_y", "b_receiver_x", "b_receiver_y", "b_midpoint_x", "b_midpoint_y", "b_header_offset", "b_computed_offset", "b_offset", "b_azimuth"}
	if err := c.Write(header); err != nil {
		return err
	}
	metadataValues := func(m *CompareTraceMetadata) []string {
		if m == nil {
			return make([]string, 16)
		}
		return []string{strconv.FormatInt(m.TraceNumber, 10), strconv.FormatInt(int64(m.SourceID), 10), strconv.FormatInt(int64(m.ReceiverID), 10), strconv.FormatInt(int64(m.CDP), 10), strconv.FormatInt(int64(m.Inline), 10), strconv.FormatInt(int64(m.Crossline), 10), strconv.FormatFloat(m.SourceX, 'g', -1, 64), strconv.FormatFloat(m.SourceY, 'g', -1, 64), strconv.FormatFloat(m.ReceiverX, 'g', -1, 64), strconv.FormatFloat(m.ReceiverY, 'g', -1, 64), strconv.FormatFloat(m.MidpointX, 'g', -1, 64), strconv.FormatFloat(m.MidpointY, 'g', -1, 64), strconv.FormatFloat(m.HeaderOffset, 'g', -1, 64), strconv.FormatFloat(m.ComputedOffset, 'g', -1, 64), strconv.FormatFloat(m.Offset, 'g', -1, 64), strconv.FormatFloat(m.Azimuth, 'g', -1, 64)}
	}
	writeRow := func(row *CompareMatchReportRow) error {
		classification, category, aTrace, bTrace, pairStrategy, keyStrategy := "summary", "summary", "", "", "", ""
		sourceID, receiverID, cdp, offset, keySourceX, keySourceY, keyReceiverX, keyReceiverY := "", "", "", "", "", "", "", ""
		var aMetadata, bMetadata *CompareTraceMetadata
		if row != nil {
			classification = row.Classification
			category = row.Category
			if row.ATrace != 0 || row.Classification == "matched" || row.Classification == "a_only" || row.Classification == "ambiguous_a" || row.Classification == "invalid_a" {
				aTrace = strconv.FormatInt(row.ATrace, 10)
			}
			if row.BTrace != 0 || row.Classification == "matched" || row.Classification == "b_only" || row.Classification == "ambiguous_b" || row.Classification == "invalid_b" {
				bTrace = strconv.FormatInt(row.BTrace, 10)
			}
			pairStrategy = row.Strategy.String()
			if row.Key != nil {
				keyStrategy = row.Key.Strategy.String()
				sourceID = strconv.FormatInt(int64(row.Key.SourceID), 10)
				receiverID = strconv.FormatInt(int64(row.Key.ReceiverID), 10)
				cdp = strconv.FormatInt(int64(row.Key.CDP), 10)
				offset = strconv.FormatFloat(row.Key.Offset, 'g', -1, 64)
				keySourceX = strconv.FormatFloat(row.Key.SourceX, 'g', -1, 64)
				keySourceY = strconv.FormatFloat(row.Key.SourceY, 'g', -1, 64)
				keyReceiverX = strconv.FormatFloat(row.Key.ReceiverX, 'g', -1, 64)
				keyReceiverY = strconv.FormatFloat(row.Key.ReceiverY, 'g', -1, 64)
			}
			aMetadata, bMetadata = row.AMetadata, row.BMetadata
		}
		values := []string{r.SchemaVersion, r.APath, r.BPath, r.Selection, r.Primary, r.Secondary, strconv.Itoa(r.SampleWindow.Start), strconv.Itoa(r.SampleWindow.End), r.Strategy.String(), strconv.Itoa(r.Matched), strconv.Itoa(r.AOnly), strconv.Itoa(r.BOnly), strconv.Itoa(r.AmbiguousA), strconv.Itoa(r.AmbiguousB), strconv.Itoa(r.InvalidA), strconv.Itoa(r.InvalidB), strconv.FormatBool(r.Axis.Compatible), r.Axis.Reason, r.Reason, classification, category, aTrace, bTrace, pairStrategy, keyStrategy, sourceID, receiverID, cdp, offset, keySourceX, keySourceY, keyReceiverX, keyReceiverY}
		values = append(values, metadataValues(aMetadata)...)
		values = append(values, metadataValues(bMetadata)...)
		return c.Write(values)
	}
	if len(r.Rows) == 0 {
		if err := writeRow(nil); err != nil {
			return err
		}
	} else {
		for i := range r.Rows {
			if err := writeRow(&r.Rows[i]); err != nil {
				return err
			}
		}
	}
	c.Flush()
	return c.Error()
}
