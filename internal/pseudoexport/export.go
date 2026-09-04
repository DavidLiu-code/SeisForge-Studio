// Package pseudoexport plans and executes cropped SEG-Y exports from a
// pseudo-3-D crooked-line view. It intentionally owns no UI or long-lived
// reader state: callers freeze the visible canonical geometry into a plan,
// then execute that immutable snapshot in a background worker.
package pseudoexport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const manifestVersion = 1

// LineInput is one checked, visible pseudo-3-D line at the instant the user
// requests an export. Geometry must already be the canonical calibrated
// acquisition-order SEG-Y geometry used by the pseudo view.
type LineInput struct {
	ID                  string
	Name                string
	SourcePath          string
	GeometryFingerprint string
	Geometry            *geometry.CrookedLineGeometry
	Calibration         project.CoordinateCalibration
	SampleIntervalUS    int
	SamplesPerTrace     int
	BytesPerSample      int
	DataStart           int64
	FormatCode          int
	Endian              segy.Endian
	TraceCount          int64
	FileSize            int64
	ExtendedTextHeaders int
	SampleStart         int
	SampleEnd           int
	SampleWindowSet     bool
}

// PlanInput is the complete visible-view snapshot. Lines contains only lines
// currently checked for display; camera zoom, rotation and occlusion are not
// part of export eligibility.
type PlanInput struct {
	ProjectName    string
	NavigationPath string
	SpatialRange   pseudo3d.XYRange
	TimeRange      pseudo3d.TimeRange
	Lines          []LineInput
}

// PseudoLineExportPlan is an independent, immutable-by-convention per-line
// export snapshot. TraceIndices follow original SEG-Y acquisition order.
type PseudoLineExportPlan struct {
	ID                  string                        `json:"id"`
	Name                string                        `json:"name"`
	SourcePath          string                        `json:"source_path"`
	OutputName          string                        `json:"output_name"`
	GeometryFingerprint string                        `json:"geometry_fingerprint"`
	Calibration         project.CoordinateCalibration `json:"calibration"`
	TraceIndices        []int64                       `json:"trace_indices"`
	SampleStart         int                           `json:"sample_start"`
	SampleEnd           int                           `json:"sample_end"`
	SampleIntervalUS    int                           `json:"sample_interval_us"`
	SamplesPerTrace     int                           `json:"source_samples_per_trace"`
	BytesPerSample      int                           `json:"bytes_per_sample"`
	DataStart           int64                         `json:"data_start"`
	FormatCode          int                           `json:"format_code"`
	Endian              segy.Endian                   `json:"endian"`
	SourceTraceCount    int64                         `json:"source_trace_count"`
	SourceFileSize      int64                         `json:"source_file_size"`
	ExtendedTextHeaders int                           `json:"extended_text_headers"`
	ActualTimeRange     pseudo3d.TimeRange            `json:"actual_time_range"`
	ActualSpatialRange  pseudo3d.XYRange              `json:"actual_spatial_range"`
	ClippedSpatialRange pseudo3d.XYRange              `json:"clipped_spatial_range"`
	EstimatedBytes      int64                         `json:"estimated_bytes"`
}

// PseudoViewExportPlan freezes the checked lines and physical selection.
type PseudoViewExportPlan struct {
	ProjectName    string                 `json:"project_name"`
	NavigationPath string                 `json:"navigation_path,omitempty"`
	SpatialRange   pseudo3d.XYRange       `json:"spatial_range"`
	TimeRange      pseudo3d.TimeRange     `json:"requested_time_range"`
	Lines          []PseudoLineExportPlan `json:"lines"`
	TotalTraces    int                    `json:"total_traces"`
	EstimatedBytes int64                  `json:"estimated_bytes"`
}

// PseudoExportProgress describes overall batch progress. LineIndex is
// one-based. Percent measures work processed, so a failed line is advanced at
// the failure boundary and later lines never make progress go backwards.
type PseudoExportProgress struct {
	Stage           string
	LineIndex       int
	LineCount       int
	LineName        string
	TraceDone       int
	TraceTotal      int
	ProcessedTraces int
	TotalTraces     int
	Percent         float64
}

// PseudoLineExportResult records a complete, failed, or cancelled line.
type PseudoLineExportResult struct {
	ID                  string                        `json:"id"`
	Name                string                        `json:"name"`
	SourcePath          string                        `json:"source_path"`
	OutputPath          string                        `json:"output_path,omitempty"`
	Status              string                        `json:"status"`
	Error               string                        `json:"error,omitempty"`
	GeometryFingerprint string                        `json:"geometry_fingerprint"`
	Calibration         project.CoordinateCalibration `json:"calibration"`
	TraceIndices        []int64                       `json:"trace_indices"`
	TraceCount          int                           `json:"trace_count"`
	SampleStart         int                           `json:"sample_start"`
	SampleEnd           int                           `json:"sample_end"`
	SampleCount         int                           `json:"sample_count"`
	SampleIntervalUS    int                           `json:"sample_interval_us"`
	BytesPerSample      int                           `json:"bytes_per_sample"`
	SourceDataStart     int64                         `json:"source_data_start"`
	SourceFormatCode    int                           `json:"source_format_code"`
	SourceEndian        segy.Endian                   `json:"source_endian"`
	SourceTraceCount    int64                         `json:"source_trace_count"`
	SourceFileSize      int64                         `json:"source_file_size"`
	EstimatedBytes      int64                         `json:"estimated_bytes"`
	ActualTimeRange     pseudo3d.TimeRange            `json:"actual_time_range"`
	RequestedTimeRange  pseudo3d.TimeRange            `json:"requested_time_range"`
	ActualSpatialRange  pseudo3d.XYRange              `json:"actual_spatial_range"`
	ClippedSpatialRange pseudo3d.XYRange              `json:"clipped_spatial_range"`
	RequestedSpatial    pseudo3d.XYRange              `json:"requested_spatial_range"`
	OutputBytes         int64                         `json:"output_bytes,omitempty"`
	OutputTraceCount    int64                         `json:"output_trace_count,omitempty"`
	OutputDataStart     int64                         `json:"output_data_start,omitempty"`
	OutputFormatCode    int                           `json:"output_format_code,omitempty"`
}

// CopiedFile records the byte-identical project navigation copy.
type CopiedFile struct {
	SourcePath string `json:"source_path,omitempty"`
	OutputPath string `json:"output_path,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

// PseudoExportResult is both returned to callers and embedded in the JSON
// manifest. A non-empty manifest path is available for completed, partial,
// failed, and cancelled batches.
type PseudoExportResult struct {
	Version         int                      `json:"version"`
	ProjectName     string                   `json:"project_name"`
	CreatedAt       time.Time                `json:"created_at"`
	FinishedAt      time.Time                `json:"finished_at"`
	OutputDirectory string                   `json:"output_directory"`
	ManifestPath    string                   `json:"manifest_path"`
	Status          string                   `json:"status"`
	Cancelled       bool                     `json:"cancelled"`
	SpatialRange    pseudo3d.XYRange         `json:"spatial_range"`
	RequestedTime   pseudo3d.TimeRange       `json:"requested_time_range"`
	EstimatedBytes  int64                    `json:"estimated_bytes"`
	TotalTraces     int                      `json:"total_traces"`
	CompletedLines  int                      `json:"completed_lines"`
	FailedLines     int                      `json:"failed_lines"`
	NavigationCopy  CopiedFile               `json:"navigation_copy"`
	Lines           []PseudoLineExportResult `json:"lines"`
	Errors          []string                 `json:"errors,omitempty"`
}

// BuildPlan validates and freezes a visible pseudo-view selection. It keeps
// the real support traces returned by ClipTrajectory, then walks the canonical
// trajectory once to restore acquisition order and remove overlaps between
// multiple range re-entries.
func BuildPlan(input PlanInput) (PseudoViewExportPlan, error) {
	spatial := input.SpatialRange.Normalized()
	timeRange := input.TimeRange.Normalized()
	if !spatial.Valid && !timeRange.Valid {
		return PseudoViewExportPlan{}, errors.New("pseudo export requires a spatial or time range")
	}
	if len(input.Lines) == 0 {
		return PseudoViewExportPlan{}, errors.New("no visible pseudo lines selected")
	}
	plan := PseudoViewExportPlan{
		ProjectName: strings.TrimSpace(input.ProjectName), NavigationPath: strings.TrimSpace(input.NavigationPath),
		SpatialRange: spatial, TimeRange: timeRange,
	}
	if plan.NavigationPath != "" {
		absolute, err := filepath.Abs(plan.NavigationPath)
		if err != nil {
			return PseudoViewExportPlan{}, fmt.Errorf("resolve navigation path: %w", err)
		}
		plan.NavigationPath = filepath.Clean(absolute)
	}
	if plan.ProjectName == "" {
		plan.ProjectName = "SeisForge Studio"
	}
	seenSources := make(map[string]bool, len(input.Lines))
	for _, source := range input.Lines {
		line, ok, err := buildLinePlan(source, spatial, timeRange)
		if err != nil {
			return PseudoViewExportPlan{}, fmt.Errorf("plan line %q: %w", source.Name, err)
		}
		if !ok {
			continue
		}
		key, err := canonicalPathKey(line.SourcePath)
		if err != nil {
			return PseudoViewExportPlan{}, fmt.Errorf("plan line %q: %w", source.Name, err)
		}
		if seenSources[key] {
			return PseudoViewExportPlan{}, fmt.Errorf("source SEG-Y is selected more than once: %s", line.SourcePath)
		}
		seenSources[key] = true
		plan.TotalTraces += len(line.TraceIndices)
		plan.EstimatedBytes += line.EstimatedBytes
		plan.Lines = append(plan.Lines, line)
	}
	if len(plan.Lines) == 0 {
		return PseudoViewExportPlan{}, errors.New("selected spatial/time range has no line intersection")
	}
	reserved := []string{"pseudo_crop_manifest.json"}
	if plan.NavigationPath != "" {
		reserved = append(reserved, filepath.Base(plan.NavigationPath))
	}
	assignOutputNames(plan.Lines, reserved)
	return plan, nil
}

func buildLinePlan(input LineInput, spatial pseudo3d.XYRange, timeRange pseudo3d.TimeRange) (PseudoLineExportPlan, bool, error) {
	if input.Geometry == nil {
		return PseudoLineExportPlan{}, false, errors.New("canonical geometry is missing")
	}
	g := input.Geometry
	if len(g.TraceIndices) < 2 || len(g.TraceIndices) != len(g.X) || len(g.X) != len(g.Y) || len(g.X) != len(g.Distance) {
		return PseudoLineExportPlan{}, false, errors.New("canonical geometry arrays are empty or unmatched")
	}
	if strings.TrimSpace(input.SourcePath) == "" {
		return PseudoLineExportPlan{}, false, errors.New("source path is empty")
	}
	sourcePath, err := filepath.Abs(input.SourcePath)
	if err != nil {
		return PseudoLineExportPlan{}, false, fmt.Errorf("resolve source path: %w", err)
	}
	if input.SampleIntervalUS <= 0 || input.SamplesPerTrace < 1 || input.BytesPerSample <= 0 || input.DataStart < 3600 ||
		input.FormatCode <= 0 || input.TraceCount < 1 || input.FileSize < input.DataStart {
		return PseudoLineExportPlan{}, false, errors.New("invalid SEG-Y sample metadata")
	}
	var traces []int64
	clippedSpatial := pseudo3d.XYRange{}
	if spatial.Valid {
		runs, err := pseudo3d.ClipTrajectory(g.TraceIndices, g.X, g.Y, g.Distance, spatial)
		if err != nil {
			return PseudoLineExportPlan{}, false, err
		}
		if len(runs) == 0 {
			return PseudoLineExportPlan{}, false, nil
		}
		clippedSpatial = boundsOfRuns(runs)
		selected := make(map[int64]struct{})
		for _, run := range runs {
			for _, trace := range run.TraceIndices {
				selected[trace] = struct{}{}
			}
		}
		traces = make([]int64, 0, len(selected))
		for _, trace := range g.TraceIndices {
			if _, ok := selected[trace]; ok {
				traces = append(traces, trace)
				delete(selected, trace)
			}
		}
		if len(selected) != 0 {
			return PseudoLineExportPlan{}, false, errors.New("clipped support trace is absent from canonical geometry")
		}
	} else {
		traces = append([]int64(nil), g.TraceIndices...)
		clippedSpatial = boundsOfCoordinates(g.X, g.Y)
	}
	if len(traces) == 0 {
		return PseudoLineExportPlan{}, false, nil
	}
	actualSpatial := boundsOfSelectedGeometry(g, traces)
	for i, trace := range traces {
		if trace < 0 {
			return PseudoLineExportPlan{}, false, errors.New("negative source trace index")
		}
		if i > 0 && traces[i-1] >= trace {
			return PseudoLineExportPlan{}, false, errors.New("canonical trace indices are not strictly increasing")
		}
	}
	sampleStart, sampleEnd := 0, input.SamplesPerTrace-1
	actual := pseudo3d.TimeRange{StartMS: 0, EndMS: float64(sampleEnd*input.SampleIntervalUS) / 1000, Valid: true}
	if timeRange.Valid {
		if input.SampleWindowSet {
			sampleStart, sampleEnd = input.SampleStart, input.SampleEnd
			if sampleStart < 0 || sampleEnd <= sampleStart || sampleEnd >= input.SamplesPerTrace {
				return PseudoLineExportPlan{}, false, errors.New("frozen sample window is out of range")
			}
			actual = pseudo3d.TimeRange{StartMS: float64(sampleStart*input.SampleIntervalUS) / 1000,
				EndMS: float64(sampleEnd*input.SampleIntervalUS) / 1000, Valid: true}
		} else {
			var ok bool
			sampleStart, sampleEnd, actual, ok = timeRange.SampleWindow(input.SampleIntervalUS, input.SamplesPerTrace)
			if !ok {
				return PseudoLineExportPlan{}, false, nil
			}
		}
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(input.SourcePath), filepath.Ext(input.SourcePath))
	}
	ns := sampleEnd - sampleStart + 1
	estimated := input.DataStart + int64(maxInt(0, len(traces))*(240+ns*input.BytesPerSample))
	return PseudoLineExportPlan{
		ID: input.ID, Name: name, SourcePath: filepath.Clean(sourcePath), GeometryFingerprint: input.GeometryFingerprint,
		Calibration: input.Calibration, TraceIndices: traces, SampleStart: sampleStart, SampleEnd: sampleEnd,
		SampleIntervalUS: input.SampleIntervalUS, SamplesPerTrace: input.SamplesPerTrace,
		BytesPerSample: input.BytesPerSample, DataStart: input.DataStart,
		FormatCode: input.FormatCode, Endian: input.Endian, SourceTraceCount: input.TraceCount, SourceFileSize: input.FileSize,
		ExtendedTextHeaders: input.ExtendedTextHeaders, ActualTimeRange: actual, ActualSpatialRange: actualSpatial,
		ClippedSpatialRange: clippedSpatial, EstimatedBytes: estimated,
	}, true, nil
}

func assignOutputNames(lines []PseudoLineExportPlan, reserved []string) {
	counts := make(map[string]int, len(lines))
	for i := range lines {
		counts[strings.ToLower(filepath.Base(lines[i].SourcePath))]++
	}
	used := make(map[string]bool, len(lines)+len(reserved))
	for _, name := range reserved {
		used[strings.ToLower(name)] = true
	}
	for i := range lines {
		base := filepath.Base(lines[i].SourcePath)
		if base == "." || base == "" {
			base = "line.sgy"
		}
		if counts[strings.ToLower(base)] > 1 {
			base = nameWithStableSuffix(base, lines[i].SourcePath)
		}
		if used[strings.ToLower(base)] {
			base = nameWithStableSuffix(base, lines[i].SourcePath)
		}
		candidate := base
		for suffix := 2; used[strings.ToLower(candidate)]; suffix++ {
			ext := filepath.Ext(base)
			candidate = fmt.Sprintf("%s_%d%s", strings.TrimSuffix(base, ext), suffix, ext)
		}
		used[strings.ToLower(candidate)] = true
		lines[i].OutputName = candidate
	}
}

func boundsOfRuns(runs []pseudo3d.TraceRun) pseudo3d.XYRange {
	var x, y []float64
	for _, run := range runs {
		for _, point := range run.Points {
			x = append(x, point.X)
			y = append(y, point.Y)
		}
	}
	return boundsOfCoordinates(x, y)
}

func boundsOfCoordinates(x, y []float64) pseudo3d.XYRange {
	if len(x) == 0 || len(x) != len(y) {
		return pseudo3d.XYRange{}
	}
	bounds := pseudo3d.XYRange{XMin: x[0], XMax: x[0], YMin: y[0], YMax: y[0], Valid: true}
	for i := 1; i < len(x); i++ {
		if x[i] < bounds.XMin {
			bounds.XMin = x[i]
		}
		if x[i] > bounds.XMax {
			bounds.XMax = x[i]
		}
		if y[i] < bounds.YMin {
			bounds.YMin = y[i]
		}
		if y[i] > bounds.YMax {
			bounds.YMax = y[i]
		}
	}
	return bounds
}

func boundsOfSelectedGeometry(g *geometry.CrookedLineGeometry, traces []int64) pseudo3d.XYRange {
	if g == nil || len(traces) == 0 {
		return pseudo3d.XYRange{}
	}
	x := make([]float64, 0, len(traces))
	y := make([]float64, 0, len(traces))
	position := 0
	for _, trace := range traces {
		for position < len(g.TraceIndices) && g.TraceIndices[position] < trace {
			position++
		}
		if position >= len(g.TraceIndices) || g.TraceIndices[position] != trace {
			return pseudo3d.XYRange{}
		}
		x = append(x, g.X[position])
		y = append(y, g.Y[position])
	}
	return boundsOfCoordinates(x, y)
}

func nameWithStableSuffix(base, identity string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(identity))))
	ext := filepath.Ext(base)
	return fmt.Sprintf("%s_%x%s", strings.TrimSuffix(base, ext), sum[:4], ext)
}

// Export executes a frozen plan into a unique project timestamp directory.
// Per-line failures are returned in result.Lines and do not stop later lines.
// Cancellation stops before the next trace/line, preserves completed outputs,
// and returns both the result (with a written manifest) and context.Canceled.
func Export(ctx context.Context, plan PseudoViewExportPlan, parentDirectory string, progress func(PseudoExportProgress)) (PseudoExportResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validatePlanForExport(plan); err != nil {
		return PseudoExportResult{}, err
	}
	parent, err := filepath.Abs(strings.TrimSpace(parentDirectory))
	if err != nil || parent == "" {
		return PseudoExportResult{}, errors.New("invalid export parent directory")
	}
	if err = os.MkdirAll(parent, 0o755); err != nil {
		return PseudoExportResult{}, fmt.Errorf("create export parent: %w", err)
	}
	started := time.Now()
	directory, err := createUniqueOutputDirectory(parent, plan.ProjectName, started)
	if err != nil {
		return PseudoExportResult{}, err
	}
	result := PseudoExportResult{
		Version: manifestVersion, ProjectName: plan.ProjectName, CreatedAt: started, OutputDirectory: directory,
		ManifestPath: filepath.Join(directory, "pseudo_crop_manifest.json"), Status: "running",
		SpatialRange: plan.SpatialRange, RequestedTime: plan.TimeRange, EstimatedBytes: plan.EstimatedBytes,
		TotalTraces: plan.TotalTraces, Lines: make([]PseudoLineExportResult, 0, len(plan.Lines)),
	}
	if plan.NavigationPath != "" {
		result.NavigationCopy = copyNavigationFile(plan.NavigationPath, directory)
		if result.NavigationCopy.Status == "failed" {
			result.Errors = append(result.Errors, result.NavigationCopy.Error)
		}
	} else {
		result.NavigationCopy.Status = "not_present"
	}
	lastReportedTraces := 0
	report := func(update PseudoExportProgress) {
		if update.ProcessedTraces < lastReportedTraces {
			update.ProcessedTraces = lastReportedTraces
			update.Percent = percent(lastReportedTraces, plan.TotalTraces)
		} else {
			lastReportedTraces = update.ProcessedTraces
		}
		emitProgress(progress, update)
	}
	report(PseudoExportProgress{Stage: "starting", LineCount: len(plan.Lines), TotalTraces: plan.TotalTraces})
	processed := 0
	var cancelErr error
	for index, line := range plan.Lines {
		lineResult := lineResultFromPlan(line, plan.SpatialRange, plan.TimeRange)
		lineResult.OutputPath = filepath.Join(directory, line.OutputName)
		if err := ctx.Err(); err != nil {
			lineResult.Status, lineResult.Error = "cancelled", err.Error()
			result.Lines = append(result.Lines, lineResult)
			cancelErr = err
			break
		}
		reader, openErr := segy.Open(line.SourcePath)
		if openErr != nil {
			lineResult.Status, lineResult.Error = "failed", openErr.Error()
			result.FailedLines++
			result.Lines = append(result.Lines, lineResult)
			processed += len(line.TraceIndices)
			report(lineProgress("failed", index, len(plan.Lines), line, len(line.TraceIndices), processed, plan.TotalTraces))
			continue
		}
		sourceInfo := reader.Info
		metadataErr := validateSourceMetadata(sourceInfo, line)
		if metadataErr != nil {
			_ = reader.Close()
			lineResult.Status, lineResult.Error = "failed", metadataErr.Error()
			result.FailedLines++
			result.Lines = append(result.Lines, lineResult)
			processed += len(line.TraceIndices)
			report(lineProgress("failed", index, len(plan.Lines), line, len(line.TraceIndices), processed, plan.TotalTraces))
			continue
		}
		before := processed
		exportErr := reader.ExportSection(lineResult.OutputPath, segy.SectionExportOptions{
			Context: ctx, TraceIndices: append([]int64(nil), line.TraceIndices...), SampleStart: line.SampleStart, SampleEnd: line.SampleEnd,
			Progress: func(done, total int) {
				report(lineProgress("exporting", index, len(plan.Lines), line, done, before+done, plan.TotalTraces))
			},
		})
		if exportErr == nil {
			var outputInfo segy.Info
			outputInfo, exportErr = verifyExportedSection(lineResult.OutputPath, sourceInfo, line)
			if exportErr == nil {
				lineResult.OutputTraceCount = outputInfo.TraceCount
				lineResult.OutputDataStart = outputInfo.DataStart
				lineResult.OutputFormatCode = outputInfo.FormatCode
			} else {
				_ = os.Remove(lineResult.OutputPath)
			}
		}
		// The source reader is read-only. Once the atomically committed output has
		// passed the independent reopen/metadata/size check, a source Close error
		// cannot invalidate that complete output.
		_ = reader.Close()
		if exportErr != nil {
			lineResult.Error = exportErr.Error()
			if errors.Is(exportErr, context.Canceled) || errors.Is(exportErr, context.DeadlineExceeded) || ctx.Err() != nil {
				lineResult.Status = "cancelled"
				cancelErr = ctx.Err()
				if cancelErr == nil {
					cancelErr = exportErr
				}
				result.Lines = append(result.Lines, lineResult)
				break
			}
			lineResult.Status = "failed"
			result.FailedLines++
			processed += len(line.TraceIndices)
			result.Lines = append(result.Lines, lineResult)
			report(lineProgress("failed", index, len(plan.Lines), line, len(line.TraceIndices), processed, plan.TotalTraces))
			continue
		}
		lineResult.Status = "completed"
		if stat, statErr := os.Stat(lineResult.OutputPath); statErr == nil {
			lineResult.OutputBytes = stat.Size()
		}
		result.CompletedLines++
		processed += len(line.TraceIndices)
		result.Lines = append(result.Lines, lineResult)
		report(lineProgress("line_complete", index, len(plan.Lines), line, len(line.TraceIndices), processed, plan.TotalTraces))
	}
	if cancelErr != nil {
		for len(result.Lines) < len(plan.Lines) {
			line := plan.Lines[len(result.Lines)]
			lineResult := lineResultFromPlan(line, plan.SpatialRange, plan.TimeRange)
			lineResult.OutputPath = filepath.Join(directory, line.OutputName)
			lineResult.Status = "not_started"
			lineResult.Error = "batch cancelled before this line started"
			result.Lines = append(result.Lines, lineResult)
		}
		result.Cancelled = true
		result.Status = "cancelled"
	} else if result.FailedLines == 0 && result.NavigationCopy.Status != "failed" {
		result.Status = "completed"
	} else if result.CompletedLines > 0 {
		result.Status = "partial"
	} else {
		result.Status = "failed"
	}
	result.FinishedAt = time.Now()
	manifestErr := writeManifest(result.ManifestPath, result)
	if manifestErr != nil {
		return result, fmt.Errorf("write pseudo export manifest: %w", manifestErr)
	}
	if cancelErr != nil {
		report(PseudoExportProgress{Stage: "cancelled", LineCount: len(plan.Lines), ProcessedTraces: lastReportedTraces, TotalTraces: plan.TotalTraces, Percent: percent(lastReportedTraces, plan.TotalTraces)})
		return result, cancelErr
	}
	report(PseudoExportProgress{Stage: result.Status, LineCount: len(plan.Lines), ProcessedTraces: plan.TotalTraces, TotalTraces: plan.TotalTraces, Percent: 100})
	return result, nil
}

// validatePlanForExport is deliberately repeated at the public execution
// boundary. BuildPlan produces these invariants, but callers can persist,
// edit, or construct an exported plan directly. In particular an unsafe or
// duplicate OutputName must never escape the unique result directory or
// overwrite the copied DAT/manifest.
func validatePlanForExport(plan PseudoViewExportPlan) error {
	if len(plan.Lines) == 0 || plan.TotalTraces <= 0 {
		return errors.New("pseudo export plan is empty")
	}
	if !plan.SpatialRange.Normalized().Valid && !plan.TimeRange.Normalized().Valid {
		return errors.New("pseudo export plan has no spatial or time range")
	}
	reserved := map[string]bool{"pseudo_crop_manifest.json": true}
	if plan.NavigationPath != "" {
		reserved[strings.ToLower(filepath.Base(plan.NavigationPath))] = true
	}
	seenNames := make(map[string]bool, len(plan.Lines))
	seenSources := make(map[string]bool, len(plan.Lines))
	totalTraces := 0
	var estimatedBytes int64
	for index, line := range plan.Lines {
		name := line.OutputName
		if !safeOutputBaseName(name) {
			return fmt.Errorf("pseudo export line %d has an unsafe output name %q", index+1, name)
		}
		nameKey := strings.ToLower(name)
		if reserved[nameKey] {
			return fmt.Errorf("pseudo export line %d output name %q conflicts with a reserved artifact", index+1, name)
		}
		if seenNames[nameKey] {
			return fmt.Errorf("pseudo export output name is duplicated: %q", name)
		}
		seenNames[nameKey] = true
		sourceKey, err := canonicalPathKey(line.SourcePath)
		if err != nil {
			return fmt.Errorf("pseudo export line %d source: %w", index+1, err)
		}
		if seenSources[sourceKey] {
			return fmt.Errorf("pseudo export source is duplicated: %q", line.SourcePath)
		}
		seenSources[sourceKey] = true
		if len(line.TraceIndices) == 0 || line.SampleStart < 0 || line.SampleEnd < line.SampleStart ||
			line.SampleEnd >= line.SamplesPerTrace || line.BytesPerSample <= 0 || line.DataStart < 3600 ||
			line.SampleIntervalUS <= 0 || line.SourceTraceCount < 1 || line.SourceFileSize < line.DataStart {
			return fmt.Errorf("pseudo export line %d has invalid frozen trace/sample metadata", index+1)
		}
		for position, trace := range line.TraceIndices {
			if trace < 0 || trace >= line.SourceTraceCount || (position > 0 && line.TraceIndices[position-1] >= trace) {
				return fmt.Errorf("pseudo export line %d has invalid acquisition-order trace indices", index+1)
			}
		}
		totalTraces += len(line.TraceIndices)
		estimatedBytes += line.EstimatedBytes
	}
	if totalTraces != plan.TotalTraces {
		return fmt.Errorf("pseudo export trace total mismatch: got %d, want %d", plan.TotalTraces, totalTraces)
	}
	if estimatedBytes != plan.EstimatedBytes {
		return fmt.Errorf("pseudo export byte estimate mismatch: got %d, want %d", plan.EstimatedBytes, estimatedBytes)
	}
	return nil
}

func safeOutputBaseName(name string) bool {
	if name == "" || strings.TrimSpace(name) != name || name == "." || name == ".." || filepath.IsAbs(name) || filepath.Base(name) != name ||
		strings.ContainsAny(name, `<>:"/\|?*`) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	for _, character := range name {
		if character < 32 {
			return false
		}
	}
	stem := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" {
		return false
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
		return false
	}
	return true
}

func lineResultFromPlan(line PseudoLineExportPlan, requestedSpatial pseudo3d.XYRange, requestedTime pseudo3d.TimeRange) PseudoLineExportResult {
	return PseudoLineExportResult{
		ID: line.ID, Name: line.Name, SourcePath: line.SourcePath, GeometryFingerprint: line.GeometryFingerprint,
		Calibration: line.Calibration, TraceIndices: append([]int64(nil), line.TraceIndices...), TraceCount: len(line.TraceIndices),
		SampleStart: line.SampleStart, SampleEnd: line.SampleEnd, SampleCount: line.SampleEnd - line.SampleStart + 1,
		SampleIntervalUS: line.SampleIntervalUS, BytesPerSample: line.BytesPerSample, SourceDataStart: line.DataStart,
		SourceFormatCode: line.FormatCode, SourceEndian: line.Endian, SourceTraceCount: line.SourceTraceCount,
		SourceFileSize: line.SourceFileSize, EstimatedBytes: line.EstimatedBytes,
		ActualTimeRange: line.ActualTimeRange, RequestedTimeRange: requestedTime,
		ActualSpatialRange: line.ActualSpatialRange, ClippedSpatialRange: line.ClippedSpatialRange, RequestedSpatial: requestedSpatial,
	}
}

func verifyExportedSection(path string, source segy.Info, line PseudoLineExportPlan) (segy.Info, error) {
	output, err := segy.Open(path)
	if err != nil {
		return segy.Info{}, fmt.Errorf("reopen exported SEG-Y: %w", err)
	}
	info := output.Info
	closeErr := output.Close()
	if closeErr != nil {
		return segy.Info{}, fmt.Errorf("close exported SEG-Y verification reader: %w", closeErr)
	}
	ns := line.SampleEnd - line.SampleStart + 1
	if info.TraceCount != int64(len(line.TraceIndices)) || info.SamplesPerTrace != ns ||
		info.SampleIntervalUS != source.SampleIntervalUS || info.FormatCode != source.FormatCode ||
		info.Endian != source.Endian || info.DataStart != source.DataStart || info.ExtendedTextHeaders != source.ExtendedTextHeaders {
		return segy.Info{}, fmt.Errorf("exported SEG-Y metadata verification failed (trace/ns/dt/format/dataStart %d/%d/%d/%d/%d)",
			info.TraceCount, info.SamplesPerTrace, info.SampleIntervalUS, info.FormatCode, info.DataStart)
	}
	expectedSize := info.DataStart + int64(len(line.TraceIndices))*int64(240+ns*source.BytesPerSample)
	if info.FileSize != expectedSize {
		return segy.Info{}, fmt.Errorf("exported SEG-Y size verification failed: got %d, want %d", info.FileSize, expectedSize)
	}
	return info, nil
}

func validateSourceMetadata(info segy.Info, line PseudoLineExportPlan) error {
	if info.SampleIntervalUS != line.SampleIntervalUS || info.SamplesPerTrace != line.SamplesPerTrace ||
		info.BytesPerSample != line.BytesPerSample || info.DataStart != line.DataStart || info.FormatCode != line.FormatCode ||
		info.Endian != line.Endian || info.TraceCount != line.SourceTraceCount || info.FileSize != line.SourceFileSize ||
		info.ExtendedTextHeaders != line.ExtendedTextHeaders {
		return fmt.Errorf("source SEG-Y metadata changed (dt/ns/bps/dataStart/format/traces/size %d/%d/%d/%d/%d/%d/%d, planned %d/%d/%d/%d/%d/%d/%d)",
			info.SampleIntervalUS, info.SamplesPerTrace, info.BytesPerSample, info.DataStart, info.FormatCode, info.TraceCount, info.FileSize,
			line.SampleIntervalUS, line.SamplesPerTrace, line.BytesPerSample, line.DataStart, line.FormatCode, line.SourceTraceCount, line.SourceFileSize)
	}
	for _, trace := range line.TraceIndices {
		if trace < 0 || trace >= info.TraceCount {
			return fmt.Errorf("planned trace %d is outside source trace count %d", trace, info.TraceCount)
		}
	}
	return nil
}

func lineProgress(stage string, zeroIndex, lineCount int, line PseudoLineExportPlan, done, processed, total int) PseudoExportProgress {
	return PseudoExportProgress{
		Stage: stage, LineIndex: zeroIndex + 1, LineCount: lineCount, LineName: line.Name,
		TraceDone: done, TraceTotal: len(line.TraceIndices), ProcessedTraces: processed, TotalTraces: total, Percent: percent(processed, total),
	}
}

func percent(done, total int) float64 {
	if total <= 0 {
		return 0
	}
	value := float64(done) * 100 / float64(total)
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func emitProgress(callback func(PseudoExportProgress), update PseudoExportProgress) {
	if callback != nil {
		callback(update)
	}
}

func createUniqueOutputDirectory(parent, projectName string, now time.Time) (string, error) {
	name := sanitizeProjectName(projectName)
	base := fmt.Sprintf("%s_pseudo_crop_%s", name, now.Format("20060102_150405"))
	for suffix := 1; suffix < 10000; suffix++ {
		candidate := filepath.Join(parent, base)
		if suffix > 1 {
			candidate = filepath.Join(parent, fmt.Sprintf("%s_%02d", base, suffix))
		}
		err := os.Mkdir(candidate, 0o755)
		if err == nil {
			return candidate, nil
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("create export directory: %w", err)
		}
	}
	return "", errors.New("could not create a unique export directory")
}

func sanitizeProjectName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "SeisForgeStudio"
	}
	invalid := `<>:"/\\|?*`
	value = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(invalid, r) {
			return '_'
		}
		return r
	}, value)
	value = strings.Trim(value, ". ")
	if value == "" {
		return "SeisForgeStudio"
	}
	return value
}

func copyNavigationFile(source, directory string) CopiedFile {
	result := CopiedFile{SourcePath: filepath.Clean(source), Status: "failed"}
	in, err := os.Open(source)
	if err != nil {
		result.Error = fmt.Sprintf("copy navigation DAT: %v", err)
		return result
	}
	defer in.Close()
	name := filepath.Base(source)
	if name == "" || name == "." {
		name = "navigation.dat"
	}
	if strings.EqualFold(name, "pseudo_crop_manifest.json") {
		name = nameWithStableSuffix(name, source)
	}
	result.OutputPath = filepath.Join(directory, name)
	out, err := os.OpenFile(result.OutputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		result.Error = fmt.Sprintf("copy navigation DAT: %v", err)
		return result
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hash), in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(result.OutputPath)
		result.Error = fmt.Sprintf("copy navigation DAT: %v", errors.Join(copyErr, syncErr, closeErr))
		return result
	}
	result.Status, result.Bytes, result.SHA256 = "completed", written, hex.EncodeToString(hash.Sum(nil))
	return result
}

func writeManifest(path string, result PseudoExportResult) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	ok := false
	defer func() {
		_ = temporary.Close()
		if !ok {
			_ = os.Remove(temporaryPath)
		}
	}()
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(result); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	if _, err = os.Stat(path); err == nil {
		return errors.New("manifest destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func canonicalPathKey(path string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || abs == "" {
		return "", errors.New("invalid source path")
	}
	return strings.ToLower(filepath.Clean(abs)), nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// SortedOutputPaths is a convenience for validation and UI summaries.
func (result PseudoExportResult) SortedOutputPaths() []string {
	paths := make([]string, 0, len(result.Lines))
	for _, line := range result.Lines {
		if line.Status == "completed" && line.OutputPath != "" {
			paths = append(paths, line.OutputPath)
		}
	}
	sort.Strings(paths)
	return paths
}
