//go:build windows

package main

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestPoststackWaveParameterPagesPreserveDraftAndDefaults(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	h, _, e := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("STATIC"))), uintptr(unsafe.Pointer(u16("wave-settings-test"))), WS_POPUP, 0, 0, 390, 390, 0, 0, 0, 0)
	if h == 0 {
		t.Fatal(e)
	}
	oldParam, oldOwner, oldCompare, oldMode, oldPC, oldStyle := paramHwnd, paramOwner, compareHwnd, compareMode, pc, poststackWaveDisplay
	defer func() {
		pDestroyWindow.Call(h)
		paramHwnd, paramOwner, compareHwnd, compareMode, pc, poststackWaveDisplay = oldParam, oldOwner, oldCompare, oldMode, oldPC, oldStyle
	}()
	paramHwnd, paramOwner, compareHwnd, compareMode, pc = h, h, h, 0, paramControls{}
	applied := poststackWavePresetStyle(poststackWaveDefaultStyle(), true)
	applied.Mode, applied.Swing = poststackPlotWiggleVA, 1.5
	poststackWaveDisplay = applied
	createPoststackWaveParamPage()
	if pc.waveUI.tabs == 0 {
		t.Fatal("native secondary tab creation failed")
	}
	setText(pc.waveUI.swing, "2")
	poststackSelectWaveCombo(pc.waveUI.density, int(poststackWaveDensityStride))
	setText(pc.waveUI.stride, "3")
	for i := range pc.waveUI.pages {
		pc.waveUI.page = i
		showPoststackWaveParamSection(true)
	}
	draft, err := poststackParamWaveDraft()
	if err != nil || draft.Swing != 2 || draft.Stride != 3 || draft.Density != poststackWaveDensityStride || draft.NegativeColor != applied.NegativeColor || poststackWaveDisplay != applied {
		t.Fatalf("draft=%+v applied=%+v err=%v", draft, poststackWaveDisplay, err)
	}
	poststackSelectWaveCombo(pc.waveUI.refMode, 1)
	setText(pc.waveFields[2], "0")
	if _, err := poststackParamWaveDraft(); err == nil {
		t.Fatal("zero manual reference must not be accepted as Auto")
	}
	poststackWaveParamCommand(idWaveDefaults, 0)
	draft, err = poststackParamWaveDraft()
	if err != nil || draft != poststackWaveDefaultStyle() || poststackWaveDisplay != applied {
		t.Fatalf("restore must only change draft: draft=%+v applied=%+v err=%v", draft, poststackWaveDisplay, err)
	}
}
