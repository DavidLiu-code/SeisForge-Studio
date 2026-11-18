package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	PackManifestName     = "algorithm-pack.json"
	PackSchema           = "seisforge.algorithm-pack.v1"
	maxPackManifestBytes = 64 << 10
	maxPackMembers       = 64
)

var (
	packID      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
	packVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?(?:\+[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)
	packName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,131}$`)
)

type packManifest struct {
	Schema    string   `json:"schema"`
	ID        string   `json:"id"`
	Version   string   `json:"version"`
	Manifests []string `json:"manifests"`
}

func equalPackPath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func realPackPath(path string, dir bool) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if dir && !info.IsDir() || !dir && !info.Mode().IsRegular() {
		return "", errors.New("algorithm package paths must be real directories or regular files, not symlinks")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !equalPackPath(resolved, path) {
		return "", errors.New("algorithm package paths cannot traverse symlinks")
	}
	return resolved, nil
}

func readPackMarker(path string) ([]byte, error) {
	if _, err := realPackPath(path, false); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	payload, err := io.ReadAll(io.LimitReader(f, maxPackManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxPackManifestBytes {
		return nil, errors.New("algorithm package manifest exceeds size limit")
	}
	return payload, nil
}

func parsePackMarker(data []byte) (packManifest, error) {
	var marker packManifest
	dec := json.NewDecoder(bytes.NewReader(data))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return marker, errors.New("algorithm package manifest must be a JSON object")
	}
	seen := make(map[string]bool, 4)
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return marker, err
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return marker, errors.New("algorithm package manifest has an invalid or duplicate field")
		}
		seen[name] = true
		var dest any
		switch name {
		case "schema":
			dest = &marker.Schema
		case "id":
			dest = &marker.ID
		case "version":
			dest = &marker.Version
		case "manifests":
			dest = &marker.Manifests
		default:
			return marker, fmt.Errorf("unknown algorithm package field %q", name)
		}
		if err := dec.Decode(dest); err != nil {
			return marker, fmt.Errorf("algorithm package field %q: %w", name, err)
		}
	}
	if end, err := dec.Token(); err != nil || end != json.Delim('}') {
		return marker, errors.New("algorithm package manifest is incomplete")
	}
	if _, err := dec.Token(); err != io.EOF {
		return marker, errors.New("unexpected trailing algorithm package data")
	}
	if marker.Schema != PackSchema || !packID.MatchString(marker.ID) || len(marker.Version) > 128 || !packVersion.MatchString(marker.Version) {
		return marker, errors.New("unsupported algorithm package schema, ID or version")
	}
	if n := len(marker.Manifests); n == 0 || n > maxPackMembers {
		return marker, fmt.Errorf("algorithm package must list 1–%d manifests", maxPackMembers)
	}
	return marker, nil
}

func flatPackName(name string) bool {
	return packName.MatchString(name) && !strings.HasSuffix(name, ".") && filepath.IsLocal(name) && filepath.Base(name) == name
}

// LoadPackManifest reads only explicitly listed descriptors (no recursive
// discovery and no process launch). Every member must refer to the same flat,
// regular executable in the package directory. An invalid member rejects the
// whole package instead of making it appear partly installed.
func LoadPackManifest(ctx context.Context, path, hostVersion string) ([]Module, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Base(path), PackManifestName) {
		return nil, errors.New("not an algorithm-pack.json path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, err := realPackPath(filepath.Dir(abs), true)
	if err != nil {
		return nil, err
	}
	path, err = realPackPath(filepath.Join(root, filepath.Base(abs)), false)
	if err != nil {
		return nil, err
	}
	payload, err := readPackMarker(path)
	if err != nil {
		return nil, err
	}
	marker, err := parsePackMarker(payload)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(payload)
	out := make([]Module, 0, len(marker.Manifests))
	seenNames, seenIDs := make(map[string]bool), make(map[string]bool)
	sharedExecutable := ""
	for _, name := range marker.Manifests {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lower := strings.ToLower(name)
		if !flatPackName(name) || !strings.HasSuffix(lower, ".json") || lower == PackManifestName || lower == "manifest.json" || seenNames[lower] {
			return nil, fmt.Errorf("invalid or duplicate algorithm package member %q", name)
		}
		seenNames[lower] = true
		memberPath, err := realPackPath(filepath.Join(root, name), false)
		if err != nil {
			return nil, fmt.Errorf("algorithm package member %q: %w", name, err)
		}
		m, err := LoadManifest(memberPath, hostVersion)
		if err != nil {
			return nil, fmt.Errorf("algorithm package member %q: %w", name, err)
		}
		if seenIDs[m.descriptor.ID] {
			return nil, fmt.Errorf("duplicate algorithm package member ID %q", m.descriptor.ID)
		}
		seenIDs[m.descriptor.ID] = true
		if !flatPackName(m.descriptor.Executable) {
			return nil, errors.New("algorithm package executable must be a package-local filename")
		}
		exe, err := realPackPath(filepath.Join(root, m.descriptor.Executable), false)
		if err != nil || !equalPackPath(exe, m.executable) || sharedExecutable != "" && !equalPackPath(exe, sharedExecutable) {
			return nil, errors.New("algorithm package members must share one regular executable in their directory")
		}
		sharedExecutable = exe
		m.packPath, m.packHash, m.packID, m.packVersion = path, hash, marker.ID, marker.Version
		identity := sha256.Sum256([]byte(m.fingerprint + "\x00" + path + "\x00" + hex.EncodeToString(hash[:])))
		m.fingerprint = hex.EncodeToString(identity[:])
		out = append(out, m)
	}
	return out, nil
}
