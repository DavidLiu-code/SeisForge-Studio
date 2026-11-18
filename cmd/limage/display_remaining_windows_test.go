//go:build windows

package main

import (
	"bytes"
	"math"
	"testing"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
)

func TestRemainingDisplayRasterColorsAreSharedAndInvalidDistinct(t *testing.T) {
	pal := remainingLegacyPalette(2)
	m := remainingMapping(-1, 1, false, pal)
	image := remainingRaster([]float64{-2, 0, 2, math.NaN()}, m)
	if len(image) != 16 || !bytes.Equal(image[:3], []byte{pal[255].b, pal[255].g, pal[255].r}) ||
		!bytes.Equal(image[8:11], []byte{pal[0].b, pal[0].g, pal[0].r}) || bytes.Equal(image[12:15], image[4:7]) {
		t.Fatalf("saturation / invalid pixels: %v", image)
	}
	want := []byte{image[8], image[4], image[0]}
	flipped := remainingReverseRaster(true, false, 4, 1, image)
	if !bytes.Equal([]byte{flipped[4], flipped[8], flipped[12]}, want) {
		t.Fatalf("raster reversal lost source coordinates: %v", flipped)
	}
}

func TestRemainingExactStatisticsSeparateMappedAndRaw(t *testing.T) {
	var st displaycore.Stats
	remainingCollectTrace(&st, []float64{-2, 0, 3, math.Inf(1)}, nil, true)
	if st.Raw.Count != 3 || st.Raw.Range.Min != -2 || st.Raw.Range.Max != 3 ||
		st.Mapping().Count != 3 || st.Mapping().Range.Max >= st.Raw.Range.Max {
		t.Fatalf("nonfinite/raw/mapped extrema: %+v", st)
	}
	var delta displaycore.Stats
	remainingCollectTrace(&delta, []float64{5, 2, 2, 1}, []float64{2, 2, 10, math.NaN()}, false)
	if delta.Raw.Count != 3 || delta.Raw.Range.Min != -8 || delta.Raw.Range.Max != 3 {
		t.Fatalf("difference statistics: %+v", delta)
	}
}

func TestRemainingCrookedMapMirroringPreservesWorldPick(t *testing.T) {
	m := makeCrookedMapTransform(RECT{Right: 500, Bottom: 250}, geometrycore.Bounds{XMin: 20, XMax: 70, YMin: 10, YMax: 20, HasXY: true})
	origX, origY := m.toPixel(30, 12)
	m.reverseX, m.reverseY = true, true
	mx, my := m.toPixel(30, 12)
	if mx <= origX || my >= origY {
		t.Fatalf("axes not mirrored: (%d,%d) instead of (%d,%d)", mx, my, origX, origY)
	}
	x, y := m.toWorld(mx, my)
	if math.Abs(x-30) > .1 || math.Abs(y-12) > .1 {
		t.Fatalf("pick moved world coordinate: %.3f,%.3f", x, y)
	}
}

func TestRemainingPseudoTexturePreservesMaskInPreview(t *testing.T) {
	source := &pseudo3dcore.Texture{Width: 4, Height: 2, Indices: []byte{1, 2, 3, 4, 5, 6, 7, 8}, Invalid: []bool{false, false, true, false, false, false, false, false}}
	preview := pseudoTextureWindow2D(source, .5, 1, 0, 1, pseudo3dcore.TextureSize{Width: 2, Height: 2})
	if preview == nil || len(preview.Invalid) != 4 || !preview.Invalid[0] || preview.Invalid[1] {
		t.Fatalf("invalid texture sample lost during viewport clipping: %+v", preview)
	}
}
