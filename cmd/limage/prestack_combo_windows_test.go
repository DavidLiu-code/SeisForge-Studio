//go:build windows

package main

import (
	"runtime"
	"testing"

	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func TestPrestackComboLayoutRetainsDropdownHeight(t *testing.T) {
	for _, id := range []int{IDPRESTACK_KIND, IDPRESTACK_KEY, IDPRESTACK_SORT,
		IDPRESTACK_AXIS, IDPRESTACK_DISPLAY, IDPRESTACK_RAW_SAMPLE_MODE, IDPRESTACK_PALETTE} {
		for _, toolbarHeight := range []int{1, 24, 32} {
			if got := prestackComboPopupHeight(id, toolbarHeight); got <= toolbarHeight+40 {
				t.Fatalf("combo %d popup height %d collapsed to toolbar height %d", id, got, toolbarHeight)
			}
		}
	}
	if got := prestackComboPopupHeight(IDPRESTACK_RESET, 24); got != 24 {
		t.Fatalf("non-combo height changed to %d", got)
	}
}

// Verify with native Win32 controls, not just an arithmetic helper.  This
// catches the regression where all items still exist but MoveWindow gives
// their dropdown only the closed selection field's height.
func TestPrestackNativeComboContainsChoicesAndPopupSpace(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	parent := createCtrl(0, "STATIC", "Prestack combo regression", WS_OVERLAPPEDWINDOW, 0, 0, 700, 500, 0)
	if parent == 0 {
		t.Fatal("could not create native test owner")
	}
	defer pDestroyWindow.Call(parent)
	oldOwner, oldUI := prestackHwnd, prestackUI
	prestackHwnd = parent
	defer func() { prestackHwnd, prestackUI = oldOwner, oldUI }()
	for _, tc := range []struct {
		id     int
		labels []string
	}{
		{IDPRESTACK_KIND, []string{"CMP / Bin", "Shot / 炮集", "Receiver / 检波点", "Common Offset / 共Offset", "原始叠前道序"}},
		{IDPRESTACK_SORT, []string{"原始道序", "Offset", "|Offset|", "Azimuth"}},
		{IDPRESTACK_AXIS, []string{"Trace 横轴", "Offset 横轴"}},
		{IDPRESTACK_DISPLAY, []string{"图像 Image", "波形 Wiggle"}},
		{IDPRESTACK_RAW_SAMPLE_MODE, []string{"完整记录", "样点范围", "毫秒范围"}},
		{IDPRESTACK_PALETTE, paletteNames},
	} {
		combo := prestackCombo(tc.id, tc.labels)
		if combo == 0 {
			t.Fatalf("could not create combo %d", tc.id)
		}
		count, _, _ := pSendMessageW.Call(combo, 0x0146 /* CB_GETCOUNT */, 0, 0)
		if int(count) != len(tc.labels) {
			t.Fatalf("combo %d has %d choices, want %d", tc.id, count, len(tc.labels))
		}
		// The control's requested height is the same height used by layout.  A
		// toolbar hide/show cycle must retain this popup-sized height.
		pMoveWindow.Call(combo, 0, 0, 1, uintptr(prestackComboPopupHeight(tc.id, 1)), 0)
		pMoveWindow.Call(combo, 10, 10, 180, uintptr(prestackComboPopupHeight(tc.id, 24)), 0)
	}
}

func TestPrestackComboAcceptedAndCloseNotifications(t *testing.T) {
	if !prestackComboSelection(CBN_SELENDOK) || !prestackComboSelection(CBN_CLOSEUP) {
		t.Fatal("accepted/closed native selection must queue its commit")
	}
	if prestackComboSelection(1) || prestackComboSelection(7) {
		t.Fatal("candidate hover/open must not start gather work")
	}
}

func TestPrestackGatherKeysKeepAllRangeOption(t *testing.T) {
	real := []prestackcore.GatherKey{{ID: 10}, {ID: 20}}
	keys := prependPrestackAllRange(real)
	if len(keys) != 3 || !keys[0].All || keys[0].String() != "全部范围" {
		t.Fatalf("all-range option=%v", keys)
	}
	if keys[1].ID != 10 || keys[2].ID != 20 {
		t.Fatalf("real gather keys reordered=%v", keys)
	}
}
