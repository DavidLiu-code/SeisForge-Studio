package segy

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
)

type TimeSliceBlockStats struct {
	CacheHit     bool
	BlockStart   int
	BlockEnd     int
	Duration     time.Duration
	BlockSamples int
}

type timeSlab struct {
	start, end int
	values     []float32 // sample-major: [sample-start][validTrace]
}

type slabLoad struct {
	done chan struct{}
	slab *timeSlab
	err  error
}

// TimeSliceCache stores small sample slabs rather than isolated time slices.
// A single disk pass therefore feeds many neighboring slider positions.
type TimeSliceCache struct {
	file         *File
	geom         *GeometryIndex
	blockSamples int
	maxBlocks    int
	workers      int

	mu      sync.Mutex
	blocks  map[int]*timeSlab
	lru     []int
	loading map[int]*slabLoad
}

func NewTimeSliceCache(f *File, g *GeometryIndex) *TimeSliceCache {
	n := len(g.TraceNumbers)
	// Target ~32 MiB per slab, bounded to 4..32 samples. Three slabs per
	// volume retain current + previous + next during bidirectional prefetch.
	bs := 32
	if n > 0 {
		maxS := (32 << 20) / (n * 4)
		switch {
		case maxS >= 32:
			bs = 32
		case maxS >= 16:
			bs = 16
		case maxS >= 8:
			bs = 8
		default:
			bs = 4
		}
	}
	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	if workers > 16 {
		workers = 16
	}
	return &TimeSliceCache{file: f, geom: g, blockSamples: bs, maxBlocks: 3, workers: workers,
		blocks: make(map[int]*timeSlab), loading: make(map[int]*slabLoad)}
}

func (c *TimeSliceCache) BlockSamples() int { return c.blockSamples }

func (c *TimeSliceCache) blockStart(sample int) int {
	if sample < 0 {
		sample = 0
	}
	return (sample / c.blockSamples) * c.blockSamples
}

func (c *TimeSliceCache) touchLocked(start int) {
	out := c.lru[:0]
	for _, v := range c.lru {
		if v != start {
			out = append(out, v)
		}
	}
	c.lru = append(out, start)
	for len(c.lru) > c.maxBlocks {
		evict := c.lru[0]
		c.lru = c.lru[1:]
		delete(c.blocks, evict)
	}
}

func (c *TimeSliceCache) GetSliceCached(sample int) ([]float32, bool) {
	if sample < 0 || sample >= c.file.Info.SamplesPerTrace {
		return nil, false
	}
	start := c.blockStart(sample)
	c.mu.Lock()
	slab := c.blocks[start]
	if slab != nil {
		c.touchLocked(start)
	}
	c.mu.Unlock()
	if slab == nil || sample < slab.start || sample > slab.end {
		return nil, false
	}
	ntr := len(c.geom.TraceNumbers)
	off := (sample - slab.start) * ntr
	return slab.values[off : off+ntr], true
}

func (c *TimeSliceCache) GetSlice(sample int) ([]float32, TimeSliceBlockStats, error) {
	started := time.Now()
	if sample < 0 || sample >= c.file.Info.SamplesPerTrace {
		return nil, TimeSliceBlockStats{}, errors.New("time-slice sample out of range")
	}
	if v, ok := c.GetSliceCached(sample); ok {
		s := c.blockStart(sample)
		end := s + c.blockSamples - 1
		if end >= c.file.Info.SamplesPerTrace {
			end = c.file.Info.SamplesPerTrace - 1
		}
		return v, TimeSliceBlockStats{CacheHit: true, BlockStart: s, BlockEnd: end, Duration: time.Since(started), BlockSamples: end - s + 1}, nil
	}
	bs := c.blockStart(sample)

	c.mu.Lock()
	if existing := c.loading[bs]; existing != nil {
		c.mu.Unlock()
		<-existing.done
		if existing.err != nil {
			return nil, TimeSliceBlockStats{}, existing.err
		}
		v, _ := c.GetSliceCached(sample)
		return v, TimeSliceBlockStats{CacheHit: true, BlockStart: existing.slab.start, BlockEnd: existing.slab.end, Duration: time.Since(started), BlockSamples: existing.slab.end - existing.slab.start + 1}, nil
	}
	load := &slabLoad{done: make(chan struct{})}
	c.loading[bs] = load
	c.mu.Unlock()

	slab, err := c.loadBlock(bs)
	c.mu.Lock()
	load.slab, load.err = slab, err
	delete(c.loading, bs)
	if err == nil {
		c.blocks[bs] = slab
		c.touchLocked(bs)
	}
	close(load.done)
	c.mu.Unlock()
	if err != nil {
		return nil, TimeSliceBlockStats{}, err
	}
	ntr := len(c.geom.TraceNumbers)
	off := (sample - slab.start) * ntr
	return slab.values[off : off+ntr], TimeSliceBlockStats{CacheHit: false, BlockStart: slab.start, BlockEnd: slab.end, Duration: time.Since(started), BlockSamples: slab.end - slab.start + 1}, nil
}

func (c *TimeSliceCache) loadBlock(start int) (*timeSlab, error) {
	end := start + c.blockSamples - 1
	if end >= c.file.Info.SamplesPerTrace {
		end = c.file.Info.SamplesPerTrace - 1
	}
	if start < 0 || start > end {
		return nil, errors.New("invalid time slab")
	}
	ns := end - start + 1
	ntr := len(c.geom.TraceNumbers)
	if ntr == 0 {
		return nil, errors.New("empty geometry index")
	}
	values := make([]float32, ns*ntr)
	jobs := make(chan int, c.workers*4)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	bps := c.file.Info.BytesPerSample
	for w := 0; w < c.workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw := make([]byte, ns*bps)
			for i := range jobs {
				tr := c.geom.TraceNumbers[i]
				off := c.file.Info.DataStart + tr*c.file.Info.TraceBytes + 240 + int64(start*bps)
				if _, err := c.file.f.ReadAt(raw, off); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					continue
				}
				for s := 0; s < ns; s++ {
					values[s*ntr+i] = float32(decodeSample(raw[s*bps:(s+1)*bps], c.file.Info.FormatCode, c.file.Info.Endian))
				}
			}
		}()
	}
	for i := 0; i < ntr; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return &timeSlab{start: start, end: end, values: values}, nil
}

// Prefetch asynchronously warms the block containing sample. Duplicate loads
// are coalesced by GetSlice.
func (c *TimeSliceCache) Prefetch(sample int) {
	if sample < 0 || sample >= c.file.Info.SamplesPerTrace {
		return
	}
	if _, ok := c.GetSliceCached(sample); ok {
		return
	}
	go func() { _, _, _ = c.GetSlice(sample) }()
}

func (c *TimeSliceCache) String() string {
	return fmt.Sprintf("%d-sample slabs, %d blocks, %d workers", c.blockSamples, c.maxBlocks, c.workers)
}
