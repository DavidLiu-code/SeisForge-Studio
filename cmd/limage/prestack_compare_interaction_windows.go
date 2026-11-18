//go:build windows

package main

import (
	"math"
	"strings"
	"syscall"
	"unsafe"

	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

var pPrestackGetClassName = user32.NewProc("GetClassNameW")

// Text entry owns its character keys, including the e in a scientific number.
// Buttons and closed combo boxes retain the workspace shortcuts.
func prestackTextInputClass(name string) bool {
	name = strings.ToLower(name)
	return name == "edit" || strings.HasPrefix(name, "richedit")
}

func prestackTextInput(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	var name [128]uint16
	n, _, _ := pPrestackGetClassName.Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	return n != 0 && prestackTextInputClass(syscall.UTF16ToString(name[:]))
}

// Display changes must run one renderer, not read A twice when Compare is open.
func renderPrestackDisplayChange(page int, gather, compare func()) {
	if page == 3 {
		compare()
	} else {
		gather()
	}
}

// Panel titles do not represent samples. Keep all pointer math in the same
// image rectangle that paintPrestackCompare displays below its 34-pixel title.
func prestackCompareRasterRect(panel RECT) RECT {
	if panel.Right-panel.Left <= 2 || panel.Bottom-panel.Top <= 35 {
		return RECT{}
	}
	panel.Left++
	panel.Top += 34
	panel.Right--
	panel.Bottom--
	return panel
}

func prestackCompareAxisIndex(pixel, start, end, first, last int) int {
	if last <= first || end-start <= 1 {
		return first
	}
	f := float64(clampInt(pixel-start, 0, end-start-1)) / float64(end-start-1)
	return first + int(math.Round(f*float64(last-first)))
}

func prestackCompareZoomRange(p0, p1, start, end, first, last int) (int, int) {
	a := prestackCompareAxisIndex(p0, start, end, first, last)
	b := prestackCompareAxisIndex(p1, start, end, first, last)
	return min(a, b), max(a, b)
}

func prestackComparePanRange(p0, p1, pixels, first, last, total int) (int, int) {
	span := max(0, last-first)
	shift := int(math.Round(float64(p0-p1) * float64(span) / float64(max(1, pixels-1))))
	lo := clampInt(first+shift, 0, max(0, total-1-span))
	return lo, lo + span
}

func prestackCompareTraceForPanel(panel int, row prestackcore.CompareLogicalRow, deltaCompatible bool) (int64, string, bool) {
	switch panel {
	case 0:
		return row.ATrace, "A", row.HasA
	case 1:
		return row.BTrace, "B", row.HasB
	case 2:
		return row.ATrace, "A", deltaCompatible && row.Classification == prestackcore.CompareRowMatched && row.HasA && row.HasB
	default:
		return 0, "", false
	}
}
