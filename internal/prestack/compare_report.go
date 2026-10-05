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
	Pairs         []CompareMatchReportPair `json:"pairs,omitempty"`
	Rows          []CompareMatchReportRow  `json:"rows,omitempty"`
}

// CompareMatchReportPair contains only physical trace numbers and the
// explainable metadata key used for a matched pair.
type CompareMatchReportPair struct {
	ATrace   int64            `json:"a_trace"`
	BTrace   int64            `json:"b_trace"`
	Strategy MatchKeyStrategy `json:"strategy"`
	Key      TraceMatchKey    `json:"key"`
}

// CompareMatchReportRow is a metadata-only classification row. A row may
// describe a matched pair or one side of an unmatched/ambiguous/invalid
// classification. No samples or amplitudes are ever included.
type CompareMatchReportRow struct {
	Classification string           `json:"classification"`
	ATrace         int64            `json:"a_trace,omitempty"`
	BTrace         int64            `json:"b_trace,omitempty"`
	Strategy       MatchKeyStrategy `json:"strategy"`
	Key            *TraceMatchKey   `json:"key,omitempty"`
}

// CompareMatchReportOptions supplies provenance fields that are not part of
// the match algorithm itself.
type CompareMatchReportOptions struct {
	APath        string
	BPath        string
	Selection    string
	SampleWindow SampleWindow
	IncludePairs bool
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
		SampleWindow: options.SampleWindow,
		Strategy:     result.Strategy,
		Matched:      len(result.Pairs),
		AOnly:        len(result.AOnly), BOnly: len(result.BOnly),
		AmbiguousA: len(result.AmbiguousA), AmbiguousB: len(result.AmbiguousB),
		InvalidA: len(result.InvalidA), InvalidB: len(result.InvalidB),
		Axis: axis,
	}
	if options.IncludePairs {
		r.Pairs = make([]CompareMatchReportPair, len(result.Pairs))
	}
	rows := make([]CompareMatchReportRow, 0, len(result.Pairs)+len(result.AOnly)+len(result.BOnly)+len(result.AmbiguousA)+len(result.AmbiguousB)+len(result.InvalidA)+len(result.InvalidB))
	for i, p := range result.Pairs {
		if options.IncludePairs {
			r.Pairs[i] = CompareMatchReportPair{ATrace: p.ATrace, BTrace: p.BTrace, Strategy: p.Strategy, Key: p.Key}
		}
		key := p.Key
		rows = append(rows, CompareMatchReportRow{Classification: "matched", ATrace: p.ATrace, BTrace: p.BTrace, Strategy: p.Strategy, Key: &key})
	}
	appendTraceRows := func(classification string, traces []int64) {
		for _, trace := range traces {
			rows = append(rows, CompareMatchReportRow{Classification: classification, ATrace: trace, Strategy: MatchKeyInvalid})
		}
	}
	appendTraceRows("a_only", result.AOnly)
	appendTraceRows("b_only", result.BOnly)
	appendTraceRows("ambiguous_a", result.AmbiguousA)
	appendTraceRows("ambiguous_b", result.AmbiguousB)
	appendTraceRows("invalid_a", result.InvalidA)
	appendTraceRows("invalid_b", result.InvalidB)
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
	if err := c.Write([]string{"schema_version", "a_path", "b_path", "selection", "sample_window_start", "sample_window_end", "strategy", "matched", "a_only", "b_only", "ambiguous_a", "ambiguous_b", "invalid_a", "invalid_b", "sample_axis_compatible", "axis_reason", "classification", "a_trace", "b_trace", "pair_strategy", "key_strategy", "source_id", "receiver_id", "cdp", "offset"}); err != nil {
		return err
	}
	writeRow := func(row *CompareMatchReportRow) error {
		classification, aTrace, bTrace, pairStrategy, keyStrategy := "summary", "", "", "", ""
		sourceID, receiverID, cdp, offset := "", "", "", ""
		if row != nil {
			classification = row.Classification
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
			}
		}
		return c.Write([]string{r.SchemaVersion, r.APath, r.BPath, r.Selection, strconv.Itoa(r.SampleWindow.Start), strconv.Itoa(r.SampleWindow.End), r.Strategy.String(), strconv.Itoa(r.Matched), strconv.Itoa(r.AOnly), strconv.Itoa(r.BOnly), strconv.Itoa(r.AmbiguousA), strconv.Itoa(r.AmbiguousB), strconv.Itoa(r.InvalidA), strconv.Itoa(r.InvalidB), strconv.FormatBool(r.Axis.Compatible), r.Axis.Reason, classification, aTrace, bTrace, pairStrategy, keyStrategy, sourceID, receiverID, cdp, offset})
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
