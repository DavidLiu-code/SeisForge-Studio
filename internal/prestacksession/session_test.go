package prestacksession

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func validState() SessionState {
	return SessionState{
		ASource:  SourceIdentity{Path: `C:\data\a.sgy`, Size: 1234, ModTimeNS: 987654321},
		AMapping: prestack.DefaultHeaderMapping(),
		Selection: prestack.GatherSelection{Type: prestack.GatherCMP, Key: prestack.GatherKey{All: true},
			Secondary: prestack.SecondaryOffset, OffsetBinSize: 20, AzimuthBinSize: 1,
			SampleStart: 2, SampleEnd: 91},
		Display: DisplayState{Mode: "Image", Palette: "黑灰白", WiggleDecimation: 5, GainPercent: 6,
			AGC: true, RangeEnabled: true, RangeMin: -2, RangeMax: 2, SampleStart: 2, SampleEnd: 91,
			ViewFirst: 0, ViewLast: 24},
		Compare: CompareState{Enabled: true, BSource: &SourceIdentity{Path: `C:\data\b.sgy`, Size: 222, ModTimeNS: 333},
			BMapping: func() *prestack.HeaderMapping { m := prestack.DefaultHeaderMapping(); return &m }(),
			Filter:   prestack.DefaultCompareFilterSet(), Mode: "Image", SampleStart: 2, SampleEnd: 91,
			ViewXMin: 0, ViewXMax: 24, ViewYMin: 0, ViewYMax: 91, AxisCompatible: true},
		QC: QCState{CurrentType: "CMP", CurrentKey: "全部范围"},
		Geometry: GeometryState{Layers: []string{"Source", "Receiver"}, Style: "标准", Zoom: 1, PanX: 2, PanY: -1,
			SelectedSourceID: 11, SelectedReceiverID: 22, LinkActive: true, BoundsValid: true,
			BoundsXMin: -10, BoundsXMax: 100, BoundsYMin: -20, BoundsYMax: 200},
		Mapping: MappingState{Target: "A"},
	}
}

func TestCaptureApplyDefensiveRoundTrip(t *testing.T) {
	state := validState()
	s, err := CapturePrestackSessionSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	if s.SchemaVersion != SchemaVersion || s.A.Path != state.ASource.Path || s.Selection.Secondary != prestack.SecondaryOffset {
		t.Fatalf("snapshot lost state: %+v", s)
	}
	state.Selection.Keys = []prestack.GatherKey{{ID: 99}}
	state.Geometry.Layers[0] = "mutated"
	state.Compare.BSource.Path = "mutated"
	if len(s.Selection.Keys) != 0 || s.Geometry.Layers[0] != "Source" || s.Compare.BSource.Path != `C:\data\b.sgy` {
		t.Fatal("capture aliases caller-owned memory")
	}
	got, err := ApplyPrestackSessionSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	got.Geometry.Layers[0] = "changed"
	got.Compare.BSource.Path = "changed"
	if s.Geometry.Layers[0] != "Source" || s.Compare.BSource.Path != `C:\data\b.sgy` {
		t.Fatal("apply returned aliases")
	}
}

func TestGatherOnlySnapshotAllowsCompareSentinelRange(t *testing.T) {
	state := validState()
	state.Compare = CompareState{Enabled: false, SampleStart: 0, SampleEnd: 0, ViewXMin: 0, ViewXMax: -1, ViewYMin: 0, ViewYMax: -1}
	s, err := CapturePrestackSessionSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	if s.Compare.Enabled {
		t.Fatal("gather-only snapshot unexpectedly enabled Compare")
	}
	if err := ValidatePrestackSessionSnapshot(s); err != nil {
		t.Fatalf("gather-only sentinel Compare range rejected: %v", err)
	}
}

func TestValidateRejectsInvalidValuesAndUnknownSchema(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Snapshot)
	}{
		{"schema", func(s *Snapshot) { s.SchemaVersion = "future" }},
		{"path", func(s *Snapshot) { s.A.Path = "" }},
		{"mapping", func(s *Snapshot) { s.AMapping.OffsetByte = 999 }},
		{"gain", func(s *Snapshot) { s.Display.GainPercent = math.Inf(1) }},
		{"range", func(s *Snapshot) { s.Display.RangeMin, s.Display.RangeMax = 1, 1 }},
		{"geometry", func(s *Snapshot) { s.Geometry.Zoom = -1 }},
		{"compare", func(s *Snapshot) { s.Compare.ViewXMin, s.Compare.ViewXMax = 4, 2 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := CapturePrestackSessionSnapshot(validState())
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(&s)
			if err := ValidatePrestackSessionSnapshot(s); err == nil || !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("want ErrInvalidSnapshot, got %v", err)
			}
		})
	}
}

func TestSaveLoadAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := CapturePrestackSessionSnapshot(validState())
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, s, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != SchemaVersion || loaded.A != s.A || loaded.Compare.BSource == nil {
		t.Fatalf("loaded snapshot mismatch: %+v", loaded)
	}
	if err := Save(path, s, false); !errors.Is(err, ErrSnapshotExists) {
		t.Fatalf("want ErrSnapshotExists, got %v", err)
	}
	s.Display.Palette = "彩虹"
	if err := Save(path, s, true); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil || loaded.Display.Palette != "彩虹" {
		t.Fatalf("overwrite failed: %v %+v", err, loaded)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".prestack-session-") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestLoadRejectsTrailingJSONAndRuntimeFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.json")
	s, err := CapturePrestackSessionSnapshot(validState())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("\n{}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("want invalid trailing JSON, got %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":"seisforge.prestack.session.v1","a":{"path":"x","size":1,"mod_time_ns":1},"a_mapping":{"OffsetByte":999}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("want invalid mapping/default state, got %v", err)
	}
}
