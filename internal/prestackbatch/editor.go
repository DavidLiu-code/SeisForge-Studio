package prestackbatch

import (
	"fmt"
	"path/filepath"
	"strings"
)

// TaskRef identifies one editable task in a plan. Indexes are intentionally
// not persisted. IDs are regenerated deterministically after an edit, so
// callers should refresh Tasks after any mutation before issuing another ref.
type TaskRef struct {
	Kind JobKind
	ID   string
}

// TaskSummary is a runtime-neutral row for a plan editor. It contains paths
// and metadata only; it never owns a reader, index, amplitude buffer, or
// rendered image.
type TaskSummary struct {
	Ref     TaskRef
	Output  string
	Result  string
	Format  ReportFormat
	SourceA SourceIdentity
	SourceB *SourceIdentity
	Recipe  string
	Session string
}

// PlanEditor provides defensive, deterministic edits over an existing Plan.
// It deliberately keeps the v1 JSON schema unchanged: figures and QC reports
// remain separate arrays and only their order within each kind is editable.
type PlanEditor struct {
	plan Plan
}

// NewEditor returns an editor containing a defensive copy of plan.
func NewEditor(plan Plan) (*PlanEditor, error) {
	if err := Validate(plan); err != nil {
		return nil, err
	}
	return &PlanEditor{plan: plan.Clone()}, nil
}

// Plan returns a defensive copy of the edited plan.
func (e *PlanEditor) Plan() Plan {
	if e == nil {
		return Plan{}
	}
	return e.plan.Clone()
}

// Tasks returns figures followed by QC tasks in their current order.
func (e *PlanEditor) Tasks() []TaskSummary {
	if e == nil {
		return nil
	}
	tasks := make([]TaskSummary, 0, len(e.plan.Figures)+len(e.plan.QCReports))
	for _, j := range e.plan.Figures {
		t := TaskSummary{Ref: TaskRef{Kind: JobFigure, ID: j.ID}, Output: j.Output, Result: j.Result,
			SourceA: j.SourceA, Recipe: j.Recipe}
		if j.SourceB != nil {
			b := *j.SourceB
			t.SourceB = &b
		}
		tasks = append(tasks, t)
	}
	for _, j := range e.plan.QCReports {
		tasks = append(tasks, TaskSummary{Ref: TaskRef{Kind: JobQC, ID: j.ID}, Output: j.Output,
			Format: j.Format, SourceA: j.Source, SourceB: cloneSource(j.SourceB), Session: j.Session})
	}
	return tasks
}

// SetOutputDirectory changes the plan output root and keeps every task's
// relative filename. It does not move files on disk.
func (e *PlanEditor) SetOutputDirectory(directory string) error {
	if e == nil {
		return fmt.Errorf("%w: nil plan editor", ErrInvalidPlan)
	}
	root, err := cleanOutputDirectory(directory)
	if err != nil {
		return err
	}
	old, _ := filepath.Abs(filepath.Clean(e.plan.OutputDirectory))
	for i := range e.plan.Figures {
		e.plan.Figures[i].Output = remapTarget(old, root, e.plan.Figures[i].Output)
		e.plan.Figures[i].Result = remapTarget(old, root, e.plan.Figures[i].Result)
	}
	for i := range e.plan.QCReports {
		e.plan.QCReports[i].Output = remapTarget(old, root, e.plan.QCReports[i].Output)
	}
	e.plan.OutputDirectory = root
	return e.validateAndRenumber()
}

// Remove deletes a task identified by kind and ID.
func (e *PlanEditor) Remove(ref TaskRef) error {
	if e == nil {
		return fmt.Errorf("%w: nil plan editor", ErrInvalidPlan)
	}
	switch ref.Kind {
	case JobFigure:
		for i, j := range e.plan.Figures {
			if j.ID == ref.ID {
				e.plan.Figures = append(e.plan.Figures[:i], e.plan.Figures[i+1:]...)
				return e.validateAndRenumber()
			}
		}
	case JobQC:
		for i, j := range e.plan.QCReports {
			if j.ID == ref.ID {
				e.plan.QCReports = append(e.plan.QCReports[:i], e.plan.QCReports[i+1:]...)
				return e.validateAndRenumber()
			}
		}
	default:
		return fmt.Errorf("%w: unsupported task kind %q", ErrInvalidPlan, ref.Kind)
	}
	return fmt.Errorf("%w: task %s not found", ErrInvalidPlan, ref.ID)
}

// Move reorders a task within its kind. The destination is a zero-based index
// in the current list (before removal of the source row).
func (e *PlanEditor) Move(ref TaskRef, destination int) error {
	if e == nil {
		return fmt.Errorf("%w: nil plan editor", ErrInvalidPlan)
	}
	if destination < 0 {
		return fmt.Errorf("%w: invalid task destination", ErrInvalidPlan)
	}
	switch ref.Kind {
	case JobFigure:
		if destination >= len(e.plan.Figures) {
			return fmt.Errorf("%w: invalid task destination", ErrInvalidPlan)
		}
		for i := range e.plan.Figures {
			if e.plan.Figures[i].ID == ref.ID {
				j := e.plan.Figures[i]
				e.plan.Figures = append(e.plan.Figures[:i], e.plan.Figures[i+1:]...)
				e.plan.Figures = append(e.plan.Figures, FigureJob{})
				copy(e.plan.Figures[destination+1:], e.plan.Figures[destination:])
				e.plan.Figures[destination] = j
				return e.validateAndRenumber()
			}
		}
	case JobQC:
		if destination >= len(e.plan.QCReports) {
			return fmt.Errorf("%w: invalid task destination", ErrInvalidPlan)
		}
		for i := range e.plan.QCReports {
			if e.plan.QCReports[i].ID == ref.ID {
				j := e.plan.QCReports[i]
				e.plan.QCReports = append(e.plan.QCReports[:i], e.plan.QCReports[i+1:]...)
				e.plan.QCReports = append(e.plan.QCReports, QCJob{})
				copy(e.plan.QCReports[destination+1:], e.plan.QCReports[destination:])
				e.plan.QCReports[destination] = j
				return e.validateAndRenumber()
			}
		}
	default:
		return fmt.Errorf("%w: unsupported task kind %q", ErrInvalidPlan, ref.Kind)
	}
	return fmt.Errorf("%w: task %s not found", ErrInvalidPlan, ref.ID)
}

// UpdateFigureOutput updates output and result filenames. Names must stay
// relative to the plan output directory.
func (e *PlanEditor) UpdateFigureOutput(ref TaskRef, output, result string) error {
	if e == nil || ref.Kind != JobFigure {
		return fmt.Errorf("%w: invalid figure task", ErrInvalidPlan)
	}
	for i := range e.plan.Figures {
		if e.plan.Figures[i].ID == ref.ID {
			newOutput, err := planTarget(e.plan.OutputDirectory, output, "")
			if err != nil {
				return err
			}
			newResult, err := planTarget(e.plan.OutputDirectory, result, "")
			if err != nil {
				return err
			}
			candidate := e.plan.Clone()
			candidate.Figures[i].Output = newOutput
			candidate.Figures[i].Result = newResult
			if err := Validate(candidate); err != nil {
				return err
			}
			e.plan = candidate
			return e.validateAndRenumber()
		}
	}
	return fmt.Errorf("%w: task %s not found", ErrInvalidPlan, ref.ID)
}

// UpdateQCOutput updates a QC output filename and report format.
func (e *PlanEditor) UpdateQCOutput(ref TaskRef, output string, format ReportFormat) error {
	if e == nil || ref.Kind != JobQC {
		return fmt.Errorf("%w: invalid QC task", ErrInvalidPlan)
	}
	if format != ReportJSON && format != ReportCSV {
		return fmt.Errorf("%w: unsupported QC format %q", ErrInvalidPlan, format)
	}
	for i := range e.plan.QCReports {
		if e.plan.QCReports[i].ID == ref.ID {
			target, err := planTarget(e.plan.OutputDirectory, output, "")
			if err != nil {
				return err
			}
			// Validate the candidate before mutating the editor, so a failed
			// format/path update leaves the current plan untouched.
			candidate := e.plan.Clone()
			candidate.QCReports[i].Output, candidate.QCReports[i].Format = target, format
			if err := Validate(candidate); err != nil {
				return err
			}
			e.plan = candidate
			return e.validateAndRenumber()
		}
	}
	return fmt.Errorf("%w: task %s not found", ErrInvalidPlan, ref.ID)
}

// Save validates and atomically saves the edited plan.
func (e *PlanEditor) Save(path string, overwrite bool) error {
	if e == nil {
		return fmt.Errorf("%w: nil plan editor", ErrInvalidPlan)
	}
	if err := e.validateAndRenumber(); err != nil {
		return err
	}
	return Save(path, e.plan, overwrite)
}

func (e *PlanEditor) validateAndRenumber() error {
	for i := range e.plan.Figures {
		e.plan.Figures[i].ID = fmt.Sprintf("figure-%03d", i+1)
	}
	for i := range e.plan.QCReports {
		e.plan.QCReports[i].ID = fmt.Sprintf("qc-%03d", i+1)
	}
	return Validate(e.plan)
}

func cloneSource(source *SourceIdentity) *SourceIdentity {
	if source == nil {
		return nil
	}
	c := *source
	return &c
}

func cleanOutputDirectory(directory string) (string, error) {
	if strings.TrimSpace(directory) == "" {
		return "", fmt.Errorf("%w: invalid output directory", ErrInvalidPlan)
	}
	root, err := filepath.Abs(filepath.Clean(directory))
	if err != nil || filepath.Dir(root) == root {
		return "", fmt.Errorf("%w: invalid output directory", ErrInvalidPlan)
	}
	return root, nil
}

func remapTarget(oldRoot, newRoot, target string) string {
	if rel, err := filepath.Rel(oldRoot, target); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Join(newRoot, rel)
	}
	return target
}
