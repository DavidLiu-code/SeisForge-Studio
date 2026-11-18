package poststack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// AtomicWrite publishes a derived-result sidecar or metadata file without
// exposing a partially written file. Existing output is rejected unless the
// caller explicitly sets overwrite.
func AtomicWrite(ctx context.Context, path string, data []byte, overwrite bool) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("output path is required")
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".seisforge-poststack-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = checkContext(ctx); err != nil {
		return err
	}
	return publishResultFile(tmpPath, path, overwrite)
}

// CopyFileAtomic copies a validated derived SEG-Y or sidecar into place. It
// intentionally does not read the source into memory.
func CopyFileAtomic(ctx context.Context, src, dst string, overwrite bool) error {
	if strings.TrimSpace(src) == "" || strings.TrimSpace(dst) == "" {
		return errors.New("source and destination are required")
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	before, err := in.Stat()
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return errors.New("source must be a regular file")
	}
	if target, statErr := os.Stat(dst); statErr == nil && os.SameFile(before, target) {
		return errors.New("source and destination must be distinct files")
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".seisforge-poststack-copy-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }()
	buf := make([]byte, 128*1024)
	for {
		if err := checkContext(ctx); err != nil {
			return err
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	after, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return errors.New("source changed during copy")
	}
	return publishResultFile(tmpPath, dst, overwrite)
}

func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
