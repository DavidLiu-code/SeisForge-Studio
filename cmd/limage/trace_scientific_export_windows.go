//go:build windows

package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unsafe"
)

const traceScientificOutputPixels = 40_000_000

func (s traceScientificScene) raster(dpi int) (*image.RGBA, error) {
	if dpi <= 0 || dpi > 2400 || s.dpi <= 0 || s.width < 1 || s.height < 1 {
		return nil, fmt.Errorf("图像尺寸或导出 DPI 无效")
	}
	scale := float64(dpi) / float64(s.dpi)
	w, h := int(math.Round(float64(s.width)*scale)), int(math.Round(float64(s.height)*scale))
	if w < 1 || h < 1 || int64(w)*int64(h) > traceScientificOutputPixels {
		return nil, fmt.Errorf("图像尺寸为 %d × %d 像素，调整窗口尺寸或降低导出 DPI", w, h)
	}
	// Keep the temporary DC's calls on the same native thread whether the
	// caller is the window thread or a background export worker.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	dc, _, _ := pCreateCompatibleDC.Call(0)
	if dc == 0 {
		return nil, fmt.Errorf("无法创建图像画布")
	}
	defer pDeleteDC.Call(dc)
	bmi := BITMAPINFO{Header: BITMAPINFOHEADER{Size: uint32(unsafe.Sizeof(BITMAPINFOHEADER{})), Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32, Compression: BI_RGB}}
	var bits uintptr
	bmp, _, _ := pCreateDIBSection.Call(dc, uintptr(unsafe.Pointer(&bmi)), DIB_RGB_COLORS, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp != 0 {
		defer pDeleteObject.Call(bmp)
	}
	if bmp == 0 || bits == 0 {
		return nil, fmt.Errorf("无法创建图像位图")
	}
	old, _, _ := pSelectObject.Call(dc, bmp)
	if old == 0 || old == ^uintptr(0) {
		return nil, fmt.Errorf("无法选择图像位图")
	}
	defer pSelectObject.Call(dc, old)
	s.paintAt(dc, 0, 0, scale)
	traceScientificGdiFlush.Call()
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	src := unsafe.Slice((*byte)(unsafe.Pointer(bits)), w*h*4)
	for i := 0; i < w*h*4; i += 4 {
		rgba.Pix[i], rgba.Pix[i+1], rgba.Pix[i+2], rgba.Pix[i+3] = src[i+2], src[i+1], src[i], 255
	}
	return rgba, nil
}

var traceScientificGdiFlush = gdi32.NewProc("GdiFlush")
var traceScientificRegisterClipboard = user32.NewProc("RegisterClipboardFormatW")

func traceScientificPNG(w io.Writer, img image.Image, dpi int) error {
	if dpi < 1 || dpi > 2400 {
		return fmt.Errorf("导出 DPI 无效")
	}
	var p bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&p, img); err != nil {
		return err
	}
	stream := p.Bytes()
	// Emit standard PNG physical resolution metadata between IHDR and IDAT.
	// 8 byte signature + 12 byte chunk framing + 13 byte IHDR payload.
	const pos = 8 + 12 + 13
	if len(stream) < pos || string(stream[12:16]) != "IHDR" {
		return fmt.Errorf("PNG 编码未生成有效文件头")
	}
	metres := uint32(math.Round(float64(dpi) / 0.0254))
	chunk := make([]byte, 21)
	binary.BigEndian.PutUint32(chunk, 9)
	copy(chunk[4:], "pHYs")
	binary.BigEndian.PutUint32(chunk[8:], metres)
	binary.BigEndian.PutUint32(chunk[12:], metres)
	chunk[16] = 1 // unit = metre
	binary.BigEndian.PutUint32(chunk[17:], crc32.ChecksumIEEE(chunk[4:17]))
	for _, part := range [][]byte{stream[:pos], chunk, stream[pos:]} {
		if n, err := w.Write(part); err != nil {
			return err
		} else if n != len(part) {
			return io.ErrShortWrite
		}
	}
	return nil
}

func traceScientificPublish(path, extension string, save func(io.Writer) error) error {
	if !strings.EqualFold(filepath.Ext(path), extension) {
		return fmt.Errorf("图像文件名必须以 %s 结尾", extension)
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".seisforge-graph-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = save(f)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s traceScientificScene) writePNG(path string, dpi int) error {
	if err := s.checkDestination(path); err != nil {
		return err
	}
	img, err := s.raster(dpi)
	if err != nil {
		return err
	}
	return traceScientificPublish(path, ".png", func(w io.Writer) error { return traceScientificPNG(w, img, dpi) })
}

func traceScientificSVGNumber(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

func traceScientificSVGColor(v uint32) string {
	return fmt.Sprintf("#%02x%02x%02x", v&255, v>>8&255, v>>16&255)
}

func (s traceScientificScene) svg(w io.Writer) error {
	if s.width < 1 || s.height < 1 || s.dpi <= 0 {
		return fmt.Errorf("图像尺寸无效")
	}
	bw := bufio.NewWriter(w)
	n := traceScientificSVGNumber
	write := func(text string) { _, _ = bw.WriteString(text) }
	fmt.Fprintf(bw, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"%sin\" height=\"%sin\" viewBox=\"0 0 %d %d\">\n",
		n(float64(s.width)/float64(s.dpi)), n(float64(s.height)/float64(s.dpi)), s.width, s.height)
	write("<rect width=\"100%\" height=\"100%\" fill=\"white\"/>\n<defs>\n")
	for i, p := range s.paths {
		if p.clip != nil {
			c := p.clip
			fmt.Fprintf(bw, "<clipPath id=\"panel%d\"><rect x=\"%s\" y=\"%s\" width=\"%s\" height=\"%s\"/></clipPath>\n",
				i, n(c.l), n(c.t), n(c.width()), n(c.height()))
		}
	}
	write("</defs>\n")
	for i, p := range s.paths {
		write("<path fill=\"none\" stroke=\"" + traceScientificSVGColor(p.stroke.ink) + "\" stroke-linecap=\"butt\" stroke-linejoin=\"round\" stroke-width=\"" + n(p.stroke.width) + "\"")
		if len(p.stroke.dash) > 0 {
			dash := make([]string, len(p.stroke.dash))
			for j := range dash {
				dash[j] = n(p.stroke.dash[j])
			}
			write(" stroke-dasharray=\"" + strings.Join(dash, " ") + "\"")
		}
		if p.clip != nil {
			fmt.Fprintf(bw, " clip-path=\"url(#panel%d)\"", i)
		}
		write(" d=\"")
		for _, run := range p.runs {
			for j, v := range run {
				if j == 0 {
					if len(run) == 1 {
						write("M" + n(v.x-1) + " " + n(v.y) + "L" + n(v.x+1) + " " + n(v.y))
						break
					}
					write("M")
				} else {
					write("L")
				}
				write(n(v.x) + " " + n(v.y))
			}
		}
		write("\"/>\n")
	}
	for _, t := range s.text {
		anchor := [...]string{"start", "middle", "end"}[max(0, min(2, t.align+1))]
		fmt.Fprintf(bw, "<text x=\"%s\" y=\"%s\" font-family=\"Arial, Microsoft YaHei UI, sans-serif\" font-size=\"%s\" fill=\"%s\" text-anchor=\"%s\"",
			n(t.at.x), n(t.at.y), n(t.pt*float64(s.dpi)/72), traceScientificSVGColor(t.ink), anchor)
		if t.rotation != 0 {
			fmt.Fprintf(bw, " transform=\"rotate(%s %s %s)\"", n(-float64(t.rotation)/10), n(t.at.x), n(t.at.y))
		}
		write(">")
		if err := xml.EscapeText(bw, []byte(t.text)); err != nil {
			return err
		}
		write("</text>\n")
	}
	write("</svg>\n")
	return bw.Flush()
}

func (s traceScientificScene) writeSVG(path string) error {
	if err := s.checkDestination(path); err != nil {
		return err
	}
	return traceScientificPublish(path, ".svg", s.svg)
}

func (s traceScientificScene) checkDestination(path string) error {
	dst, _ := filepath.Abs(path)
	info, _ := os.Stat(path)
	for _, source := range s.readOnlySources {
		p, _ := filepath.Abs(source)
		if p != "" && strings.EqualFold(dst, p) {
			return fmt.Errorf("不能覆盖单道分析的源数据")
		}
		if info != nil {
			if src, err := os.Stat(source); err == nil && os.SameFile(info, src) {
				return fmt.Errorf("不能覆盖单道分析的源数据")
			}
		}
	}
	return nil
}

func traceScientificDIB(src *image.RGBA, dpi int) []byte {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	stride := (w*3 + 3) &^ 3
	// CF_DIB carries a BITMAPINFOHEADER (no BMP file header), followed by
	// bottom-up BGR rows. Also set pixels per metre for Office/fallback users.
	b := make([]byte, 40+stride*h)
	binary.LittleEndian.PutUint32(b, 40)
	binary.LittleEndian.PutUint32(b[4:], uint32(w))
	binary.LittleEndian.PutUint32(b[8:], uint32(h))
	binary.LittleEndian.PutUint16(b[12:], 1)
	binary.LittleEndian.PutUint16(b[14:], 24)
	binary.LittleEndian.PutUint32(b[20:], uint32(stride*h))
	ppm := uint32(math.Round(float64(dpi) / 0.0254))
	binary.LittleEndian.PutUint32(b[24:], ppm)
	binary.LittleEndian.PutUint32(b[28:], ppm)
	for y := 0; y < h; y++ {
		j := 40 + (h-1-y)*stride
		for x := 0; x < w; x++ {
			i := y*src.Stride + x*4
			b[j], b[j+1], b[j+2] = src.Pix[i+2], src.Pix[i+1], src.Pix[i]
			j += 3
		}
	}
	return b
}

func traceScientificClipboardFormat(format uintptr, payload []byte) bool {
	if format == 0 || len(payload) == 0 {
		return false
	}
	h, _, _ := traceGlobalAlloc.Call(traceGMEMMoveable, uintptr(len(payload)))
	if h == 0 {
		return false
	}
	p, _, _ := traceGlobalLock.Call(h)
	if p == 0 {
		traceGlobalFree.Call(h)
		return false
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(p)), len(payload)), payload)
	traceGlobalUnlock.Call(h)
	ok, _, _ := traceClipboardSet.Call(format, h)
	if ok == 0 {
		traceGlobalFree.Call(h)
		return false
	}
	return true // clipboard owns the memory
}

func (s traceScientificScene) copyPNG(hwnd uintptr, dpi int) error {
	img, err := s.raster(dpi)
	if err != nil {
		return err
	}
	var packed bytes.Buffer
	if err = traceScientificPNG(&packed, img, dpi); err != nil {
		return err
	}
	dib := traceScientificDIB(img, dpi)
	ok, _, _ := traceClipboardOpen.Call(hwnd)
	if ok == 0 {
		return fmt.Errorf("无法打开剪贴板")
	}
	defer traceClipboardClose.Call()
	if ok, _, _ = traceClipboardEmpty.Call(); ok == 0 {
		return fmt.Errorf("无法更新剪贴板")
	}
	pngFormat, _, _ := traceScientificRegisterClipboard.Call(uintptr(unsafe.Pointer(u16("PNG"))))
	pngOK := traceScientificClipboardFormat(pngFormat, packed.Bytes())
	dibOK := traceScientificClipboardFormat(8 /* CF_DIB */, dib)
	if !pngOK && !dibOK {
		return fmt.Errorf("无法复制图件")
	}
	return nil
}
