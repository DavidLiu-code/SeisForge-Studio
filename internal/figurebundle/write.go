package figurebundle

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// checkedTarget rejects links in output-directory components and an existing
// linked/nonregular output. Publication still uses native atomic primitives;
// a prior existence check alone is never relied on for no-overwrite behavior.
func checkedTarget(path string, directory bool, overwrite bool) (string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("empty or invalid output path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if filepath.Dir(abs) == abs {
		return "", fmt.Errorf("output cannot be a filesystem root")
	}
	for parent := filepath.Dir(abs); ; parent = filepath.Dir(parent) {
		st, err := os.Lstat(parent)
		if err != nil {
			return "", err
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return "", fmt.Errorf("output parent is not a plain directory: %s", parent)
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	if st, err := os.Lstat(abs); err == nil {
		if directory || !overwrite {
			return "", fmt.Errorf("%w: %s", ErrExists, abs)
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return "", fmt.Errorf("output is not a regular file: %s", abs)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return abs, nil
}

type contextWriter struct {
	ctx context.Context
	out io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.out.Write(p)
}

// encodeVerifiedPNG verifies the encoded image, not merely the caller's image
// before encoding. This also catches an image that changes during export.
func encodeVerifiedPNG(ctx context.Context, f *os.File, img image.Image, expected string) error {
	actual, err := pixelSHA256(ctx, img)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expected) {
		return ErrPixelMismatch
	}
	if err := png.Encode(contextWriter{ctx, f}, img); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	decoded, err := png.Decode(f)
	if err != nil {
		return err
	}
	actual, err = pixelSHA256(ctx, decoded)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expected) {
		return ErrPixelMismatch
	}
	return ctx.Err()
}

// WriteVerifiedPNG publishes only an encoded PNG with the expected canonical
// pixel hash. Cancellation wins before publication; publication itself is the
// atomic commit point and is not rolled back after success.
func WriteVerifiedPNG(ctx context.Context, output string, img image.Image, expectedHash string, overwrite bool) error {
	ctx = usableContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validSHA256(expectedHash) {
		return fmt.Errorf("invalid expected pixel SHA-256")
	}
	if _, _, err := imageDimensions(img); err != nil {
		return err
	}
	target, err := checkedTarget(output, false, overwrite)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer func() { _ = f.Close(); _ = os.Remove(temporary) }()
	if err := encodeVerifiedPNG(ctx, f, img, expectedHash); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return publishFile(temporary, target, overwrite)
}

// WriteBundle creates a complete new directory in one non-replacing commit.
// Callers validate source identities/domain payloads; this package validates
// their envelope, pixel proof and bounded files but never executes the tool.
func WriteBundle(ctx context.Context, dir string, manifest BundleManifest, reference image.Image, toolPath string) error {
	ctx = usableContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if manifest.Schema == "" {
		manifest.Schema = Schema
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(toolPath) == "" || strings.ContainsRune(toolPath, 0) {
		return fmt.Errorf("invalid renderer tool path")
	}
	w, h, err := imageDimensions(reference)
	if err != nil {
		return err
	}
	if w != manifest.Width || h != manifest.Height {
		return fmt.Errorf("%w: reference dimensions differ", ErrPixelMismatch)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if int64(len(data)) > MaxManifestBytes {
		return fmt.Errorf("%w: JSON exceeds 32 MiB", ErrInvalidManifest)
	}
	target, err := checkedTarget(dir, true, false)
	if err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return err
	}
	// Never clean target: it may be a pre-existing/concurrently created bundle.
	defer os.RemoveAll(temporary)
	for _, item := range []struct {
		name string
		data []byte
	}{
		{RecipeFilename, data}, {ScriptFilename, []byte(renderScript(toolPath))}, {ReadmeFilename, []byte(readmeText(manifest))},
	} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeSynced(filepath.Join(temporary, item.name), item.data); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(filepath.Join(temporary, ReferenceFilename), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = encodeVerifiedPNG(ctx, f, reference, manifest.PixelSHA256)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return publishNewDirectory(temporary, target)
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}
