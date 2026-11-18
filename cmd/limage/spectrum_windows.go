//go:build windows

package main

import (
	"fmt"
	"math"
	"sort"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

const (
	IDCSP_CLOSE = 6101
)

type spectrumCurve struct {
	Freq       []float64
	DB         []float64
	PeakHz     float64
	TraceCount int
	NFFT       int
}

type spectrumViewData struct {
	A, B                       spectrumCurve
	BaseA, BaseB               spectrumCurve
	DiffFreq, DiffDB           []float64
	Label                      string
	HasB                       bool
	StateA, StateB             string
	Reference                  float64
	BError                     string
	RatioReason                string
	RatioTraceCount, RatioNFFT int
}

var (
	spectrumHwnd uintptr
	spectrumData spectrumViewData
)

func evenlySampleTraces(in []int64, maxN int) []int64 {
	if len(in) <= maxN {
		return append([]int64(nil), in...)
	}
	out := make([]int64, 0, maxN)
	for i := 0; i < maxN; i++ {
		j := int(math.Round(float64(i) * float64(len(in)-1) / float64(maxN-1)))
		out = append(out, in[j])
	}
	return out
}

func nextPow2LE(n int) int {
	if n < 2 {
		return 0
	}
	p := 1
	for p<<1 <= n && p < 4096 {
		p <<= 1
	}
	if p > 4096 {
		p = 4096
	}
	return p
}

func meanSpectrum(f *segy.File, traces []int64, s0, s1 int) (spectrumCurve, error) {
	if f == nil {
		return spectrumCurve{}, fmt.Errorf("没有可用于频谱计算的数据")
	}
	return meanSpectrumRead(f.Info.SamplesPerTrace, f.Info.SampleIntervalUS, traces, s0, s1, f.ReadTraceWindow)
}

func meanSpectrumRead(samples, dtUS int, traces []int64, s0, s1 int, read func(int64, int, int) ([]float64, error)) (spectrumCurve, error) {
	if dtUS <= 0 || len(traces) == 0 {
		return spectrumCurve{}, fmt.Errorf("没有可用于频谱计算的道")
	}
	if s0 < 0 {
		s0 = 0
	}
	if s1 < 0 || s1 >= samples {
		s1 = samples - 1
	}
	if s1-s0+1 < 32 {
		return spectrumCurve{}, fmt.Errorf("当前时间窗过短，至少需要 32 个采样点")
	}
	nfft := nextPow2LE(s1 - s0 + 1)
	if nfft < 32 {
		return spectrumCurve{}, fmt.Errorf("时间窗不足")
	}
	s1 = s0 + nfft - 1
	traces = evenlySampleTraces(traces, 96)
	acc := make([]float64, nfft/2+1)
	used := 0
	amplitudeOptions := segyanalysis.LegacyQCAmplitudeOptions()
	for _, tr := range traces {
		x, err := read(tr, s0, s1)
		if err != nil {
			continue
		}
		amplitude, spectrumErr := segyanalysis.ComputeAmplitudeSpectrum(x, dtUS, amplitudeOptions)
		if spectrumErr != nil || amplitude.NFFT != nfft || len(amplitude.LinearAmplitude) != len(acc) {
			continue
		}
		for k, value := range amplitude.LinearAmplitude {
			acc[k] += value
		}
		used++
	}
	if used == 0 {
		return spectrumCurve{}, fmt.Errorf("频谱读取失败")
	}
	mx := 0.0
	peakK := 1
	for k := 1; k < len(acc); k++ {
		acc[k] /= float64(used)
		if acc[k] > mx {
			mx = acc[k]
			peakK = k
		}
	}
	if mx <= 0 {
		mx = 1
	}
	freq := make([]float64, len(acc))
	db := make([]float64, len(acc))
	dt := float64(dtUS) * 1e-6
	for k, v := range acc {
		freq[k] = float64(k) / (float64(nfft) * dt)
		ratio := v / mx
		if ratio < 1e-6 {
			ratio = 1e-6
		}
		db[k] = 20 * math.Log10(ratio)
		if db[k] < -80 {
			db[k] = -80
		}
	}
	return spectrumCurve{Freq: freq, DB: db, PeakHz: float64(peakK) / (float64(nfft) * dt), TraceCount: used, NFFT: nfft}, nil
}

func tracesInBounds(g *segy.GeometryIndex, b segy.SliceBounds) ([]int64, map[[2]int32]int64) {
	if g == nil {
		return nil, nil
	}
	out := make([]int64, 0)
	m := make(map[[2]int32]int64)
	for i, tr := range g.TraceNumbers {
		if i >= len(g.RowOfTrace) || i >= len(g.ColOfTrace) {
			break
		}
		ri, ci := int(g.RowOfTrace[i]), int(g.ColOfTrace[i])
		if ri < 0 || ri >= len(g.InlineValues) || ci < 0 || ci >= len(g.CrosslineValues) {
			continue
		}
		il, xl := g.InlineValues[ri], g.CrosslineValues[ci]
		if il < b.InlineMin || il > b.InlineMax || xl < b.CrosslineMin || xl > b.CrosslineMax {
			continue
		}
		out = append(out, tr)
		m[[2]int32{il, xl}] = tr
	}
	return out, m
}

func spectrumTraceLists() ([]int64, []int64, string, error) {
	d := compareTD
	if d == nil || d.fa == nil || d.ga == nil {
		return nil, nil, "", fmt.Errorf("请先完成 A 的三维几何加载")
	}
	if compareMode == 0 || compareMode == 1 {
		ta, ca, actual, err := d.ga.LineTraceNumbers(compareMode, compareLineCoord)
		if err != nil {
			return nil, nil, "", err
		}
		ta, ca = filterLineRange(ta, ca, compareLineXMin, compareLineXMax)
		label := fmt.Sprintf("%s %d | samples %d-%d", []string{"Inline", "Crossline"}[compareMode], actual, compareSampleStart, compareSampleEnd)
		if d.gb == nil {
			return ta, nil, label, nil
		}
		tb, cb, actualB, err := d.gb.LineTraceNumbers(compareMode, compareLineCoord)
		if err != nil {
			return nil, nil, "", err
		}
		if actualB != actual {
			return ta, nil, label, fmt.Errorf("A/B 当前几何线不一致：%d / %d", actual, actualB)
		}
		tb, cb = filterLineRange(tb, cb, compareLineXMin, compareLineXMax)
		pa, pb, _ := pairLineTracesByCoord(ta, ca, tb, cb)
		return pa, pb, label, nil
	}
	ta, ma := tracesInBounds(d.ga, d.bounds)
	label := fmt.Sprintf("Time Slice area IL %d-%d / XL %d-%d | spectrum time window samples %d-%d", d.bounds.InlineMin, d.bounds.InlineMax, d.bounds.CrosslineMin, d.bounds.CrosslineMax, compareSampleStart, compareSampleEnd)
	if d.gb == nil {
		return ta, nil, label, nil
	}
	_, mb := tracesInBounds(d.gb, d.bounds)
	pa := make([]int64, 0)
	pb := make([]int64, 0)
	keys := make([][2]int32, 0, len(ma))
	for k := range ma {
		if _, ok := mb[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] == keys[j][0] {
			return keys[i][1] < keys[j][1]
		}
		return keys[i][0] < keys[j][0]
	})
	for _, k := range keys {
		pa = append(pa, ma[k])
		pb = append(pb, mb[k])
	}
	return pa, pb, label, nil
}

func interpCurve(c spectrumCurve, f float64) float64 {
	if len(c.Freq) == 0 || f < c.Freq[0] || f > c.Freq[len(c.Freq)-1] {
		return math.NaN()
	}
	i := sort.SearchFloat64s(c.Freq, f)
	if i == 0 {
		return c.DB[0]
	}
	if i >= len(c.Freq) {
		return c.DB[len(c.DB)-1]
	}
	f0, f1 := c.Freq[i-1], c.Freq[i]
	if f1 <= f0 {
		return c.DB[i]
	}
	t := (f - f0) / (f1 - f0)
	return c.DB[i-1] + t*(c.DB[i]-c.DB[i-1])
}

func buildSpectrumData() (spectrumViewData, error) {
	r, err := capturePoststackSpectrumRequest()
	if err != nil {
		return spectrumViewData{}, err
	}
	return computePoststackSpectrum(r)
}

func buildViewFilterSpectrum(s *viewFilterSnapshot) (spectrumViewData, error) {
	r, err := capturePoststackSpectrumRequest()
	if err != nil {
		return spectrumViewData{}, err
	}
	for i := range r.sources {
		if s != nil && len(s.rows[i].traces) > 0 {
			r.sources[i].view = &s.rows[i]
		}
	}
	return computePoststackSpectrum(r)
}

func refreshViewFilterSpectrum() {
	startPoststackSpectrumAnalysis()
}

func showSpectrumWindow() {
	if spectrumHwnd != 0 {
		syncPoststackSpectrumControls()
		showTopLevelWindowRestored(spectrumHwnd)
		startPoststackSpectrumAnalysis()
		return
	}
	restore := beginAlgorithmDPIContext()
	defer restore()
	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("Limage64Spectrum"))), uintptr(unsafe.Pointer(u16("频谱与视图滤波 — "+APP_NAME))), WS_OVERLAPPEDWINDOW|WS_CLIPCHILDREN, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 940, 760, compareHwnd, 0, 0, 0)
	if h == 0 {
		return
	}
	spectrumHwnd = h
	makePoststackSpectrumControls(h)
	fitPoststackSpectrumWindow(h)
	syncPoststackSpectrumControls()
	pShowWindow.Call(h, SW_SHOW)
	pUpdateWindow.Call(h)
	startPoststackSpectrumAnalysis()
}

func spectrumPoint(freq, db, fmax float64, r RECT, yMin, yMax float64) (int, int) {
	fx := freq / fmax
	if fx < 0 {
		fx = 0
	}
	if fx > 1 {
		fx = 1
	}
	fy := (yMax - db) / (yMax - yMin)
	if fy < 0 {
		fy = 0
	}
	if fy > 1 {
		fy = 1
	}
	return int(r.Left) + int(fx*float64(r.Right-r.Left)), int(r.Top) + int(fy*float64(r.Bottom-r.Top))
}

func spectrumWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		if poststackSpectrumCommand(wParam) {
			return 0
		}
	case WM_PAINT:
		paintPoststackSpectrum(h)
		return 0
	case WM_SIZE:
		layoutPoststackSpectrumControls(h)
		return 0
	case WM_LBUTTONDBLCLK:
		x, y := int(int16(lParam)), int(int16(lParam>>16))
		if displaySurfaceClick(h, x, y, false) {
			return 0
		}
	case 0x007B: // WM_CONTEXTMENU
		if displayContextMenu(h, lParam) {
			return 0
		}
	case spectrumReadyMessage:
		acceptPoststackSpectrumAnalysis()
		return 0
	case algorithmWMDPIChanged:
		if lParam != 0 {
			r := *(*RECT)(unsafe.Pointer(lParam))
			pSetWindowPos.Call(h, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), SWP_NOZORDER|SWP_NOACTIVATE)
			layoutPoststackSpectrumControls(h)
		}
		return 0
	case WM_GETMINMAXINFO:
		if lParam != 0 {
			m := (*MINMAXINFO)(unsafe.Pointer(lParam))
			dpi, area := spectrumWindowDPI(h), algorithmMonitorArea(h)
			m.PtMinTrackSize.X = int32(minInt(580*dpi/96, int(area.Right-area.Left)))
			m.PtMinTrackSize.Y = int32(minInt(580*dpi/96, int(area.Bottom-area.Top)))
		}
		return 0
	case WM_ERASEBKGND:
		return 1
	case algorithmWMControlStatic:
		pSetTextColor.Call(wParam, rgbRef(45, 55, 70))
		brush, _, _ := algorithmSysColorBrush.Call(COLOR_WINDOW)
		return brush
	case 0x0135: // WM_CTLCOLORBTN
		brush, _, _ := algorithmSysColorBrush.Call(COLOR_WINDOW)
		return brush
	case WM_CLOSE:
		closePoststackSpectrum()
		return 0
	case WM_DESTROY:
		displayCloseOwner(h)
		destroyPoststackSpectrum()
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}
