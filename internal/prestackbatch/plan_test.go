package prestackbatch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
