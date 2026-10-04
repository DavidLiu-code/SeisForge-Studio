package prestack

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// CompareMatchReport is a versioned, metadata-only summary of an A/B match.
// It intentionally contains no sample arrays or amplitude values, so it is
// safe to export alongside a provenance/QC report.
type CompareMatchReport struct {
	SchemaVersion string                   `json:"schema_version"`
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
}

// CompareMatchReportPair contains only physical trace numbers and the
// explainable metadata key used for a match.
type CompareMatchReportPair struct {
	ATrace   int64            `json:"a_trace"`
	BTrace   int64            `json:"b_trace"`
	Strategy MatchKeyStrategy `json:"strategy"`
	Key      TraceMatchKey    `json:"key"`
}

// BuildCompareMatchReport creates a defensive report snapshot.  Include pairs
// is optional for large comparisons; counts remain complete either way.
func BuildCompareMatchReport(result CompareMatchResult, axis SampleAxisCompatibility, includePairs bool) CompareMatchReport {
	r := CompareMatchReport{
		SchemaVersion: "1.0",
		Strategy:      result.Strategy,
		Matched:       len(result.Pairs),
		AOnly:         len(result.AOnly), BOnly: len(result.BOnly),
		AmbiguousA: len(result.AmbiguousA), AmbiguousB: len(result.AmbiguousB),
		InvalidA: len(result.InvalidA), InvalidB: len(result.InvalidB),
		Axis: axis,
	}
	if includePairs {
		r.Pairs = make([]CompareMatchReportPair, len(result.Pairs))
		for i, p := range result.Pairs {
			r.Pairs[i] = CompareMatchReportPair{ATrace: p.ATrace, BTrace: p.BTrace, Strategy: p.Strategy, Key: p.Key}
		}
	}
	return r
}

func (r CompareMatchReport) MarshalJSON() ([]byte, error) {
	type alias CompareMatchReport
	return json.Marshal(alias(r))
}

func (r CompareMatchReport) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// WriteCompareMatchReportJSON writes the same stable report representation
// used by JSON() to an arbitrary destination.
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

// WriteCompareMatchReportCSV writes fixed columns and one row per matched
// pair.  An empty pair list still emits a summary row, making the file useful
// when every record is unmatched or ambiguous.
func WriteCompareMatchReportCSV(w io.Writer, r CompareMatchReport) error {
	if w == nil {
		return fmt.Errorf("nil report writer")
	}
	c := csv.NewWriter(w)
	if err := c.Write([]string{"schema_version", "strategy", "matched", "a_only", "b_only", "ambiguous_a", "ambiguous_b", "invalid_a", "invalid_b", "sample_axis_compatible", "axis_reason", "a_trace", "b_trace", "pair_strategy", "key_strategy", "source_id", "receiver_id", "cdp", "offset"}); err != nil {
		return err
	}
	writeRow := func(p *CompareMatchReportPair) error {
		aTrace, bTrace, pairStrategy, keyStrategy := "", "", "", ""
		sourceID, receiverID, cdp, offset := "", "", "", ""
		if p != nil {
			aTrace = strconv.FormatInt(p.ATrace, 10)
			bTrace = strconv.FormatInt(p.BTrace, 10)
			pairStrategy = p.Strategy.String()
			keyStrategy = p.Key.Strategy.String()
			sourceID = strconv.FormatInt(int64(p.Key.SourceID), 10)
			receiverID = strconv.FormatInt(int64(p.Key.ReceiverID), 10)
			cdp = strconv.FormatInt(int64(p.Key.CDP), 10)
			offset = strconv.FormatFloat(p.Key.Offset, 'g', -1, 64)
		}
		return c.Write([]string{r.SchemaVersion, r.Strategy.String(), strconv.Itoa(r.Matched), strconv.Itoa(r.AOnly), strconv.Itoa(r.BOnly), strconv.Itoa(r.AmbiguousA), strconv.Itoa(r.AmbiguousB), strconv.Itoa(r.InvalidA), strconv.Itoa(r.InvalidB), strconv.FormatBool(r.Axis.Compatible), r.Axis.Reason, aTrace, bTrace, pairStrategy, keyStrategy, sourceID, receiverID, cdp, offset})
	}
	if len(r.Pairs) == 0 {
		if err := writeRow(nil); err != nil {
			return err
		}
	} else {
		for i := range r.Pairs {
			if err := writeRow(&r.Pairs[i]); err != nil {
				return err
			}
		}
	}
	c.Flush()
	return c.Error()
}
