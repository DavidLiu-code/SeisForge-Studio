//go:build windows

package main

import (
	"context"
	"errors"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

// Immutable UI-thread snapshot. This worker has no access to prestackState.
type prestackCompareRenderRequest struct {
	token              prestackCompareRenderResult
	aData, bData       *dataset.SeismicDataset
	rows               []prestackcore.CompareLogicalRow
	denseRows, agc     bool
	gain               float64
	useLimits          bool
	limitMin, limitMax float64
	axisCompatible     bool
}

// Preserve the existing float-values -> shared range -> A-B -> palette/BGRA
// pipeline while giving every reader a lifetime ending before publication.
func renderPrestackCompareFrame(ctx context.Context, request prestackCompareRenderRequest) *prestackCompareRenderResult {
	result := request.token
	if result.err = ctx.Err(); result.err != nil {
		return &result
	}
	if request.aData == nil {
		result.err = errors.New("Compare A dataset unavailable")
		return &result
	}
	ra, err := request.aData.OpenReader()
	if err != nil {
		result.err = err
		return &result
	}
	defer ra.Close()
	var rb *segy.File
	if prestackCompareHasSide(request.rows, true) && request.bData != nil {
		rb, result.bErr = request.bData.OpenReader()
	}
	if rb != nil {
		defer rb.Close()
	}
	opts := segy.RenderOptions{Width: result.width, Height: result.height, SampleStart: int(result.sampleFirst), SampleEnd: int(result.sampleLast), AGC: request.agc, GainPercent: request.gain, ClipPercent: 99, DisplayMode: segy.DisplayAdaptive, ReadStrategy: segy.ReadStrategySparseMapped, Workers: 2}
	av, err := renderPrestackCompareSideValues(ctx, ra, request.rows, false, request.denseRows, opts)
	if err != nil {
		result.err = err
		return &result
	}
	var bv []float64
	if rb != nil {
		bv, result.bErr = renderPrestackCompareSideValues(ctx, rb, request.rows, true, request.denseRows, opts)
		if result.bErr != nil {
			bv = nil
		}
	}
	lo, hi := prestackCompareSharedDisplayRange(av, bv, request.gain, request.useLimits, request.limitMin, request.limitMax)
	result.a = prestackCompareValuesBGRA(av, lo, hi, result.palette)
	if len(bv) > 0 {
		result.b = prestackCompareValuesBGRA(bv, lo, hi, result.palette)
	}
	if request.axisCompatible && len(bv) > 0 && prestackCompareHasMatchedSlot(request.rows, result.width) {
		diff, diffErr := prestackCompareDifferenceSlots(av, bv, request.rows, result.width, result.height)
		if diffErr != nil {
			result.bErr = diffErr
		} else {
			lo, hi = prestackCompareDeltaDisplayRange(diff, request.gain, request.useLimits, request.limitMin, request.limitMax)
			result.delta = prestackCompareValuesBGRA(diff, lo, hi, result.palette)
		}
	}
	return &result
}

// The delivery registry is mutex-protected on BOTH the UI and worker sides.
// Full owner/dataset/mapping/selection/display/size validation still happens
// in acceptPrestackCompareCompletion on the UI thread before swapping pixels.
// A supplied notifier is used by barrier tests; production posts to Win32.
func publishPrestackCompareRender(ctx context.Context, result *prestackCompareRenderResult, notify func(uintptr, int64) bool) bool {
	if result == nil || ctx.Err() != nil {
		return false
	}
	prestackDeliveryMu.Lock()
	defer prestackDeliveryMu.Unlock()
	owner := uintptr(result.owner)
	if prestackDeliveryHwnd != owner || prestackDeliveryOwnerToken != result.ownerToken ||
		prestackDeliveryCompareRenderGeneration != result.generation || ctx.Err() != nil {
		return false
	}
	prestackPendingCompareRender = result
	posted := false
	if notify != nil {
		posted = notify(owner, result.generation)
	} else {
		r, _, _ := pPostMessageW.Call(owner, WM_PRESTACK_COMPARE_RENDER, uintptr(result.generation), 0)
		posted = r != 0
	}
	if !posted {
		prestackPendingCompareRender = nil
	}
	return posted
}
