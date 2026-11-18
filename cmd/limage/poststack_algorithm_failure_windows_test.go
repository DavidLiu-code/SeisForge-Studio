//go:build windows

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
)

func TestPoststackModuleFailureExplainsAndKeepsRawDiagnostics(t *testing.T) {
	const raw = "algorithm process failed: exit status 1; module rejected its configuration"
	a := poststackAlgorithmAttempt{ID: "test.failure", Name: "Example", Parameters: `{"gain":3}`, Traces: 8, Samples: 256}
	n := describeAlgorithmFailure(a, errors.New(raw), true, false, false)
	for _, want := range []string{"未能生成有效结果", "仍显示原有 B"} {
		if !strings.Contains(n.reason, want) {
			t.Fatalf("missing %s from %q", want, n.reason)
		}
	}
	if n.title != "处理失败 · Example" || !strings.Contains(n.details, raw) || !strings.Contains(n.details, a.Parameters) {
		t.Fatalf("incomplete failure details: %+v", n)
	}
	if n := describeAlgorithmFailure(a, context.DeadlineExceeded, false, false, false); !strings.Contains(n.title, "超时") {
		t.Fatalf("timeout shown as normal failure: %+v", n)
	}
	if n := describeAlgorithmFailure(a, context.Canceled, false, false, false); n.title != "" {
		t.Fatal("cancel must not display failure")
	}
}

func TestPoststackFailedWorkerDoesNotReplaceAcceptedBOrDelta(t *testing.T) {
	isolatePoststackWorker(t)
	oldA, oldB, oldVisible, oldDiff, oldD := compareAPath, compareBPath, compareBVisible, compareShowDiff, compareD
	oldFailure := algorithmFailure
	t.Cleanup(func() {
		compareAPath, compareBPath, compareBVisible, compareShowDiff, compareD = oldA, oldB, oldVisible, oldDiff, oldD
		algorithmFailure = oldFailure
	})
	compareAPath, compareBPath, compareBVisible, compareShowDiff = "source.sgy", "accepted-B.sfdata", true, true
	compareD.bgra = []byte{1, 2, 3, 4}
	_, e := beginPoststackJob()
	e.Done = true
	e.Attempt = poststackAlgorithmAttempt{ID: "test.failure", Name: "Example", Parameters: `{"gain":3}`}
	e.Err = errors.New("exit status 1; module rejected its configuration")
	activePoststackWorker.completed <- e
	if !pollPoststackWorkerCompletion() {
		t.Fatal("failed worker not acknowledged")
	}
	if compareBPath != "accepted-B.sfdata" || !compareBVisible || !compareShowDiff || len(compareD.bgra) != 4 {
		t.Fatal("failed worker touched the previously displayed B or A−B")
	}
	if algorithmProgress != 0 || cadzowCancel != nil || poststackProcessingSession.Status() != poststack.StatusFailed || algorithmStatus != "处理失败 · Example" || !strings.Contains(algorithmFailure.reason, "仍显示原有 B") {
		t.Fatalf("wrong terminal UI state: %q (%d), %+v", algorithmStatus, algorithmProgress, algorithmFailure)
	}
}

func TestAlgorithmFailureReusesFixedMultilineStatusLane(t *testing.T) {
	for _, size := range [][2]int{{400, 265}, {400, 330}, {500, 400}, {660, 490}, {950, 650}} {
		l := layoutAlgorithmDialog(size[0], size[1])
		if l.failure != l.status || l.failure.y < l.body.y+l.body.h ||
			l.failure.y+l.failure.h > l.progress.y || l.failure.w < 320 || l.failure.h < 51 {
			t.Fatalf("failure or normal status does not fit the fixed shared lane at %v: %+v", size, l)
		}
	}
}
