//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

// This opt-in soak runs production paths and the same exact/readback oracles
// in one resident Windows test process. It does not replace desktop testing.
func TestQAContinuousPoststack(t *testing.T) {
	value := os.Getenv("SEISFORGE_QA_SOAK_DURATION")
	if value == "" {
		t.Skip("set SEISFORGE_QA_SOAK_DURATION, e.g. 60m, for a resident Windows soak")
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 || duration > 90*time.Minute {
		t.Fatalf("invalid soak duration %q (must be in (0, 90m])", value)
	}
	checkpoint := 2 * time.Minute
	if duration < checkpoint {
		checkpoint = duration / 4
	}
	started := time.Now()
	next := started.Add(checkpoint)
	tempPrefix := filepath.Join(os.TempDir(), "TestQAContinuousPoststack*")
	queryHandles := kernel32.NewProc("GetProcessHandleCount")
	queryGUI := user32.NewProc("GetGuiResources")
	queryMemory := kernel32.NewProc("K32GetProcessMemoryInfo")
	type processMemory struct {
		Size, Faults                   uint32
		PeakWorkingSet, WorkingSet     uintptr
		PeakPagedPool, PagedPool       uintptr
		PeakNonPagedPool, NonPagedPool uintptr
		PageFile, PeakPageFile         uintptr
		Private                        uintptr
	}
	const currentProcess = ^uintptr(0)
	sample := func(stage string, n int, work time.Duration) {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		var handles uint32
		queryHandles.Call(currentProcess, uintptr(unsafe.Pointer(&handles)))
		gdi, _, _ := queryGUI.Call(currentProcess, 0)
		user, _, _ := queryGUI.Call(currentProcess, 1)
		memory := processMemory{}
		memory.Size = uint32(unsafe.Sizeof(memory))
		queryMemory.Call(currentProcess, uintptr(unsafe.Pointer(&memory)), uintptr(memory.Size))
		var files int
		var tempBytes int64
		roots, _ := filepath.Glob(tempPrefix)
		for _, root := range roots {
			_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err == nil && !entry.IsDir() {
					files++
					if info, err := entry.Info(); err == nil {
						tempBytes += info.Size()
					}
				}
				return nil
			})
		}
		fmt.Printf("qa-soak stage=%s at=%s pid=%d loops=%d elapsed=%s assertions_time=%s handles=%d gdi=%d user=%d live_heap=%d private_bytes=%d working_set=%d goroutines=%d outstanding_temp_files=%d temp_bytes=%d\n",
			stage, time.Now().Format(time.RFC3339), os.Getpid(), n, time.Since(started).Round(time.Millisecond), work.Round(time.Millisecond), handles, gdi, user, ms.HeapAlloc, memory.Private, memory.WorkingSet, runtime.NumGoroutine(), files, tempBytes)
	}
	sample("start", 0, 0)
	var active time.Duration
	for cycle := 1; ; cycle++ {
		since := time.Now()
		if !t.Run(fmt.Sprintf("round-%06d", cycle), func(t *testing.T) {
			t.Run("open-A-B-filter-restore", TestViewFilterNativeApplyRestore)
			t.Run("zoom-and-gain-without-reanalysis", TestTraceScientificBoxClampsToInitialPanelAndKeepsAppliedSettings)
			t.Run("manual-time-and-rollback", TestTraceScientificManualScopeCommitsOnlyUsableResult)
			t.Run("stale-result-and-owner-close", TestTraceScientificLateCompletionKeepsLatestWholeSnapshotAndExport)
			t.Run("stage-and-segy-reread", TestCompareNumericalExportLocalAndCommittedStage)
			t.Run("owned-settings-close", TestSettingsCloseOnlyDestroysItsOwnNativeDialog)
			if cycle == 1 || cycle%50 == 0 {
				t.Run("full-formal-filter-B-and-exact-readback", TestCompareNumericalExportFullFormalCoverageAndExactCrop)
			}
		}) {
			sample("failed", cycle, active+time.Since(since))
			t.FailNow()
		}
		active += time.Since(since)
		now := time.Now()
		if cycle == 1 || !now.Before(next) {
			sample("checkpoint", cycle, active)
			next = now.Add(checkpoint)
		}
		if now.Sub(started) >= duration {
			sample("passed", cycle, active)
			break
		}
	}
}
