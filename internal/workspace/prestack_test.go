package workspace

import "testing"

func TestPrestackKindPreservesExistingModeNumbers(t *testing.T) {
	if KindHome != 0 || Kind2D != 1 || Kind3D != 2 || KindCrooked != 3 || KindPrestack != 4 {
		t.Fatal("persisted workspace kind numbers changed")
	}
	if KindPrestack.String() != "Prestack" {
		t.Fatalf("unexpected Prestack label %q", KindPrestack.String())
	}
}

func TestPrestackSwitchAndCloseRestorePreviousWorkspace(t *testing.T) {
	manager := NewManager(nil)
	line := &fakeWorkspace{kind: Kind2D}
	prestack := &fakeWorkspace{kind: KindPrestack}
	for _, w := range []Workspace{line, prestack} {
		if err := manager.Register(w); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Open(Kind2D, testDataset()); err != nil {
		t.Fatal(err)
	}
	oldGeneration := manager.Generation()
	if err := manager.Open(KindPrestack, testDataset()); err != nil {
		t.Fatal(err)
	}
	if manager.ActiveKind() != KindPrestack || manager.IsCurrent(Kind2D, oldGeneration) || line.hideCount != 1 {
		t.Fatal("Prestack did not atomically replace the previous workspace")
	}
	if err := manager.CloseActive(); err != nil {
		t.Fatal(err)
	}
	if manager.ActiveKind() != Kind2D || line.showCount != 2 || prestack.closeCount != 1 {
		t.Fatal("closing Prestack did not restore the previous workspace")
	}
}
