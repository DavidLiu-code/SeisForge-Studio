//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/registry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
	"github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi/artifact"
)

func TestPluginParameterRowsPreserveAllTypesAndNestedPaths(t *testing.T) {
	schema := []api.ParameterDescriptor{{ID: "options", Type: "group", Parameters: []api.ParameterDescriptor{
		{ID: "rank", Type: "integer"}, {ID: "scale", Type: "number"}, {ID: "keep", Type: "boolean"},
		{ID: "label", Type: "string"}, {ID: "mode", Type: "enum"}, {ID: "file", Type: "file"}, {ID: "window", Type: "range"},
	}}}
	parent := []string{"root"}
	rows := algorithmParameterRows(schema, parent)
	parent[0] = "mutated"
	if len(rows) != 8 || rows[0].Height <= 0 {
		t.Fatalf("all schema types must have a row: %+v", rows)
	}
	for i, row := range rows[1:] {
		if !reflect.DeepEqual(row.Path, []string{"root", "options", schema[0].Parameters[i].ID}) || row.Height <= 0 {
			t.Fatalf("nested path was flattened or aliased: %+v", row)
		}
	}
	rows[1].Path[0] = "other"
	if rows[2].Path[0] != "root" {
		t.Fatal("parameter paths share mutable storage")
	}
}

func TestPluginNoInstalledModuleCannotFallBackToCopy(t *testing.T) {
	oldCZ, oldRegistry := cz, poststackAlgorithms
	cz, poststackAlgorithms = cadzowControls{}, registry.New(APP_VERSION)
	t.Cleanup(func() { cz, poststackAlgorithms = oldCZ, oldRegistry })
	_, values, err := readAlgorithmParameters()
	if err == nil || values != nil || !strings.Contains(err.Error(), "不会回退") {
		t.Fatalf("uninstalled module must fail explicitly, got values=%v error=%v", values, err)
	}
}

func pluginArtifactResult(t *testing.T) *generatedPoststackResult {
	t.Helper()
	source := filepath.Join(t.TempDir(), "A.sgy")
	if err := testsegy.Write(source, testsegy.Options{Rows: 1, Cols: 3, Samples: 8, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(t.TempDir(), "input.sfdata")
	in, err := poststack.CaptureAlgorithmSection(context.Background(), poststack.SectionRequest{
		SourcePath: source, TraceIndices: []int64{2, 0}, SampleStart: 1, SampleEnd: 4, AlgorithmID: "example.noop",
	}, inputPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := artifact.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	dir := t.TempDir()
	output := filepath.Join(dir, "output.sfdata")
	if err := artifact.Write(context.Background(), output, a.Metadata(), a.TraceMetadata(), func(trace int64) ([]float64, []bool, error) {
		return a.ReadTraceWindow(trace, 0, a.SamplesPerTrace())
	}); err != nil {
		t.Fatal(err)
	}
	section, err := in.AcceptResult(context.Background(), dir, output, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = section.Cleanup() })
	return &generatedPoststackResult{Section: section, Algorithm: "example.noop"}
}

func TestPluginArtifactCompareAndTraceAnalysisUseSamePhysicalMapping(t *testing.T) {
	r := pluginArtifactResult(t)
	generatedPoststackMu.Lock()
	old := generatedPoststackResults
	generatedPoststackResults = []*generatedPoststackResult{r}
	generatedPoststackMu.Unlock()
	t.Cleanup(func() {
		generatedPoststackMu.Lock()
		generatedPoststackResults = old
		generatedPoststackMu.Unlock()
	})
	b, err := openPoststackCompareSource(r.Section.Part2)
	if err != nil {
		t.Fatal(err)
	}
	want, err := b.ReadTraceWindow(1, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	wantHeader, err := b.ReadTraceHeader(1)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	got := readTraceAnalysisTarget(traceAnalysisTarget{Role: "B", Path: r.Section.Part2, Trace: 1, SampleStart: 0, SampleEnd: 3}, false)
	if got.headerError != "" || got.waveError != "" || !reflect.DeepEqual(want, got.samples) || got.traceHeader.Crossline != wantHeader.Crossline {
		t.Fatalf("single-trace analysis opened the artifact as SEG-Y or changed mapping: header=%s waveform=%s", got.headerError, got.waveError)
	}
	if got.info.FormatCode != 6 || !reflect.DeepEqual(generatedPoststackTraceOrder(r.Section.Part2), []int64{2, 0}) {
		t.Fatal("artifact precision or original A trace order was lost")
	}
	r.retired.Store(true)
	if stale, err := openPoststackCompareSource(r.Section.Part2); err == nil {
		stale.Close()
		t.Fatal("retired artifact was reopened")
	}
	if _, err := os.Stat(r.Section.SourcePath); err != nil {
		t.Fatal("A changed while opening/retiring B")
	}
}

func TestPluginArtifactStaleGenerationDisposesWithoutChangingA(t *testing.T) {
	isolatePoststackMailbox(t)
	_, token := poststackProcessingSession.BeginJob()
	r := pluginArtifactResult(t)
	postPoststackEvent(poststackUIEvent{Generation: 19, Token: token, Done: true, Result: r})
	if _, err := os.Stat(r.Section.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale artifact task directory retained: %v", err)
	}
	if _, err := os.Stat(r.Section.SourcePath); err != nil {
		t.Fatalf("stale artifact cleanup changed A: %v", err)
	}
}
