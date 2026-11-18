package poststack

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAtomicWriteRejectsExistingAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	if err := AtomicWrite(context.Background(), path, []byte("one"), false); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(context.Background(), path, []byte("two"), false); err == nil {
		t.Fatal("expected existing output rejection")
	}
	if err := AtomicWrite(context.Background(), path, []byte("two"), true); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "two" {
		t.Fatalf("got %q %v", got, err)
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".seisforge-poststack-") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestAtomicWriteCancellationDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "result")
	if err := AtomicWrite(ctx, path, []byte("data"), false); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected output: %v", err)
	}
}

func TestCopyFileAtomicAndHash(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.sgy")
	dst := filepath.Join(dir, "b.sgy")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CopyFileAtomic(context.Background(), src, dst, false); err != nil {
		t.Fatal(err)
	}
	h1, err := FileSHA256(src)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := FileSHA256(dst)
	if err != nil || h1 != h2 {
		t.Fatalf("hash mismatch: %q %q %v", h1, h2, err)
	}
}

func TestAtomicConcurrentPublishNeverOverwritesWinner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "winner")
	var successes atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if AtomicWrite(context.Background(), path, []byte("complete"), false) == nil {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful publishers=%d", successes.Load())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "complete" {
		t.Fatalf("%q %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v %v", entries, err)
	}
}

func TestCopySameSourceRejectedEvenWithOverwrite(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(src, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CopyFileAtomic(context.Background(), src, src, true); err == nil {
		t.Fatal("source overwrite accepted")
	}
	got, err := os.ReadFile(src)
	if err != nil || string(got) != "original" {
		t.Fatalf("source changed: %q %v", got, err)
	}
}

func TestCopyCancellationAndExistingTargetPreserved(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	if err := os.WriteFile(src, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CopyFileAtomic(ctx, src, dst, true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := CopyFileAtomic(context.Background(), src, dst, false); err == nil {
		t.Fatal("overwrote existing target")
	}
	data, _ := os.ReadFile(dst)
	if string(data) != "target" {
		t.Fatal("existing file changed")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("temporary files leaked: %v", entries)
	}
}
