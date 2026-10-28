package figurebundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testImage() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 7, 5))
	for y := 0; y < 5; y++ {
		for x := 0; x < 7; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 29), uint8(y * 41), uint8((x + y) * 17), uint8(96 + x*20)})
		}
	}
	return img
}

func testManifest(t *testing.T, img image.Image) BundleManifest {
	t.Helper()
	hash, err := PixelSHA256(img)
	if err != nil {
		t.Fatal(err)
	}
	return BundleManifest{Schema: Schema, RendererSHA256: strings.Repeat("a", 64), ApplicationVersion: "test-v1",
		SourceA: SourceIdentity{Path: `C:\数据\A's & literal $(value).sgy`, Size: 123, ModifiedUnixNano: 42, SHA256: strings.Repeat("b", 64)},
		Payload: json.RawMessage(`{"width":7,"height":5,"gain":1.25}`), PixelSHA256: hash, Width: img.Bounds().Dx(), Height: img.Bounds().Dy()}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func requireCleanParent(t *testing.T, parent string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var actual []string
	for _, e := range entries {
		actual = append(actual, e.Name())
	}
	if len(want) == 0 {
		want = nil
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("unexpected output/temp entries: got %v want %v", actual, want)
	}
}

func TestBundleManifestStrictAndBounded(t *testing.T) {
	m := testManifest(t, testImage())
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeManifest(bytes.NewReader(data))
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("roundtrip: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*BundleManifest)
	}{
		{"schema", func(m *BundleManifest) { m.Schema = "seisforge.figure.v2" }},
		{"renderer hash", func(m *BundleManifest) { m.RendererSHA256 = "bad" }},
		{"source hash", func(m *BundleManifest) { m.SourceA.SHA256 = strings.Repeat("z", 64) }},
		{"pixel hash", func(m *BundleManifest) { m.PixelSHA256 = "" }},
		{"version", func(m *BundleManifest) { m.ApplicationVersion = " " }},
		{"source path", func(m *BundleManifest) { m.SourceA.Path = "" }},
		{"negative size", func(m *BundleManifest) { m.SourceA.Size = -1 }},
		{"payload nil", func(m *BundleManifest) { m.Payload = nil }},
		{"payload null", func(m *BundleManifest) { m.Payload = json.RawMessage("null") }},
		{"width", func(m *BundleManifest) { m.Width = 0 }},
		{"pixel limit", func(m *BundleManifest) { m.Width = 8001; m.Height = 8000 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			altered := m
			test.change(&altered)
			b, e := json.Marshal(altered)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = DecodeManifest(bytes.NewReader(b)); !errors.Is(e, ErrInvalidManifest) {
				t.Fatalf("accepted invalid manifest: %v", e)
			}
		})
	}
	for _, data := range [][]byte{[]byte("{"), append(append([]byte{}, data...), []byte(" {}")...), append([]byte(`{"unknown":1,`), data[1:]...)} {
		if _, err := DecodeManifest(bytes.NewReader(data)); err == nil {
			t.Fatal("accepted malformed/unknown/trailing JSON")
		}
	}
	spaces := &spaceReader{}
	if _, err := DecodeManifest(io.MultiReader(bytes.NewReader(data), io.LimitReader(spaces, MaxManifestBytes+1))); err == nil {
		t.Fatal("accepted oversized input")
	}
	if spaces.read > MaxManifestBytes+1 {
		t.Fatal("manifest read exceeded fixed bound")
	}
	if _, err := DecodeManifest(nil); err == nil {
		t.Fatal("accepted nil reader")
	}
}

type spaceReader struct{ read int64 }

func (r *spaceReader) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = ' '
	}
	r.read += int64(len(b))
	return len(b), nil
}

func TestPixelSHA256CanonicalAndPNGEncodingIndependent(t *testing.T) {
	img := testImage()
	want, err := PixelSHA256(img)
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []png.CompressionLevel{png.NoCompression, png.BestSpeed, png.BestCompression} {
		var buffer bytes.Buffer
		if err := (&png.Encoder{CompressionLevel: level}).Encode(&buffer, img); err != nil {
			t.Fatal(err)
		}
		decoded, err := png.Decode(&buffer)
		if err != nil {
			t.Fatal(err)
		}
		got, err := PixelSHA256(decoded)
		if err != nil || got != want {
			t.Fatalf("encoding changed canonical pixels: %v", err)
		}
	}
	shifted := image.NewNRGBA(image.Rect(-4, 11, 3, 16))
	for y := 0; y < 5; y++ {
		for x := 0; x < 7; x++ {
			shifted.Set(x-4, y+11, img.At(x, y))
		}
	}
	if got, _ := PixelSHA256(shifted); got != want {
		t.Fatal("origin affects hash")
	}
	shifted.SetNRGBA(-4, 11, color.NRGBA{255, 255, 255, 255})
	if got, _ := PixelSHA256(shifted); got == want {
		t.Fatal("changed pixel kept hash")
	}
	a, b := image.NewRGBA(image.Rect(0, 0, 2, 1)), image.NewRGBA(image.Rect(0, 0, 1, 2))
	ha, _ := PixelSHA256(a)
	hb, _ := PixelSHA256(b)
	if ha == hb {
		t.Fatal("dimensions omitted from hash")
	}
	var typedNil *image.RGBA
	for _, img := range []image.Image{nil, typedNil, image.NewRGBA(image.Rectangle{}), hugeImage{}} {
		if _, err := PixelSHA256(img); err == nil {
			t.Fatal("invalid raster accepted")
		}
	}
}

type hugeImage struct{}

func (hugeImage) Bounds() image.Rectangle { return image.Rect(0, 0, 8001, 8000) }
func (hugeImage) ColorModel() color.Model { return color.RGBAModel }
func (hugeImage) At(int, int) color.Color { panic("oversized image pixels must not be read") }

type actionContext struct {
	context.Context
	calls, at int
	action    func()
}

func (c *actionContext) Err() error {
	c.calls++
	if c.calls == c.at {
		c.action()
	}
	return c.Context.Err()
}

func TestHashFileStreamsCancelsAndDetectsChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.bin")
	data := bytes.Repeat([]byte("full source\x00"), 200_000)
	writeTestFile(t, path, data)
	expected := sha256.Sum256(data)
	got, err := HashFile(context.Background(), path)
	if err != nil || got != hex.EncodeToString(expected[:]) {
		t.Fatalf("full hash: %s %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := HashFile(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var changeErr error
	changing := &actionContext{Context: context.Background(), at: 3, action: func() { changeErr = os.Chtimes(path, st.ModTime(), st.ModTime().Add(time.Second)) }}
	if _, err := HashFile(changing, path); changeErr != nil || !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("source change: %v / %v", changeErr, err)
	}
	if !bytes.Equal(data, readTestFile(t, path)) {
		t.Fatal("hashing wrote source bytes")
	}
	if _, err := HashFile(context.Background(), filepath.Dir(path)); err == nil {
		t.Fatal("directory accepted as source")
	}
}

func TestWriteBundleCompleteAndExistingDirectoryPreserved(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "bundle")
	img := testImage()
	manifest := testManifest(t, img)
	manifest.Schema = ""
	tool := `C:\工具's & literal $(value)\renderer.exe`
	if err := WriteBundle(context.Background(), dir, manifest, img, tool); err != nil {
		t.Fatal(err)
	}
	requireCleanParent(t, parent, "bundle")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !reflect.DeepEqual(names, []string{ReadmeFilename, RecipeFilename, ReferenceFilename, ScriptFilename}) {
		t.Fatalf("bundle files: %v", names)
	}
	got, err := ReadManifest(filepath.Join(dir, RecipeFilename))
	if err != nil || got.Schema != Schema {
		t.Fatal("recipe did not roundtrip")
	}
	f, err := os.Open(filepath.Join(dir, ReferenceFilename))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if hash, _ := PixelSHA256(decoded); hash != got.PixelSHA256 {
		t.Fatal("reference proof differs")
	}
	script := readTestFile(t, filepath.Join(dir, ScriptFilename))
	if !bytes.HasPrefix(script, []byte{0xef, 0xbb, 0xbf}) || !bytes.Contains(script, []byte(powerShellLiteral(tool))) {
		t.Fatal("script lacks PS5.1 BOM or escaped literal")
	}
	for _, text := range []string{"-WindowStyle Hidden", "--render-recipe", "--source-a", "--source-b", "--result", "--overwrite", "ConvertTo-NativeArgument"} {
		if !bytes.Contains(script, []byte(text)) {
			t.Fatalf("script omitted %s", text)
		}
	}
	if !bytes.Contains(readTestFile(t, filepath.Join(dir, ReadmeFilename)), []byte("not the reproduction algorithm")) {
		t.Fatal("README omitted reference/replay boundary")
	}
	before := readTestFile(t, filepath.Join(dir, RecipeFilename))
	if err := WriteBundle(context.Background(), dir, manifest, img, tool); !errors.Is(err, ErrExists) {
		t.Fatalf("existing directory: %v", err)
	}
	if !bytes.Equal(before, readTestFile(t, filepath.Join(dir, RecipeFilename))) {
		t.Fatal("existing bundle overwritten")
	}
}

type observedImage struct {
	image.Image
	once   bool
	action func()
}

func (i *observedImage) At(x, y int) color.Color {
	if !i.once {
		i.once = true
		i.action()
	}
	return i.Image.At(x, y)
}

func TestBundleCancellationMismatchAndConcurrentTargetLeaveNoTemps(t *testing.T) {
	for _, kind := range []string{"cancel", "pixel mismatch", "dimension mismatch", "concurrent empty target"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "bundle")
			img := testImage()
			m := testManifest(t, img)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var reference image.Image = img
			var actionErr error
			switch kind {
			case "cancel":
				reference = &observedImage{Image: img, action: cancel}
			case "pixel mismatch":
				m.PixelSHA256 = strings.Repeat("0", 64)
			case "dimension mismatch":
				m.Width++
			case "concurrent empty target":
				reference = &observedImage{Image: img, action: func() { actionErr = os.Mkdir(target, 0700) }}
			}
			err := WriteBundle(ctx, target, m, reference, "renderer.exe")
			if err == nil || actionErr != nil {
				t.Fatalf("expected failure: %v / %v", err, actionErr)
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
			if kind == "concurrent empty target" {
				requireCleanParent(t, parent, "bundle")
				requireCleanParent(t, target)
			} else {
				requireCleanParent(t, parent)
			}
		})
	}
}

func TestVerifiedPNGOverwriteCancellationAndRace(t *testing.T) {
	for _, kind := range []string{"new", "overwrite denied", "overwrite allowed", "bad hash", "cancel", "target races"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "figure.png")
			img := testImage()
			expected, _ := PixelSHA256(img)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var rendered image.Image = img
			overwrite := kind == "overwrite allowed"
			if strings.HasPrefix(kind, "overwrite") {
				writeTestFile(t, target, []byte("old"))
			}
			if kind == "bad hash" {
				expected = strings.Repeat("0", 64)
			}
			if kind == "cancel" {
				rendered = &observedImage{Image: img, action: cancel}
			}
			var actionErr error
			if kind == "target races" {
				rendered = &observedImage{Image: img, action: func() { actionErr = os.WriteFile(target, []byte("race"), 0600) }}
			}
			err := WriteVerifiedPNG(ctx, target, rendered, expected, overwrite)
			if actionErr != nil {
				t.Fatal(actionErr)
			}
			if kind == "new" || overwrite {
				if err != nil {
					t.Fatal(err)
				}
				f, e := os.Open(target)
				if e != nil {
					t.Fatal(e)
				}
				decoded, e := png.Decode(f)
				_ = f.Close()
				if e != nil {
					t.Fatal(e)
				}
				got, _ := PixelSHA256(decoded)
				if got != expected {
					t.Fatal("published pixels differ")
				}
				requireCleanParent(t, parent, "figure.png")
			} else {
				if err == nil {
					t.Fatal("unexpected publication")
				}
				switch kind {
				case "overwrite denied":
					if string(readTestFile(t, target)) != "old" {
						t.Fatal("overwrote existing")
					}
					requireCleanParent(t, parent, "figure.png")
				case "target races":
					if string(readTestFile(t, target)) != "race" {
						t.Fatal("overwrote concurrent output")
					}
					requireCleanParent(t, parent, "figure.png")
				default:
					requireCleanParent(t, parent)
				}
			}
		})
	}
}

func TestOutputSymlinkDirectoryRejected(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	link := filepath.Join(parent, "link")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	img := testImage()
	m := testManifest(t, img)
	if err := WriteBundle(context.Background(), filepath.Join(link, "bundle"), m, img, "renderer.exe"); err == nil {
		t.Fatal("linked output parent accepted")
	}
	if err := WriteVerifiedPNG(context.Background(), filepath.Join(link, "figure.png"), img, m.PixelSHA256, false); err == nil {
		t.Fatal("linked PNG parent accepted")
	}
	requireCleanParent(t, real)
}
