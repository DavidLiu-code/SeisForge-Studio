//go:build windows

package main

import (
	"fmt"
	"math"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

const (
	traceScientificWaveKey = "trace-analysis/waveform"
	traceScientificSpecKey = "trace-analysis/spectrum"
)

type traceScientificAxes struct {
	x, y displaycore.Axis
}

func (a traceScientificAxes) valid() bool { return a.x.ID != "" && a.y.ID != "" }

func traceScientificNativeAxes(d traceScientificData, opt traceScientificPlotOptions) [2]traceScientificAxes {
	unit := "s"
	if opt.TimeInMS {
		unit = "ms"
	}
	amp := makeChartAxis("amplitude", "振幅", "", d.amplitude.Min, d.amplitude.Max, false)
	time := makeChartAxis("time", "时间", "s", d.time.Min/1e6, d.time.Max/1e6, true)
	time.Unit = unit // all native time values, including ranges and steps, remain seconds
	freq := makeChartAxis("frequency", "频率", "Hz", d.frequency.Min, d.frequency.Max, false)
	level := makeChartAxis("amplitude", "幅度", "dB", d.level.Min, d.level.Max, false)
	if opt.LinearSpectrum {
		level = makeChartAxis("amplitude", "FFT 线性幅度", "", d.linear.Min, d.linear.Max, false)
	}
	return [2]traceScientificAxes{{amp, time}, {freq, level}}
}

func traceScientificAxisIdentity(i int, linear bool) string {
	id := traceChartIdentity()
	if i == 1 {
		if linear {
			return id + "fft:linear"
		}
		return id + "fft:dB"
	}
	return id
}

// Called once at the end of the normal options builder, so the live view,
// clipboard and export share both native choices and the editor preferences.
// It reads metadata and already-completed analysis only.
func traceScientificDecorateOptions(owner uintptr, r *traceAnalysisResult, o *traceScientificPlotOptions) {
	if o == nil || r == nil {
		return
	}
	proposed := *o
	proposed.nativeAxes, proposed.axes = [2]traceScientificAxes{}, [2]traceScientificAxes{}
	d := traceScientificPlotData(r, proposed)
	native := traceScientificNativeAxes(d, proposed)
	base := proposed
	base.TimeSet, base.AmpSet, base.FrequencySet, base.DBSet, base.LinearSet, base.FullWindow = false, false, false, false, false, true
	extents := traceScientificNativeAxes(traceScientificPlotData(r, base), base)
	for i, ax := range native {
		if extents[i].x.Data.Valid() {
			ax.x.Data = extents[i].x.Data
		}
		if extents[i].y.Data.Valid() {
			ax.y.Data = extents[i].y.Data
		}
		native[i] = ax
	}
	axes := native
	for i, key := range [...]string{traceScientificWaveKey, traceScientificSpecKey} {
		// A different source or a dB/linear switch may still have the last
		// painted surface. Never use its numeric axis for this frame.
		if state := displayOwners[owner]; state != nil {
			if live := state.surfaces[key]; live != nil && live.s.Identity == traceScientificAxisIdentity(i, proposed.LinearSpectrum) {
				axes[i].x = displayAxisFor(owner, key, native[i].x)
				axes[i].y = displayAxisFor(owner, key, native[i].y)
			}
		}
		// Explicit range edits are already folded into the native options by the
		// Apply callback. Do not let stale persisted axis ranges override a box
		// zoom or survive a reset; keep the axis style (units, reverse, ticks).
		axes[i].x.View, axes[i].y.View = native[i].x.View, native[i].y.View
	}
	// The compact toolbar and the shared axis editor have one unit choice.
	// An inherited chart style may be older than that choice (or from a
	// different analysis), so never let it silently relabel the time axis.
	axes[0].y.Unit = native[0].y.Unit
	o.nativeAxes, o.axes = native, axes
}

// An explicit unit choice in the compact native wave controls has the same
// effect as changing coordinates in the common editor.
func traceScientificUseLocalTimeUnit(owner uintptr, inMS bool) {
	state := displayOwners[owner]
	if state == nil {
		return
	}
	live := state.surfaces[traceScientificWaveKey]
	if live == nil || live.saved == nil {
		return
	}
	unit := "s"
	if inMS {
		unit = "ms"
	}
	for i := range live.saved.Axes {
		if live.saved.Axes[i].ID == "time" {
			live.saved.Axes[i].Unit = unit
		}
	}
}

type traceScientificAxisHit struct {
	key            string
	native         traceScientificAxes
	plot           traceScientificBounds
	xLabel, yLabel traceScientificBounds
	xPlan, yPlan   displaycore.TickPlan
}

func traceScientificAxisRECT(r traceScientificBounds, x, y int) RECT {
	return RECT{Left: int32(math.Floor(r.l)) + int32(x), Top: int32(math.Floor(r.t)) + int32(y),
		Right: int32(math.Ceil(r.r)) + int32(x), Bottom: int32(math.Ceil(r.b)) + int32(y)}
}

func (s traceScientificScene) registerAxes(owner uintptr, x, y int) {
	if owner == 0 || traceAnalysisCurrent == nil {
		return
	}
	done := map[string]bool{}
	for _, hit := range s.axisHits {
		if done[hit.key] {
			continue // all waveform panes edit the same shared axis pair
		}
		done[hit.key] = true
		idx, title := 0, "单道波形"
		if hit.key == traceScientificSpecKey {
			idx, title = 1, "单道频谱"
		}
		linear := s.linearSpectrum
		identity := traceScientificAxisIdentity(idx, linear)
		a := hit
		displayRegister(owner, a.key, displaySurface{
			Title: title, Identity: identity, Axes: []displaycore.Axis{a.native.x, a.native.y}, LiveViewport: true,
			Plot:      traceScientificAxisRECT(a.plot, x, y),
			Regions:   map[string]RECT{a.native.x.ID: traceScientificAxisRECT(a.xLabel, x, y), a.native.y.ID: traceScientificAxisRECT(a.yLabel, x, y)},
			AxisTicks: map[string]displaycore.TickPlan{a.native.x.ID: a.xPlan, a.native.y.ID: a.yPlan},
			Apply: func(st displaycore.Settings) error {
				state := displayOwners[owner]
				if state == nil || state.surfaces[a.key] == nil || state.surfaces[a.key].order == 0 ||
					traceScientificAxisIdentity(idx, traceScientificStyle.options.LinearSpectrum) != identity || !chartDisplayIsLive(a.key) {
					return fmt.Errorf("数据或频谱模式已更新，请重新打开坐标设置")
				}
				return traceScientificApplyAxisViews(a.key, linear, st)
			},
		})
	}
}

// A separate hit list is necessary for the disjoint left gutters of the
// side-by-side waveform panes. A bounding union would cover curve interiors.
func (s traceScientificScene) openAxesAt(owner uintptr, x, y, originX, originY int) bool {
	key, id := s.scientificAxisAt(x-originX, y-originY)
	if state := displayOwners[owner]; id != "" && state != nil && state.surfaces[key] != nil && state.surfaces[key].order != 0 {
		displayOpenEditor(owner, key, id, false)
		return true
	}
	return false
}

func (s traceScientificScene) scientificAxisAt(x, y int) (string, string) {
	p := traceScientificPoint{float64(x), float64(y)}
	for _, hit := range s.axisHits {
		// Curves retain double-click ownership, even along the first/last
		// coordinate, while the separate exterior bands open settings.
		if p.x > hit.plot.l && p.x < hit.plot.r && p.y > hit.plot.t && p.y < hit.plot.b {
			return "", ""
		}
	}
	for _, hit := range s.axisHits {
		if traceScientificInside(p, hit.xLabel) {
			return hit.key, hit.native.x.ID
		} else if traceScientificInside(p, hit.yLabel) {
			return hit.key, hit.native.y.ID
		}
	}
	return "", ""
}

func traceScientificApplyAxisViews(key string, linear bool, settings displaycore.Settings) error {
	d := &traceScientificStyle
	opts := d.options
	manual, mode, hasManual, symmetric := d.manual, d.mode, d.hasManual, d.symmetric
	changedAmp, changedSpec := false, false
	for _, a := range settings.Axes {
		if key == traceScientificWaveKey && a.ID == "time" && (a.Unit == "s" || a.Unit == "ms") {
			opts.TimeInMS = a.Unit == "ms"
		}
		if !a.ViewEdited {
			continue
		}
		lo, hi := a.View.Min, a.View.Max
		switch {
		case key == traceScientificWaveKey && a.ID == "time":
			lo, hi = lo*1e6, hi*1e6 // canonical seconds -> native µs
			if !traceScientificFinite(hi-lo) || hi <= lo {
				return fmt.Errorf("时间范围超出可显示范围")
			}
			opts.TimeSet, opts.TimeMinUS, opts.TimeMaxUS = true, lo, hi
			d.zoom.waveY = displaycore.Range{}
		case key == traceScientificWaveKey && a.ID == "amplitude":
			manual, mode, hasManual = segyanalysis.DisplayRange{Min: lo, Max: hi}, segyanalysis.ScaleManual, true
			symmetric, changedAmp = lo == -hi, true
			d.zoom.waveX = displaycore.Range{}
		case key == traceScientificSpecKey && a.ID == "frequency":
			if lo < 0 {
				return fmt.Errorf("频率范围不能小于 0")
			}
			opts.FrequencySet, opts.FrequencyMinHz, opts.FrequencyMaxHz, changedSpec = true, lo, hi, true
			d.zoom.specX = displaycore.Range{}
		case key == traceScientificSpecKey && a.ID == "amplitude":
			if linear {
				opts.LinearSet, opts.LinearMin, opts.LinearMax = true, lo, hi
				d.zoom.specLinear = displaycore.Range{}
			} else {
				opts.DBSet, opts.DBMin, opts.DBMax = true, lo, hi
				d.zoom.specDB = displaycore.Range{}
			}
			changedSpec = true
		}
	}
	timeUnitChanged := opts.TimeInMS != d.options.TimeInMS
	d.options, d.manual, d.mode, d.hasManual, d.symmetric = opts, manual, mode, hasManual, symmetric
	if timeUnitChanged {
		if ctl := traceAnalysisUI.scientific.timeUnits; ctl != 0 {
			pSendMessageW.Call(ctl, CB_SETCURSEL, uintptr(boolToInt(opts.TimeInMS)), 0)
		}
		persistTraceScientificStylePreference()
	}
	if changedAmp && traceAnalysisUI.scale != 0 {
		pSendMessageW.Call(traceAnalysisUI.scale, CB_SETCURSEL, uintptr(mode), 0)
		pSendMessageW.Call(traceAnalysisUI.scientific.symmetric, BM_SETCHECK, uintptr(boolToInt(symmetric)), 0)
		traceScientificFillAmpFields()
	}
	if changedSpec && traceAnalysisUI.scientific.specMode != 0 {
		traceScientificFillSpectrumFields()
	}
	if traceAnalysisHwnd != 0 {
		pInvalidateRect.Call(traceAnalysisHwnd, 0, 0)
	}
	return nil
}
