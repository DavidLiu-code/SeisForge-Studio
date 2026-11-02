//go:build windows

package main

import (
	"context"
	"os"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestackbatch"
)

func TestParsePrestackBatchCommand(t *testing.T) {
	o, err := parsePrestackBatchCommand([]string{"--batch-plan", "plan.json", "--batch-report", "run.json", "--overwrite"})
	if err != nil || o.plan != "plan.json" || o.report != "run.json" || !o.overwrite {
		t.Fatalf("parse batch plan: %+v, %v", o, err)
	}
	for _, args := range [][]string{{"--batch-plan"}, {"--batch-plan", "plan.json", "extra"}} {
		if _, err := parsePrestackBatchCommand(args); err == nil {
			t.Fatalf("expected invalid batch args: %#v", args)
		}
	}
}

func TestVerifyPrestackBatchSourceChecksRecordedIdentity(t *testing.T) {
	path := t.TempDir() + "\\source.sgy"
	data := []byte("metadata-only fixture")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPrestackBatchSource(context.Background(), path, prestackbatch.SourceIdentity{Path: path, Size: info.Size(), ModifiedUnixNano: info.ModTime().UnixNano()}); err != nil {
		t.Fatalf("identity should validate: %v", err)
	}
	if err := verifyPrestackBatchSource(context.Background(), path, prestackbatch.SourceIdentity{Path: path, Size: info.Size() + 1}); err == nil {
		t.Fatal("expected size mismatch")
	}
}
