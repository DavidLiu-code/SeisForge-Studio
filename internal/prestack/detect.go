package prestack

import (
	"context"
	"errors"
	"sort"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

type MappingCandidate struct {
	Field       string
	Byte        int
	UniqueCount int
	Coverage    float64
	Confidence  float64
	Explanation string
}

type MappingDetection struct {
	Mapping           HeaderMapping
	Candidates        []MappingCandidate
	Confidence        float64
	NeedsConfirmation bool
	Warnings          []string
}

// DetectHeaderMapping inspects a bounded, deterministic sample of trace
// headers. It never scans samples and does not select a mapping merely because
// a field has changing bytes; candidate confidence is deliberately exposed to
// the UI for manual confirmation.
func DetectHeaderMapping(ctx context.Context, reader *segy.File) (MappingDetection, error) {
	if reader == nil {
		return MappingDetection{}, errors.New("nil SEG-Y reader")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return MappingDetection{}, err
	}
	m := DefaultHeaderMapping()
	total := reader.Info.TraceCount
	if total < 1 {
		return MappingDetection{}, errors.New("empty SEG-Y file")
	}
	maxHeaders := int64(1024)
	if total < maxHeaders {
		maxHeaders = total
	}
	traces := make([]int64, maxHeaders)
	for i := range traces {
		if maxHeaders == 1 {
			traces[i] = 0
		} else {
			traces[i] = int64(i) * (total - 1) / (maxHeaders - 1)
		}
	}
	vals := map[string][]int32{"source": {}, "receiver": {}, "cdp": {}, "inline": {}, "crossline": {}, "offset": {}}
	for i, trace := range traces {
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return MappingDetection{}, err
			}
		}
		h, err := reader.ReadTraceHeader(trace)
		if err != nil {
			return MappingDetection{}, err
		}
		vals["source"] = append(vals["source"], h.FieldRecord)
		vals["receiver"] = append(vals["receiver"], h.TraceInField)
		vals["cdp"] = append(vals["cdp"], h.CDP)
		vals["inline"] = append(vals["inline"], h.Inline)
		vals["crossline"] = append(vals["crossline"], h.Crossline)
		vals["offset"] = append(vals["offset"], h.Offset)
	}
	candidate := func(field string, bytePos int) MappingCandidate {
		unique := map[int32]struct{}{}
		nonZero := 0
		for _, v := range vals[field] {
			unique[v] = struct{}{}
			if v != 0 {
				nonZero++
			}
		}
		coverage := float64(nonZero) / float64(len(vals[field]))
		u := float64(len(unique)) / float64(max(1, len(vals[field])))
		confidence := coverage * (0.65 + 0.35*u)
		if field == "offset" {
			confidence = coverage
		}
		return MappingCandidate{Field: field, Byte: bytePos, UniqueCount: len(unique), Coverage: coverage, Confidence: confidence}
	}
	inline := candidate("inline", 189)
	crossline := candidate("crossline", 193)
	cdp := candidate("cdp", 21)
	source := candidate("source", 9)
	receiver := candidate("receiver", 13)
	offset := candidate("offset", 37)
	result := MappingDetection{Mapping: m, Candidates: []MappingCandidate{source, receiver, cdp, offset, inline, crossline}}
	for i := range result.Candidates {
		result.Candidates[i].Explanation = "有限道头样本覆盖率与唯一值数量"
	}
	if inline.Confidence >= 0.6 && crossline.Confidence >= 0.6 {
		result.Confidence = (inline.Confidence + crossline.Confidence) / 2
	} else {
		result.Confidence = cdp.Confidence
	}
	result.NeedsConfirmation = result.Confidence < 0.7
	if result.NeedsConfirmation {
		result.Warnings = append(result.Warnings, "道头自动映射置信度较低，请确认 Source/Receiver/CDP/Inline/Crossline 字节")
	}
	return result, nil
}

// SortCandidates is useful for UI mapping pages that want confidence order,
// while retaining deterministic ties in SEG-Y field order.
func SortCandidates(candidates []MappingCandidate) []MappingCandidate {
	out := append([]MappingCandidate(nil), candidates...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	return out
}
