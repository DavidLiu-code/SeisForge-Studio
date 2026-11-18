package mappingprofile

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func validProfile(t *testing.T) Profile {
	t.Helper()
	p, err := New("field default", TargetA, prestack.DefaultHeaderMapping(), QualitySummary{
		SampledTraces: 16, ValidHeaders: 15, Confidence: .93,
		Fields:        []FieldSummary{{Field: "SourceID", Total: 16, Present: 16, Coverage: 1}, {Field: "Offset", Total: 16, Present: 15, Coverage: .9375, ConflictCount: 1}},
		ConflictCount: 1, Warnings: []string{"one header is incomplete"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProfileRoundTripAndDefensiveCopies(t *testing.T) {
	p := validProfile(t)
	profiles, err := Upsert(nil, p, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Find(profiles, "FIELD DEFAULT")
	if err != nil || got.Target != TargetA || len(got.Quality.Fields) != 2 {
		t.Fatalf("find mismatch: %+v %v", got, err)
	}
	got.Quality.Fields[0].Field = "changed"
	got.Quality.Warnings[0] = "changed"
	if profiles[0].Quality.Fields[0].Field == "changed" || profiles[0].Quality.Warnings[0] == "changed" {
		t.Fatal("Find returned aliased slices")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	if err := Save(path, profiles, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || len(loaded) != 1 || loaded[0].Name != p.Name {
		t.Fatalf("load mismatch: %+v %v", loaded, err)
	}
	if err := Save(path, profiles, false); !errors.Is(err, ErrProfileExists) {
		t.Fatalf("want overwrite protection, got %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if len(entry.Name()) > 0 && entry.Name()[0] == '.' {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestProfileValidationAndUpsert(t *testing.T) {
	p := validProfile(t)
	bad := p
	bad.Target = "C"
	if err := Validate(bad); err == nil || !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("want invalid target, got %v", err)
	}
	bad = p
	bad.Quality.Confidence = math.NaN()
	if err := Validate(bad); err == nil {
		t.Fatal("accepted NaN confidence")
	}
	bad = p
	bad.Quality.Fields = append(bad.Quality.Fields, bad.Quality.Fields[0])
	if err := Validate(bad); err == nil {
		t.Fatal("accepted duplicate field summary")
	}
	if _, err := Upsert([]Profile{p}, p, false); !errors.Is(err, ErrProfileExists) {
		t.Fatalf("want duplicate profile error, got %v", err)
	}
	p.Description = "updated"
	updated, err := Upsert([]Profile{validProfile(t)}, p, true)
	if err != nil || updated[0].Description != "updated" {
		t.Fatalf("replace failed: %+v %v", updated, err)
	}
}

func TestLoadRejectsSchemaAndTrailingData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"future","profiles":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("want schema error, got %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":"seisforge.prestack.mapping-profile.v1","profiles":[]} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("want trailing data error, got %v", err)
	}
}
