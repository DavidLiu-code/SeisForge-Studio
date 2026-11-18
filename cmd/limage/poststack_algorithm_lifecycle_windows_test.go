//go:build windows

package main

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
)

func isolatePoststackWorker(t *testing.T) {
	t.Helper()
	isolatePoststackMailbox(t)
	oldWorker := activePoststackWorker
	oldDialog, oldOwner := cadzowHwnd, cadzowOwner
	oldStatus, oldSource, oldProgress := algorithmStatus, algorithmStatusSource, algorithmProgress
	oldLog, oldRecord := algorithmLog, algorithmRecordPath
	oldLogWindow, oldLogEdit := algorithmLogWindow, algorithmLogEdit
	activePoststackWorker = nil
	cadzowHwnd, cadzowOwner = 0, 0
	algorithmLogWindow, algorithmLogEdit = 0, 0
	t.Cleanup(func() {
		activePoststackWorker = oldWorker
		cadzowHwnd, cadzowOwner = oldDialog, oldOwner
		algorithmStatus, algorithmStatusSource, algorithmProgress = oldStatus, oldSource, oldProgress
		algorithmLog, algorithmRecordPath = oldLog, oldRecord
		algorithmLogWindow, algorithmLogEdit = oldLogWindow, oldLogEdit
	})
}

func TestPoststackUserCancellationWaitsForWorkerCleanup(t *testing.T) {
	isolatePoststackWorker(t)
	ctx, event := beginPoststackJob()
	w := activePoststackWorker
	r := poststackGeneratedFixture(t)
	poststackMailboxMu.Lock()
	poststackMailbox = &poststackUIEvent{Generation: event.Generation, Token: event.Token, Result: r}
	poststackMailboxMu.Unlock()
	requestPoststackCancel(false)
	if !errors.Is(ctx.Err(), context.Canceled) || !poststackTaskCancelling() {
		t.Fatal("cancel did not invalidate/cancel the worker")
	}
	if cadzowCancel == nil || atomic.LoadInt32(&compareBusyCount) != 1 {
		t.Fatal("controls unlocked before the worker cleanup acknowledgement")
	}
	if pollPoststackWorkerCompletion() {
		t.Fatal("worker cleanup was acknowledged prematurely")
	}
	if _, err := os.Stat(r.Section.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending result was not retired: %v", err)
	}
	// Only the worker's final cleanup path sends this acknowledgement.
	event.Done, event.Err = true, context.Canceled
	w.completed <- event
	if !pollPoststackWorkerCompletion() {
		t.Fatal("cleanup acknowledgement was discarded as a stale token")
	}
	if cadzowCancel != nil || activePoststackWorker != nil || atomic.LoadInt32(&compareBusyCount) != 0 {
		t.Fatal("acknowledged worker retained running state")
	}
}

func TestPoststackStartCommandHasVisiblePreflightFeedback(t *testing.T) {
	isolatePoststackWorker(t)
	oldTD, oldWorkspace := compareTD, compareWorkspaceActive
	compareTD, compareWorkspaceActive = nil, false
	t.Cleanup(func() { compareTD, compareWorkspaceActive = oldTD, oldWorkspace })
	for _, id := range []int{IDCZ_START, IDCZ_PREVIEW} {
		algorithmStatus = "就绪"
		cadzowWndProc(0, WM_COMMAND, uintptr(id), 0)
		if algorithmStatus != "请进入比并等待 A 图像完成" {
			t.Fatalf("command %d did not enter preflight: %q", id, algorithmStatus)
		}
	}
}

func TestPoststackBusyStartShowsReasonWithoutStartingSuccessor(t *testing.T) {
	isolatePoststackWorker(t)
	_, event := beginPoststackJob()
	w := activePoststackWorker
	algorithmStatus = "就绪"
	startCadzowTask(false)
	if algorithmStatus != "处理任务仍在运行，请等待完成或取消运行" || activePoststackWorker != w {
		t.Fatal("busy start silently returned or replaced the active worker")
	}
	requestPoststackCancel(false)
	startCadzowTask(true)
	if !strings.Contains(algorithmStatus, "等待算法进程和临时文件释放") || activePoststackWorker != w {
		t.Fatal("cancelling start silently returned or replaced the active worker")
	}
	event.Done, event.Err = true, context.Canceled
	w.completed <- event
	pollPoststackWorkerCompletion()
}

func TestPoststackConfirmedCloseDefersUntilCancellationAcknowledged(t *testing.T) {
	isolatePoststackWorker(t)
	_, event := beginPoststackJob()
	w := activePoststackWorker
	requestPoststackCancel(true)
	if !w.closeAfter || !w.cancelling || cadzowCancel == nil {
		t.Fatal("confirmed close did not retain the live worker until cleanup")
	}
	requestPoststackCancel(false)
	if !w.closeAfter {
		t.Fatal("second cancel lost the pending close request")
	}
	event.Done, event.Err = true, context.Canceled
	w.completed <- event
	pollPoststackWorkerCompletion()
	if activePoststackWorker != nil || cadzowCancel != nil {
		t.Fatal("close acknowledgement retained task ownership")
	}
}

func TestPoststackCompareCloseDefersUntilWorkerCleanup(t *testing.T) {
	isolatePoststackWorker(t)
	ctx, event := beginPoststackJob()
	w := activePoststackWorker
	if requestPoststackCompareClose(w.owner + 1) {
		t.Fatal("an unrelated owner cannot defer/cancel Compare")
	}
	if !requestPoststackCompareClose(w.owner) || !w.closeCompare || !w.quiet {
		t.Fatal("Compare close did not defer destruction until cleanup")
	}
	if !errors.Is(ctx.Err(), context.Canceled) || cadzowCancel == nil || activePoststackWorker != w {
		t.Fatal("Compare close released task ownership before cleanup acknowledgement")
	}
	if pollPoststackWorkerCompletion() {
		t.Fatal("Compare close acknowledged cleanup prematurely")
	}
	event.Done, event.Err = true, context.Canceled
	w.completed <- event
	if !pollPoststackWorkerCompletion() || activePoststackWorker != nil || cadzowCancel != nil {
		t.Fatal("Compare close did not reap the acknowledged worker")
	}
	if requestPoststackCompareClose(w.owner) {
		t.Fatal("the repeated WM_CLOSE must now be allowed to destroy Compare")
	}
}

func TestPoststackContextInvalidationWaitsWithoutAcceptingOldResult(t *testing.T) {
	isolatePoststackWorker(t)
	ctx, event := beginPoststackJob()
	w := activePoststackWorker
	r := poststackGeneratedFixture(t)
	cancelCadzowTask()
	if !errors.Is(ctx.Err(), context.Canceled) || !w.quiet || cadzowCancel == nil {
		t.Fatal("context invalidation must cancel but retain worker ownership")
	}
	event.Result, event.Done = r, true
	w.completed <- event
	pollPoststackWorkerCompletion()
	if _, err := os.Stat(r.Section.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old completion retained its temporary result: %v", err)
	}
	if _, err := os.Stat(r.Section.SourcePath); err != nil {
		t.Fatalf("old completion touched A: %v", err)
	}
}

func TestPoststackStaleCompletionExplainsSameInputButResetsNewInput(t *testing.T) {
	for _, newInput := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-A", true: "new-A"}[newInput], func(t *testing.T) {
			isolatePoststackWorker(t)
			oldA, oldLast := compareAPath, compareSampleEnd
			compareAPath = "A.sgy"
			t.Cleanup(func() { compareAPath, compareSampleEnd = oldA, oldLast })
			_, event := beginPoststackJob()
			w := activePoststackWorker
			event.Done = true
			algorithmStatus = "正在处理"
			if newInput {
				compareAPath = "new-A.sgy"
			} else {
				compareSampleEnd++
			}
			w.completed <- event
			pollPoststackWorkerCompletion()
			if poststackTaskBusy() || activePoststackWorker != nil {
				t.Fatal("stale completion retained worker ownership")
			}
			if newInput {
				if algorithmStatus != "" || algorithmStatusSource != compareAPath {
					t.Fatalf("new A inherited old status: %q", algorithmStatus)
				}
			} else if !strings.Contains(algorithmStatus, "上下文已变化（时间窗）") {
				t.Fatalf("same A did not retain the specific cancellation reason: %q", algorithmStatus)
			}
		})
	}
}

func TestPoststackInputViewGuardDistinguishesRepaintFromRangeChange(t *testing.T) {
	isolatePoststackWorker(t)
	oldLast := compareSampleEnd
	t.Cleanup(func() { compareSampleEnd = oldLast })
	_, event := beginPoststackJob()
	if poststackInputViewChanged() {
		t.Fatal("unchanged view invalidated a live worker")
	}
	compareSampleEnd++
	if !poststackInputViewChanged() {
		t.Fatal("changed sample window did not invalidate a worker")
	}
	requestPoststackCancel(false)
	event.Done, event.Err = true, context.Canceled
	activePoststackWorker.completed <- event
	pollPoststackWorkerCompletion()
}

func TestPoststackProcessingContextSeparatesDisplayFromRawSelection(t *testing.T) {
	base := poststackViewStamp{Owner: 5, Epoch: 2, Workspace: true, A: "A.sgy", B: "B.sgy", Mode: 0, Line: 1870,
		First: 4, Last: 350, TraceFirst: 0, TraceLast: 2272, TraceStep: 1, InlineByte: 189, CrosslineByte: 21,
		X0: 184, X1: 2456, Width: 1100, Height: 700, Algorithm: "demo", ModuleFingerprint: "module", ParameterFingerprint: "params"}
	display := base
	display.Width, display.Height, display.Palette, display.Display = 900, 510, 3, "wiggle"
	display.Gain, display.Clip, display.Minimum, display.Maximum, display.AGC, display.Limits = 4, 2, math.NaN(), 7, true, true
	display.Dataset, display.Geometry, display.Render = 2, 8, 16
	if change := poststackJobContextChange(base, display, false); change != "" {
		t.Fatalf("display-only changes invalidated raw processing: %q", change)
	}
	if change := poststackJobContextChange(base, display, true); change == "" {
		t.Fatal("the existing export guard was unexpectedly relaxed")
	}
	for _, c := range []struct {
		want string
		edit func(*poststackViewStamp)
	}{
		{"对比工作区", func(s *poststackViewStamp) { s.Epoch++ }},
		{"A/B 文件", func(s *poststackViewStamp) { s.A = "new-A.sgy" }},
		{"A/B 文件", func(s *poststackViewStamp) { s.B = "" }},
		{"道头映射", func(s *poststackViewStamp) { s.CrosslineByte++ }},
		{"道头映射", func(s *poststackViewStamp) { s.TraceOrder = true }},
		{"剖面方向或线号", func(s *poststackViewStamp) { s.Line++ }},
		{"剖面方向或线号", func(s *poststackViewStamp) { s.Mode++ }},
		{"时间窗", func(s *poststackViewStamp) { s.First++ }},
		{"道范围", func(s *poststackViewStamp) { s.TraceFirst++ }},
		{"道范围", func(s *poststackViewStamp) { s.TraceLast-- }},
		{"道范围", func(s *poststackViewStamp) { s.TraceStep++ }},
		{"道范围", func(s *poststackViewStamp) { s.X1-- }},
		{"算法模块或参数", func(s *poststackViewStamp) { s.ModuleFingerprint = "changed" }},
		{"算法模块或参数", func(s *poststackViewStamp) { s.ParameterFingerprint = "changed" }},
	} {
		changed := base
		c.edit(&changed)
		if got := poststackJobContextChange(base, changed, false); got != c.want {
			t.Errorf("change %q, want %q", got, c.want)
		}
	}
}

func TestPoststackWorkerClosedOwnerRetiresUndeliverableResult(t *testing.T) {
	isolatePoststackWorker(t)
	r := poststackGeneratedFixture(t)
	w := &poststackWorker{completed: make(chan poststackUIEvent, 1)}
	e := poststackUIEvent{worker: w, Done: true, Result: r, Stamp: poststackViewStamp{Owner: 0}}
	completePoststackWorker(e)
	pending := <-w.completed
	if pending.Result != nil || pending.Err == nil {
		t.Fatal("undeliverable completion still owns a loadable result")
	}
	if _, err := os.Stat(r.Section.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed owner leaked result directory: %v", err)
	}
}

func TestPoststackAlgorithmProgressDoesNotResetOnLogOrResult(t *testing.T) {
	for _, kind := range []string{"started", "log", "result", ""} {
		if got := poststackAlgorithmProgress(api.ProgressEvent{Type: kind}); got != -1 {
			t.Fatalf("%s incorrectly supplied a percentage: %d", kind, got)
		}
	}
	if got := poststackAlgorithmProgress(api.ProgressEvent{Type: "progress", Value: .5}); got != 50 {
		t.Fatalf("progress mapping = %d, want 50", got)
	}
}

func TestPoststackWorkerLogIsBoundedAndKeepsRunRecord(t *testing.T) {
	w := &poststackWorker{}
	for i := 0; i < 1000; i++ {
		w.log(strings.Repeat("x", 8192), "")
	}
	w.log("complete", "run.json")
	if len(w.logs) != 128 || len(w.logs[0]) > 4100 || w.logs[127] != "complete" || w.recordPath != "run.json" {
		t.Fatal("worker log bounds/record retention failed")
	}
	w.log(strings.Repeat("参数", 4096), "")
	if !utf8.ValidString(w.logs[len(w.logs)-1]) {
		t.Fatal("bounded log split a UTF-8 character")
	}
}

func TestPoststackPreviewRemainsOwnedButExcludedFromFormalResults(t *testing.T) {
	formal := poststackGeneratedFixture(t)
	preview := poststackGeneratedFixture(t)
	nextPreview := poststackGeneratedFixture(t)
	preview.Preview, nextPreview.Preview = true, true
	generatedPoststackMu.Lock()
	old := generatedPoststackResults
	generatedPoststackResults = []*generatedPoststackResult{formal, preview, nextPreview}
	generatedPoststackMu.Unlock()
	t.Cleanup(func() {
		generatedPoststackMu.Lock()
		generatedPoststackResults = old
		generatedPoststackMu.Unlock()
	})
	results := formalPoststackResults()
	if len(results) != 1 || results[0] != formal {
		t.Fatal("temporary preview entered the formal results selector")
	}
	if _, _, ok := generatedPoststackSourceWindow(preview.Section.Part2); !ok {
		t.Fatal("temporary preview lost its source sample mapping")
	}
	f, err := openPoststackCompareSource(preview.Section.Part2)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	retirePoststackPreviewsExcept(nextPreview)
	if _, err := os.Stat(preview.Section.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("superseded preview directory retained: %v", err)
	}
	for _, r := range []*generatedPoststackResult{formal, nextPreview} {
		if _, err := os.Stat(r.Section.Dir); err != nil {
			t.Fatalf("valid formal/current preview result removed: %v", err)
		}
	}
	if _, _, ok := generatedPoststackSourceWindow(preview.Section.Part2); ok {
		t.Fatal("superseded preview remains discoverable")
	}
}

func TestPoststackPreviewDoesNotAppendFormalSessionMetadata(t *testing.T) {
	isolatePoststackWorker(t)
	formal := poststackGeneratedFixture(t)
	_, token := poststackProcessingSession.BeginJob()
	if !acceptPoststackResultMetadata(token, formal) {
		t.Fatal("formal result rejected")
	}
	preview := poststackGeneratedFixture(t)
	preview.Preview = true
	_, token = poststackProcessingSession.BeginJob()
	if !acceptPoststackResultMetadata(token, preview) {
		t.Fatal("preview completion rejected")
	}
	results := poststackProcessingSession.Results()
	if len(results) != 1 || results[0].Path != formal.Section.Part2 {
		t.Fatal("temporary preview was added to formal session history")
	}
}
