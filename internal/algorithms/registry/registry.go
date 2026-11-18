// Package registry discovers metadata only. Discovery never executes a module.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
)

const MaxManifestBytes = 1 << 20

// Module is an immutable discovery snapshot. Trust applies to its complete
// fingerprint, including canonical paths, manifest bytes and executable bytes.
type Module struct {
	descriptor                                api.AlgorithmDescriptor
	directory, manifest, executable           string
	manifestHash, executableHash, fingerprint string
	packPath, packID, packVersion             string
	packHash                                  [sha256.Size]byte
	builtin                                   bool
}

func (m Module) Descriptor() api.AlgorithmDescriptor { return api.CloneDescriptor(m.descriptor) }
func (m Module) Directory() string                   { return m.directory }
func (m Module) ManifestPath() string                { return m.manifest }
func (m Module) ExecutablePath() string              { return m.executable }
func (m Module) ManifestSHA256() string              { return m.manifestHash }
func (m Module) ExecutableSHA256() string            { return m.executableHash }
func (m Module) Fingerprint() string                 { return m.fingerprint }
func (m Module) IsBuiltin() bool                     { return m.builtin }
func (m Module) PackManifestPath() string            { return m.packPath }
func (m Module) PackID() string                      { return m.packID }
func (m Module) PackVersion() string                 { return m.packVersion }

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func contained(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// Verify must run immediately before execution, even for --describe.
func (m Module) Verify() error {
	if m.fingerprint == "" {
		return errors.New("algorithm module is not registered")
	}
	if m.builtin {
		// Only RegisterBuiltin can create this immutable host-owned snapshot.
		// No manifest field or external executable path grants this capability.
		return nil
	}
	for _, path := range []string{m.manifest, m.executable} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || !contained(m.directory, resolved) || resolved != path {
			return errors.New("algorithm module path changed; discover and authorize it again")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("algorithm module is no longer a regular file")
		}
	}
	mh, err := hashFile(m.manifest)
	if err != nil {
		return err
	}
	eh, err := hashFile(m.executable)
	if err != nil {
		return err
	}
	if mh != m.manifestHash || eh != m.executableHash {
		return errors.New("algorithm module changed; discover and authorize it again")
	}
	if m.packPath != "" {
		payload, err := readPackMarker(m.packPath)
		if err != nil || sha256.Sum256(payload) != m.packHash {
			return errors.New("algorithm package changed; discover and authorize it again")
		}
	}
	return nil
}

func LoadManifest(path, hostVersion string) (Module, error) {
	var out Module
	abs, err := filepath.Abs(path)
	if err != nil {
		return out, err
	}
	moduleRoot, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return out, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return out, err
	}
	if !contained(moduleRoot, abs) {
		return out, errors.New("manifest escapes its module directory")
	}
	f, err := os.Open(abs)
	if err != nil {
		return out, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, MaxManifestBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return out, readErr
	}
	if closeErr != nil {
		return out, closeErr
	}
	if len(data) > MaxManifestBytes {
		return out, errors.New("algorithm manifest exceeds size limit")
	}
	desc, err := api.ParseManifest(data, hostVersion)
	if err != nil {
		return out, err
	}
	if !filepath.IsLocal(desc.Executable) || strings.Contains(desc.Executable, ":") {
		return out, errors.New("executable must be a module-local relative path")
	}
	root := moduleRoot
	executable, err := filepath.EvalSymlinks(filepath.Join(root, desc.Executable))
	if err != nil {
		return out, fmt.Errorf("algorithm executable: %w", err)
	}
	if !contained(root, executable) {
		return out, errors.New("algorithm executable escapes its module directory")
	}
	info, err := os.Stat(executable)
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() {
		return out, errors.New("algorithm executable is not a regular file")
	}
	mh := sha256.Sum256(data)
	eh, err := hashFile(executable)
	if err != nil {
		return out, err
	}
	out = Module{descriptor: api.CloneDescriptor(desc), directory: root, manifest: abs, executable: executable, manifestHash: hex.EncodeToString(mh[:]), executableHash: eh}
	identity := sha256.Sum256([]byte(abs + "\x00" + executable + "\x00" + out.manifestHash + "\x00" + eh))
	out.fingerprint = hex.EncodeToString(identity[:])
	return out, nil
}

type Issue struct{ Path, Message string }
type AlgorithmRegistry struct {
	mu          sync.RWMutex
	hostVersion string
	modules     map[string]Module
	builtins    map[string]Module
}
type Registry = AlgorithmRegistry

func New(hostVersion string) *AlgorithmRegistry {
	return &AlgorithmRegistry{hostVersion: hostVersion, modules: make(map[string]Module), builtins: make(map[string]Module)}
}

// RegisterBuiltin is called only by trusted host code. It registers a public
// implementation compiled into the host, without giving external manifests an
// in-process execution capability. Descriptor/UI/parameter contracts remain
// identical to external modules. Scanning cannot replace a reserved builtin ID.
func (r *AlgorithmRegistry) RegisterBuiltin(desc api.AlgorithmDescriptor) error {
	if err := api.ValidateDescriptor(desc, r.hostVersion); err != nil {
		return err
	}
	data, err := json.Marshal(desc)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	mh := hex.EncodeToString(digest[:])
	identity := sha256.Sum256([]byte("host-builtin\x00" + r.hostVersion + "\x00" + mh))
	m := Module{descriptor: api.CloneDescriptor(desc), builtin: true, manifestHash: mh,
		fingerprint: hex.EncodeToString(identity[:])}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, exists := r.modules[desc.ID]; exists && !old.builtin {
		return errors.New("algorithm ID is already registered by an external module")
	}
	r.builtins[desc.ID], r.modules[desc.ID] = m, m
	return nil
}

func DefaultDirectory() (string, error) {
	root := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if root == "" {
		var err error
		root, err = os.UserCacheDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(root, "Limage", "algorithms"), nil
}

// Scan accepts explicitly configured module/package directories or their parents.
// It inspects only immediate children; no PATH or seismic-file directory lookup.
// An absent directory is a normal, empty installation.
func (r *AlgorithmRegistry) Scan(ctx context.Context, roots []string) ([]Issue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var issues []Issue
	var paths []string
	addDirectory := func(dir string) bool {
		pack := filepath.Join(dir, PackManifestName)
		if _, err := os.Lstat(pack); err == nil {
			paths = append(paths, pack)
			return true
		}
		path := filepath.Join(dir, "manifest.json")
		if _, err := os.Stat(path); err == nil {
			paths = append(paths, path)
			return true
		}
		return false
	}
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return issues, err
		}
		if addDirectory(root) {
			continue
		}
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			issues = append(issues, Issue{root, err.Error()})
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				addDirectory(filepath.Join(root, entry.Name()))
			}
		}
	}
	sort.Strings(paths)
	modules := make(map[string]Module)
	r.mu.RLock()
	for id, m := range r.builtins {
		modules[id] = m
	}
	r.mu.RUnlock()
	seenPaths := make(map[string]bool)
	seenCanonical := make(map[string]bool)
	conflicts := make(map[string]bool)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return issues, err
		}
		abs, _ := filepath.Abs(path)
		if seenPaths[abs] {
			continue
		}
		seenPaths[abs] = true
		var loaded []Module
		var err error
		if filepath.Base(path) == PackManifestName {
			loaded, err = LoadPackManifest(ctx, path, r.hostVersion)
		} else {
			var m Module
			m, err = LoadManifest(path, r.hostVersion)
			loaded = []Module{m}
		}
		if err != nil {
			issues = append(issues, Issue{path, err.Error()})
			continue
		}
		for _, m := range loaded {
			canonical := m.ManifestPath()
			if runtime.GOOS == "windows" {
				canonical = strings.ToLower(canonical)
			}
			if seenCanonical[canonical] {
				continue
			}
			seenCanonical[canonical] = true
			id := m.descriptor.ID
			if old, exists := modules[id]; exists && old.builtin {
				issues = append(issues, Issue{m.ManifestPath(), "reserved built-in algorithm ID: " + id})
				continue
			}
			if _, exists := modules[id]; exists || conflicts[id] {
				delete(modules, id)
				conflicts[id] = true
				issues = append(issues, Issue{m.ManifestPath(), "duplicate algorithm ID: " + id})
				continue
			}
			modules[id] = m
		}
	}
	if err := ctx.Err(); err != nil {
		return issues, err
	}
	r.mu.Lock()
	r.modules = modules
	r.mu.Unlock()
	return issues, nil
}

func (r *AlgorithmRegistry) Get(id string) (Module, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.modules[id]
	return m, ok
}
func (r *AlgorithmRegistry) Modules() []Module {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.modules))
	for id := range r.modules {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Module, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.modules[id])
	}
	return out
}
