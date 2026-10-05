package segy

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"testing"
)

func TestIBM(t *testing.T) {
	if v := ibmFloat(0x41100000); v < 0.9999 || v > 1.0001 {
		t.Fatalf("IBM 1.0 decoded as %g", v)
	}
}
func TestDistHeader(t *testing.T) {
	h := make([]byte, 400)
	h[16] = 0x07
	h[17] = 0xd0
	h[20] = 0x03
	h[21] = 0xe8
	h[24] = 0
	h[25] = 5
	e, dt, ns, fc, bps, ok := detectHeader(h)
	if !ok || e != Big || dt != 2000 || ns != 1000 || fc != 5 || bps != 4 {
		t.Fatalf("bad detect %v %d %d %d %d %v", e, dt, ns, fc, bps, ok)
	}
}

func TestOpenAndRenderSynthetic(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/synthetic.sgy"
	const ns = 64
	const ntr = 20
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 3600)
	binary.BigEndian.PutUint16(header[3216:3218], 2000)
	binary.BigEndian.PutUint16(header[3220:3222], ns)
	binary.BigEndian.PutUint16(header[3224:3226], 5)
	if _, err = f.Write(header); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 240+ns*4)
	for tr := 0; tr < ntr; tr++ {
		for i := 0; i < ns; i++ {
			v := float32(math.Sin(float64(i)*0.2 + float64(tr)*0.15))
			binary.BigEndian.PutUint32(raw[240+i*4:244+i*4], math.Float32bits(v))
		}
		if _, err = f.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Info.TraceCount != ntr || s.Info.SamplesPerTrace != ns || s.Info.FormatCode != 5 {
		t.Fatalf("bad info: %+v", s.Info)
	}
	p, err := s.Render(100, 80, true, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 8000 {
		t.Fatalf("bad image size %d", len(p))
	}
	// The first-load renderer is multi-threaded. Verify that worker count does
	// not change the image and that progress reaches completion.
	var progressDone, progressTotal int
	o := RenderOptions{Width: 100, Height: 80, AGC: true, ClipPercent: 99, TraceStart: 0, TraceEnd: -1, TraceStep: 1, SampleStart: 0, SampleEnd: -1, Workers: 1}
	p1, _, err := s.RenderWithOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	o.Workers = 4
	o.Progress = func(done, total int) { progressDone, progressTotal = done, total }
	p4, _, err := s.RenderWithOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p1, p4) {
		t.Fatal("multi-thread render differs from single-thread render")
	}
	if progressTotal != 100 || progressDone != 100 {
		t.Fatalf("progress did not finish: %d/%d", progressDone, progressTotal)
	}
}

func TestSparseOver4GBUses64BitOffsets(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/large_sparse.sgy"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 3600)
	binary.BigEndian.PutUint16(header[3216:3218], 2000)
	binary.BigEndian.PutUint16(header[3220:3222], 1)
	binary.BigEndian.PutUint16(header[3224:3226], 5)
	if _, err = f.Write(header); err != nil {
		t.Fatal(err)
	}
	target := int64(5)*1024*1024*1024 + 12345
	if err = f.Truncate(target); err != nil {
		t.Skipf("sparse >4GB file unsupported by test filesystem: %v", err)
	}
	f.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Info.FileSize <= int64(4)*1024*1024*1024 {
		t.Fatalf("expected >4GB, got %d", s.Info.FileSize)
	}
	if s.Info.TraceCount <= 0 {
		t.Fatal("no traces computed")
	}
}

func TestWindowedTraceIOAndTraceStep(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/windowed.sgy"
	const ns = 32
	const ntr = 12
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 3600)
	binary.BigEndian.PutUint16(header[3216:3218], 2000)
	binary.BigEndian.PutUint16(header[3220:3222], ns)
	binary.BigEndian.PutUint16(header[3224:3226], 5)
	if _, err = f.Write(header); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 240+ns*4)
	for tr := 0; tr < ntr; tr++ {
		for i := 0; i < ns; i++ {
			v := float32(tr*1000 + i)
			binary.BigEndian.PutUint32(raw[240+i*4:244+i*4], math.Float32bits(v))
		}
		if _, err = f.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w, err := s.readTraceWindow(5, 7, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 5 || w[0] != 5007 || w[4] != 5011 {
		t.Fatalf("bad window: %v", w)
	}
	_, st, err := s.RenderWithOptions(RenderOptions{
		Width: 40, Height: 20, ClipPercent: 99,
		TraceStart: 2, TraceEnd: 10, TraceStep: 3,
		SampleStart: 4, SampleEnd: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Selected traces are 2, 5, 8; trace 11 would exceed TraceEnd=10.
	if st.TraceStart != 2 || st.TraceEnd != 8 || st.TraceStep != 3 {
		t.Fatalf("bad stepped trace stats: %+v", st)
	}
	if st.SampleStart != 4 || st.SampleEnd != 12 {
		t.Fatalf("bad sample window stats: %+v", st)
	}
}

func TestLegacyGainPercentileTrim(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/gain.sgy"
	const ns = 100
	const ntr = 2
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 3600)
	binary.BigEndian.PutUint16(header[3216:3218], 2000)
	binary.BigEndian.PutUint16(header[3220:3222], ns)
	binary.BigEndian.PutUint16(header[3224:3226], 5)
	if _, err = f.Write(header); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 240+ns*4)
	for tr := 0; tr < ntr; tr++ {
		for i := 0; i < ns; i++ {
			v := float32(i - 50)
			binary.BigEndian.PutUint32(raw[240+i*4:244+i*4], math.Float32bits(v))
		}
		if _, err = f.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, st0, err := s.RenderWithOptions(RenderOptions{Width: 20, Height: 100, GainPercent: 0, TraceStart: 0, TraceEnd: 1, SampleStart: 0, SampleEnd: ns - 1})
	if err != nil {
		t.Fatal(err)
	}
	_, st10, err := s.RenderWithOptions(RenderOptions{Width: 20, Height: 100, GainPercent: 10, TraceStart: 0, TraceEnd: 1, SampleStart: 0, SampleEnd: ns - 1})
	if err != nil {
		t.Fatal(err)
	}
	if !(st10.MapMin > st0.MapMin && st10.MapMax < st0.MapMax) {
		t.Fatalf("gain should trim both tails: gain0=%g..%g gain10=%g..%g", st0.MapMin, st0.MapMax, st10.MapMin, st10.MapMax)
	}
	if math.Abs(st10.MapMin-(-40)) > 1.1 || math.Abs(st10.MapMax-40) > 1.1 {
		t.Fatalf("unexpected legacy percentile bounds: %g..%g", st10.MapMin, st10.MapMax)
	}
}

func TestPoststackGeometryCacheAndTimeSliceSlab(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir+"/cache")
	path := dir + "/cube3d.sgy"
	const ns = 24
	const ni = 4
	const nx = 5
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 3600)
	binary.BigEndian.PutUint16(header[3216:3218], 2000)
	binary.BigEndian.PutUint16(header[3220:3222], ns)
	binary.BigEndian.PutUint16(header[3224:3226], 5)
	if _, err = f.Write(header); err != nil {
		t.Fatal(err)
	}
	for ii := 0; ii < ni; ii++ {
		for xx := 0; xx < nx; xx++ {
			raw := make([]byte, 240+ns*4)
			il := int32(100 + ii*2)
			xl := int32(200 + xx*3)
			binary.BigEndian.PutUint32(raw[188:192], uint32(il))
			binary.BigEndian.PutUint32(raw[192:196], uint32(xl))
			tr := ii*nx + xx
			for s := 0; s < ns; s++ {
				v := float32(tr*1000 + s)
				binary.BigEndian.PutUint32(raw[240+s*4:244+s*4], math.Float32bits(v))
			}
			if _, err = f.Write(raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g, st, err := s.BuildGeometryIndexCached(189, 193, 4)
	if err != nil {
		t.Fatal(err)
	}
	if st.FromCache {
		t.Fatal("first geometry build unexpectedly came from cache")
	}
	if !g.Poststack || g.Rows() != ni || g.Cols() != nx || g.ValidTraceCount != ni*nx {
		t.Fatalf("bad geometry: post=%v rows=%d cols=%d valid=%d", g.Poststack, g.Rows(), g.Cols(), g.ValidTraceCount)
	}
	g2, st2, err := s.BuildGeometryIndexCached(189, 193, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.FromCache || g2.Rows() != ni || g2.Cols() != nx {
		t.Fatalf("geometry cache not reused: %+v", st2)
	}

	c := NewTimeSliceCache(s, g2)
	vals, bst, err := c.GetSlice(7)
	if err != nil {
		t.Fatal(err)
	}
	if bst.CacheHit {
		t.Fatal("first slab access unexpectedly hit RAM cache")
	}
	if len(vals) != ni*nx || vals[6] != 6007 {
		t.Fatalf("bad time slice values: len=%d v6=%g", len(vals), vals[6])
	}
	vals2, bst2, err := c.GetSlice(8)
	if err != nil {
		t.Fatal(err)
	}
	if !bst2.CacheHit || vals2[6] != 6008 {
		t.Fatalf("neighboring slice should hit slab cache: hit=%v v6=%g", bst2.CacheHit, vals2[6])
	}

	raster, mask, err := g2.RasterizeTimeSlice(vals, nx, ni, g2.Bounds())
	if err != nil {
		t.Fatal(err)
	}
	for i, ok := range mask {
		if !ok {
			t.Fatalf("unexpected missing raster cell %d", i)
		}
	}
	if raster[0] != 7 || raster[len(raster)-1] != float32((ni*nx-1)*1000+7) {
		t.Fatalf("unexpected raster corners: %g %g", raster[0], raster[len(raster)-1])
	}

	// Inline/Crossline comparison support: geometry lines must be ordered by
	// the other spatial coordinate and arbitrary trace lists must render.
	trIL, coordsIL, actualIL, err := g2.LineTraceNumbers(0, 102)
	if err != nil || actualIL != 102 || len(trIL) != nx || len(coordsIL) != nx {
		t.Fatalf("bad inline line: actual=%d n=%d coords=%d err=%v", actualIL, len(trIL), len(coordsIL), err)
	}
	for i := 1; i < len(coordsIL); i++ {
		if coordsIL[i] <= coordsIL[i-1] {
			t.Fatal("inline line is not ordered by crossline")
		}
	}
	pix, stLine, err := s.RenderTraceIndices(trIL, RenderOptions{Width: nx, Height: ns, GainPercent: 0, SampleStart: 0, SampleEnd: ns - 1})
	if err != nil || len(pix) != nx*ns || stLine.SampleEnd != ns-1 {
		t.Fatalf("line render failed: len=%d stats=%+v err=%v", len(pix), stLine, err)
	}

	trXL, coordsXL, actualXL, err := g2.LineTraceNumbers(1, 206)
	if err != nil || actualXL != 206 || len(trXL) != ni || len(coordsXL) != ni {
		t.Fatalf("bad crossline line: actual=%d n=%d coords=%d err=%v", actualXL, len(trXL), len(coordsXL), err)
	}
}

func TestDetectGeometryBytesStandardAndCustom(t *testing.T) {
	makeCube := func(path string, ilByte, xlByte int) {
		const ns = 8
		const ni = 8
		const nx = 10
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		header := make([]byte, 3600)
		binary.BigEndian.PutUint16(header[3216:3218], 2000)
		binary.BigEndian.PutUint16(header[3220:3222], ns)
		binary.BigEndian.PutUint16(header[3224:3226], 5)
		if _, err = f.Write(header); err != nil {
			t.Fatal(err)
		}
		for ii := 0; ii < ni; ii++ {
			for xx := 0; xx < nx; xx++ {
				raw := make([]byte, 240+ns*4)
				binary.BigEndian.PutUint32(raw[ilByte-1:ilByte+3], uint32(int32(1000+ii)))
				binary.BigEndian.PutUint32(raw[xlByte-1:xlByte+3], uint32(int32(2000+xx)))
				// Add realistic distractors: trace sequence and CDP/X coordinates.
				tr := ii*nx + xx
				binary.BigEndian.PutUint32(raw[0:4], uint32(tr+1))
				binary.BigEndian.PutUint32(raw[20:24], uint32(50000+tr))
				binary.BigEndian.PutUint32(raw[180:184], uint32(400000+xx*25+ii*3))
				binary.BigEndian.PutUint32(raw[184:188], uint32(6000000+ii*25+xx*2))
				for s := 0; s < ns; s++ {
					binary.BigEndian.PutUint32(raw[240+s*4:244+s*4], math.Float32bits(float32(tr+s)))
				}
				if _, err = f.Write(raw); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		il, xl int
	}{
		{"standard", 189, 193}, {"custom", 101, 137},
	} {
		path := t.TempDir() + "/" + tc.name + ".sgy"
		makeCube(path, tc.il, tc.xl)
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.DetectGeometryBytes(2048)
		s.Close()
		if err != nil {
			t.Fatal(err)
		}
		if r.InlineByte != tc.il || r.CrosslineByte != tc.xl {
			t.Fatalf("%s: detected %d/%d score %.1f confidence %.2f, want %d/%d; candidates=%+v", tc.name, r.InlineByte, r.CrosslineByte, r.Score, r.Confidence, tc.il, tc.xl, r.Candidates)
		}
		if r.Score < 65 {
			t.Fatalf("%s: score too low %.1f", tc.name, r.Score)
		}
	}
}

func TestTimeSliceCacheKeepsCurrentAndBothNeighbors(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/three_slabs.sgy"
	const ns = 128
	const ntr = 4
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 3600)
	binary.BigEndian.PutUint16(header[3216:3218], 2000)
	binary.BigEndian.PutUint16(header[3220:3222], ns)
	binary.BigEndian.PutUint16(header[3224:3226], 5)
	if _, err = f.Write(header); err != nil {
		t.Fatal(err)
	}
	for tr := 0; tr < ntr; tr++ {
		raw := make([]byte, 240+ns*4)
		binary.BigEndian.PutUint32(raw[188:192], uint32(100+tr/2))
		binary.BigEndian.PutUint32(raw[192:196], uint32(200+tr%2))
		for s := 0; s < ns; s++ {
			binary.BigEndian.PutUint32(raw[240+s*4:244+s*4], math.Float32bits(float32(tr*1000+s)))
		}
		if _, err = f.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	g, _, err := s.BuildGeometryIndexCached(189, 193, 0)
	if err != nil {
		t.Fatal(err)
	}
	c := NewTimeSliceCache(s, g)
	if c.BlockSamples() != 32 {
		t.Fatalf("expected 32-sample slabs for small test volume, got %d", c.BlockSamples())
	}
	// Current block = 64..95; then explicitly warm previous and next. The
	// three-block LRU must retain the current block so a palette/gain remap can
	// still retrieve the active time slice after bidirectional prefetch.
	for _, sample := range []int{70, 38, 102} {
		if _, _, err := c.GetSlice(sample); err != nil {
			t.Fatal(err)
		}
	}
	v, ok := c.GetSliceCached(70)
	if !ok || len(v) != ntr || v[2] != 2070 {
		t.Fatalf("active slab was evicted after warming both neighbors: ok=%v len=%d", ok, len(v))
	}
}

func TestRenderTraceDifferencePairs(t *testing.T) {
	dir := t.TempDir()
	makeFile := func(path string, bias float32) {
		const ns = 32
		const ntr = 5
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		header := make([]byte, 3600)
		binary.BigEndian.PutUint16(header[3216:3218], 2000)
		binary.BigEndian.PutUint16(header[3220:3222], ns)
		binary.BigEndian.PutUint16(header[3224:3226], 5)
		if _, err = f.Write(header); err != nil {
			t.Fatal(err)
		}
		for tr := 0; tr < ntr; tr++ {
			raw := make([]byte, 240+ns*4)
			for s := 0; s < ns; s++ {
				v := float32(math.Sin(float64(s)*0.25)+float64(tr)*0.1) + bias
				binary.BigEndian.PutUint32(raw[240+s*4:244+s*4], math.Float32bits(v))
			}
			if _, err = f.Write(raw); err != nil {
				t.Fatal(err)
			}
		}
		f.Close()
	}
	pa, pb := dir+"/a.sgy", dir+"/b.sgy"
	makeFile(pa, 0)
	makeFile(pb, 0.25)
	a, err := Open(pa)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(pb)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tr := []int64{0, 1, 2, 3, 4}
	pix, st, err := RenderTraceDifferencePairs(a, b, tr, tr, RenderOptions{Width: 40, Height: 32, GainPercent: 0, SampleStart: 0, SampleEnd: 31})
	if err != nil {
		t.Fatal(err)
	}
	if len(pix) != 40*32 {
		t.Fatalf("bad residual image size: %d", len(pix))
	}
	if math.Abs(st.ObservedMin+0.25) > 1e-5 || math.Abs(st.ObservedMax+0.25) > 1e-5 {
		t.Fatalf("expected A-B=-0.25, got observed %g..%g", st.ObservedMin, st.ObservedMax)
	}
	if math.Abs(st.MapMin+st.MapMax) > 1e-9 {
		t.Fatalf("residual display range must be symmetric: %g..%g", st.MapMin, st.MapMax)
	}
}

func TestRenderTraceDifferencePairsKeepsLegacySampleCountSemantics(t *testing.T) {
	dir := t.TempDir()
	makeFile := func(path string, samples int) {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		header := make([]byte, 3600)
		binary.BigEndian.PutUint16(header[3216:3218], 2000)
		binary.BigEndian.PutUint16(header[3220:3222], uint16(samples))
		binary.BigEndian.PutUint16(header[3224:3226], 5)
		if _, err := f.Write(header); err != nil {
			t.Fatal(err)
		}
		raw := make([]byte, 240+samples*4)
		for i := 0; i < samples; i++ {
			binary.BigEndian.PutUint32(raw[240+i*4:244+i*4], math.Float32bits(float32(i)))
		}
		if _, err := f.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	pa, pb := dir+"/a.sgy", dir+"/b.sgy"
	makeFile(pa, 4)
	makeFile(pb, 5)
	a, err := Open(pa)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(pb)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, _, err := RenderTraceDifferencePairs(a, b, []int64{0}, []int64{0}, RenderOptions{Width: 8, Height: 8, SampleStart: 0, SampleEnd: 3}); err != nil {
		t.Fatalf("legacy renderer changed sample-count behavior: %v", err)
	}
	if _, _, err := RenderTraceDifferencePairsStrict(a, b, []int64{0}, []int64{0}, RenderOptions{Width: 8, Height: 8, SampleStart: 0, SampleEnd: 3}); err == nil {
		t.Fatal("strict renderer accepted sample-count mismatch")
	}
}

func TestGeometryFastRegularPathAndIrregularFallback(t *testing.T) {
	makeCube := func(path string, missing bool) {
		const ns = 12
		const ni = 6
		const nx = 9
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		header := make([]byte, 3600)
		binary.BigEndian.PutUint16(header[3216:3218], 2000)
		binary.BigEndian.PutUint16(header[3220:3222], ns)
		binary.BigEndian.PutUint16(header[3224:3226], 5)
		if _, err = f.Write(header); err != nil {
			t.Fatal(err)
		}
		for ii := 0; ii < ni; ii++ {
			for xx := 0; xx < nx; xx++ {
				if missing && ii == 2 && xx == 4 {
					continue
				}
				raw := make([]byte, 240+ns*4)
				binary.BigEndian.PutUint32(raw[188:192], uint32(1000+ii))
				binary.BigEndian.PutUint32(raw[192:196], uint32(2000+xx))
				if _, err = f.Write(raw); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name              string
		missing, wantFast bool
	}{
		{"regular", false, true}, {"missing-bin", true, false},
	} {
		dir := t.TempDir()
		path := dir + "/" + tc.name + ".sgy"
		makeCube(path, tc.missing)
		t.Setenv("XDG_CACHE_HOME", dir+"/cache")
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		g, st, err := s.BuildGeometryIndexCached(189, 193, 4)
		s.Close()
		if err != nil {
			t.Fatal(err)
		}
		if st.FastRegular != tc.wantFast {
			t.Fatalf("%s FastRegular=%v want %v stats=%+v", tc.name, st.FastRegular, tc.wantFast, st)
		}
		if g.Rows() != 6 || g.Cols() != 9 {
			t.Fatalf("%s rows/cols=%d/%d", tc.name, g.Rows(), g.Cols())
		}
		if tc.missing && g.ValidTraceCount != 53 {
			t.Fatalf("missing-bin valid=%d", g.ValidTraceCount)
		}
		if !tc.missing && st.HeaderReads > 160 {
			t.Fatalf("regular fast path touched too many headers: %d", st.HeaderReads)
		}
	}
}
