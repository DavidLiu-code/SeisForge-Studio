//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/registry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
)

// This creates a hidden test-owned native window, not an interactive desktop
// session. Only the public noop fixture gets test-local fingerprint approval;
// installed/private modules and the production authorization path are untouched.
func TestPoststackNativeRunCommandGeneratesArtifactBAndDelta(t *testing.T) {
	t.Run("Run", func(t *testing.T) { testPoststackNativeProcessing(t, false) })
	t.Run("PreviewDuringDisplayChanges", func(t *testing.T) { testPoststackNativeProcessing(t, true) })
}

func testPoststackNativeProcessing(t *testing.T, preview bool) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	isolatePoststackWorker(t)
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)
	moduleDir := filepath.Join(dir, "module")
	if err := os.Mkdir(moduleDir, 0700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(moduleDir, "algorithm-noop.exe")
	cmd := exec.Command("go", "build", "-o", exe, "../algorithm-noop")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build public noop: %v\n%s", err, out)
	}
	desc := api.AlgorithmDescriptor{ID: "demo.noop", Version: "1.0.0", Name: "Test public noop", ProtocolVersion: api.ProtocolVersion, Executable: "algorithm-noop.exe", MinimumHostVersion: "1.10.4", InputKinds: []string{"section2d"}, OutputKinds: []string{"section2d"}, SupportsProgress: true, SupportsCancel: true}
	manifest, _ := json.Marshal(desc)
	if err := os.WriteFile(filepath.Join(moduleDir, "manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	reg := registry.New("1.10.4")
	if issues, err := reg.Scan(context.Background(), []string{moduleDir}); err != nil || len(issues) != 0 {
		t.Fatalf("fixture scan: %v %+v", err, issues)
	}
	module, ok := reg.Get(desc.ID)
	if !ok {
		t.Fatal("public noop not registered")
	}
	oldRegistry, oldTrust := poststackAlgorithms, poststackModuleTrust
	poststackAlgorithms, poststackModuleTrust = reg, map[string]bool{module.Fingerprint(): true}
	t.Cleanup(func() { poststackAlgorithms, poststackModuleTrust = oldRegistry, oldTrust })

	oldTD, oldAPath, oldBPath := compareTD, compareAPath, compareBPath
	oldA, oldB, oldD := compareA, compareB, compareD
	oldWorkspace, oldBVisible, oldDiff := compareWorkspaceActive, compareBVisible, compareShowDiff
	oldMode, oldFirst, oldLast := compareMode, compareSampleStart, compareSampleEnd
	oldLine, oldX0, oldX1 := compareLineCoord, compareLineXMin, compareLineXMax
	oldTrace0, oldTrace1, oldStep := compareTraceStart, compareTraceEnd, compareTraceStep
	oldAuto, oldDeltaReason := compareAutoGeom, compareDeltaReason
	oldPalette, oldGain, oldAGC := comparePaletteIndex, gainPercent, agc
	oldGeometryPending := compareAlgorithmGeometryPending
	compareAlgorithmGeometryPending = 0
	oldPendingDisplay := poststackPendingBDisplay
	oldGen, oldEpoch := atomic.LoadInt64(&compareGen), atomic.LoadInt64(&poststackEpoch)
	generatedPoststackMu.Lock()
	oldResults := generatedPoststackResults
	generatedPoststackResults = nil
	generatedPoststackMu.Unlock()
	compareAsyncMu.Lock()
	oldPrepare := comparePendingPrepare
	comparePendingPrepare = nil
	compareAsyncMu.Unlock()
	oldPrepareCancel := comparePrepareCancel
	comparePrepareCancel = nil
	compareTD = nil
	t.Cleanup(func() {
		cancelPoststackComparePrepare()
		if compareTD != nil {
			compareTD.close()
		}
		compareTD = nil
		releaseGeneratedPoststackResults()
		generatedPoststackMu.Lock()
		generatedPoststackResults = oldResults
		generatedPoststackMu.Unlock()
		compareAsyncMu.Lock()
		if comparePendingPrepare != nil && comparePendingPrepare.data != nil {
			comparePendingPrepare.data.close()
		}
		comparePendingPrepare = oldPrepare
		compareAsyncMu.Unlock()
		comparePrepareCancel = oldPrepareCancel
		compareTD, compareAPath, compareBPath = oldTD, oldAPath, oldBPath
		compareA, compareB, compareD = oldA, oldB, oldD
		compareWorkspaceActive, compareBVisible, compareShowDiff = oldWorkspace, oldBVisible, oldDiff
		compareMode, compareSampleStart, compareSampleEnd = oldMode, oldFirst, oldLast
		compareLineCoord, compareLineXMin, compareLineXMax = oldLine, oldX0, oldX1
		compareTraceStart, compareTraceEnd, compareTraceStep = oldTrace0, oldTrace1, oldStep
		compareAutoGeom, compareDeltaReason = oldAuto, oldDeltaReason
		comparePaletteIndex, gainPercent, agc = oldPalette, oldGain, oldAGC
		compareAlgorithmGeometryPending = oldGeometryPending
		poststackPendingBDisplay = oldPendingDisplay
		atomic.StoreInt64(&compareGen, oldGen)
		atomic.StoreInt64(&poststackEpoch, oldEpoch)
	})
	source := filepath.Join(dir, "A.sgy")
	if err := testsegy.Write(source, testsegy.Options{Rows: 2, Cols: 15, Samples: 64, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	fa, err := segy.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	ga, _, err := fa.BuildGeometryIndexCached(189, 193, 1)
	if err != nil {
		_ = fa.Close()
		t.Fatal(err)
	}
	compareTD = &compareTimeData{fa: fa, ga: ga, maxSampleA: 63}
	compareAPath, compareBPath = source, ""
	compareWorkspaceActive, compareBVisible, compareShowDiff, compareAutoGeom = true, false, false, false
	compareMode, compareSampleStart, compareSampleEnd = 0, 3, 57
	compareLineCoord, compareLineXMin, compareLineXMax = 1000, 2002, 2011
	compareTraceStart, compareTraceEnd, compareTraceStep = 0, -1, 1
	var callbackErr error
	callback := syscall.NewCallback(func(h uintptr, msg uint32, w, l uintptr) uintptr {
		switch msg {
		case WM_COMMAND:
			return cadzowWndProc(h, msg, w, l)
		case WM_CADZOW_DONE:
			handleCadzowDone()
			return 0
		case WM_COMPARE_READY:
			compareAsyncMu.Lock()
			if comparePendingPrepare != nil {
				callbackErr = comparePendingPrepare.err
			}
			compareAsyncMu.Unlock()
			if callbackErr == nil {
				handlePrepareReady()
			}
			return 0
		}
		r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), w, l)
		return r
	})
	class := u16(fmt.Sprintf("PoststackNativeRunTest_%d", time.Now().UnixNano()))
	wc := WNDCLASSEX{CbSize: uint32(unsafe.Sizeof(WNDCLASSEX{})), WndProc: callback, ClassName: class}
	if atom, _, e := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		t.Fatal(e)
	}
	unregister := user32.NewProc("UnregisterClassW")
	t.Cleanup(func() { unregister.Call(uintptr(unsafe.Pointer(class)), 0) })
	h, _, e := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(u16("hidden processing fixture"))), WS_OVERLAPPEDWINDOW, 0, 0, 1000, 700, 0, 0, 0, 0)
	if h == 0 {
		t.Fatal(e)
	}
	compareHwnd, cadzowHwnd, cadzowOwner = h, h, h
	t.Cleanup(func() { pDestroyWindow.Call(h); compareHwnd, cadzowHwnd, cadzowOwner = 0, 0, 0 })
	t.Cleanup(func() {
		if poststackTaskBusy() {
			requestPoststackCancel(false)
			deadline := time.Now().Add(10 * time.Second)
			for poststackTaskBusy() && time.Now().Before(deadline) {
				pollPoststackWorkerCompletion()
				time.Sleep(time.Millisecond)
			}
			if poststackTaskBusy() {
				t.Error("fixture worker did not acknowledge cancellation before native window cleanup")
			}
		}
	})
	cz.algorithm = createCtrl(h, "COMBOBOX", "", WS_CHILD|CBS_DROPDOWNLIST|CBS_HASSTRINGS, 0, 0, 200, 200, IDCZ_ALGORITHM)
	cz.algorithmIDs = []string{desc.ID}
	pSendMessageW.Call(cz.algorithm, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(desc.Name))))
	pSendMessageW.Call(cz.algorithm, CB_SETCURSEL, 0, 0)
	cz.timeout = createCtrl(h, "EDIT", "1", WS_CHILD, 0, 0, 50, 24, IDCZ_TIMEOUT)
	cz.start = createCtrl(h, "BUTTON", "Run", WS_CHILD|BS_PUSHBUTTON, 0, 0, 80, 24, IDCZ_START)
	cz.preview = createCtrl(h, "BUTTON", "Preview", WS_CHILD|BS_PUSHBUTTON, 0, 0, 80, 24, IDCZ_PREVIEW)
	cc.ilByte = createCtrl(h, "EDIT", "189", WS_CHILD, 0, 0, 50, 24, IDCMP_ILBYTE)
	cc.xlByte = createCtrl(h, "EDIT", "193", WS_CHILD, 0, 0, 50, 24, IDCMP_XLBYTE)
	if cz.start == 0 || cz.preview == 0 || cz.timeout == 0 || cz.algorithm == 0 || cc.ilByte == 0 || cc.xlByte == 0 {
		t.Fatal("native fixture controls unavailable")
	}
	// Authorization's native modal loop can dispatch owner events. Validate
	// again after it returns instead of using stale pre-dialog trace indices.
	authorizationStamp := capturePoststackViewStamp()
	if !poststackAuthorizationContextValid(authorizationStamp, h) {
		t.Fatal("unchanged authorization context rejected")
	}
	compareSampleEnd++
	if poststackAuthorizationContextValid(authorizationStamp, h) {
		t.Fatal("changed authorization sample window accepted")
	}
	compareSampleEnd--
	if poststackAuthorizationContextValid(authorizationStamp, h+1) {
		t.Fatal("replaced authorization dialog accepted")
	}
	savedTD := compareTD
	compareTD = nil
	if poststackAuthorizationContextValid(authorizationStamp, h) {
		t.Fatal("closed A accepted after authorization")
	}
	compareTD = savedTD
	_, comparePrepareCancel = context.WithCancel(context.Background())
	updateCadzowAlgorithmUI()
	isEnabled := user32.NewProc("IsWindowEnabled")
	if on, _, _ := isEnabled.Call(cz.preview); on != 0 {
		t.Fatal("preview available against an old Reader during Prepare")
	}
	startCadzowTask(true)
	if poststackTaskBusy() || !strings.Contains(algorithmStatus, "正在准备") {
		t.Fatalf("pending Prepare did not block preview: %s", algorithmStatus)
	}
	cancelPoststackComparePrepare()
	updateCadzowAlgorithmUI()
	algorithmStatus = "就绪"
	button := cz.start
	if preview {
		button = cz.preview
	}
	pSendMessageW.Call(button, 0x00F5, 0, 0) // BM_CLICK -> native parent WM_COMMAND -> startCadzowTask
	if !poststackTaskBusy() {
		t.Fatalf("Run command did not launch worker: %s", algorithmStatus)
	}
	if poststackInputViewChanged() {
		t.Fatal("unchanged initial view marked stale")
	}
	if preview {
		before := activePoststackWorker.stamp
		pMoveWindow.Call(h, 0, 0, 1130, 740, 0)
		if after := capturePoststackViewStamp(); before.Width == after.Width || before.Height == after.Height {
			t.Fatal("fixture did not resize the native Compare client")
		}
		comparePaletteIndex, gainPercent, agc = 1, 13, !agc
		renderCompareLine()
		if poststackInputViewChanged() || poststackTaskCancelling() {
			t.Fatal("resizing/recoloring Compare cancelled a preview of unchanged samples")
		}
	}
	peek := user32.NewProc("PeekMessageW")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && (poststackTaskBusy() || len(compareB.bgra) == 0 || len(compareD.bgra) == 0) {
		var m MSG
		for {
			got, _, _ := peek.Call(uintptr(unsafe.Pointer(&m)), h, 0, 0, 1)
			if got == 0 {
				break
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
		if callbackErr != nil {
			t.Fatalf("B prepare failed: %v", callbackErr)
		}
		if !poststackTaskBusy() && strings.HasPrefix(algorithmStatus, "失败") {
			t.Fatal(algorithmStatus)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if poststackTaskBusy() || compareTD == nil || compareTD.fb == nil || len(compareA.bgra) == 0 || len(compareB.bgra) == 0 || len(compareD.bgra) == 0 {
		t.Fatalf("native Run did not complete A/B/Delta: status=%s pending=%v A=%d B=%d D=%d reason=%s", algorithmStatus, poststackTaskBusy(), len(compareA.bgra), len(compareB.bgra), len(compareD.bgra), compareDeltaReason)
	}
	_, _, lineA, errA := compareTD.ga.LineTraceNumbers(0, 1000)
	_, _, lineB, errB := compareTD.gb.LineTraceNumbers(0, 1000)
	if errA != nil || errB != nil || lineA != 1000 || lineB != lineA {
		t.Fatalf("generated Copy lost real Inline geometry: A=%d B=%d errors=%v/%v", lineA, lineB, errA, errB)
	}
	for i, value := range compareD.indices {
		if value != compareD.indices[0] {
			t.Fatalf("Copy difference is not uniform zero at %d", i)
		}
	}
	results := formalPoststackResults()
	if preview {
		if len(results) != 0 {
			t.Fatal("native preview added a formal result")
		}
		results = generatedPoststackResults
	}
	if len(results) != 1 || len(results[0].Section.TraceIndices) != 10 || results[0].Section.SampleStart != 3 || results[0].Section.SampleEnd != 57 {
		t.Fatalf("current visible trace/sample window lost: %+v", results)
	}
	if results[0].Preview != preview {
		t.Fatal("native command lost preview/Run semantics")
	}
	if results[0].RecordPath == "" {
		t.Fatal("successful run record path missing")
	}
	// A is now looking at a larger window than the previously captured B.
	// Reloading that B should not shrink A's sample range or turn the disabled
	// difference pane back on. The same-geometry overlap remains drawable.
	compareSampleStart, compareSampleEnd, compareShowDiff = 1, 60, false
	beforeReload, _ := currentCompareViewSnapshot()
	var beforeFrame, afterFrame RECT
	algorithmGetWindowRect.Call(h, uintptr(unsafe.Pointer(&beforeFrame)))
	loadGeneratedPoststackResult(results[0])
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && (comparePrepareCancel != nil || len(compareB.bgra) == 0) {
		var m MSG
		for {
			got, _, _ := peek.Call(uintptr(unsafe.Pointer(&m)), h, 0, 0, 1)
			if got == 0 {
				break
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
		if callbackErr != nil {
			t.Fatalf("B reload failed: %v", callbackErr)
		}
		time.Sleep(2 * time.Millisecond)
	}
	afterReload, _ := currentCompareViewSnapshot()
	algorithmGetWindowRect.Call(h, uintptr(unsafe.Pointer(&afterFrame)))
	if len(compareB.bgra) == 0 || comparePrepareCancel != nil || compareShowDiff || compareLineCoord != 1000 ||
		afterReload != beforeReload || afterFrame != beforeFrame {
		t.Fatalf("B reload changed Compare geometry: frame %v -> %v, view %+v -> %+v, B=%d, IL=%d, difference=%v",
			beforeFrame, afterFrame, beforeReload, afterReload, len(compareB.bgra), compareLineCoord, compareShowDiff)
	}
	if after, e := os.ReadFile(source); e != nil || !bytes.Equal(original, after) {
		t.Fatal("A fixture changed")
	}
	t.Logf("native BM_CLICK -> Run -> artifact B -> matched geometry -> A/B/Delta complete (%d traces x %d samples)", len(results[0].Section.TraceIndices), results[0].Section.SampleEnd-results[0].Section.SampleStart+1)
}
