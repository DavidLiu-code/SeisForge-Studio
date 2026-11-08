//go:build windows

package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func TestPrestackCompareReaderWorkerRejectsCompletionAfterBarrier(t *testing.T) {
	aPath, bPath := compareResourceFixture(t, "render_A.sgy"), compareResourceFixture(t, "render_B.sgy")
	aData, err := dataset.NewManager().Open(aPath)
	if err != nil {
		t.Fatal(err)
	}
	bData, err := dataset.NewManager().Open(bPath)
	if err != nil {
		t.Fatal(err)
	}
	savedState, savedUI, savedHwnd := prestackState, prestackUI, prestackHwnd
	prestackDeliveryMu.Lock()
	oldOwner, oldOwnerToken := prestackDeliveryHwnd, prestackDeliveryOwnerToken
	oldIndexGen, oldGatherGen := prestackDeliveryIndexGeneration, prestackDeliveryRenderGeneration
	oldBGen, oldCompareGen := prestackDeliveryCompareBGeneration, prestackDeliveryCompareRenderGeneration
	oldIndex, oldGather, oldB, oldCompare := prestackPendingIndex, prestackPendingRender, prestackPendingCompareB, prestackPendingCompareRender
	prestackDeliveryMu.Unlock()
	t.Cleanup(func() {
		prestackState, prestackUI, prestackHwnd = savedState, savedUI, savedHwnd
		prestackDeliveryMu.Lock()
		prestackDeliveryHwnd, prestackDeliveryOwnerToken = oldOwner, oldOwnerToken
		prestackDeliveryIndexGeneration, prestackDeliveryRenderGeneration = oldIndexGen, oldGatherGen
		prestackDeliveryCompareBGeneration, prestackDeliveryCompareRenderGeneration = oldBGen, oldCompareGen
		prestackPendingIndex, prestackPendingRender, prestackPendingCompareB, prestackPendingCompareRender = oldIndex, oldGather, oldB, oldCompare
		prestackDeliveryMu.Unlock()
	})
	prestackUI, prestackHwnd = prestackControls{}, 0
	rows := make([]prestackcore.CompareLogicalRow, 60)
	for i := range rows {
		rows[i] = prestackcore.CompareLogicalRow{Classification: prestackcore.CompareRowMatched, HasA: true, HasB: true, ATrace: int64(i), BTrace: int64(len(rows) - 1 - i), AIndex: i, BIndex: len(rows) - 1 - i}
	}
	for _, change := range []string{"current", "cancel", "resize", "close B", "workspace", "owner", "newer pending", "post failure"} {
		t.Run(change, func(t *testing.T) {
			generation := atomic.AddInt64(&prestackGeneration, 1)
			prestackState = prestackSession{dataset: aData, index: &prestackcore.PrestackIndex{}, compareBDataset: bData,
				compareBIndex: &prestackcore.PrestackIndex{}, page: 3, ownerToken: 2, workspaceGeneration: 3,
				datasetGeneration: 4, mappingGeneration: 5, selectionGeneration: 6, compareBGeneration: 7,
				compareBDatasetGeneration: 8, compareBMappingGeneration: 9, compareModeGeneration: 10,
				compareDisplayGeneration: 11, resizeGeneration: 12, sampleFirst: 2, sampleLast: 29,
				compareRenderGeneration: generation, compareRendering: true}
			token := prestackCompareRenderResult{owner: 17, ownerToken: 2, workspaceGeneration: 3,
				datasetGeneration: 4, aMappingGeneration: 5, selectionGeneration: 6, bGeneration: 7,
				bDatasetGeneration: 8, bMappingGeneration: 9, compareModeGeneration: 10, displayGeneration: 11,
				resizeGeneration: 12, sampleFirst: 2, sampleLast: 29, comparePage: 3, width: 20, height: 16,
				palette: 2, generation: generation}
			request := prestackCompareRenderRequest{token: token, aData: aData, bData: bData, rows: rows,
				denseRows: true, gain: 5, axisCompatible: true}
			prestackDeliveryMu.Lock()
			prestackDeliveryHwnd, prestackDeliveryOwnerToken = 17, 2
			prestackDeliveryCompareRenderGeneration = generation
			prestackPendingCompareRender = nil
			prestackDeliveryMu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready := make(chan *prestackCompareRenderResult, 1)
			release, done := make(chan struct{}), make(chan bool, 1)
			notifications := 0
			go func() {
				result := renderPrestackCompareFrame(ctx, request)
				ready <- result
				<-release // cancellation/replacement happens deterministically here
				done <- publishPrestackCompareRender(ctx, result, func(owner uintptr, gen int64) bool {
					notifications++
					return owner == 17 && gen == generation && change != "post failure"
				})
			}()
			var result *prestackCompareRenderResult
			select {
			case result = <-ready:
			case <-time.After(5 * time.Second):
				close(release)
				cancel()
				t.Fatal("real Reader render did not reach delivery barrier")
			}
			// Do not fail before releasing/joining the worker, even if a Reader
			// or buffer check reports a regression.
			if result.err != nil || result.bErr != nil || len(result.a) != 20*16*4 || len(result.b) != 20*16*4 || len(result.delta) != 20*16*4 {
				t.Errorf("real worker did not produce complete A/B/Delta buffers: A/B errors=%v/%v lengths=%d/%d/%d", result.err, result.bErr, len(result.a), len(result.b), len(result.delta))
			}
			for _, path := range []string{aPath, bPath} {
				if err := compareResourceExclusiveRead(path); err != nil {
					t.Errorf("Reader remained open at completed-frame barrier: %v", err)
				}
			}
			var newer *prestackCompareRenderResult
			switch change {
			case "cancel":
				cancel()
			case "resize":
				prestackState.resizeGeneration++
				// No cancel func is attached: this verifies the protected
				// generation gate independently of context cancellation.
				invalidatePrestackCompareRender()
			case "close B":
				closePrestackCompareB(nil)
			case "workspace":
				cancelPrestackJobs()
				prestackState.workspaceGeneration++
				prestackState.datasetGeneration++
			case "owner":
				prestackDeliveryMu.Lock()
				prestackDeliveryOwnerToken++
				prestackDeliveryMu.Unlock()
			case "newer pending":
				invalidatePrestackCompareRender()
				newer = &prestackCompareRenderResult{generation: prestackState.compareRenderGeneration}
				prestackDeliveryMu.Lock()
				prestackPendingCompareRender = newer
				prestackDeliveryMu.Unlock()
			}
			close(release)
			var published bool
			select {
			case published = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Compare worker failed to finish after barrier release")
			}
			prestackDeliveryMu.Lock()
			pending := prestackPendingCompareRender
			prestackDeliveryMu.Unlock()
			if change == "current" {
				if !published || notifications != 1 || pending != result {
					t.Fatal("current completion was not published")
				}
				if accepted, err := acceptPrestackCompareCompletion(result, generation, &prestackState, 17, 20, 16); !accepted || err != nil {
					t.Fatalf("current completion failed full UI token gate: accepted=%v err=%v", accepted, err)
				}
			} else {
				wantNotifications := 0
				if change == "post failure" {
					wantNotifications = 1
				}
				if published || notifications != wantNotifications || pending != newer {
					t.Fatalf("stale/failed completion changed pending UI state: published=%v messages=%d pending=%p want=%p", published, notifications, pending, newer)
				}
			}
			if prestackState.dataset != aData || prestackState.index == nil {
				t.Fatal("B/Compare lifecycle disturbed independent A")
			}
		})
	}
}
