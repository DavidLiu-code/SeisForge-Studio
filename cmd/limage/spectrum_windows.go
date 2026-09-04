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
	A, B             spectrumCurve
	DiffFreq, DiffDB []float64
	Label            string
	HasB             bool
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
	if f == nil || len(traces) == 0 {
		return spectrumCurve{}, fmt.Errorf("没有可用于频谱计算的道")
	}
	if s0 < 0 {
		s0 = 0
	}
	if s1 < 0 || s1 >= f.Info.SamplesPerTrace {
		s1 = f.Info.SamplesPerTrace - 1
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
		x, err := f.ReadTraceWindow(tr, s0, s1)
		if err != nil {
			continue
		}
		amplitude, spectrumErr := segyanalysis.ComputeAmplitudeSpectrum(x, f.Info.SampleIntervalUS, amplitudeOptions)
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
	dt := float64(f.Info.SampleIntervalUS) * 1e-6
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
	ta, tb, label, err := spectrumTraceLists()
	if err != nil && len(ta) == 0 {
		return spectrumViewData{}, err
	}
	d := compareTD
	a, ea := meanSpectrum(d.fa, ta, compareSampleStart, compareSampleEnd)
	if ea != nil {
		return spectrumViewData{}, ea
	}
	out := spectrumViewData{A: a, Label: label}
	if d.fb != nil && len(tb) > 0 {
		b, eb := meanSpectrum(d.fb, tb, compareSampleStart, compareSampleEnd)
		if eb != nil {
			return spectrumViewData{}, eb
		}
		out.B = b
		out.HasB = true
		maxF := math.Min(a.Freq[len(a.Freq)-1], b.Freq[len(b.Freq)-1])
		for i, f := range a.Freq {
			if f > maxF {
				break
			}
			bv := interpCurve(b, f)
			if !math.IsNaN(bv) {
				out.DiffFreq = append(out.DiffFreq, f)
				out.DiffDB = append(out.DiffDB, a.DB[i]-bv)
			}
		}
	}
	return out, nil
}

func showSpectrumWindow() {
	if compareTD == nil {
		message(compareHwnd, "频谱", "请先打开 A 并完成几何加载。", MB_OK|MB_ICONINFORMATION)
		return
	}
	setCompareBusy(1)
	defer setCompareBusy(-1)
	setText(cc.status, "正在计算当前可见范围的平均振幅谱...")
	d, err := buildSpectrumData()
	if err != nil {
		message(compareHwnd, "频谱", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	spectrumData = d
	if spectrumHwnd != 0 {
		pInvalidateRect.Call(spectrumHwnd, 0, 0)
		pSetForeground.Call(spectrumHwnd)
		return
	}
	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16("Limage64Spectrum"))), uintptr(unsafe.Pointer(u16(APP_NAME+" - 频谱对比"))), WS_OVERLAPPEDWINDOW, uintptr(CW_USEDEFAULT), uintptr(CW_USEDEFAULT), 900, 620, compareHwnd, 0, 0, 0)
	if h == 0 {
		return
	}
	spectrumHwnd = h
	createCtrl(h, "BUTTON", "关闭", WS_CHILD|WS_VISIBLE|WS_TABSTOP|BS_PUSHBUTTON, 812, 8, 60, 24, IDCSP_CLOSE)
	pShowWindow.Call(h, SW_SHOW)
	pUpdateWindow.Call(h)
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

func drawSpectrumCurve(hdc uintptr, c spectrumCurve, r RECT, fmax float64, color uintptr) {
	if len(c.Freq) < 2 {
		return
	}
	pen, _, _ := pCreatePen.Call(PS_SOLID, 2, color)
	old, _, _ := pSelectObject.Call(hdc, pen)
	started := false
	for i, f := range c.Freq {
		if f > fmax {
			break
		}
		x, y := spectrumPoint(f, c.DB[i], fmax, r, -60, 0)
		if !started {
			pMoveToEx.Call(hdc, uintptr(x), uintptr(y), 0)
			started = true
		} else {
			pLineTo.Call(hdc, uintptr(x), uintptr(y))
		}
	}
	pSelectObject.Call(hdc, old)
	if pen != 0 {
		pDeleteObject.Call(pen)
	}
}

func paintSpectrum(h uintptr) {
	var ps PAINTSTRUCT
	hdc, _, _ := pBeginPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(h, uintptr(unsafe.Pointer(&ps)))
	cw, ch := clientSize(h)
	if cw < 200 || ch < 180 {
		return
	}
	d := spectrumData
	fmax := 0.0
	if len(d.A.Freq) > 0 {
		fmax = d.A.Freq[len(d.A.Freq)-1]
	}
	if d.HasB && len(d.B.Freq) > 0 {
		fmax = math.Min(fmax, d.B.Freq[len(d.B.Freq)-1])
	}
	if fmax <= 0 {
		return
	}
	// Seismic QC convention: show the useful band to 125 Hz by default.
	// For lower-rate data, never exceed the physical Nyquist limit.
	if fmax > 125.0 {
		fmax = 125.0
	}
	top := RECT{Left: 70, Top: 65, Right: int32(cw - 28), Bottom: int32(ch - 220)}
	if !d.HasB {
		top.Bottom = int32(ch - 65)
	}
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(40, 40, 40))
	old, _, _ := pSelectObject.Call(hdc, pen)
	drawLine(hdc, int(top.Left), int(top.Top), int(top.Left), int(top.Bottom))
	drawLine(hdc, int(top.Left), int(top.Bottom), int(top.Right), int(top.Bottom))
	pSelectObject.Call(hdc, old)
	pDeleteObject.Call(pen)
	drawAxisText(hdc, "Normalized amplitude spectrum (dB)", 70, 38, cw-30, 58, DT_CENTER)
	drawAxisText(hdc, "0 dB", 8, int(top.Top)-8, 64, int(top.Top)+10, DT_RIGHT)
	drawAxisText(hdc, "-60", 8, int(top.Bottom)-10, 64, int(top.Bottom)+8, DT_RIGHT)
	// Frequency ticks. 125 Hz is the default QC limit; if Nyquist is below
	// that value the final tick is the actual physical limit.
	tickStep := 25.0
	for f := 0.0; f <= fmax+1e-9; f += tickStep {
		x, _ := spectrumPoint(f, -60, fmax, top, -60, 0)
		drawLine(hdc, x, int(top.Bottom), x, int(top.Bottom)+5)
		label := fmt.Sprintf("%.0f", f)
		if math.Abs(f-fmax) < 1e-6 || f+tickStep <= fmax+1e-9 {
			drawAxisText(hdc, label, x-28, int(top.Bottom)+5, x+28, int(top.Bottom)+24, DT_CENTER)
		}
	}
	// If Nyquist is not an exact multiple of 25 Hz, explicitly label the end.
	if math.Mod(fmax, tickStep) > 1e-6 {
		x, _ := spectrumPoint(fmax, -60, fmax, top, -60, 0)
		drawLine(hdc, x, int(top.Bottom), x, int(top.Bottom)+5)
		drawAxisText(hdc, fmt.Sprintf("%.1f", fmax), x-38, int(top.Bottom)+5, x+10, int(top.Bottom)+24, DT_RIGHT)
	}
	drawAxisText(hdc, "Frequency (Hz)", int(top.Left), int(top.Bottom)+25, int(top.Right), int(top.Bottom)+45, DT_CENTER)
	drawSpectrumCurve(hdc, d.A, top, fmax, rgbRef(0, 80, 220))
	if d.HasB {
		drawSpectrumCurve(hdc, d.B, top, fmax, rgbRef(220, 40, 40))
	}
	legend := fmt.Sprintf("A  peak %.1f Hz | %d traces | NFFT %d", d.A.PeakHz, d.A.TraceCount, d.A.NFFT)
	if d.HasB {
		legend += fmt.Sprintf("     B  peak %.1f Hz | %d traces | NFFT %d", d.B.PeakHz, d.B.TraceCount, d.B.NFFT)
	}
	drawAxisText(hdc, legend, 70, 8, cw-100, 30, DT_LEFT)
	drawAxisText(hdc, d.Label, 70, 30, cw-30, 48, DT_LEFT)
	if d.HasB && len(d.DiffFreq) > 1 {
		bot := RECT{Left: 70, Top: int32(ch - 155), Right: int32(cw - 28), Bottom: int32(ch - 55)}
		pen, _, _ = pCreatePen.Call(PS_SOLID, 1, rgbRef(40, 40, 40))
		old, _, _ = pSelectObject.Call(hdc, pen)
		drawLine(hdc, int(bot.Left), int(bot.Top), int(bot.Left), int(bot.Bottom))
		drawLine(hdc, int(bot.Left), int((bot.Top+bot.Bottom)/2), int(bot.Right), int((bot.Top+bot.Bottom)/2))
		drawLine(hdc, int(bot.Left), int(bot.Bottom), int(bot.Right), int(bot.Bottom))
		pSelectObject.Call(hdc, old)
		pDeleteObject.Call(pen)
		maxAbs := 6.0
		for _, v := range d.DiffDB {
			if math.Abs(v) > maxAbs {
				maxAbs = math.Abs(v)
			}
		}
		if maxAbs > 30 {
			maxAbs = 30
		}
		drawAxisText(hdc, "A - B spectral difference (dB)", 70, int(bot.Top)-22, cw-30, int(bot.Top)-3, DT_CENTER)
		gp, _, _ := pCreatePen.Call(PS_SOLID, 2, rgbRef(0, 150, 60))
		old, _, _ = pSelectObject.Call(hdc, gp)
		started := false
		for i, f := range d.DiffFreq {
			if f > fmax {
				break
			}
			fx := f / fmax
			yval := d.DiffDB[i]
			if yval > maxAbs {
				yval = maxAbs
			}
			if yval < -maxAbs {
				yval = -maxAbs
			}
			fy := (maxAbs - yval) / (2 * maxAbs)
			x := int(bot.Left) + int(fx*float64(bot.Right-bot.Left))
			y := int(bot.Top) + int(fy*float64(bot.Bottom-bot.Top))
			if !started {
				pMoveToEx.Call(hdc, uintptr(x), uintptr(y), 0)
				started = true
			} else {
				pLineTo.Call(hdc, uintptr(x), uintptr(y))
			}
		}
		pSelectObject.Call(hdc, old)
		pDeleteObject.Call(gp)
		drawAxisText(hdc, fmt.Sprintf("+%.1f", maxAbs), 8, int(bot.Top)-8, 64, int(bot.Top)+10, DT_RIGHT)
		drawAxisText(hdc, fmt.Sprintf("-%.1f", maxAbs), 8, int(bot.Bottom)-10, 64, int(bot.Bottom)+8, DT_RIGHT)
	}
}

func spectrumWndProc(h uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case WM_COMMAND:
		if int(wParam&0xffff) == IDCSP_CLOSE {
			pDestroyWindow.Call(h)
			return 0
		}
	case WM_PAINT:
		paintSpectrum(h)
		return 0
	case WM_SIZE:
		pInvalidateRect.Call(h, 0, 1)
		return 0
	case WM_CLOSE:
		pDestroyWindow.Call(h)
		return 0
	case WM_DESTROY:
		spectrumHwnd = 0
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, uintptr(msg), wParam, lParam)
	return r
}
