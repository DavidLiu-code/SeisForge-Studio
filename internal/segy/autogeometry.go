package segy

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// GeometryByteCandidate describes one candidate pair of 4-byte trace-header
// fields that behaves like a 3-D post-stack bin grid. Byte positions are
// 1-based, matching SEG-Y manuals and the Limage UI.
type GeometryByteCandidate struct {
	InlineByte      int
	CrosslineByte   int
	Axis1Byte       int
	Axis2Byte       int
	Score           float64 // 0..100
	Confidence      float64 // 0..1, relative confidence for this candidate
	UniqueInline    int
	UniqueCrossline int
	UniquePairs     int
	DuplicateRatio  float64
	OneAxisRatio    float64
	Rectangularity  float64
	StepConsistency float64
	StandardPair    bool
	LabelHeuristic  bool // true when IL/XL names are inferred from trace order
	Summary         string
}

type GeometryDetectResult struct {
	InlineByte      int
	CrosslineByte   int
	Score           float64
	Confidence      float64
	Ambiguous       bool
	LabelsHeuristic bool
	SampleHeaders   int
	Duration        time.Duration
	Candidates      []GeometryByteCandidate
}

type headerSample struct {
	trace int64
	group int
	hdr   [240]byte
}

type fieldFeature struct {
	bytePos      int
	values       []int32
	unique       int
	uniqueRatio  float64
	zeroFrac     float64
	sameFrac     float64
	stepCons     float64
	dominantStep int64
	maxAbs       int64
	score        float64
	changes      int
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// geometrySamplePlan chooses a handful of consecutive windows distributed
// through the file. Consecutive samples preserve trace-order information,
// while distributing windows across the file avoids mistaking a field that is
// constant inside one line for a globally constant header word.
func (s *File) geometrySamplePlan(maxHeaders int) [][2]int64 {
	n := s.Info.TraceCount
	if n <= 0 {
		return nil
	}
	if maxHeaders <= 0 {
		maxHeaders = 3072
	}
	if maxHeaders > 8192 {
		maxHeaders = 8192
	}
	if int64(maxHeaders) >= n {
		return [][2]int64{{0, n}}
	}
	windows := 6
	if maxHeaders < windows*64 {
		windows = 4
	}
	win := maxHeaders / windows
	if win < 64 {
		win = 64
	}
	if int64(win) > n {
		win = int(n)
	}
	out := make([][2]int64, 0, windows)
	seen := map[int64]bool{}
	for k := 0; k < windows; k++ {
		var center int64
		if windows == 1 {
			center = n / 2
		} else {
			center = int64(math.Round(float64(k) * float64(n-1) / float64(windows-1)))
		}
		start := center - int64(win)/2
		if start < 0 {
			start = 0
		}
		if start+int64(win) > n {
			start = n - int64(win)
		}
		if start < 0 {
			start = 0
		}
		if seen[start] {
			continue
		}
		seen[start] = true
		out = append(out, [2]int64{start, start + int64(win)})
	}
	return out
}

func (s *File) readGeometrySamples(maxHeaders int) ([]headerSample, error) {
	plan := s.geometrySamplePlan(maxHeaders)
	if len(plan) == 0 {
		return nil, errors.New("empty SEG-Y")
	}
	total := 0
	for _, p := range plan {
		total += int(p[1] - p[0])
	}
	out := make([]headerSample, 0, total)
	for g, p := range plan {
		for tr := p[0]; tr < p[1]; tr++ {
			var x headerSample
			x.trace, x.group = tr, g
			off := s.Info.DataStart + tr*s.Info.TraceBytes
			if _, err := s.f.ReadAt(x.hdr[:], off); err != nil {
				return nil, err
			}
			out = append(out, x)
		}
	}
	return out, nil
}

func dominantAbsStep(values []int32, samples []headerSample) (step int64, consistency float64, sameFrac float64, changes int) {
	hist := make(map[int64]int)
	totalTransitions, same := 0, 0
	for i := 1; i < len(values); i++ {
		if samples[i].group != samples[i-1].group || samples[i].trace != samples[i-1].trace+1 {
			continue
		}
		totalTransitions++
		d := int64(values[i]) - int64(values[i-1])
		if d == 0 {
			same++
			continue
		}
		changes++
		a := abs64(d)
		// Huge jumps are often reset jumps or byte-shift artefacts. They still
		// count as changes but do not define the dominant grid increment.
		if a > 0 && a <= 100000000 {
			hist[a]++
		}
	}
	if totalTransitions > 0 {
		sameFrac = float64(same) / float64(totalTransitions)
	}
	best := 0
	for k, c := range hist {
		if c > best {
			best, step = c, k
		}
	}
	if changes > 0 {
		consistency = float64(best) / float64(changes)
	}
	return
}

func makeFieldFeature(bytePos int, samples []headerSample, endian Endian) fieldFeature {
	f := fieldFeature{bytePos: bytePos, values: make([]int32, len(samples))}
	counts := make(map[int32]struct{}, minAuto(len(samples), 4096))
	zeros := 0
	var maxAbs int64
	for i := range samples {
		v := traceI32(samples[i].hdr[bytePos-1:bytePos+3], endian)
		f.values[i] = v
		counts[v] = struct{}{}
		if v == 0 {
			zeros++
		}
		a := abs64(int64(v))
		if a > maxAbs {
			maxAbs = a
		}
	}
	f.unique = len(counts)
	if len(samples) > 0 {
		f.uniqueRatio = float64(f.unique) / float64(len(samples))
		f.zeroFrac = float64(zeros) / float64(len(samples))
	}
	f.dominantStep, f.stepCons, f.sameFrac, f.changes = dominantAbsStep(f.values, samples)
	f.maxAbs = maxAbs

	// A grid index must vary, but it should also repeat. Sequence numbers and
	// physical X/Y coordinates often have nearly one unique value per trace.
	variation := 0.0
	if f.unique >= 2 {
		switch {
		case f.uniqueRatio <= 0.35:
			variation = 1
		case f.uniqueRatio <= 0.70:
			variation = 1 - (f.uniqueRatio-0.35)/0.70
		case f.uniqueRatio <= 0.92:
			variation = 0.50 * (0.92 - f.uniqueRatio) / 0.22
		default:
			variation = 0
		}
	}
	nonzero := 1 - f.zeroFrac
	if nonzero < 0.25 {
		nonzero *= 4
	} else {
		nonzero = 1
	}
	magnitude := 1.0
	switch {
	case maxAbs <= 10_000_000:
		magnitude = 1
	case maxAbs <= 100_000_000:
		magnitude = 0.75
	case maxAbs <= 1_000_000_000:
		magnitude = 0.35
	default:
		magnitude = 0.10
	}
	regular := math.Max(f.stepCons, f.sameFrac)
	f.score = 0.45*variation + 0.20*nonzero + 0.20*regular + 0.15*magnitude
	// Standard Rev-1 locations get only a modest prior; statistics can still
	// override them when a vendor wrote geometry elsewhere.
	if bytePos == 189 || bytePos == 193 {
		f.score += 0.08
	}
	if f.score > 1 {
		f.score = 1
	}
	return f
}

func pairMetrics(a, b fieldFeature, samples []headerSample) (uniquePairs int, dupRatio, oneAxis, rect, stepCons float64) {
	pairSet := make(map[[2]int32]struct{}, len(samples))
	valid := 0
	one, changed := 0, 0
	// Per-window rectangularity avoids penalizing sparse global sampling.
	type windowSets struct {
		a, b map[int32]struct{}
		p    map[[2]int32]struct{}
	}
	wins := map[int]*windowSets{}
	aOnlySteps, bOnlySteps := map[int64]int{}, map[int64]int{}
	aOnlyN, bOnlyN := 0, 0
	for i := range samples {
		va, vb := a.values[i], b.values[i]
		if va == 0 && vb == 0 {
			continue
		}
		p := [2]int32{va, vb}
		pairSet[p] = struct{}{}
		valid++
		w := wins[samples[i].group]
		if w == nil {
			w = &windowSets{map[int32]struct{}{}, map[int32]struct{}{}, map[[2]int32]struct{}{}}
			wins[samples[i].group] = w
		}
		w.a[va] = struct{}{}
		w.b[vb] = struct{}{}
		w.p[p] = struct{}{}
		if i == 0 || samples[i].group != samples[i-1].group || samples[i].trace != samples[i-1].trace+1 {
			continue
		}
		da := a.values[i] != a.values[i-1]
		db := b.values[i] != b.values[i-1]
		if da || db {
			changed++
			if da != db {
				one++
			}
		}
		if da && !db {
			d := abs64(int64(a.values[i]) - int64(a.values[i-1]))
			if d > 0 && d <= 100000000 {
				aOnlySteps[d]++
				aOnlyN++
			}
		}
		if db && !da {
			d := abs64(int64(b.values[i]) - int64(b.values[i-1]))
			if d > 0 && d <= 100000000 {
				bOnlySteps[d]++
				bOnlyN++
			}
		}
	}
	uniquePairs = len(pairSet)
	if valid > 0 {
		dupRatio = float64(valid-uniquePairs) / float64(valid)
	}
	if changed > 0 {
		oneAxis = float64(one) / float64(changed)
	}
	rs, nw := 0.0, 0
	for _, w := range wins {
		prod := len(w.a) * len(w.b)
		if prod == 0 || len(w.p) < 2 {
			continue
		}
		fill := float64(len(w.p)) / float64(prod)
		if fill > 1 {
			fill = 1
		}
		rs += fill
		nw++
	}
	if nw > 0 {
		rect = rs / float64(nw)
	}
	dominant := func(h map[int64]int, n int) float64 {
		if n == 0 {
			return 0
		}
		m := 0
		for _, c := range h {
			if c > m {
				m = c
			}
		}
		return float64(m) / float64(n)
	}
	sa, sb := dominant(aOnlySteps, aOnlyN), dominant(bOnlySteps, bOnlyN)
	if aOnlyN == 0 {
		sa = a.stepCons
	}
	if bOnlyN == 0 {
		sb = b.stepCons
	}
	stepCons = (sa + sb) / 2
	return
}

func minAuto(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// DetectGeometryBytes statistically estimates the two 4-byte grid-index
// fields in a fixed-trace-length 3-D post-stack SEG-Y. It reads only a small
// sample of 240-byte trace headers; no seismic amplitudes are decoded.
//
// The two mathematical grid axes can be identified much more reliably than
// their semantic names. If the pair is not the standard 189/193 pair, Limage
// labels the slower-changing trace-order axis as Inline and marks the result as
// heuristic, so the UI can offer a one-click swap.
func (s *File) DetectGeometryBytes(maxHeaders int) (GeometryDetectResult, error) {
	started := time.Now()
	samples, err := s.readGeometrySamples(maxHeaders)
	if err != nil {
		return GeometryDetectResult{}, err
	}
	if len(samples) < 32 {
		return GeometryDetectResult{}, errors.New("too few traces for automatic geometry detection")
	}

	fields := make([]fieldFeature, 0, 237)
	for b := 1; b <= 237; b++ {
		fields = append(fields, makeFieldFeature(b, samples, s.Info.Endian))
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].score > fields[j].score })
	// Keep a generous set so custom fields survive, plus force standard bytes.
	keepN := 56
	if len(fields) < keepN {
		keepN = len(fields)
	}
	keep := append([]fieldFeature(nil), fields[:keepN]...)
	have := map[int]bool{}
	for _, f := range keep {
		have[f.bytePos] = true
	}
	for _, b := range []int{189, 193} {
		if !have[b] {
			for _, f := range fields {
				if f.bytePos == b {
					keep = append(keep, f)
					break
				}
			}
		}
	}

	candidates := make([]GeometryByteCandidate, 0, 64)
	for i := 0; i < len(keep); i++ {
		for j := i + 1; j < len(keep); j++ {
			a, b := keep[i], keep[j]
			// overlapping 4-byte words are almost always shifted views of the same field
			if a.bytePos <= b.bytePos+3 && b.bytePos <= a.bytePos+3 {
				continue
			}
			up, dup, one, rect, step := pairMetrics(a, b, samples)
			if up < 4 {
				continue
			}
			dupScore := 1 - clamp01(dup/0.20)
			fieldScore := (a.score + b.score) / 2
			score := 88 * (0.34*one + 0.24*rect + 0.16*dupScore + 0.14*step + 0.12*fieldScore)
			standard := (a.bytePos == 189 && b.bytePos == 193) || (a.bytePos == 193 && b.bytePos == 189)
			if standard {
				score += 8
			}
			// Four-byte header words are conventionally placed on 4-byte
			// boundaries (1-based positions 1,5,9,...). A shifted view of a
			// big-endian integer can preserve exactly the same grid pattern, so
			// alignment is a strong tie-breaker, but still not a hard rule.
			if a.bytePos%4 == 1 {
				score += 3
			} else {
				score -= 2
			}
			if b.bytePos%4 == 1 {
				score += 3
			} else {
				score -= 2
			}
			// Rev-1 defines 181/185 as ensemble X/Y coordinates. They can form
			// a beautiful 2-D grid and therefore are the most dangerous false
			// positives for an IL/XL detector. Treat this as a prior, not an
			// exclusion, because proprietary files sometimes repurpose fields.
			if a.bytePos == 181 || a.bytePos == 185 {
				score -= 6
			}
			if b.bytePos == 181 || b.bytePos == 185 {
				score -= 6
			}
			if score > 100 {
				score = 100
			}
			// Hard reject obvious non-grid pairs even if one marginal metric is high.
			if one < 0.45 || rect < 0.20 {
				score *= 0.55
			}

			ilb, xlb := a.bytePos, b.bytePos
			heuristic := true
			if standard {
				ilb, xlb = 189, 193
				heuristic = false
			} else {
				// The slow-changing axis is the best practical ordering heuristic;
				// it is not universally equivalent to the semantic inline axis.
				if a.changes > b.changes {
					ilb, xlb = b.bytePos, a.bytePos
				}
			}
			ca, cb := a, b
			if ilb == b.bytePos {
				ca, cb = b, a
			}
			c := GeometryByteCandidate{InlineByte: ilb, CrosslineByte: xlb, Axis1Byte: a.bytePos, Axis2Byte: b.bytePos, Score: score,
				UniqueInline: ca.unique, UniqueCrossline: cb.unique, UniquePairs: up, DuplicateRatio: dup, OneAxisRatio: one, Rectangularity: rect, StepConsistency: step, StandardPair: standard, LabelHeuristic: heuristic}
			c.Summary = fmt.Sprintf("IL/XL %d/%d score %.1f | grid %.0f%% one-axis %.0f%% dup %.1f%%", c.InlineByte, c.CrosslineByte, c.Score, 100*c.Rectangularity, 100*c.OneAxisRatio, 100*c.DuplicateRatio)
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return GeometryDetectResult{}, errors.New("no plausible Inline/Crossline header pair found")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	if len(candidates) > 8 {
		candidates = candidates[:8]
	}
	best := candidates[0]
	second := 0.0
	if len(candidates) > 1 {
		second = candidates[1].Score
	}
	margin := best.Score - second
	conf := clamp01(0.62*(best.Score/100) + 0.38*clamp01(margin/15))
	// A strong standard pair deserves additional confidence, but never bypass
	// the geometry tests entirely.
	if best.StandardPair && best.Score >= 75 {
		conf = math.Min(1, conf+0.10)
	}
	ambiguous := best.Score < 65 || margin < 3 || conf < 0.58
	for i := range candidates {
		m := candidates[i].Score - second
		if i == 0 {
			m = margin
		}
		candidates[i].Confidence = clamp01(0.65*candidates[i].Score/100 + 0.35*clamp01(m/15))
	}
	return GeometryDetectResult{InlineByte: best.InlineByte, CrosslineByte: best.CrosslineByte, Score: best.Score, Confidence: conf, Ambiguous: ambiguous, LabelsHeuristic: best.LabelHeuristic, SampleHeaders: len(samples), Duration: time.Since(started), Candidates: candidates}, nil
}
