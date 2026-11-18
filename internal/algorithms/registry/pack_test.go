package registry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
)

func writePackMember(t *testing.T, root, name string, d api.AlgorithmDescriptor) {
	t.Helper()
	payload, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, name), payload, 0600); err != nil {
		t.Fatal(err)
	}
}

func writePack(t *testing.T, root string, names ...string) string {
	t.Helper()
	marker, err := json.Marshal(packManifest{Schema: PackSchema, ID: "test.pack", Version: "1.2.3", Manifests: names})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, PackManifestName)
	if err = os.WriteFile(path, marker, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func makePack(t *testing.T, path string) string {
	t.Helper()
	marker := writePack(t, path, "second.json", "first.json")
	if err := os.WriteFile(filepath.Join(path, "worker.exe"), []byte("this discovery must not execute"), 0600); err != nil {
		t.Fatal(err)
	}
	writePackMember(t, path, "first.json", testDescriptor("pack.first"))
	writePackMember(t, path, "second.json", testDescriptor("pack.second"))
	writePackMember(t, path, "not-listed.json", testDescriptor("pack.not-listed"))
	return marker
}

func TestPackDiscoveryReadsBoundedMembersAlongsideLegacy(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	packRoot := filepath.Join(root, "强背景 pack")
	marker := makePack(t, packRoot)
	makeModule(t, filepath.Join(root, "unrelated"), testDescriptor("unrelated.module"))
	makePack(t, filepath.Join(root, "unmarked subdirectory", "not direct"))
	r := New("1.10.4")
	if issues, err := r.Scan(ctx, []string{root, packRoot}); err != nil || len(issues) != 0 || len(r.Modules()) != 3 {
		t.Fatalf("expected two pack members and a legacy module, without duplicates: %v %v %v", r.Modules(), issues, err)
	}
	first, ok := r.Get("pack.first")
	second, _ := r.Get("pack.second")
	legacy, _ := r.Get("unrelated.module")
	wantMarker, err := filepath.EvalSymlinks(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || first.PackID() != "test.pack" || first.PackVersion() != "1.2.3" || first.PackManifestPath() != wantMarker ||
		first.ExecutablePath() != second.ExecutablePath() || first.Fingerprint() == second.Fingerprint() ||
		first.Verify() != nil || second.Verify() != nil || legacy.PackManifestPath() != "" {
		t.Fatalf("unexpected package/member identities: %v, %v, %v", first, second, legacy)
	}
	writePack(t, packRoot, "first.json", "second.json") // any marker change invalidates member trust
	if first.Verify() == nil {
		t.Fatal("changed package marker retained authorization")
	}
	if issues, err := r.Scan(ctx, []string{packRoot}); err != nil || len(issues) != 0 || len(r.Modules()) != 2 {
		t.Fatalf("explicit package directory was not independently discovered: %+v %v", issues, err)
	}
	reloaded, _ := r.Get("pack.first")
	if reloaded.Fingerprint() == first.Fingerprint() || reloaded.Verify() != nil {
		t.Fatal("reconfigured marker did not create a fresh verified identity")
	}
	if err := os.WriteFile(reloaded.ManifestPath(), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if reloaded.Verify() == nil {
		t.Fatal("changing one member did not invalidate its trust")
	}
}

func TestPackRejectsInvalidPathsAndIncompleteMembership(t *testing.T) {
	for _, name := range []string{"../outside.json", "nested/member.json", `nested\member.json`, `/outside.json`, `C:outside.json`, "worker.exe", PackManifestName, "manifest.json", "FIRST.json", "missing.json"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			makePack(t, root)
			writePack(t, root, "first.json", name)
			r := New("1.10.4")
			if issues, err := r.Scan(context.Background(), []string{root}); err != nil || len(issues) != 1 || len(r.Modules()) != 0 {
				t.Fatalf("invalid list made a partial package selectable: %+v, %v", issues, err)
			}
		})
	}
	root := t.TempDir()
	marker := makePack(t, root)
	writePackMember(t, root, "second.json", testDescriptor("pack.first"))
	if out, err := LoadPackManifest(context.Background(), marker, "1.10.4"); err == nil || out != nil {
		t.Fatal("duplicate method ID accepted")
	}
	d := testDescriptor("pack.second")
	d.Executable = "another.exe"
	if err := os.WriteFile(filepath.Join(root, d.Executable), []byte("different executable"), 0600); err != nil {
		t.Fatal(err)
	}
	writePackMember(t, root, "second.json", d)
	if out, err := LoadPackManifest(context.Background(), marker, "1.10.4"); err == nil || out != nil {
		t.Fatal("package without a shared executable accepted")
	}
}

func TestPackMarkerIsStrictAndSizeBounded(t *testing.T) {
	root := t.TempDir()
	path := makePack(t, root)
	const base = `{"schema":"seisforge.algorithm-pack.v1","id":"test.pack","version":"1.0.0","manifests":["first.json"]}`
	for _, payload := range []string{base + ` {}`, strings.Replace(base, `"id":`, `"id":"duplicate","id":`, 1),
		strings.Replace(base, `"id":`, `"unknown_field":1,"id":`, 1), strings.Replace(base, `"first.json"`, "", 1),
		strings.Repeat(" ", maxPackManifestBytes) + base, strings.Replace(base, "pack.v1", "pack.v2", 1),
		strings.Replace(base, `"first.json"`, strings.Repeat(`"first.json",`, maxPackMembers)+`"second.json"`, 1)} {
		if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
		if modules, err := LoadPackManifest(context.Background(), path, "1.10.4"); err == nil || len(modules) != 0 {
			t.Fatalf("invalid marker of %d bytes accepted", len(payload))
		}
	}
}

func TestPackRejectsSymlinkedMemberEvenInsidePackageDirectory(t *testing.T) {
	root := t.TempDir()
	marker := makePack(t, root)
	if err := os.Symlink(filepath.Join(root, "second.json"), filepath.Join(root, "linked.json")); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	writePack(t, root, "first.json", "linked.json")
	if modules, err := LoadPackManifest(context.Background(), marker, "1.10.4"); err == nil || len(modules) != 0 {
		t.Fatal("symlinked package member accepted")
	}
}

func TestPackRejectsSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	makePack(t, filepath.Join(realDir, "pack"))
	link := filepath.Join(root, "linked")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if modules, err := LoadPackManifest(context.Background(), filepath.Join(link, "pack", PackManifestName), "1.10.4"); err == nil || len(modules) != 0 {
		t.Fatal("package below a symlinked ancestor was accepted")
	}
}

func TestPackAndLegacyDuplicateIDStillFailClosed(t *testing.T) {
	root := t.TempDir()
	makePack(t, filepath.Join(root, "pack"))
	makeModule(t, filepath.Join(root, "legacy"), testDescriptor("pack.second"))
	r := New("1.10.4")
	issues, err := r.Scan(context.Background(), []string{root})
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0].Message, "duplicate algorithm ID") || len(r.Modules()) != 1 {
		t.Fatalf("duplicate ID did not fail closed independently of other pack members: %v %v", issues, err)
	}
	if _, ok := r.Get("pack.second"); ok {
		t.Fatal("duplicate ID became executable")
	}
}
