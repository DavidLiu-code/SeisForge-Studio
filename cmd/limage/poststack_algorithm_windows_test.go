//go:build windows

package main

import (
	"context"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/registry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPoststackAlgorithmMissingNeverFallsBack(t *testing.T) {
	old, controls := poststackAlgorithms, cz
	defer func() { poststackAlgorithms = old; cz = controls }()
	poststackAlgorithms = registry.New(APP_VERSION)
	cz = cadzowControls{}
	issues, err := poststackAlgorithms.Scan(context.Background(), []string{t.TempDir()})
	if err != nil || len(issues) != 0 || len(poststackAlgorithms.Modules()) != 0 {
		t.Fatalf("empty installation: %v %v", issues, err)
	}
	if _, _, err := readAlgorithmParameters(); err == nil {
		t.Fatal("missing algorithm must not execute Copy")
	}
}
func TestPoststackAlgorithmGenericParameterRows(t *testing.T) {
	types := []string{"integer", "number", "boolean", "string", "enum", "file", "range"}
	var schema []api.ParameterDescriptor
	for _, typ := range types {
		schema = append(schema, api.ParameterDescriptor{ID: typ, Type: typ, Name: typ})
	}
	rows := algorithmParameterRows([]api.ParameterDescriptor{{ID: "group", Type: "group", Parameters: schema}}, nil)
	if len(rows) != 8 {
		t.Fatalf("rows=%d", len(rows))
	}
	for i, p := range rows[1:] {
		if p.Descriptor.Type != types[i] || len(p.Path) != 2 || p.Path[0] != "group" {
			t.Fatalf("row: %+v", p)
		}
	}
}
func TestPoststackAlgorithmNamesAndFingerprint(t *testing.T) {
	at := time.Date(2026, 11, 12, 1, 2, 3, 0, time.Local)
	params := map[string]any{"rank": 2, "method": "example"}
	a, b := algorithmOutputNames("地震 A.sgy", "example", params, at)
	if !strings.HasPrefix(filepath.Base(a), "地震 A_20261112_010203_example_") || !strings.HasSuffix(b, "_Part2.sgy") {
		t.Fatalf("%s %s", a, b)
	}
	before := algorithmParameterFingerprint(params)
	params["rank"] = 3
	if before == algorithmParameterFingerprint(params) {
		t.Fatal("parameter change did not invalidate fingerprint")
	}
}

func TestPoststackAlgorithmGroupDefaultControls(t *testing.T) {
	schema := []api.ParameterDescriptor{{ID: "group", Type: "group", Default: map[string]any{"gain": 2.0}, Parameters: []api.ParameterDescriptor{{ID: "gain", Type: "number", Default: 1.0}}}}
	rows := algorithmParameterRows(schema, nil)
	if rows[1].Descriptor.Default != 2.0 || schema[0].Parameters[0].Default != 1.0 {
		t.Fatal("group defaults must populate controls without mutating registry descriptors")
	}
}

func TestPoststackAlgorithmCommonPresentationDoesNotChangeSchemaOrder(t *testing.T) {
	schema := []api.ParameterDescriptor{
		{ID: "trace_window", Type: "integer", UI: &api.ParameterUI{Section: api.ParameterUICommon, Order: 0}},
		{ID: "time_window", Type: "integer", Default: 0, UI: &api.ParameterUI{Section: api.ParameterUICommon, Order: 1}},
		{ID: "output_sample_block", Type: "integer", Default: 0},
		{ID: "frequency_max_hz", Type: "number", UI: &api.ParameterUI{Section: api.ParameterUICommon, Order: 3}},
		{ID: "rank", Type: "integer", UI: &api.ParameterUI{Section: api.ParameterUICommon, Order: 2}},
	}
	rows := algorithmParameterRows(schema, nil)
	markCommonAlgorithmParameters(rows)
	if len(rows) != len(schema) || rows[2].Common || rows[3].CommonOrder != 3 || rows[4].CommonOrder != 2 || rows[1].Descriptor.Default != 0 || rows[2].Descriptor.Default != 0 {
		t.Fatalf("presentation mutated the schema or lost the zero values: %+v", rows)
	}
	for i := range rows {
		if rows[i].Descriptor.ID != schema[i].ID {
			t.Fatal("UI ordering changed the manifest order")
		}
	}
	legacy := algorithmParameterRows([]api.ParameterDescriptor{{ID: "first", Type: "integer"}, {ID: "second", Type: "number"}}, nil)
	markCommonAlgorithmParameters(legacy)
	if !legacy[0].Common || !legacy[1].Common || legacy[1].CommonOrder != 1 {
		t.Fatal("legacy descriptors lost original order")
	}
}

func TestPoststackArtifactExportRejectsDoubleRounding(t *testing.T) {
	for _, tc := range []struct {
		origin    int64
		first, dt int
		exact     bool
	}{
		{500, 1, 500, false}, {1000, 1, 500, false}, {500, 0, 500, false},
		{1000, 2, 500, true}, {-2000, 3, 2000, true}, {0, -1, 1000, false},
	} {
		if got := poststackArtifactSEGYCropExact(tc.origin, tc.first, tc.dt); got != tc.exact {
			t.Fatalf("%+v got %v", tc, got)
		}
	}
}

func TestPoststackAlgorithmCaptureUsesVisiblePhysicalRange(t *testing.T) {
	restore := installCompareTraceSelectionFixture(t)
	defer restore()
	oldStart, oldEnd, oldStep := compareTraceStart, compareTraceEnd, compareTraceStep
	defer func() { compareTraceStart, compareTraceEnd, compareTraceStep = oldStart, oldEnd, oldStep }()
	compareTraceStart, compareTraceEnd, compareTraceStep = 3, 5, 2
	traces, coords, _ := cadzowCurrentProfile()
	if !reflect.DeepEqual(traces, []int64{3, 5}) || !reflect.DeepEqual(coords, []int32{100, 120}) {
		t.Fatalf("capture escaped current physical selection: %v / %v", traces, coords)
	}
}

func TestPoststackArtifactCompactRowsDoNotReuseAPhysicalRange(t *testing.T) {
	oldStart, oldEnd, oldStep := compareTraceStart, compareTraceEnd, compareTraceStep
	generatedPoststackMu.Lock()
	old := generatedPoststackResults
	generatedPoststackResults = []*generatedPoststackResult{{Section: &poststack.SectionResult{Part2: "derived.sfdata", TraceIndices: []int64{10, 12, 14}}}}
	generatedPoststackMu.Unlock()
	defer func() {
		compareTraceStart, compareTraceEnd, compareTraceStep = oldStart, oldEnd, oldStep
		generatedPoststackMu.Lock()
		generatedPoststackResults = old
		generatedPoststackMu.Unlock()
	}()
	compareTraceStart, compareTraceEnd, compareTraceStep = 10, 14, 2
	start, end, step := compareTraceRangeForGeometry(&segy.File{Info: segy.Info{Path: "derived.sfdata", TraceCount: 3}})
	if start != 0 || end != 2 || step != 1 {
		t.Fatalf("A physical range applied to compact B: %d %d %d", start, end, step)
	}
}
