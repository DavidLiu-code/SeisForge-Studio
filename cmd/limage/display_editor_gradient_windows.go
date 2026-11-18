//go:build windows

package main

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

const displayGradientPrecision = 1000000 // steps per unit fraction: 0.0001%

// IDs belong to this editor session, not the serialized color mapping.
// The accepted stop slice remains the one exact source for the renderer.
type displayGradientSession struct {
	key      string
	stops    []displaycore.GradientStop
	ids      []uint64
	authored []uint64
	selected uint64
	pending  float64 // visual coordinate, always mapMin=0%, mapMax=100%
}

func (d displayGradientSession) clone() displayGradientSession {
	d.stops = slices.Clone(d.stops)
	d.ids = slices.Clone(d.ids)
	d.authored = slices.Clone(d.authored)
	return d
}

func displayEditorBindGradient(w *displayEditor) {
	key := displayDraftKey(w)
	if w.gradientKey == key && slices.Equal(w.gradientEdit.stops, w.stops) && len(w.gradientEdit.ids) == len(w.stops) {
		return
	}
	displayEditorRestoreGradient(w, w.gradientSessions[key])
}

func displayEditorRestoreGradient(w *displayEditor, state displayGradientSession) {
	w.gradientKey = displayDraftKey(w)
	w.gradientHovered = 0
	w.nodeUIKey = ""
	if state.key != w.gradientKey || len(state.ids) != len(w.stops) || !slices.Equal(state.stops, w.stops) {
		state = displayGradientSession{key: w.gradientKey, stops: slices.Clone(w.stops), ids: make([]uint64, len(w.stops)), pending: .5}
		for i := range state.ids {
			w.gradientSerial++
			state.ids[i] = w.gradientSerial
		}
	}
	w.gradientEdit = state.clone()
	w.selection = -1
	if state.selected != 0 {
		w.selection = slices.Index(state.ids, state.selected)
	}
}

func displayEditorSelectGradientNode(w *displayEditor, index int) {
	displayEditorBindGradient(w)
	w.selection, w.gradientEdit.selected = -1, 0
	if index >= 0 && index < len(w.gradientEdit.ids) {
		w.selection, w.gradientEdit.selected = index, w.gradientEdit.ids[index]
	}
}

func displayEditorTrackGradient(w *displayEditor) {
	w.gradientEdit.stops = append(w.gradientEdit.stops[:0], w.stops...)
}

func displayGradientVisual(pos float64, reverse bool) float64 {
	if reverse {
		return 1 - pos
	}
	return pos
}

func displayGradientQuantize(pos float64) float64 {
	return math.Round(pos*displayGradientPrecision) / displayGradientPrecision
}

func displayGradientPercent(pos float64) string {
	s := strconv.FormatFloat(pos*100, 'f', 4, 64)
	return strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
}

func displayGradientFractionAt(bar RECT, x int) float64 {
	if bar.Right <= bar.Left {
		return .5
	}
	return displayGradientQuantize(math.Max(0, math.Min(1, float64(x-int(bar.Left))/float64(bar.Right-bar.Left))))
}

func displayGradientBoundsBetween(left, right float64) (float64, float64) {
	// Use the same grid for dragging and typing. Leave at least one grid
	// step from the existing neighbors, without moving either neighbor.
	lo := (math.Ceil(left*displayGradientPrecision-1e-9) + 1) / displayGradientPrecision
	hi := (math.Floor(right*displayGradientPrecision+1e-9) - 1) / displayGradientPrecision
	return lo, hi
}

func displayEditorAddGradientNode(w *displayEditor) bool {
	displayEditorBindGradient(w)
	if len(w.stops) < 2 {
		return false
	}
	pending := w.gradientEdit.pending
	raw := displayGradientQuantize(displayGradientVisual(pending, displayChecked(w.controls[deReverse])))
	i := sort.Search(len(w.stops), func(i int) bool { return w.stops[i].Position >= raw })
	snap := float64(1) / displayGradientPrecision
	if i < len(w.stops) && math.Abs(w.stops[i].Position-raw) < snap || i > 0 && math.Abs(w.stops[i-1].Position-raw) < snap {
		if i == len(w.stops) || i > 0 && math.Abs(w.stops[i-1].Position-raw) < math.Abs(w.stops[i].Position-raw) {
			i--
		}
		displayEditorSelectGradientNode(w, i)
		return true
	}
	if len(w.stops) >= 512 || i == 0 || i == len(w.stops) {
		return false
	}
	lo, hi := displayGradientBoundsBetween(w.stops[i-1].Position, w.stops[i].Position)
	if raw < lo || raw > hi {
		if raw < (w.stops[i-1].Position+w.stops[i].Position)/2 {
			i--
		}
		displayEditorSelectGradientNode(w, i)
		return true
	}
	// Sample before inserting from the preserved LUT/gradient in storage
	// order. The Reverse flag only transforms the visual coordinate once.
	ramp := displaycore.ColorMapping{Stops: w.stops}
	w.stops = slices.Insert(w.stops, i, displaycore.GradientStop{Position: raw, Color: ramp.ColorAtPosition(raw)})
	w.gradientSerial++
	id := w.gradientSerial
	w.gradientEdit.ids = slices.Insert(w.gradientEdit.ids, i, id)
	w.gradientEdit.authored = append(w.gradientEdit.authored, id)
	displayEditorTrackGradient(w)
	displayEditorSelectGradientNode(w, i)
	return true
}

func displayEditorDeleteGradientNode(w *displayEditor, i int) bool {
	if i <= 0 || i+1 >= len(w.stops) {
		return false
	}
	id := w.gradientEdit.ids[i]
	w.stops = slices.Delete(w.stops, i, i+1)
	w.gradientEdit.ids = slices.Delete(w.gradientEdit.ids, i, i+1)
	w.gradientEdit.authored = slices.DeleteFunc(w.gradientEdit.authored, func(candidate uint64) bool { return candidate == id })
	if w.gradientHovered == id {
		w.gradientHovered = 0
	}
	displayEditorTrackGradient(w)
	displayEditorSelectGradientNode(w, i-1)
	return true
}

func displayEditorMoveGradientNode(w *displayEditor, visual float64, clamp bool) bool {
	i := w.selection
	if i <= 0 || i+1 >= len(w.stops) || math.IsNaN(visual) || math.IsInf(visual, 0) || visual < 0 || visual > 1 {
		return false
	}
	p := displayGradientQuantize(displayGradientVisual(visual, displayChecked(w.controls[deReverse])))
	lo, hi := displayGradientBoundsBetween(w.stops[i-1].Position, w.stops[i+1].Position)
	if lo > hi || !clamp && (p < lo || p > hi) {
		return false
	}
	w.stops[i].Position = max(lo, min(hi, p))
	displayEditorTrackGradient(w)
	return true
}

func displayEditorChooseGradientColor(w *displayEditor) bool {
	return displayEditorChooseGradientColorWith(w, func(c displaycore.RGB) (displaycore.RGB, bool) {
		choice := displayChooseColorData{Owner: w.handle, Result: uint32(rgbRef(c.R, c.G, c.B)), Custom: uintptr(unsafe.Pointer(&displayCustomColors[0])), Flags: 3}
		choice.Size = uint32(unsafe.Sizeof(choice))
		if ok, _, _ := displayChooseColor.Call(uintptr(unsafe.Pointer(&choice))); ok == 0 {
			return c, false
		}
		v := choice.Result
		return displaycore.RGB{R: byte(v), G: byte(v >> 8), B: byte(v >> 16)}, true
	})
}

func displayEditorChooseGradientColorWith(w *displayEditor, pick func(displaycore.RGB) (displaycore.RGB, bool)) bool {
	i := w.selection
	if i < 0 || i >= len(w.stops) {
		return false
	}
	id, key := w.gradientEdit.ids[i], w.gradientKey
	c, ok := pick(w.stops[i].Color)
	if !ok {
		return false // no live mutation; Cancel keeps both draft and scene
	}
	if displayEditors[w.handle] != w || w.gradientKey != key || displayEditorColorRangeLocked(w) {
		return false
	}
	i = slices.Index(w.gradientEdit.ids, id)
	if i < 0 {
		return false
	}
	w.stops[i].Color = c
	displayEditorTrackGradient(w)
	displayEditorSelectGradientNode(w, i)
	return true
}

func displayEditorDrawGradientSwatch(w *displayEditor, item *algorithmDrawItem) {
	background := displaycore.RGB{R: 105, G: 118, B: 132}
	color := displaycore.RGB{R: 224, G: 226, B: 230}
	disabled := item.state&0x0004 != 0 // ODS_DISABLED
	if !disabled && w.selection >= 0 && w.selection < len(w.stops) {
		color = w.stops[w.selection].Color
	}
	displayFill(item.dc, item.r, background)
	r := item.r
	b := int32(max(1, displayPixelDIP(2, displayDPI(w.handle))))
	r.Left, r.Top, r.Right, r.Bottom = r.Left+b, r.Top+b, r.Right-b, r.Bottom-b
	displayFill(item.dc, r, color)
	if item.state&0x0010 != 0 { // ODS_FOCUS
		algorithmDrawFocus.Call(item.dc, uintptr(unsafe.Pointer(&r)))
	}
}

func displayEditorGradientPendingText(w *displayEditor) string {
	return fmt.Sprintf("待添加：%s%%  ·  点击色带空白选位置，双击直接添加。", displayGradientPercent(w.gradientEdit.pending))
}

func displayEditorGradientPoint(w *displayEditor, x, y int) (m displaycore.ColorMapping, node int, ok bool) {
	if !w.pageColor || w.section != displayPageGradient || w.invalid || displayEditorColorRangeLocked(w) ||
		!displayWithin(w.gradient, x, y) && !displayWithin(w.nodeBand, x, y) {
		return m, -1, false
	}
	m, _, _, ready := displayEditorPreviewColor(w)
	if !ready {
		return m, -1, false
	}
	return m, displayEditorNodeHit(w, x, y, displaycore.Range{}, m), true
}

func displayEditorGradientClick(w *displayEditor, x, y int, double bool) bool {
	_, node, ok := displayEditorGradientPoint(w, x, y)
	if !ok {
		return false
	}
	pSetFocus.Call(w.body)
	if double {
		displayEditorFinishGesture(w, false, true)
	}
	if node >= 0 {
		previous := w.selection
		d := displayCaptureDraft(w)
		displayEditorSelectGradientNode(w, node)
		if double {
			displayEditorRefreshNode(w, true)
			displayEditorGradientCommand(w, deChoose, 0)
		} else if node > 0 && node+1 < len(w.stops) {
			w.gestureDraft, w.gestureNode, w.gestureMoved = &d, previous, false
			w.gradientDragX, w.dragging = x, true
			displaySetCapture.Call(w.body)
		}
	} else if displayWithin(w.gradient, x, y) {
		w.gradientEdit.pending = displayGradientFractionAt(w.gradient, x)
		if double {
			displayEditorGradientCommand(w, deAdd, 0)
		}
	}
	displayEditorRefreshNode(w, node >= 0 && !double)
	displayInvalidatePreview(w)
	return true
}

var displayGradientTrackMouse = user32.NewProc("TrackMouseEvent")

func displayEditorTrackGradientHover(w *displayEditor, x, y int) {
	_, node, ok := displayEditorGradientPoint(w, x, y)
	var hovered uint64
	if ok && node >= 0 {
		hovered = w.gradientEdit.ids[node]
	}
	if hovered != w.gradientHovered {
		w.gradientHovered = hovered
		displayInvalidatePreview(w)
	}
	if ok && !w.gradientTracking {
		event := struct {
			Size, Flags uint32
			Hwnd        uintptr
			Time        uint32
		}{Flags: 2, Hwnd: w.body} // TME_LEAVE
		event.Size = uint32(unsafe.Sizeof(event))
		tracked, _, _ := displayGradientTrackMouse.Call(uintptr(unsafe.Pointer(&event)))
		w.gradientTracking = tracked != 0
	}
}

func displayEditorClearGradientHover(w *displayEditor) {
	w.gradientTracking = false
	if w.gradientHovered != 0 {
		w.gradientHovered = 0
		displayInvalidatePreview(w)
	}
}

func displayEditorGradientDeleteKey(w *displayEditor, focus uintptr) bool {
	if focus == 0 || focus != w.body || !w.pageColor || w.section != displayPageGradient || displayEditorColorRangeLocked(w) {
		return false
	}
	return displayEditorGradientCommand(w, deDelete, 0)
}
