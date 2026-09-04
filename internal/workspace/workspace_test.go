package workspace

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/project"
)

type fakeWorkspace struct {
	kind       Kind
	openErr    error
	showErr    error
	hideErr    error
	closeErr   error
	openCount  int
	showCount  int
	hideCount  int
	closeCount int
	last       OpenRequest
}

type fakeProjectWorkspace struct {
	fakeWorkspace
	projectErr   error
	projectCount int
	lastProject  ProjectOpenRequest
	emptyErr     error
	emptyCount   int
	lastEmpty    EmptyOpenRequest
}

func (w *fakeProjectWorkspace) OpenProject(request ProjectOpenRequest) error {
	w.projectCount++
	w.lastProject = request
	return w.projectErr
}

func (w *fakeProjectWorkspace) OpenEmpty(request EmptyOpenRequest) error {
	w.emptyCount++
	w.lastEmpty = request
	return w.emptyErr
}

func (w *fakeWorkspace) Kind() Kind { return w.kind }
func (w *fakeWorkspace) Open(request OpenRequest) error {
	w.openCount++
	w.last = request
	return w.openErr
}
func (w *fakeWorkspace) Show() error {
	w.showCount++
	return w.showErr
}
func (w *fakeWorkspace) Hide() error {
	w.hideCount++
	return w.hideErr
}
func (w *fakeWorkspace) Close() error {
	w.closeCount++
	return w.closeErr
}

func testDataset() *dataset.SeismicDataset {
	return &dataset.SeismicDataset{Path: filepath.Join("C:\\", "data", "cube.sgy"), Metadata: dataset.Metadata{TraceCount: 100, SamplesPerTrace: 50}}
}

func TestRegisterAndAtomicFailureRestore(t *testing.T) {
	manager := NewManager(nil)
	line := &fakeWorkspace{kind: Kind2D}
	volume := &fakeWorkspace{kind: Kind3D}
	if err := manager.Register(line); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(volume); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(&fakeWorkspace{kind: Kind2D}); err == nil {
		t.Fatal("duplicate registration was accepted")
	}
	if err := manager.Open(Kind2D, testDataset()); err != nil {
		t.Fatal(err)
	}
	lineGeneration := manager.Generation()
	volume.openErr = errors.New("geometry failed synchronously")
	if err := manager.Open(Kind3D, testDataset()); err == nil {
		t.Fatal("expected target open failure")
	}
	if manager.ActiveKind() != Kind2D {
		t.Fatalf("failed switch did not preserve 2-D: %v", manager.ActiveKind())
	}
	if manager.Generation() != lineGeneration || !manager.IsCurrent(Kind2D, lineGeneration) {
		t.Fatal("failed switch invalidated the restored workspace generation")
	}
	if line.hideCount != 0 {
		t.Fatalf("current workspace was hidden before target accepted request: %d", line.hideCount)
	}
	if line.openCount != 1 || volume.openCount != 1 {
		t.Fatalf("unexpected cross-routing: 2-D opens=%d 3-D opens=%d", line.openCount, volume.openCount)
	}
}

func TestSetInitialHomeWorkspace(t *testing.T) {
	manager := NewManager(nil)
	home := &fakeWorkspace{kind: KindHome}
	if err := manager.Register(home); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetInitial(KindHome); err != nil {
		t.Fatal(err)
	}
	if manager.ActiveKind() != KindHome || home.openCount != 0 {
		t.Fatalf("initial home should not receive a data request: active=%v opens=%d", manager.ActiveKind(), home.openCount)
	}
}

func TestAdoptCompatibilityWorkspaceDoesNotCallOpen(t *testing.T) {
	manager := NewManager(nil)
	home := &fakeWorkspace{kind: KindHome}
	line := &fakeWorkspace{kind: Kind2D}
	_ = manager.Register(home)
	_ = manager.Register(line)
	_ = manager.SetInitial(KindHome)
	if err := manager.Adopt(Kind2D); err != nil {
		t.Fatal(err)
	}
	if manager.ActiveKind() != Kind2D || line.openCount != 0 {
		t.Fatalf("compatibility adoption invoked Open: active=%v opens=%d", manager.ActiveKind(), line.openCount)
	}
}

func TestSwitchCloseRestoresPrevious(t *testing.T) {
	manager := NewManager(nil)
	line := &fakeWorkspace{kind: Kind2D}
	volume := &fakeWorkspace{kind: Kind3D}
	_ = manager.Register(line)
	_ = manager.Register(volume)
	if err := manager.Open(Kind2D, testDataset()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Open(Kind3D, testDataset(), WithSampleRange(4, 20)); err != nil {
		t.Fatal(err)
	}
	if line.hideCount != 1 || volume.showCount != 1 {
		t.Fatalf("workspace switch not committed: line hide=%d volume show=%d", line.hideCount, volume.showCount)
	}
	if got := volume.last.EffectiveSampleRange(); got.Start != 4 || got.End != 20 {
		t.Fatalf("sample range not delivered: %+v", got)
	}
	if err := manager.CloseActive(); err != nil {
		t.Fatal(err)
	}
	if manager.ActiveKind() != Kind2D || line.showCount != 2 || volume.closeCount != 1 {
		t.Fatalf("previous workspace not restored: active=%v line shows=%d volume closes=%d", manager.ActiveKind(), line.showCount, volume.closeCount)
	}
}

func TestGenerationDiscardsExpiredAsyncResultAnd3DDoesNotOpen2D(t *testing.T) {
	manager := NewManager(nil)
	line := &fakeWorkspace{kind: Kind2D}
	volume := &fakeWorkspace{kind: Kind3D}
	_ = manager.Register(line)
	_ = manager.Register(volume)
	if err := manager.Open(Kind2D, testDataset()); err != nil {
		t.Fatal(err)
	}
	oldGeneration := manager.Generation()
	if err := manager.Open(Kind3D, testDataset()); err != nil {
		t.Fatal(err)
	}
	currentGeneration := manager.Generation()
	if manager.IsCurrent(Kind2D, oldGeneration) {
		t.Fatal("expired 2-D generation was accepted")
	}
	if !manager.IsCurrent(Kind3D, currentGeneration) {
		t.Fatal("current 3-D generation was rejected")
	}
	if line.openCount != 1 {
		t.Fatalf("opening 3-D triggered the 2-D Open path: %d", line.openCount)
	}
}

func TestInvalidRangeFailsBeforeWorkspaceMutation(t *testing.T) {
	manager := NewManager(nil)
	line := &fakeWorkspace{kind: Kind2D}
	_ = manager.Register(line)
	if err := manager.Open(Kind2D, testDataset(), WithSampleRange(0, 50)); err == nil {
		t.Fatal("invalid inclusive sample range was accepted")
	}
	if line.openCount != 0 || manager.ActiveKind() != KindHome {
		t.Fatalf("invalid request mutated workspace: opens=%d active=%v", line.openCount, manager.ActiveKind())
	}
}

func TestTraceContainsBasenameAndGeneration(t *testing.T) {
	var events []TraceEvent
	manager := NewManager(func(event TraceEvent) { events = append(events, event) })
	line := &fakeWorkspace{kind: Kind2D}
	_ = manager.Register(line)
	if err := manager.Open(Kind2D, testDataset()); err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 || events[0].Dataset != "cube.sgy" || events[0].Generation == 0 {
		t.Fatalf("unexpected trace events: %+v", events)
	}
}

func TestProjectSwitchIsAtomic(t *testing.T) {
	manager := NewManager(nil)
	line := &fakeWorkspace{kind: Kind2D}
	crooked := &fakeProjectWorkspace{fakeWorkspace: fakeWorkspace{kind: KindCrooked}}
	_ = manager.Register(line)
	_ = manager.Register(crooked)
	if err := manager.Open(Kind2D, testDataset()); err != nil {
		t.Fatal(err)
	}
	p, err := project.FromDataset(testDataset())
	if err != nil {
		t.Fatal(err)
	}
	crooked.projectErr = errors.New("project rejected")
	if err := manager.OpenProject(KindCrooked, p); err == nil {
		t.Fatal("project failure was not returned")
	}
	if manager.ActiveKind() != Kind2D || line.hideCount != 0 {
		t.Fatalf("failed project switch did not preserve 2-D: active=%v hides=%d", manager.ActiveKind(), line.hideCount)
	}
	crooked.projectErr = nil
	if err := manager.OpenProject(KindCrooked, p); err != nil {
		t.Fatal(err)
	}
	if manager.ActiveKind() != KindCrooked || crooked.projectCount != 2 || crooked.lastProject.Generation == 0 {
		t.Fatalf("project switch was not committed: active=%v count=%d request=%+v", manager.ActiveKind(), crooked.projectCount, crooked.lastProject)
	}
}

func TestEmptyWorkspaceSwitchAndFailureRestore(t *testing.T) {
	manager := NewManager(nil)
	line := &fakeWorkspace{kind: Kind2D}
	crooked := &fakeProjectWorkspace{fakeWorkspace: fakeWorkspace{kind: KindCrooked}}
	_ = manager.Register(line)
	_ = manager.Register(crooked)
	if err := manager.Open(Kind2D, testDataset()); err != nil {
		t.Fatal(err)
	}
	crooked.emptyErr = errors.New("shell rejected")
	if err := manager.OpenEmpty(KindCrooked); err == nil {
		t.Fatal("empty shell failure was not returned")
	}
	if manager.ActiveKind() != Kind2D || line.hideCount != 0 {
		t.Fatalf("failed empty switch changed the active workspace: active=%v hides=%d", manager.ActiveKind(), line.hideCount)
	}
	crooked.emptyErr = nil
	if err := manager.OpenEmpty(KindCrooked); err != nil {
		t.Fatal(err)
	}
	if manager.ActiveKind() != KindCrooked || crooked.emptyCount != 2 || crooked.lastEmpty.Generation == 0 {
		t.Fatalf("empty workspace was not committed: active=%v count=%d request=%+v", manager.ActiveKind(), crooked.emptyCount, crooked.lastEmpty)
	}
	if crooked.openCount != 0 || crooked.projectCount != 0 {
		t.Fatalf("empty route crossed data/project APIs: opens=%d projects=%d", crooked.openCount, crooked.projectCount)
	}
}
