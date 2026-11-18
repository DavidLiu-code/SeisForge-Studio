package prestackbatch

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExecutionReportSchemaVersion identifies the metadata-only sidecar written
// for one batch execution. It deliberately does not alter the batch plan v1
// schema, so existing plans remain executable by older builds.
const ExecutionReportSchemaVersion = "seisforge.prestack.batch.execution.v1"

type ExecutionStatus string

const (
	ExecutionPending   ExecutionStatus = "pending"
	ExecutionRunning   ExecutionStatus = "running"
	ExecutionSucceeded ExecutionStatus = "succeeded"
	ExecutionFailed    ExecutionStatus = "failed"
	ExecutionCanceled  ExecutionStatus = "canceled"
	ExecutionSkipped   ExecutionStatus = "skipped"
)

// ExecutionTaskReport contains status and output metadata only. It must never
// contain amplitudes, samples, readers, or rendered buffers.
type ExecutionTaskReport struct {
	ID          string          `json:"id"`
	Kind        JobKind         `json:"kind"`
	Status      ExecutionStatus `json:"status"`
	Output      string          `json:"output"`
	Result      string          `json:"result,omitempty"`
	StartedUTC  string          `json:"started_utc,omitempty"`
	FinishedUTC string          `json:"finished_utc,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// ExecutionReport is a reproducibility-friendly record of one run. Source
// identities remain in the plan; this sidecar records only plan identity,
// task outcomes and output paths.
type ExecutionReport struct {
	SchemaVersion          string                `json:"schema_version"`
	PlanPath               string                `json:"plan_path"`
	PlanApplicationVersion string                `json:"plan_application_version"`
	PlanSHA256             string                `json:"plan_sha256,omitempty"`
	RunID                  string                `json:"run_id"`
	StartedUTC             string                `json:"started_utc"`
	FinishedUTC            string                `json:"finished_utc,omitempty"`
	Status                 ExecutionStatus       `json:"status"`
	Overwrite              bool                  `json:"overwrite"`
	Error                  string                `json:"error,omitempty"`
	Tasks                  []ExecutionTaskReport `json:"tasks"`
}

// NewExecutionReport creates a deterministic task list in the same Figure →
// QC order used by the executor.
func NewExecutionReport(planPath string, plan Plan, runID string, overwrite bool) ExecutionReport {
	r := ExecutionReport{SchemaVersion: ExecutionReportSchemaVersion,
		PlanPath: filepath.Clean(planPath), PlanApplicationVersion: plan.ApplicationVersion,
		RunID: strings.TrimSpace(runID), StartedUTC: time.Now().UTC().Format(time.RFC3339Nano),
		Status: ExecutionRunning, Overwrite: overwrite,
		Tasks: make([]ExecutionTaskReport, 0, len(plan.Figures)+len(plan.QCReports))}
	for _, job := range plan.Figures {
		r.Tasks = append(r.Tasks, ExecutionTaskReport{ID: job.ID, Kind: JobFigure, Status: ExecutionPending, Output: job.Output, Result: job.Result})
	}
	for _, job := range plan.QCReports {
		r.Tasks = append(r.Tasks, ExecutionTaskReport{ID: job.ID, Kind: JobQC, Status: ExecutionPending, Output: job.Output})
	}
	return r
}

// ExecutionReportPath returns the default sidecar beside a plan.
func ExecutionReportPath(planPath string) string {
	clean := filepath.Clean(planPath)
	ext := filepath.Ext(clean)
	if ext == "" {
		return clean + ".execution.json"
	}
	return strings.TrimSuffix(clean, ext) + ".execution.json"
}

func (r *ExecutionReport) SetTask(index int, status ExecutionStatus, errText string) error {
	if r == nil || index < 0 || index >= len(r.Tasks) {
		return fmt.Errorf("%w: invalid execution task index", ErrInvalidPlan)
	}
	if !validExecutionStatus(status) {
		return fmt.Errorf("%w: invalid execution status %q", ErrInvalidPlan, status)
	}
	t := &r.Tasks[index]
	t.Status = status
	t.Error = strings.TrimSpace(errText)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if status == ExecutionRunning && t.StartedUTC == "" {
		t.StartedUTC = now
	}
	if status == ExecutionSucceeded || status == ExecutionFailed || status == ExecutionCanceled || status == ExecutionSkipped {
		if t.StartedUTC == "" {
			t.StartedUTC = now
		}
		t.FinishedUTC = now
	}
	return nil
}

func (r *ExecutionReport) Finish(status ExecutionStatus, errText string) error {
	if r == nil || !validExecutionStatus(status) || (status != ExecutionSucceeded && status != ExecutionFailed && status != ExecutionCanceled) {
		return fmt.Errorf("%w: invalid execution final status", ErrInvalidPlan)
	}
	r.Status, r.Error, r.FinishedUTC = status, strings.TrimSpace(errText), time.Now().UTC().Format(time.RFC3339Nano)
	return nil
}

func SaveExecutionReport(path string, report ExecutionReport, overwrite bool) error {
	if err := ValidateExecutionReport(report); err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: empty execution report path", ErrInvalidPlan)
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".prestack-execution-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(append(b, '\n')); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return publishPlan(tmpName, path, overwrite)
}

func LoadExecutionReport(path string) (ExecutionReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return ExecutionReport{}, err
	}
	defer f.Close()
	var report ExecutionReport
	dec := json.NewDecoder(io.LimitReader(f, 32<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&report); err != nil {
		return ExecutionReport{}, fmt.Errorf("%w: decode execution report: %v", ErrInvalidPlan, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ExecutionReport{}, fmt.Errorf("%w: trailing execution report JSON", ErrInvalidPlan)
	}
	if err := ValidateExecutionReport(report); err != nil {
		return ExecutionReport{}, err
	}
	report.Tasks = append([]ExecutionTaskReport(nil), report.Tasks...)
	return report, nil
}

func ValidateExecutionReport(r ExecutionReport) error {
	if r.SchemaVersion != ExecutionReportSchemaVersion || strings.TrimSpace(r.PlanPath) == "" || strings.TrimSpace(r.PlanApplicationVersion) == "" || strings.TrimSpace(r.RunID) == "" || r.StartedUTC == "" || !validExecutionStatus(r.Status) {
		return fmt.Errorf("%w: invalid execution report identity", ErrInvalidPlan)
	}
	seen := map[string]bool{}
	for _, task := range r.Tasks {
		if strings.TrimSpace(task.ID) == "" || (task.Kind != JobFigure && task.Kind != JobQC) || !validExecutionStatus(task.Status) || strings.TrimSpace(task.Output) == "" {
			return fmt.Errorf("%w: invalid execution task", ErrInvalidPlan)
		}
		if seen[task.ID] {
			return fmt.Errorf("%w: duplicate execution task %q", ErrInvalidPlan, task.ID)
		}
		seen[task.ID] = true
		if task.Kind == JobFigure && strings.TrimSpace(task.Result) == "" {
			return fmt.Errorf("%w: figure task %q missing result", ErrInvalidPlan, task.ID)
		}
	}
	return nil
}

func validExecutionStatus(status ExecutionStatus) bool {
	switch status {
	case ExecutionPending, ExecutionRunning, ExecutionSucceeded, ExecutionFailed, ExecutionCanceled, ExecutionSkipped:
		return true
	default:
		return false
	}
}
