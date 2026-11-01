package prestackbatch

import "strings"

// BatchPlanDraft is the UI-facing, runtime-neutral input for a batch plan.
// It contains only recipe/session paths and output names; it deliberately does
// not retain a Reader, Index, HWND, amplitude buffer, or any derived gather.
// Build validates the referenced recipe/session and produces the versioned
// Plan used by the executor.
type BatchPlanDraft struct {
	ApplicationVersion string
	OutputDirectory    string
	Figures            []FigureInput
	QCReports          []QCInput
}

// NewDraft returns an empty plan draft for the supplied application version
// and output directory.
func NewDraft(applicationVersion, outputDirectory string) BatchPlanDraft {
	return BatchPlanDraft{ApplicationVersion: strings.TrimSpace(applicationVersion), OutputDirectory: outputDirectory}
}

// AddFigure appends one recipe replay task. Empty output/result names are
// allowed and receive deterministic defaults during Build.
func (d *BatchPlanDraft) AddFigure(recipe, output, result string) {
	if d == nil {
		return
	}
	d.Figures = append(d.Figures, FigureInput{Recipe: recipe, Output: output, Result: result})
}

// AddQC appends one metadata-only QC export task. An empty format defaults to
// JSON during Build.
func (d *BatchPlanDraft) AddQC(session, output string, format ReportFormat) {
	if d == nil {
		return
	}
	d.QCReports = append(d.QCReports, QCInput{Session: session, Output: output, Format: format})
}

// Clone returns a defensive copy suitable for a background plan-build task.
func (d BatchPlanDraft) Clone() BatchPlanDraft {
	d.Figures = append([]FigureInput(nil), d.Figures...)
	d.QCReports = append([]QCInput(nil), d.QCReports...)
	return d
}

// Build validates the draft and returns a versioned, deterministic Plan.
func (d BatchPlanDraft) Build() (Plan, error) {
	return BuildPlan(Input{ApplicationVersion: d.ApplicationVersion, OutputDirectory: d.OutputDirectory,
		Figures: append([]FigureInput(nil), d.Figures...), QCReports: append([]QCInput(nil), d.QCReports...)})
}
