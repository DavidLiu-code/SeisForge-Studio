//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// The dataset may have been selected before this worker opens its Reader.
// Core cache identity guards the Reader's opening snapshot onward; this local
// boundary also rejects a source replacement since Dataset creation. Keep this
// stricter policy out of shared Dataset.OpenReader and legacy workspaces.
func validatePrestackDatasetReader(data *dataset.SeismicDataset, reader *segy.File) error {
	if data == nil || reader == nil {
		return fmt.Errorf("%w: missing dataset or Reader", segy.ErrSourceChanged)
	}
	m, i := data.Metadata, reader.Info
	if !strings.EqualFold(filepath.Clean(data.Path), filepath.Clean(i.Path)) ||
		m.FileSize != i.FileSize || m.Endian != i.Endian ||
		m.SampleIntervalUS != i.SampleIntervalUS || m.SamplesPerTrace != i.SamplesPerTrace ||
		m.FormatCode != i.FormatCode || m.BytesPerSample != i.BytesPerSample ||
		m.ExtendedTextHeaders != i.ExtendedTextHeaders || m.DataStart != i.DataStart ||
		m.TraceBytes != i.TraceBytes || m.TraceCount != i.TraceCount {
		return fmt.Errorf("%w: dataset and Reader layout differ; reopen the SEG-Y file", segy.ErrSourceChanged)
	}
	stat, err := os.Stat(data.Path)
	if err != nil {
		return fmt.Errorf("%w: dataset source: %w", segy.ErrSourceChanged, err)
	}
	if stat.Size() != m.FileSize || !stat.ModTime().Equal(m.ModifiedAt) {
		return fmt.Errorf("%w: dataset size or modification time differs; reopen the SEG-Y file", segy.ErrSourceChanged)
	}
	return nil
}

func openPrestackDatasetReader(data *dataset.SeismicDataset) (*segy.File, error) {
	reader, err := data.OpenReader()
	if err != nil {
		return nil, err
	}
	if err = validatePrestackDatasetReader(data, reader); err != nil {
		_ = reader.Close()
		return nil, err
	}
	return reader, nil
}

// Both index workers own their short-lived Reader. Cache lookup is deliberately
// after mapping detection/confirmation, never a substitute for that decision.
func buildPrestackAIndex(ctx context.Context, data *dataset.SeismicDataset, mapping prestackcore.HeaderMapping, detect bool, options prestackcore.CacheOptions, progress func(prestackcore.IndexProgress), result *prestackIndexResult) {
	if result == nil {
		return
	}
	if result.err = ctx.Err(); result.err != nil {
		return
	}
	reader, err := openPrestackDatasetReader(data)
	if err != nil {
		result.err = err
		return
	}
	defer reader.Close()
	if detect {
		result.detected = true
		result.detection, result.err = prestackcore.DetectHeaderMapping(ctx, reader)
		if result.err != nil {
			return
		}
		mapping = result.detection.Mapping
		if result.detection.NeedsConfirmation {
			result.err = ctx.Err()
			return
		}
	}
	result.index, result.cacheStats, result.err = prestackcore.BuildIndexCached(ctx, reader, mapping, options, progress)
}

func prestackIndexCacheSummary(stats prestackcore.IndexBuildStats) string {
	status := "缓存失效，扫描道头"
	if stats.Hit {
		status = "缓存命中"
	} else if stats.MissReason == "cache bypass requested" {
		status = "绕过缓存，扫描道头"
	}
	status += " | 加载完成"
	if stats.WriteWarning != "" {
		status += " | 缓存未保存：" + stats.WriteWarning
	}
	return status
}

type prestackCacheProgress struct {
	generation int64
	aToken     prestackAsyncToken
	bToken     prestackCompareBResult
	progress   prestackcore.IndexProgress
}

// Access only under prestackDeliveryMu. Progress uses the same owner and
// dataset/mapping/selection gates as the completed index it describes.
var prestackPendingCacheProgressA, prestackPendingCacheProgressB *prestackCacheProgress

func prestackCacheProgressPercent(p prestackcore.IndexProgress) int64 {
	if p.Total <= 0 {
		return 0
	}
	return min(int64(100), max(int64(0), p.Done*100/p.Total))
}

func prestackCacheProgressText(role string, p prestackcore.IndexProgress) string {
	stage := strings.TrimSpace(p.Stage)
	if stage == "" || stage == "扫描道头" {
		stage = "缓存失效，扫描道头"
	}
	if stage == "加载完成" {
		// A worker progress notification precedes its final source-identity,
		// cancellation and UI-token gates. Only an accepted result may claim
		// completion; the final observer can still be followed by an error.
		stage = "正在校验道头索引"
	}
	return fmt.Sprintf("%s %s %d%%（仅道头元数据）", role, stage, prestackCacheProgressPercent(p))
}

func prestackCacheProgressMatches(p *prestackCacheProgress, state prestackSession, owner uintptr, currentA prestackAsyncToken, sideB bool) bool {
	if p == nil {
		return false
	}
	if sideB {
		return state.compareBLoading && prestackCompareBResultMatches(&p.bToken, state, owner, p.generation)
	}
	return state.loading && owner == currentA.owner && p.generation == state.indexGeneration && prestackAsyncSourceMatches(p.aToken, currentA)
}

func publishPrestackCacheProgressA(ctx context.Context, token prestackAsyncToken, generation int64, p prestackcore.IndexProgress) {
	prestackDeliveryMu.Lock()
	defer prestackDeliveryMu.Unlock()
	if ctx.Err() != nil || prestackDeliveryHwnd != token.owner || prestackDeliveryOwnerToken != token.ownerToken || prestackDeliveryIndexGeneration != generation {
		return
	}
	prestackPendingCacheProgressA = &prestackCacheProgress{generation: generation, aToken: token, progress: p}
	if posted, _, _ := pPostMessageW.Call(token.owner, WM_PRESTACK_PROGRESS, uintptr(generation), 0); posted == 0 {
		prestackPendingCacheProgressA = nil
	}
}

func publishPrestackCacheProgressB(ctx context.Context, token prestackCompareBResult, p prestackcore.IndexProgress) {
	prestackDeliveryMu.Lock()
	defer prestackDeliveryMu.Unlock()
	if ctx.Err() != nil || prestackDeliveryHwnd != token.owner || prestackDeliveryOwnerToken != token.ownerToken || prestackDeliveryCompareBGeneration != token.generation {
		return
	}
	prestackPendingCacheProgressB = &prestackCacheProgress{generation: token.generation, bToken: token, progress: p}
	if posted, _, _ := pPostMessageW.Call(token.owner, WM_PRESTACK_COMPARE_B_PROGRESS, uintptr(token.generation), 0); posted == 0 {
		prestackPendingCacheProgressB = nil
	}
}

func receivePrestackCacheProgress(generation int64, sideB bool) {
	prestackDeliveryMu.Lock()
	var p *prestackCacheProgress
	if sideB {
		if prestackPendingCacheProgressB != nil && prestackPendingCacheProgressB.generation == generation {
			p, prestackPendingCacheProgressB = prestackPendingCacheProgressB, nil
		}
	} else if prestackPendingCacheProgressA != nil && prestackPendingCacheProgressA.generation == generation {
		p, prestackPendingCacheProgressA = prestackPendingCacheProgressA, nil
	}
	prestackDeliveryMu.Unlock()
	if !prestackCacheProgressMatches(p, prestackState, prestackHwnd, currentPrestackAsyncToken(), sideB) {
		return
	}
	if sideB {
		setPrestackStatus(prestackCacheProgressText("B", p.progress))
		return
	}
	pSendMessageW.Call(prestackUI.progress, PBM_SETPOS, uintptr(prestackCacheProgressPercent(p.progress)), 0)
	setPrestackStatus(prestackCacheProgressText("A", p.progress))
}
