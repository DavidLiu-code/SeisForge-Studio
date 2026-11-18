//go:build windows

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

// The Mapping target controls both manual Apply and automatic detection. A B
// request must never fall through to A merely because B is absent or busy.
func dispatchPrestackMappingAuto(state prestackSession, detectA func(), detectB func(string, prestackcore.HeaderMapping)) bool {
	if !state.mappingTargetB {
		if detectA == nil {
			return false
		}
		detectA()
		return true
	}
	if state.compareBDataset == nil || state.compareBLoading || detectB == nil {
		return false
	}
	detectB(state.compareBDataset.Path, state.compareBMapping)
	return true
}

// This worker owns every reader it opens, including the early-return paths
// for cancellation and low-confidence mapping detection. It only writes its
// private result, never A or the window's mutable session.
func buildPrestackCompareBIndex(ctx context.Context, result *prestackCompareBResult) {
	buildPrestackCompareBIndexCached(ctx, result, prestackcore.CacheOptions{}, nil)
}

func buildPrestackCompareBIndexCached(ctx context.Context, result *prestackCompareBResult, options prestackcore.CacheOptions, progress func(prestackcore.IndexProgress)) {
	if result == nil {
		return
	}
	if result.err = ctx.Err(); result.err != nil {
		return
	}
	result.dataset, result.err = dataset.NewManager().Open(result.path)
	if result.err != nil {
		return
	}
	reader, err := openPrestackDatasetReader(result.dataset)
	if err != nil {
		result.err = err
		return
	}
	defer reader.Close()
	if result.detected {
		result.detection, result.err = prestackcore.DetectHeaderMapping(ctx, reader)
		if result.err != nil {
			return
		}
		result.mapping = result.detection.Mapping
		if result.detection.NeedsConfirmation {
			result.err = ctx.Err()
			return
		}
	}
	result.index, result.cacheStats, result.err = prestackcore.BuildIndexCached(ctx, reader, result.mapping, options, progress)
}

// Persistent metadata identity is checked separately from presentation. The
// full delivery predicate still rejects every stale page/selection token.
func prestackCompareBIdentityMatches(r *prestackCompareBResult, state prestackSession, owner uintptr, generation int64) bool {
	return r != nil && r.generation == generation && r.generation == state.compareBGeneration && r.owner == owner &&
		r.ownerToken == state.ownerToken && r.workspaceGeneration == state.workspaceGeneration &&
		r.datasetGeneration == state.datasetGeneration && r.aMappingGeneration == state.mappingGeneration &&
		r.bDatasetGeneration == state.compareBDatasetGeneration && r.mappingGeneration == state.compareBMappingGeneration
}

// An obsolete completion of the *current* B request needs a terminal action.
// Discard its index, cancel its context, and start again with the latest UI
// identity. Otherwise a page/selection change leaves B stuck at "loading".
// Replaced/closed B and a different owner/dataset/mapping must never restart.
func retryPrestackCompareBViewChange(r *prestackCompareBResult, state *prestackSession, owner uintptr, generation int64, restart func(string, *prestackcore.HeaderMapping, bool)) bool {
	if state == nil || !state.compareBLoading || restart == nil ||
		!prestackCompareBIdentityMatches(r, *state, owner, generation) ||
		prestackCompareBResultMatches(r, *state, owner, generation) || strings.TrimSpace(r.path) == "" {
		return false
	}
	if state.compareBCancel != nil {
		state.compareBCancel()
		state.compareBCancel = nil
	}
	state.compareBLoading = false
	mapping := state.compareBMapping
	restart(r.path, &mapping, r.detected)
	return true
}

func prestackCompareBMappingSummary(detection prestackcore.MappingDetection) string {
	var summary strings.Builder
	fmt.Fprintf(&summary, "B 独立映射自动检测 | 置信度 %.1f%% | A 映射不变。\r\n", 100*detection.Confidence)
	summary.WriteString("SEG-Y 字节为 1-based；0 表示禁用可选字段。\r\n")
	for _, c := range detection.Candidates {
		fmt.Fprintf(&summary, "%s | byte %d | 覆盖 %.1f%% | 唯一值 %d | 置信度 %.1f%% | %s\r\n", c.Field, c.Byte, 100*c.Coverage, c.UniqueCount, 100*c.Confidence, c.Explanation)
	}
	for _, warning := range detection.Warnings {
		summary.WriteString("警告：" + warning + "\r\n")
	}
	if detection.NeedsConfirmation {
		summary.WriteString("请检查或修改 B 字段，然后点击“确认映射并建立索引”；不会修改 A。")
	}
	return summary.String()
}

// Keep the summary and byte edits bound to the same Mapping target. In
// particular, a completed B detection must never leave B's confidence or
// warnings next to A's fields after the target selector changes.
func prestackMappingSummary(state prestackSession) string {
	if state.mappingTargetB {
		if state.compareBLoading {
			return "B 独立映射：正在检测或建立索引；A 映射保持不变。"
		}
		if state.compareBDataset == nil {
			return "B 未加载。请先在 Compare 页面加载 B；A 映射保持不变。"
		}
		summary := "B 独立映射：尚未执行 B 自动检测；当前字节设置独立于 A。\r\nSEG-Y 字节为 1-based；0 表示禁用可选字段。\r\n"
		if state.compareBDetection.Mapping != (prestackcore.HeaderMapping{}) {
			summary = prestackCompareBMappingSummary(state.compareBDetection)
		}
		if state.compareBIndex != nil {
			for _, warning := range state.compareBIndex.Warnings {
				summary += "B 索引警告：" + warning + "\r\n"
			}
		}
		return summary
	}
	var summary strings.Builder
	summary.WriteString("A 道头映射。SEG-Y 字节为 1-based；0 表示禁用可选字段。Offset 不应用坐标比例因子。\r\n")
	fmt.Fprintf(&summary, "A 最近自动检测置信度 %.1f%%\r\n", state.detection.Confidence*100)
	for _, c := range state.detection.Candidates {
		fmt.Fprintf(&summary, "%s | byte %d | 覆盖 %.1f%% | 唯一值 %d | 置信度 %.1f%% | %s\r\n", c.Field, c.Byte, c.Coverage*100, c.UniqueCount, c.Confidence*100, c.Explanation)
	}
	for _, warning := range state.detection.Warnings {
		summary.WriteString("A 检测警告：" + warning + "\r\n")
	}
	if state.index != nil {
		for _, warning := range state.index.Warnings {
			summary.WriteString("A 索引警告：" + warning + "\r\n")
		}
	} else if state.detection.NeedsConfirmation {
		summary.WriteString("请检查 A 字段后点击“确认映射并建立索引”。")
	}
	return summary.String()
}
