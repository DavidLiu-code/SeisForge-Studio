//go:build windows

package main

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
)

func lifecycleProjectLine(id string) *projectcore.CrookedProjectLine {
	return &projectcore.CrookedProjectLine{ID: id, Name: id, Path: id + ".sgy", Dataset: &dataset.SeismicDataset{Path: id + ".sgy"}}
}

func TestCrookedHideShowReschedulesOnlyUnfinishedProjectGeometry(t *testing.T) {
	preserveCrookedDeliveryGlobals(t)
	savedState := crookedState
	savedRenderGen := atomic.LoadInt64(&crookedRenderGen)
	savedClickGen := atomic.LoadInt64(&crookedMapClickGen)
	savedBaseDirty := crookedBaseDirty
	t.Cleanup(func() {
		crookedState = savedState
		atomic.StoreInt64(&crookedRenderGen, savedRenderGen)
		atomic.StoreInt64(&crookedMapClickGen, savedClickGen)
		crookedBaseDirty = savedBaseDirty
	})

	lines := []*projectcore.CrookedProjectLine{
		lifecycleProjectLine("active-unfinished"),
		lifecycleProjectLine("background-unfinished"),
		lifecycleProjectLine("already-ready"),
		lifecycleProjectLine("terminal-error"),
	}
	crookedState = crookedSession{
		project: &projectcore.CrookedProject{Lines: lines},
		projectLines: []crookedProjectLineState{
			{line: lines[0], loading: true},
			{line: lines[1], loading: true},
			{line: lines[2], loading: true, ready: true},
			{line: lines[3], loading: true, errorText: "bad headers"},
		},
		activeLine:           0,
		loading:              true,
		rendering:            true,
		pendingNavigation:    &crookedNavigationTarget{},
		pendingMapClick:      &crookedNavigationTarget{},
		projectGeometryReady: false,
	}
	atomic.StoreInt64(&crookedLoadGen, 100)
	atomic.StoreInt64(&crookedRenderGen, 200)
	atomic.StoreInt64(&crookedProjectGen, 300)
	atomic.StoreInt64(&crookedMapClickGen, 400)
	crookedPendingMu.Lock()
	crookedDeliveryHwnd = 0
	oldDeliveryEpoch := crookedDeliveryEpoch
	crookedPending = &crookedPrepareResult{}
	crookedProjectPending = []*crookedProjectGeometryResult{{}}
	crookedPendingMu.Unlock()

	suspendCrookedPreparationForHide()
	if atomic.LoadInt64(&crookedLoadGen) != 101 || atomic.LoadInt64(&crookedRenderGen) != 201 || atomic.LoadInt64(&crookedProjectGen) != 301 || atomic.LoadInt64(&crookedMapClickGen) != 401 {
		t.Fatal("Hide did not invalidate all Crooked worker/click generations together")
	}
	if !crookedState.projectGeometrySuspended || crookedState.loading || crookedState.rendering || crookedState.pendingNavigation != nil || crookedState.pendingMapClick != nil {
		t.Fatalf("Hide left transient project work alive: %+v", crookedState)
	}
	crookedPendingMu.Lock()
	deliveryEpoch, preparePending, projectPending := crookedDeliveryEpoch, crookedPending, crookedProjectPending
	crookedPendingMu.Unlock()
	if deliveryEpoch != oldDeliveryEpoch+1 || preparePending != nil || len(projectPending) != 0 {
		t.Fatalf("Hide did not atomically invalidate/drain deliveries: epoch=%d want=%d prepare=%+v project=%d", deliveryEpoch, oldDeliveryEpoch+1, preparePending, len(projectPending))
	}
	for index := range crookedState.projectLines {
		if crookedState.projectLines[index].loading {
			t.Fatalf("line %d retained stale loading state", index)
		}
	}
	if !crookedState.projectLines[2].ready || crookedState.projectLines[3].errorText != "bad headers" {
		t.Fatal("Hide discarded a completed or failed terminal state")
	}
	if got := crookedProjectResumeActiveLine(); got != 0 {
		t.Fatalf("active unfinished line was not eligible for foreground restart: %d", got)
	}
	if !crookedProjectLineNeedsGeometry(&crookedState.projectLines[1]) {
		t.Fatal("unfinished non-active line was not eligible for header-only background restart")
	}
	if crookedProjectLineNeedsGeometry(&crookedState.projectLines[2]) || crookedProjectLineNeedsGeometry(&crookedState.projectLines[3]) {
		t.Fatal("ready/error line would be scheduled a second time")
	}

	show := phase1FunctionSource(t, "app_phase1_windows.go", "showCrookedWorkspace")
	if !strings.Contains(show, "resumeCrookedProjectPreparation") {
		t.Fatal("Crooked Show does not resume an interrupted project")
	}
	resume := phase1FunctionSource(t, "crooked_windows.go", "resumeCrookedProjectPreparation")
	if strings.Index(resume, "activateCrookedProjectLine") < 0 || strings.Index(resume, "startCrookedProjectBackground") < 0 || !strings.Contains(resume, "atomic.LoadInt64(&crookedProjectGen)") {
		t.Fatal("project resume does not restart both active and background geometry paths")
	}
	background := phase1FunctionSource(t, "crooked_windows.go", "startCrookedProjectBackground")
	if !strings.Contains(background, "crookedProjectLineNeedsGeometry") || !strings.Contains(background, "BuildCrookedCachedProgress") || strings.Contains(background, "RenderTraces") {
		t.Fatal("background restart is not filtered header-only geometry work")
	}
}

func TestCrookedErrorRetryLeavesPublishedStateUntilTerminalResult(t *testing.T) {
	savedState := crookedState
	savedBaseDirty := crookedBaseDirty
	t.Cleanup(func() {
		crookedState = savedState
		crookedBaseDirty = savedBaseDirty
	})
	failed, ready := lifecycleProjectLine("retry-failed"), lifecycleProjectLine("ready")
	crookedState = crookedSession{
		project: &projectcore.CrookedProject{Lines: []*projectcore.CrookedProjectLine{failed, ready}},
		projectLines: []crookedProjectLineState{
			{line: failed, errorText: "old calibration error"},
			{line: ready, ready: true},
		},
		activeLine:           0,
		projectGeometryReady: true,
	}
	if done, total := crookedProjectRecognitionProgress(); done != 2 || total != 2 {
		t.Fatalf("terminal failure was not initially counted: %d/%d", done, total)
	}
	beginCrookedProjectLineLoading(&crookedState.projectLines[0])
	updateCrookedProjectPublication()
	if crookedState.projectLines[0].errorText != "" || !crookedState.projectLines[0].loading {
		t.Fatal("retry did not clear the terminal error before entering loading")
	}
	if done, total := crookedProjectRecognitionProgress(); done != 1 || total != 2 || crookedState.projectGeometryReady {
		t.Fatalf("loading retry was incorrectly counted as terminal: %d/%d published=%v", done, total, crookedState.projectGeometryReady)
	}

	// Only a new terminal result may make the project atomically publishable.
	crookedState.projectLines[0].loading = false
	crookedState.projectLines[0].errorText = "new terminal error"
	updateCrookedProjectPublication()
	if !crookedState.projectGeometryReady {
		t.Fatal("project was not republished after the retry reached a terminal state")
	}

	activate := phase1FunctionSource(t, "crooked_windows.go", "activateCrookedProjectLine")
	started := strings.Index(activate, "startCrookedPrepare")
	marked := strings.Index(activate, "beginCrookedProjectLineLoading")
	published := strings.Index(activate, "updateCrookedProjectPublication")
	if started < 0 || marked <= started || published <= marked {
		t.Fatal("active retry does not clear its error/update publication after a successful start")
	}
	readyHandler := phase1FunctionSource(t, "crooked_windows.go", "handleCrookedReady")
	calibration := strings.Index(readyHandler, "setCrookedProjectLineGeometry")
	if calibration < 0 {
		t.Fatal("active ready handler no longer calibrates project geometry")
	}
	loadingReset := strings.Index(readyHandler[calibration:], "projectState.loading = false")
	if loadingReset < 0 {
		t.Fatal("active calibration failure does not clear loading for a later retry")
	}
}

func TestCrookedShowRestartsInterruptedReadyLineSection(t *testing.T) {
	preserveCrookedDeliveryGlobals(t)
	savedState := crookedState
	savedRenderGen := atomic.LoadInt64(&crookedRenderGen)
	savedClickGen := atomic.LoadInt64(&crookedMapClickGen)
	t.Cleanup(func() {
		crookedState = savedState
		atomic.StoreInt64(&crookedRenderGen, savedRenderGen)
		atomic.StoreInt64(&crookedMapClickGen, savedClickGen)
	})

	line := lifecycleProjectLine("ready-active-section")
	crookedState = crookedSession{
		project:              &projectcore.CrookedProject{Lines: []*projectcore.CrookedProjectLine{line}},
		projectLines:         []crookedProjectLineState{{line: line, ready: true, loading: true}},
		activeLine:           0,
		dataset:              line.Dataset,
		geometry:             nil, // startCrookedPrepare cleared it before Hide.
		loading:              true,
		projectGeometryReady: true,
	}
	crookedPendingMu.Lock()
	crookedDeliveryHwnd = 0
	crookedPendingMu.Unlock()
	suspendCrookedPreparationForHide()
	if crookedState.loading || crookedState.projectLines[0].loading || !crookedState.projectGeometryReady {
		t.Fatal("Hide did not leave the ready canonical state with foreground work cancelled")
	}
	if got := crookedActiveSectionPrepareDataset(); got != line.Dataset {
		t.Fatal("Show would not restart the interrupted foreground section for an already-ready line")
	}

	crookedState.loading = true
	if got := crookedActiveSectionPrepareDataset(); got != nil {
		t.Fatal("an already-running foreground section would be started twice")
	}
	crookedState.loading = false
	crookedState.projectLines[0].ready = false
	if got := crookedActiveSectionPrepareDataset(); got != nil {
		t.Fatal("unfinished canonical geometry was incorrectly routed through the ready-line section restart")
	}

	show := phase1FunctionSource(t, "app_phase1_windows.go", "showCrookedWorkspace")
	lookup := strings.Index(show, "crookedActiveSectionPrepareDataset")
	start := strings.Index(show, "startCrookedPrepare")
	if lookup < 0 || start <= lookup {
		t.Fatal("Crooked Show does not restart an interrupted ready-line foreground section")
	}
}

func TestCrookedHeaderSpecChangeClosesPseudoBeforeGeometryRebuild(t *testing.T) {
	source := phase1FunctionSource(t, "crooked_windows.go", "applyCrookedHeaderSettings")
	closed := strings.Index(source, "closePseudoWindowForProjectChange")
	assigned := strings.Index(source, "crookedState.spec = spec")
	rebuilt := strings.Index(source, "startCrookedProjectBackground")
	if closed < 0 || assigned < 0 || rebuilt < 0 || closed >= assigned || assigned >= rebuilt {
		t.Fatalf("header-spec rebuild does not invalidate pseudo first: close=%d assign=%d rebuild=%d", closed, assigned, rebuilt)
	}
	if !strings.Contains(source, "projectGeometrySuspended = false") {
		t.Fatal("header-spec rebuild retained a suspended state from the previous geometry generation")
	}
	startProject := phase1FunctionSource(t, "crooked_windows.go", "startCrookedProject")
	if !strings.Contains(startProject, "projectGeometrySuspended = false") {
		t.Fatal("opening a new project retained the previous project's suspended state")
	}
}
