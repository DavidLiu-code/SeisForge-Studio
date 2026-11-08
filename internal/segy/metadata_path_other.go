//go:build !windows

package segy

import (
	"os"
	"path/filepath"
)

func metadataCanonicalPath(_ *os.File, path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}
