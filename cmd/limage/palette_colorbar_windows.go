//go:build windows

package main

import "encoding/base64"

// industrialPaletteIndex is appended after the historical 20 palettes.  The
// source colorbar.npy is a 165x3 float64 RGB table; it is stored here as a
// compact, reviewable RGB LUT so the application does not require Python or
// NumPy at runtime.  The LUT was linearly resampled to 256 entries and each
// channel rounded to the nearest byte.
const industrialPaletteIndex = 20

const industrialColorbarBase64 = "AgMDBAUFBgcHBwkJCQsLCw0NDQ8PDhEREBMTEhUVFBcXFRkZFxsbGR0dGx8fHCEhHiMjICUlIicnJCoqJSwsJy4uKTAwKzIyLDQ0LjY2MDg4Mjo6Mzw8NT4+N0BAOUJCO0REPEZGPkhIQEpKQkxMQ05ORVBQR1JSSVRUSlZWTFhYTlpaUFxcUV5eU2BgVWJiV2RkWWZmWmhoXGpqXmxsYG5uYXBwY3JyZXV1Z3d3aHl5ant7bH19bn9/cIGBcYODc4WFdYeHd4mJeIuLeo2NfI+PfpGRf5OTgZWVg5eXhZmZhpubiJ2dip+fjKGhjqOjj6Wlkaenk6mplaurlq2tmK+vmrGxnLOznbW1n7e3obm5o7u7pb29psDAqMLCqsTErMbGrcjIr8rKsczMs87OtNDQttLSuNTUutbWu9jYvdrav9zcwd7ew+DgxOLixuTkyObmyujoy+rqzezsz+7u0fDw0vLy1PT01vb22Pj42vr63Pz84P7+5P//6P//8f//9//++f789vzz7/no5fXa3vLR2O/I0uu/0em80ee60Oa50OS40OK2z+C1z96zz9yyz9uwztmvztetztWsztOqzdGpzdCozc6mzMylzMqjzMiizMagy8Wfy8Ody8Gcy7+ayr2ZyruYyrqWybiVybaTybSSybKQyLCPyK+NyK2MyKuKx6mJx6eIx6WGx6SFxqKDxqCCxp6AxZx/xZp9xZl8xZd6xJV5xJN4xJF2xI91w45zw4xyw4pwwohvwoZtwoRswoNqwYFpwX9owX1mwXtlwHljwHhiwHZgv3Rfv3Jdv3Bcv25avm1ZvmtYvmlWvmdVvWVTvWNSvWJQvGBPvF5NvFxMvFpKu1hJu1dIu1VGu1NFulFDuk9Cuk1Aukw/uUo9uUg8uUY6uEQ5uEI3uEE2uD81tz0ztzsytzkwtzcvtjYttjQstjIqtTAptS4ntSwmtSsltCkjtCcitCUgtCMfsyEdsyAcsx4ashwZshoXshgWshYVsRUTsRMSsREQsQ8P"

var industrialColorbarPalette = func() [256]rgb {
	var palette [256]rgb
	raw, err := base64.StdEncoding.DecodeString(industrialColorbarBase64)
	if err != nil || len(raw) != len(palette)*3 {
		panic("invalid embedded industrial colorbar")
	}
	for i := range palette {
		palette[i] = rgb{r: raw[i*3], g: raw[i*3+1], b: raw[i*3+2]}
	}
	return palette
}()

func paletteRGB(index int) [256]rgb {
	if index == industrialPaletteIndex {
		return industrialColorbarPalette
	}
	if index < 0 || index >= len(legacyAnchors) {
		index = 2
	}
	a := legacyAnchors[index]
	start, mid, end := tcolor(a[2]), tcolor(a[1]), tcolor(a[0])
	var palette [256]rgb
	for i := 0; i < 128; i++ {
		palette[i] = rgb{lerp(start.r, mid.r, i), lerp(start.g, mid.g, i), lerp(start.b, mid.b, i)}
		palette[128+i] = rgb{lerp(mid.r, end.r, i), lerp(mid.g, end.g, i), lerp(mid.b, end.b, i)}
	}
	return palette
}

func paletteIndexValid(index int) bool {
	return index >= 0 && index < len(paletteNames)
}
