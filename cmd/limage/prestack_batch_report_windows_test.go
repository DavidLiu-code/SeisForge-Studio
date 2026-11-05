//go:build windows

package main

import (
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestackbatch"
)

func TestFormatPrestackExecutionReportShowsMetadataOnly(t *testing.T) {
	report := prestackbatch.ExecutionReport{
		SchemaVersion:          prestackbatch.ExecutionReportSchemaVersion,
		PlanPath:               `C:\tmp\demo_batch.json`,
		PlanApplicationVersion: "1.10.4",
		RunID:                  "run-1",
		StartedUTC:             "2026-11-04T00:00:00Z",
		Status:                 prestackbatch.ExecutionSucceeded,
		Tasks: []prestackbatch.ExecutionTaskReport{{
			ID: "figure-1", Kind: prestackbatch.JobFigure, Status: prestackbatch.ExecutionSucceeded,
			Output: `C:\tmp\demo.png`, Result: `C:\tmp\demo.result.json`,
		}},
	}
	text := formatPrestackExecutionReport(`C:\tmp\demo.execution.json`, report)
	for _, want := range []string{"执行报告", "成功", "figure-1", "demo.png", "demo.result.json"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report text missing %q: %s", want, text)
		}
	}
	for _, forbidden := range []string{"samples", "amplitudes", "振幅数组", "样点数组"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Fatalf("metadata report unexpectedly contains %q: %s", forbidden, text)
		}
	}
}
