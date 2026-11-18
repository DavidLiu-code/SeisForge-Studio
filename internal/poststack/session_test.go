package poststack

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSessionRejectsStaleResultsAndKeepsDefensiveCopies(t *testing.T) {
	s := NewSession("owner")
	s.SetAReady()
	_, first := s.BeginJob()
	_, second := s.BeginJob()
	if s.Accept(first) {
		t.Fatal("stale token accepted")
	}
	if !s.Accept(second) {
		t.Fatal("current token rejected")
	}
	result := ResultRef{ID: "b1", AlgorithmID: "copy", Path: "b.sgy", Params: Params{Values: map[string]float64{"factor": 1}}, Metadata: Metadata{TraceCount: 1, SamplesPerTrace: 1, SampleIntervalUS: 2000, Direction: "inline"}, TraceIndices: []int64{7}, CreatedAt: time.Now()}
	if !s.Complete(second, result) {
		t.Fatal("result rejected")
	}
	result.TraceIndices[0] = 99
	result.Params.Values["factor"] = 8
	got, ok := s.CurrentResult()
	if !ok || got.TraceIndices[0] != 7 || got.Params.Values["factor"] != 1 {
		t.Fatalf("stored result was not defensive: %#v", got)
	}
	if s.Status() != StatusCompareReady {
		t.Fatalf("status=%s", s.Status())
	}
}

func TestSessionSetAReadyCancelsAndDropsPriorResults(t *testing.T) {
	s := NewSession("owner")
	s.SetAReady()
	_, token := s.BeginJob()
	if !s.Complete(token, ResultRef{ID: "old"}) {
		t.Fatal("complete")
	}
	ctx, token := s.BeginJob()
	s.SetAReady()
	if !errors.Is(ctx.Err(), context.Canceled) || s.Accept(token) {
		t.Fatal("old A worker remains active")
	}
	if len(s.Results()) != 0 {
		t.Fatal("old A results retained")
	}
	if _, ok := s.CurrentResult(); ok {
		t.Fatal("old B retained")
	}
}

func TestSessionClosedAndNilCannotStartWorker(t *testing.T) {
	s := NewSession("owner")
	s.Close()
	ctx, token := s.BeginJob()
	if !errors.Is(ctx.Err(), context.Canceled) || s.Accept(token) {
		t.Fatal("closed session started worker")
	}
	var absent *Session
	ctx, _ = absent.BeginJob()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("nil session started worker")
	}
}

func TestSessionRejectsEveryTokenFieldMismatch(t *testing.T) {
	s := NewSession("owner")
	s.SetAReady()
	_, token := s.BeginJob()
	for i := 0; i < reflect.TypeOf(token).NumField(); i++ {
		changed := token
		field := reflect.ValueOf(&changed).Elem().Field(i)
		switch field.Kind() {
		case reflect.String:
			field.SetString(field.String() + "-other")
		case reflect.Int:
			field.SetInt(field.Int() + 1)
		default:
			field.SetUint(field.Uint() + 1)
		}
		if s.Accept(changed) {
			t.Errorf("accepted different %s", reflect.TypeOf(token).Field(i).Name)
		}
	}
}

func TestSessionContextChangesCancelOldWorkers(t *testing.T) {
	s := NewSession("owner")
	s.SetAReady()
	ctx, token := s.BeginJob()
	if s.UpdateContext(token) {
		t.Fatal("identical context invalidated task")
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	next := token
	next.PanelWidth = 480
	if !s.UpdateContext(next) || ctx.Err() == nil || s.Accept(token) {
		t.Fatal("resize did not invalidate")
	}
	ctx, token = s.BeginJob()
	if !s.Fail(token) || ctx.Err() == nil || s.Accept(token) {
		t.Fatal("failure did not invalidate")
	}
	if s.Status() != StatusFailed {
		t.Fatal(s.Status())
	}
}

func TestSessionCompletionCannotRepeatOrClobberNewJob(t *testing.T) {
	s := NewSession("owner")
	_, first := s.BeginJob()
	if !s.Complete(first, ResultRef{ID: "first"}) {
		t.Fatal("complete")
	}
	if s.Complete(first, ResultRef{ID: "duplicate"}) {
		t.Fatal("duplicate completion")
	}
	ctx, second := s.BeginJob()
	if s.Fail(first) || !s.Accept(second) || ctx.Err() != nil {
		t.Fatal("old failure affected new job")
	}
	if !s.SelectResult(0) || ctx.Err() == nil || s.Accept(second) {
		t.Fatal("result selection did not cancel pending work")
	}
	s.ClearResults()
	if len(s.Results()) != 0 {
		t.Fatal("clear retained metadata")
	}
}

func TestSessionFinishDoesNotAppendResultAndRejectsStale(t *testing.T) {
	s := NewSession("owner")
	s.SetAReady()
	ctx, token := s.BeginJob()
	if !s.Finish(token) || ctx.Err() == nil || s.Status() != StatusAReady {
		t.Fatal("finish failed to return to A-ready")
	}
	if s.Finish(token) || len(s.Results()) != 0 {
		t.Fatal("finish accepted twice or appended a B result")
	}
	_, token = s.BeginJob()
	if !s.Complete(token, ResultRef{ID: "existing-B", Path: "B.sgy"}) {
		t.Fatal("could not install B")
	}
	_, stale := s.BeginJob()
	ctx, current := s.BeginJob()
	if s.Finish(stale) || ctx.Err() != nil || s.Status() != StatusGenerating {
		t.Fatal("stale export completion affected the active worker")
	}
	if !s.Finish(current) || ctx.Err() == nil || s.Status() != StatusCompareReady {
		t.Fatal("finish failed to preserve Compare-ready")
	}
	results := s.Results()
	if len(results) != 1 || results[0].ID != "existing-B" {
		t.Fatalf("export finish changed B history: %#v", results)
	}
	var absent *Session
	if absent.Finish(JobToken{}) {
		t.Fatal("nil session accepted completion")
	}
	s.Close()
	if s.Finish(current) {
		t.Fatal("closed session accepted completion")
	}
}

func TestSessionCancelAndCloseInvalidateTokens(t *testing.T) {
	s := NewSession("owner")
	_, token := s.BeginJob()
	s.Cancel()
	if s.Accept(token) {
		t.Fatal("cancelled token accepted")
	}
	if s.Status() != StatusCancelled {
		t.Fatalf("status=%s", s.Status())
	}
	s.Close()
	if s.Status() != StatusClosed {
		t.Fatalf("status=%s", s.Status())
	}
	if s.SelectResult(0) {
		t.Fatal("closed session accepted selection")
	}
}
