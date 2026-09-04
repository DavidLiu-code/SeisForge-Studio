// Package app is the single path-to-workspace router used by Home, Recent,
// drag/drop and workspace-to-workspace commands.
package app

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/workspace"
)

const AutoDetectHeaders = 3000

type Application struct {
	Data       *dataset.Manager
	Workspaces *workspace.Manager
	trace      workspace.TraceFunc

	mu      sync.RWMutex
	current *dataset.SeismicDataset
	project *project.CrookedProject
}

func New(data *dataset.Manager, workspaces *workspace.Manager, trace workspace.TraceFunc) *Application {
	if data == nil {
		data = dataset.NewManager()
	}
	if workspaces == nil {
		workspaces = workspace.NewManager(trace)
	}
	return &Application{Data: data, Workspaces: workspaces, trace: trace}
}

func NewDefault(trace workspace.TraceFunc) *Application {
	return New(dataset.NewManager(), workspace.NewManager(trace), trace)
}

// OpenPath is the only path-based route. KindAuto applies the unchanged
// score>=75 and confidence>=0.58 recommendation thresholds before dispatch.
func (a *Application) OpenPath(kind workspace.Kind, path string, options ...workspace.OpenOption) (*dataset.SeismicDataset, error) {
	data, err := a.Data.Open(path)
	if err != nil {
		a.emitFailure("data_open_failed", kind, path, nil, err)
		return nil, err
	}
	return data, a.OpenDataset(kind, data, options...)
}

func (a *Application) OpenDataset(kind workspace.Kind, data *dataset.SeismicDataset, options ...workspace.OpenOption) error {
	if data == nil {
		return fmt.Errorf("cannot route a nil dataset")
	}
	resolved := kind
	if kind == workspace.KindAuto {
		reader, err := data.OpenReader()
		if err != nil {
			a.emitFailure("geometry_probe_failed", kind, data.Path, data, err)
			return err
		}
		detection, detectErr := geometry.Detect(reader, AutoDetectHeaders)
		closeErr := reader.Close()
		if detectErr != nil {
			a.emitFailure("geometry_probe_failed", kind, data.Path, data, detectErr)
			return detectErr
		}
		if closeErr != nil {
			a.emitFailure("geometry_probe_failed", kind, data.Path, data, closeErr)
			return closeErr
		}
		switch detection.Kind {
		case geometry.KindRegular3D:
			resolved = workspace.Kind3D
		case geometry.KindCrookedLine:
			resolved = workspace.KindCrooked
		default:
			resolved = workspace.Kind2D
			data.SetGeometry(geometry.NewLine2D(data.Metadata.TraceCount))
		}
		a.emit(workspace.TraceEvent{Action: "geometry_probe", Workspace: resolved, Dataset: data.Basename(), Geometry: detection.Kind, GeometryScore: detection.Score, GeometryConfidence: detection.Confidence, Generation: a.Workspaces.Generation() + 1})
	}
	if err := a.Workspaces.Open(resolved, data, options...); err != nil {
		return err
	}
	a.mu.Lock()
	a.current = data
	a.project = nil
	a.mu.Unlock()
	return nil
}

// OpenWorkspace activates a workspace before a dataset or project is chosen.
// It is used by the Home Crooked card so file selection happens inside the
// dedicated workspace rather than in the Home router.
func (a *Application) OpenWorkspace(kind workspace.Kind) error {
	if err := a.Workspaces.OpenEmpty(kind); err != nil {
		return err
	}
	a.mu.Lock()
	a.current = nil
	a.project = nil
	a.mu.Unlock()
	return nil
}

// OpenCrookedFolder and OpenCrookedPaths are the only collection-based
// routes. They validate metadata before asking the workspace manager to make
// an atomic switch.
func (a *Application) OpenCrookedFolder(path string) (*project.CrookedProject, error) {
	p, err := project.OpenFolder(a.Data, path)
	if err != nil {
		a.emitFailure("project_data_open_failed", workspace.KindCrooked, path, nil, err)
		return nil, err
	}
	return p, a.OpenCrookedProject(p)
}

func (a *Application) OpenCrookedPaths(paths []string) (*project.CrookedProject, error) {
	p, err := project.OpenPaths(a.Data, paths)
	if err != nil {
		path := ""
		if len(paths) > 0 {
			path = paths[0]
		}
		a.emitFailure("project_data_open_failed", workspace.KindCrooked, path, nil, err)
		return nil, err
	}
	return p, a.OpenCrookedProject(p)
}

func (a *Application) OpenCrookedProject(p *project.CrookedProject) error {
	if p == nil {
		return fmt.Errorf("cannot route a nil project")
	}
	if err := a.Workspaces.OpenProject(workspace.KindCrooked, p); err != nil {
		return err
	}
	a.mu.Lock()
	a.project = p
	a.current = nil
	for _, line := range p.Lines {
		if line.Valid() {
			a.current = line.Dataset
			break
		}
	}
	a.mu.Unlock()
	return nil
}

func (a *Application) CurrentDataset() *dataset.SeismicDataset {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.current
}

func (a *Application) CurrentProject() *project.CrookedProject {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.project
}

// AdoptDataset is the narrow bridge for the legacy range-load dialog. New
// path entry points must use OpenPath instead.
func (a *Application) AdoptDataset(kind workspace.Kind, data *dataset.SeismicDataset) error {
	if data == nil {
		return fmt.Errorf("cannot adopt a nil dataset")
	}
	if err := a.Workspaces.Adopt(kind); err != nil {
		return err
	}
	a.mu.Lock()
	a.current = data
	a.project = nil
	a.mu.Unlock()
	return nil
}

func (a *Application) emitFailure(action string, kind workspace.Kind, path string, data *dataset.SeismicDataset, err error) {
	if a.trace == nil {
		return
	}
	geometryKind := geometry.KindUnknown
	if data != nil && data.Geometry() != nil {
		geometryKind = data.Geometry().Kind()
	}
	a.emit(workspace.TraceEvent{Action: action, Workspace: kind, Dataset: filepath.Base(path), Geometry: geometryKind, Generation: a.Workspaces.Generation(), Err: err})
}

func (a *Application) emit(event workspace.TraceEvent) {
	if a.trace != nil {
		a.trace(event)
	}
}
