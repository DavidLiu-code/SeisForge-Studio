package prestack

// This file contains the metadata-only QC layer.  It deliberately does not
// open a Reader or retain amplitude samples; callers can build reports after
// the normal PrestackIndex scan has completed.

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// QCReportVersion is the report schema version used by CSV/JSON exports.
const QCReportVersion = "1"

const qcReportVersion = QCReportVersion

// cloneDistribution returns a defensive copy so UI code cannot mutate the
// index's immutable histogram while a report is being exported.
func cloneDistribution(d Distribution) Distribution {
	d.Bins = append([]DistributionBin(nil), d.Bins...)
	return d
}

func cloneQualityStats(q QualityStats) QualityStats {
	q.FoldDistribution = cloneDistribution(q.FoldDistribution)
	q.OffsetDistribution = cloneDistribution(q.OffsetDistribution)
	q.AzimuthDistribution = cloneDistribution(q.AzimuthDistribution)
	return q
}

// QualityStats returns file-wide metadata counters and adaptive distributions.
// A copy is returned on every call.  Synthetic callers that construct an
// index directly are supported as well; the counters are computed lazily when
// BuildIndex has not populated the cached value.
func (p *PrestackIndex) QualityStats() QualityStats {
	if p == nil {
		return QualityStats{}
	}
	if p.quality.TotalTraces != 0 || len(p.Records) == 0 {
		return cloneQualityStats(p.quality)
	}
	return computeQualityStats(p)
}

// BuildQCReport creates a stable, metadata-only report for the complete index
// and the currently selected gather.  The current gather is also highlighted
// in each distribution bucket; no trace sample data is consulted.
func BuildQCReport(index *PrestackIndex, current GatherResult) QCReport {
	if index == nil {
		return QCReport{Version: qcReportVersion}
	}
	q := index.QualityStats()
	isAll := current.Selection.Key.All
	if !isAll {
		// All is a file-wide view, not one enormous Fold bucket.
		q.FoldDistribution = addCurrentCounts(q.FoldDistribution, []float64{float64(len(current.TraceIndices))})
		q.OffsetDistribution = addCurrentCounts(q.OffsetDistribution, selectedOffsetValues(index, current.TraceIndices))
		q.AzimuthDistribution = addCurrentCounts(q.AzimuthDistribution, selectedAzimuthValues(index, current.TraceIndices))
	}
	summary := GatherQCSummary{
		Type:               current.Selection.Type.String(),
		Key:                current.Selection.Key.String(),
		All:                isAll,
		PhysicalTraceCount: len(current.TraceIndices),
		OffsetRange:        current.OffsetRange,
		AzimuthRange:       current.AzimuthRange,
	}
	if !isAll {
		summary.Fold = len(current.TraceIndices)
	}
	return QCReport{Version: qcReportVersion, SourcePath: index.SourcePath, Quality: q, CurrentGather: summary}
}

// MarshalJSONReport serializes a QC report without introducing a second
// schema layer.  It is provided as a named helper for UI code and tests.
func MarshalJSONReport(report QCReport) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}

// WriteJSON writes a QC report as UTF-8 JSON.
func (r QCReport) WriteJSON(path string) error {
	b, err := MarshalJSONReport(r)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}

// MarshalCSVReport returns a stable two-column CSV report.  Histograms are
// emitted after scalar statistics using section/field/value rows so Excel and
// spreadsheet readers can consume the file without custom JSON handling.
func MarshalCSVReport(report QCReport) ([]byte, error) {
	// Use encoding/csv to escape paths and translated keys correctly.
	var buf csvBuffer
	w := csv.NewWriter(&buf)
	write := func(row ...string) { _ = w.Write(row) }
	write("section", "field", "value")
	write("report", "version", report.Version)
	write("report", "source_path", report.SourcePath)
	write("file", "total_traces", strconv.Itoa(report.Quality.TotalTraces))
	write("file", "indexed_traces", strconv.Itoa(report.Quality.IndexedTraces))
	write("file", "valid_header_traces", strconv.Itoa(report.Quality.ValidHeaderTraces))
	write("file", "invalid_header_traces", strconv.Itoa(report.Quality.InvalidHeaderTraces))
	write("file", "missing_cdp", strconv.Itoa(report.Quality.MissingCDP))
	write("file", "missing_ffid", strconv.Itoa(report.Quality.MissingFFID))
	write("file", "missing_source", strconv.Itoa(report.Quality.MissingSource))
	write("file", "missing_receiver", strconv.Itoa(report.Quality.MissingReceiver))
	write("file", "missing_offset", strconv.Itoa(report.Quality.MissingOffset))
	write("file", "coordinate_scalar_errors", strconv.Itoa(report.Quality.CoordinateScalarErrors))
	write("file", "sample_count_inconsistent", strconv.Itoa(report.Quality.SampleCountInconsistent))
	write("file", "sample_interval_inconsistent", strconv.Itoa(report.Quality.SampleIntervalInconsistent))
	write("gathers", "cmp_count", strconv.Itoa(report.Quality.CMPCount))
	write("gathers", "shot_count", strconv.Itoa(report.Quality.ShotCount))
	write("gathers", "receiver_count", strconv.Itoa(report.Quality.ReceiverCount))
	write("gathers", "common_offset_bin_count", strconv.Itoa(report.Quality.CommonOffsetBinCount))
	write("fold", "bins", strconv.Itoa(report.Quality.Fold.Bins))
	write("fold", "traces", strconv.Itoa(report.Quality.Fold.Traces))
	write("fold", "min", strconv.Itoa(report.Quality.Fold.Min))
	write("fold", "max", strconv.Itoa(report.Quality.Fold.Max))
	write("fold", "mean", formatFloat(report.Quality.Fold.Mean))
	writeRange := func(section, name string, r ValueRange) {
		if !r.Valid {
			write(section, name+"_valid", "false")
			return
		}
		write(section, name+"_min", formatFloat(r.Min))
		write(section, name+"_max", formatFloat(r.Max))
	}
	writeRange("file", "offset", report.Quality.OffsetRange)
	writeRange("file", "azimuth", report.Quality.AzimuthRange)
	write("current_gather", "type", report.CurrentGather.Type)
	write("current_gather", "key", report.CurrentGather.Key)
	write("current_gather", "all", strconv.FormatBool(report.CurrentGather.All))
	write("current_gather", "physical_trace_count", strconv.Itoa(report.CurrentGather.PhysicalTraceCount))
	write("current_gather", "fold", strconv.Itoa(report.CurrentGather.Fold))
	writeRange("current_gather", "offset", report.CurrentGather.OffsetRange)
	writeRange("current_gather", "azimuth", report.CurrentGather.AzimuthRange)
	writeDistribution := func(name string, d Distribution) {
		for i, b := range d.Bins {
			write(name, fmt.Sprintf("bin_%d_min", i), formatFloat(b.Min))
			write(name, fmt.Sprintf("bin_%d_max", i), formatFloat(b.Max))
			write(name, fmt.Sprintf("bin_%d_count", i), strconv.Itoa(b.Count))
			if b.CurrentCount != 0 {
				write(name, fmt.Sprintf("bin_%d_current_count", i), strconv.Itoa(b.CurrentCount))
			}
		}
	}
	writeDistribution("fold_distribution", report.Quality.FoldDistribution)
	writeDistribution("offset_distribution", report.Quality.OffsetDistribution)
	writeDistribution("azimuth_distribution", report.Quality.AzimuthDistribution)
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// csvBuffer is a tiny bytes.Buffer-compatible writer kept local to avoid
// exposing implementation details in the public API.
type csvBuffer struct{ b []byte }

func (b *csvBuffer) Write(p []byte) (int, error) {
	b.b = append(b.b, p...)
	return len(p), nil
}
func (b *csvBuffer) Bytes() []byte { return b.b }

func formatFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// WriteCSV writes a QC report as CSV.
func (r QCReport) WriteCSV(path string) error {
	b, err := MarshalCSVReport(r)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

// ExportQCReport chooses CSV or JSON from the filename extension.  Unknown or
// missing extensions default to JSON so the result remains self-describing.
func (r QCReport) ExportQCReport(path string) error {
	if path == "" {
		return errors.New("empty QC report path")
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return r.WriteCSV(path)
	default:
		return r.WriteJSON(path)
	}
}

func computeQualityStats(p *PrestackIndex) QualityStats {
	q := QualityStats{TotalTraces: len(p.Records), IndexedTraces: len(p.Records), CMPCount: len(p.tables[GatherCMP].keys), ShotCount: len(p.tables[GatherShot].keys), ReceiverCount: len(p.tables[GatherReceiver].keys), Fold: p.fold}
	q.CommonOffsetBinCount = len(p.AvailableGathersConfigured(GatherOffset, DefaultOffsetBinSize))
	q.OffsetRange, q.AzimuthRange = p.OffsetRange, p.AzimuthRange
	baseSamples, baseInterval := p.BinarySampleCount, p.BinarySampleIntervalUS
	if baseSamples <= 0 || baseInterval <= 0 {
		for _, r := range p.Records {
			if baseSamples <= 0 && r.SampleCount > 0 {
				baseSamples = r.SampleCount
			}
			if baseInterval <= 0 && r.SampleIntervalUS > 0 {
				baseInterval = r.SampleIntervalUS
			}
			if baseSamples > 0 && baseInterval > 0 {
				break
			}
		}
	}
	offsets := make([]float64, 0, len(p.Records))
	azimuths := make([]float64, 0, len(p.Records))
	folds := make([]float64, 0, len(p.Bins))
	for _, b := range p.Bins {
		folds = append(folds, float64(b.Fold))
	}
	for _, r := range p.Records {
		if !r.HeaderValid || r.HeaderError != "" {
			q.InvalidHeaderTraces++
		} else {
			q.ValidHeaderTraces++
		}
		if !r.HasCDP {
			q.MissingCDP++
		}
		if r.SourceID == 0 {
			q.MissingFFID++
		}
		if r.SourceID == 0 || !r.HasSource {
			q.MissingSource++
		}
		if r.ReceiverID == 0 || !r.HasReceiver {
			q.MissingReceiver++
		}
		if !r.HasOffset || math.IsNaN(r.Offset) || math.IsInf(r.Offset, 0) {
			q.MissingOffset++
		} else {
			offsets = append(offsets, r.Offset)
		}
		if r.HasAzimuth && !math.IsNaN(r.Azimuth) && !math.IsInf(r.Azimuth, 0) {
			azimuths = append(azimuths, r.Azimuth)
		}
		if r.CoordinateScalarError {
			q.CoordinateScalarErrors++
		}
		if baseSamples > 0 && r.HeaderSampleCount != baseSamples {
			q.SampleCountInconsistent++
		}
		if baseInterval > 0 && r.HeaderSampleIntervalUS != baseInterval {
			q.SampleIntervalInconsistent++
		}
	}
	q.FoldDistribution = makeDistribution(folds)
	q.OffsetDistribution = makeDistribution(offsets)
	q.AzimuthDistribution = makeDistribution(azimuths)
	return q
}

func makeDistribution(values []float64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}
	minV, maxV := values[0], values[0]
	for _, v := range values[1:] {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	n := 32
	if maxV <= minV {
		n = 1
	}
	d := Distribution{Min: minV, Max: maxV, Bins: make([]DistributionBin, n)}
	width := (maxV - minV) / float64(n)
	if width <= 0 || math.IsNaN(width) || math.IsInf(width, 0) {
		d.Bins[0] = DistributionBin{Min: minV, Max: maxV, Count: len(values)}
		return d
	}
	for i := range d.Bins {
		d.Bins[i].Min = minV + float64(i)*width
		d.Bins[i].Max = minV + float64(i+1)*width
	}
	for _, v := range values {
		i := int(math.Floor((v - minV) / width))
		if i < 0 {
			i = 0
		}
		if i >= n {
			i = n - 1
		}
		d.Bins[i].Count++
	}
	return d
}

func foldValues(p *PrestackIndex) []float64 {
	out := make([]float64, 0, len(p.Bins))
	for _, b := range p.Bins {
		out = append(out, float64(b.Fold))
	}
	return out
}
func offsetValues(p *PrestackIndex) []float64 {
	out := make([]float64, 0, len(p.Records))
	for _, r := range p.Records {
		if r.HasOffset && !math.IsNaN(r.Offset) && !math.IsInf(r.Offset, 0) {
			out = append(out, r.Offset)
		}
	}
	return out
}
func azimuthValues(p *PrestackIndex) []float64 {
	out := make([]float64, 0, len(p.Records))
	for _, r := range p.Records {
		if r.HasAzimuth && !math.IsNaN(r.Azimuth) && !math.IsInf(r.Azimuth, 0) {
			out = append(out, r.Azimuth)
		}
	}
	return out
}

func addCurrentCounts(d Distribution, selected []float64) Distribution {
	if len(selected) == 0 || len(d.Bins) == 0 {
		return d
	}
	if d.Max <= d.Min {
		d.Bins[0].CurrentCount = len(selected)
		return d
	}
	width := (d.Max - d.Min) / float64(len(d.Bins))
	if width <= 0 {
		return d
	}
	for _, v := range selected {
		i := int(math.Floor((v - d.Min) / width))
		if i < 0 {
			i = 0
		}
		if i >= len(d.Bins) {
			i = len(d.Bins) - 1
		}
		d.Bins[i].CurrentCount++
	}
	return d
}

func selectedOffsetValues(p *PrestackIndex, traces []int64) []float64 {
	if p == nil {
		return nil
	}
	out := make([]float64, 0, len(traces))
	for _, tr := range traces {
		if tr >= 0 && tr < int64(len(p.Records)) {
			r := p.Records[tr]
			if r.HasOffset && !math.IsNaN(r.Offset) && !math.IsInf(r.Offset, 0) {
				out = append(out, r.Offset)
			}
		}
	}
	return out
}

func selectedAzimuthValues(p *PrestackIndex, traces []int64) []float64 {
	if p == nil {
		return nil
	}
	out := make([]float64, 0, len(traces))
	for _, tr := range traces {
		if tr >= 0 && tr < int64(len(p.Records)) {
			r := p.Records[tr]
			if r.HasAzimuth && !math.IsNaN(r.Azimuth) && !math.IsInf(r.Azimuth, 0) {
				out = append(out, r.Azimuth)
			}
		}
	}
	return out
}
