//go:build windows

package figurebundle

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPowerShell51ScriptPreservesNativeArguments(t *testing.T) {
	// These checks always run, even when policy prevents executing an unsigned
	// local .ps1. Do not weaken policy merely to obtain a subprocess PASS.
	staticTool := `C:\工具's & unicode space\renderer's.exe`
	staticScript := []byte(renderScript(staticTool))
	if !bytes.HasPrefix(staticScript, []byte{0xef, 0xbb, 0xbf}) ||
		!bytes.Contains(staticScript, []byte(powerShellLiteral(staticTool))) ||
		!bytes.Contains(staticScript, []byte("ConvertTo-NativeArgument")) ||
		!bytes.Contains(staticScript, []byte("-WindowStyle Hidden")) {
		t.Fatal("script lost BOM, literal quoting, native argument quoting or hidden launch")
	}
	if bytes.Contains(staticScript, []byte("ExecutionPolicy")) {
		t.Fatal("generated script must not change or bypass execution policy")
	}
	powerShell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(powerShell); err != nil {
		t.Fatalf("Windows PowerShell 5.1 required: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// A parent PowerShell 7 process can pass a PSModulePath whose Security
	// module is incompatible with Windows PowerShell. Load its own built-in
	// cmdlet module explicitly; this reads policy without changing any scope.
	policyQuery := `$ErrorActionPreference = 'Stop'; Import-Module -Name (Join-Path $PSHOME 'Modules\Microsoft.PowerShell.Security\Microsoft.PowerShell.Security.psd1') -ErrorAction Stop; Get-ExecutionPolicy`
	policyCommand := exec.CommandContext(ctx, powerShell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", policyQuery)
	policyCommand.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	policyOutput, err := policyCommand.CombinedOutput()
	if err != nil {
		t.Fatalf("read effective execution policy: %v\n%s", err, policyOutput)
	}
	policy := strings.TrimSpace(string(policyOutput))
	switch policy {
	case "Restricted", "AllSigned":
		t.Skipf("native PowerShell subprocess skipped: effective policy %s disallows this unsigned script; static BOM/quoting checks passed; policy unchanged", policy)
	case "RemoteSigned", "Unrestricted", "Bypass":
		// Respect the already-effective policy; never set a different one.
	default:
		t.Fatalf("unrecognized effective execution policy %q", policy)
	}
	parent := t.TempDir()
	toolDirectory := filepath.Join(parent, "工具's & unicode space")
	if err := os.Mkdir(toolDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(toolDirectory, "renderer's & 实验.exe")
	stub := filepath.Join(parent, "native_probe.go")
	writeTestFile(t, stub, []byte(`package main
import("encoding/json";"os")
func main(){
 args:=os.Args[1:]; values:=map[string]string{}
 for i:=0;i+1<len(args);i++{if args[i]!="--overwrite"{values[args[i]]=args[i+1]}}
 result:=values["--result"];if result==""{os.Exit(9)}
 data,err:=json.Marshal(args);if err!=nil{os.Exit(10)}
 if err=os.WriteFile(result,data,0600);err!=nil{os.Exit(11)}
 if values["--source-a"]=="FAIL"{os.Exit(7)}
}`))
	build := exec.CommandContext(ctx, "go", "build", "-o", tool, stub)
	build.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile native argument probe: %v\n%s", err, output)
	}
	img := testImage()
	manifest := testManifest(t, img)
	manifest.SourceB = &SourceIdentity{Path: `C:\有 空格\B"quoted"\尾部\`, SHA256: manifest.SourceA.SHA256}
	dir := filepath.Join(parent, "bundle's & 图")
	if err := WriteBundle(context.Background(), dir, manifest, img, tool); err != nil {
		t.Fatal(err)
	}
	script, recipe := filepath.Join(dir, ScriptFilename), filepath.Join(dir, RecipeFilename)
	run := func(t *testing.T, output string, extra []string, sourceA, sourceB string, overwrite bool, exit int) {
		t.Helper()
		commandArgs := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-File", script, "-Output", output}
		commandArgs = append(commandArgs, extra...)
		command := exec.CommandContext(ctx, powerShell, commandArgs...)
		command.Dir = parent // $PSScriptRoot, not the current directory, owns recipe resolution.
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		text, err := command.CombinedOutput()
		if exit == 0 && err != nil {
			t.Fatalf("native script: %v\n%s", err, text)
		}
		if exit != 0 {
			if errorExit, ok := err.(*exec.ExitError); !ok || errorExit.ExitCode() != exit {
				t.Fatalf("script exit=%v, want %d\n%s", err, exit, text)
			}
		}
		var got []string
		if err := json.Unmarshal(readTestFile(t, output+".result.json"), &got); err != nil {
			t.Fatal(err)
		}
		want := []string{"--render-recipe", recipe, "--output", output, "--source-a", sourceA, "--result", output + ".result.json"}
		if sourceB != "" {
			want = append(want, "--source-b", sourceB)
		}
		if overwrite {
			want = append(want, "--overwrite")
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("native arguments changed\ngot: %#v\nwant:%#v", got, want)
		}
	}
	t.Run("defaults_unicode_and_quotes", func(t *testing.T) {
		run(t, filepath.Join(parent, "default's & 图.png"), nil, manifest.SourceA.Path, manifest.SourceB.Path, false, 0)
	})
	t.Run("source_tool_output_overrides", func(t *testing.T) {
		otherTool := filepath.Join(toolDirectory, "second tool.exe")
		writeTestFile(t, otherTool, readTestFile(t, tool))
		a, b := `C:\迁移 A's & source.sgy`, `D:\迁移 B & source.sgy`
		run(t, filepath.Join(parent, "override's & 图.png"), []string{"-Tool", otherTool, "-SourceA", a, "-SourceB", b, "-Overwrite"}, a, b, true, 0)
	})
	t.Run("nonzero_native_exit", func(t *testing.T) {
		run(t, filepath.Join(parent, "failed 图.png"), []string{"-SourceA", "FAIL"}, "FAIL", manifest.SourceB.Path, false, 7)
	})
}
