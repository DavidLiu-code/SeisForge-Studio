//go:build windows

package main

import (
	"bytes"
	"runtime"
	"testing"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

func TestPoststackDefaultGainRemapsSavedStyleWithoutMovingExplicitLimits(t *testing.T) {
	saved := displaycore.ColorMapping{Range: displaycore.Range{Min: -10, Max: 10}, Title: "我的色标", Stops: []displaycore.GradientStop{{Position: 0, Color: displaycore.RGB{}}, {Position: 1, Color: displaycore.RGB{R: 255, G: 255, B: 255}}}}
	live := displaycore.ColorMapping{Range: displaycore.Range{Min: -4, Max: 4}}
	updated := poststackLiveColorRange(saved, live)
	if updated.Range != live.Range || updated.Title != saved.Title || saved.Range.Min != -10 {
		t.Fatalf("style/range = %+v", updated)
	}
	a, b := make([]byte, 4), make([]byte, 4)
	if !displayRenderColorMapping(&saved, []float64{3}, a) || !displayRenderColorMapping(&updated, []float64{3}, b) || bytes.Equal(a, b) {
		t.Fatalf("gain must alter displayed pixel: %v %v", a, b)
	}
	saved.SetRangePolicy(displaycore.ManualRange)
	if got := poststackLiveColorRange(saved, live); got.Range != saved.Range {
		t.Fatalf("explicit range moved: %+v", got.Range)
	}
}

func TestPoststackDisplayShortcutRoutesAndFocus(t *testing.T) {
	for _, c := range []struct {
		key           uintptr
		gain, palette int
	}{
		{VK_Q, 1, 0}, {VK_W, -1, 0}, {VK_E, 0, 1}, {VK_R, 0, -1}, {VK_LEFT, 0, 0}, {VK_RETURN, 0, 0},
	} {
		if gain, palette := poststackShortcutAction(c.key); gain != c.gain || palette != c.palette {
			t.Fatalf("key %x = (%d, %d), want (%d, %d)", c.key, gain, palette, c.gain, c.palette)
		}
	}
	for _, c := range []struct {
		target         uintptr
		class          string
		modified, want bool
	}{
		{1, "canvas", false, true}, {2, "Button", false, true}, {2, "msctls_trackbar32", false, true},
		{2, "Edit", false, false}, {2, "ComboBox", false, false}, {1, "canvas", true, false},
	} {
		if got := poststackShortcutFocus(c.target, 1, c.class, c.modified); got != c.want {
			t.Fatalf("focus %+v: %v", c, got)
		}
	}
}

func TestPoststackNativeShortcutTargetsExcludeEditorsAndOwnedWindows(t *testing.T) {
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	owner := makeSettingsTestWindow(t, "STATIC", 0)
	peer := makeSettingsTestWindow(t, "STATIC", owner)
	edit := createCtrl(owner, "EDIT", "900", WS_CHILD|WS_TABSTOP, 0, 0, 80, 22, 1)
	const cbsDropdown = 0x0002 // editable native combo-box variant
	combo := createCtrl(owner, "COMBOBOX", "", WS_CHILD|WS_TABSTOP|cbsDropdown, 0, 25, 80, 80, 2)
	button := createCtrl(owner, "BUTTON", "A", WS_CHILD|WS_TABSTOP, 0, 50, 80, 22, 3)
	if edit == 0 || combo == 0 || button == 0 {
		t.Fatal("failed to create native key-message targets")
	}
	oldHwnd, oldPath, oldTD, oldMode, oldControls := compareHwnd, compareAPath, compareTD, compareMode, cc
	oldReader, oldGain, oldLoadHwnd, oldVolume := sf, gainPercent, loadHwnd, volumeHwnd
	t.Cleanup(func() {
		compareHwnd, compareAPath, compareTD, compareMode, cc = oldHwnd, oldPath, oldTD, oldMode, oldControls
		sf, gainPercent, loadHwnd, volumeHwnd = oldReader, oldGain, oldLoadHwnd, oldVolume
	})
	compareHwnd, compareAPath, compareTD, compareMode, cc = owner, "fixture.sgy", nil, 2, compareControls{}
	sf, gainPercent, loadHwnd, volumeHwnd = nil, 20, 0, 0
	palette := 0
	cycle := func(n int) { palette += n }
	for _, target := range []uintptr{edit, combo, peer} {
		if translatePoststackDisplayShortcut(target, owner, VK_Q, cycle) || translatePoststackDisplayShortcut(target, owner, VK_E, cycle) || gainPercent != 20 || palette != 0 {
			t.Fatal("data edit, combo box or owned dialog consumed a native shortcut")
		}
	}
	if !translatePoststackDisplayShortcut(owner, owner, VK_Q, cycle) || gainPercent != 21 ||
		!translatePoststackDisplayShortcut(button, owner, VK_W, cycle) || gainPercent != 20 ||
		!translatePoststackDisplayShortcut(button, owner, VK_E, cycle) || palette != 1 ||
		!translatePoststackDisplayShortcut(owner, owner, VK_R, cycle) || palette != 0 {
		t.Fatalf("canvas/button shortcut route: gain=%g palette delta=%d", gainPercent, palette)
	}
	t.Log("native Compare canvas and button accept Q/W/E/R; EDIT, COMBOBOX and independent owned HWND leave gain/palette unchanged")
}

func TestPoststackStatusGroupsAndDoesNotOccludeCoordinates(t *testing.T) {
	err := "等分数须为 1–20"
	if got := compareNoticeText([]compareTickNotice{{"A", err}, {"A", err}, {"B", err}, {"A−B", "没有共同位置"}}); got != "A/B："+err+" | A−B：没有共同位置" {
		t.Fatalf("status = %s", got)
	}
	if got := poststackTickNotice(displaycore.TickPlan{SinglePoint: true}); got != "" {
		t.Fatalf("a legal single point is not a warning: %s", got)
	}
	for _, w := range []int{1080, 1120, 1360, 1932} {
		for _, progress := range []bool{false, true} {
			r := compareChromeStatusRects(w, progress)
			if r.status.Right > int32(w) || r.status.Left <= r.coordinates.Right || r.status.Top != r.coordinates.Top || progress && (r.progress.Left <= r.coordinates.Right || r.progress.Right >= r.status.Left) {
				t.Fatalf("w=%d progress=%v: %+v", w, progress, r)
			}
		}
	}
}

func TestPoststackGainDoesNotRequireLegacyReaderInCompare(t *testing.T) {
	oldReader, oldPath, oldGain := sf, compareAPath, gainPercent
	oldCompare, oldVolume := compareHwnd, volumeHwnd
	t.Cleanup(func() {
		sf, compareAPath, gainPercent = oldReader, oldPath, oldGain
		compareHwnd, volumeHwnd = oldCompare, oldVolume
	})
	sf, compareAPath, gainPercent, compareHwnd, volumeHwnd = nil, "compare-only.sgy", 30, 0, 0
	changeGainPercent(1)
	if gainPercent != 31 {
		t.Fatalf("Q in Compare: gain = %g", gainPercent)
	}
	changeGainPercent(-1)
	if gainPercent != 30 {
		t.Fatalf("W in Compare: gain = %g", gainPercent)
	}
}
