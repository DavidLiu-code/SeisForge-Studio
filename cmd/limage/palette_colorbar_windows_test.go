//go:build windows

package main

import "testing"

func TestIndustrialColorbarPaletteIsAvailableAcrossRenderers(t *testing.T) {
	if industrialPaletteIndex != 20 || len(paletteNames) <= industrialPaletteIndex || paletteNames[industrialPaletteIndex] != "工业蓝白红" {
		t.Fatalf("industrial palette catalog entry missing: index=%d names=%v", industrialPaletteIndex, paletteNames)
	}
	if !paletteIndexValid(industrialPaletteIndex) || paletteIndexValid(industrialPaletteIndex+1) {
		t.Fatal("industrial palette validity bounds are incorrect")
	}
	palette := paletteRGB(industrialPaletteIndex)
	if palette[0] != (rgb{2, 3, 3}) || palette[255] != (rgb{177, 15, 15}) {
		t.Fatalf("embedded LUT endpoints changed: first=%+v last=%+v", palette[0], palette[255])
	}
	if palette[80] == palette[160] {
		t.Fatal("industrial LUT unexpectedly collapsed to a flat color")
	}
	if got := crookedPaletteBGRA([]byte{0, 128, 255}, industrialPaletteIndex); len(got) != 12 {
		t.Fatalf("industrial palette BGRA length=%d", len(got))
	}
	if got := pseudoPalette(industrialPaletteIndex); got[0].R != 2 || got[255].R != 177 {
		t.Fatalf("pseudo renderer did not use industrial LUT: first=%+v last=%+v", got[0], got[255])
	}
}
