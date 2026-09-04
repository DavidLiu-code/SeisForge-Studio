//go:build windows

package main

// traceAnalysisTarget identifies one physical SEG-Y trace and the sample
// window that was visible when the user selected it. Trace and sample values
// are zero-based internally; the analysis UI presents trace numbers as
// one-based values.
type traceAnalysisTarget struct {
	Role         string
	Path         string
	Trace        int64
	SampleStart  int
	SampleEnd    int
	MarkerSample int
	Inline       int32
	Crossline    int32
	HasGeometry  bool
}

// traceAnalysisSelection contains one source trace, or an A/B pair when the
// user selects the residual panel. It intentionally contains no window or
// Reader handles so every analysis task owns an independent SEG-Y lifetime.
type traceAnalysisSelection struct {
	Targets    []traceAnalysisTarget
	Difference bool
	Context    string
}
