//go:build !windows

package poststack

import "os"

func publishResultFile(src, dst string, overwrite bool) error {
	if overwrite {
		return os.Rename(src, dst)
	}
	// Link atomically fails if the target already exists, unlike Unix Rename.
	// The caller always removes the temporary source link after publication.
	return os.Link(src, dst)
}
