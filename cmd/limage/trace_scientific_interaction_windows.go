//go:build windows

package main

import (
	"math"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

// Unlike the applied axis settings, a graph viewport is intentionally erased
// by Reset Zoom. It never owns source samples, filtering, or FFT options.
type traceScientificViewport struct {
	waveX, waveY, specX, specDB, specLinear displaycore.Range
}

func (v traceScientificViewport) apply(o *traceScientificPlotOptions) {
	if v.waveX.Valid() {
		o.AmpSet, o.AmpMin, o.AmpMax = true, v.waveX.Min, v.waveX.Max
	}
	if v.waveY.Valid() {
		o.TimeSet, o.TimeMinUS, o.TimeMaxUS = true, v.waveY.Min*1e6, v.waveY.Max*1e6
	}
	if v.specX.Valid() {
		o.FrequencySet, o.FrequencyMinHz, o.FrequencyMaxHz = true, v.specX.Min, v.specX.Max
	}
	if v.specDB.Valid() {
		o.DBSet, o.DBMin, o.DBMax = true, v.specDB.Min, v.specDB.Max
	}
	if v.specLinear.Valid() {
		o.LinearSet, o.LinearMin, o.LinearMax = true, v.specLinear.Min, v.specLinear.Max
	}
}

type traceScientificBoxDrag struct {
	owner            uintptr
	plot             traceScientificBounds
	start, end       traceScientificPoint
	axes             traceScientificAxes
	spectrum, linear bool
}

var traceScientificBox traceScientificBoxDrag

func (d traceScientificBoxDrag) rectangle(threshold float64) (traceScientificBounds, bool) {
	end := traceScientificPoint{max(d.plot.l, min(d.end.x, d.plot.r)), max(d.plot.t, min(d.end.y, d.plot.b))}
	r := traceScientificBounds{min(d.start.x, end.x), min(d.start.y, end.y), max(d.start.x, end.x), max(d.start.y, end.y)}
	return r, r.width() >= threshold && r.height() >= threshold
}

func traceScientificUnproject(r traceScientificBounds, axes traceScientificAxes, p traceScientificPoint) (float64, float64) {
	f, g := (p.x-r.l)/r.width(), (p.y-r.t)/r.height()
	if axes.x.Reverse {
		f = 1 - f
	}
	if axes.y.Down == axes.y.Reverse {
		g = 1 - g
	}
	return axes.x.View.Min*(1-f) + axes.x.View.Max*f, axes.y.View.Min*(1-g) + axes.y.View.Max*g
}

func (d traceScientificBoxDrag) ranges(threshold float64) (displaycore.Range, displaycore.Range, bool) {
	r, ok := d.rectangle(threshold)
	if !ok || !d.axes.valid() {
		return displaycore.Range{}, displaycore.Range{}, false
	}
	a, b := traceScientificUnproject(d.plot, d.axes, traceScientificPoint{r.l, r.t})
	x, y := traceScientificUnproject(d.plot, d.axes, traceScientificPoint{r.r, r.b})
	xRange, yRange := displaycore.Range{Min: min(a, x), Max: max(a, x)}, displaycore.Range{Min: min(b, y), Max: max(b, y)}
	return xRange, yRange, xRange.Valid() && yRange.Valid()
}

func traceScientificCancelBox(h uintptr, release bool) bool {
	if h == 0 || traceScientificBox.owner != h {
		return false
	}
	traceScientificBox = traceScientificBoxDrag{}
	if release {
		pReleaseCapture.Call()
	}
	pInvalidateRect.Call(h, 0, 0)
	return true
}

func traceScientificMouseBox(h uintptr, msg uint32, p traceScientificPoint) bool {
	if traceScientificBox.owner == h {
		switch msg {
		case WM_MOUSEMOVE:
			traceScientificBox.end = p
			pInvalidateRect.Call(h, 0, 0)
			return true
		case WM_LBUTTONUP:
			d := traceScientificBox
			d.end = p
			traceScientificCancelBox(h, true)
			threshold := float64(displayPixelDIP(5, displayDPI(h)))
			if a, b, ok := d.ranges(threshold); ok {
				z := &traceScientificStyle.zoom
				if !d.spectrum {
					z.waveX, z.waveY = a, b
				} else {
					z.specX = a
					if d.linear {
						z.specLinear = b
					} else {
						z.specDB = b
					}
				}
			}
			return true
		}
	}
	if msg != WM_LBUTTONDOWN || traceScientificDrag.active {
		return false
	}
	scene := traceScientificActiveScene
	for _, hit := range scene.axisHits {
		r := hit.plot
		// Axes themselves and their exterior labels remain outside the box hit.
		if p.x <= r.l || p.x >= r.r || p.y <= r.t || p.y >= r.b {
			continue
		}
		spec := hit.key == traceScientificSpecKey
		which := 0
		if spec {
			which = 1
		}
		traceScientificBox = traceScientificBoxDrag{owner: h, plot: r, start: p, end: p, axes: scene.effectiveAxes[which], spectrum: spec, linear: scene.linearSpectrum}
		pSetFocus.Call(h)
		pSetCapture.Call(h)
		return true
	}
	return false
}

func paintTraceScientificBox(h, hdc uintptr, top int) {
	if traceScientificBox.owner != h {
		return
	}
	if r, visible := traceScientificBox.rectangle(float64(displayPixelDIP(5, displayDPI(h)))); visible {
		pixels := RECT{Left: int32(math.Round(r.l)), Top: int32(math.Round(r.t)) + int32(top), Right: int32(math.Round(r.r)), Bottom: int32(math.Round(r.b)) + int32(top)}
		traceScientificTint(hdc, pixels, uint32(rgbRef(36, 107, 177)))
	}
}

func traceScientificCurveMenu() {
	owner := traceAnalysisHwnd
	menu, _, _ := displayCreatePopup.Call()
	if menu == 0 || owner == 0 {
		return
	}
	defer displayDestroyMenu.Call(menu)
	add := func(id, flags uintptr, text string) {
		displayAppendMenu.Call(menu, flags, id, uintptr(unsafe.Pointer(u16(text))))
	}
	o := &traceScientificStyle.options
	flag := func(on bool) uintptr {
		if on {
			return 8 // MF_CHECKED
		}
		return 0
	}
	add(26, flag(!o.ResearchStyle), "当前样式")
	add(27, flag(o.ResearchStyle), "科研简洁")
	add(28, flag(!o.HideGrid), "显示浅色网格")
	add(0, 0x800, "") // MF_SEPARATOR
	for i, role := range []string{"A", "B", "A−B"} {
		base := uintptr(1 + 6*i)
		add(base, 0, role+" 颜色…")
		value := o.CurvePattern[i]
		if value == 0 {
			value = i + 1
		}
		for j, pattern := range []string{"实线", "虚线", "点划线"} {
			flags := uintptr(0)
			if j+1 == value {
				flags = 8
			} // MF_CHECKED
			add(base+uintptr(j+1), flags, role+" · "+pattern)
		}
		add(0, 0x800, "") // MF_SEPARATOR
	}
	add(25, 0, "恢复来源颜色和线型")
	var p POINT
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	command, _, _ := displayTrackPopup.Call(menu, 0x102, uintptr(p.X), uintptr(p.Y), 0, owner, 0)
	if command == 0 || owner != traceAnalysisHwnd {
		return
	}
	switch command {
	case 26, 27:
		o.ResearchStyle = command == 27
	case 28:
		o.HideGrid = !o.HideGrid
	case 25:
		o.CurveColorSet, o.CurvePattern = [3]bool{}, [3]int{}
	default:
		role, action := (int(command)-1)/6, (int(command)-1)%6
		if role < 0 || role >= 3 {
			return
		}
		if action == 0 {
			s := traceScientificScene{dpi: 96, style: *o}
			choice := displayChooseColorData{Owner: owner, Result: s.ink([]string{"A", "B", "A−B"}[role]).ink, Custom: uintptr(unsafe.Pointer(&displayCustomColors[0])), Flags: 3}
			choice.Size = uint32(unsafe.Sizeof(choice))
			if ok, _, _ := displayChooseColor.Call(uintptr(unsafe.Pointer(&choice))); ok == 0 || traceAnalysisHwnd != owner {
				return
			}
			o.CurveColor[role], o.CurveColorSet[role] = choice.Result, true
		} else {
			o.CurvePattern[role] = action
		}
	}
	persistTraceScientificStylePreference()
	traceScientificRepaint()
}
