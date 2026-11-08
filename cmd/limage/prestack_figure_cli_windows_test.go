//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/figurebundle"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func figureCLIWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func figureCLIWritePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, img)
	err = errors.Join(err, f.Close())
	if err != nil {
		t.Fatal(err)
	}
}

func figureCLIFixture(t *testing.T, compare ...bool) (prestackFigureCLIOptions, figurebundle.BundleManifest, *image.NRGBA) {
	t.Helper()
	state, p := figureGatherFixture(t, 900, 650, 0, false)
	// The lower-level fixture intentionally retains its Reader until cleanup.
	// Replay an identical independent copy so an exclusive-open check below
	// genuinely detects only command-worker Reader leaks.
	aPath := filepath.Join(t.TempDir(), "A copied source.sgy")
	aBytes, err := os.ReadFile(state.dataset.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aPath, aBytes, 0600); err != nil {
		t.Fatal(err)
	}
	state.dataset, err = dataset.NewManager().Open(aPath)
	if err != nil {
		t.Fatal(err)
	}
	p.SourceAMetadata = state.dataset.Metadata
	if len(compare) != 0 && compare[0] {
		bPath := filepath.Join(t.TempDir(), "B reordered source.sgy")
		data, err := os.ReadFile(state.dataset.Path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		state.compareBDataset, err = dataset.NewManager().Open(bPath)
		if err != nil {
			t.Fatal(err)
		}
		state.page = 3
		state.compareFilterMask = 31
		state.compareBAxis = prestackcore.SampleAxisCompatibility{Compatible: true}
		state.compareRows = []prestackcore.CompareLogicalRow{
			{Classification: prestackcore.CompareRowMatched, HasA: true, HasB: true, ATrace: 0, BTrace: 2},
			{Classification: prestackcore.CompareRowMatched, HasA: true, HasB: true, ATrace: 3, BTrace: 4},
		}
		state.compareDisplayRows = state.compareRows
		layout := prestackPanelLayoutForSize(900, 650, 3)
		panels := prestackComparePanelRectsForContent(prestackLayoutRECT(layout.Content), prestackCompareSummaryHeight)
		raster := prestackCompareRasterRect(panels[0])
		state.compareWidth, state.compareHeight = int(raster.Right-raster.Left), int(raster.Bottom-raster.Top)
		frame := renderPrestackCompareFrame(context.Background(), prestackCompareRenderRequest{
			token: prestackCompareRenderResult{width: state.compareWidth, height: state.compareHeight, palette: state.palette, sampleFirst: int64(state.sampleFirst), sampleLast: int64(state.sampleLast)},
			aData: state.dataset, bData: state.compareBDataset, rows: state.compareRows, denseRows: true, agc: state.agc, gain: state.gain, axisCompatible: true})
		if frame.err != nil || frame.bErr != nil {
			t.Fatalf("Compare fixture failed: %v / %v", frame.err, frame.bErr)
		}
		state.compareA, state.compareB, state.compareDelta = frame.a, frame.b, frame.delta
		p, err = CapturePrestackFigurePayload(&state, 900, 650)
		if err != nil {
			t.Fatal(err)
		}
	}
	img, err := PaintPrestackFigurePayload(p, &state)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	engineHash, err := figurebundle.HashFile(context.Background(), exe)
	if err != nil {
		t.Fatal(err)
	}
	sourceHash, err := figurebundle.HashFile(context.Background(), state.dataset.Path)
	if err != nil {
		t.Fatal(err)
	}
	pixelHash, err := figurebundle.PixelSHA256(img)
	if err != nil {
		t.Fatal(err)
	}
	m := figurebundle.BundleManifest{Schema: figurebundle.Schema, ApplicationVersion: APP_VERSION, RendererSHA256: engineHash,
		SourceA: figurebundle.SourceIdentity{Path: state.dataset.Path, Size: state.dataset.Metadata.FileSize, ModifiedUnixNano: state.dataset.Metadata.ModifiedAt.UnixNano(), SHA256: sourceHash},
		Payload: payload, PixelSHA256: pixelHash, Width: img.Bounds().Dx(), Height: img.Bounds().Dy()}
	if state.compareBDataset != nil {
		b := state.compareBDataset
		hash, err := figurebundle.HashFile(context.Background(), b.Path)
		if err != nil {
			t.Fatal(err)
		}
		m.SourceB = &figurebundle.SourceIdentity{Path: b.Path, Size: b.Metadata.FileSize, ModifiedUnixNano: b.Metadata.ModifiedAt.UnixNano(), SHA256: hash}
	}
	dir := t.TempDir()
	o := prestackFigureCLIOptions{recipe: filepath.Join(dir, "recipe.json"), output: filepath.Join(dir, "rendered.png"), result: filepath.Join(dir, "result.json")}
	figureCLIWriteJSON(t, o.recipe, m)
	figureCLIWritePNG(t, filepath.Join(dir, "reference.png"), img)
	for _, name := range []string{"render.ps1", "README.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("protected bundle asset"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return o, m, img
}

func TestPrestackFigureCLICompareRendersFromIndependentSources(t *testing.T) {
	o, m, _ := figureCLIFixture(t, true)
	r, err := runPrestackFigureCommand(context.Background(), o, func() error { return nil })
	if err != nil || r.Status != "ok" || r.PixelSHA256 != m.PixelSHA256 || m.SourceB == nil || r.SourceBSHA256 != m.SourceB.SHA256 {
		t.Fatalf("Compare replay failed: %+v %v", r, err)
	}
	if err := compareResourceExclusiveRead(m.SourceA.Path); err != nil {
		t.Fatalf("CLI leaked source A Reader: %v", err)
	}
	if err := compareResourceExclusiveRead(m.SourceB.Path); err != nil {
		t.Fatalf("CLI leaked source B Reader: %v", err)
	}
}

func TestPrestackFigureCLIRendersRealSourceAndResultJSON(t *testing.T) {
	o, m, reference := figureCLIFixture(t)
	authorized := 0
	result, err := runPrestackFigureCommand(context.Background(), o, func() error { authorized++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if authorized != 1 || result.Status != "ok" || result.PixelSHA256 != m.PixelSHA256 || result.SourceASHA256 != m.SourceA.SHA256 || result.RendererSHA256 != m.RendererSHA256 {
		t.Fatalf("bad CLI result: %+v, authorizations=%d", result, authorized)
	}
	f, err := os.Open(o.output)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := figurebundle.PixelSHA256(decoded)
	if err != nil || hash != result.PixelSHA256 || decoded.Bounds() != reference.Bounds() {
		t.Fatalf("written PNG pixels changed: %s %v", hash, err)
	}
	raw, err := os.ReadFile(o.result)
	if err != nil {
		t.Fatal(err)
	}
	var report prestackFigureCLIResult
	if err := json.Unmarshal(raw, &report); err != nil || report != result {
		t.Fatalf("machine-readable result differs: %s %v", raw, err)
	}
	for _, forbidden := range []string{"amplitudes", "samples", "bgra", "indices", "base64", "png_bytes"} {
		if bytes.Contains(bytes.ToLower(raw), []byte(forbidden)) || bytes.Contains(bytes.ToLower(m.Payload), []byte(`"`+forbidden+`"`)) {
			t.Fatalf("inline amplitude/raster field leaked: %s", forbidden)
		}
	}
}

func TestPrestackFigureCLISourceOverrideAllowsIdenticalCopy(t *testing.T) {
	o, m, _ := figureCLIFixture(t)
	copyPath := filepath.Join(t.TempDir(), "relocated.sgy")
	b, err := os.ReadFile(m.SourceA.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(copyPath, time.Now(), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	o.sourceA = copyPath
	r, err := runPrestackFigureCommand(context.Background(), o, func() error { return nil })
	if err != nil || r.Status != "ok" {
		t.Fatalf("identical relocated source was rejected: %+v %v", r, err)
	}
}

func TestPrestackFigureCLIRejectsFailuresWithoutPNG(t *testing.T) {
	for _, kind := range []string{"license", "engine", "version", "source", "source_size", "payload_unknown", "payload_version", "reference", "pixel", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			o, m, reference := figureCLIFixture(t)
			authorize := func() error { return nil }
			ctx := context.Background()
			switch kind {
			case "license":
				authorize = func() error { return errors.New("activate the GUI application first") }
			case "engine":
				m.RendererSHA256 = strings.Repeat("0", 64)
			case "version":
				m.ApplicationVersion = "0.0.0"
			case "source", "source_size":
				f, err := os.OpenFile(m.SourceA.Path, os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				at := m.SourceA.Size - 1
				if kind == "source_size" {
					at = m.SourceA.Size
				}
				_, err = f.WriteAt([]byte{0xA9}, at)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "payload_unknown":
				m.Payload = append([]byte(`{"InjectedAmplitude":[1,2,3],`), m.Payload[1:]...)
			case "payload_version":
				var p prestackFigurePayload
				if err := json.Unmarshal(m.Payload, &p); err != nil {
					t.Fatal(err)
				}
				p.Version++
				m.Payload, _ = json.Marshal(p)
			case "reference", "pixel":
				reference.SetNRGBA(0, 0, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
				figureCLIWritePNG(t, filepath.Join(filepath.Dir(o.recipe), "reference.png"), reference)
				if kind == "pixel" {
					// Consistent reference+manifest still must not replace a real
					// source re-render: the recalculated image differs from both.
					m.PixelSHA256, _ = figurebundle.PixelSHA256(reference)
				}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			figureCLIWriteJSON(t, o.recipe, m)
			result, err := runPrestackFigureCommand(ctx, o, authorize)
			if err == nil || result.Status != "error" {
				t.Fatalf("failure accepted: %+v %v", result, err)
			}
			if _, err := os.Stat(o.output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failure produced output: %v", err)
			}
			raw, err := os.ReadFile(o.result)
			if err != nil || !bytes.Contains(raw, []byte(`"status": "error"`)) {
				t.Fatalf("failure lacks JSON status: %s %v", raw, err)
			}
			leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(o.recipe), ".figure-*.tmp"))
			if len(leftovers) != 0 {
				t.Fatalf("temporary files retained: %v", leftovers)
			}
		})
	}
}

func TestPrestackFigureCLIProtectedDestinationsAndHardLinks(t *testing.T) {
	o, m, _ := figureCLIFixture(t)
	exe, _ := os.Executable()
	protected := []string{m.SourceA.Path, o.recipe, exe, filepath.Join(filepath.Dir(o.recipe), "reference.png"), filepath.Join(filepath.Dir(o.recipe), "render.ps1"), filepath.Join(filepath.Dir(o.recipe), "README.txt")}
	for _, path := range protected {
		for _, resultCollision := range []bool{false, true} {
			options := o
			options.overwrite = true
			if resultCollision {
				options.result = path
			} else {
				options.output = path
			}
			before, err := figurebundle.HashFile(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runPrestackFigureCommand(context.Background(), options, func() error { t.Fatal("collision reached authorization"); return nil }); err == nil {
				t.Fatalf("protected collision accepted: %s", path)
			}
			after, _ := figurebundle.HashFile(context.Background(), path)
			if before != after {
				t.Fatalf("protected file modified: %s", path)
			}
		}
	}
	o.result = o.output
	if _, err := runPrestackFigureCommand(context.Background(), o, func() error { return nil }); err == nil {
		t.Fatal("output/result same path accepted")
	}
	o.result = ""
	link := filepath.Join(filepath.Dir(o.recipe), "alias.png")
	if err := os.Link(m.SourceA.Path, link); err != nil {
		t.Fatal(err)
	}
	o.output, o.overwrite = link, true
	if _, err := runPrestackFigureCommand(context.Background(), o, func() error { return nil }); err == nil {
		t.Fatal("hard-link alias could overwrite source")
	}
}

func TestPrestackFigureCommandFlagsAndNoAuthorizationBypass(t *testing.T) {
	if handled, code := handlePrestackFigureCommand([]string{"ordinary.sgy"}); handled || code != 0 {
		t.Fatal("normal GUI arguments intercepted")
	}
	o, err := parsePrestackFigureCommand([]string{"--render-recipe=x.json", "--output", "out.png", "--source-a", "A.sgy", "--source-b", "B.sgy", "--overwrite", "--result", "out.json"})
	if err != nil || o.recipe != "x.json" || !o.overwrite || o.result != "out.json" {
		t.Fatalf("flags lost: %+v %v", o, err)
	}
	for _, args := range [][]string{{"--render-recipe", "x"}, {"--render-recipe", "x", "--output", "y", "--skip-license"}, {"--render-recipe", "x", "--output", "y", "extra"}} {
		if _, err := parsePrestackFigureCommand(args); err == nil {
			t.Fatalf("unsafe/invalid flags accepted: %v", args)
		}
	}
}

func TestPrestackFigureCLIRejectsNonFigureExtensionsEvenWithOverwrite(t *testing.T) {
	o, _, _ := figureCLIFixture(t)
	for _, resultFile := range []bool{false, true} {
		for _, extension := range []string{".sgy", ".pidx", ".exe", ".txt", ".png"} {
			if !resultFile && extension == ".png" {
				continue
			}
			path := filepath.Join(t.TempDir(), "unrelated"+extension)
			original := []byte("unrelated file must remain untouched")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			options := o
			options.overwrite = true
			if resultFile {
				options.result = path
			} else {
				options.output = path
			}
			if _, err := runPrestackFigureCommand(context.Background(), options, func() error { t.Fatal("invalid extension reached authorization"); return nil }); err == nil {
				t.Fatalf("nonfigure destination accepted: %s", path)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("unrelated destination modified: %s %v", path, err)
			}
		}
	}
	o.output, o.result = filepath.Join(filepath.Dir(o.recipe), "UPPER.PNG"), filepath.Join(filepath.Dir(o.recipe), "UPPER.JSON")
	if err := guardPrestackFigureWrites(o, []string{o.recipe}); err != nil {
		t.Fatalf("valid uppercase extensions rejected: %v", err)
	}
}
