package prestackbatch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/figurebundle"
)

func TestPlanTargetsAreDeterministicAndInsideOutput(t *testing.T) {
	dir := t.TempDir()
	recipe := filepath.Join(dir, "recipe.json")
	source := filepath.Join(dir, "a.sgy")
	manifest := figurebundle.BundleManifest{Schema: figurebundle.Schema, ApplicationVersion: "test", SourceA: figurebundle.SourceIdentity{Path: source, Size: 1, ModifiedUnixNano: 2, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, RendererSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PixelSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Width: 1, Height: 1, Payload: []byte(`{"mode":"image"}`)}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(recipe, data, 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(Input{ApplicationVersion: "test", OutputDirectory: filepath.Join(dir, "out"), Figures: []FigureInput{{Recipe: recipe, Output: "z.png"}, {Recipe: recipe, Output: "a.png"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Figures) != 2 || filepath.Base(plan.Figures[0].Output) != "a.png" || filepath.Base(plan.Figures[1].Output) != "z.png" {
		t.Fatalf("unexpected deterministic order: %+v", plan.Figures)
	}
	if err := Validate(plan); err != nil {
		t.Fatal(err)
	}
}

func TestPlanRejectsEscapeAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	recipe := filepath.Join(dir, "recipe.json")
	source := filepath.Join(dir, "a.sgy")
	manifest := figurebundle.BundleManifest{Schema: figurebundle.Schema, ApplicationVersion: "test", SourceA: figurebundle.SourceIdentity{Path: source, Size: 1, ModifiedUnixNano: 2}, RendererSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PixelSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Width: 1, Height: 1, Payload: []byte(`{"mode":"image"}`)}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(recipe, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPlan(Input{ApplicationVersion: "test", OutputDirectory: filepath.Join(dir, "out"), Figures: []FigureInput{{Recipe: recipe, Output: "../escape.png"}}}); err == nil {
		t.Fatal("expected output escape rejection")
	}
	plan := Plan{SchemaVersion: SchemaVersion, ApplicationVersion: "test", OutputDirectory: filepath.Join(t.TempDir(), "out")}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := Save(path, plan, false); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, plan, false); !errors.Is(err, ErrPlanExists) {
		t.Fatalf("expected overwrite protection, got %v", err)
	}
}

func TestBatchPlanDraftClonesAndBuilds(t *testing.T) {
	dir := t.TempDir()
	recipe := filepath.Join(dir, "recipe.json")
	source := filepath.Join(dir, "a.sgy")
	manifest := figurebundle.BundleManifest{Schema: figurebundle.Schema, ApplicationVersion: "test", SourceA: figurebundle.SourceIdentity{Path: source, Size: 1, ModifiedUnixNano: 2, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, RendererSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PixelSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", Width: 1, Height: 1, Payload: []byte(`{"mode":"image"}`)}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(recipe, data, 0600); err != nil {
		t.Fatal(err)
	}
	draft := NewDraft("test", filepath.Join(dir, "out"))
	draft.AddFigure(recipe, "image.png", "image.result.json")
	clone := draft.Clone()
	clone.Figures[0].Output = "changed.png"
	if draft.Figures[0].Output == clone.Figures[0].Output {
		t.Fatal("draft clone aliases figure inputs")
	}
	plan, err := draft.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Figures) != 1 || filepath.Base(plan.Figures[0].Output) != "image.png" {
		t.Fatalf("unexpected plan: %+v", plan.Figures)
	}
}

func editorFixture(t *testing.T) Plan {
	t.Helper()
	root := filepath.Join(t.TempDir(), "out")
	return Plan{
		SchemaVersion: SchemaVersion, ApplicationVersion: "test", OutputDirectory: root,
		Figures: []FigureJob{
			{ID: "figure-001", Recipe: filepath.Join(root, "r1.json"), Output: filepath.Join(root, "one.png"), Result: filepath.Join(root, "one.result.json"), SourceA: SourceIdentity{Path: "a.sgy", Size: 1, ModifiedUnixNano: 2}},
			{ID: "figure-002", Recipe: filepath.Join(root, "r2.json"), Output: filepath.Join(root, "two.png"), Result: filepath.Join(root, "two.result.json"), SourceA: SourceIdentity{Path: "a.sgy", Size: 1, ModifiedUnixNano: 2}},
		},
		QCReports: []QCJob{{ID: "qc-001", Session: filepath.Join(root, "s.json"), Source: SourceIdentity{Path: "a.sgy", Size: 1, ModifiedUnixNano: 2}, Output: filepath.Join(root, "qc.json"), Format: ReportJSON}},
	}
}

func TestPlanEditorMovesRemovesAndUpdatesTasks(t *testing.T) {
	editor, err := NewEditor(editorFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	tasks := editor.Tasks()
	if len(tasks) != 3 || tasks[0].Ref.ID != "figure-001" || tasks[2].Ref.Kind != JobQC {
		t.Fatalf("unexpected task list: %+v", tasks)
	}
	if err := editor.Move(TaskRef{Kind: JobFigure, ID: "figure-001"}, 1); err != nil {
		t.Fatal(err)
	}
	if got := editor.Plan().Figures[1].Recipe; !strings.HasSuffix(got, "r1.json") {
		t.Fatalf("move did not reorder figures: %+v", editor.Plan().Figures)
	}
	if err := editor.UpdateFigureOutput(TaskRef{Kind: JobFigure, ID: "figure-002"}, "edited.png", "edited.result.json"); err != nil {
		t.Fatal(err)
	}
	if err := editor.UpdateQCOutput(TaskRef{Kind: JobQC, ID: "qc-001"}, "edited.csv", ReportCSV); err != nil {
		t.Fatal(err)
	}
	if err := editor.Remove(TaskRef{Kind: JobFigure, ID: "figure-001"}); err != nil {
		t.Fatal(err)
	}
	p := editor.Plan()
	if len(p.Figures) != 1 || p.Figures[0].ID != "figure-001" || filepath.Base(p.Figures[0].Output) != "edited.png" {
		t.Fatalf("unexpected edited plan: %+v", p)
	}
	if len(p.QCReports) != 1 || p.QCReports[0].Format != ReportCSV || filepath.Base(p.QCReports[0].Output) != "edited.csv" {
		t.Fatalf("unexpected QC edit: %+v", p.QCReports[0])
	}
}

func TestPlanEditorRemapsDirectoryAndReturnsDefensiveCopy(t *testing.T) {
	p := editorFixture(t)
	editor, err := NewEditor(p)
	if err != nil {
		t.Fatal(err)
	}
	newRoot := filepath.Join(t.TempDir(), "new-out")
	if err := editor.SetOutputDirectory(newRoot); err != nil {
		t.Fatal(err)
	}
	got := editor.Plan()
	if got.OutputDirectory != newRoot || filepath.Dir(got.Figures[0].Output) != newRoot || filepath.Dir(got.QCReports[0].Output) != newRoot {
		t.Fatalf("output directory was not remapped: %+v", got)
	}
	got.Figures[0].Output = "mutated"
	if editor.Plan().Figures[0].Output == "mutated" {
		t.Fatal("Plan returned an aliased figure slice")
	}
	if err := editor.UpdateFigureOutput(TaskRef{Kind: JobFigure, ID: "figure-001"}, "../escape.png", "ok.result.json"); err == nil {
		t.Fatal("expected output escape rejection")
	}
}

func TestPlanEditorRejectsDuplicateOutputWithoutMutation(t *testing.T) {
	editor, err := NewEditor(editorFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	before := editor.Plan()
	if err := editor.UpdateFigureOutput(TaskRef{Kind: JobFigure, ID: "figure-001"}, "two.png", "one.result.json"); err == nil {
		t.Fatal("expected duplicate figure output rejection")
	}
	after := editor.Plan()
	if after.Figures[0].Output != before.Figures[0].Output || after.Figures[0].Result != before.Figures[0].Result {
		t.Fatalf("failed figure update mutated plan: before=%+v after=%+v", before.Figures[0], after.Figures[0])
	}
	if err := editor.UpdateQCOutput(TaskRef{Kind: JobQC, ID: "qc-001"}, "two.png", ReportCSV); err == nil {
		t.Fatal("expected duplicate QC output rejection")
	}
	after = editor.Plan()
	if after.QCReports[0].Output != before.QCReports[0].Output || after.QCReports[0].Format != before.QCReports[0].Format {
		t.Fatalf("failed QC update mutated plan: before=%+v after=%+v", before.QCReports[0], after.QCReports[0])
	}
}

func TestPlanEditorSaveRoundTripPreservesTasksAndSources(t *testing.T) {
	p := editorFixture(t)
	original := p.Clone()
	editor, err := NewEditor(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.Move(TaskRef{Kind: JobFigure, ID: "figure-002"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := editor.SetOutputDirectory(filepath.Join(t.TempDir(), "edited-out")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "edited-plan.json")
	if err := editor.Save(path, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(loaded); err != nil {
		t.Fatal(err)
	}
	if len(loaded.Figures) != len(original.Figures) || len(loaded.QCReports) != len(original.QCReports) {
		t.Fatalf("round-trip task counts changed: got figures=%d qc=%d", len(loaded.Figures), len(loaded.QCReports))
	}
	if loaded.Figures[0].SourceA != original.Figures[1].SourceA || loaded.Figures[1].SourceA != original.Figures[0].SourceA {
		t.Fatalf("source identity/order changed unexpectedly: %+v", loaded.Figures)
	}
	if loaded.QCReports[0].Source != original.QCReports[0].Source {
		t.Fatalf("QC source identity changed: got=%+v want=%+v", loaded.QCReports[0].Source, original.QCReports[0].Source)
	}
	if filepath.Dir(loaded.Figures[0].Output) != loaded.OutputDirectory || filepath.Dir(loaded.QCReports[0].Output) != loaded.OutputDirectory {
		t.Fatalf("outputs were not remapped under output directory: %+v", loaded)
	}
	// Saving an edited copy must not mutate the caller's plan or source identity.
	if original.Figures[0].SourceA != p.Figures[0].SourceA || original.QCReports[0].Source != p.QCReports[0].Source {
		t.Fatal("source identity mutated while editing")
	}
}

func TestPlanEditorInvalidMutationsAreAtomic(t *testing.T) {
	editor, err := NewEditor(editorFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	before := editor.Plan()
	if err := editor.Move(TaskRef{Kind: JobFigure, ID: "figure-001"}, 99); err == nil {
		t.Fatal("expected out-of-range move rejection")
	}
	if err := editor.Move(TaskRef{Kind: JobQC, ID: "missing"}, 0); err == nil {
		t.Fatal("expected missing task rejection")
	}
	if err := editor.UpdateQCOutput(TaskRef{Kind: JobQC, ID: "qc-001"}, "valid.json", ReportFormat("xml")); err == nil {
		t.Fatal("expected invalid report format rejection")
	}
	after := editor.Plan()
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("failed mutations changed plan: before=%s after=%s", beforeJSON, afterJSON)
	}
}

func TestPlanEditorTaskGroupsRemainFigureThenQC(t *testing.T) {
	editor, err := NewEditor(editorFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.Move(TaskRef{Kind: JobQC, ID: "qc-001"}, 0); err != nil {
		t.Fatal(err)
	}
	tasks := editor.Tasks()
	if len(tasks) != 3 || tasks[0].Ref.Kind != JobFigure || tasks[1].Ref.Kind != JobFigure || tasks[2].Ref.Kind != JobQC {
		t.Fatalf("editor exposed cross-kind ordering: %+v", tasks)
	}
	if err := editor.Remove(TaskRef{Kind: JobFigure, ID: "figure-001"}); err != nil {
		t.Fatal(err)
	}
	tasks = editor.Tasks()
	if len(tasks) != 2 || tasks[0].Ref.ID != "figure-001" || tasks[1].Ref.ID != "qc-001" {
		t.Fatalf("IDs were not deterministically regenerated: %+v", tasks)
	}
}
