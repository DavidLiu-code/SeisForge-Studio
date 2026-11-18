//go:build windows

package main

import displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"

// Shared adapters for display sources that use the legacy indexed palette.
func remainingLegacyPalette(index int) [256]rgb {
	if index < 0 || index >= len(legacyAnchors) {
		index = 2
	}
	a := legacyAnchors[index]
	x, y, z := tcolor(a[2]), tcolor(a[1]), tcolor(a[0])
	var out [256]rgb
	for i := 0; i < 128; i++ {
		out[i] = rgb{lerp(x.r, y.r, i), lerp(x.g, y.g, i), lerp(x.b, y.b, i)}
		out[i+128] = rgb{lerp(y.r, z.r, i), lerp(y.g, z.g, i), lerp(y.b, z.b, i)}
	}
	return out
}

func remainingColorHidden(owner uintptr, key string) bool {
	return !displayColorbarReserved(owner, key)
}

func remainingAxis(id, title, unit string, data, view displaycore.Range, down bool) displaycore.Axis {
	return displaycore.Axis{ID: id, Title: title, Unit: unit, Data: data, View: view, Down: down}
}

func remainingReverseRaster(x, y bool, width, height int, dst []byte) []byte {
	if (!x && !y) || len(dst) != 4*width*height {
		return dst
	}
	out := make([]byte, len(dst))
	for j := 0; j < height; j++ {
		for i := 0; i < width; i++ {
			jj, ii := j, i
			if x {
				ii = width - i - 1
			}
			if y {
				jj = height - j - 1
			}
			a, b := 4*(j*width+i), 4*(jj*width+ii)
			copy(out[a:a+4], dst[b:b+4])
		}
	}
	return out
}
