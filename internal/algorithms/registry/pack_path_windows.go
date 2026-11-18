package registry

import (
	"path/filepath"
	"strings"
	"syscall"
)

// On Windows, EvalSymlinks also expands real 8.3 names. Confirm that each
// changed spelling can be obtained from its directory entry without following
// any symlink or junction. File identity alone would not establish this: a
// redirected ancestor may reach the same file but still traverse a reparse
// point. FindFirstFile reports the entry's own attributes and long filename.
func isPackPathSpellingAlias(path, resolved string) bool {
	volume := filepath.VolumeName(path)
	if volume == "" || !filepath.IsAbs(path) {
		return false
	}
	original, normalized := volume+"\\", volume+"\\"
	for _, part := range strings.Split(strings.TrimPrefix(path[len(volume):], "\\"), "\\") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		original = filepath.Join(original, part)
		name, err := syscall.UTF16PtrFromString(original)
		if err != nil {
			return false
		}
		var data syscall.Win32finddata
		handle, err := syscall.FindFirstFile(name, &data)
		if err != nil {
			return false
		}
		syscall.FindClose(handle)
		if data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return false
		}
		normalized = filepath.Join(normalized, syscall.UTF16ToString(data.FileName[:]))
	}
	return equalPackPath(normalized, resolved)
}
