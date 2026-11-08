package prestackbatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// HistorySchemaVersion identifies the metadata-only execution history. It is
// deliberately separate from both the batch-plan and execution-report
// schemas so older plans and reports remain readable.
const HistorySchemaVersion = "seisforge.prestack.batch.history.v1"

const maxHistoryEntries = 200

// HistoryEntry is a compact record of one batch run. It contains paths and
// counters only; it never contains a reader, amplitude, sample or bitmap.
type HistoryEntry struct {
	RunID               string          `json:"run_id"`
	PlanPath            string          `json:"plan_path"`
	ExecutionReportPath string          `json:"execution_report_path"`
	OutputDirectory     string          `json:"output_directory,omitempty"`
	ApplicationVersion  string          `json:"application_version"`
	Status              ExecutionStatus `json:"status"`
	StartedUTC          string          `json:"started_utc"`
	FinishedUTC         string          `json:"finished_utc,omitempty"`
	FigureCount         int             `json:"figure_count"`
	QCCount             int             `json:"qc_count"`
	SucceededCount      int             `json:"succeeded_count"`
	FailedCount         int             `json:"failed_count"`
	CanceledCount       int             `json:"canceled_count"`
	SkippedCount        int             `json:"skipped_count"`
	IncompleteCount     int             `json:"incomplete_count"`
}

// BatchHistory is the on-disk metadata-only history document.
type BatchHistory struct {
	SchemaVersion string         `json:"schema_version"`
	Entries       []HistoryEntry `json:"entries"`
}

// DefaultHistoryPath returns the per-user history location. It never places
// history beside a SEG-Y file or inside the repository.
func DefaultHistoryPath() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(root) == "" {
		root = os.TempDir()
	}
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("%w: user cache directory unavailable", ErrInvalidPlan)
	}
	return filepath.Join(root, "Limage", "prestack_batch", "history.json"), nil
}

// NewHistoryEntry converts a validated execution report to a metadata-only
// history row. outputDirectory may be empty; in that case the plan directory
// is used as a conservative display fallback.
func NewHistoryEntry(report ExecutionReport, outputDirectory string) (HistoryEntry, error) {
	return NewHistoryEntryAt(report, ExecutionReportPath(report.PlanPath), outputDirectory)
}

// NewHistoryEntryAt is like NewHistoryEntry but preserves a caller-supplied
// execution report path. The batch CLI accepts an explicit --batch-report
// path, so history must point at the report that was actually written rather
// than deriving a sidecar beside the plan.
func NewHistoryEntryAt(report ExecutionReport, reportPath, outputDirectory string) (HistoryEntry, error) {
	if err := ValidateExecutionReport(report); err != nil {
		return HistoryEntry{}, err
	}
	if strings.TrimSpace(reportPath) == "" {
		return HistoryEntry{}, fmt.Errorf("%w: empty execution report path", ErrInvalidPlan)
	}
	entry := HistoryEntry{
		RunID:               strings.TrimSpace(report.RunID),
		PlanPath:            filepath.Clean(report.PlanPath),
		ExecutionReportPath: filepath.Clean(reportPath),
		ApplicationVersion:  strings.TrimSpace(report.PlanApplicationVersion),
		Status:              report.Status,
		StartedUTC:          report.StartedUTC,
		FinishedUTC:         report.FinishedUTC,
		OutputDirectory:     strings.TrimSpace(outputDirectory),
	}
	if entry.OutputDirectory == "" {
		entry.OutputDirectory = filepath.Dir(entry.PlanPath)
	} else {
		entry.OutputDirectory = filepath.Clean(entry.OutputDirectory)
	}
	for _, task := range report.Tasks {
		if task.Kind == JobFigure {
			entry.FigureCount++
		} else if task.Kind == JobQC {
			entry.QCCount++
		}
		switch task.Status {
		case ExecutionSucceeded:
			entry.SucceededCount++
		case ExecutionFailed:
			entry.FailedCount++
		case ExecutionCanceled:
			entry.CanceledCount++
		case ExecutionSkipped, ExecutionPending, ExecutionRunning:
			entry.IncompleteCount++
		}
	}
	return entry, nil
}

// NewBatchHistory returns an empty, valid history document.
func NewBatchHistory() BatchHistory {
	return BatchHistory{SchemaVersion: HistorySchemaVersion, Entries: []HistoryEntry{}}
}

// Clone returns a defensive copy suitable for UI filtering.
func (h BatchHistory) Clone() BatchHistory {
	h.Entries = append([]HistoryEntry(nil), h.Entries...)
	return h
}

// ValidateHistory checks schema identity, row metadata and deterministic
// ordering invariants. Entries must be newest first and RunID values unique.
func ValidateHistory(h BatchHistory) error {
	if h.SchemaVersion != HistorySchemaVersion {
		return fmt.Errorf("%w: invalid history schema", ErrInvalidPlan)
	}
	if len(h.Entries) > maxHistoryEntries {
		return fmt.Errorf("%w: history contains %d entries (maximum %d)", ErrInvalidPlan, len(h.Entries), maxHistoryEntries)
	}
	seen := make(map[string]struct{}, len(h.Entries))
	var previous string
	for i, entry := range h.Entries {
		if strings.TrimSpace(entry.RunID) == "" || strings.TrimSpace(entry.PlanPath) == "" || strings.TrimSpace(entry.ExecutionReportPath) == "" || strings.TrimSpace(entry.ApplicationVersion) == "" || entry.StartedUTC == "" || !validExecutionStatus(entry.Status) {
			return fmt.Errorf("%w: invalid history entry %d", ErrInvalidPlan, i)
		}
		if _, ok := seen[entry.RunID]; ok {
			return fmt.Errorf("%w: duplicate history run id %q", ErrInvalidPlan, entry.RunID)
		}
		seen[entry.RunID] = struct{}{}
		if _, err := time.Parse(time.RFC3339Nano, entry.StartedUTC); err != nil {
			return fmt.Errorf("%w: invalid history start time for %q", ErrInvalidPlan, entry.RunID)
		}
		if entry.FinishedUTC != "" {
			if _, err := time.Parse(time.RFC3339Nano, entry.FinishedUTC); err != nil {
				return fmt.Errorf("%w: invalid history finish time for %q", ErrInvalidPlan, entry.RunID)
			}
		}
		if entry.FigureCount < 0 || entry.QCCount < 0 || entry.SucceededCount < 0 || entry.FailedCount < 0 || entry.CanceledCount < 0 || entry.SkippedCount < 0 || entry.IncompleteCount < 0 {
			return fmt.Errorf("%w: negative history counter for %q", ErrInvalidPlan, entry.RunID)
		}
		if previous != "" && entry.StartedUTC > previous {
			return fmt.Errorf("%w: history entries are not newest first", ErrInvalidPlan)
		}
		previous = entry.StartedUTC
	}
	return nil
}

// LoadHistory reads and validates a history document. Missing history is
// treated as an empty document; malformed files are returned untouched so a
// caller can display the error without losing the old data.
func LoadHistory(path string) (BatchHistory, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return BatchHistory{}, fmt.Errorf("%w: empty history path", ErrInvalidPlan)
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewBatchHistory(), nil
	}
	if err != nil {
		return BatchHistory{}, err
	}
	defer f.Close()
	var history BatchHistory
	dec := json.NewDecoder(io.LimitReader(f, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&history); err != nil {
		return BatchHistory{}, fmt.Errorf("%w: decode batch history: %v", ErrInvalidPlan, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return BatchHistory{}, fmt.Errorf("%w: trailing batch history JSON", ErrInvalidPlan)
	}
	if err := ValidateHistory(history); err != nil {
		return BatchHistory{}, err
	}
	history.Entries = append([]HistoryEntry(nil), history.Entries...)
	return history, nil
}

// SaveHistory atomically replaces path. It never writes a partial document;
// callers that need explicit backup behavior should copy an existing file
// before invoking this function.
func SaveHistory(path string, history BatchHistory) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return fmt.Errorf("%w: empty history path", ErrInvalidPlan)
	}
	if err := ValidateHistory(history); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".prestack-history-*.tmp")
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
	return publishPlan(tmpName, path, true)
}

// AppendHistory adds or replaces a row by RunID, keeps newest-started-first
// ordering, and trims the document to the newest 200 rows.
func AppendHistory(path string, entry HistoryEntry) error {
	history, err := LoadHistory(path)
	if err != nil {
		return err
	}
	if err := ValidateHistory(BatchHistory{SchemaVersion: HistorySchemaVersion, Entries: []HistoryEntry{entry}}); err != nil {
		return err
	}
	updated := false
	for i := range history.Entries {
		if history.Entries[i].RunID == entry.RunID {
			history.Entries[i] = entry
			updated = true
			break
		}
	}
	if !updated {
		history.Entries = append(history.Entries, entry)
	}
	sort.SliceStable(history.Entries, func(i, j int) bool {
		if history.Entries[i].StartedUTC != history.Entries[j].StartedUTC {
			return history.Entries[i].StartedUTC > history.Entries[j].StartedUTC
		}
		return history.Entries[i].RunID > history.Entries[j].RunID
	})
	if len(history.Entries) > maxHistoryEntries {
		history.Entries = history.Entries[:maxHistoryEntries]
	}
	return SaveHistory(path, history)
}
