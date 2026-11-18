// Package poststack contains the UI-independent post-stack processing
// contracts. Win32 code consumes this package but is not part of it.
package poststack

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
)

type Metadata struct {
	TraceCount       int
	SamplesPerTrace  int
	SampleIntervalUS int
	TimeOriginUS     int64
	Direction        string
	InlineCount      int
	CrosslineCount   int
}

func (m Metadata) Validate() error {
	if m.TraceCount <= 0 || m.SamplesPerTrace <= 0 {
		return errors.New("trace and sample counts must be positive")
	}
	if m.SampleIntervalUS <= 0 {
		return errors.New("sample interval must be positive")
	}
	if m.Direction == "" {
		return errors.New("direction is required")
	}
	return nil
}

// CheckCompatibility is deliberately stricter than a display-only renderer:
// processing results may be compared numerically only on the same sample and
// geometry axes. It never silently truncates or resamples either input.
func CheckCompatibility(a, b Metadata) error {
	if err := a.Validate(); err != nil {
		return fmt.Errorf("A metadata: %w", err)
	}
	if err := b.Validate(); err != nil {
		return fmt.Errorf("B metadata: %w", err)
	}
	if a.TraceCount != b.TraceCount {
		return errors.New("trace count is incompatible")
	}
	if a.SamplesPerTrace != b.SamplesPerTrace {
		return errors.New("sample count is incompatible")
	}
	if a.SampleIntervalUS != b.SampleIntervalUS {
		return errors.New("sample interval is incompatible")
	}
	if a.TimeOriginUS != b.TimeOriginUS {
		return errors.New("time origin is incompatible")
	}
	if a.Direction != b.Direction || a.InlineCount != b.InlineCount || a.CrosslineCount != b.CrosslineCount {
		return errors.New("geometry direction or dimensions are incompatible")
	}
	return nil
}

// SampleWindow uses a half-open [First, Last) range in the source record.
type SampleWindow struct{ First, Last int }

func (w SampleWindow) Validate(m Metadata) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if w.First < 0 || w.Last <= w.First || w.Last > m.SamplesPerTrace {
		return errors.New("sample window lies outside the record")
	}
	return nil
}

func CheckWindowCompatibility(a, b Metadata, aw, bw SampleWindow) error {
	if err := CheckCompatibility(a, b); err != nil {
		return err
	}
	if err := aw.Validate(a); err != nil {
		return fmt.Errorf("A: %w", err)
	}
	if err := bw.Validate(b); err != nil {
		return fmt.Errorf("B: %w", err)
	}
	if aw != bw {
		return errors.New("sample windows are incompatible")
	}
	return nil
}

type Input struct {
	Metadata Metadata
	Samples  [][]float32
}

func (in Input) Validate() error {
	if err := in.Metadata.Validate(); err != nil {
		return err
	}
	if len(in.Samples) != in.Metadata.TraceCount {
		return fmt.Errorf("trace count mismatch: got %d want %d", len(in.Samples), in.Metadata.TraceCount)
	}
	for i, trace := range in.Samples {
		if len(trace) != in.Metadata.SamplesPerTrace {
			return fmt.Errorf("sample count mismatch at trace %d: got %d want %d", i, len(trace), in.Metadata.SamplesPerTrace)
		}
		for j, v := range trace {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return fmt.Errorf("non-finite sample at trace %d sample %d", i, j)
			}
		}
	}
	return nil
}

func (in Input) Clone() Input {
	out := Input{Metadata: in.Metadata, Samples: make([][]float32, len(in.Samples))}
	for i := range in.Samples {
		out.Samples[i] = append([]float32(nil), in.Samples[i]...)
	}
	return out
}

type Result struct {
	Metadata   Metadata
	Samples    [][]float32
	Algorithm  string
	Parameters Params
}

func (r Result) Validate() error {
	return (Input{Metadata: r.Metadata, Samples: r.Samples}).Validate()
}

func (r Result) Clone() Result {
	return Result{Metadata: r.Metadata, Samples: (Input{Metadata: r.Metadata, Samples: r.Samples}).Clone().Samples, Algorithm: r.Algorithm, Parameters: r.Parameters.Clone()}
}

type Params struct {
	Values  map[string]float64
	Strings map[string]string
}

func (p Params) Float(id string, fallback float64) float64 {
	if v, ok := p.Values[id]; ok {
		return v
	}
	return fallback
}

func (p Params) String(id, fallback string) string {
	if v, ok := p.Strings[id]; ok {
		return v
	}
	return fallback
}

func (p Params) Clone() Params {
	q := Params{Values: make(map[string]float64, len(p.Values)), Strings: make(map[string]string, len(p.Strings))}
	for k, v := range p.Values {
		q.Values[k] = v
	}
	for k, v := range p.Strings {
		q.Strings[k] = v
	}
	return q
}

type Progress struct {
	Percent int
	Message string
}

type ParameterSpec struct {
	ID, Label, Unit string
	Default         float64
	Min, Max, Step  float64
	Integer         bool
}

type AlgorithmSpec struct {
	ID        string
	Label     string
	Available bool
	Reason    string
	Params    []ParameterSpec
}

type Algorithm interface {
	Spec() AlgorithmSpec
	Validate(Metadata, Params) error
	Run(context.Context, Input, Params, func(Progress)) (Result, error)
}

type Registry struct {
	mu    sync.RWMutex
	items map[string]Algorithm
}

// NewEmptyRegistry creates an isolated registry for an application or plugin
// host.  Call Register on it to add only the algorithms that should be
// exposed by that build.  This is useful for public drawing-only builds that
// intentionally do not ship private processing adapters.
func NewEmptyRegistry() *Registry {
	return &Registry{items: make(map[string]Algorithm)}
}

func NewRegistry() *Registry {
	r := NewEmptyRegistry()
	RegisterBuiltinAlgorithms(r)
	return r
}

// RegisterBuiltinAlgorithms retains only the legacy Copy/Gain test and
// compatibility operations. Installed external modules use the separate
// manifest registry and runner; no private algorithm stub belongs here.
func RegisterBuiltinAlgorithms(r *Registry) {
	if r == nil {
		return
	}
	_ = r.Register(copyAlgorithm{})
	_ = r.Register(gainAlgorithm{})
}

func (r *Registry) Register(a Algorithm) error {
	if a == nil || a.Spec().ID == "" {
		return errors.New("algorithm and ID are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[a.Spec().ID]; exists {
		return fmt.Errorf("algorithm %q already registered", a.Spec().ID)
	}
	r.items[a.Spec().ID] = a
	return nil
}

func (r *Registry) Get(id string) (Algorithm, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.items[id]
	return a, ok
}

func (r *Registry) Specs() []AlgorithmSpec {
	r.mu.RLock()
	out := make([]AlgorithmSpec, 0, len(r.items))
	for _, a := range r.items {
		spec := a.Spec()
		spec.Params = append([]ParameterSpec(nil), spec.Params...)
		out = append(out, spec)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type copyAlgorithm struct{}

func (copyAlgorithm) Spec() AlgorithmSpec {
	return AlgorithmSpec{ID: "copy", Label: "Copy / Identity", Available: true}
}

func (copyAlgorithm) Validate(m Metadata, _ Params) error { return m.Validate() }

func (copyAlgorithm) Run(ctx context.Context, in Input, p Params, report func(Progress)) (Result, error) {
	if err := checkContext(ctx); err != nil {
		return Result{}, err
	}
	if err := in.Validate(); err != nil {
		return Result{}, err
	}
	if err := checkContext(ctx); err != nil {
		return Result{}, err
	}
	if report != nil {
		report(Progress{Percent: 0, Message: "copying"})
	}
	r := Result{Metadata: in.Metadata, Samples: make([][]float32, len(in.Samples)), Algorithm: "copy", Parameters: p.Clone()}
	for i, samples := range in.Samples {
		if err := checkContext(ctx); err != nil {
			return Result{}, err
		}
		r.Samples[i] = append([]float32(nil), samples...)
	}
	if report != nil {
		report(Progress{Percent: 100, Message: "complete"})
	}
	if err := checkContext(ctx); err != nil {
		return Result{}, err
	}
	return r, nil
}

type gainAlgorithm struct{}

func (gainAlgorithm) Spec() AlgorithmSpec {
	return AlgorithmSpec{ID: "gain", Label: "Gain", Available: true, Params: []ParameterSpec{{ID: "factor", Label: "Gain factor", Default: 1, Min: 0, Max: 10, Step: 0.1}}}
}

func (gainAlgorithm) Validate(m Metadata, p Params) error {
	if err := m.Validate(); err != nil {
		return err
	}
	f := p.Float("factor", 1)
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 10 {
		return errors.New("gain factor must be finite and in 0..10")
	}
	return nil
}

func (gainAlgorithm) Run(ctx context.Context, in Input, p Params, report func(Progress)) (Result, error) {
	if err := checkContext(ctx); err != nil {
		return Result{}, err
	}
	if err := in.Validate(); err != nil {
		return Result{}, err
	}
	if err := (gainAlgorithm{}).Validate(in.Metadata, p); err != nil {
		return Result{}, err
	}
	f := p.Float("factor", 1)
	out := Input{Metadata: in.Metadata, Samples: make([][]float32, len(in.Samples))}
	for i, trace := range in.Samples {
		if err := checkContext(ctx); err != nil {
			return Result{}, err
		}
		out.Samples[i] = make([]float32, len(trace))
		for j, v := range trace {
			if j%4096 == 0 {
				if err := checkContext(ctx); err != nil {
					return Result{}, err
				}
			}
			value := float64(v) * f
			if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > math.MaxFloat32 {
				return Result{}, fmt.Errorf("gain result overflows float32 at trace %d sample %d", i, j)
			}
			out.Samples[i][j] = float32(value)
		}
		if report != nil {
			report(Progress{Percent: (i + 1) * 100 / len(in.Samples), Message: "applying gain"})
		}
	}
	if err := checkContext(ctx); err != nil {
		return Result{}, err
	}
	return Result{Metadata: out.Metadata, Samples: out.Samples, Algorithm: "gain", Parameters: p.Clone()}, nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
