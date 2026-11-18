//go:build windows

package main

import "github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"

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
	// A generated artifact is locally cropped, but waveform coordinates and
	// A-B alignment remain in A's original sample axis. Zero preserves the
	// ordinary SEG-Y / prestack analysis behavior. Captured on the UI thread.
	SourceSampleOffset int
	Inline             int32
	Crossline          int32
	HasGeometry        bool
	Filtered           *viewFilterRows // immutable committed display samples; headers remain raw
	FrozenSamples      []float64       // private single-trace copy; never points into the committed view
	Bandpass           poststack.ViewBandpass
	IsFiltered         bool
	WindowUnavailable  bool
	PhysicalWindow     bool
	ManualWindow       bool
	FirstUS, LastUS    int64
}

// traceAnalysisSelection contains one or more corresponding source traces.
// It intentionally contains no window or
// Reader handles so every analysis task owns an independent SEG-Y lifetime.
type traceAnalysisSelection struct {
	Targets           []traceAnalysisTarget
	Difference        bool
	Context           string
	FromPoststackView bool
	UnfilteredContext string
	CompareGroup      bool
	Anchor            tracePoststackAnchor
	PairNote          string
	Window            traceAnalysisScope
}
