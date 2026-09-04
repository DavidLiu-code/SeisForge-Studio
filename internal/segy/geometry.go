package segy

import (
	"crypto/sha256"
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// GeometryIndex is a compact post-stack 3-D geometry index built from SEG-Y
// trace headers. Header byte locations are 1-based, matching SEG-Y manuals.
// The default Rev-1 locations are Inline=189 and Crossline=193.
type GeometryIndex struct {
	SourcePath      string
	FileSize        int64
	ModTimeUnixNano int64
	TraceCount      int64
	InlineByte      int
	CrosslineByte   int

	InlineValues    []int32
	CrosslineValues []int32
	TraceNumbers    []int64 // zero-based SEG-Y trace indices for valid geometry traces
	RowOfTrace      []int32 // rank in InlineValues
	ColOfTrace      []int32 // rank in CrosslineValues

	ValidTraceCount int64
	DuplicateCount  int64
	Poststack       bool

	denseGrid []int32
	prepOnce  sync.Once
}

type GeometryBuildStats struct {
	FromCache   bool
	CachePath   string
	Duration    time.Duration
	Workers     int
	FastRegular bool // geometry was inferred/validated from a tiny header sample
	HeaderReads int  // approximate headers touched by the fast path
}

type geometryCacheFile struct {
	Magic   string
	Version int
	Index   GeometryIndex
}

const geometryCacheMagic = "LIMAGE-GEOMETRY-INDEX"
const geometryCacheVersion = 1

// geometryMemoryCache makes an index immediately reusable inside the same
// Limage process while its persistent .lidx file is being serialized in the
// background. This keeps the fast regular-grid path non-blocking without
// making a second A/B/体 request rebuild the same index.
var geometryMemoryCache sync.Map // cache path -> *GeometryIndex

func traceI32(b []byte, e Endian) int32 {
	return int32(u32(b, e))
}

func geometryCachePath(path string, st os.FileInfo, inlineByte, crosslineByte int) (string, error) {
	d, err := os.UserCacheDir()
	if err != nil || d == "" {
		d = os.TempDir()
	}
	d = filepath.Join(d, "Limage", "geometry")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(path)
	key := fmt.Sprintf("%s|%d|%d|%d|%d", abs, st.Size(), st.ModTime().UnixNano(), inlineByte, crosslineByte)
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(d, fmt.Sprintf("%x.lidx", sum[:16])), nil
}

func loadGeometryCache(path string, st os.FileInfo, inlineByte, crosslineByte int) (*GeometryIndex, string, bool) {
	cp, err := geometryCachePath(path, st, inlineByte, crosslineByte)
	if err != nil {
		return nil, "", false
	}
	if v, ok := geometryMemoryCache.Load(cp); ok {
		if idx, ok2 := v.(*GeometryIndex); ok2 && idx != nil && idx.FileSize == st.Size() && idx.ModTimeUnixNano == st.ModTime().UnixNano() && idx.InlineByte == inlineByte && idx.CrosslineByte == crosslineByte {
			idx.prepare()
			return idx, cp, true
		}
	}
	f, err := os.Open(cp)
	if err != nil {
		return nil, cp, false
	}
	defer f.Close()
	var c geometryCacheFile
	if err := gob.NewDecoder(f).Decode(&c); err != nil || c.Magic != geometryCacheMagic || c.Version != geometryCacheVersion {
		return nil, cp, false
	}
	idx := c.Index
	if idx.FileSize != st.Size() || idx.ModTimeUnixNano != st.ModTime().UnixNano() || idx.TraceCount <= 0 || idx.InlineByte != inlineByte || idx.CrosslineByte != crosslineByte {
		return nil, cp, false
	}
	idx.prepare()
	geometryMemoryCache.Store(cp, &idx)
	return &idx, cp, true
}

func saveGeometryCache(cp string, idx *GeometryIndex) {
	if cp == "" || idx == nil {
		return
	}
	geometryMemoryCache.Store(cp, idx)
	tmp := cp + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	encErr := gob.NewEncoder(f).Encode(geometryCacheFile{Magic: geometryCacheMagic, Version: geometryCacheVersion, Index: *idx})
	closeErr := f.Close()
	if encErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = os.Rename(tmp, cp)
}

// readGeometryPair reads only the two requested 4-byte geometry words from a
// trace header. The whole 240-byte header is fetched in one ReadAt so it remains
// compatible with ordinary buffered files on Windows.
func (s *File) readGeometryPair(trace int64, inlineByte, crosslineByte int, reads *int) (int32, int32, error) {
	if trace < 0 || trace >= s.Info.TraceCount {
		return 0, 0, errors.New("trace index out of range")
	}
	var hdr [240]byte
	off := s.Info.DataStart + trace*s.Info.TraceBytes
	if _, err := s.f.ReadAt(hdr[:], off); err != nil {
		return 0, 0, err
	}
	if reads != nil {
		*reads = *reads + 1
	}
	il := traceI32(hdr[inlineByte-1:inlineByte+3], s.Info.Endian)
	xl := traceI32(hdr[crosslineByte-1:crosslineByte+3], s.Info.Endian)
	return il, xl, nil
}

// tryBuildRegularGeometry recognizes the overwhelmingly common post-stack
// layout where one grid axis varies trace-by-trace and the other changes only
// at line boundaries. Instead of reading every trace header, it locates the
// first line boundary with exponential+binary search, infers the two grid
// increments, then validates sparse checkpoints across the entire file.
//
// A validated regular volume can therefore build a million-trace geometry
// index after only O(log(line length) + checkpoints) header reads. Irregular,
// missing-bin, prestack and exotic-order volumes simply return ok=false and
// fall back to the exhaustive scanner below.
func (s *File) tryBuildRegularGeometry(inlineByte, crosslineByte int, progress func(done, total int)) (*GeometryIndex, int, bool, error) {
	n := s.Info.TraceCount
	if n < 4 {
		return nil, 0, false, nil
	}
	reads := 0
	read := func(tr int64) (int32, int32, error) {
		return s.readGeometryPair(tr, inlineByte, crosslineByte, &reads)
	}
	il0, xl0, err := read(0)
	if err != nil {
		return nil, reads, false, err
	}
	if il0 == 0 && xl0 == 0 {
		return nil, reads, false, nil
	}

	// Find an adjacent trace where exactly one coordinate changes. The first
	// few traces can occasionally contain duplicates, so tolerate a short run.
	var il1, xl1 int32
	var firstChange int64 = -1
	for tr := int64(1); tr < n && tr <= 64; tr++ {
		il, xl, e := read(tr)
		if e != nil {
			return nil, reads, false, e
		}
		ci, cx := il != il0, xl != xl0
		if ci != cx { // exactly one axis changed
			il1, xl1, firstChange = il, xl, tr
			break
		}
		if ci && cx { // both moved immediately: not a simple row-major cube
			return nil, reads, false, nil
		}
	}
	if firstChange < 0 || firstChange != 1 {
		// Repeated bins at the start are not a clean one-trace-per-bin poststack grid.
		return nil, reads, false, nil
	}
	fastIsXL := il1 == il0 && xl1 != xl0
	fastIsIL := xl1 == xl0 && il1 != il0
	if !fastIsXL && !fastIsIL {
		return nil, reads, false, nil
	}
	fast0, slow0 := il0, xl0
	fastStep := il1 - il0
	if fastIsXL {
		fast0, slow0 = xl0, il0
		fastStep = xl1 - xl0
	}
	if fastStep == 0 {
		return nil, reads, false, nil
	}

	// Locate first trace whose slow coordinate differs from trace 0.
	slowAt := func(tr int64) (int32, error) {
		il, xl, e := read(tr)
		if e != nil {
			return 0, e
		}
		if fastIsXL {
			return il, nil
		}
		return xl, nil
	}
	lo, hi := int64(1), int64(2)
	for hi < n {
		v, e := slowAt(hi)
		if e != nil {
			return nil, reads, false, e
		}
		if v != slow0 {
			break
		}
		lo = hi
		if hi > n/2 {
			hi = n - 1
		} else {
			hi *= 2
		}
		if hi == lo {
			break
		}
	}
	if hi >= n {
		hi = n - 1
	}
	vh, e := slowAt(hi)
	if e != nil {
		return nil, reads, false, e
	}
	if vh == slow0 {
		return nil, reads, false, nil
	}
	left, right := lo+1, hi
	for left < right {
		mid := left + (right-left)/2
		v, e := slowAt(mid)
		if e != nil {
			return nil, reads, false, e
		}
		if v == slow0 {
			left = mid + 1
		} else {
			right = mid
		}
	}
	lineLen := left
	if lineLen < 2 || n%lineLen != 0 {
		return nil, reads, false, nil
	}
	nLines := n / lineLen
	if nLines < 2 {
		return nil, reads, false, nil
	}

	// Verify the first line is an arithmetic progression on the fast axis.
	ilLast, xlLast, e := read(lineLen - 1)
	if e != nil {
		return nil, reads, false, e
	}
	fastLast := ilLast
	if fastIsXL {
		fastLast = xlLast
	}
	expectedFastLast := fast0 + int32(lineLen-1)*fastStep
	if fastLast != expectedFastLast {
		return nil, reads, false, nil
	}

	// Infer slow step and whether line 2 reverses fast-axis direction.
	ilL1, xlL1, e := read(lineLen)
	if e != nil {
		return nil, reads, false, e
	}
	slow1, fastStart2 := xlL1, ilL1
	if fastIsXL {
		slow1, fastStart2 = ilL1, xlL1
	}
	slowStep := slow1 - slow0
	if slowStep == 0 {
		return nil, reads, false, nil
	}
	serpentine := false
	if fastStart2 == fastLast {
		serpentine = true
	} else if fastStart2 != fast0 {
		return nil, reads, false, nil
	}

	// Verify the last line starts where the arithmetic slow-axis model predicts.
	lastLineStart := (nLines - 1) * lineLen
	ilLS, xlLS, e := read(lastLineStart)
	if e != nil {
		return nil, reads, false, e
	}
	slowLS := xlLS
	if fastIsXL {
		slowLS = ilLS
	}
	if slowLS != slow0+int32(nLines-1)*slowStep {
		return nil, reads, false, nil
	}

	predict := func(tr int64) (int32, int32) {
		row := tr / lineLen
		col := tr % lineLen
		if serpentine && row%2 == 1 {
			col = lineLen - 1 - col
		}
		fast := fast0 + int32(col)*fastStep
		slow := slow0 + int32(row)*slowStep
		if fastIsXL {
			return slow, fast
		}
		return fast, slow
	}
	// Validate endpoints, line boundaries, and evenly distributed checkpoints.
	checks := make(map[int64]struct{}, 160)
	add := func(x int64) {
		if x >= 0 && x < n {
			checks[x] = struct{}{}
		}
	}
	for _, x := range []int64{0, 1, lineLen - 1, lineLen, lineLen + 1, 2*lineLen - 1, n - 1, lastLineStart} {
		add(x)
	}
	const nCheck = 96
	for i := 0; i < nCheck; i++ {
		add(int64(i) * (n - 1) / (nCheck - 1))
	}
	for tr := range checks {
		il, xl, e := read(tr)
		if e != nil {
			return nil, reads, false, e
		}
		pi, px := predict(tr)
		if il != pi || xl != px {
			return nil, reads, false, nil
		}
	}
	if progress != nil {
		progress(1, 1)
	}

	// Build the same public index shape as the exhaustive path, but entirely
	// from arithmetic rather than disk I/O. This preserves all existing callers.
	ilVals := make([]int32, 0, nLines)
	xlVals := make([]int32, 0, lineLen)
	if fastIsXL {
		for r := int64(0); r < nLines; r++ {
			ilVals = append(ilVals, slow0+int32(r)*slowStep)
		}
		for c := int64(0); c < lineLen; c++ {
			xlVals = append(xlVals, fast0+int32(c)*fastStep)
		}
	} else {
		for c := int64(0); c < lineLen; c++ {
			ilVals = append(ilVals, fast0+int32(c)*fastStep)
		}
		for r := int64(0); r < nLines; r++ {
			xlVals = append(xlVals, slow0+int32(r)*slowStep)
		}
	}
	sort.Slice(ilVals, func(i, j int) bool { return ilVals[i] < ilVals[j] })
	sort.Slice(xlVals, func(i, j int) bool { return xlVals[i] < xlVals[j] })
	ilRank := make(map[int32]int32, len(ilVals))
	for i, v := range ilVals {
		ilRank[v] = int32(i)
	}
	xlRank := make(map[int32]int32, len(xlVals))
	for i, v := range xlVals {
		xlRank[v] = int32(i)
	}
	traces := make([]int64, int(n))
	rows := make([]int32, int(n))
	cols := make([]int32, int(n))
	for tr := int64(0); tr < n; tr++ {
		il, xl := predict(tr)
		traces[tr] = tr
		rows[tr] = ilRank[il]
		cols[tr] = xlRank[xl]
	}
	st, _ := s.f.Stat()
	idx := &GeometryIndex{SourcePath: s.Info.Path, FileSize: s.Info.FileSize, TraceCount: n, InlineByte: inlineByte, CrosslineByte: crosslineByte,
		InlineValues: ilVals, CrosslineValues: xlVals, TraceNumbers: traces, RowOfTrace: rows, ColOfTrace: cols,
		ValidTraceCount: n, DuplicateCount: 0, Poststack: true}
	if st != nil {
		idx.ModTimeUnixNano = st.ModTime().UnixNano()
	}
	idx.prepare()
	return idx, reads, true, nil
}

// BuildGeometryIndexCached scans only 240-byte trace headers. On the first
// open it creates a compact persistent cache under the user's OS cache folder;
// subsequent opens of the unchanged SEG-Y restore the index immediately.
func (s *File) BuildGeometryIndexCached(inlineByte, crosslineByte, workers int) (*GeometryIndex, GeometryBuildStats, error) {
	return s.BuildGeometryIndexCachedProgress(inlineByte, crosslineByte, workers, nil)
}

// BuildGeometryIndexCachedProgress is the progress-reporting variant used by
// the Limage workspace on first load. Header parsing is always multi-threaded;
// progress callbacks may be invoked from worker goroutines.
func (s *File) BuildGeometryIndexCachedProgress(inlineByte, crosslineByte, workers int, progress func(done, total int)) (*GeometryIndex, GeometryBuildStats, error) {
	start := time.Now()
	if inlineByte <= 0 {
		inlineByte = 189
	}
	if crosslineByte <= 0 {
		crosslineByte = 193
	}
	if inlineByte+3 > 240 || crosslineByte+3 > 240 {
		return nil, GeometryBuildStats{}, errors.New("inline/crossline header byte must fit inside the 240-byte trace header")
	}
	st, err := s.f.Stat()
	if err != nil {
		return nil, GeometryBuildStats{}, err
	}
	if idx, cp, ok := loadGeometryCache(s.Info.Path, st, inlineByte, crosslineByte); ok {
		if progress != nil {
			progress(1, 1)
		}
		return idx, GeometryBuildStats{FromCache: true, CachePath: cp, Duration: time.Since(start)}, nil
	}
	cp, _ := geometryCachePath(s.Info.Path, st, inlineByte, crosslineByte)
	// First try the O(100-header) regular-grid inference. This is the key
	// large-volume path: a conventional ordered post-stack cube should never
	// require a full trace-header scan merely to show its first Inline.
	if idx, reads, ok, ferr := s.tryBuildRegularGeometry(inlineByte, crosslineByte, progress); ferr != nil {
		return nil, GeometryBuildStats{}, ferr
	} else if ok {
		// Reuse immediately in-process; persist in the background so cache
		// serialization never blocks first paint.
		geometryMemoryCache.Store(cp, idx)
		go saveGeometryCache(cp, idx)
		return idx, GeometryBuildStats{FromCache: false, CachePath: cp, Duration: time.Since(start), Workers: 1, FastRegular: true, HeaderReads: reads}, nil
	}
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers < 1 {
		workers = 1
	}
	if workers > 16 {
		workers = 16
	}

	n := int(s.Info.TraceCount)
	ils := make([]int32, n)
	xls := make([]int32, n)
	valid := make([]bool, n)
	jobs := make(chan int, workers*4)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	var doneHeaders int64
	var lastPct int64 = -1
	report := func() {
		if progress == nil || n <= 0 {
			return
		}
		d := atomic.AddInt64(&doneHeaders, 1)
		pct := d * 100 / int64(n)
		for {
			old := atomic.LoadInt64(&lastPct)
			if pct <= old {
				return
			}
			if atomic.CompareAndSwapInt64(&lastPct, old, pct) {
				progress(int(d), n)
				return
			}
		}
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hdr := make([]byte, 240)
			for tr := range jobs {
				off := s.Info.DataStart + int64(tr)*s.Info.TraceBytes
				if _, err := s.f.ReadAt(hdr, off); err != nil {
					report()
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					continue
				}
				il := traceI32(hdr[inlineByte-1:inlineByte+3], s.Info.Endian)
				xl := traceI32(hdr[crosslineByte-1:crosslineByte+3], s.Info.Endian)
				// A zero value can be legal, but both being zero is the common
				// signature of missing geometry headers.
				if il == 0 && xl == 0 {
					report()
					continue
				}
				ils[tr], xls[tr], valid[tr] = il, xl, true
				report()
			}
		}()
	}
	for tr := 0; tr < n; tr++ {
		jobs <- tr
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, GeometryBuildStats{}, firstErr
	}

	ilSet := make(map[int32]struct{})
	xlSet := make(map[int32]struct{})
	pairSet := make(map[[2]int32]struct{})
	duplicates := int64(0)
	validCount := int64(0)
	for tr := 0; tr < n; tr++ {
		if !valid[tr] {
			continue
		}
		il, xl := ils[tr], xls[tr]
		ilSet[il] = struct{}{}
		xlSet[xl] = struct{}{}
		p := [2]int32{il, xl}
		if _, exists := pairSet[p]; exists {
			duplicates++
		} else {
			pairSet[p] = struct{}{}
		}
		validCount++
	}
	if validCount < 4 || len(ilSet) < 2 || len(xlSet) < 2 {
		return nil, GeometryBuildStats{}, fmt.Errorf("no usable 3-D post-stack geometry found at trace-header bytes %d/%d", inlineByte, crosslineByte)
	}
	ilVals := make([]int32, 0, len(ilSet))
	for v := range ilSet {
		ilVals = append(ilVals, v)
	}
	xlVals := make([]int32, 0, len(xlSet))
	for v := range xlSet {
		xlVals = append(xlVals, v)
	}
	sort.Slice(ilVals, func(i, j int) bool { return ilVals[i] < ilVals[j] })
	sort.Slice(xlVals, func(i, j int) bool { return xlVals[i] < xlVals[j] })
	ilRank := make(map[int32]int32, len(ilVals))
	xlRank := make(map[int32]int32, len(xlVals))
	for i, v := range ilVals {
		ilRank[v] = int32(i)
	}
	for i, v := range xlVals {
		xlRank[v] = int32(i)
	}

	traces := make([]int64, 0, validCount)
	rows := make([]int32, 0, validCount)
	cols := make([]int32, 0, validCount)
	for tr := 0; tr < n; tr++ {
		if !valid[tr] {
			continue
		}
		traces = append(traces, int64(tr))
		rows = append(rows, ilRank[ils[tr]])
		cols = append(cols, xlRank[xls[tr]])
	}
	duplicateRatio := float64(duplicates) / float64(validCount)
	idx := &GeometryIndex{
		SourcePath: s.Info.Path, FileSize: st.Size(), ModTimeUnixNano: st.ModTime().UnixNano(), TraceCount: s.Info.TraceCount,
		InlineByte: inlineByte, CrosslineByte: crosslineByte,
		InlineValues: ilVals, CrosslineValues: xlVals, TraceNumbers: traces, RowOfTrace: rows, ColOfTrace: cols,
		ValidTraceCount: validCount, DuplicateCount: duplicates,
		// Post-stack volumes should have roughly one trace per IL/XL bin.
		Poststack: duplicateRatio <= 0.10,
	}
	idx.prepare()
	saveGeometryCache(cp, idx)
	if progress != nil {
		progress(n, n)
	}
	return idx, GeometryBuildStats{FromCache: false, CachePath: cp, Duration: time.Since(start), Workers: workers}, nil
}

func (g *GeometryIndex) prepare() {
	if g == nil {
		return
	}
	g.prepOnce.Do(func() {
		rows, cols := len(g.InlineValues), len(g.CrosslineValues)
		if rows < 1 || cols < 1 {
			return
		}
		cells64 := int64(rows) * int64(cols)
		// Dense lookup greatly accelerates rasterization. Cap it to ~80 MB.
		if cells64 > 20_000_000 {
			return
		}
		grid := make([]int32, int(cells64))
		for i := range grid {
			grid[i] = -1
		}
		for i := range g.TraceNumbers {
			r, c := int(g.RowOfTrace[i]), int(g.ColOfTrace[i])
			if r < 0 || r >= rows || c < 0 || c >= cols {
				continue
			}
			k := r*cols + c
			if grid[k] < 0 {
				grid[k] = int32(i)
			}
		}
		g.denseGrid = grid
	})
}

func (g *GeometryIndex) Rows() int { return len(g.InlineValues) }
func (g *GeometryIndex) Cols() int { return len(g.CrosslineValues) }

// TraceAt returns the zero-based SEG-Y trace number stored at an exact
// Inline/Crossline bin. It exposes the index lookup without changing the v1
// cache representation used by the renderer.
func (g *GeometryIndex) TraceAt(inline, crossline int32) (int64, bool) {
	if g == nil {
		return 0, false
	}
	row := sort.Search(len(g.InlineValues), func(i int) bool { return g.InlineValues[i] >= inline })
	col := sort.Search(len(g.CrosslineValues), func(i int) bool { return g.CrosslineValues[i] >= crossline })
	if row >= len(g.InlineValues) || col >= len(g.CrosslineValues) || g.InlineValues[row] != inline || g.CrosslineValues[col] != crossline {
		return 0, false
	}
	g.prepare()
	if len(g.denseGrid) == len(g.InlineValues)*len(g.CrosslineValues) {
		indexedTrace := g.denseGrid[row*len(g.CrosslineValues)+col]
		if indexedTrace >= 0 && int(indexedTrace) < len(g.TraceNumbers) {
			return g.TraceNumbers[indexedTrace], true
		}
		return 0, false
	}
	for i := range g.TraceNumbers {
		if int(g.RowOfTrace[i]) == row && int(g.ColOfTrace[i]) == col {
			return g.TraceNumbers[i], true
		}
	}
	return 0, false
}

func (g *GeometryIndex) Bounds() SliceBounds {
	b := SliceBounds{}
	if g == nil || len(g.InlineValues) == 0 || len(g.CrosslineValues) == 0 {
		return b
	}
	b.InlineMin, b.InlineMax = g.InlineValues[0], g.InlineValues[len(g.InlineValues)-1]
	b.CrosslineMin, b.CrosslineMax = g.CrosslineValues[0], g.CrosslineValues[len(g.CrosslineValues)-1]
	return b
}

type SliceBounds struct {
	InlineMin, InlineMax       int32
	CrosslineMin, CrosslineMax int32
}

func UnionSliceBounds(a, b *GeometryIndex) SliceBounds {
	ba, bb := a.Bounds(), b.Bounds()
	out := ba
	if out.InlineMin == 0 && out.InlineMax == 0 && len(a.InlineValues) == 0 {
		out = bb
		return out
	}
	if bb.InlineMin < out.InlineMin {
		out.InlineMin = bb.InlineMin
	}
	if bb.InlineMax > out.InlineMax {
		out.InlineMax = bb.InlineMax
	}
	if bb.CrosslineMin < out.CrosslineMin {
		out.CrosslineMin = bb.CrosslineMin
	}
	if bb.CrosslineMax > out.CrosslineMax {
		out.CrosslineMax = bb.CrosslineMax
	}
	return out
}

func nearestRank(vals []int32, target float64) int {
	if len(vals) == 0 {
		return -1
	}
	if target < float64(vals[0]) || target > float64(vals[len(vals)-1]) {
		return -1
	}
	i := sort.Search(len(vals), func(i int) bool { return float64(vals[i]) >= target })
	if i <= 0 {
		return 0
	}
	if i >= len(vals) {
		return len(vals) - 1
	}
	if target-float64(vals[i-1]) <= float64(vals[i])-target {
		return i - 1
	}
	return i
}

// RasterizeTimeSlice maps trace-domain values onto an Inline/Crossline image.
// Missing bins are marked false in mask; the caller can render them as white.
func (g *GeometryIndex) RasterizeTimeSlice(values []float32, width, height int, bounds SliceBounds) ([]float32, []bool, error) {
	if g == nil || len(values) != len(g.TraceNumbers) {
		return nil, nil, errors.New("time-slice values do not match geometry index")
	}
	if width < 2 || height < 2 {
		return nil, nil, errors.New("invalid raster size")
	}
	g.prepare()
	out := make([]float32, width*height)
	mask := make([]bool, width*height)
	if bounds.InlineMax <= bounds.InlineMin || bounds.CrosslineMax <= bounds.CrosslineMin {
		bounds = g.Bounds()
	}
	rows, cols := g.Rows(), g.Cols()
	if len(g.denseGrid) == rows*cols && rows > 0 && cols > 0 {
		yRanks := make([]int, height)
		xRanks := make([]int, width)
		for y := 0; y < height; y++ {
			f := float64(y) / float64(height-1)
			il := float64(bounds.InlineMin) + f*float64(bounds.InlineMax-bounds.InlineMin)
			yRanks[y] = nearestRank(g.InlineValues, il)
		}
		for x := 0; x < width; x++ {
			f := float64(x) / float64(width-1)
			xl := float64(bounds.CrosslineMin) + f*float64(bounds.CrosslineMax-bounds.CrosslineMin)
			xRanks[x] = nearestRank(g.CrosslineValues, xl)
		}
		for y := 0; y < height; y++ {
			r := yRanks[y]
			for x := 0; x < width; x++ {
				c := xRanks[x]
				if r < 0 || c < 0 {
					continue
				}
				ti := g.denseGrid[r*cols+c]
				if ti >= 0 && int(ti) < len(values) {
					k := y*width + x
					out[k] = values[ti]
					mask[k] = true
				}
			}
		}
		return out, mask, nil
	}

	// Sparse fallback: splat valid traces into output pixels and average bins.
	sums := make([]float64, width*height)
	counts := make([]uint32, width*height)
	denIL := float64(bounds.InlineMax - bounds.InlineMin)
	denXL := float64(bounds.CrosslineMax - bounds.CrosslineMin)
	for i := range g.TraceNumbers {
		il := g.InlineValues[g.RowOfTrace[i]]
		xl := g.CrosslineValues[g.ColOfTrace[i]]
		if il < bounds.InlineMin || il > bounds.InlineMax || xl < bounds.CrosslineMin || xl > bounds.CrosslineMax {
			continue
		}
		y := int((float64(il-bounds.InlineMin)/denIL)*float64(height-1) + 0.5)
		x := int((float64(xl-bounds.CrosslineMin)/denXL)*float64(width-1) + 0.5)
		if x < 0 || x >= width || y < 0 || y >= height {
			continue
		}
		k := y*width + x
		sums[k] += float64(values[i])
		counts[k]++
	}
	for k, n := range counts {
		if n > 0 {
			out[k] = float32(sums[k] / float64(n))
			mask[k] = true
		}
	}
	return out, mask, nil
}

// LineTraceNumbers returns the traces on the nearest requested Inline or
// Crossline, ordered by the other spatial coordinate. axis=0 selects an
// Inline and orders by Crossline; axis=1 selects a Crossline and orders by
// Inline. The returned coordinate is the actual nearest line present.
func (g *GeometryIndex) LineTraceNumbers(axis int, target int32) ([]int64, []int32, int32, error) {
	if g == nil || len(g.TraceNumbers) == 0 {
		return nil, nil, 0, errors.New("empty geometry index")
	}
	g.prepare()
	var selectedRank int
	var actual int32
	if axis == 0 {
		selectedRank = nearestRank(g.InlineValues, float64(target))
		if selectedRank < 0 {
			if target < g.InlineValues[0] {
				selectedRank = 0
			} else {
				selectedRank = len(g.InlineValues) - 1
			}
		}
		actual = g.InlineValues[selectedRank]
	} else {
		selectedRank = nearestRank(g.CrosslineValues, float64(target))
		if selectedRank < 0 {
			if target < g.CrosslineValues[0] {
				selectedRank = 0
			} else {
				selectedRank = len(g.CrosslineValues) - 1
			}
		}
		actual = g.CrosslineValues[selectedRank]
	}
	// Dense-grid fast path: avoid scanning millions of geometry entries every
	// time the user steps to a neighboring Inline/Crossline.
	rowsN, colsN := g.Rows(), g.Cols()
	if len(g.denseGrid) == rowsN*colsN && rowsN > 0 && colsN > 0 {
		if axis == 0 {
			traces := make([]int64, 0, colsN)
			coords := make([]int32, 0, colsN)
			for c := 0; c < colsN; c++ {
				ti := g.denseGrid[selectedRank*colsN+c]
				if ti >= 0 && int(ti) < len(g.TraceNumbers) {
					traces = append(traces, g.TraceNumbers[ti])
					coords = append(coords, g.CrosslineValues[c])
				}
			}
			if len(traces) > 0 {
				return traces, coords, actual, nil
			}
		} else {
			traces := make([]int64, 0, rowsN)
			coords := make([]int32, 0, rowsN)
			for r := 0; r < rowsN; r++ {
				ti := g.denseGrid[r*colsN+selectedRank]
				if ti >= 0 && int(ti) < len(g.TraceNumbers) {
					traces = append(traces, g.TraceNumbers[ti])
					coords = append(coords, g.InlineValues[r])
				}
			}
			if len(traces) > 0 {
				return traces, coords, actual, nil
			}
		}
	}
	type item struct {
		tr    int64
		coord int32
	}
	items := make([]item, 0)
	for i, tr := range g.TraceNumbers {
		r, c := int(g.RowOfTrace[i]), int(g.ColOfTrace[i])
		if axis == 0 {
			if r != selectedRank {
				continue
			}
			items = append(items, item{tr: tr, coord: g.CrosslineValues[c]})
		} else {
			if c != selectedRank {
				continue
			}
			items = append(items, item{tr: tr, coord: g.InlineValues[r]})
		}
	}
	if len(items) == 0 {
		return nil, nil, actual, errors.New("selected geometry line contains no traces")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].coord < items[j].coord })
	traces := make([]int64, len(items))
	coords := make([]int32, len(items))
	for i, it := range items {
		traces[i], coords[i] = it.tr, it.coord
	}
	return traces, coords, actual, nil
}
