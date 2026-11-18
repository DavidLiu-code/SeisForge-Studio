//go:build windows

package main

// The existing spectrum window owns the filter editor. No second top-level
// filter window, shared Reader, or filtering of display/palette bytes is used.
import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

const (
	spectrumTargetA = 6730 + iota
	spectrumTargetB
	spectrumTargetBoth
	spectrumLowID
	spectrumHighID
	spectrumApplyID
	spectrumRestoreID
	spectrumBaseID
	spectrumCurrentID
	spectrumRatioID
	spectrumDesignResponseID
	spectrumDesignMethodID
	spectrumDesignOrderID
	spectrumDesignPhaseID
	spectrumDesignRippleID
	spectrumDesignAttenuationID
	spectrumResponsePreviewID
	spectrumReadyMessage = WM_USER + 245
)

type spectrumControls struct {
	object, a, b, both, scope, lowLabel, low, highLabel, high            uintptr
	method, stateA, stateB, status, apply, restore, close, base, current uintptr
	ratio                                                                uintptr
	designLabels, designFields                                           [6]uintptr
	responsePreview                                                      uintptr
}

type spectrumLayoutRects struct {
	Plot, Legend, Object, A, B, Both, Scope, LowLabel, Low, HighLabel, High RECT
	Method, StateA, StateB, Status, Apply, Restore, Close, Base, Current    RECT
	Ratio                                                                   RECT
	DesignLabels, DesignFields                                              [6]RECT
	ResponsePreview                                                         RECT
}

var spectrumUI spectrumControls
var spectrumFont uintptr
var spectrumFontDPI int
var spectrumDraft struct {
	low, high, source, error                     string
	target                                       viewFilterTarget
	initialized, updating, showBase, showCurrent bool
	showRatio                                    bool
	order, ripple, attenuation                   string
	response                                     poststack.FilterResponse
	method                                       poststack.FilterMethod
	phase                                        poststack.FilterPhase
}

const (
	spectrumDesignResponse = iota
	spectrumDesignMethod
	spectrumDesignOrder
	spectrumDesignPhase
	spectrumDesignRipple
	spectrumDesignAttenuation
)

var spectrumResponses = [...]poststack.FilterResponse{poststack.FilterLowpass, poststack.FilterHighpass, poststack.FilterBandpass, poststack.FilterBandstop, poststack.FilterLegacyBandpass}
var spectrumMethods = [...]poststack.FilterMethod{poststack.FilterButterworth, poststack.FilterChebyshevI, poststack.FilterChebyshevII, poststack.FilterElliptic}
var spectrumPhases = [...]poststack.FilterPhase{poststack.FilterForward, poststack.FilterZeroPhase}

func spectrumEnumIndex[T comparable](options []T, current T) int {
	for i, v := range options {
		if current == v {
			return i
		}
	}
	return 0
}

type spectrumSettingKey struct {
	Enabled bool
	Params  poststack.ViewBandpass
}

func spectrumSettings() (out [2]spectrumSettingKey) {
	for i, p := range viewFilterSideSettings() {
		if p != nil {
			out[i] = spectrumSettingKey{true, *p}
		}
	}
	return
}

type spectrumSource struct {
	row      viewFilterRows
	view     *viewFilterRows // immutable committed numerical view, never palette data
	identity os.FileInfo
	keys     [][2]int32 // immutable actual IL/XL; never pair physical trace numbers
}

type spectrumRequest struct {
	ctx      context.Context
	key      viewFilterKey
	settings [2]spectrumSettingKey
	sources  [2]spectrumSource
	label    string
	bError   string
}

type spectrumResult struct {
	id      uint64
	request spectrumRequest
	data    spectrumViewData
	err     error
}

var spectrumTask struct {
	id     uint64
	cancel context.CancelFunc
}
var spectrumMailbox struct {
	sync.Mutex
	id     uint64
	result *spectrumResult
}

func spectrumSideStatus(side string, s spectrumSettingKey) string {
	if s.Enabled {
		return side + " 当前视图：" + s.Params.Summary()
	}
	return side + " 当前视图：基准数据（未启用视图滤波）"
}

func capturePoststackSpectrumRequest() (spectrumRequest, error) {
	r := spectrumRequest{ctx: context.Background(), key: viewFilterCurrentKey(), settings: spectrumSettings()}
	d := compareTD
	if d == nil || d.fa == nil || d.ga == nil || poststackCompareInputPreparing() {
		return r, fmt.Errorf("等待当前剖面和几何加载完成")
	}
	frame := committedViewFilter()
	if compareMode == 2 {
		frame = nil
	}
	if compareMode != 2 && frame == nil && (r.settings[0].Enabled || r.settings[1].Enabled || viewFilterIsPending()) {
		return r, fmt.Errorf("等待当前滤波视图更新完成；频谱不会混用旧视图")
	}
	for i, f := range []*segy.File{d.fa, d.fb} {
		if f == nil || (i == 1 && r.key.b == "") {
			continue
		}
		if i == 1 && f.Info.Path != r.key.b {
			r.bError = "B 数据源正在切换，等待新 B 就绪"
			continue
		}
		g := d.ga
		if i == 1 {
			g = d.gb
		}
		if g == nil {
			if i == 1 {
				r.bError = "B 几何尚未就绪"
			}
			continue
		}
		row := viewFilterRows{path: f.Info.Path, ns: f.Info.SamplesPerTrace, dt: f.Info.SampleIntervalUS}
		generated := false
		if i == 1 {
			row.offset, generated = generatedPoststackSourceStart(row.path)
		}
		if frame != nil && len(frame.rows[i].traces) > 0 {
			row = frame.rows[i]
			r.sources[i].view = &frame.rows[i]
		} else {
			if compareMode == 2 {
				// Keep the pre-existing area-spectrum operation for Time Slice.
				// Filter controls stay disabled: a slice is not a time section.
				row.traces, _ = tracesInBounds(g, d.bounds)
			} else {
				traces, coords, _, err := g.LineTraceNumbers(compareMode, compareLineCoord)
				if err != nil {
					if i == 0 {
						return r, err
					}
					r.bError = err.Error()
					continue
				}
				lo, hi, step := compareTraceRangeForGeometry(f)
				traces, coords = filterPhysicalTraceRange(traces, coords, lo, hi, step)
				row.traces, row.coords = filterLineRange(traces, coords, compareLineXMin, compareLineXMax)
			}
			if len(row.traces) > 0 {
				var err error
				row.originUS, err = f.TraceTimeOriginUS(row.traces[0])
				if err != nil {
					if i == 0 {
						return r, err
					}
					r.bError = err.Error()
					continue
				}
			}
			if i == 1 && !generated && row.dt > 0 && r.sources[0].row.dt > 0 {
				a := r.sources[0].row
				row.physicalTimeWindow = true
				row.requestFirstUS, row.requestLastUS = viewFilterSampleTime(a, a.first), viewFilterSampleTime(a, a.last)
			}
			row.first, row.last = viewFilterRowWindow(r.key, row)
		}
		row.traces = append([]int64(nil), row.traces...)
		row.coords = append([]int32(nil), row.coords...)
		if len(row.traces) == 0 || row.last < row.first {
			if i == 0 {
				return r, fmt.Errorf("当前范围没有可分析的 A 道")
			}
			r.bError = "当前范围没有可分析的 B 道"
			continue
		}
		identity, err := f.SourceFileInfo()
		if err != nil {
			if i == 0 {
				return r, err
			}
			r.bError = err.Error()
			continue
		}
		r.sources[i].row, r.sources[i].identity = row, identity
		r.sources[i].keys = spectrumGeometryKeys(g, row.traces)
	}
	r.label = "相同道、时间窗、Hann 窗与 FFT；基准/当前共用幅度参考，不分别归一化"
	return r, nil
}

func spectrumGeometryKeys(g *segy.GeometryIndex, traces []int64) [][2]int32 {
	if g == nil {
		return nil
	}
	positions := make(map[int64]int, len(traces))
	for i, tr := range traces {
		positions[tr] = i
	}
	keys, found := make([][2]int32, len(traces)), make([]bool, len(traces))
	for i, tr := range g.TraceNumbers {
		j, ok := positions[tr]
		if !ok || i >= len(g.RowOfTrace) || i >= len(g.ColOfTrace) {
			continue
		}
		ri, ci := g.RowOfTrace[i], g.ColOfTrace[i]
		if ri < 0 || ci < 0 || int(ri) >= len(g.InlineValues) || int(ci) >= len(g.CrosslineValues) {
			continue
		}
		keys[j], found[j] = [2]int32{g.InlineValues[ri], g.CrosslineValues[ci]}, true
	}
	for _, ok := range found {
		if !ok {
			return nil
		}
	}
	return keys
}

// The ratio is not a subtraction of the independently averaged display curves.
// It is calculated from the same unambiguous geometry pairs, selected physical
// time interval, Hann window and FFT. Incompatible sides remain independently
// browsable without claiming a meaningful A/B ratio.
func spectrumRatioPairs(sources [2]spectrumSource) ([][2]int64, error) {
	a, b := sources[0].row, sources[1].row
	if len(a.traces) == 0 || len(b.traces) == 0 {
		return nil, fmt.Errorf("B 尚未就绪")
	}
	if a.dt <= 0 || a.dt != b.dt {
		return nil, fmt.Errorf("A/B 采样间隔不一致")
	}
	if viewFilterSampleTime(a, a.first) != viewFilterSampleTime(b, b.first) || viewFilterSampleTime(a, a.last) != viewFilterSampleTime(b, b.last) {
		return nil, fmt.Errorf("A/B 当前实际时间窗不一致")
	}
	var maps [2]map[[2]int32]int64
	for side, source := range sources {
		if len(source.keys) != len(source.row.traces) {
			return nil, fmt.Errorf("A/B 缺少可比较的实际几何")
		}
		maps[side] = make(map[[2]int32]int64, len(source.keys))
		for i, key := range source.keys {
			if _, duplicate := maps[side][key]; duplicate {
				return nil, fmt.Errorf("A/B 几何键重复，无法唯一配对")
			}
			maps[side][key] = source.row.traces[i]
		}
	}
	pairs := make([][2]int64, 0, len(a.traces))
	for i, key := range sources[0].keys {
		if tr, ok := maps[1][key]; ok {
			pairs = append(pairs, [2]int64{a.traces[i], tr})
		}
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("当前 A/B 范围没有匹配的几何道")
	}
	return pairs, nil
}

func spectrumRatioLinear(ctx context.Context, sources [2]spectrumSource, reads [2]func(int64, int, int) ([]float64, error)) (spectrumCurve, error) {
	pairs, err := spectrumRatioPairs(sources)
	if err != nil {
		return spectrumCurve{}, err
	}
	row := sources[0].row
	row.traces = make([]int64, len(pairs))
	for i := range row.traces {
		row.traces[i] = int64(i)
	}
	pairedRead := func(side int) func(int64, int, int) ([]float64, error) {
		return func(pair int64, first, last int) ([]float64, error) {
			offset := sources[side].row.first - row.first
			return reads[side](pairs[pair][side], first+offset, last+offset)
		}
	}
	curves, linear, err := spectrumLinearPair(ctx, row, pairedRead(0), pairedRead(1))
	if err != nil {
		return spectrumCurve{}, err
	}
	out := curves[0]
	for i, a := range linear[0] {
		b := linear[1][i]
		out.DB[i] = math.NaN() // zero amplitude has no finite logarithmic ratio
		if a > 0 && b > 0 {
			out.DB[i] = 20 * (math.Log10(a) - math.Log10(b))
		}
	}
	return out, nil
}

func computeSpectrumRatio(r spectrumRequest) (spectrumCurve, error) {
	if _, err := spectrumRatioPairs(r.sources); err != nil {
		return spectrumCurve{}, err
	}
	var reads [2]func(int64, int, int) ([]float64, error)
	for side, source := range r.sources {
		f, err := openPoststackCompareSource(source.row.path)
		if err != nil {
			return spectrumCurve{}, err
		}
		defer f.Close()
		if _, err = viewFilterSourceIdentity(f, source.row.path, source.identity); err != nil {
			return spectrumCurve{}, err
		}
		read := f.ReadTraceWindow
		if source.view != nil {
			read = source.view.read
		}
		reads[side] = func(tr int64, first, last int) ([]float64, error) {
			if err := r.ctx.Err(); err != nil {
				return nil, err
			}
			header, err := f.ReadTraceHeader(tr)
			if err != nil {
				return nil, err
			}
			origin, err := f.TraceTimeOriginUS(tr)
			if err != nil {
				return nil, err
			}
			row := source.row
			if origin != row.originUS || (header.SampleCount != 0 && header.SampleCount != row.ns) || (header.SampleIntervalUS != 0 && header.SampleIntervalUS != row.dt) {
				return nil, fmt.Errorf("%c 匹配道 %d 的采样轴不一致", 'A'+side, tr+1)
			}
			values, err := read(tr, first, last)
			if err == nil {
				_, err = viewFilterSourceIdentity(f, row.path, source.identity)
			}
			return values, err
		}
	}
	return spectrumRatioLinear(r.ctx, r.sources, reads)
}

// Both curves are read from exactly the same selected traces/window. Failures
// reject the pair rather than silently averaging different subsets.
func spectrumLinearPair(ctx context.Context, row viewFilterRows, base, current func(int64, int, int) ([]float64, error)) ([2]spectrumCurve, [2][]float64, error) {
	var curves [2]spectrumCurve
	var linear [2][]float64
	if row.dt <= 0 || len(row.traces) == 0 {
		return curves, linear, fmt.Errorf("没有可分析的道或采样间隔")
	}
	if row.last-row.first+1 < 32 {
		return curves, linear, fmt.Errorf("频谱分析至少需要 32 个可见样点；仍可调整频率并应用视图滤波")
	}
	nfft := nextPow2LE(row.last - row.first + 1)
	traces := evenlySampleTraces(row.traces, 96)
	for side := range curves {
		linear[side] = make([]float64, nfft/2+1)
		curves[side] = spectrumCurve{Freq: make([]float64, nfft/2+1), DB: make([]float64, nfft/2+1), TraceCount: len(traces), NFFT: nfft}
	}
	for traceIndex, tr := range traces {
		if err := ctx.Err(); err != nil {
			return curves, linear, err
		}
		for side, read := range []func(int64, int, int) ([]float64, error){base, current} {
			x, err := read(tr, row.first, row.first+nfft-1)
			if err != nil {
				return curves, linear, err
			}
			a, err := segyanalysis.ComputeAmplitudeSpectrum(x, row.dt, segyanalysis.LegacyQCAmplitudeOptions())
			if err != nil {
				return curves, linear, err
			}
			if a.NFFT != nfft || len(a.LinearAmplitude) != len(linear[side]) {
				return curves, linear, fmt.Errorf("频谱样点数不一致")
			}
			for k, v := range a.LinearAmplitude {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					return curves, linear, fmt.Errorf("频谱幅值不是有限值")
				}
				// A running mean stays within the finite magnitude bounds;
				// summing first can overflow even when the mean is finite.
				linear[side][k] += (v - linear[side][k]) / float64(traceIndex+1)
			}
		}
	}
	// Coherent one-sided amplitude for a symmetric Hann window. Unlike the
	// legacy per-curve maximum, this preserves the measured attenuation.
	scale := 4 / float64(nfft-1)
	for side := range curves {
		peak := 1
		for k := range linear[side] {
			linear[side][k] *= scale
			if k == 0 || k == nfft/2 {
				linear[side][k] *= 0.5
			}
			curves[side].Freq[k] = float64(k) / (float64(nfft) * float64(row.dt) * 1e-6)
			if k > 0 && linear[side][k] > linear[side][peak] {
				peak = k
			}
		}
		curves[side].PeakHz = curves[side].Freq[peak]
	}
	return curves, linear, nil
}

func calibrateSpectrum(curves []*spectrumCurve, linear [][]float64, reference float64) {
	if reference <= 0 || math.IsNaN(reference) || math.IsInf(reference, 0) {
		reference = 1
	}
	for i, c := range curves {
		for k, value := range linear[i] {
			db := -100.0
			if value > 0 {
				db = math.Max(-100, 20*(math.Log10(value)-math.Log10(reference)))
			}
			c.DB[k] = db
		}
	}
}

func computePoststackSpectrum(r spectrumRequest) (spectrumViewData, error) {
	d := spectrumViewData{Label: r.label, StateA: spectrumSideStatus("A", r.settings[0]), StateB: spectrumSideStatus("B", r.settings[1]), BError: r.bError}
	var curves [2][2]spectrumCurve
	var linear [2][2][]float64
	for i, source := range r.sources {
		row := source.row
		if len(row.traces) == 0 {
			continue
		}
		if err := r.ctx.Err(); err != nil {
			return d, err
		}
		err := func() error {
			f, err := openPoststackCompareSource(row.path)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err = viewFilterSourceIdentity(f, row.path, source.identity); err != nil {
				return err
			}
			read := f.ReadTraceWindow
			if source.view != nil {
				read = source.view.read
			}
			curves[i], linear[i], err = spectrumLinearPair(r.ctx, row, f.ReadTraceWindow, read)
			if err != nil {
				return err
			}
			_, err = viewFilterSourceIdentity(f, row.path, source.identity)
			return err
		}()
		if err != nil {
			if i == 0 || r.ctx.Err() != nil {
				return d, fmt.Errorf("%s 频谱：%w", []string{"A", "B"}[i], err)
			}
			d.BError = "B 频谱不可用：" + err.Error()
			curves[i], linear[i] = [2]spectrumCurve{}, [2][]float64{}
			continue
		}
		for k, v := range linear[i][0] {
			if k > 0 && v > d.Reference {
				d.Reference = v
			}
		}
		if i == 1 {
			d.HasB = true
		}
	}
	for i := range curves {
		calibrateSpectrum([]*spectrumCurve{&curves[i][0], &curves[i][1]}, [][]float64{linear[i][0], linear[i][1]}, d.Reference)
	}
	d.BaseA, d.A, d.BaseB, d.B = curves[0][0], curves[0][1], curves[1][0], curves[1][1]
	if d.HasB {
		ratio, err := computeSpectrumRatio(r)
		if err != nil {
			d.RatioReason = err.Error()
		} else {
			d.DiffFreq, d.DiffDB = ratio.Freq, ratio.DB
			d.RatioTraceCount, d.RatioNFFT = ratio.TraceCount, ratio.NFFT
		}
	} else {
		d.RatioReason = "B 尚未就绪"
	}
	if d.BError != "" {
		d.StateB = d.BError
	}
	return d, r.ctx.Err()
}

func cancelPoststackSpectrumAnalysis() {
	if spectrumTask.cancel != nil {
		spectrumTask.cancel()
		spectrumTask.cancel = nil
	}
	spectrumTask.id++
	spectrumMailbox.Lock()
	spectrumMailbox.id, spectrumMailbox.result = spectrumTask.id, nil
	spectrumMailbox.Unlock()
}

func startPoststackSpectrumAnalysis() {
	if spectrumHwnd == 0 {
		return
	}
	cancelPoststackSpectrumAnalysis()
	syncPoststackSpectrumControls()
	r, err := capturePoststackSpectrumRequest()
	if err != nil {
		spectrumData = spectrumViewData{Label: err.Error()}
		pInvalidateRect.Call(spectrumHwnd, 0, 1)
		return
	}
	r.ctx, spectrumTask.cancel = context.WithCancel(context.Background())
	id, h := spectrumTask.id, spectrumHwnd
	spectrumData = spectrumViewData{Label: "正在分析基准与当前视图频谱…"}
	pInvalidateRect.Call(h, 0, 1)
	go func() {
		d, err := computePoststackSpectrum(r)
		spectrumMailbox.Lock()
		publish := r.ctx.Err() == nil && spectrumMailbox.id == id
		if publish {
			spectrumMailbox.result = &spectrumResult{id: id, request: r, data: d, err: err}
		}
		spectrumMailbox.Unlock()
		if publish {
			pPostMessageW.Call(h, spectrumReadyMessage, 0, 0)
		}
	}()
}

func acceptPoststackSpectrumAnalysis() {
	spectrumMailbox.Lock()
	result := spectrumMailbox.result
	spectrumMailbox.result = nil
	spectrumMailbox.Unlock()
	if !spectrumResultCurrent(result, spectrumTask.id, viewFilterCurrentKey(), spectrumSettings()) {
		return
	}
	spectrumTask.cancel = nil
	if result.err != nil {
		spectrumData = spectrumViewData{Label: result.err.Error()}
	} else {
		spectrumData = result.data
	}
	syncPoststackSpectrumControls()
	pInvalidateRect.Call(spectrumHwnd, 0, 1)
}

func spectrumResultCurrent(result *spectrumResult, id uint64, key viewFilterKey, settings [2]spectrumSettingKey) bool {
	return result != nil && result.id == id && result.request.key == key && result.request.settings == settings
}

func makePoststackSpectrumControls(h uintptr) {
	label := func(text string) uintptr {
		return createCtrl(h, "STATIC", text, WS_CHILD|WS_VISIBLE|0x0080, 0, 0, 1, 1, 0)
	}
	button := func(text string, id int, style uint32) uintptr {
		if style&0xf == BS_PUSHBUTTON || style&BS_PUSHLIKE != 0 {
			return createPoststackNamedToolButton(h, id, 0, text, WS_VISIBLE|style, 0, 0, 1, 1)
		}
		return createCtrl(h, "BUTTON", text, WS_CHILD|WS_VISIBLE|WS_TABSTOP|poststackToolbarClipSiblings|style, 0, 0, 1, 1, id)
	}
	edit := func(id int) uintptr {
		return createCtrl(h, "EDIT", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|WS_BORDER|ES_AUTOHSCROLL, 0, 0, 1, 1, id)
	}
	combo := func(id int, values []string) uintptr {
		ctl := createCtrl(h, "COMBOBOX", "", WS_CHILD|WS_VISIBLE|WS_TABSTOP|CBS_DROPDOWNLIST|WS_VSCROLL, 0, 0, 1, 170, id)
		for _, value := range values {
			pSendMessageW.Call(ctl, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(value))))
		}
		return ctl
	}
	spectrumUI = spectrumControls{object: label("作用对象："), a: button("A", spectrumTargetA, BS_AUTORADIOBUTTON|WS_GROUP), b: button("B", spectrumTargetB, BS_AUTORADIOBUTTON), both: button("A+B", spectrumTargetBoth, BS_AUTORADIOBUTTON), scope: label(""), lowLabel: label("边界 / 下界 (Hz)"), low: edit(spectrumLowID), highLabel: label("边界上限 (Hz)"), high: edit(spectrumHighID), method: label(""), stateA: label(""), stateB: label(""), status: label(""), apply: button("应用到视图", spectrumApplyID, BS_PUSHBUTTON), restore: button("恢复所选视图", spectrumRestoreID, BS_PUSHBUTTON), close: button("关闭", IDCSP_CLOSE, BS_PUSHBUTTON), base: button("基准频谱（虚线）", spectrumBaseID, BS_AUTOCHECKBOX), current: button("当前视图（实线）", spectrumCurrentID, BS_AUTOCHECKBOX)}
	for i, name := range [...]string{"响应类型", "设计方法", "设计阶数", "相位方式", "通带纹波 Rp (dB)", "阻带衰减 Rs (dB)"} {
		spectrumUI.designLabels[i] = label(name)
	}
	spectrumUI.designFields = [6]uintptr{
		combo(spectrumDesignResponseID, []string{"低通", "高通", "带通", "带阻", "兼容带通"}),
		combo(spectrumDesignMethodID, []string{"Butterworth", "Chebyshev I", "Chebyshev II", "Elliptic"}),
		edit(spectrumDesignOrderID),
		combo(spectrumDesignPhaseID, []string{"单向", "双向零相位"}),
		edit(spectrumDesignRippleID),
		edit(spectrumDesignAttenuationID),
	}
	spectrumUI.responsePreview = button("滤波响应预览", spectrumResponsePreviewID, BS_PUSHBUTTON)
	if spectrumDraft.target == 0 {
		spectrumDraft.target = viewFilterBoth
		spectrumDraft.showBase, spectrumDraft.showCurrent = true, true
	}
	spectrumUI.ratio = button("显示 A/B 幅度谱比", spectrumRatioID, BS_AUTOCHECKBOX)
}

func spectrumWindowDPI(h uintptr) int {
	if algorithmDPI.Find() == nil {
		if n, _, _ := algorithmDPI.Call(h); n >= 96 && n <= 480 {
			return int(n)
		}
	}
	return 96
}

func fitPoststackSpectrumWindow(h uintptr) {
	dpi, area := spectrumWindowDPI(h), algorithmMonitorArea(h)
	w, height := minInt(int(area.Right-area.Left), 940*dpi/96), minInt(int(area.Bottom-area.Top), 760*dpi/96)
	x, y := int(area.Left)+(int(area.Right-area.Left)-w)/2, int(area.Top)+(int(area.Bottom-area.Top)-height)/2
	pSetWindowPos.Call(h, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(height), SWP_NOZORDER|SWP_NOACTIVATE)
	layoutPoststackSpectrumControls(h)
}

func spectrumLayout(width, height, dpi int) spectrumLayoutRects {
	if dpi <= 0 {
		dpi = 96
	}
	w, h := width*96/dpi, height*96/dpi
	left, inner := 16, maxInt(1, w-32)
	footer := 282
	// A work-area-constrained high-DPI window can be shorter than the nominal
	// minimum. Give the controls priority and suppress the graph before allowing
	// the apply/restore footer to fall below the visible client rectangle.
	y := maxInt(8, h-footer)
	rc := func(x, y, w, h int) RECT {
		return RECT{int32(x * dpi / 96), int32(y * dpi / 96), int32((x + maxInt(1, w)) * dpi / 96), int32((y + maxInt(1, h)) * dpi / 96)}
	}
	l := spectrumLayoutRects{Legend: rc(58, 10, w-80, 44), Plot: rc(58, 66, w-84, y-112), Base: rc(left, y, 139, 22), Current: rc(left+145, y, 139, 22), Scope: rc(left, y+25, inner, 35), Object: rc(left, y+62, 76, 22), A: rc(left+78, y+62, 88, 22), B: rc(left+169, y+62, 48, 22), Both: rc(left+220, y+62, 59, 22)}
	l.Ratio = rc(left+291, y, inner-291, 22)
	l.Method = rc(left+287, y+62, inner-287, 22)
	if y < 130 {
		l.Plot, l.Legend = RECT{}, RECT{}
	}
	cell := maxInt(1, (inner-24)/4)
	field := func(col, row int) (RECT, RECT) {
		x, top := left+col*(cell+8), y+87+row*47
		return rc(x, top, cell, 17), rc(x, top+18, cell, 26)
	}
	for i := range 4 {
		l.DesignLabels[i], l.DesignFields[i] = field(i, 0)
	}
	l.LowLabel, l.Low = field(0, 1)
	l.HighLabel, l.High = field(1, 1)
	l.DesignLabels[spectrumDesignRipple], l.DesignFields[spectrumDesignRipple] = field(2, 1)
	l.DesignLabels[spectrumDesignAttenuation], l.DesignFields[spectrumDesignAttenuation] = field(3, 1)
	l.StateA, l.StateB, l.Status = rc(left, y+181, inner, 18), rc(left, y+201, inner, 18), rc(left, y+221, inner, 20)
	by := maxInt(y+244, h-36)
	l.Close, l.Restore, l.Apply = rc(w-left-60, by, 60, 29), rc(w-left-200, by, 132, 29), rc(w-left-340, by, 132, 29)
	l.ResponsePreview = rc(left, by, 123, 29)
	return l
}

func spectrumControlPositions(l spectrumLayoutRects) map[uintptr]RECT {
	c := spectrumUI
	out := map[uintptr]RECT{c.object: l.Object, c.a: l.A, c.b: l.B, c.both: l.Both, c.scope: l.Scope, c.lowLabel: l.LowLabel, c.low: l.Low, c.highLabel: l.HighLabel, c.high: l.High, c.method: l.Method, c.stateA: l.StateA, c.stateB: l.StateB, c.status: l.Status, c.apply: l.Apply, c.restore: l.Restore, c.close: l.Close, c.base: l.Base, c.current: l.Current, c.ratio: l.Ratio, c.responsePreview: l.ResponsePreview}
	for i, h := range c.designLabels {
		out[h], out[c.designFields[i]] = l.DesignLabels[i], l.DesignFields[i]
	}
	return out
}

func layoutPoststackSpectrumControls(h uintptr) {
	if h == 0 || spectrumUI.a == 0 {
		return
	}
	dpi := spectrumWindowDPI(h)
	w, height := clientSize(h)
	l := spectrumLayout(w, height, dpi)
	if spectrumFontDPI != dpi {
		old := spectrumFont
		spectrumFont, _, _ = pCreateFontW.Call(uintptr(-maxInt(1, 13*dpi/96)), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(u16("Microsoft YaHei UI"))))
		spectrumFontDPI = dpi
		font := spectrumFont
		if font == 0 {
			font = hFont
		}
		for ctl := range spectrumControlPositions(l) {
			pSendMessageW.Call(ctl, WM_SETFONT, font, 0)
		}
		if old != 0 {
			pDeleteObject.Call(old)
		}
	}
	for ctl, r := range spectrumControlPositions(l) {
		if ctl == spectrumUI.designFields[spectrumDesignResponse] || ctl == spectrumUI.designFields[spectrumDesignMethod] || ctl == spectrumUI.designFields[spectrumDesignPhase] {
			r.Bottom = r.Top + int32(166*dpi/96)
		}
		pSetWindowPos.Call(ctl, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), SWP_NOZORDER|SWP_NOACTIVATE|0x0008)
	}
	pRedrawWindow.Call(h, 0, 0, RDW_INVALIDATE|RDW_ERASE|RDW_ALLCHILDREN)
}

func setSpectrumText(h uintptr, text string) {
	if h != 0 && getText(h) != text {
		setText(h, text)
	}
}

func syncPoststackSpectrumControls() {
	if spectrumHwnd == 0 || spectrumUI.a == 0 {
		return
	}
	_, scope, status, dt := poststackViewFilterDialogInfo()
	hasB := compareWorkspaceActive && compareTD != nil && compareTD.fb != nil && compareActiveB()
	if !hasB && spectrumDraft.target != viewFilterA {
		spectrumDraft.target = viewFilterA
	}
	if hasB && spectrumDraft.target == viewFilterB {
		scope, dt = poststackViewFilterSideScope(1)
	}
	if hasB && spectrumDraft.target == viewFilterBoth {
		bScope, bDT := poststackViewFilterSideScope(1)
		scope = "A " + strings.ReplaceAll(scope, "\r\n", " · ") + "\r\nB " + strings.ReplaceAll(bScope, "\r\n", " · ")
		if bDT <= 0 {
			dt = 0
		}
	}
	if spectrumDraft.source != compareAPath {
		spectrumDraft.source = compareAPath
		spectrumDraft.error = ""
	}
	ensureSpectrumFilterDraft(dt)
	spectrumDraft.updating = true
	setSpectrumText(spectrumUI.low, spectrumDraft.low)
	setSpectrumText(spectrumUI.high, spectrumDraft.high)
	for _, e := range []struct {
		i     int
		value string
	}{{spectrumDesignOrder, spectrumDraft.order}, {spectrumDesignRipple, spectrumDraft.ripple}, {spectrumDesignAttenuation, spectrumDraft.attenuation}} {
		setSpectrumText(spectrumUI.designFields[e.i], e.value)
	}
	for _, e := range []struct{ i, index int }{
		{spectrumDesignResponse, spectrumEnumIndex(spectrumResponses[:], spectrumDraft.response)},
		{spectrumDesignMethod, spectrumEnumIndex(spectrumMethods[:], spectrumDraft.method)},
		{spectrumDesignPhase, spectrumEnumIndex(spectrumPhases[:], spectrumDraft.phase)},
	} {
		pSendMessageW.Call(spectrumUI.designFields[e.i], CB_SETCURSEL, uintptr(e.index), 0)
	}
	high := poststack.FilterParameterApplies(poststack.BandpassHighParameter, spectrumDraft.response, spectrumDraft.method)
	setSpectrumText(spectrumUI.lowLabel, "边界频率 (Hz)")
	if high {
		setSpectrumText(spectrumUI.lowLabel, "边界下限 (Hz)")
	}
	showControls([]uintptr{spectrumUI.highLabel, spectrumUI.high}, high)
	showControls([]uintptr{spectrumUI.designLabels[spectrumDesignRipple], spectrumUI.designFields[spectrumDesignRipple]}, poststack.FilterParameterApplies(poststack.FilterRippleParameter, spectrumDraft.response, spectrumDraft.method))
	showControls([]uintptr{spectrumUI.designLabels[spectrumDesignAttenuation], spectrumUI.designFields[spectrumDesignAttenuation]}, poststack.FilterParameterApplies(poststack.FilterAttenuationParameter, spectrumDraft.response, spectrumDraft.method))
	spectrumDraft.updating = false
	setSpectrumText(spectrumUI.a, "A")
	if !compareWorkspaceActive {
		setSpectrumText(spectrumUI.a, "当前剖面")
	}
	for ctl, selected := range map[uintptr]bool{spectrumUI.a: spectrumDraft.target == viewFilterA, spectrumUI.b: spectrumDraft.target == viewFilterB, spectrumUI.both: spectrumDraft.target == viewFilterBoth, spectrumUI.base: spectrumDraft.showBase, spectrumUI.current: spectrumDraft.showCurrent} {
		pSendMessageW.Call(ctl, BM_SETCHECK, uintptr(boolToInt(selected)), 0)
	}
	pEnableWindow.Call(spectrumUI.b, uintptr(boolToInt(hasB)))
	pEnableWindow.Call(spectrumUI.both, uintptr(boolToInt(hasB)))
	pEnableWindow.Call(spectrumUI.ratio, uintptr(boolToInt(hasB)))
	pSendMessageW.Call(spectrumUI.ratio, BM_SETCHECK, uintptr(boolToInt(spectrumDraft.showRatio)), 0)
	if spectrumDraft.target != viewFilterBoth {
		scope = strings.ReplaceAll(scope, "\r\n", " · ")
	}
	setSpectrumText(spectrumUI.scope, "范围："+scope)
	method := ""
	if dt > 0 {
		method = fmt.Sprintf("dt %.6g ms · Nyquist %.6g Hz", float64(dt)/1000, 500000/float64(dt))
	}
	setSpectrumText(spectrumUI.method, method)
	settings := spectrumSettings()
	setSpectrumText(spectrumUI.stateA, spectrumSideStatus("A", settings[0]))
	if hasB {
		setSpectrumText(spectrumUI.stateB, spectrumSideStatus("B", settings[1])+"；B 基准为当前 B，不是原始 A")
	} else {
		setSpectrumText(spectrumUI.stateB, "B：未加载；仅允许处理 A。新 B 默认不启用视图滤波。")
	}
	if hasB && spectrumData.BError != "" {
		setSpectrumText(spectrumUI.stateB, spectrumData.BError)
	}
	if compareMode == 2 {
		setSpectrumText(spectrumUI.stateA, "当前为时间切片基准频谱；逐道视图滤波仅在 Inline/Crossline 时间剖面生效。")
	}
	if spectrumDraft.error != "" {
		status = spectrumDraft.error
	} else if dt > 0 && status == "" {
		status = "参数草稿不自动应用；响应预览不修改 A/B。"
	}
	setSpectrumText(spectrumUI.status, status)
	pEnableWindow.Call(spectrumUI.apply, uintptr(boolToInt(dt > 0)))
	pEnableWindow.Call(spectrumUI.restore, uintptr(boolToInt(dt > 0)))
	pEnableWindow.Call(spectrumUI.responsePreview, uintptr(boolToInt(dt > 0)))
}

func ensureSpectrumFilterDraft(dt int) {
	if spectrumDraft.initialized {
		return
	}
	for _, pd := range poststack.BandpassDescriptor(dt).Parameters {
		switch pd.ID {
		case poststack.BandpassLowParameter:
			spectrumDraft.low = fmt.Sprint(pd.Default)
		case poststack.BandpassHighParameter:
			spectrumDraft.high = fmt.Sprint(pd.Default)
		case poststack.FilterResponseParameter:
			spectrumDraft.response = poststack.FilterResponse(fmt.Sprint(pd.Default))
		case poststack.FilterMethodParameter:
			spectrumDraft.method = poststack.FilterMethod(fmt.Sprint(pd.Default))
		case poststack.FilterOrderParameter:
			spectrumDraft.order = fmt.Sprint(pd.Default)
		case poststack.FilterPhaseParameter:
			spectrumDraft.phase = poststack.FilterPhase(fmt.Sprint(pd.Default))
		case poststack.FilterRippleParameter:
			spectrumDraft.ripple = fmt.Sprint(pd.Default)
		case poststack.FilterAttenuationParameter:
			spectrumDraft.attenuation = fmt.Sprint(pd.Default)
		}
	}
	spectrumDraft.initialized = dt > 0
}

func saveSpectrumDraft() {
	if !spectrumDraft.updating && spectrumUI.low != 0 {
		spectrumDraft.low, spectrumDraft.high = getText(spectrumUI.low), getText(spectrumUI.high)
		spectrumDraft.order = getText(spectrumUI.designFields[spectrumDesignOrder])
		spectrumDraft.ripple = getText(spectrumUI.designFields[spectrumDesignRipple])
		spectrumDraft.attenuation = getText(spectrumUI.designFields[spectrumDesignAttenuation])
	}
}

func spectrumDraftParameters() (poststack.ViewBandpass, int, error) {
	saveSpectrumDraft()
	_, _, _, dt := poststackViewFilterDialogInfo()
	if spectrumDraft.target == viewFilterB {
		_, dt = poststackViewFilterSideScope(1)
	}
	if spectrumDraft.target == viewFilterBoth {
		_, dtB := poststackViewFilterSideScope(1)
		if dtB <= 0 || dt <= 0 {
			return poststack.ViewBandpass{}, 0, fmt.Errorf("A 或 B 采样间隔尚不可用")
		}
		// Validate against the lower Nyquist (larger sampling interval).
		dt = max(dt, dtB)
	}
	data := map[string]any{
		poststack.FilterResponseParameter: string(spectrumDraft.response),
		poststack.FilterMethodParameter:   string(spectrumDraft.method),
		poststack.FilterPhaseParameter:    string(spectrumDraft.phase),
	}
	order, err := strconv.Atoi(strings.TrimSpace(spectrumDraft.order))
	if err != nil || order < 1 || order > poststack.FilterMaxOrder {
		return poststack.ViewBandpass{}, dt, fmt.Errorf("设计阶数必须是 1–%d 的整数", poststack.FilterMaxOrder)
	}
	data[poststack.FilterOrderParameter] = order
	for _, a := range []struct{ id, name, value string }{
		{poststack.BandpassLowParameter, "边界频率", spectrumDraft.low},
		{poststack.BandpassHighParameter, "边界上限", spectrumDraft.high},
		{poststack.FilterRippleParameter, "通带纹波 Rp", spectrumDraft.ripple},
		{poststack.FilterAttenuationParameter, "阻带衰减 Rs", spectrumDraft.attenuation},
	} {
		if poststack.FilterParameterApplies(a.id, spectrumDraft.response, spectrumDraft.method) {
			value, err := strconv.ParseFloat(strings.TrimSpace(a.value), 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return poststack.ViewBandpass{}, dt, fmt.Errorf("%s必须为有限数字", a.name)
			}
			data[a.id] = value
		}
	}
	p, err := poststack.BandpassParameters(data, dt)
	return p, dt, err
}

func applySpectrumFilter() {
	p, _, err := spectrumDraftParameters()
	if err == nil {
		err = applyPoststackViewFilterTarget(spectrumDraft.target, p)
	}
	if err != nil {
		spectrumDraft.error = "未应用：" + err.Error()
	} else {
		spectrumDraft.error = ""
		cancelPoststackSpectrumAnalysis()
	}
	syncPoststackSpectrumControls()
}

func refreshSpectrumFilterResponse() {
	if h := temporalFilterResponseOwners[spectrumHwnd]; h != 0 {
		p, dt, err := spectrumDraftParameters()
		updateTemporalFilterResponse(h, dt, p, err, "")
	}
}

func poststackSpectrumCommand(w uintptr) bool {
	switch int(w & 0xffff) {
	case spectrumTargetA:
		spectrumDraft.target = viewFilterA
	case spectrumTargetB:
		spectrumDraft.target = viewFilterB
	case spectrumTargetBoth:
		spectrumDraft.target = viewFilterBoth
	case spectrumLowID, spectrumHighID, spectrumDesignOrderID, spectrumDesignRippleID, spectrumDesignAttenuationID:
		if w>>16&0xffff == 0x0300 && !spectrumDraft.updating {
			spectrumDraft.initialized = true
			saveSpectrumDraft()
			refreshSpectrumFilterResponse()
		}
		return true
	case spectrumDesignResponseID, spectrumDesignMethodID, spectrumDesignPhaseID:
		if w>>16&0xffff != 1 || spectrumDraft.updating {
			return true
		}
		idx := int(w&0xffff) - spectrumDesignResponseID
		raw, _, _ := pSendMessageW.Call(spectrumUI.designFields[idx], CB_GETCURSEL, 0, 0)
		i := int(raw)
		switch idx {
		case spectrumDesignResponse:
			if i >= 0 && i < len(spectrumResponses) {
				spectrumDraft.response = spectrumResponses[i]
			}
		case spectrumDesignMethod:
			if i >= 0 && i < len(spectrumMethods) {
				spectrumDraft.method = spectrumMethods[i]
			}
		case spectrumDesignPhase:
			if i >= 0 && i < len(spectrumPhases) {
				spectrumDraft.phase = spectrumPhases[i]
			}
		}
		spectrumDraft.error = ""
	case spectrumResponsePreviewID:
		p, dt, err := spectrumDraftParameters()
		if err != nil {
			spectrumDraft.error = "不能预览：" + err.Error()
		} else {
			spectrumDraft.error = ""
			if spectrumDraft.target == viewFilterBoth {
				_, _, _, aDT := poststackViewFilterDialogInfo()
				_, bDT := poststackViewFilterSideScope(1)
				if aDT != bDT {
					spectrumDraft.error = fmt.Sprintf("A/B 采样间隔不同；本次预览采用 dt %.6g ms（较低 Nyquist）。", float64(dt)/1000)
				}
			}
			showTemporalFilterResponse(spectrumHwnd, dt, p)
		}
	case spectrumApplyID:
		applySpectrumFilter()
		return true
	case spectrumRestoreID:
		spectrumDraft.error = ""
		if err := restorePoststackViewFilterTarget(spectrumDraft.target); err != nil {
			spectrumDraft.error = err.Error()
		}
	case spectrumBaseID:
		spectrumDraft.showBase = !spectrumDraft.showBase
		pInvalidateRect.Call(spectrumHwnd, 0, 1)
	case spectrumCurrentID:
		spectrumDraft.showCurrent = !spectrumDraft.showCurrent
		pInvalidateRect.Call(spectrumHwnd, 0, 1)
	case spectrumRatioID:
		spectrumDraft.showRatio = !spectrumDraft.showRatio
		pInvalidateRect.Call(spectrumHwnd, 0, 1)
	case IDCSP_CLOSE:
		closePoststackSpectrum()
		return true
	default:
		return false
	}
	syncPoststackSpectrumControls()
	refreshSpectrumFilterResponse()
	return true
}

func translatePoststackSpectrumMessage(m *MSG) bool {
	h := spectrumHwnd
	if h == 0 || m == nil || m.Message != WM_KEYDOWN {
		return false
	}
	if m.Hwnd != h {
		if child, _, _ := pIsChild.Call(h, m.Hwnd); child == 0 {
			return false
		}
	}
	switch m.WParam {
	case VK_ESCAPE:
		closePoststackSpectrum()
		return true
	case VK_RETURN:
		if m.Hwnd == spectrumUI.low || m.Hwnd == spectrumUI.high || m.Hwnd == spectrumUI.designFields[spectrumDesignOrder] || m.Hwnd == spectrumUI.designFields[spectrumDesignRipple] || m.Hwnd == spectrumUI.designFields[spectrumDesignAttenuation] {
			applySpectrumFilter()
			return true
		}
	case 0x09:
		ok, _, _ := algorithmIsDialogMessage.Call(h, uintptr(unsafe.Pointer(m)))
		return ok != 0
	}
	return false
}

func closePoststackSpectrum() {
	if spectrumHwnd != 0 {
		pDestroyWindow.Call(spectrumHwnd)
	}
}

func destroyPoststackSpectrum() {
	saveSpectrumDraft()
	cancelPoststackSpectrumAnalysis()
	if spectrumFont != 0 {
		pDeleteObject.Call(spectrumFont)
	}
	spectrumFont, spectrumFontDPI = 0, 0
	spectrumHwnd = 0
	spectrumUI = spectrumControls{}
	spectrumData = spectrumViewData{}
}

func paintPoststackSpectrum(h uintptr) {
	displayBeginPaint(h)
	var ps PAINTSTRUCT
	dc, _, _ := pBeginPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
	brush, _, _ := algorithmSysColorBrush.Call(COLOR_WINDOW)
	pFillRect.Call(dc, uintptr(unsafe.Pointer(&ps.RcPaint)), brush)
	if spectrumFont != 0 {
		old, _, _ := pSelectObject.Call(dc, spectrumFont)
		defer pSelectObject.Call(dc, old)
	}
	pSetBkMode.Call(dc, TRANSPARENT)
	pSetTextColor.Call(dc, rgbRef(35, 45, 60))
	w, height := clientSize(h)
	l := spectrumLayout(w, height, spectrumWindowDPI(h))
	if l.Plot.Right <= l.Plot.Left || l.Plot.Bottom <= l.Plot.Top {
		return
	}
	d := spectrumData
	text := func(s string, r RECT, flags int) {
		drawAxisText(dc, s, int(r.Left), int(r.Top), int(r.Right), int(r.Bottom), uint32(flags))
	}
	if len(d.A.Freq) == 0 {
		text(d.Label, l.Plot, DT_CENTER)
		return
	}
	if spectrumDraft.showRatio && len(d.DiffFreq) == 0 {
		text("当前 A/B 幅度谱比（dB）不可用："+d.RatioReason, l.Plot, DT_CENTER)
		return
	}
	curves := []spectrumCurve{d.A, d.B, d.BaseA, d.BaseB}
	key, title := "spectrum/main", "频谱"
	if spectrumDraft.showRatio {
		curves = []spectrumCurve{{Freq: d.DiffFreq, DB: d.DiffDB}}
		key, title = "spectrum/ratio", "A/B 幅度谱比"
	}
	top := l.Plot
	fMin, fMax, dbMin, dbMax := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, c := range curves {
		for i := 0; i < len(c.Freq) && i < len(c.DB); i++ {
			if math.IsNaN(c.DB[i]) || math.IsInf(c.DB[i], 0) {
				continue
			}
			fMin, fMax = math.Min(fMin, c.Freq[i]), math.Max(fMax, c.Freq[i])
			dbMin, dbMax = math.Min(dbMin, c.DB[i]), math.Max(dbMax, c.DB[i])
		}
	}
	if math.IsInf(fMin, 1) {
		text("没有有效频谱点", top, DT_CENTER)
		return
	}
	xBase := makeChartAxis("frequency", "频率", "Hz", fMin, fMax, false)
	yBase := makeChartAxis("amplitude", "幅度", "dB", dbMin, dbMax, false)
	xBase.View.Min = 0
	yBase.View.Min, yBase.View.Max = -80, math.Ceil(math.Max(0, dbMax)/20)*20
	if spectrumDraft.showRatio {
		yBase.Title = "幅度谱比"
		yBase.View.Min, yBase.View.Max = -40, 40
	}
	xAxis, yAxis := registerChartAxes(h, key, title, spectrumChartIdentity, top, xBase, yBase)
	displayDrawAxes(dc, top, xAxis, yAxis)
	legend := fmt.Sprintf("A 蓝色  %d 道 / FFT %d", d.A.TraceCount, d.A.NFFT)
	if d.HasB {
		legend += fmt.Sprintf("    B 红色  %d 道 / FFT %d", d.B.TraceCount, d.B.NFFT)
	}
	legend += fmt.Sprintf("    统一 dB 参考 %.5g", d.Reference)
	if spectrumDraft.showRatio {
		legend = fmt.Sprintf("当前 A/B 幅度谱比（dB） · %d 对匹配道 / FFT %d", d.RatioTraceCount, d.RatioNFFT)
	}
	text(legend, l.Legend, DT_LEFT)
	caption := "基准虚线 / 当前实线；相同分析窗、Hann 与幅度标定"
	if spectrumDraft.showRatio {
		caption = "20 log10(A/B)；仅匹配道和相同实际时间窗；零幅值频点不绘制"
	}
	drawAxisText(dc, caption, int(top.Left), int(top.Top)-25, int(top.Right), int(top.Top)-3, DT_CENTER)
	saved, _, _ := pSaveDC.Call(dc)
	pIntersectClipRect.Call(dc, uintptr(top.Left), uintptr(top.Top), uintptr(top.Right+1), uintptr(top.Bottom+1))
	defer pRestoreDC.Call(dc, saved)
	curve := func(c spectrumCurve, color uintptr, dashed bool) {
		if len(c.Freq) < 2 {
			return
		}
		style, width := uintptr(PS_SOLID), uintptr(2)
		if dashed {
			style, width = 1, 1
		}
		p, _, _ := pCreatePen.Call(style, width, color)
		old, _, _ := pSelectObject.Call(dc, p)
		connected, penPlaced := false, false
		var prevF, prevV float64
		var penX, penY int
		for i, f := range c.Freq {
			if i >= len(c.DB) {
				break
			}
			if math.IsNaN(c.DB[i]) || math.IsInf(c.DB[i], 0) || math.IsNaN(f) || math.IsInf(f, 0) {
				connected = false
				continue
			}
			v := c.DB[i]
			if !yAxis.ViewEdited {
				v = clampFloat(v, yAxis.View.Min, yAxis.View.Max)
			}
			if connected {
				if x0, y0, x1, y1, ok := chartClipSegment(xAxis, yAxis, prevF, prevV, f, v, top); ok {
					if !penPlaced || penX != x0 || penY != y0 {
						pMoveToEx.Call(dc, uintptr(x0), uintptr(y0), 0)
					}
					pLineTo.Call(dc, uintptr(x1), uintptr(y1))
					penPlaced, penX, penY = true, x1, y1
				}
			}
			prevF, prevV = f, v
			connected = true
		}
		pSelectObject.Call(dc, old)
		pDeleteObject.Call(p)
	}
	if spectrumDraft.showRatio {
		curve(spectrumCurve{Freq: d.DiffFreq, DB: d.DiffDB}, rgbRef(125, 45, 170), false)
		return
	}
	if spectrumDraft.showBase {
		curve(d.BaseA, rgbRef(30, 100, 210), true)
		if d.HasB {
			curve(d.BaseB, rgbRef(210, 60, 55), true)
		}
	}
	if spectrumDraft.showCurrent {
		curve(d.A, rgbRef(30, 100, 210), false)
		if d.HasB {
			curve(d.B, rgbRef(210, 60, 55), false)
		}
	}
}
