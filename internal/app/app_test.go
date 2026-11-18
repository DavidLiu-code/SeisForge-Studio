package app

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/workspace"
)

type routeWorkspace struct {
	kind         workspace.Kind
	openCount    int
	last         workspace.OpenRequest
	projectCount int
	lastProject  workspace.ProjectOpenRequest
	emptyCount   int
	lastEmpty    workspace.EmptyOpenRequest
}

func (w *routeWorkspace) Kind() workspace.Kind { return w.kind }
func (w *routeWorkspace) Open(request workspace.OpenRequest) error {
	w.openCount++
	w.last = request
	return nil
}
func (w *routeWorkspace) Show() error  { return nil }
func (w *routeWorkspace) Hide() error  { return nil }
func (w *routeWorkspace) Close() error { return nil }
func (w *routeWorkspace) OpenProject(request workspace.ProjectOpenRequest) error {
	w.projectCount++
	w.lastProject = request
	return nil
}
func (w *routeWorkspace) OpenEmpty(request workspace.EmptyOpenRequest) error {
	w.emptyCount++
	w.lastEmpty = request
	return nil
}

func newRouteApplication(t *testing.T) (*Application, *routeWorkspace, *routeWorkspace, *routeWorkspace) {
	t.Helper()
	manager := workspace.NewManager(nil)
	line := &routeWorkspace{kind: workspace.Kind2D}
	volume := &routeWorkspace{kind: workspace.Kind3D}
	crooked := &routeWorkspace{kind: workspace.KindCrooked}
	for _, item := range []workspace.Workspace{line, volume, crooked} {
		if err := manager.Register(item); err != nil {
			t.Fatal(err)
		}
	}
	return New(dataset.NewManager(), manager, nil), line, volume, crooked
}

func TestExplicitRoutesUseOnlyRequestedWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cube.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 8, Cols: 8, Samples: 12, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	application, line, volume, crooked := newRouteApplication(t)
	data, err := application.OpenPath(workspace.Kind3D, path)
	if err != nil {
		t.Fatal(err)
	}
	if data == nil || volume.openCount != 1 || line.openCount != 0 || crooked.openCount != 0 {
		t.Fatalf("explicit 3-D route crossed adapters: line=%d volume=%d crooked=%d", line.openCount, volume.openCount, crooked.openCount)
	}
	if application.CurrentDataset() != data || volume.last.Dataset != data {
		t.Fatal("application did not retain the committed dataset")
	}
}

func TestAutoRouteUsesFrozenThresholds(t *testing.T) {
	dir := t.TempDir()
	regularPath := filepath.Join(dir, "regular.sgy")
	linePath := filepath.Join(dir, "line.sgy")
	crookedPath := filepath.Join(dir, "crooked.sgy")
	if err := testsegy.Write(regularPath, testsegy.Options{Rows: 8, Cols: 8, Samples: 4, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	if err := testsegy.Write(linePath, testsegy.Options{Rows: 8, Cols: 8, Samples: 4}); err != nil {
		t.Fatal(err)
	}
	coordinates := make([]testsegy.TraceCoordinate, 64)
	for i := range coordinates {
		coordinates[i] = testsegy.TraceCoordinate{X: int32(i * 100), Y: int32(i * i), CDP: int32(1000 + i)}
	}
	if err := testsegy.Write(crookedPath, testsegy.Options{Rows: 1, Cols: len(coordinates), Samples: 4, CoordinateScalar: 1, Coordinates: coordinates}); err != nil {
		t.Fatal(err)
	}
	application, line, volume, crooked := newRouteApplication(t)
	if _, err := application.OpenPath(workspace.KindAuto, regularPath); err != nil {
		t.Fatal(err)
	}
	if volume.openCount != 1 || line.openCount != 0 {
		t.Fatalf("regular auto route mismatch: line=%d volume=%d", line.openCount, volume.openCount)
	}
	if _, err := application.OpenPath(workspace.KindAuto, linePath); err != nil {
		t.Fatal(err)
	}
	if volume.openCount != 1 || line.openCount != 1 {
		t.Fatalf("line auto route mismatch: line=%d volume=%d", line.openCount, volume.openCount)
	}
	if _, err := application.OpenPath(workspace.KindAuto, crookedPath); err != nil {
		t.Fatal(err)
	}
	if crooked.openCount != 1 || volume.openCount != 1 || line.openCount != 1 {
		t.Fatalf("crooked auto route mismatch: line=%d volume=%d crooked=%d", line.openCount, volume.openCount, crooked.openCount)
	}
}

func TestInvalidDatasetNeverReachesWorkspace(t *testing.T) {
	application, line, volume, _ := newRouteApplication(t)
	if _, err := application.OpenPath(workspace.Kind3D, filepath.Join(t.TempDir(), "missing.sgy")); err == nil {
		t.Fatal("missing data unexpectedly opened")
	}
	if line.openCount != 0 || volume.openCount != 0 {
		t.Fatalf("invalid dataset reached a workspace: line=%d volume=%d", line.openCount, volume.openCount)
	}
}

func TestWorkspaceFailureDoesNotReplaceCurrentDataset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "line.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 8, Cols: 8, Samples: 4}); err != nil {
		t.Fatal(err)
	}
	manager := workspace.NewManager(nil)
	broken := &failingRouteWorkspace{kind: workspace.Kind2D}
	if err := manager.Register(broken); err != nil {
		t.Fatal(err)
	}
	application := New(dataset.NewManager(), manager, nil)
	if _, err := application.OpenPath(workspace.Kind2D, path); err == nil {
		t.Fatal("workspace failure was not returned")
	}
	if application.CurrentDataset() != nil {
		t.Fatal("failed route replaced current dataset")
	}
}

type failingRouteWorkspace struct{ kind workspace.Kind }

func (w *failingRouteWorkspace) Kind() workspace.Kind             { return w.kind }
func (w *failingRouteWorkspace) Open(workspace.OpenRequest) error { return errors.New("open failed") }
func (w *failingRouteWorkspace) Show() error                      { return nil }
func (w *failingRouteWorkspace) Hide() error                      { return nil }
func (w *failingRouteWorkspace) Close() error                     { return nil }

func TestCrookedFolderUsesProjectRoute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "line.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 8, Samples: 4}); err != nil {
		t.Fatal(err)
	}
	application, line, volume, crooked := newRouteApplication(t)
	p, err := application.OpenCrookedFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || crooked.projectCount != 1 || line.openCount != 0 || volume.openCount != 0 {
		t.Fatalf("project route crossed adapters: project=%v crooked=%d line=%d volume=%d", p != nil, crooked.projectCount, line.openCount, volume.openCount)
	}
	if application.CurrentProject() != p || application.CurrentDataset() == nil {
		t.Fatal("committed project/current active dataset was not retained")
	}
	var _ *projectcore.CrookedProject = p
}

func TestOpenWorkspaceActivatesEmptyCrookedWithoutDataset(t *testing.T) {
	application, line, volume, crooked := newRouteApplication(t)
	if err := application.OpenWorkspace(workspace.KindCrooked); err != nil {
		t.Fatal(err)
	}
	if crooked.emptyCount != 1 || crooked.openCount != 0 || crooked.projectCount != 0 {
		t.Fatalf("empty route mismatch: empty=%d open=%d project=%d", crooked.emptyCount, crooked.openCount, crooked.projectCount)
	}
	if line.openCount != 0 || volume.openCount != 0 || application.CurrentDataset() != nil || application.CurrentProject() != nil {
		t.Fatal("empty Crooked activation retained or routed seismic data")
	}
}
