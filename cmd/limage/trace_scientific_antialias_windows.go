//go:build windows

package main

import (
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// The classic GDI Polyline fallback takes integer vertices. The default path
// below passes the same scene's fractional vertices to the Windows-supplied
// GDI+ stroke rasterizer. No samples are interpolated or smoothed in the model.
var (
	traceSystemDir     = traceSystemDirectory()
	traceGP            = syscall.NewLazyDLL(filepath.Join(traceSystemDir, "gdiplus.dll"))
	traceGPStart       = traceGP.NewProc("GdiplusStartup")
	traceGPCreate      = traceGP.NewProc("GdipCreateFromHDC")
	traceGPDelete      = traceGP.NewProc("GdipDeleteGraphics")
	traceGPSmooth      = traceGP.NewProc("GdipSetSmoothingMode")
	traceGPPixelUnit   = traceGP.NewProc("GdipSetPageUnit")
	traceGPCreatePen   = traceGP.NewProc("GdipCreatePen1")
	traceGPDeletePen   = traceGP.NewProc("GdipDeletePen")
	traceGPDash        = traceGP.NewProc("GdipSetPenDashArray")
	traceGPJoin        = traceGP.NewProc("GdipSetPenLineJoin")
	traceGPLine        = traceGP.NewProc("GdipDrawLines")
	traceGPClip        = traceGP.NewProc("GdipSetClipRectI")
	traceGPReset       = traceGP.NewProc("GdipResetClip")
	traceGPCreateBrush = traceGP.NewProc("GdipCreateSolidFill")
	traceGPDeleteBrush = traceGP.NewProc("GdipDeleteBrush")
	traceGPFill        = traceGP.NewProc("GdipFillRectangleI")
	traceGPOnce        sync.Once
	traceGPToken       uintptr
)

func traceSystemDirectory() string {
	var b [32768]uint16
	get := syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemDirectoryW")
	n, _, _ := get.Call(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	if n == 0 || n >= uintptr(len(b)) {
		return "" // no attempt to load a DLL from the working directory
	}
	return syscall.UTF16ToString(b[:n])
}

func traceGPGraphics(hdc uintptr) uintptr {
	traceGPOnce.Do(func() {
		if traceSystemDir == "" || traceGPStart.Find() != nil {
			return
		}
		input := struct {
			Version                                uint32
			Callback                               uintptr
			SuppressThread, SuppressExternalCodecs uint32
		}{Version: 1, SuppressExternalCodecs: 1}
		r, _, _ := traceGPStart.Call(uintptr(unsafe.Pointer(&traceGPToken)), uintptr(unsafe.Pointer(&input)), 0)
		if r != 0 {
			traceGPToken = 0
		}
	})
	if traceGPToken == 0 {
		return 0
	}
	var g uintptr
	if r, _, _ := traceGPCreate.Call(hdc, uintptr(unsafe.Pointer(&g))); r != 0 {
		return 0
	}
	traceGPPixelUnit.Call(g, 2) // UnitPixel
	traceGPSmooth.Call(g, 4)    // SmoothingModeAntiAlias
	return g
}

func traceGPARGB(ink uint32, alpha uint8) uintptr {
	return uintptr(uint32(alpha)<<24 | (ink&255)<<16 | ink&0xff00 | (ink>>16)&255)
}

func traceGPPen(ink uint32, width float64, dash []float64, scale float64) uintptr {
	var pen uintptr
	res, _, _ := traceGPCreatePen.Call(traceGPARGB(ink, 255), uintptr(math.Float32bits(float32(width))), 2, uintptr(unsafe.Pointer(&pen)))
	if res != 0 {
		return 0
	}
	traceGPJoin.Call(pen, 2) // LineJoinRound
	if len(dash) != 0 {
		pattern := make([]float32, len(dash))
		for i, v := range dash {
			pattern[i] = float32(v * scale / width) // GDI+ dash lengths are in pen-width units
		}
		traceGPDash.Call(pen, uintptr(unsafe.Pointer(&pattern[0])), uintptr(len(pattern)))
		runtime.KeepAlive(pattern)
	}
	return pen
}

func (s traceScientificScene) drawAntialiased(hdc uintptr, x, y int, scale float64) bool {
	g := traceGPGraphics(hdc)
	if g == 0 {
		return false
	}
	defer traceGPDelete.Call(g)
	cache := make(map[string]uintptr)
	defer func() {
		for _, pen := range cache {
			traceGPDeletePen.Call(pen)
		}
	}()
	type point struct{ x, y float32 }
	var pts []point
	for _, p := range s.paths {
		key := fmt.Sprintf("%x:%g:%v", p.stroke.ink, p.stroke.width, p.stroke.dash)
		pen := cache[key]
		if pen == 0 {
			pen = traceGPPen(p.stroke.ink, p.stroke.width*scale, p.stroke.dash, scale)
			if pen == 0 {
				return false
			}
			cache[key] = pen
		}
		if p.clip != nil {
			c := *p.clip
			l, t := x+int(math.Floor(c.l*scale)), y+int(math.Floor(c.t*scale))
			r, b := x+int(math.Ceil(c.r*scale)), y+int(math.Ceil(c.b*scale))
			traceGPClip.Call(g, uintptr(l), uintptr(t), uintptr(r-l+1), uintptr(b-t+1), 0)
		}
		for _, run := range p.runs {
			pts = pts[:0]
			for _, v := range run {
				pts = append(pts, point{float32(float64(x) + v.x*scale), float32(float64(y) + v.y*scale)})
			}
			if len(pts) == 1 {
				p := pts[0]
				pts[0] = point{p.x - 1, p.y}
				pts = append(pts, point{p.x + 1, p.y})
			}
			if len(pts) > 1 {
				traceGPLine.Call(g, pen, uintptr(unsafe.Pointer(&pts[0])), uintptr(len(pts)))
				runtime.KeepAlive(pts)
			}
		}
		if p.clip != nil {
			traceGPReset.Call(g)
		}
	}
	return true
}

func traceScientificTint(hdc uintptr, r RECT, ink uint32) {
	g := traceGPGraphics(hdc)
	if g != 0 {
		var b uintptr
		if st, _, _ := traceGPCreateBrush.Call(traceGPARGB(ink, 42), uintptr(unsafe.Pointer(&b))); st == 0 {
			traceGPFill.Call(g, b, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top))
			traceGPDeleteBrush.Call(b)
		}
		traceGPDelete.Call(g)
	}
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, uintptr(ink))
	if pen != 0 {
		old, _, _ := pSelectObject.Call(hdc, pen)
		drawLine(hdc, int(r.Left), int(r.Top), int(r.Right), int(r.Top))
		drawLine(hdc, int(r.Right), int(r.Top), int(r.Right), int(r.Bottom))
		drawLine(hdc, int(r.Right), int(r.Bottom), int(r.Left), int(r.Bottom))
		drawLine(hdc, int(r.Left), int(r.Bottom), int(r.Left), int(r.Top))
		pSelectObject.Call(hdc, old)
		pDeleteObject.Call(pen)
	}
}
