//go:build windows

package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
)

func preserveCrookedDeliveryGlobals(t *testing.T) {
	t.Helper()
	crookedPendingMu.Lock()
	savedEpoch, savedHWND := crookedDeliveryEpoch, crookedDeliveryHwnd
	savedPending, savedRender := crookedPending, crookedRenderPending
	savedProject := crookedProjectPending
	savedPost := crookedPostWindowMessage
	crookedPendingMu.Unlock()
	savedLoad := atomic.LoadInt64(&crookedLoadGen)
	savedProjectGen := atomic.LoadInt64(&crookedProjectGen)
	savedSpecGen := atomic.LoadInt64(&crookedSpecGen)
	t.Cleanup(func() {
		crookedPendingMu.Lock()
		crookedDeliveryEpoch, crookedDeliveryHwnd = savedEpoch, savedHWND
		crookedPending, crookedRenderPending = savedPending, savedRender
		crookedProjectPending = savedProject
		crookedPostWindowMessage = savedPost
		crookedPendingMu.Unlock()
		atomic.StoreInt64(&crookedLoadGen, savedLoad)
		atomic.StoreInt64(&crookedProjectGen, savedProjectGen)
		atomic.StoreInt64(&crookedSpecGen, savedSpecGen)
	})
}

func TestCrookedDelayedMapClickKeepsReleaseHitAcrossViewportChange(t *testing.T) {
	line, geometry := crookedGeometryFixFixture(t, "line-delayed-click", []float64{0, 5, 11, 18}, []float64{0, 3, 1, 6})
	state := crookedProjectLineState{line: line, geometry: geometry, ready: true}
	state.fingerprint = crookedGeometryFingerprint(line.ID, geometry)
	state.mapPath = canonicalCrookedMapPath(&state, crookedCanonicalMapPointLimit)

	previousState := crookedState
	previousProjectGen := atomic.LoadInt64(&crookedProjectGen)
	previousSpecGen := atomic.LoadInt64(&crookedSpecGen)
	previousClickGen := atomic.LoadInt64(&crookedMapClickGen)
	t.Cleanup(func() {
		crookedState = previousState
		atomic.StoreInt64(&crookedProjectGen, previousProjectGen)
		atomic.StoreInt64(&crookedSpecGen, previousSpecGen)
		atomic.StoreInt64(&crookedMapClickGen, previousClickGen)
	})

	atomic.StoreInt64(&crookedProjectGen, 23)
	atomic.StoreInt64(&crookedSpecGen, 5)
	atomic.StoreInt64(&crookedMapClickGen, 91)
	crookedState = crookedSession{project: &projectcore.CrookedProject{}, projectLines: []crookedProjectLineState{state}, projectGeometryReady: true}
	wantTrace := geometry.TraceIndices[len(geometry.TraceIndices)-1]
	hit := crookedMapHit{LineID: line.ID, LineIndex: 0, TraceIndex: wantTrace, Position: len(geometry.TraceIndices) - 1, Fingerprint: state.fingerprint, Valid: true}
	crookedState.pendingMapClick = crookedMapTargetFromHit(hit, 812.5)

	// Simulate resize/zoom and a completely different pixel path during the
	// system double-click interval.  The committed click must still be the
	// structured hit captured at button release, never a fresh pixel hit-test.
	bounds := crookedMapPathBounds(state.mapPath)
	crookedState.geometryViewport = pseudo3dcore.XYRange{XMin: bounds.XMin, XMax: bounds.XMax, YMin: bounds.YMin, YMax: bounds.YMax, Valid: true}
	for i := range crookedState.projectLines[0].mapPath.Points {
		crookedState.projectLines[0].mapPath.Points[i].X += 1e6
		crookedState.projectLines[0].mapPath.Points[i].Y -= 1e6
	}
	got := takeCrookedMapClick(91)
	if got == nil || got.lineID != line.ID || got.traceIndex != wantTrace || got.geometryFingerprint != state.fingerprint || got.timeMS != 812.5 {
		t.Fatalf("delayed click was changed by the later viewport: %+v", got)
	}
}

func TestCrookedPrepareDeliveryRejectsStaleEpochAndPostFailure(t *testing.T) {
	preserveCrookedDeliveryGlobals(t)
	atomic.StoreInt64(&crookedLoadGen, 41)
	atomic.StoreInt64(&crookedProjectGen, 7)
	atomic.StoreInt64(&crookedSpecGen, 3)
	crookedPendingMu.Lock()
	crookedDeliveryEpoch, crookedDeliveryHwnd = 12, 99
	crookedPending, crookedRenderPending, crookedProjectPending = nil, nil, nil
	crookedPendingMu.Unlock()

	posts := 0
	crookedPostWindowMessage = func(uintptr, uint32, uintptr, uintptr) bool {
		posts++
		return true
	}
	postCrookedReady(&crookedPrepareResult{deliveryEpoch: 11, gen: 41, projectGen: 7, specGen: 3})
	if posts != 0 || crookedPending != nil {
		t.Fatalf("stale lifecycle result reached HWND delivery: posts=%d pending=%+v", posts, crookedPending)
	}

	crookedPostWindowMessage = func(uintptr, uint32, uintptr, uintptr) bool {
		posts++
		return false
	}
	postCrookedReady(&crookedPrepareResult{deliveryEpoch: 12, gen: 41, projectGen: 7, specGen: 3})
	crookedPendingMu.Lock()
	pending, hwnd, epoch := crookedPending, crookedDeliveryHwnd, crookedDeliveryEpoch
	crookedPendingMu.Unlock()
	if posts != 1 || pending != nil || hwnd != 0 || epoch != 13 {
		t.Fatalf("failed PostMessage did not atomically invalidate/drain delivery: posts=%d pending=%+v hwnd=%d epoch=%d", posts, pending, hwnd, epoch)
	}
}

func TestCrookedPrepareDeliveryCannotInstallAfterResetDrain(t *testing.T) {
	preserveCrookedDeliveryGlobals(t)
	atomic.StoreInt64(&crookedLoadGen, 52)
	atomic.StoreInt64(&crookedProjectGen, 8)
	atomic.StoreInt64(&crookedSpecGen, 4)
	crookedPendingMu.Lock()
	crookedDeliveryEpoch, crookedDeliveryHwnd = 20, 101
	crookedPending, crookedRenderPending, crookedProjectPending = nil, nil, nil
	crookedPendingMu.Unlock()

	enteredPost := make(chan struct{})
	releasePost := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(releasePost) }) })
	crookedPostWindowMessage = func(uintptr, uint32, uintptr, uintptr) bool {
		close(enteredPost)
		<-releasePost
		return true
	}
	delivered := make(chan struct{})
	go func() {
		postCrookedReady(&crookedPrepareResult{deliveryEpoch: 20, gen: 52, projectGen: 8, specGen: 4})
		close(delivered)
	}()
	<-enteredPost

	drained := make(chan struct{})
	go func() {
		invalidateCrookedDeliveries(false)
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("reset drained while worker installation/notification was not atomic")
	case <-time.After(25 * time.Millisecond):
	}
	once.Do(func() { close(releasePost) })
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("worker delivery did not finish")
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("reset did not finish draining")
	}
	crookedPendingMu.Lock()
	pending, epoch := crookedPending, crookedDeliveryEpoch
	crookedPendingMu.Unlock()
	if pending != nil || epoch != 21 {
		t.Fatalf("old worker installed a result after reset drain: pending=%+v epoch=%d", pending, epoch)
	}
}
