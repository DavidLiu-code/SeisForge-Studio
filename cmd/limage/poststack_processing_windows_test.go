//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestPoststackToolbarBOperationsOnlyInWorkspace(t *testing.T) {
	for _, width := range []int{800, 1120, 1360, 1920} {
		ordinary := poststackToolbarRects(width, true, false, false)
		for _, id := range []int{IDCMP_B, IDCMP_CADZOW, IDCMP_CLOSE_B, IDCMP_DIFF} {
			if _, visible := ordinary[id]; visible {
				t.Fatalf("ordinary section exposes B command %d", id)
			}
		}
		if _, visible := ordinary[ID_COMPARE]; !visible {
			t.Fatal("ordinary section lost 比")
		}
		for _, hasB := range []bool{false, true} {
			active := poststackToolbarRects(width, true, true, hasB)
			for _, id := range []int{IDCMP_B, IDCMP_CADZOW} {
				if _, visible := active[id]; !visible {
					t.Fatalf("Compare missing %d", id)
				}
			}
			_, closeVisible := active[IDCMP_CLOSE_B]
			_, deltaVisible := active[IDCMP_DIFF]
			if closeVisible != hasB || deltaVisible != hasB {
				t.Fatal("B state not reflected in toolbar")
			}
			for id, r := range active {
				if r.Left < 0 || r.Top < 0 || r.Right > int32(width) || r.Bottom <= r.Top || r.Right <= r.Left {
					t.Fatalf("width %d, control %d out of bounds: %+v", width, id, r)
				}
				for other, s := range active {
					if id != other && r.Left < s.Right && s.Left < r.Right && r.Top < s.Bottom && s.Top < r.Bottom {
						t.Fatalf("overlapping Compare controls %d/%d: %+v %+v", id, other, r, s)
					}
				}
			}
		}
	}
	if len(poststackToolbarRects(1120, false, false, false)) != 0 {
		t.Fatal("home exposes section controls")
	}
}

func poststackGeneratedFixture(t *testing.T) *generatedPoststackResult {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "A.sgy")
	if err := testsegy.Write(source, testsegy.Options{Rows: 1, Cols: 3, Samples: 8, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	section, err := poststack.ProcessSection(context.Background(), poststack.SectionRequest{SourcePath: source, TempDir: dir, TraceIndices: []int64{2, 0}, SampleStart: 1, SampleEnd: 4, AlgorithmID: "copy", Gain: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = section.Cleanup() })
	return &generatedPoststackResult{Section: section, Algorithm: "copy", Name1: "Copy_Part1.sgy", Name2: "Copy_Part2.sgy"}
}

// These tests never create a window or send input. Zero HWND/control handles
// keep the lifecycle assertions independent of desktop state.
func isolatePoststackMailbox(t *testing.T) {
	t.Helper()
	oldSession, oldCancel, oldGen := poststackProcessingSession, cadzowCancel, atomic.LoadInt64(&cadzowGen)
	oldHwnd, oldCC, oldCZ := compareHwnd, cc, cz
	oldBusy, oldProgress := atomic.LoadInt32(&compareBusyCount), compareProgressVisible
	poststackMailboxMu.Lock()
	oldPending := poststackMailbox
	poststackMailbox = nil
	poststackMailboxMu.Unlock()
	poststackProcessingSession = poststack.NewSession("ui-free-test")
	cadzowCancel = nil
	compareHwnd = 0
	cc = compareControls{}
	cz = cadzowControls{}
	atomic.StoreInt64(&cadzowGen, 20)
	atomic.StoreInt32(&compareBusyCount, 0)
	t.Cleanup(func() {
		poststackMailboxMu.Lock()
		pending := poststackMailbox
		poststackMailbox = oldPending
		poststackMailboxMu.Unlock()
		disposePoststackEvent(pending)
		poststackProcessingSession = oldSession
		cadzowCancel = oldCancel
		atomic.StoreInt64(&cadzowGen, oldGen)
		compareHwnd = oldHwnd
		cc = oldCC
		cz = oldCZ
		atomic.StoreInt32(&compareBusyCount, oldBusy)
		compareProgressVisible = oldProgress
	})
}

func TestPoststackMailboxStaleGenerationDisposesPrivateResult(t *testing.T) {
	isolatePoststackMailbox(t)
	_, token := poststackProcessingSession.BeginJob()
	r := poststackGeneratedFixture(t)
	postPoststackEvent(poststackUIEvent{Generation: 19, Token: token, Done: true, Result: r})
	if _, err := os.Stat(r.Section.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale result leaked: %v", err)
	}
	if _, err := os.Stat(r.Section.SourcePath); err != nil {
		t.Fatalf("A was removed: %v", err)
	}
	poststackMailboxMu.Lock()
	pending := poststackMailbox
	poststackMailboxMu.Unlock()
	if pending != nil {
		t.Fatal("stale event published")
	}
}

func TestPoststackMailboxStaleTokenCannotReplaceNewEvent(t *testing.T) {
	isolatePoststackMailbox(t)
	_, token := poststackProcessingSession.BeginJob()
	current := &poststackUIEvent{Generation: 20, Token: token, Message: "current"}
	poststackMailboxMu.Lock()
	poststackMailbox = current
	poststackMailboxMu.Unlock()
	r := poststackGeneratedFixture(t)
	stale := token
	stale.SelectionGeneration++
	postPoststackEvent(poststackUIEvent{Generation: 20, Token: stale, Done: true, Result: r})
	poststackMailboxMu.Lock()
	pending := poststackMailbox
	poststackMailboxMu.Unlock()
	if pending != current {
		t.Fatal("stale completion replaced current event")
	}
	if _, err := os.Stat(r.Section.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale temporary result retained: %v", err)
	}
}

func TestPoststackCancellationDrainsPendingWithoutChangingA(t *testing.T) {
	isolatePoststackMailbox(t)
	oldA, oldB, oldTD := compareAPath, compareBPath, compareTD
	t.Cleanup(func() { compareAPath, compareBPath, compareTD = oldA, oldB, oldTD })
	compareAPath, compareBPath = "A-read-only.sgy", "previous-B.sgy"
	compareTD = &compareTimeData{}
	retainedA := compareTD
	ctx, cancel := context.WithCancel(context.Background())
	cadzowCancel = cancel
	atomic.StoreInt32(&compareBusyCount, 1)
	r := poststackGeneratedFixture(t)
	poststackMailboxMu.Lock()
	poststackMailbox = &poststackUIEvent{Generation: 20, Done: true, Result: r}
	poststackMailboxMu.Unlock()
	cancelCadzowTask()
	if !errors.Is(ctx.Err(), context.Canceled) || cadzowCancel != nil {
		t.Fatal("task not cancelled")
	}
	if atomic.LoadInt32(&compareBusyCount) != 0 {
		t.Fatal("busy count leaked")
	}
	if atomic.LoadInt64(&cadzowGen) == 20 {
		t.Fatal("cancel did not invalidate generation")
	}
	if compareAPath != "A-read-only.sgy" || compareBPath != "previous-B.sgy" || compareTD != retainedA {
		t.Fatal("cancellation changed current data")
	}
	if _, err := os.Stat(r.Section.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending result leaked: %v", err)
	}
}

func TestGeneratedPoststackWindowUsesSourceSampleCoordinates(t *testing.T) {
	r := poststackGeneratedFixture(t)
	generatedPoststackMu.Lock()
	old := generatedPoststackResults
	generatedPoststackResults = []*generatedPoststackResult{r}
	generatedPoststackMu.Unlock()
	t.Cleanup(func() { generatedPoststackMu.Lock(); generatedPoststackResults = old; generatedPoststackMu.Unlock() })
	first, last, ok := generatedPoststackSourceWindow(r.Section.Part2)
	if !ok || first != 1 || last != 4 {
		t.Fatalf("wrong original sample window: %d %d %v", first, last, ok)
	}
	if _, _, ok := generatedPoststackSourceWindow("unrelated.sgy"); ok {
		t.Fatal("unrelated B uses generated mapping")
	}
	r.retired.Store(true)
	if _, _, ok := generatedPoststackSourceWindow(r.Section.Part2); ok {
		t.Fatal("retired B mapping remains active")
	}
}

func TestPoststackParameterMinimumAndNames(t *testing.T) {
	var m MINMAXINFO
	cadzowWndProc(0, WM_GETMINMAXINFO, 0, uintptr(unsafe.Pointer(&m)))
	area := algorithmMonitorArea(0)
	w, h := algorithmOuterSize(algorithmMinWidth, algorithmMinHeight)
	if m.PtMinTrackSize.X != int32(minInt(w, int(area.Right-area.Left))) || m.PtMinTrackSize.Y != int32(minInt(h, int(area.Bottom-area.Top))) {
		t.Fatalf("parameter controls can be clipped: %+v", m.PtMinTrackSize)
	}
	at := time.Date(2026, 11, 11, 12, 13, 14, 0, time.Local)
	a, b := algorithmOutputNames("A.sgy", "example", map[string]any{"factor": 1.5}, at)
	if !strings.HasPrefix(a, "A_20261111_121314_example_p") || !strings.HasSuffix(b, "_Part2.sgy") {
		t.Fatalf("unexpected provenance names: %s %s", a, b)
	}
}
