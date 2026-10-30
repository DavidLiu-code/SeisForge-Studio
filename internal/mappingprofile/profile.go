// Package mappingprofile stores named, metadata-only HeaderMapping profiles.
// It is intentionally independent from Win32 controls and SEG-Y readers so
// profiles can be validated and tested before they are applied to A or B.
package mappingprofile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

const SchemaVersion = "seisforge.prestack.mapping-profile.v1"

var (
	ErrInvalidProfile = errors.New("invalid prestack mapping profile")
	ErrProfileExists  = errors.New("mapping profile already exists")
	ErrProfileMissing = errors.New("mapping profile not found")
)

type Target string

const (
	TargetA Target = "A"
	TargetB Target = "B"
)

// FieldSummary is a deterministic coverage record for one mapped field.
// Total and Present refer to the headers sampled while validating a profile.
type FieldSummary struct {
	Field         string  `json:"field"`
	Total         int     `json:"total"`
	Present       int     `json:"present"`
	Coverage      float64 `json:"coverage"`
	ConflictCount int     `json:"conflict_count"`
}

// QualitySummary keeps the human-facing evidence that accompanied a mapping
// decision. It is advisory metadata; applying a profile always validates the
// actual HeaderMapping again.
type QualitySummary struct {
	SampledTraces int            `json:"sampled_traces"`
	ValidHeaders  int            `json:"valid_headers"`
	Fields        []FieldSummary `json:"fields,omitempty"`
	ConflictCount int            `json:"conflict_count"`
	Confidence    float64        `json:"confidence"`
	Warnings      []string       `json:"warnings,omitempty"`
}

// Profile is a named mapping choice. SourcePathHint is optional and is only a
// hint for UI filtering; it is never used as a file identity or cache key.
type Profile struct {
	Name           string                 `json:"name"`
	Description    string                 `json:"description,omitempty"`
	Target         Target                 `json:"target"`
	SourcePathHint string                 `json:"source_path_hint,omitempty"`
	Mapping        prestack.HeaderMapping `json:"mapping"`
	Quality        QualitySummary         `json:"quality"`
}

type File struct {
	SchemaVersion string    `json:"schema_version"`
	Profiles      []Profile `json:"profiles"`
}

func New(name string, target Target, mapping prestack.HeaderMapping, quality QualitySummary) (Profile, error) {
	p := Profile{Name: strings.TrimSpace(name), Target: target, Mapping: mapping, Quality: cloneQuality(quality)}
	if err := Validate(p); err != nil {
		return Profile{}, err
	}
	return p, nil
}

func (p Profile) Clone() Profile {
	p.Quality = cloneQuality(p.Quality)
	return p
}

func cloneQuality(q QualitySummary) QualitySummary {
	q.Fields = append([]FieldSummary(nil), q.Fields...)
	q.Warnings = append([]string(nil), q.Warnings...)
	return q
}

func CloneProfiles(in []Profile) []Profile {
	out := make([]Profile, len(in))
	for i := range in {
		out[i] = in[i].Clone()
	}
	return out
}

func Validate(p Profile) error {
	if strings.TrimSpace(p.Name) == "" || len([]rune(p.Name)) > 128 {
		return fmt.Errorf("%w: name must contain 1..128 characters", ErrInvalidProfile)
	}
	for _, r := range p.Name {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: name contains a control character", ErrInvalidProfile)
		}
	}
	if p.Target != TargetA && p.Target != TargetB {
		return fmt.Errorf("%w: target must be A or B", ErrInvalidProfile)
	}
	if err := p.Mapping.Validate(); err != nil {
		return fmt.Errorf("%w: mapping: %v", ErrInvalidProfile, err)
	}
	q := p.Quality
	if q.SampledTraces < 0 || q.ValidHeaders < 0 || q.ValidHeaders > q.SampledTraces || q.ConflictCount < 0 || !finite(q.Confidence) || q.Confidence < 0 || q.Confidence > 1 {
		return fmt.Errorf("%w: invalid quality summary", ErrInvalidProfile)
	}
	seen := make(map[string]struct{}, len(q.Fields))
	for _, f := range q.Fields {
		if strings.TrimSpace(f.Field) == "" || f.Total < 0 || f.Present < 0 || f.Present > f.Total || f.ConflictCount < 0 || !finite(f.Coverage) || f.Coverage < 0 || f.Coverage > 1 {
			return fmt.Errorf("%w: invalid field summary", ErrInvalidProfile)
		}
		if _, ok := seen[f.Field]; ok {
			return fmt.Errorf("%w: duplicate field summary %q", ErrInvalidProfile, f.Field)
		}
		seen[f.Field] = struct{}{}
	}
	return nil
}

func ValidateFile(f File) error {
	if f.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema %q", ErrInvalidProfile, f.SchemaVersion)
	}
	seen := make(map[string]struct{}, len(f.Profiles))
	for _, p := range f.Profiles {
		if err := Validate(p); err != nil {
			return err
		}
		key := strings.ToLower(strings.TrimSpace(p.Name))
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: duplicate profile name %q", ErrInvalidProfile, p.Name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func Upsert(profiles []Profile, p Profile, replace bool) ([]Profile, error) {
	if err := Validate(p); err != nil {
		return nil, err
	}
	out := CloneProfiles(profiles)
	key := strings.ToLower(strings.TrimSpace(p.Name))
	for i := range out {
		if strings.ToLower(strings.TrimSpace(out[i].Name)) != key {
			continue
		}
		if !replace {
			return nil, ErrProfileExists
		}
		out[i] = p.Clone()
		return out, nil
	}
	out = append(out, p.Clone())
	return out, nil
}

func Find(profiles []Profile, name string) (Profile, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	for _, p := range profiles {
		if strings.ToLower(strings.TrimSpace(p.Name)) == key {
			return p.Clone(), nil
		}
	}
	return Profile{}, ErrProfileMissing
}

func Save(path string, profiles []Profile, overwrite bool) error {
	f := File{SchemaVersion: SchemaVersion, Profiles: CloneProfiles(profiles)}
	if err := ValidateFile(f); err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: empty path", ErrInvalidProfile)
	}
	path = filepath.Clean(path)
	if _, err := os.Stat(path); err == nil && !overwrite {
		return ErrProfileExists
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mapping-profile-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return publishProfile(tmpName, path, overwrite)
}

func Load(path string) ([]Profile, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	var f File
	dec := json.NewDecoder(fh)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrInvalidProfile, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%w: trailing JSON", ErrInvalidProfile)
		}
		return nil, fmt.Errorf("%w: trailing data: %v", ErrInvalidProfile, err)
	}
	if err := ValidateFile(f); err != nil {
		return nil, err
	}
	return CloneProfiles(f.Profiles), nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
