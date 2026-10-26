//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

// An exclusive, read-only Windows open is an observable handle-lifetime
// check: it fails while segy.File owns a live read handle. Unlike Rename,
// this remains useful even when a reader permits FILE_SHARE_DELETE.
func compareResourceExclusiveRead(path string) error {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	return syscall.CloseHandle(h)
}

func compareResourceFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	headers := make([]testsegy.TraceHeader, 60)
	for i := range headers {
		headers[i] = testsegy.TraceHeader{
			SourceID: int32(100 + i/12), ReceiverID: int32(200 + i%12), CDP: int32(300 + i%6),
			Offset: int32(i%12*20 - 100), SourceX: int32(i / 12 * 40), SourceY: 0,
			ReceiverX: int32(i % 12 * 20), ReceiverY: 100, Units: 1,
			SampleCount: 32, SampleIntervalUS: 2000,
		}
	}
	if err := testsegy.Write(path, testsegy.Options{Rows: 5, Cols: 12, Samples: 32, TraceHeaders: headers}); err != nil {
		t.Fatal(err)
	}
	return path
}

// Trigger cancellation at the production BuildIndex entry, after its caller
// has opened the Reader. This avoids an arbitrary sleep or a pre-cancelled
// context that would never exercise the defer reader.Close() path.
type compareResourceCancelContext struct {
	context.Context
	cancel context.CancelFunc
	calls  int
	before func()
}

func (c *compareResourceCancelContext) Err() error {
	c.calls++
	if c.calls == 2 {
		c.before()
		c.cancel()
	}
	return c.Context.Err()
}

func TestPrestackCompareBProductionIndexReleasesRealReaders(t *testing.T) {
	aPath, bPath := compareResourceFixture(t, "A.sgy"), compareResourceFixture(t, "B.sgy")
	a, err := dataset.NewManager().Open(aPath)
	if err != nil {
		t.Fatal(err)
	}
	aReader, err := a.OpenReader()
	if err != nil {
		t.Fatal(err)
	}
	defer aReader.Close()
	if err := compareResourceExclusiveRead(aPath); err == nil {
		t.Fatal("exclusive-open probe failed to detect the live A Reader")
	}
	for _, detect := range []bool{false, true} {
		for cycle := 0; cycle < 8; cycle++ {
			r := &prestackCompareBResult{path: bPath, mapping: prestackcore.DefaultHeaderMapping(), detected: detect}
			buildPrestackCompareBIndex(context.Background(), r)
			if r.err != nil || r.dataset == nil || (!r.detection.NeedsConfirmation && r.index == nil) {
				t.Fatalf("B cycle %d detect=%v: index=%v err=%v", cycle, detect, r.index != nil, r.err)
			}
			if err := compareResourceExclusiveRead(bPath); err != nil {
				t.Fatalf("B cycle %d detect=%v retained a Reader: %v", cycle, detect, err)
			}
			if got, err := aReader.ReadSample(7, 3); err != nil || got != 703 {
				t.Fatalf("B work affected independent A Reader: value=%v err=%v", got, err)
			}
		}
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	observedOpen := false
	ctx := &compareResourceCancelContext{Context: base, cancel: cancel, before: func() {
		observedOpen = compareResourceExclusiveRead(bPath) != nil
	}}
	r := &prestackCompareBResult{path: bPath, mapping: prestackcore.DefaultHeaderMapping()}
	buildPrestackCompareBIndex(ctx, r)
	if !observedOpen || !errors.Is(r.err, context.Canceled) || r.index != nil {
		t.Fatalf("did not cancel an actually-open B Reader: opened=%v err=%v", observedOpen, r.err)
	}
	if err := compareResourceExclusiveRead(bPath); err != nil {
		t.Fatalf("cancelled B index retained Reader: %v", err)
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.sgy")
	if err := os.WriteFile(corrupt, []byte("not a SEG-Y"), 0600); err != nil {
		t.Fatal(err)
	}
	r = &prestackCompareBResult{path: corrupt, mapping: prestackcore.DefaultHeaderMapping()}
	buildPrestackCompareBIndex(context.Background(), r)
	if r.err == nil || r.dataset != nil || r.index != nil {
		t.Fatal("invalid B unexpectedly published dataset/index")
	}
	if err := compareResourceExclusiveRead(corrupt); err != nil {
		t.Fatalf("failed B validation retained Reader: %v", err)
	}
	if got, err := aReader.ReadSample(7, 3); err != nil || got != 703 {
		t.Fatalf("B cancellation/failure invalidated A Reader: %v / %v", got, err)
	}
}

func TestPrestackCompareRepeatedCloseCancelsIndexAndDropsPendingBuffers(t *testing.T) {
	savedState, savedUI, savedHwnd := prestackState, prestackUI, prestackHwnd
	prestackDeliveryMu.Lock()
	savedB, savedRender, savedDelivery := prestackPendingCompareB, prestackPendingCompareRender, prestackDeliveryCompareBGeneration
	prestackDeliveryMu.Unlock()
	t.Cleanup(func() {
		prestackState, prestackUI, prestackHwnd = savedState, savedUI, savedHwnd
		prestackDeliveryMu.Lock()
		prestackPendingCompareB, prestackPendingCompareRender, prestackDeliveryCompareBGeneration = savedB, savedRender, savedDelivery
		prestackDeliveryMu.Unlock()
	})
	prestackHwnd, prestackUI = 0, prestackControls{}
	a := &dataset.SeismicDataset{Path: "unchanged-A.sgy"}
	aIndex := &prestackcore.PrestackIndex{}
	bPath := compareResourceFixture(t, "repeated-B.sgy")
	for cycle := 0; cycle < 24; cycle++ {
		ctx, cancel := context.WithCancel(context.Background())
		renderCtx, renderCancel := context.WithCancel(context.Background())
		prestackState = prestackSession{
			page: 0, dataset: a, index: aIndex, mapping: prestackcore.DefaultHeaderMapping(),
			datasetGeneration: 11, mappingGeneration: 12, selectionGeneration: 13,
			compareBLoading: true, compareBCancel: cancel, compareRendering: true, compareRenderCancel: renderCancel,
			compareA: make([]byte, 4096), compareB: make([]byte, 4096), compareDelta: make([]byte, 4096),
			compareWidth: 32, compareHeight: 32,
			compareRows:        []prestackcore.CompareLogicalRow{{HasB: true, BTrace: 1}},
			compareDisplayRows: []prestackcore.CompareLogicalRow{{HasB: true, BTrace: 1}},
		}
		prestackDeliveryMu.Lock()
		prestackPendingCompareB = &prestackCompareBResult{dataset: &dataset.SeismicDataset{Path: bPath}, index: &prestackcore.PrestackIndex{}}
		prestackPendingCompareRender = &prestackCompareRenderResult{a: make([]byte, 4096), b: make([]byte, 4096), delta: make([]byte, 4096)}
		prestackDeliveryMu.Unlock()
		done := make(chan *prestackCompareBResult, 1)
		go func() {
			r := &prestackCompareBResult{path: bPath, mapping: prestackcore.DefaultHeaderMapping()}
			buildPrestackCompareBIndex(ctx, r)
			done <- r
		}()
		closePrestackCompareB(nil)
		select {
		case r := <-done:
			if r.err != nil && !errors.Is(r.err, context.Canceled) {
				t.Fatalf("cycle %d: unexpected worker error: %v", cycle, r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("cycle %d: cancelled production index work did not finish", cycle)
		}
		if err := compareResourceExclusiveRead(bPath); err != nil {
			t.Fatalf("cycle %d: completed/cancelled worker retained Reader: %v", cycle, err)
		}
		if ctx.Err() != context.Canceled || renderCtx.Err() != context.Canceled || prestackState.compareBCancel != nil || prestackState.compareRenderCancel != nil || prestackState.compareBLoading || prestackState.compareRendering {
			t.Fatalf("cycle %d: close retained an attached task", cycle)
		}
		prestackDeliveryMu.Lock()
		pending := prestackPendingCompareB != nil || prestackPendingCompareRender != nil
		prestackDeliveryMu.Unlock()
		if pending || len(prestackState.compareA) != 0 || len(prestackState.compareB) != 0 || len(prestackState.compareDelta) != 0 || len(prestackState.compareRows) != 0 || len(prestackState.compareDisplayRows) != 0 {
			t.Fatalf("cycle %d: close retained pending or displayed buffers", cycle)
		}
		if prestackState.dataset != a || prestackState.index != aIndex || prestackState.datasetGeneration != 11 || prestackState.mappingGeneration != 12 || prestackState.selectionGeneration != 13 {
			t.Fatalf("cycle %d: B close changed A identity", cycle)
		}
	}
}

// This exercises the actual raster and Ctrl-preview GDI routines on a real
// memory DC, without creating a HWND, sending input, or touching another app.
// It is not a substitute for resize/WM_PAINT testing in the live GUI.
func TestPrestackCompareOffscreenRasterAndCtrlPreviewReleaseGDI(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	savedState := prestackState
	defer func() { prestackState = savedState }()
	getGuiResources := syscall.NewLazyDLL("user32.dll").NewProc("GetGuiResources")
	getCurrentObject := syscall.NewLazyDLL("gdi32.dll").NewProc("GetCurrentObject")
	getTextColor := syscall.NewLazyDLL("gdi32.dll").NewProc("GetTextColor")
	getBkMode := syscall.NewLazyDLL("gdi32.dll").NewProc("GetBkMode")
	getStretchMode := syscall.NewLazyDLL("gdi32.dll").NewProc("GetStretchBltMode")
	getClipBox := syscall.NewLazyDLL("gdi32.dll").NewProc("GetClipBox")
	getPixel := syscall.NewLazyDLL("gdi32.dll").NewProc("GetPixel")
	flush := syscall.NewLazyDLL("gdi32.dll").NewProc("GdiFlush")
	resourceCount := func(flag uintptr) uintptr {
		n, _, _ := getGuiResources.Call(^uintptr(0), flag) // Current-process pseudo handle.
		return n
	}
	panels := [3]RECT{
		{Left: 8, Top: 8, Right: 208, Bottom: 258},
		{Left: 220, Top: 8, Right: 420, Bottom: 258},
		{Left: 432, Top: 8, Right: 632, Bottom: 258},
	}
	raster := prestackCompareRasterRect(panels[0])
	width, height := int(raster.Right-raster.Left), int(raster.Bottom-raster.Top)
	pixels := make([]byte, width*height*4)
	for i := range pixels {
		pixels[i] = 120
	}
	prestackState = prestackSession{
		page: 3, compareWidth: width, compareHeight: height, compareRenderGeneration: 123,
		compareA: pixels, compareB: pixels, compareDelta: pixels,
		compareBAxis: prestackcore.SampleAxisCompatibility{Compatible: true},
		compareDisplayRows: []prestackcore.CompareLogicalRow{
			{Classification: prestackcore.CompareRowMatched, HasA: true, HasB: true, ATrace: 7, BTrace: 11},
			{Classification: prestackcore.CompareRowMatched, HasA: true, HasB: true, ATrace: 4, BTrace: 8},
			{Classification: prestackcore.CompareRowMatched, HasA: true, HasB: true, ATrace: 2, BTrace: 5},
		},
		comparePickPreview: prestackComparePickPreview{Active: true, Panel: 0, Row: 1, Generation: 123},
	}
	type dcState struct {
		objects       [4]uintptr
		text, bk, blt uintptr
		clip          RECT
	}
	snapshot := func(dc uintptr) dcState {
		var s dcState
		for i, objectType := range []uintptr{1, 2, 6, 7} { // PEN, BRUSH, FONT, BITMAP.
			s.objects[i], _, _ = getCurrentObject.Call(dc, objectType)
		}
		s.text, _, _ = getTextColor.Call(dc)
		s.bk, _, _ = getBkMode.Call(dc)
		s.blt, _, _ = getStretchMode.Call(dc)
		getClipBox.Call(dc, uintptr(unsafe.Pointer(&s.clip)))
		return s
	}
	drawCycle := func(cycle int) {
		dc, _, err := pCreateCompatibleDC.Call(0)
		if dc == 0 {
			t.Fatalf("CreateCompatibleDC: %v", err)
		}
		defer func() {
			if ok, _, _ := pDeleteDC.Call(dc); ok == 0 {
				t.Errorf("cycle %d: DeleteDC failed", cycle)
			}
		}()
		bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: 640, Height: -300, Planes: 1, BitCount: 32, Compression: BI_RGB}}
		var bits uintptr
		bitmap, _, err := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, uintptr(unsafe.Pointer(&bits)), 0, 0)
		if bitmap == 0 || bits == 0 {
			t.Fatalf("CreateDIBSection: %v", err)
		}
		oldBitmap, _, _ := pSelectObject.Call(dc, bitmap)
		defer func() {
			pSelectObject.Call(dc, oldBitmap)
			if ok, _, _ := pDeleteObject.Call(bitmap); ok == 0 {
				t.Errorf("cycle %d: bitmap remained selected or could not be deleted", cycle)
			}
		}()
		white, _, _ := pGetStockObject.Call(WHITE_BRUSH)
		canvas := RECT{Right: 640, Bottom: 300}
		pFillRect.Call(dc, uintptr(unsafe.Pointer(&canvas)), white)
		pSetTextColor.Call(dc, rgbRef(17, 34, 51))
		pSetBkMode.Call(dc, 2)         // OPAQUE; preview temporarily uses TRANSPARENT.
		pSetStretchBltMode.Call(dc, 3) // COLORONCOLOR; raster temporarily uses HALFTONE.
		pIntersectClipRect.Call(dc, 2, 3, 638, 298)
		before := snapshot(dc)
		liveGDI := resourceCount(0)
		if liveGDI < 2 {
			t.Fatal("GetGuiResources did not observe the real DC/bitmap")
		}
		for _, panel := range panels {
			paintPrestackCompareRaster(dc, prestackCompareRasterRect(panel), pixels, width, height)
		}
		paintPrestackComparePickPreview(dc, panels)
		flush.Call()
		if after := snapshot(dc); after != before {
			t.Fatalf("cycle %d: raster/preview did not restore selected objects/text/clip/stretch state: before=%+v after=%+v", cycle, before, after)
		}
		if cycle >= 0 && resourceCount(0) != liveGDI {
			t.Fatalf("cycle %d: raster/preview leaked a GDI object: before=%d after=%d", cycle, liveGDI, resourceCount(0))
		}
		// Ensure the native drawing really ran; a count-only test could pass if
		// a readiness guard silently skipped every preview.
		for _, panel := range panels {
			r := prestackCompareRasterRect(panel)
			x := prestackComparePickX(1, 3, r)
			color, _, _ := getPixel.Call(dc, uintptr(x), uintptr(r.Top+40))
			if color != rgbRef(255, 180, 0) {
				t.Fatalf("cycle %d: Ctrl marker was not painted: color=%#x", cycle, color)
			}
		}
		if color, _, _ := getPixel.Call(dc, 3, 3); color != rgbRef(255, 255, 255) {
			t.Fatalf("cycle %d: drawing escaped scene rectangles", cycle)
		}
	}
	// Load the GDI/font subsystem before checking a stable baseline.
	for i := 0; i < 4; i++ {
		drawCycle(-1)
	}
	beforeGDI, beforeUSER := resourceCount(0), resourceCount(1)
	for i := 0; i < 64; i++ {
		drawCycle(i)
	}
	flush.Call()
	afterGDI, afterUSER := resourceCount(0), resourceCount(1)
	if afterGDI != beforeGDI || afterUSER != beforeUSER {
		t.Fatalf("offscreen drawing retained native resources: GDI %d→%d USER %d→%d", beforeGDI, afterGDI, beforeUSER, afterUSER)
	}
	t.Logf("64 native offscreen raster/preview cycles: GDI %d→%d, USER %d→%d", beforeGDI, afterGDI, beforeUSER, afterUSER)
}
