//go:build windows

package main

import (
	"errors"
	"fmt"
	"math"
	"syscall"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
)

func temporalFilterEnumLabel(id, value string) string {
	switch id {
	case poststack.FilterResponseParameter:
		return poststack.FilterResponse(value).Label()
	case poststack.FilterMethodParameter:
		return poststack.FilterMethod(value).Label()
	case poststack.FilterPhaseParameter:
		return poststack.FilterPhase(value).Label()
	}
	return value
}

func selectedAlgorithmFilterDesign() (response poststack.FilterResponse, method poststack.FilterMethod) {
	if cz.moduleID != poststack.BandpassAlgorithmID {
		return
	}
	for _, field := range cz.parameters {
		id := field.Descriptor.ID
		if (id != poststack.FilterResponseParameter && id != poststack.FilterMethodParameter) || field.Value == 0 {
			continue
		}
		i, _, _ := pSendMessageW.Call(field.Value, CB_GETCURSEL, 0, 0)
		if i >= uintptr(len(field.Descriptor.Options)) {
			continue
		}
		if id == poststack.FilterResponseParameter {
			response = poststack.FilterResponse(field.Descriptor.Options[i])
		} else {
			method = poststack.FilterMethod(field.Descriptor.Options[i])
		}
	}
	return
}

func refreshAlgorithmFilterApplicability() bool {
	active := cz.moduleID == poststack.BandpassAlgorithmID
	response, method := selectedAlgorithmFilterDesign()
	markAlgorithmFilterApplicability(cz.parameters, active, response, method)
	return active
}

func markAlgorithmFilterApplicability(rows []algorithmParameterControl, active bool, response poststack.FilterResponse, method poststack.FilterMethod) {
	for i := range rows {
		rows[i].Omit = active && !poststack.FilterParameterApplies(rows[i].Descriptor.ID, response, method)
	}
}

func refreshFormalTemporalResponse(open bool) {
	owner := cadzowHwnd
	if !open && temporalFilterResponseOwners[owner] == 0 {
		return
	}
	_, values, err := readAlgorithmParameters()
	dtUS, samples := 0, 0
	if compareTD != nil && compareTD.fa != nil {
		dtUS = compareTD.fa.Info.SampleIntervalUS
		samples = compareTD.fa.Info.SamplesPerTrace
	}
	var p poststack.ViewBandpass
	var contextWarning string
	if err == nil {
		p, err = poststack.BandpassParameters(values, dtUS)
	}
	if err == nil {
		if minimum, _ := p.MinSamples(dtUS); samples < minimum {
			contextWarning = fmt.Sprintf("当前真实时间上下文 %d 点，不足 %d 点；不能执行。", samples, minimum)
		}
	}
	if open {
		showTemporalFilterResponseModel(owner, dtUS, p, err, contextWarning)
	} else {
		updateTemporalFilterResponse(temporalFilterResponseOwners[owner], dtUS, p, err, contextWarning)
	}
}

type temporalResponseModel struct {
	p              poststack.ViewBandpass
	dtUS, order    int
	minimumSamples int
	hz, db         []float64
	contextWarning string
	err            error
}

func makeTemporalResponseModel(dtUS int, p poststack.ViewBandpass, err error, contextWarning string) temporalResponseModel {
	model := temporalResponseModel{p: p, dtUS: dtUS, err: err, contextWarning: contextWarning}
	if model.err != nil {
		return model
	}
	model.order, model.err = p.ImplementationOrder(dtUS)
	if model.err != nil {
		return model
	}
	nyquist := 500000 / float64(dtUS)
	model.hz = make([]float64, 513)
	for i := range model.hz {
		model.hz[i] = nyquist * float64(i) / float64(len(model.hz)-1)
	}
	model.hz[len(model.hz)-1] = nyquist
	model.db, model.err = p.ResponseDB(dtUS, model.hz)
	model.minimumSamples, _ = p.MinSamples(dtUS)
	return model
}

var temporalFilterResponseModels = map[uintptr]temporalResponseModel{}
var temporalFilterResponseOwners = map[uintptr]uintptr{}
var temporalFilterResponseCallback = syscall.NewCallback(temporalResponseWndProc)
var temporalFilterResponseRegistered bool

func showTemporalFilterResponse(owner uintptr, dtUS int, p poststack.ViewBandpass) {
	showTemporalFilterResponseModel(owner, dtUS, p, nil, "")
}

func updateTemporalFilterResponse(h uintptr, dtUS int, p poststack.ViewBandpass, err error, contextWarning string) {
	if h != 0 {
		temporalFilterResponseModels[h] = makeTemporalResponseModel(dtUS, p, err, contextWarning)
		pInvalidateRect.Call(h, 0, 1)
	}
}

func showTemporalFilterResponseModel(owner uintptr, dtUS int, p poststack.ViewBandpass, err error, contextWarning string) {
	h := temporalFilterResponseOwners[owner]
	if h == 0 {
		const className = "Limage64TemporalFilterResponse"
		hi, _, _ := pGetModuleHandleW.Call(0)
		if !temporalFilterResponseRegistered {
			cursor, _, _ := pLoadCursorW.Call(0, IDC_ARROW)
			registerClass(className, temporalFilterResponseCallback, COLOR_WINDOW+1, hi, cursor)
			temporalFilterResponseRegistered = true
		}
		dpi, area := displayDPI(owner), algorithmMonitorArea(owner)
		width := min(int(area.Right-area.Left), displayPixelDIP(600, dpi))
		height := min(int(area.Bottom-area.Top), displayPixelDIP(340, dpi))
		left := int(area.Left) + (int(area.Right-area.Left)-width)/2
		top := int(area.Top) + (int(area.Bottom-area.Top)-height)/2
		h, _, _ = pCreateWindowExW.Call(settingsToolWindowStyle,
			uintptr(unsafe.Pointer(u16(className))), uintptr(unsafe.Pointer(u16("滤波响应预览 — SeisForge Studio"))),
			WS_OVERLAPPEDWINDOW, uintptr(left), uintptr(top), uintptr(width), uintptr(height), owner, 0, hi, 0)
		if h == 0 {
			return
		}
		temporalFilterResponseOwners[owner] = h
	}
	updateTemporalFilterResponse(h, dtUS, p, err, contextWarning)
	showOwnedSettingsWindow(h)
}

func temporalResponseWndProc(h uintptr, msg uint32, w, l, _, _ uintptr) uintptr {
	switch msg {
	case WM_PAINT:
		var ps PAINTSTRUCT
		dc, _, _ := pBeginPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
		var rc RECT
		pGetClientRect.Call(h, uintptr(unsafe.Pointer(&rc)))
		brush, _, _ := algorithmSysColorBrush.Call(COLOR_WINDOW)
		pFillRect.Call(dc, uintptr(unsafe.Pointer(&rc)), brush)
		if model, ok := temporalFilterResponseModels[h]; ok {
			paintTemporalResponse(dc, rc, displayDPI(h), model)
		}
		pEndPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
		return 0
	case WM_SIZE, 0x02e0: // WM_DPICHANGED
		if msg == 0x02e0 && l != 0 {
			r := (*RECT)(unsafe.Pointer(l))
			pSetWindowPos.Call(h, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0x0014)
		}
		pInvalidateRect.Call(h, 0, 1)
		return 0
	case WM_GETMINMAXINFO:
		if l != 0 {
			m := (*MINMAXINFO)(unsafe.Pointer(l))
			area, dpi := algorithmMonitorArea(h), displayDPI(h)
			m.PtMinTrackSize.X = int32(min(int(area.Right-area.Left), displayPixelDIP(415, dpi)))
			m.PtMinTrackSize.Y = int32(min(int(area.Bottom-area.Top), displayPixelDIP(290, dpi)))
		}
		return 0
	case WM_CLOSE:
		owner, _, _ := settingsWindowOwner.Call(h, 4) // GW_OWNER
		closeOwnedSettingsWindow(h, owner)
		return 0
	case WM_DESTROY:
		for owner, child := range temporalFilterResponseOwners {
			if child == h {
				delete(temporalFilterResponseOwners, owner)
				break
			}
		}
		delete(temporalFilterResponseModels, h)
	case WM_KEYDOWN:
		if w == 0x1b {
			pSendMessageW.Call(h, WM_CLOSE, 0, 0)
			return 0
		}
	}
	result, _, _ := pDefWindowProcW.Call(h, uintptr(msg), w, l)
	return result
}

func paintTemporalResponse(dc uintptr, rect RECT, dpi int, model temporalResponseModel) {
	width, height := int(rect.Right-rect.Left), int(rect.Bottom-rect.Top)
	toPx := func(v int) int { return displayPixelDIP(float64(v), dpi) }
	text := func(s string, l, t, r, b int, format uint32) {
		box := RECT{Left: rect.Left + int32(l), Top: rect.Top + int32(t), Right: rect.Left + int32(r), Bottom: rect.Top + int32(b)}
		if format&0x10 == 0 { // DT_WORDBREAK vs. DT_SINGLELINE.
			format |= DT_SINGLELINE
		}
		pDrawTextW.Call(dc, uintptr(unsafe.Pointer(u16(s))), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&box)), uintptr(format))
	}
	pSetBkMode.Call(dc, TRANSPARENT)
	pSetTextColor.Call(dc, rgbRef(41, 51, 67))
	f := displayNewPlotFonts(10, dpi, 0)
	defer f.close()
	restore := displaySelectPlotFont(dc, f.han)
	defer restore()
	if model.err == nil && len(model.db) == 0 {
		model.err = errors.New("无可用响应")
	}
	if model.err != nil {
		text("参数待校验："+model.err.Error(), toPx(12), toPx(12), width-toPx(12), height-toPx(10), 0x10)
		return
	}
	p := model.p.Canonical()
	summary := p.Summary()
	switch p.Method {
	case poststack.FilterChebyshevI:
		summary += fmt.Sprintf(" · Rp %.4g dB", p.RippleDB)
	case poststack.FilterChebyshevII:
		summary += fmt.Sprintf(" · Rs %.4g dB", p.AttenuationDB)
	case poststack.FilterElliptic:
		summary += fmt.Sprintf(" · Rp %.4g / Rs %.4g dB", p.RippleDB, p.AttenuationDB)
	}
	nyquist := model.hz[len(model.hz)-1]
	contextLine := fmt.Sprintf("dt %.6g ms · Nyquist %.6g Hz · 单程实现 %d 阶", float64(model.dtUS)/1000, nyquist, model.order)
	if model.contextWarning == "" {
		contextLine += fmt.Sprintf(" · 至少 %d 点上下文", model.minimumSamples)
	} else {
		contextLine += " · " + model.contextWarning
	}
	margin := toPx(12)
	text(summary, margin, margin, width-margin, toPx(38), 0x10)
	text(contextLine, margin, toPx(41), width-margin, toPx(77), 0x10)
	text(p.EdgeMeaning(), margin, height-toPx(28), width-margin, height, 0x10)
	left, top := toPx(58), toPx(78)
	right, bottom := width-toPx(38), height-toPx(51)
	if right-left < toPx(105) || bottom-top < toPx(45) {
		return
	}
	floor := -80.0
	if p.AttenuationDB > 0 {
		rs := p.AttenuationDB
		if p.Phase == poststack.FilterZeroPhase {
			rs *= 2
		}
		floor = math.Min(floor, -math.Ceil(rs/20)*20)
	}
	xAt := func(hz float64) int {
		return int(rect.Left) + left + int(math.Round(hz/nyquist*float64(right-left)))
	}
	yAt := func(db float64) int {
		return int(rect.Top) + top + int(math.Round(math.Min(1, math.Max(0, db/floor))*float64(bottom-top)))
	}
	pen := func(color uintptr, style, lineWidth int, draw func()) {
		hp, _, _ := pCreatePen.Call(uintptr(style), uintptr(lineWidth), color)
		if hp == 0 {
			return
		}
		old, _, _ := pSelectObject.Call(dc, hp)
		draw()
		pSelectObject.Call(dc, old)
		pDeleteObject.Call(hp)
	}
	pen(rgbRef(220, 225, 233), 0, 1, func() {
		for i := range 5 {
			level := floor * float64(i) / 4
			lineY := yAt(level)
			drawLine(dc, xAt(0), lineY, xAt(nyquist), lineY)
			label := "0"
			if i != 0 {
				label = fmt.Sprintf("%.5g", level)
			}
			text(label, 0, lineY-int(rect.Top)-toPx(9), left-toPx(6), lineY-int(rect.Top)+toPx(9), DT_RIGHT)
		}
	})
	pen(rgbRef(145, 82, 30), 1, 1, func() {
		bounds := []float64{p.LowHz}
		if p.HighHz > 0 {
			bounds = append(bounds, p.HighHz)
		}
		for _, hz := range bounds {
			drawLine(dc, xAt(hz), yAt(0), xAt(hz), yAt(floor))
		}
	})
	pen(rgbRef(32, 70, 151), 0, max(1, toPx(2)), func() {
		for i := 1; i < len(model.db); i++ {
			drawLine(dc, xAt(model.hz[i-1]), yAt(model.db[i-1]), xAt(model.hz[i]), yAt(model.db[i]))
		}
	})
	for i := range 5 {
		value := nyquist * float64(i) / 4
		x := xAt(value) - int(rect.Left)
		text(fmt.Sprintf("%.5g", value), x-toPx(37), bottom+toPx(3), x+toPx(37), bottom+toPx(22), DT_CENTER)
	}
	text("dB", margin, top-toPx(16), left, top, 0)
	text("Hz", right-toPx(16), bottom+toPx(22), width-margin, bottom+toPx(41), DT_RIGHT)
}
