package registry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
)

func testDescriptor(id string) api.AlgorithmDescriptor {
	return api.AlgorithmDescriptor{ID: id, Version: "1.0.0", Name: "Test", ProtocolVersion: api.ProtocolVersion, InputKinds: []string{"section2d"}, OutputKinds: []string{"section2d"}, Executable: "worker.exe", MinimumHostVersion: "1.10.4", Parameters: []api.ParameterDescriptor{{ID: "choice", Type: "enum", Options: []string{"one", "two"}, Default: "one"}}}
}
func makeModule(t *testing.T, root string, desc api.AlgorithmDescriptor) string {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "worker.exe"), []byte("not executable; discovery must not execute"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "manifest.json")
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestDiscoveryPassiveAbsentAndDefensive(t *testing.T) {
	root := t.TempDir()
	r := New("1.10.4")
	if issues, err := r.Scan(context.Background(), []string{filepath.Join(root, "missing")}); err != nil || len(issues) != 0 || len(r.Modules()) != 0 {
		t.Fatalf("missing install: %v %v", issues, err)
	}
	path := makeModule(t, filepath.Join(root, "z"), testDescriptor("demo.z"))
	makeModule(t, filepath.Join(root, "a"), testDescriptor("demo.a"))
	if issues, err := r.Scan(context.Background(), []string{root}); err != nil || len(issues) != 0 {
		t.Fatalf("scan: %v %v", issues, err)
	}
	mods := r.Modules()
	if len(mods) != 2 || mods[0].Descriptor().ID != "demo.a" {
		t.Fatal("unstable discovery ordering")
	}
	m, ok := r.Get("demo.z")
	if !ok || m.ManifestPath() != path || m.Fingerprint() == "" {
		t.Fatal("missing discovered identity")
	}
	d := m.Descriptor()
	d.Parameters[0].Options[0] = "mutated"
	if m.Descriptor().Parameters[0].Options[0] != "one" {
		t.Fatal("descriptor is not defensive")
	}
	if err := m.Verify(); err != nil {
		t.Fatal(err)
	}
	if issues, err := r.Scan(context.Background(), []string{root, filepath.Dir(path)}); err != nil || len(issues) != 0 || len(r.Modules()) != 2 {
		t.Fatalf("same module added explicitly twice: %v %v", issues, err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if m.Verify() == nil {
		t.Fatal("changed manifest retained authorization")
	}
}
func TestDiscoveryRejectsDuplicateInvalidAndUnknown(t *testing.T) {
	root := t.TempDir()
	makeModule(t, filepath.Join(root, "a"), testDescriptor("duplicate"))
	makeModule(t, filepath.Join(root, "b"), testDescriptor("duplicate"))
	bad := testDescriptor("future")
	bad.MinimumHostVersion = "99.0.0"
	makeModule(t, filepath.Join(root, "c"), bad)
	bad = testDescriptor("oldprotocol")
	bad.ProtocolVersion = "old"
	makeModule(t, filepath.Join(root, "d"), bad)
	r := New("1.10.4")
	issues, err := r.Scan(context.Background(), []string{root})
	if err != nil || len(issues) != 3 || len(r.Modules()) != 0 {
		t.Fatalf("rejected modules: %v %v %v", issues, err, r.Modules())
	}
	if _, ok := r.Get("unknown"); ok {
		t.Fatal("unknown ID fallback")
	}
}
func TestExecutableAndManifestCannotEscapeModule(t *testing.T) {
	root := t.TempDir()
	module := filepath.Join(root, "module")
	for _, exe := range []string{"../outside.exe", filepath.Join(root, "outside.exe"), "C:worker.exe"} {
		d := testDescriptor("escape")
		d.Executable = exe
		path := makeModule(t, module, d)
		if _, err := LoadManifest(path, "1.10.4"); err == nil {
			t.Fatalf("accepted executable %q", exe)
		}
	}
	out := filepath.Join(root, "outside.exe")
	if err := os.WriteFile(out, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	path := makeModule(t, module, testDescriptor("escape"))
	link := filepath.Join(module, "link.exe")
	if err := os.Symlink(out, link); err != nil {
		t.Log("symlink test skipped on host without symlink privilege:", err)
		return
	}
	d := testDescriptor("escape")
	d.Executable = "link.exe"
	data, _ := json.Marshal(d)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path, "1.10.4"); err == nil {
		t.Fatal("accepted symlink escape")
	}
}
func TestCancelledScanPreservesOldRegistry(t *testing.T) {
	root := t.TempDir()
	makeModule(t, root, testDescriptor("demo"))
	r := New("1.10.4")
	_, err := r.Scan(context.Background(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	before := r.Modules()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = r.Scan(ctx, nil); err == nil {
		t.Fatal("cancelled scan accepted")
	}
	if !reflect.DeepEqual(before, r.Modules()) {
		t.Fatal("cancel replaced registry")
	}
}
func TestDefaultModuleDirectoryNotSourceOrPATH(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	path, err := DefaultDirectory()
	if err != nil || !strings.HasSuffix(path, filepath.Join("Limage", "algorithms")) {
		t.Fatalf("%q %v", path, err)
	}
}

func TestBuiltinRegistrationSurvivesScanAndCannotBeShadowed(t *testing.T) {
	r := New("1.10.4")
	desc := testDescriptor("seisforge.bandpass.zero_phase")
	if err := r.RegisterBuiltin(desc); err != nil {
		t.Fatal(err)
	}
	desc.Parameters[0].Options[0] = "changed"
	m, ok := r.Get(desc.ID)
	if !ok || !m.IsBuiltin() || m.Verify() != nil || m.ExecutablePath() != "" || m.ManifestPath() != "" {
		t.Fatal("builtin requires external installation", m)
	}
	if m.Descriptor().Parameters[0].Options[0] != "one" {
		t.Fatal("builtin descriptor aliases caller")
	}
	fingerprint := m.Fingerprint()
	root := t.TempDir()
	makeModule(t, filepath.Join(root, "shadow"), testDescriptor(desc.ID))
	makeModule(t, filepath.Join(root, "other"), testDescriptor("example.public"))
	issues, err := r.Scan(context.Background(), []string{root})
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0].Message, "reserved") {
		t.Fatalf("shadow not reported: %v %v", issues, err)
	}
	m, ok = r.Get(desc.ID)
	if !ok || !m.IsBuiltin() || m.Fingerprint() != fingerprint || len(r.Modules()) != 2 {
		t.Fatal("external manifest replaced builtin")
	}
	if _, err := r.Scan(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(r.Modules()) != 1 {
		t.Fatal("missing module directory removed public builtin")
	}
}
