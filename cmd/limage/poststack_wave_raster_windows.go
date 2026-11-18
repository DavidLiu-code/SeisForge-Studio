//go:build windows

package main

import "math"

// Wave paths stay in floating point until a screen-sized coverage raster.
// This works with both the Win32 paint DC and the existing BMP export DC;
// no renderer or GUI dependency is loaded.
type poststackWavePoint struct{ x, y float64 }

type poststackWaveRaster struct {
	w, h  int
	bgra  []byte
	line  [2][]byte
	fills [2][3][]byte
}

func newPoststackWaveRaster(w, h int, strokes bool, fills [2]bool) *poststackWaveRaster {
	if w <= 0 || h <= 0 {
		return nil
	}
	r := &poststackWaveRaster{w: w, h: h, bgra: make([]byte, w*h*4)}
	for i := range r.bgra {
		r.bgra[i] = 255
	}
	for ink := range r.line {
		if strokes {
			r.line[ink] = make([]byte, w*h)
		}
		if fills[ink] {
			for phase := range r.fills[ink] {
				r.fills[ink][phase] = make([]byte, w*h)
			}
		}
	}
	return r
}

func poststackWaveCoverage(f float64) byte {
	return byte(255*max(0, min(1, f)) + .5)
}

func (r *poststackWaveRaster) area(ink int, base float64, a, b poststackWavePoint) {
	if a.y > b.y {
		a, b = b, a
	}
	if a.y == b.y || a.x == base && b.x == base {
		return
	}
	// At three fixed subpixel scan lines, measure the exact horizontal
	// overlap. Different segments meeting in a pixel fill its respective
	// scan lines without alpha-dark seams or alternating-polygon holes.
	from := int(max(0, math.Floor(a.y)))
	to := int(min(float64(r.h-1), math.Floor(b.y)))
	for y := from; y <= to; y++ {
		for phase, plane := range r.fills[ink] {
			sy := float64(y) + (float64(phase)+.5)/3
			if sy < a.y || sy >= b.y {
				continue
			}
			x := waveScreenLerp(a.x, b.x, (sy-a.y)/(b.y-a.y))
			left, right := min(base, x), max(base, x)
			if right <= 0 || left >= float64(r.w) {
				continue
			}
			start := int(max(0, math.Floor(left)))
			end := int(min(float64(r.w-1), math.Floor(right)))
			if start > end {
				continue
			}
			i, j := y*r.w+start, y*r.w+end
			if i == j {
				plane[i] = max(plane[i], poststackWaveCoverage(right-left))
				continue
			}
			plane[i] = max(plane[i], poststackWaveCoverage(float64(start+1)-left))
			plane[j] = max(plane[j], poststackWaveCoverage(right-float64(end)))
			for cell := i + 1; cell < j; cell++ {
				plane[cell] = 255
			}
		}
	}
}

func waveScreenLerp(a, b, f float64) float64 { return (1-f)*a + f*b }

// Clip to a pen-expanded display box before visiting a line's pixels. The
// displayed projection can extend past a reference without overflow or an
// artificial amplitude clamp; the only truncation is clipping at the canvas.
func poststackWaveClipLine(a, b poststackWavePoint, w, h int, fringe float64) (poststackWavePoint, poststackWavePoint, bool) {
	lo, hi := 0.0, 1.0
	for _, axis := range [][4]float64{
		{a.x, b.x, -fringe, float64(w) + fringe},
		{a.y, b.y, -fringe, float64(h) + fringe},
	} {
		start, end, lower, upper := axis[0], axis[1], axis[2], axis[3]
		d := end - start
		if d == 0 {
			if start < lower || start > upper {
				return a, b, false
			}
			continue
		}
		t0, t1 := (lower-start)/d, (upper-start)/d
		lo, hi = max(lo, min(t0, t1)), min(hi, max(t0, t1))
	}
	if hi < lo {
		return a, b, false
	}
	return poststackWavePoint{waveScreenLerp(a.x, b.x, lo), waveScreenLerp(a.y, b.y, lo)},
		poststackWavePoint{waveScreenLerp(a.x, b.x, hi), waveScreenLerp(a.y, b.y, hi)}, true
}

func (r *poststackWaveRaster) stroke(plane []byte, a, b poststackWavePoint, width float64) {
	radius := width * .5
	fringe := radius + .5
	a, b, ok := poststackWaveClipLine(a, b, r.w, r.h, fringe)
	if !ok {
		return
	}
	dx, dy := b.x-a.x, b.y-a.y
	swap := math.Abs(dx) > math.Abs(dy)
	u0, u1, v0, v1, wide, tall := a.y, b.y, a.x, b.x, r.w, r.h
	if swap {
		u0, u1, v0, v1, wide, tall = a.x, b.x, a.y, b.y, r.h, r.w
	}
	uMid := max(0, int(math.Floor(min(u0, u1)-fringe)))
	uEnd := min(tall-1, int(math.Floor(max(u0, u1)+fringe)))
	length2 := dx*dx + dy*dy
	// Scan in the dominant direction so a long horizontal excursion does
	// not revisit its whole axis-aligned bounding box for every time row.
	for u := uMid; u <= uEnd; u++ {
		uc := float64(u) + .5
		t := 0.0
		if u1 != u0 {
			t = max(0, min(1, (uc-u0)/(u1-u0)))
		}
		vc := waveScreenLerp(v0, v1, t)
		vFrom := max(0, int(math.Floor(vc-fringe*1.42)))
		vEnd := min(wide-1, int(math.Floor(vc+fringe*1.42)))
		for v := vFrom; v <= vEnd; v++ {
			x, y := v, u
			if swap {
				x, y = u, v
			}
			xp, yp := float64(x)+.5, float64(y)+.5
			f := 0.0
			if length2 > 0 {
				f = max(0, min(1, ((xp-a.x)*dx+(yp-a.y)*dy)/length2))
			}
			ex, ey := xp-a.x-f*dx, yp-a.y-f*dy
			i := y*r.w + x
			plane[i] = max(plane[i], poststackWaveCoverage(fringe-math.Sqrt(ex*ex+ey*ey)))
		}
	}
}

func (r *poststackWaveRaster) blend(i int, color uintptr, alpha int) {
	if alpha <= 0 {
		return
	}
	// GDI COLORREF is 0x00BBGGRR, whereas the DIB is stored as BGRA bytes.
	for j := 0; j < 3; j++ {
		src := int((color >> (8 * (2 - j))) & 255)
		k := i*4 + j
		r.bgra[k] = byte((src*alpha + int(r.bgra[k])*(255-alpha) + 127) / 255)
	}
}

func (r *poststackWaveRaster) baseline(x float64) {
	if x < -.5 || x > float64(r.w)+.5 {
		return
	}
	lo, hi := max(0, int(math.Floor(x-.5))), min(r.w-1, int(math.Floor(x+.5)))
	for i := lo; i <= hi; i++ {
		coverage := int(poststackWaveCoverage(1 - math.Abs(float64(i)+.5-x)))
		for y := 0; y < r.h; y++ {
			r.blend(y*r.w+i, rgbRef(207, 216, 226), coverage)
		}
	}
}

func (r *poststackWaveRaster) finish(colors [2]poststackWaveInk, opacity float64) {
	for ink, c := range colors {
		if r.fills[ink][0] != nil {
			p := r.fills[ink]
			for i, a := range p[0] {
				alpha := opacity * float64(int(a)+int(p[1][i])+int(p[2][i])) / 3
				r.blend(i, c.color, int(math.Round(alpha)))
			}
		}
	}
	for ink, c := range colors {
		for i, alpha := range r.line[ink] {
			r.blend(i, c.color, int(alpha))
		}
	}
}
