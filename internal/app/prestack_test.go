package app

import (
	"path/filepath"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/workspace"
)

func TestExplicitPrestackDoesNotEnterPoststackAdapters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prestack.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 3, Cols: 8, Samples: 12, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	a, line, volume, crooked := newRouteApplication(t)
	prestack := &routeWorkspace{kind: workspace.KindPrestack}
	if err := a.Workspaces.Register(prestack); err != nil {
		t.Fatal(err)
	}
	data, err := a.OpenPath(workspace.KindPrestack, path)
	if err != nil {
		t.Fatal(err)
	}
	if prestack.openCount != 1 || prestack.last.Dataset != data || a.CurrentDataset() != data {
		t.Fatal("explicit Prestack route did not retain the requested dataset")
	}
	if line.openCount != 0 || volume.openCount != 0 || crooked.openCount != 0 {
		t.Fatal("explicit Prestack route entered a post-stack adapter")
	}
	if data.Geometry() != nil {
		t.Fatal("explicit Prestack route unexpectedly ran post-stack geometry detection")
	}
}

func TestAutoRouteNeverSelectsPrestack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ordinary.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 1, Cols: 8, Samples: 12}); err != nil {
		t.Fatal(err)
	}
	a, line, _, _ := newRouteApplication(t)
	prestack := &routeWorkspace{kind: workspace.KindPrestack}
	if err := a.Workspaces.Register(prestack); err != nil {
		t.Fatal(err)
	}
	if _, err := a.OpenPath(workspace.KindAuto, path); err != nil {
		t.Fatal(err)
	}
	if prestack.openCount != 0 || line.openCount != 1 {
		t.Fatal("adding the explicit Prestack workspace changed automatic routing")
	}
}
