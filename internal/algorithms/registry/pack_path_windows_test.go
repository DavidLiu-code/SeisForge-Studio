package registry

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWindowsShortNamePackageRootHasCanonicalIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "SeisForge algorithm modules")
	marker := makePack(t, filepath.Join(root, "Module package with space"))
	makeModule(t, filepath.Join(root, "Standalone module with space"), testDescriptor("test.legacy"))
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	name, err := syscall.UTF16PtrFromString(canonical)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 32768)
	count, err := syscall.GetShortPathName(name, &buf[0], uint32(len(buf)))
	if err != nil || count == 0 || count >= uint32(len(buf)) {
		t.Skipf("volume did not provide a short-name alias: %v", err)
	}
	short := syscall.UTF16ToString(buf[:count])
	if equalPackPath(short, canonical) {
		t.Skip("volume does not assign short names")
	}
	wantMarker, err := filepath.EvalSymlinks(marker)
	if err != nil {
		t.Fatal(err)
	}
	r := New("1.10.4")
	ctx := context.Background()
	issues, err := r.Scan(ctx, []string{short})
	if err != nil || len(issues) != 0 || len(r.Modules()) != 3 {
		t.Fatalf("short-name spelling rejected: issues %+v, error %v", issues, err)
	}
	before := r.Modules()
	for _, m := range before {
		if err := m.Verify(); err != nil {
			t.Fatal(err)
		}
		if m.PackManifestPath() != "" && m.PackManifestPath() != wantMarker {
			t.Fatal("package identity was not canonical")
		}
	}
	issues, err = r.Scan(ctx, []string{short, canonical})
	after := r.Modules()
	if err != nil || len(issues) != 0 || len(after) != len(before) {
		t.Fatalf("aliases produced duplicate registrations: issues %+v, error %v", issues, err)
	}
	for i, m := range before {
		if after[i].Fingerprint() != m.Fingerprint() {
			t.Fatalf("fingerprint depended on directory spelling for %s", m.Descriptor().ID)
		}
	}
}
