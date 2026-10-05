package prestack

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// MatchKeyStrategy describes the metadata key used for one trace match.  The
// order of the first three strategies is significant: MatchRecords always
// prefers stable Source/Receiver IDs, then scaled coordinates, and finally
// CDP plus signed Offset as an explicit, lossy fallback.
type MatchKeyStrategy uint8

const (
	MatchKeyInvalid MatchKeyStrategy = iota
	MatchKeySourceReceiverID
	MatchKeySourceReceiverXY
	MatchKeyCDPOffset
	MatchKeyMixed
)

// Aliases keep the names convenient for callers while preserving one stable
// enum representation in reports.
const (
	MatchStrategyInvalid          = MatchKeyInvalid
	MatchStrategySourceReceiver   = MatchKeySourceReceiverID
	MatchStrategySourceReceiverXY = MatchKeySourceReceiverXY
	MatchStrategyCDPOffset        = MatchKeyCDPOffset
	MatchStrategyMixed            = MatchKeyMixed
)

func (s MatchKeyStrategy) String() string {
	switch s {
	case MatchKeySourceReceiverID:
		return "source_receiver_id"
	case MatchKeySourceReceiverXY:
		return "source_receiver_xy"
	case MatchKeyCDPOffset:
		return "cdp_signed_offset"
	case MatchKeyMixed:
		return "mixed"
	default:
		return "invalid"
	}
}

// TraceMatchKey is an explainable, metadata-only key.  Float fields are
// normalized by makeTraceMatchKey before they are used as map keys.  The
// public values are retained so a report can explain exactly why a pair was
// matched without retaining any samples.
type TraceMatchKey struct {
	Strategy   MatchKeyStrategy `json:"strategy"`
	SourceID   int32            `json:"source_id,omitempty"`
	ReceiverID int32            `json:"receiver_id,omitempty"`
	CDP        int32            `json:"cdp,omitempty"`
	SourceX    float64          `json:"source_x,omitempty"`
	SourceY    float64          `json:"source_y,omitempty"`
	ReceiverX  float64          `json:"receiver_x,omitempty"`
	ReceiverY  float64          `json:"receiver_y,omitempty"`
	Offset     float64          `json:"offset,omitempty"`
}

// TraceMatchPair links physical trace numbers, never raster columns.  AIndex
// and BIndex are zero-based positions in the input selections and are useful
// when a caller needs to recover metadata after a stable A-order match.
type TraceMatchPair struct {
	ATrace   int64            `json:"a_trace"`
	BTrace   int64            `json:"b_trace"`
	AIndex   int              `json:"a_index"`
	BIndex   int              `json:"b_index"`
	Key      TraceMatchKey    `json:"key"`
	Strategy MatchKeyStrategy `json:"strategy"`
}

// CompareMatchResult is immutable with respect to a PrestackIndex: it owns
// all slices it returns and contains only trace numbers and metadata.  A and B
// are never reordered; Pairs follows the order of the A input selection.
type CompareMatchResult struct {
	Pairs      []TraceMatchPair `json:"pairs"`
	AOnly      []int64          `json:"a_only"`
	BOnly      []int64          `json:"b_only"`
	AmbiguousA []int64          `json:"ambiguous_a"`
	AmbiguousB []int64          `json:"ambiguous_b"`
	InvalidA   []int64          `json:"invalid_a"`
	InvalidB   []int64          `json:"invalid_b"`
	Strategy   MatchKeyStrategy `json:"strategy"`
	Matched    int              `json:"matched"`
	Ambiguous  int              `json:"ambiguous"`
	Invalid    int              `json:"invalid"`
	// Reason explains why no pair was produced or why one side is unavailable.
	// It contains metadata only and is safe to surface in the Compare UI.
	Reason string `json:"reason,omitempty"`
}

func (r CompareMatchResult) String() string {
	return fmt.Sprintf("%s: matched=%d a_only=%d b_only=%d ambiguous=%d invalid=%d", r.Strategy,
		len(r.Pairs), len(r.AOnly), len(r.BOnly), len(r.AmbiguousA)+len(r.AmbiguousB), len(r.InvalidA)+len(r.InvalidB))
}

// Clone returns a defensive copy suitable for handing to UI/report code.
func (r CompareMatchResult) Clone() CompareMatchResult {
	r.Pairs = append([]TraceMatchPair(nil), r.Pairs...)
	r.AOnly = append([]int64(nil), r.AOnly...)
	r.BOnly = append([]int64(nil), r.BOnly...)
	r.AmbiguousA = append([]int64(nil), r.AmbiguousA...)
	r.AmbiguousB = append([]int64(nil), r.AmbiguousB...)
	r.InvalidA = append([]int64(nil), r.InvalidA...)
	r.InvalidB = append([]int64(nil), r.InvalidB...)
	return r
}

// MatchRecords matches two ordered metadata selections.  It deliberately
// does not compare physical trace numbers: B may have been reordered and A's
// order is always retained in the returned Pairs slice.
func MatchRecords(a, b []PrestackTraceRecord) CompareMatchResult {
	return matchRecords(a, b)
}

// MatchTraceRecords is the error-returning spelling used by asynchronous
// callers.  Metadata matching currently has no recoverable error, but the
// signature gives future callers a stable place to report malformed input.
func MatchTraceRecords(a, b []PrestackTraceRecord) (CompareMatchResult, error) {
	return matchRecords(a, b), nil
}

// MatchPrestackRecords is an explicit alias for package users that prefer the
// domain name in their call sites.
func MatchPrestackRecords(a, b []PrestackTraceRecord) (CompareMatchResult, error) {
	return MatchTraceRecords(a, b)
}

// MatchGatherResults adapts the ordered physical trace lists returned by
// Gather to MatchRecords without exposing any index-table internals to UI
// code.  It copies metadata records in A/B selection order and never copies
// amplitudes.
func MatchGatherResults(aIndex *PrestackIndex, aGather GatherResult, bIndex *PrestackIndex, bGather GatherResult) (CompareMatchResult, error) {
	if aIndex == nil || bIndex == nil {
		return CompareMatchResult{}, fmt.Errorf("nil prestack index")
	}
	aRecords := make([]PrestackTraceRecord, 0, len(aGather.TraceIndices))
	bRecords := make([]PrestackTraceRecord, 0, len(bGather.TraceIndices))
	for _, trace := range aGather.TraceIndices {
		if trace < 0 || trace >= int64(len(aIndex.Records)) {
			return CompareMatchResult{}, fmt.Errorf("A trace index %d out of range", trace)
		}
		aRecords = append(aRecords, aIndex.Records[trace])
	}
	for _, trace := range bGather.TraceIndices {
		if trace < 0 || trace >= int64(len(bIndex.Records)) {
			return CompareMatchResult{}, fmt.Errorf("B trace index %d out of range", trace)
		}
		bRecords = append(bRecords, bIndex.Records[trace])
	}
	return MatchTraceRecords(aRecords, bRecords)
}

type normalizedMatchKey struct {
	strategy MatchKeyStrategy
	text     string
}

func finiteMatch(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// normalizeCoordinate uses microunit quantization to make values decoded with
// the same SEG-Y scalar compare reliably while still rejecting NaN/Inf.
func normalizeCoordinate(v float64) (float64, bool) {
	if !finiteMatch(v) {
		return 0, false
	}
	if v == 0 {
		return 0, true
	}
	return math.Round(v*1e6) / 1e6, true
}

func floatToken(v float64) string {
	v, _ = normalizeCoordinate(v)
	return fmt.Sprintf("%.6f", v)
}

func (r PrestackTraceRecord) sourceReceiverIDKey() (normalizedMatchKey, TraceMatchKey, bool) {
	if r.SourceID == 0 || r.ReceiverID == 0 {
		return normalizedMatchKey{}, TraceMatchKey{}, false
	}
	key := TraceMatchKey{Strategy: MatchKeySourceReceiverID, SourceID: r.SourceID, ReceiverID: r.ReceiverID}
	return normalizedMatchKey{strategy: key.Strategy, text: fmt.Sprintf("id:%d:%d", r.SourceID, r.ReceiverID)}, key, true
}

func (r PrestackTraceRecord) sourceReceiverXYKey() (normalizedMatchKey, TraceMatchKey, bool) {
	if !r.HasSource || !r.HasReceiver {
		return normalizedMatchKey{}, TraceMatchKey{}, false
	}
	sx, ok1 := normalizeCoordinate(r.SourceX)
	sy, ok2 := normalizeCoordinate(r.SourceY)
	rx, ok3 := normalizeCoordinate(r.ReceiverX)
	ry, ok4 := normalizeCoordinate(r.ReceiverY)
	if !(ok1 && ok2 && ok3 && ok4) {
		return normalizedMatchKey{}, TraceMatchKey{}, false
	}
	key := TraceMatchKey{Strategy: MatchKeySourceReceiverXY, SourceX: sx, SourceY: sy, ReceiverX: rx, ReceiverY: ry}
	return normalizedMatchKey{strategy: key.Strategy, text: strings.Join([]string{"xy", floatToken(sx), floatToken(sy), floatToken(rx), floatToken(ry)}, ":")}, key, true
}

func (r PrestackTraceRecord) cdpOffsetKey() (normalizedMatchKey, TraceMatchKey, bool) {
	if !r.HasCDP || !r.HasOffset || !finiteMatch(r.Offset) {
		return normalizedMatchKey{}, TraceMatchKey{}, false
	}
	o, ok := normalizeCoordinate(r.Offset)
	if !ok {
		return normalizedMatchKey{}, TraceMatchKey{}, false
	}
	key := TraceMatchKey{Strategy: MatchKeyCDPOffset, CDP: r.CDP, Offset: o}
	return normalizedMatchKey{strategy: key.Strategy, text: fmt.Sprintf("cdp:%d:%s", r.CDP, floatToken(o))}, key, true
}

func (r PrestackTraceRecord) candidateKeys(idConflict, receiverConflict bool) []struct {
	normalizedMatchKey
	public TraceMatchKey
} {
	keys := make([]struct {
		normalizedMatchKey
		public TraceMatchKey
	}, 0, 3)
	if !idConflict && !receiverConflict {
		if k, p, ok := r.sourceReceiverIDKey(); ok {
			keys = append(keys, struct {
				normalizedMatchKey
				public TraceMatchKey
			}{k, p})
		}
	}
	if k, p, ok := r.sourceReceiverXYKey(); ok {
		keys = append(keys, struct {
			normalizedMatchKey
			public TraceMatchKey
		}{k, p})
	}
	if k, p, ok := r.cdpOffsetKey(); ok {
		keys = append(keys, struct {
			normalizedMatchKey
			public TraceMatchKey
		}{k, p})
	}
	return keys
}

func traceNumber(r PrestackTraceRecord, position int) int64 {
	// BuildIndex always supplies TraceNumber.  The position fallback keeps the
	// pure metadata API useful for synthetic records whose number was omitted.
	if r.TraceNumber == 0 && position != 0 {
		return int64(position)
	}
	return r.TraceNumber
}

type idCoordinate struct{ x, y float64 }

// idConflicts returns the IDs whose observed coordinates disagree.  Conflict
// detection is intentionally per-ID: one malformed source record must not
// disable the stable ID key for every otherwise-good source in the file.
func idConflicts(records []PrestackTraceRecord, source bool) map[int32]bool {
	seen := make(map[int32]idCoordinate)
	// A stable ID is only safe when every occurrence has a usable coordinate
	// whenever at least one occurrence carries coordinates.  Keeping this
	// separate from seen lets us detect a missing coordinate regardless of
	// whether the malformed record appears before or after the valid one.
	missing := make(map[int32]bool)
	conflicts := make(map[int32]bool)
	for _, r := range records {
		var id int32
		var ok bool
		var x, y float64
		if source {
			id, ok, x, y = r.SourceID, r.HasSource, r.SourceX, r.SourceY
		} else {
			id, ok, x, y = r.ReceiverID, r.HasReceiver, r.ReceiverX, r.ReceiverY
		}
		if id == 0 {
			continue
		}
		if !ok || !finiteMatch(x) || !finiteMatch(y) {
			missing[id] = true
			continue
		}
		x, _ = normalizeCoordinate(x)
		y, _ = normalizeCoordinate(y)
		if missing[id] {
			conflicts[id] = true
		}
		if old, exists := seen[id]; exists && (old.x != x || old.y != y) {
			conflicts[id] = true
			continue
		}
		seen[id] = idCoordinate{x: x, y: y}
	}
	for id := range missing {
		if _, ok := seen[id]; ok {
			conflicts[id] = true
		}
	}
	return conflicts
}

func matchRecords(a, b []PrestackTraceRecord) CompareMatchResult {
	result := CompareMatchResult{}
	all := make([]PrestackTraceRecord, 0, len(a)+len(b))
	all = append(all, a...)
	all = append(all, b...)
	sourceConflicts := idConflicts(all, true)
	receiverConflicts := idConflicts(all, false)

	type entry struct {
		r     PrestackTraceRecord
		pos   int
		trace int64
		keys  []struct {
			normalizedMatchKey
			public TraceMatchKey
		}
	}
	valid := func(r PrestackTraceRecord) bool {
		// BuildIndex explicitly marks every successfully decoded header.  A
		// synthetic caller must do the same; HeaderError remains a useful reason
		// in reports but is not the only invalid marker.
		return r.HeaderValid && r.HeaderError == ""
	}
	ae := make([]entry, len(a))
	be := make([]entry, len(b))
	for i, r := range a {
		ae[i] = entry{r: r, pos: i, trace: traceNumber(r, i)}
		if !valid(r) {
			result.InvalidA = append(result.InvalidA, ae[i].trace)
			continue
		}
		ae[i].keys = r.candidateKeys(sourceConflicts[r.SourceID], receiverConflicts[r.ReceiverID])
		if len(ae[i].keys) == 0 {
			result.InvalidA = append(result.InvalidA, ae[i].trace)
		}
	}
	for i, r := range b {
		be[i] = entry{r: r, pos: i, trace: traceNumber(r, i)}
		if !valid(r) {
			result.InvalidB = append(result.InvalidB, be[i].trace)
			continue
		}
		be[i].keys = r.candidateKeys(sourceConflicts[r.SourceID], receiverConflicts[r.ReceiverID])
		if len(be[i].keys) == 0 {
			result.InvalidB = append(result.InvalidB, be[i].trace)
		}
	}

	bByKey := make(map[normalizedMatchKey][]int)
	for i := range be {
		for _, candidate := range be[i].keys {
			bByKey[candidate.normalizedMatchKey] = append(bByKey[candidate.normalizedMatchKey], i)
		}
	}
	aByKey := make(map[normalizedMatchKey][]int)
	for i := range ae {
		for _, candidate := range ae[i].keys {
			aByKey[candidate.normalizedMatchKey] = append(aByKey[candidate.normalizedMatchKey], i)
		}
	}

	usedB := make(map[int]bool)
	ambiguousA := make(map[int]bool)
	ambiguousB := make(map[int]bool)
	matchedB := make(map[int]bool)
	for ai := range ae {
		if len(ae[ai].keys) == 0 || !valid(ae[ai].r) {
			continue
		}
		for _, candidate := range ae[ai].keys {
			bis := bByKey[candidate.normalizedMatchKey]
			if len(bis) == 0 {
				continue
			}
			// Duplicate keys are never silently resolved by map order.  If the
			// same strategy is duplicated on either side, mark every record in
			// that duplicate group and do not fall through to a weaker key.
			if len(bis) != 1 || len(aByKey[candidate.normalizedMatchKey]) != 1 {
				for _, bi := range bis {
					ambiguousB[bi] = true
				}
				for _, other := range aByKey[candidate.normalizedMatchKey] {
					ambiguousA[other] = true
				}
				break
			}
			bi := bis[0]
			if usedB[bi] {
				ambiguousA[ai] = true
				ambiguousB[bi] = true
				break
			}
			usedB[bi] = true
			matchedB[bi] = true
			result.Pairs = append(result.Pairs, TraceMatchPair{ATrace: ae[ai].trace, BTrace: be[bi].trace,
				AIndex: ae[ai].pos, BIndex: be[bi].pos, Key: candidate.public, Strategy: candidate.strategy})
			break
		}
	}
	// A record marked ambiguous after a prior tentative match is removed from
	// pairs. This is unusual (it only occurs when a duplicate is discovered
	// through a stronger key), but keeping the invariant explicit avoids a
	// first-match leak.
	if len(ambiguousA) > 0 {
		kept := result.Pairs[:0]
		for _, pair := range result.Pairs {
			if ambiguousA[pair.AIndex] {
				delete(matchedB, pair.BIndex)
				delete(usedB, pair.BIndex)
				ambiguousB[pair.BIndex] = true
				continue
			}
			kept = append(kept, pair)
		}
		result.Pairs = kept
	}

	for i := range ae {
		if !valid(ae[i].r) || len(ae[i].keys) == 0 {
			continue
		}
		if ambiguousA[i] {
			result.AmbiguousA = append(result.AmbiguousA, ae[i].trace)
			continue
		}
		matched := false
		for _, pair := range result.Pairs {
			if pair.AIndex == i {
				matched = true
				break
			}
		}
		if !matched {
			result.AOnly = append(result.AOnly, ae[i].trace)
		}
	}
	for i := range be {
		if !valid(be[i].r) || len(be[i].keys) == 0 {
			continue
		}
		if ambiguousB[i] {
			result.AmbiguousB = append(result.AmbiguousB, be[i].trace)
			continue
		}
		if !matchedB[i] {
			result.BOnly = append(result.BOnly, be[i].trace)
		}
	}

	strategies := make(map[MatchKeyStrategy]bool)
	for _, pair := range result.Pairs {
		strategies[pair.Strategy] = true
	}
	switch len(strategies) {
	case 1:
		for strategy := range strategies {
			result.Strategy = strategy
		}
	case 0:
		result.Strategy = MatchKeyInvalid
	default:
		result.Strategy = MatchKeyMixed
	}
	result.Matched = len(result.Pairs)
	result.Ambiguous = len(result.AmbiguousA) + len(result.AmbiguousB)
	result.Invalid = len(result.InvalidA) + len(result.InvalidB)
	switch {
	case len(a) == 0 && len(b) > 0:
		result.Reason = "A 当前道集为空"
	case len(b) == 0 && len(a) > 0:
		result.Reason = "B 当前道集不可用或为空"
	case len(result.Pairs) == 0 && (len(a) > 0 || len(b) > 0):
		if result.Invalid == len(a)+len(b) {
			result.Reason = "没有可用于匹配的有效道头"
		} else {
			result.Reason = "当前道集无匹配道"
		}
	}
	return result
}

// SampleAxis contains only the coordinates needed to decide whether A and B
// may be subtracted sample-for-sample. TimeOriginMS is optional because SEG-Y
// binary headers do not carry a universal file-level delay.
type SampleAxis struct {
	SampleCount      int
	SampleIntervalUS int
	DelayMS          int
	TimeOriginMS     float64
	HasTimeOrigin    bool
}

// SampleWindow is a zero-based inclusive sample interval used for a compare
// operation.  It contains no sample values and is therefore safe to retain in
// asynchronous tokens and metadata-only reports.
type SampleWindow struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// SampleAxisCompatibility is metadata-only and suitable for a compare report.
type SampleAxisCompatibility struct {
	Compatible          bool         `json:"compatible"`
	SampleIntervalEqual bool         `json:"sample_interval_equal"`
	SampleCountEqual    bool         `json:"sample_count_equal"`
	DelayEqual          bool         `json:"delay_equal"`
	TimeOriginEqual     bool         `json:"time_origin_equal"`
	SampleWindowEqual   bool         `json:"sample_window_equal,omitempty"`
	AWindow             SampleWindow `json:"a_window,omitempty"`
	BWindow             SampleWindow `json:"b_window,omitempty"`
	A                   SampleAxis   `json:"a"`
	B                   SampleAxis   `json:"b"`
	Reason              string       `json:"reason,omitempty"`
}

// NormalizeSampleWindow clamps an inclusive sample range to an axis.  It is
// deliberately pure so UI code can validate text input before starting a
// reader task.  A reversed or empty range is rejected instead of silently
// changing the requested comparison window.
func NormalizeSampleWindow(start, end, sampleCount int) (SampleWindow, bool) {
	if sampleCount <= 0 {
		return SampleWindow{}, false
	}
	if start < 0 {
		start = 0
	}
	if end < 0 || end >= sampleCount {
		end = sampleCount - 1
	}
	if start >= sampleCount || start > end {
		return SampleWindow{}, false
	}
	return SampleWindow{Start: start, End: end}, true
}

func axisFrom(v any) (SampleAxis, bool) {
	switch x := v.(type) {
	case SampleAxis:
		return x, true
	case *SampleAxis:
		if x != nil {
			return *x, true
		}
	case segy.Info:
		return SampleAxis{SampleCount: x.SamplesPerTrace, SampleIntervalUS: x.SampleIntervalUS}, true
	case *segy.Info:
		if x != nil {
			return SampleAxis{SampleCount: x.SamplesPerTrace, SampleIntervalUS: x.SampleIntervalUS}, true
		}
	case PrestackTraceRecord:
		return SampleAxis{SampleCount: x.SampleCount, SampleIntervalUS: x.SampleIntervalUS, DelayMS: x.DelayMS, HasTimeOrigin: true, TimeOriginMS: float64(x.DelayMS)}, true
	case *PrestackTraceRecord:
		if x != nil {
			return axisFrom(*x)
		}
	}
	return SampleAxis{}, false
}

// CompareSampleAxes accepts SampleAxis, segy.Info, or PrestackTraceRecord
// values. The any signature intentionally lets UI and core callers use their
// existing reader/header type without conversion glue.
func CompareSampleAxes(a, b any) SampleAxisCompatibility {
	aa, oka := axisFrom(a)
	bb, okb := axisFrom(b)
	if !oka || !okb {
		return SampleAxisCompatibility{Reason: "unsupported sample-axis metadata"}
	}
	r := SampleAxisCompatibility{A: aa, B: bb,
		SampleIntervalEqual: aa.SampleIntervalUS > 0 && aa.SampleIntervalUS == bb.SampleIntervalUS,
		SampleCountEqual:    aa.SampleCount > 0 && aa.SampleCount == bb.SampleCount,
		DelayEqual:          aa.DelayMS == bb.DelayMS,
		TimeOriginEqual:     aa.HasTimeOrigin == bb.HasTimeOrigin && (!aa.HasTimeOrigin || (finiteMatch(aa.TimeOriginMS) && finiteMatch(bb.TimeOriginMS) && aa.TimeOriginMS == bb.TimeOriginMS))}
	r.Compatible = r.SampleIntervalEqual && r.SampleCountEqual && r.DelayEqual && r.TimeOriginEqual
	if !r.Compatible {
		parts := make([]string, 0, 4)
		if !r.SampleIntervalEqual {
			parts = append(parts, "sample interval mismatch")
		}
		if !r.SampleCountEqual {
			parts = append(parts, "sample count mismatch")
		}
		if !r.DelayEqual {
			parts = append(parts, "delay mismatch")
		}
		if !r.TimeOriginEqual {
			parts = append(parts, "time origin mismatch")
		}
		r.Reason = strings.Join(parts, "; ")
	}
	return r
}

// CompareSampleAxesWithWindows extends CompareSampleAxes with the currently
// selected sample ranges.  The ordinary CompareSampleAxes API remains
// unchanged for callers that only have file/trace axis metadata.  A delta is
// valid only when both axes and both requested windows are valid and equal.
func CompareSampleAxesWithWindows(a, b any, aWindow, bWindow SampleWindow) SampleAxisCompatibility {
	r := CompareSampleAxes(a, b)
	r.AWindow, r.BWindow = aWindow, bWindow
	aa, oka := axisFrom(a)
	bb, okb := axisFrom(b)
	validA := oka && aWindow.Start >= 0 && aWindow.End >= aWindow.Start && aWindow.End < aa.SampleCount
	validB := okb && bWindow.Start >= 0 && bWindow.End >= bWindow.Start && bWindow.End < bb.SampleCount
	r.SampleWindowEqual = validA && validB && aWindow == bWindow
	if !r.SampleWindowEqual {
		if r.Reason == "" {
			r.Reason = "sample window mismatch"
		} else {
			r.Reason += "; sample window mismatch"
		}
		r.Compatible = false
	}
	return r
}

func CheckSampleAxisCompatibilityWithWindows(a, b any, aWindow, bWindow SampleWindow) SampleAxisCompatibility {
	return CompareSampleAxesWithWindows(a, b, aWindow, bWindow)
}

func CheckSampleAxisCompatibility(a, b any) SampleAxisCompatibility {
	return CompareSampleAxes(a, b)
}

func SampleAxesCompatible(a, b any) bool {
	return CompareSampleAxes(a, b).Compatible
}

// SubtractValues computes A-B before palette mapping. It deliberately rejects
// unequal lengths instead of silently truncating or interpolating.
func SubtractValues(a, b []float64) ([]float64, error) {
	if len(a) != len(b) {
		return nil, fmt.Errorf("sample value lengths differ: %d != %d", len(a), len(b))
	}
	out := make([]float64, len(a))
	for i := range a {
		out[i] = a[i] - b[i]
	}
	return out, nil
}

func DifferenceValues(a, b []float64) ([]float64, error) { return SubtractValues(a, b) }

// SymmetricValueRange returns a zero-centred range suitable for Δ display.
func SymmetricValueRange(values []float64) (float64, float64) {
	maxAbs := 0.0
	for _, v := range values {
		if finiteMatch(v) && math.Abs(v) > maxAbs {
			maxAbs = math.Abs(v)
		}
	}
	if maxAbs == 0 {
		return -1, 1
	}
	return -maxAbs, maxAbs
}

// stableTraceNumbers is useful for reports and keeps deterministic physical
// ordering when callers need a copy of a match result's unmatched IDs.
func stableTraceNumbers(values []int64) []int64 {
	out := append([]int64(nil), values...)
	sort.SliceStable(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
