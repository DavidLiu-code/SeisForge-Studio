package poststack

import (
	"context"
	"sync"
	"time"
)

// Status describes the lifecycle visible to a native UI.
type Status string

const (
	StatusNoA           Status = "no_a"
	StatusAReady        Status = "a_ready"
	StatusParameterEdit Status = "parameter_edit"
	StatusGenerating    Status = "generating"
	StatusCompareReady  Status = "compare_ready"
	StatusCancelled     Status = "cancelled"
	StatusFailed        Status = "failed"
	StatusClosed        Status = "closed"
)

// JobToken is copied into every asynchronous job and result. A result is
// accepted only when every generation field still matches the session.
type JobToken struct {
	OwnerHWND               uintptr
	OwnerToken              string
	WorkspaceGeneration     uint64
	DatasetGeneration       uint64
	BDatasetGeneration      uint64
	AMappingGeneration      uint64
	BMappingGeneration      uint64
	SelectionGeneration     uint64
	ParameterGeneration     uint64
	ProcessingGeneration    uint64
	ResultGeneration        uint64
	CompareGeneration       uint64
	RenderGeneration        uint64
	ResizeGeneration        uint64
	SampleFirst, SampleLast int
	TraceFirst, TraceLast   int
	PanelWidth, PanelHeight int
	CompareMode             int
}

// ResultRef is metadata-only B history. Readers, HWNDs, sample arrays and
// pixel buffers belong to a particular UI/worker lifetime, never this list.
type ResultRef struct {
	ID              string
	AlgorithmID     string
	Params          Params
	Path            string
	ResidualPath    string
	SourcePath      string
	SourceSize      int64
	SourceModTimeNS int64
	SourceSHA256    string
	Metadata        Metadata
	TraceIndices    []int64
	Window          SampleWindow
	CreatedAt       time.Time
}

func (r ResultRef) Clone() ResultRef {
	r.Params = r.Params.Clone()
	r.TraceIndices = append([]int64(nil), r.TraceIndices...)
	return r
}

// Session is the pure lifecycle state shared by the Win32 orchestration layer.
// It does not own Readers or HWNDs; callers retain those resources and release
// them when a token is rejected or a session is closed.
type Session struct {
	mu      sync.Mutex
	owner   string
	status  Status
	token   JobToken
	cancel  context.CancelFunc
	closed  bool
	results []ResultRef
	current int
}

func NewSession(ownerToken string) *Session {
	return &Session{owner: ownerToken, status: StatusNoA, current: -1, token: JobToken{OwnerToken: ownerToken}}
}

func (s *Session) SetAReady() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.cancelLocked()
		s.results = nil
		s.current = -1
		s.status = StatusAReady
		s.token.DatasetGeneration++
		s.token.ProcessingGeneration++
		s.token.ResultGeneration++
		s.token.CompareGeneration++
		s.token.RenderGeneration++
	}
}

// UpdateContext binds the current UI snapshot without accepting externally
// assigned processing/result sequence numbers. Any context change cancels the
// active worker. Call before BeginJob and whenever the UI's owner, source,
// mapping, range, page, display mode or panel dimensions change.
func (s *Session) UpdateContext(next JobToken) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	next.ProcessingGeneration = s.token.ProcessingGeneration
	next.ResultGeneration = s.token.ResultGeneration
	if next == s.token {
		return false
	}
	if next.OwnerToken != s.token.OwnerToken || next.OwnerHWND != s.token.OwnerHWND || next.WorkspaceGeneration != s.token.WorkspaceGeneration || next.DatasetGeneration != s.token.DatasetGeneration {
		s.results = nil
		s.current = -1
	}
	s.cancelLocked()
	s.token = next
	s.token.ProcessingGeneration++
	if s.current >= 0 {
		s.status = StatusCompareReady
	} else {
		s.status = StatusAReady
	}
	return true
}

func (s *Session) cancelLocked() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func (s *Session) SetParameterEdit(editing bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && editing {
		s.cancelLocked()
		s.token.ParameterGeneration++
		s.token.ProcessingGeneration++
		s.status = StatusParameterEdit
	} else if !s.closed && s.status == StatusParameterEdit {
		if s.current >= 0 {
			s.status = StatusCompareReady
		} else {
			s.status = StatusAReady
		}
	}
}

// BeginJob cancels the previous job and returns a fresh cancellable context and
// token. The caller must call Complete or Cancel when the worker finishes.
func (s *Session) BeginJob() (context.Context, JobToken) {
	if s == nil {
		return cancelledContext(), JobToken{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return cancelledContext(), s.token
	}
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.token.ProcessingGeneration++
	s.token.ResultGeneration++
	s.status = StatusGenerating
	return ctx, s.token
}

func (s *Session) CurrentToken() JobToken {
	if s == nil {
		return JobToken{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

func (s *Session) Accept(token JobToken) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && token == s.token
}

// Complete accepts a generated B only if its token is current. The result is
// defensively cloned before being retained.
func (s *Session) Complete(token JobToken, result ResultRef) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || token != s.token || s.status != StatusGenerating {
		return false
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if result.ID == "" {
		result.ID = result.AlgorithmID
	}
	s.results = append(s.results, result.Clone())
	s.current = len(s.results) - 1
	s.status = StatusCompareReady
	return true
}

// Finish ends a metadata/export task without creating a new B history entry.
// A stale completion cannot cancel the current worker or change its status.
func (s *Session) Finish(token JobToken) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || token != s.token || s.status != StatusGenerating {
		return false
	}
	s.cancelLocked()
	if s.current >= 0 {
		s.status = StatusCompareReady
	} else {
		s.status = StatusAReady
	}
	return true
}

// Fail cannot cancel a newer job or discard an already usable B result.
func (s *Session) Fail(token JobToken) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || token != s.token || s.status != StatusGenerating {
		return false
	}
	s.cancelLocked()
	s.token.ProcessingGeneration++
	s.status = StatusFailed
	return true
}

func (s *Session) Cancel() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.token.ProcessingGeneration++
	s.status = StatusCancelled
}

func (s *Session) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.closed = true
	s.token.WorkspaceGeneration++
	s.token.ProcessingGeneration++
	s.status = StatusClosed
	s.results = nil
	s.current = -1
}

func (s *Session) Status() Status {
	if s == nil {
		return StatusClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Session) Results() []ResultRef {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	results := make([]ResultRef, len(s.results))
	for i, result := range s.results {
		results[i] = result.Clone()
	}
	return results
}

func (s *Session) CurrentResult() (ResultRef, bool) {
	if s == nil {
		return ResultRef{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current < 0 || s.current >= len(s.results) {
		return ResultRef{}, false
	}
	return s.results[s.current].Clone(), true
}

func (s *Session) SelectResult(index int) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || index < 0 || index >= len(s.results) {
		return false
	}
	s.cancelLocked()
	s.token.ProcessingGeneration++
	s.current = index
	s.token.ResultGeneration++
	s.token.CompareGeneration++
	s.status = StatusCompareReady
	return true
}

// ClearResults invalidates workers and drops references; the UI is responsible
// for closing its independent B Reader before removing owned temporary files.
func (s *Session) ClearResults() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.cancelLocked()
	s.token.ProcessingGeneration++
	s.token.ResultGeneration++
	s.token.CompareGeneration++
	s.results = nil
	s.current = -1
	s.status = StatusAReady
}
