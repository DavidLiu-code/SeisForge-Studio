package prestackbatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func historyReport(runID, started string, status ExecutionStatus) ExecutionReport {
	return ExecutionReport{
		SchemaVersion:          ExecutionReportSchemaVersion,
		PlanPath:               filepath.Join("out", "plan.json"),
		PlanApplicationVersion: "test",
		RunID:                  runID,
		StartedUTC:             started,
		FinishedUTC:            started,
		Status:                 status,
		Tasks: []ExecutionTaskReport{
			{ID: "figure-001", Kind: JobFigure, Status: ExecutionSucceeded, Output: "figure.png", Result: "figure.result.json"},
			{ID: "qc-001", Kind: JobQC, Status: ExecutionFailed, Output: "qc.json"},
			{ID: "qc-002", Kind: JobQC, Status: ExecutionCanceled, Output: "qc2.json"},
		},
	}
}

func TestHistoryRoundTripAndMetadataOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	report := historyReport("run-1", "2026-11-07T01:02:03Z", ExecutionFailed)
	entry, err := NewHistoryEntry(report, filepath.Join(dir, "outputs"))
	if err != nil {
		t.Fatal(err)
	}
	if entry.FigureCount != 1 || entry.QCCount != 2 || entry.SucceededCount != 1 || entry.FailedCount != 1 || entry.CanceledCount != 1 {
		t.Fatalf("unexpected counters: %+v", entry)
	}
	if err := AppendHistory(path, entry); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != 1 || loaded.Entries[0].RunID != "run-1" {
		t.Fatalf("unexpected history: %+v", loaded)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"amplitude", "samples", "reader", "bitmap"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("history contains forbidden metadata %q: %s", forbidden, raw)
		}
	}
	var decoded BatchHistory
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHistory(decoded); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryDeduplicatesAndSortsNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	first, err := NewHistoryEntry(historyReport("same", "2026-11-07T01:00:00Z", ExecutionFailed), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendHistory(path, first); err != nil {
		t.Fatal(err)
	}
	newer, err := NewHistoryEntry(historyReport("same", "2026-11-07T02:00:00Z", ExecutionSucceeded), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendHistory(path, newer); err != nil {
		t.Fatal(err)
	}
	older, err := NewHistoryEntry(historyReport("older", "2026-11-07T00:00:00Z", ExecutionSucceeded), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendHistory(path, older); err != nil {
		t.Fatal(err)
	}
	history, err := LoadHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Entries) != 2 || history.Entries[0].RunID != "same" || history.Entries[0].Status != ExecutionSucceeded || history.Entries[1].RunID != "older" {
		t.Fatalf("dedupe/order failed: %+v", history.Entries)
	}
}

func TestHistoryTrimsToNewestTwoHundredEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	for i := 0; i < maxHistoryEntries+7; i++ {
		stamp := time.Date(2026, time.November, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		entry, err := NewHistoryEntry(historyReport(fmt.Sprintf("run-%03d", i), stamp, ExecutionSucceeded), "")
		if err != nil {
			t.Fatal(err)
		}
		if err := AppendHistory(path, entry); err != nil {
			t.Fatal(err)
		}
	}
	history, err := LoadHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Entries) != maxHistoryEntries {
		t.Fatalf("history was not trimmed: %d", len(history.Entries))
	}
	if history.Entries[0].StartedUTC <= history.Entries[len(history.Entries)-1].StartedUTC {
		t.Fatal("history is not newest first")
	}
}

func TestCorruptHistoryIsNotOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	original := []byte("{not valid json\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	entry, err := NewHistoryEntry(historyReport("run-1", "2026-11-07T01:02:03Z", ExecutionSucceeded), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendHistory(path, entry); err == nil {
		t.Fatal("expected corrupt history error")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("corrupt history was overwritten: %q", after)
	}
}
