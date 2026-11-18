//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/figurebundle"
)

func figureExportFixture(t *testing.T) prestackFigureExportJob {
	t.Helper()
	state, payload := figureGatherFixture(t, 1280, 760, 0, false)
	reference, err := PaintPrestackFigurePayload(payload, &state)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return prestackFigureExportJob{payload: payload, reference: reference, sourceA: state.dataset.Path,
		executable: executable, directory: filepath.Join(t.TempDir(), "current.figure")}
}

func assertFigureNotPublished(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("unexpected target after rejected export: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed export left staging content: %v", entries)
	}
}

func TestPrestackFigureExportBuildVerifiesFullSourcesAndPixels(t *testing.T) {
	job := figureExportFixture(t)
	var stages []int
	if err := buildPrestackFigureBundle(context.Background(), job, func(percent int, _ string) { stages = append(stages, percent) }); err != nil {
		t.Fatal(err)
	}
	manifest, err := figurebundle.ReadManifest(filepath.Join(job.directory, "recipe.json"))
	if err != nil {
		t.Fatal(err)
	}
	fullHash, err := figurebundle.HashFile(context.Background(), job.sourceA)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SourceA.SHA256 != fullHash || manifest.SourceA.Size != job.payload.SourceAMetadata.FileSize || manifest.SourceA.Path != job.sourceA {
		t.Fatal("manifest does not identify the complete actual source file")
	}
	pixelHash, err := figurebundle.PixelSHA256(job.reference)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.PixelSHA256 != pixelHash || manifest.Width != job.reference.Bounds().Dx() || manifest.Height != job.reference.Bounds().Dy() {
		t.Fatal("reference identity mismatch")
	}
	if manifest.SourceB != nil {
		t.Fatal("Gather export fabricated a B dependency")
	}
	entries, err := os.ReadDir(job.directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("bundle must contain only recipe/reference/script/readme, got %v", entries)
	}
	if len(stages) < 6 || stages[len(stages)-1] != 100 {
		t.Fatalf("missing verified completion progress: %v", stages)
	}
	if len(prestackFigureWorkerSlot) != 0 {
		t.Fatal("worker slot leaked after completion")
	}
}

func TestPrestackFigureExportRejectsWrongReference(t *testing.T) {
	job := figureExportFixture(t)
	job.reference.Pix[0] ^= 1
	err := buildPrestackFigureBundle(context.Background(), job, nil)
	if err == nil || !strings.Contains(err.Error(), "does not exactly match") {
		t.Fatalf("wrong reference accepted: %v", err)
	}
	assertFigureNotPublished(t, job.directory)
}

func TestPrestackFigureExportDetectsAmplitudeMutationWithRestoredMTime(t *testing.T) {
	job := figureExportFixture(t)
	mutated := false
	err := buildPrestackFigureBundle(context.Background(), job, func(percent int, _ string) {
		if percent != 80 {
			return
		}
		before, err := os.Stat(job.sourceA)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(job.sourceA, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		// Mutate a real amplitude byte, not any sampled header/cache identity.
		offset := job.payload.SourceAMetadata.DataStart + 240 + 7
		value := []byte{0}
		if _, err = file.ReadAt(value, offset); err != nil {
			file.Close()
			t.Fatal(err)
		}
		value[0] ^= 1
		if _, err = file.WriteAt(value, offset); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err = file.Close(); err != nil {
			t.Fatal(err)
		}
		if err = os.Chtimes(job.sourceA, before.ModTime(), before.ModTime()); err != nil {
			t.Fatal(err)
		}
		mutated = true
	})
	if !mutated || err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("amplitude mutation escaped second full hash: mutated=%v err=%v", mutated, err)
	}
	assertFigureNotPublished(t, job.directory)
}

func TestPrestackFigureExportCancelAndExistingTargetAreSafe(t *testing.T) {
	job := figureExportFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	err := buildPrestackFigureBundle(ctx, job, func(percent int, _ string) {
		if percent == 15 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel was not honored: %v", err)
	}
	assertFigureNotPublished(t, job.directory)
	if len(prestackFigureWorkerSlot) != 0 {
		t.Fatal("canceled worker retained resource slot")
	}
	if err := os.Mkdir(job.directory, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(job.directory, "keep.txt")
	if err := os.WriteFile(marker, []byte("user-owned"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := buildPrestackFigureBundle(context.Background(), job, nil); err == nil {
		t.Fatal("existing directory replaced")
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "user-owned" {
		t.Fatalf("existing output touched: %q %v", content, err)
	}
}

func TestPrestackFigureExportCanceledQueuedJobDoesNotStart(t *testing.T) {
	job := figureExportFixture(t)
	prestackFigureWorkerSlot <- struct{}{}
	defer func() { <-prestackFigureWorkerSlot }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := buildPrestackFigureBundle(ctx, job, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancel not honored: %v", err)
	}
	assertFigureNotPublished(t, job.directory)
}

func TestPrestackFigureExportDeliveryRejectsCanceledAndOldOwners(t *testing.T) {
	oldButton := prestackUI.figureExport
	prestackUI.figureExport = 0
	t.Cleanup(func() { cancelPrestackFigureExport(); prestackUI.figureExport = oldButton })
	cancelPrestackFigureExport()
	ctx, cancel := context.WithCancel(context.Background())
	token := prestackFigureExportToken{generation: 41, owner: 99, ownerToken: 7, workspaceGeneration: 8, aDatasetGeneration: 9, bDatasetGeneration: 10, hasB: true}
	prestackFigureDelivery.Lock()
	prestackFigureDelivery.active, prestackFigureDelivery.cancel = token, cancel
	prestackFigureDelivery.Unlock()
	notifications := 0
	notify := func(owner uintptr) bool { notifications++; return owner == token.owner }
	old := token
	old.ownerToken++
	if publishPrestackFigureExport(prestackFigureExportResult{token: old}, notify) || postPrestackFigureProgress(prestackFigureExportProgress{token: old}, notify) {
		t.Fatal("old owner was delivered")
	}
	if notifications != 0 {
		t.Fatal("stale result notified UI")
	}
	if !postPrestackFigureProgress(prestackFigureExportProgress{token: token, percent: 50}, notify) || !publishPrestackFigureExport(prestackFigureExportResult{token: token}, notify) {
		t.Fatal("current job was not delivered")
	}
	current := token
	current.generation = 0 // UI snapshots omit the job sequence.
	current.hasB = false   // A snapshot can survive switching away from Compare.
	if !prestackFigureExportSourceMatches(token, current) {
		t.Fatal("view-only change invalidated immutable export")
	}
	current.bDatasetGeneration++
	if prestackFigureExportSourceMatches(token, current) {
		t.Fatal("B dataset replacement accepted")
	}
	cancelPrestackFigureExport()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("cancel function not invoked")
	}
	if publishPrestackFigureExport(prestackFigureExportResult{token: token}, notify) || postPrestackFigureProgress(prestackFigureExportProgress{token: token}, notify) {
		t.Fatal("canceled job resurrected delivery")
	}
	prestackFigureDelivery.Lock()
	pending := prestackFigureDelivery.done != nil || prestackFigureDelivery.progress != nil
	prestackFigureDelivery.Unlock()
	if pending {
		t.Fatal("cancel retained pending delivery")
	}
}
