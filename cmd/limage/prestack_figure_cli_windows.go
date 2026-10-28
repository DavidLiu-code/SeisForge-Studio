//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/figurebundle"
)

type prestackFigureCLIOptions struct {
	recipe, output, sourceA, sourceB, result string
	overwrite                                bool
}

type prestackFigureCLIResult struct {
	Schema         string `json:"schema"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	Output         string `json:"output,omitempty"`
	PixelSHA256    string `json:"pixel_sha256,omitempty"`
	RendererSHA256 string `json:"renderer_sha256,omitempty"`
	SourceASHA256  string `json:"source_a_sha256,omitempty"`
	SourceBSHA256  string `json:"source_b_sha256,omitempty"`
	Width          int    `json:"width,omitempty"`
	Height         int    `json:"height,omitempty"`
}

// This entry runs before HWND/font creation. It never invokes activation UI,
// auto-detection, project loading, or a live workspace.
func handlePrestackFigureCommand(args []string) (handled bool, exitCode int) {
	for _, arg := range args {
		if arg == "--render-recipe" || strings.HasPrefix(arg, "--render-recipe=") {
			handled = true
			break
		}
	}
	if !handled {
		return false, 0
	}
	options, err := parsePrestackFigureCommand(args)
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		_, err = runPrestackFigureCommand(ctx, options, authorizePrestackFigureCommand)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Figure render failed:", err)
		return true, 1
	}
	return true, 0
}

func parsePrestackFigureCommand(args []string) (prestackFigureCLIOptions, error) {
	var o prestackFigureCLIOptions
	f := flag.NewFlagSet("render-recipe", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.recipe, "render-recipe", "", "figure recipe.json")
	f.StringVar(&o.output, "output", "", "verified output PNG")
	f.StringVar(&o.sourceA, "source-a", "", "identical source A at another location")
	f.StringVar(&o.sourceB, "source-b", "", "identical source B at another location")
	f.StringVar(&o.result, "result", "", "machine-readable result JSON")
	f.BoolVar(&o.overwrite, "overwrite", false, "replace an existing output/result")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 || strings.TrimSpace(o.recipe) == "" || strings.TrimSpace(o.output) == "" {
		return o, errors.New("use --render-recipe recipe.json --output image.png [--source-a path] [--source-b path] [--overwrite] [--result result.json]")
	}
	return o, nil
}

func authorizePrestackFigureCommand() error {
	machine, _, err := machineFingerprint()
	if err == nil {
		_, err = loadInstalledLicense(machine)
	}
	if err != nil {
		return fmt.Errorf("an installed license for this machine is required; activate the GUI application first: %w", err)
	}
	return nil
}

// Only authorization is injected for UI-free tests; there is deliberately no
// command-line or environment license bypass. Rendering always uses real SEG-Y.
func runPrestackFigureCommand(ctx context.Context, o prestackFigureCLIOptions, authorize func() error) (result prestackFigureCLIResult, runErr error) {
	result.Schema, result.Status = "seisforge.figure.result.v1", "error"
	executable, err := os.Executable()
	if err != nil {
		return result, err
	}
	manifest, err := figurebundle.ReadManifest(o.recipe)
	if err != nil {
		// Unknown/malformed identity cannot authorize even a diagnostic write.
		return result, fmt.Errorf("read figure recipe: %w", err)
	}
	aPath, bPath := manifest.SourceA.Path, ""
	if o.sourceA != "" {
		aPath = o.sourceA
	}
	if manifest.SourceB != nil {
		bPath = manifest.SourceB.Path
	}
	if o.sourceB != "" {
		bPath = o.sourceB
	}
	protected := []string{o.recipe, executable, manifest.SourceA.Path, aPath}
	if manifest.SourceB != nil {
		protected = append(protected, manifest.SourceB.Path)
	}
	if bPath != "" {
		protected = append(protected, bPath)
	}
	for _, name := range []string{"recipe.json", "reference.png", "render.ps1", "README.txt"} {
		protected = append(protected, filepath.Join(filepath.Dir(o.recipe), name))
	}
	guard := func() error { return guardPrestackFigureWrites(o, protected) }
	if err := guard(); err != nil {
		return result, err
	}
	for _, target := range []string{o.output, o.result} {
		if target == "" {
			continue
		}
		if _, err := os.Stat(target); err == nil && !o.overwrite {
			return result, fmt.Errorf("destination already exists (use --overwrite): %s", target)
		}
	}
	result.Output, _ = filepath.Abs(o.output)
	// Error reports are written only after identities and all destination aliases
	// have been checked. Recheck immediately before each atomic publication too.
	defer func() {
		if runErr != nil {
			result.Status, result.Error = "error", runErr.Error()
		}
		if o.result == "" {
			return
		}
		if err := guard(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("result path guard: %w", err))
			return
		}
		if err := writePrestackFigureResult(o.result, result, o.overwrite); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("write result JSON: %w", err))
		}
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if authorize == nil {
		return result, errors.New("figure authorization is required")
	}
	if err := authorize(); err != nil {
		return result, err
	}
	if manifest.Schema != figurebundle.Schema || manifest.ApplicationVersion != APP_VERSION {
		return result, errors.New("recipe schema or application version does not match this renderer")
	}
	engineHash, err := figurebundle.HashFile(ctx, executable)
	if err != nil {
		return result, err
	}
	result.RendererSHA256 = engineHash
	if !strings.EqualFold(engineHash, manifest.RendererSHA256) {
		return result, errors.New("renderer SHA-256 differs; use the exact application binary recorded by this recipe")
	}
	var payload prestackFigurePayload
	decoder := json.NewDecoder(bytes.NewReader(manifest.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return result, fmt.Errorf("invalid figure payload: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return result, errors.New("figure payload must contain exactly one JSON object")
	}
	if err := payload.validate(); err != nil {
		return result, err
	}
	if payload.Version != prestackFigurePayloadVersion || payload.HasB != (manifest.SourceB != nil) || (!payload.HasB && o.sourceB != "") ||
		int(payload.Crop.Right-payload.Crop.Left) != manifest.Width || int(payload.Crop.Bottom-payload.Crop.Top) != manifest.Height ||
		payload.SourceAMetadata.FileSize != manifest.SourceA.Size || (payload.HasB && payload.SourceBMetadata.FileSize != manifest.SourceB.Size) {
		return result, errors.New("figure payload source identities, version, or dimensions disagree with the manifest")
	}
	if err := verifyPrestackFigureReference(ctx, filepath.Join(filepath.Dir(o.recipe), "reference.png"), manifest); err != nil {
		return result, err
	}
	if err := verifyPrestackFigureSource(ctx, aPath, manifest.SourceA); err != nil {
		return result, fmt.Errorf("source A: %w", err)
	}
	if manifest.SourceB != nil {
		if err := verifyPrestackFigureSource(ctx, bPath, *manifest.SourceB); err != nil {
			return result, fmt.Errorf("source B: %w", err)
		}
	}
	img, err := RenderPrestackFigure(ctx, payload, aPath, bPath)
	if err != nil {
		return result, fmt.Errorf("render figure: %w", err)
	}
	// Full hashes bracket the real render; mtime is not an identity requirement,
	// so byte-identical copies can be supplied by source override flags.
	if err := verifyPrestackFigureSource(ctx, aPath, manifest.SourceA); err != nil {
		return result, fmt.Errorf("source A changed during render: %w", err)
	}
	if manifest.SourceB != nil {
		if err := verifyPrestackFigureSource(ctx, bPath, *manifest.SourceB); err != nil {
			return result, fmt.Errorf("source B changed during render: %w", err)
		}
	}
	actual, err := figurebundle.PixelSHA256(img)
	if err != nil {
		return result, err
	}
	if img.Bounds().Dx() != manifest.Width || img.Bounds().Dy() != manifest.Height || !strings.EqualFold(actual, manifest.PixelSHA256) {
		return result, errors.New("re-rendered pixels differ from the captured figure; no PNG was published")
	}
	if err := guard(); err != nil {
		return result, err
	}
	if err := figurebundle.WriteVerifiedPNG(ctx, o.output, img, manifest.PixelSHA256, o.overwrite); err != nil {
		return result, err
	}
	result.Status, result.PixelSHA256 = "ok", actual
	result.Width, result.Height = img.Bounds().Dx(), img.Bounds().Dy()
	result.SourceASHA256 = manifest.SourceA.SHA256
	if manifest.SourceB != nil {
		result.SourceBSHA256 = manifest.SourceB.SHA256
	}
	return result, nil
}

func verifyPrestackFigureSource(ctx context.Context, path string, identity figurebundle.SourceIdentity) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != identity.Size {
		return errors.New("source file size or type differs from the recipe")
	}
	sum, err := figurebundle.HashFile(ctx, path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, identity.SHA256) {
		return errors.New("source SHA-256 differs from the recipe")
	}
	return nil
}

func verifyPrestackFigureReference(ctx context.Context, path string, manifest figurebundle.BundleManifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open reference image: %w", err)
	}
	defer f.Close()
	config, err := png.DecodeConfig(f)
	if err != nil || config.Width != manifest.Width || config.Height != manifest.Height {
		return errors.New("reference PNG dimensions or format disagree with the recipe")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	img, err := png.Decode(f)
	if err != nil {
		return err
	}
	hash, err := figurebundle.PixelSHA256(img)
	if err != nil {
		return err
	}
	if !strings.EqualFold(hash, manifest.PixelSHA256) {
		return errors.New("reference pixel SHA-256 differs from the recipe")
	}
	// The decoded image is validation-only, never supplied to the renderer or
	// PNG writer. The output must be calculated independently from source samples.
	return ctx.Err()
}

func guardPrestackFigureWrites(o prestackFigureCLIOptions, protected []string) error {
	if !strings.EqualFold(filepath.Ext(o.output), ".png") {
		return errors.New("--output must use a .png extension; source data/cache files cannot be render targets")
	}
	if o.result != "" && !strings.EqualFold(filepath.Ext(o.result), ".json") {
		return errors.New("--result must use a .json extension")
	}
	for _, target := range []string{o.output, o.result} {
		if target == "" {
			continue
		}
		if strings.Contains(filepath.Base(target), ":") {
			return errors.New("alternate data streams are not valid figure outputs")
		}
		for _, source := range protected {
			if source == "" {
				continue
			}
			same, err := samePrestackFigurePath(target, source)
			if err != nil {
				return err
			}
			if same {
				return fmt.Errorf("output/result would overwrite a protected input: %s", source)
			}
		}
		if info, err := os.Stat(target); err == nil && !info.Mode().IsRegular() {
			return errors.New("figure output/result must be a regular file")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if o.result != "" {
		same, err := samePrestackFigurePath(o.output, o.result)
		if err != nil {
			return err
		}
		if same {
			return errors.New("output PNG and result JSON must be different files")
		}
	}
	return nil
}

var pFigureFinalPath = kernel32.NewProc("GetFinalPathNameByHandleW")
var pFigureMoveFile = kernel32.NewProc("MoveFileExW")

func samePrestackFigurePath(a, b string) (bool, error) {
	aInfo, ae := os.Stat(a)
	bInfo, be := os.Stat(b)
	if ae == nil && be == nil && os.SameFile(aInfo, bInfo) {
		return true, nil
	}
	aa, err := canonicalPrestackFigurePath(a)
	if err != nil {
		return false, err
	}
	bb, err := canonicalPrestackFigurePath(b)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(aa, bb), nil
}

// Resolve existing ancestors by open handle, including junction/symlink parent
// aliases for an output that does not exist yet. Unlike EvalSymlinks this does
// not require enumeration permission on every ancestor directory.
func canonicalPrestackFigurePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current, suffix := abs, ""
	for {
		f, err := os.Open(current)
		if err == nil {
			defer f.Close()
			buffer := make([]uint16, 32768)
			n, _, callErr := pFigureFinalPath.Call(f.Fd(), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
			runtime.KeepAlive(f)
			if n == 0 || n >= uintptr(len(buffer)) {
				return "", fmt.Errorf("resolve figure path: %v", callErr)
			}
			resolved := syscall.UTF16ToString(buffer[:n])
			if strings.HasPrefix(resolved, `\\?\UNC\`) {
				resolved = `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`)
			} else {
				resolved = strings.TrimPrefix(resolved, `\\?\`)
			}
			return filepath.Join(resolved, suffix), nil
		}
		if errors.Is(err, os.ErrNotExist) && filepath.Dir(current) == current {
			// A copied bundle can retain an original path on a drive no longer
			// mounted here. Textual identity still protects that unavailable path.
			return filepath.Clean(abs), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("resolve figure path %s: %w", current, err)
		}
		suffix = filepath.Join(filepath.Base(current), suffix)
		current = filepath.Dir(current)
	}
}

func writePrestackFigureResult(path string, result prestackFigureCLIResult, overwrite bool) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".figure-result-*.tmp")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(result)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	from, err := syscall.UTF16PtrFromString(temp)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	flags := uintptr(0x8) // MOVEFILE_WRITE_THROUGH
	if overwrite {
		flags |= 0x1 // MOVEFILE_REPLACE_EXISTING; no delete-before-rename window.
	}
	ok, _, callErr := pFigureMoveFile.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), flags)
	if ok == 0 {
		return callErr
	}
	return nil
}
