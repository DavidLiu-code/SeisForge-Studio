//go:build !windows

package figurebundle

import "os"

func publishFile(source, target string, overwrite bool) error {
	if overwrite {
		return os.Rename(source, target)
	}
	// Hard linking is atomic and cannot replace any existing directory entry.
	// The caller subsequently removes its own temporary name.
	return os.Link(source, target)
}
