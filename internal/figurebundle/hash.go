package figurebundle

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"io"
	"os"
	"reflect"
)

func usableContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// HashFile streams the complete source, and rejects path replacement or
// size/mtime changes seen between opening and completion. It never writes it.
func HashFile(ctx context.Context, path string) (string, error) {
	ctx = usableContext(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() {
		return "", fmt.Errorf("source is not a regular file")
	}
	live, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !sameFileSnapshot(before, live) {
		return "", ErrSourceChanged
	}
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	after, err := f.Stat()
	if err != nil {
		return "", err
	}
	live, err = os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSourceChanged, err)
	}
	if !sameFileSnapshot(before, after) || !sameFileSnapshot(after, live) {
		return "", ErrSourceChanged
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func sameFileSnapshot(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func imageDimensions(img image.Image) (int, int, error) {
	if img == nil || (reflect.ValueOf(img).Kind() == reflect.Pointer && reflect.ValueOf(img).IsNil()) {
		return 0, 0, fmt.Errorf("nil figure image")
	}
	bounds := img.Bounds()
	if bounds.Max.X <= bounds.Min.X || bounds.Max.Y <= bounds.Min.Y {
		return 0, 0, fmt.Errorf("empty figure image")
	}
	// Unsigned subtraction also handles negative origins without signed-int
	// overflow for a maliciously large image.Rectangle.
	w, h := uint64(bounds.Max.X)-uint64(bounds.Min.X), uint64(bounds.Max.Y)-uint64(bounds.Min.Y)
	if w > uint64(MaxRasterPixels) || h > uint64(MaxRasterPixels)/w {
		return 0, 0, fmt.Errorf("figure exceeds 64 megapixels")
	}
	return int(w), int(h), nil
}

// PixelSHA256 hashes dimensions and row-major premultiplied 8-bit RGBA values,
// independent of image origin, PNG compression, ancillary chunks or file bytes.
func PixelSHA256(img image.Image) (string, error) { return pixelSHA256(context.Background(), img) }

func pixelSHA256(ctx context.Context, img image.Image) (string, error) {
	w, h, err := imageDimensions(img)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("seisforge.pixel.rgba8.v1\x00"))
	var dimensions [16]byte
	binary.LittleEndian.PutUint64(dimensions[:8], uint64(w))
	binary.LittleEndian.PutUint64(dimensions[8:], uint64(h))
	_, _ = hash.Write(dimensions[:])
	var buffer [32 * 1024]byte
	at, bounds := 0, img.Bounds()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, a := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			buffer[at], buffer[at+1], buffer[at+2], buffer[at+3] = byte(r>>8), byte(g>>8), byte(b>>8), byte(a>>8)
			at += 4
			if at == len(buffer) {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				_, _ = hash.Write(buffer[:at])
				at = 0
			}
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	_, _ = hash.Write(buffer[:at])
	return hex.EncodeToString(hash.Sum(nil)), nil
}
