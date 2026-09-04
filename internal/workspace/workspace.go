// Package workspace coordinates display workspaces without depending on
// Win32. UI-specific behavior is supplied by adapters in cmd/limage.
package workspace

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/project"
)

// Kind values 1/2/3 intentionally retain the existing recent.json numeric
// representation used by Limage 1.8.7.
type Kind uint8

const (
	KindHome Kind = iota
	Kind2D
	Kind3D
	KindCrooked
	KindAuto Kind = 255
)

func (k Kind) String() string {
	switch k {
	case KindHome:
		return "Home"
	case Kind2D:
		return "Line2D"
	case Kind3D:
		return "Volume3D"
	case KindCrooked:
		return "Crooked"
	case KindAuto:
		return "Auto"
	default:
		return fmt.Sprintf("Workspace(%d)", k)
	}
}

type Range struct {
	Start int64
	End   int64
}

func (r Range) validate(name string, maximum int64) error {
	if maximum <= 0 || r.Start < 0 || r.End < r.Start || r.End >= maximum {
		return fmt.Errorf("%s range [%d,%d] is outside [0,%d]", name, r.Start, r.End, maximum-1)
	}
	return nil
}

type OpenRequest struct {
	Dataset     *dataset.SeismicDataset
	TraceRange  *Range
	SampleRange *Range
	Generation  uint64
}

func (r OpenRequest) Validate() error {
	if r.Dataset == nil {
		return errors.New("workspace request has no dataset")
	}
	if r.TraceRange != nil {
		if err := r.TraceRange.validate("trace", r.Dataset.Metadata.TraceCount); err != nil {
			return err
		}
	}
	if r.SampleRange != nil {
		if err := r.SampleRange.validate("sample", int64(r.Dataset.Metadata.SamplesPerTrace)); err != nil {
			return err
		}
	}
	return nil
}

func (r OpenRequest) EffectiveTraceRange() Range {
	if r.TraceRange != nil {
		return *r.TraceRange
	}
	return Range{Start: 0, End: r.Dataset.Metadata.TraceCount - 1}
}

func (r OpenRequest) EffectiveSampleRange() Range {
	if r.SampleRange != nil {
		return *r.SampleRange
	}
	return Range{Start: 0, End: int64(r.Dataset.Metadata.SamplesPerTrace - 1)}
}

type OpenOption func(*OpenRequest)

func WithTraceRange(start, end int64) OpenOption {
	return func(r *OpenRequest) { r.TraceRange = &Range{Start: start, End: end} }
}

func WithSampleRange(start, end int) OpenOption {
	return func(r *OpenRequest) { r.SampleRange = &Range{Start: int64(start), End: int64(end)} }
}

type Workspace interface {
	Kind() Kind
	Open(OpenRequest) error
	Show() error
	Hide() error
	Close() error
}

// EmptyOpenRequest activates a workspace shell before any dataset or project
// has been selected. Generation follows the same stale-result rules as the
// data and project open requests.
type EmptyOpenRequest struct {
	Generation uint64
}

// EmptyWorkspace is optional so existing data-backed workspaces retain the
// original Workspace contract unchanged.
type EmptyWorkspace interface {
	Workspace
	OpenEmpty(EmptyOpenRequest) error
}

// ProjectOpenRequest is intentionally separate from OpenRequest so the
// original single-dataset Workspace contract remains source compatible.
type ProjectOpenRequest struct {
	Project    *project.CrookedProject
	Generation uint64
}

func (r ProjectOpenRequest) Validate() error {
	if r.Project == nil {
		return errors.New("workspace request has no project")
	}
	if r.Project.ValidLineCount() == 0 {
		return errors.New("workspace project has no valid datasets")
	}
	return nil
}

type ProjectWorkspace interface {
	Workspace
	OpenProject(ProjectOpenRequest) error
}

type TraceEvent struct {
	Action             string
	Workspace          Kind
	Previous           Kind
	Dataset            string
	Geometry           geometry.Kind
	GeometryScore      float64
	GeometryConfidence float64
	Generation         uint64
	ProjectLines       int
	Err                error
}

type TraceFunc func(TraceEvent)

type Manager struct {
	opMu sync.Mutex
	mu   sync.RWMutex

	registered map[Kind]Workspace
	active     Workspace
	history    []Workspace
	generation uint64
	trace      TraceFunc
}

func NewManager(trace TraceFunc) *Manager {
	return &Manager{registered: make(map[Kind]Workspace), trace: trace}
}

func (m *Manager) Register(w Workspace) error {
	if w == nil {
		return errors.New("cannot register a nil workspace")
	}
	kind := w.Kind()
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.registered[kind]; exists {
		return fmt.Errorf("workspace %s is already registered", kind)
	}
	m.registered[kind] = w
	return nil
}

// SetInitial records the already-created native workspace at application
// startup without fabricating a dataset OpenRequest.
func (m *Manager) SetInitial(kind Kind) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != nil {
		return fmt.Errorf("initial workspace is already set to %s", m.active.Kind())
	}
	target := m.registered[kind]
	if target == nil {
		return fmt.Errorf("workspace %s is not registered", kind)
	}
	m.active = target
	m.generation++
	return nil
}

// Adopt synchronizes a workspace opened by a temporary legacy compatibility
// API. It performs no UI calls and can be removed with that API in Phase 2.
func (m *Manager) Adopt(kind Kind) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	target := m.registered[kind]
	if target == nil {
		return fmt.Errorf("workspace %s is not registered", kind)
	}
	if m.active != nil && m.active != target {
		m.history = append(m.history, m.active)
	}
	m.active = target
	m.generation++
	return nil
}

func (m *Manager) Open(kind Kind, data *dataset.SeismicDataset, options ...OpenOption) error {
	req := OpenRequest{Dataset: data}
	for _, option := range options {
		if option != nil {
			option(&req)
		}
	}
	return m.OpenRequest(kind, req)
}

// OpenRequest performs a synchronous, atomic workspace switch. The current
// workspace stays visible until the target accepts the request; failures then
// leave it active and visible.
func (m *Manager) OpenRequest(kind Kind, req OpenRequest) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	target := m.registered[kind]
	previous := m.active
	req.Generation = m.generation + 1
	m.mu.Unlock()
	if target == nil {
		err := fmt.Errorf("workspace %s is not registered", kind)
		m.emit("open_failed", kind, kindOf(previous), req, err)
		return err
	}
	if err := req.Validate(); err != nil {
		m.emit("open_failed", kind, kindOf(previous), req, err)
		return err
	}
	m.emit("open_begin", kind, kindOf(previous), req, nil)
	if err := target.Open(req); err != nil {
		m.emit("open_failed", kind, kindOf(previous), req, err)
		return err
	}
	if target == previous {
		if err := target.Show(); err != nil {
			m.emit("open_failed", kind, kindOf(previous), req, err)
			return err
		}
		m.mu.Lock()
		m.generation = req.Generation
		m.mu.Unlock()
		m.emit("open_committed", kind, kind, req, nil)
		return nil
	}
	if previous != nil {
		if err := previous.Hide(); err != nil {
			_ = target.Close()
			m.emit("open_failed", kind, previous.Kind(), req, err)
			return err
		}
	}
	if err := target.Show(); err != nil {
		_ = target.Close()
		if previous != nil {
			_ = previous.Show()
		}
		m.emit("open_failed", kind, kindOf(previous), req, err)
		return err
	}

	m.mu.Lock()
	filtered := m.history[:0]
	for _, item := range m.history {
		if item != target {
			filtered = append(filtered, item)
		}
	}
	m.history = filtered
	if previous != nil {
		m.history = append(m.history, previous)
	}
	m.active = target
	m.generation = req.Generation
	m.mu.Unlock()
	m.emit("open_committed", kind, kindOf(previous), req, nil)
	return nil
}

// OpenProject performs the same atomic switch as OpenRequest while preserving
// the fixed Workspace.Open contract for all existing workspaces.
func (m *Manager) OpenProject(kind Kind, projectValue *project.CrookedProject) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	target := m.registered[kind]
	previous := m.active
	req := ProjectOpenRequest{Project: projectValue, Generation: m.generation + 1}
	m.mu.Unlock()
	projectTarget, ok := target.(ProjectWorkspace)
	if target == nil || !ok {
		err := fmt.Errorf("workspace %s does not accept projects", kind)
		m.emitProject("project_open_failed", kind, kindOf(previous), req, err)
		return err
	}
	if err := req.Validate(); err != nil {
		m.emitProject("project_open_failed", kind, kindOf(previous), req, err)
		return err
	}
	m.emitProject("project_open_begin", kind, kindOf(previous), req, nil)
	if err := projectTarget.OpenProject(req); err != nil {
		m.emitProject("project_open_failed", kind, kindOf(previous), req, err)
		return err
	}
	if target == previous {
		if err := target.Show(); err != nil {
			m.emitProject("project_open_failed", kind, kindOf(previous), req, err)
			return err
		}
		m.mu.Lock()
		m.generation = req.Generation
		m.mu.Unlock()
		m.emitProject("project_open_committed", kind, kind, req, nil)
		return nil
	}
	if previous != nil {
		if err := previous.Hide(); err != nil {
			_ = target.Close()
			m.emitProject("project_open_failed", kind, previous.Kind(), req, err)
			return err
		}
	}
	if err := target.Show(); err != nil {
		_ = target.Close()
		if previous != nil {
			_ = previous.Show()
		}
		m.emitProject("project_open_failed", kind, kindOf(previous), req, err)
		return err
	}

	m.mu.Lock()
	filtered := m.history[:0]
	for _, item := range m.history {
		if item != target {
			filtered = append(filtered, item)
		}
	}
	m.history = filtered
	if previous != nil {
		m.history = append(m.history, previous)
	}
	m.active = target
	m.generation = req.Generation
	m.mu.Unlock()
	m.emitProject("project_open_committed", kind, kindOf(previous), req, nil)
	return nil
}

// OpenEmpty atomically activates an empty workspace shell. The previous
// workspace remains visible until the target has accepted the request.
func (m *Manager) OpenEmpty(kind Kind) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	target := m.registered[kind]
	previous := m.active
	req := EmptyOpenRequest{Generation: m.generation + 1}
	m.mu.Unlock()
	emptyTarget, ok := target.(EmptyWorkspace)
	if target == nil || !ok {
		err := fmt.Errorf("workspace %s does not accept empty activation", kind)
		m.emitBare("empty_open_failed", kind, kindOf(previous), req.Generation, err)
		return err
	}
	m.emitBare("empty_open_begin", kind, kindOf(previous), req.Generation, nil)
	if err := emptyTarget.OpenEmpty(req); err != nil {
		m.emitBare("empty_open_failed", kind, kindOf(previous), req.Generation, err)
		return err
	}
	if target == previous {
		if err := target.Show(); err != nil {
			m.emitBare("empty_open_failed", kind, kindOf(previous), req.Generation, err)
			return err
		}
		m.mu.Lock()
		m.generation = req.Generation
		m.mu.Unlock()
		m.emitBare("empty_open_committed", kind, kind, req.Generation, nil)
		return nil
	}
	if previous != nil {
		if err := previous.Hide(); err != nil {
			_ = target.Close()
			m.emitBare("empty_open_failed", kind, previous.Kind(), req.Generation, err)
			return err
		}
	}
	if err := target.Show(); err != nil {
		_ = target.Close()
		if previous != nil {
			_ = previous.Show()
		}
		m.emitBare("empty_open_failed", kind, kindOf(previous), req.Generation, err)
		return err
	}

	m.mu.Lock()
	filtered := m.history[:0]
	for _, item := range m.history {
		if item != target {
			filtered = append(filtered, item)
		}
	}
	m.history = filtered
	if previous != nil {
		m.history = append(m.history, previous)
	}
	m.active = target
	m.generation = req.Generation
	m.mu.Unlock()
	m.emitBare("empty_open_committed", kind, kindOf(previous), req.Generation, nil)
	return nil
}

func (m *Manager) ActiveKind() Kind {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return kindOf(m.active)
}

func (m *Manager) Generation() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.generation
}

func (m *Manager) IsCurrent(kind Kind, generation uint64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.generation == generation && m.active != nil && m.active.Kind() == kind
}

func (m *Manager) CloseActive() error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.RLock()
	current := m.active
	var restore Workspace
	if n := len(m.history); n > 0 {
		restore = m.history[n-1]
	}
	m.mu.RUnlock()
	if current == nil {
		return nil
	}
	if err := current.Hide(); err != nil {
		return err
	}
	if restore != nil {
		if err := restore.Show(); err != nil {
			_ = current.Show()
			return err
		}
	}
	if err := current.Close(); err != nil {
		if restore != nil {
			_ = restore.Hide()
		}
		_ = current.Show()
		return err
	}
	m.mu.Lock()
	m.generation++
	if len(m.history) > 0 {
		m.history = m.history[:len(m.history)-1]
	}
	m.active = restore
	generation := m.generation
	m.mu.Unlock()
	m.emitBare("close_restore", kindOf(current), kindOf(restore), generation, nil)
	return nil
}

// NotifyClosed records a close initiated by the native window procedure. It
// does not call Close again, avoiding a DestroyWindow recursion.
func (m *Manager) NotifyClosed(kind Kind) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	if m.active == nil || m.active.Kind() != kind {
		m.mu.Unlock()
		return nil
	}
	var restore Workspace
	if len(m.history) > 0 {
		restore = m.history[len(m.history)-1]
		m.history = m.history[:len(m.history)-1]
	}
	m.generation++
	generation := m.generation
	m.active = restore
	m.mu.Unlock()
	if restore != nil {
		if err := restore.Show(); err != nil {
			m.emitBare("native_close_restore_failed", kind, restore.Kind(), generation, err)
			return err
		}
	}
	m.emitBare("native_close_restore", kind, kindOf(restore), generation, nil)
	return nil
}

func (m *Manager) emit(action string, kind, previous Kind, req OpenRequest, err error) {
	geometryKind := geometry.KindUnknown
	datasetName := ""
	if req.Dataset != nil {
		datasetName = req.Dataset.Basename()
		if g := req.Dataset.Geometry(); g != nil {
			geometryKind = g.Kind()
		}
	}
	m.emitEvent(TraceEvent{Action: action, Workspace: kind, Previous: previous, Dataset: datasetName, Geometry: geometryKind, Generation: req.Generation, Err: err})
}

func (m *Manager) emitBare(action string, kind, previous Kind, generation uint64, err error) {
	m.emitEvent(TraceEvent{Action: action, Workspace: kind, Previous: previous, Generation: generation, Err: err})
}

func (m *Manager) emitProject(action string, kind, previous Kind, req ProjectOpenRequest, err error) {
	name, count := "", 0
	if req.Project != nil {
		name = filepath.Base(req.Project.Root)
		count = len(req.Project.Lines)
		if name == "." || name == "" {
			name = "crooked-project"
		}
	}
	m.emitEvent(TraceEvent{Action: action, Workspace: kind, Previous: previous, Dataset: name, Geometry: geometry.KindCrookedLine, Generation: req.Generation, ProjectLines: count, Err: err})
}

func (m *Manager) emitEvent(event TraceEvent) {
	if m.trace != nil {
		m.trace(event)
	}
}

func kindOf(w Workspace) Kind {
	if w == nil {
		return KindHome
	}
	return w.Kind()
}
