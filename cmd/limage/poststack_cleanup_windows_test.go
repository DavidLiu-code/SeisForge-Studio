//go:build windows

package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestPoststackRetiredResultWaitsForIndependentReaderClose(t *testing.T) {
	r := poststackGeneratedFixture(t)
	path, err := syscall.UTF16PtrFromString(r.Section.Part2)
	if err != nil {
		t.Fatal(err)
	}
	// A separate Reader denies DELETE sharing, as a still-running index worker can.
	h, err := syscall.CreateFile(path, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(h)
	retirePoststackSection(r.Section)
	retiredPoststackMu.Lock()
	found := false
	for _, s := range retiredPoststackSections {
		found = found || s == r.Section
	}
	retiredPoststackMu.Unlock()
	if !found {
		t.Fatal("failed cleanup lost its ownership reference")
	}
	syscall.CloseHandle(h)
	retryRetiredPoststackResults()
	if _, err := os.Stat(r.Section.Dir); !os.IsNotExist(err) {
		t.Fatalf("retired result remains after Reader close: %v", err)
	}
	if _, err := os.Stat(r.Section.SourcePath); err != nil {
		t.Fatalf("A changed: %v", err)
	}
}

func TestPoststackExportAcceptanceRechecksSource(t *testing.T) {
	r := poststackGeneratedFixture(t)
	e := poststackUIEvent{Export: true, Source: r.Section}
	if err := validatePoststackEventSource(&e); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(r.Section.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(r.Section.SourcePath, info.ModTime(), info.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := validatePoststackEventSource(&e); err == nil {
		t.Fatal("source change after worker completion was accepted")
	}
}
