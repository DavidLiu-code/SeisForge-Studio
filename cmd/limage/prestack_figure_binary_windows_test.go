//go:build windows

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/figurebundle"
)

// Opt-in acceptance of the actual Windows GUI executable. Script execution is
// separately opt-in and follows the machine's existing PowerShell policy.
// Unlike unit tests, this uses the installed machine license with no bypass.
// No visible window or automation cursor is created. All data are temporary.
func TestPrestackFigureBuiltBinaryReplay(t *testing.T) {
	tool := os.Getenv("SEISFORGE_FIGURE_TEST_TOOL")
	if tool == "" {
		t.Skip("set SEISFORGE_FIGURE_TEST_TOOL to a freshly built, locally licensed EXE")
	}
	tool, err := filepath.Abs(tool)
	if err != nil {
		t.Fatal(err)
	}
	for _, compare := range []bool{false, true} {
		name := "Gather"
		if compare {
			name = "Compare"
		}
		t.Run(name, func(t *testing.T) {
			_, manifest, reference := figureCLIFixture(t, compare)
			var payload prestackFigurePayload
			if err := json.Unmarshal(manifest.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(t.TempDir(), "绘图 ' & replay.figure")
			job := prestackFigureExportJob{payload: payload, reference: reference,
				sourceA: manifest.SourceA.Path, executable: tool, directory: directory}
			if manifest.SourceB != nil {
				job.sourceB = manifest.SourceB.Path
			}
			ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
			defer stop()
			if err := buildPrestackFigureBundle(ctx, job, nil); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(directory, "复绘 ' & result.png")
			command := exec.CommandContext(ctx, tool, "--render-recipe", filepath.Join(directory, figurebundle.RecipeFilename),
				"--output", output, "--result", output+".result.json")
			if os.Getenv("SEISFORGE_FIGURE_TEST_SCRIPT") == "1" {
				command = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-File",
					filepath.Join(directory, figurebundle.ScriptFilename), "-Output", output)
			}
			command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			if log, err := command.CombinedOutput(); err != nil {
				diagnostic, _ := os.ReadFile(output + ".result.json")
				t.Fatalf("real EXE replay failed (license required): %v\n%s\n%s", err, log, diagnostic)
			}
			raw, err := os.ReadFile(output + ".result.json")
			if err != nil {
				t.Fatal(err)
			}
			var result prestackFigureCLIResult
			if err := json.Unmarshal(raw, &result); err != nil || result.Status != "ok" || result.PixelSHA256 != manifest.PixelSHA256 {
				t.Fatalf("binary replay was not pixel-identical: %s (%v)", raw, err)
			}
			t.Logf("actual EXE replay (script=%t): %s, %dx%d, pixel SHA256=%s", os.Getenv("SEISFORGE_FIGURE_TEST_SCRIPT") == "1", name, result.Width, result.Height, result.PixelSHA256)
		})
	}
}
