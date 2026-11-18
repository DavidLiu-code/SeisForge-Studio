// Package algorithmapi is the public, UI-independent external algorithm protocol.
// It intentionally contains no host data readers or private algorithm code.
package algorithmapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const ProtocolVersion = "seisforge.algorithm.v1"
const RunRecordSchema = "seisforge.algorithm.run.v1"

// ParameterUI controls form placement only; it never changes the value in a
// RunRequest. Lower order values are shown first, preserving declaration order
// for ties. A missing ParameterUI leaves placement to the host.
type ParameterUI struct {
	Section string `json:"section"`
	Order   int    `json:"order,omitempty"`
}

const (
	ParameterUICommon   = "common"
	ParameterUIAdvanced = "advanced"
)

type ParameterDescriptor struct {
	ID          string                `json:"id"`
	Type        string                `json:"type"`
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Unit        string                `json:"unit,omitempty"`
	Required    bool                  `json:"required,omitempty"`
	Default     any                   `json:"default,omitempty"`
	Min         *float64              `json:"min,omitempty"`
	Max         *float64              `json:"max,omitempty"`
	Step        *float64              `json:"step,omitempty"`
	Options     []string              `json:"options,omitempty"`
	Parameters  []ParameterDescriptor `json:"parameters,omitempty"`
	UI          *ParameterUI          `json:"ui,omitempty"`
}

type AlgorithmDescriptor struct {
	ID                 string                `json:"id"`
	Version            string                `json:"version"`
	Name               string                `json:"name"`
	Category           string                `json:"category"`
	Description        string                `json:"description"`
	ProtocolVersion    string                `json:"protocol_version"`
	InputKinds         []string              `json:"input_kinds"`
	OutputKinds        []string              `json:"output_kinds"`
	SupportsProgress   bool                  `json:"supports_progress"`
	SupportsCancel     bool                  `json:"supports_cancel"`
	Parameters         []ParameterDescriptor `json:"parameters"`
	Executable         string                `json:"executable"`
	MinimumHostVersion string                `json:"minimum_host_version"`
}

type AlgorithmInput struct {
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	Kind        string `json:"kind"`
}
type AlgorithmOutput struct {
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Role        string `json:"role,omitempty"`
}
type RunRequest struct {
	ProtocolVersion  string         `json:"protocol_version"`
	RunID            string         `json:"run_id"`
	AlgorithmID      string         `json:"algorithm_id"`
	AlgorithmVersion string         `json:"algorithm_version"`
	Parameters       map[string]any `json:"parameters"`
	InputFingerprint string         `json:"input_fingerprint"`
}
type ProgressEvent struct {
	Type    string            `json:"type"`
	Value   float64           `json:"value,omitempty"`
	Message string            `json:"message,omitempty"`
	Status  string            `json:"status,omitempty"`
	Output  string            `json:"output,omitempty"`
	Outputs []AlgorithmOutput `json:"outputs,omitempty"`
}
type ProgressReporter func(ProgressEvent)

type AlgorithmRunRecord struct {
	Schema           string         `json:"schema"`
	RunID            string         `json:"run_id"`
	AlgorithmID      string         `json:"algorithm_id"`
	AlgorithmVersion string         `json:"algorithm_version"`
	ProtocolVersion  string         `json:"protocol_version"`
	Parameters       map[string]any `json:"parameters"`
	ManifestSHA256   string         `json:"manifest_sha256"`
	ExecutableSHA256 string         `json:"executable_sha256"`
	InputFingerprint string         `json:"input_fingerprint"`
	// Host-only provenance for algorithms that read real temporal context beyond
	// the requested output window. These are metadata, never waveform arrays.
	InputContextFingerprint string                    `json:"input_context_fingerprint,omitempty"`
	ProcessingScope         *AlgorithmProcessingScope `json:"processing_scope,omitempty"`
	OutputFingerprint       string                    `json:"output_fingerprint,omitempty"`
	Outputs                 []AlgorithmOutput         `json:"outputs,omitempty"`
	Status                  string                    `json:"status"`
	StartedAt               time.Time                 `json:"started_at"`
	FinishedAt              time.Time                 `json:"finished_at"`
	DurationMS              int64                     `json:"duration_ms"`
	Error                   string                    `json:"error,omitempty"`
}

type AlgorithmProcessingScope struct {
	TraceCount       int64 `json:"trace_count"`
	SampleStart      int64 `json:"sample_start"`
	SampleEnd        int64 `json:"sample_end"`
	ContextStart     int64 `json:"context_start"`
	ContextEnd       int64 `json:"context_end"`
	SampleIntervalUS int64 `json:"sample_interval_us"`
}

type RangeValue struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
var semanticVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?(?:\+[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)

func ParseManifest(data []byte, hostVersion string) (AlgorithmDescriptor, error) {
	var d AlgorithmDescriptor
	if len(data) > 1<<20 {
		return d, errors.New("manifest exceeds 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, fmt.Errorf("manifest: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return d, errors.New("manifest contains trailing data")
	}
	return d, ValidateDescriptor(d, hostVersion)
}

func ValidateDescriptor(d AlgorithmDescriptor, hostVersion string) error {
	if !identifier.MatchString(d.ID) {
		return errors.New("invalid algorithm id")
	}
	if strings.TrimSpace(d.Name) == "" || len(d.Name) > 256 {
		return errors.New("invalid algorithm name")
	}
	if d.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported protocol version %q", d.ProtocolVersion)
	}
	if _, err := version(d.Version); err != nil {
		return fmt.Errorf("algorithm version: %w", err)
	}
	min, err := version(d.MinimumHostVersion)
	if err != nil {
		return fmt.Errorf("minimum host version: %w", err)
	}
	host, err := version(hostVersion)
	if err != nil {
		return fmt.Errorf("host version: %w", err)
	}
	for i := 0; i < 3; i++ {
		if host[i] > min[i] {
			break
		}
		if host[i] < min[i] {
			return errors.New("module requires a newer host version")
		}
	}
	if strings.TrimSpace(d.Executable) == "" || strings.ContainsRune(d.Executable, 0) {
		return errors.New("executable is required")
	}
	for _, kinds := range [][]string{d.InputKinds, d.OutputKinds} {
		if len(kinds) == 0 {
			return errors.New("input_kinds and output_kinds are required")
		}
		seen := map[string]bool{}
		for _, k := range kinds {
			if !identifier.MatchString(k) || seen[k] {
				return fmt.Errorf("invalid or duplicate kind %q", k)
			}
			seen[k] = true
		}
	}
	count := 0
	return validateSchema(d.Parameters, 0, &count)
}

func version(s string) ([3]uint64, error) {
	var v [3]uint64
	if !semanticVersion.MatchString(s) {
		return v, errors.New("expected major.minor.patch with optional prerelease/build suffix")
	}
	s = strings.TrimPrefix(s, "v")
	base := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '+' })
	if len(base) == 0 {
		return v, errors.New("expected major.minor.patch")
	}
	parts := strings.Split(base[0], ".")
	if len(parts) != 3 {
		return v, errors.New("expected major.minor.patch")
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return v, errors.New("invalid version component")
		}
		v[i] = n
	}
	return v, nil
}

func validateSchema(schema []ParameterDescriptor, depth int, count *int) error {
	if depth > 8 {
		return errors.New("parameter groups are too deeply nested")
	}
	seen := map[string]bool{}
	for _, p := range schema {
		*count++
		if *count > 256 {
			return errors.New("too many parameters")
		}
		if !identifier.MatchString(p.ID) || seen[p.ID] {
			return fmt.Errorf("invalid or duplicate parameter %q", p.ID)
		}
		seen[p.ID] = true
		switch p.Type {
		case "integer", "number", "boolean", "string", "enum", "file", "range", "group":
		default:
			return fmt.Errorf("parameter %s has unknown type %q", p.ID, p.Type)
		}
		if ui := p.UI; ui != nil {
			if ui.Section != ParameterUICommon && ui.Section != ParameterUIAdvanced {
				return fmt.Errorf("parameter %s has invalid ui section %q", p.ID, ui.Section)
			}
			if ui.Order < 0 || ui.Order > 255 {
				return fmt.Errorf("parameter %s has ui order outside 0..255", p.ID)
			}
		}
		for _, n := range []*float64{p.Min, p.Max, p.Step} {
			if n != nil && !finite(*n) {
				return fmt.Errorf("parameter %s has non-finite constraint", p.ID)
			}
		}
		if p.Min != nil || p.Max != nil || p.Step != nil {
			if p.Type != "integer" && p.Type != "number" && p.Type != "range" {
				return fmt.Errorf("parameter %s has numeric constraints on a non-numeric type", p.ID)
			}
			if p.Type == "integer" {
				for _, n := range []*float64{p.Min, p.Max, p.Step} {
					if n != nil && (*n != math.Trunc(*n) || math.Abs(*n) > 9007199254740991) {
						return fmt.Errorf("parameter %s has non-integral integer constraints", p.ID)
					}
				}
			}
		}
		if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
			return fmt.Errorf("parameter %s has inverted limits", p.ID)
		}
		if p.Step != nil && *p.Step <= 0 {
			return fmt.Errorf("parameter %s has non-positive step", p.ID)
		}
		if p.Type == "enum" {
			if len(p.Options) == 0 {
				return fmt.Errorf("parameter %s has no enum values", p.ID)
			}
			opts := map[string]bool{}
			for _, s := range p.Options {
				if opts[s] || strings.ContainsRune(s, 0) {
					return fmt.Errorf("parameter %s has invalid enum values", p.ID)
				}
				opts[s] = true
			}
		} else if len(p.Options) > 0 {
			return fmt.Errorf("parameter %s is not enum", p.ID)
		}
		if p.Type == "group" {
			if err := validateSchema(p.Parameters, depth+1, count); err != nil {
				return err
			}
		} else if len(p.Parameters) > 0 {
			return fmt.Errorf("parameter %s is not group", p.ID)
		}
		if p.Default != nil {
			if _, err := normalizeValue(p, p.Default); err != nil {
				return fmt.Errorf("parameter %s default: %w", p.ID, err)
			}
		}
	}
	return nil
}

// NormalizeParameters applies defaults and returns an independently owned,
// JSON-serializable value tree. Unknown keys and coercions are rejected.
func NormalizeParameters(schema []ParameterDescriptor, values map[string]any) (map[string]any, error) {
	count := 0
	if err := validateSchema(schema, 0, &count); err != nil {
		return nil, err
	}
	return normalize(schema, values)
}
func normalize(schema []ParameterDescriptor, values map[string]any) (map[string]any, error) {
	known := map[string]bool{}
	for _, p := range schema {
		known[p.ID] = true
	}
	for k := range values {
		if !known[k] {
			return nil, fmt.Errorf("unknown parameter %q", k)
		}
	}
	out := make(map[string]any, len(schema))
	for _, p := range schema {
		v, ok := values[p.ID]
		if !ok || v == nil {
			v = p.Default
			if v == nil {
				if p.Required {
					return nil, fmt.Errorf("parameter %s is required", p.ID)
				}
				if p.Type == "group" {
					v = map[string]any{}
				} else {
					continue
				}
			}
		}
		n, err := normalizeValue(p, v)
		if err != nil {
			return nil, fmt.Errorf("parameter %s: %w", p.ID, err)
		}
		out[p.ID] = n
	}
	return out, nil
}
func normalizeValue(p ParameterDescriptor, v any) (any, error) {
	switch p.Type {
	case "integer", "number":
		n, ok := number(v)
		if !ok || !finite(n) {
			return nil, errors.New("expected finite number")
		}
		if p.Min != nil && n < *p.Min || p.Max != nil && n > *p.Max {
			return nil, errors.New("number outside allowed range")
		}
		if p.Type == "integer" {
			if n != math.Trunc(n) || math.Abs(n) > 9007199254740991 {
				return nil, errors.New("expected exact JSON integer")
			}
			if !onParameterStep(p, n) {
				return nil, errors.New("integer is not aligned with the allowed step")
			}
			return int64(n), nil
		}
		if !onParameterStep(p, n) {
			return nil, errors.New("number is not aligned with the allowed step")
		}
		return n, nil
	case "boolean":
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("expected boolean")
		}
		return b, nil
	case "string", "file", "enum":
		s, ok := v.(string)
		if !ok || strings.ContainsRune(s, 0) || len(s) > 32768 {
			return nil, errors.New("expected bounded string without NUL")
		}
		if p.Required && strings.TrimSpace(s) == "" {
			return nil, errors.New("empty required string")
		}
		if p.Type == "enum" {
			for _, o := range p.Options {
				if o == s {
					return s, nil
				}
			}
			return nil, errors.New("unknown enum value")
		}
		return s, nil
	case "range":
		var r RangeValue
		switch t := v.(type) {
		case RangeValue:
			r = t
		case map[string]any:
			if len(t) != 2 {
				return nil, errors.New("range requires min and max")
			}
			var a, b bool
			r.Min, a = number(t["min"])
			r.Max, b = number(t["max"])
			if !a || !b {
				return nil, errors.New("range requires numeric min and max")
			}
		default:
			return nil, errors.New("expected range")
		}
		if !finite(r.Min) || !finite(r.Max) || r.Min > r.Max {
			return nil, errors.New("invalid range")
		}
		if p.Min != nil && r.Min < *p.Min || p.Max != nil && r.Max > *p.Max {
			return nil, errors.New("range outside allowed bounds")
		}
		if !onParameterStep(p, r.Min) || !onParameterStep(p, r.Max) {
			return nil, errors.New("range endpoints are not aligned with the allowed step")
		}
		return r, nil
	case "group":
		m, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("expected parameter group")
		}
		return normalize(p.Parameters, m)
	}
	return nil, errors.New("unknown parameter type")
}

// Step is anchored at the declared minimum, or zero when no minimum exists.
// Validate values without rounding: the same values are passed to the module.
func onParameterStep(p ParameterDescriptor, value float64) bool {
	if p.Step == nil {
		return true
	}
	anchor := 0.0
	if p.Min != nil {
		anchor = *p.Min
	}
	if p.Type == "integer" {
		// Schema/value validation bounds all three operands to exact JSON integers;
		// the subtraction therefore remains safely within int64.
		return (int64(value)-int64(anchor))%int64(*p.Step) == 0
	}
	q := (value - anchor) / *p.Step
	if !finite(q) {
		return false
	}
	// Permit floating representation noise (e.g. (0.3-0.1)/0.1), never half
	// steps or a tolerance that grows to a material fraction of the grid.
	const epsilon = 2.220446049250313e-16
	tolerance := math.Min(1e-7, math.Max(1e-12, 8*epsilon*math.Max(1, math.Abs(q))))
	return math.Abs(q-math.Round(q)) <= tolerance
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, e := n.Float64()
		return f, e == nil
	}
	return 0, false
}

func CloneDescriptor(d AlgorithmDescriptor) AlgorithmDescriptor {
	b, _ := json.Marshal(d)
	var copy AlgorithmDescriptor
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&copy)
	return copy
}
