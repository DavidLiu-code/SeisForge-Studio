package geometry

import (
	"crypto/sha256"
	"encoding/gob"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const (
	crookedCacheMagic   = "LIMAGE-CROOKED-INDEX"
	crookedCacheVersion = 1
)

// CrookedLineGeometry contains trace-header metadata only. TraceIndices remain
// zero-based SEG-Y trace numbers and are kept in acquisition order.
type CrookedLineGeometry struct {
	TraceIndices    []int64
	CDP             []int32
	HasCDP          []bool
	X               []float64
	Y               []float64
	Distance        []float64
	TotalDistance   float64
	CoordinateUnits int16
	Spec            segy.TraceCoordinateSpec
}

func NewCrookedLine(values []segy.TraceCoordinate, spec segy.TraceCoordinateSpec) (*CrookedLineGeometry, error) {
	normalized, err := segy.NormalizeCoordinateSpec(spec)
	if err != nil {
		return nil, err
	}
	g := &CrookedLineGeometry{Spec: normalized}
	for _, value := range values {
		if !value.Valid {
			continue
		}
		if len(g.TraceIndices) > 0 && value.Trace <= g.TraceIndices[len(g.TraceIndices)-1] {
			return nil, errors.New("crooked trace coordinates are not in acquisition order")
		}
		distance := 0.0
		if len(g.TraceIndices) > 0 {
			last := len(g.TraceIndices) - 1
			distance = g.Distance[last] + math.Hypot(value.X-g.X[last], value.Y-g.Y[last])
		}
		g.TraceIndices = append(g.TraceIndices, value.Trace)
		g.CDP = append(g.CDP, value.CDP)
		g.HasCDP = append(g.HasCDP, value.HasCDP)
		g.X = append(g.X, value.X)
		g.Y = append(g.Y, value.Y)
		g.Distance = append(g.Distance, distance)
		if g.CoordinateUnits == 0 && value.Units != 0 {
			g.CoordinateUnits = value.Units
		}
	}
	if len(g.TraceIndices) < 2 {
		return nil, errors.New("at least two valid X/Y trace coordinates are required")
	}
	g.TotalDistance = g.Distance[len(g.Distance)-1]
	if g.TotalDistance <= 0 {
		return nil, errors.New("trace-header X/Y coordinates do not define a trajectory")
	}
	return g, nil
}

func (g *CrookedLineGeometry) Kind() Kind { return KindCrookedLine }
func (g *CrookedLineGeometry) TraceCount() int64 {
	if g == nil {
		return 0
	}
	return int64(len(g.TraceIndices))
}

func (g *CrookedLineGeometry) PositionOfTrace(trace int64) (int, bool) {
	if g == nil || trace < 0 {
		return 0, false
	}
	position := sort.Search(len(g.TraceIndices), func(i int) bool { return g.TraceIndices[i] >= trace })
	return position, position < len(g.TraceIndices) && g.TraceIndices[position] == trace
}

func (g *CrookedLineGeometry) TraceLocation(trace int64) (TraceLocation, bool) {
	position, ok := g.PositionOfTrace(trace)
	if !ok || position >= len(g.X) || position >= len(g.Y) || position >= len(g.Distance) {
		return TraceLocation{}, false
	}
	location := TraceLocation{Trace: trace, X: g.X[position], Y: g.Y[position], Distance: g.Distance[position], HasXY: true}
	if position < len(g.CDP) && position < len(g.HasCDP) && g.HasCDP[position] {
		location.CDP = g.CDP[position]
		location.HasCDP = true
	}
	return location, true
}

func (g *CrookedLineGeometry) Bounds() Bounds {
	if g == nil || len(g.TraceIndices) == 0 {
		return Bounds{}
	}
	bounds := Bounds{TraceMin: g.TraceIndices[0], TraceMax: g.TraceIndices[len(g.TraceIndices)-1], XMin: g.X[0], XMax: g.X[0], YMin: g.Y[0], YMax: g.Y[0], HasXY: true}
	for i := 1; i < len(g.X); i++ {
		bounds.XMin = math.Min(bounds.XMin, g.X[i])
		bounds.XMax = math.Max(bounds.XMax, g.X[i])
		bounds.YMin = math.Min(bounds.YMin, g.Y[i])
		bounds.YMax = math.Max(bounds.YMax, g.Y[i])
	}
	return bounds
}

func (g *CrookedLineGeometry) TraceAtDistance(distance float64) (int64, int, bool) {
	if g == nil || len(g.Distance) == 0 {
		return 0, 0, false
	}
	if distance <= 0 {
		return g.TraceIndices[0], 0, true
	}
	if distance >= g.TotalDistance {
		last := len(g.TraceIndices) - 1
		return g.TraceIndices[last], last, true
	}
	i := sort.Search(len(g.Distance), func(i int) bool { return g.Distance[i] >= distance })
	if i > 0 && distance-g.Distance[i-1] <= g.Distance[i]-distance {
		i--
	}
	return g.TraceIndices[i], i, true
}

func (g *CrookedLineGeometry) NearestTraceXY(x, y float64) (int64, int, bool) {
	if g == nil || len(g.TraceIndices) == 0 {
		return 0, 0, false
	}
	best, bestD2 := 0, math.Inf(1)
	for i := range g.TraceIndices {
		dx, dy := g.X[i]-x, g.Y[i]-y
		d2 := dx*dx + dy*dy
		if d2 < bestD2 {
			best, bestD2 = i, d2
		}
	}
	return g.TraceIndices[best], best, true
}

func (g *CrookedLineGeometry) CDPMonotonic() bool {
	if g == nil || len(g.CDP) < 2 || len(g.HasCDP) != len(g.CDP) {
		return false
	}
	direction := int64(0)
	for i := 1; i < len(g.CDP); i++ {
		if !g.HasCDP[i-1] || !g.HasCDP[i] {
			return false
		}
		delta := int64(g.CDP[i]) - int64(g.CDP[i-1])
		if delta == 0 {
			continue
		}
		if direction == 0 {
			direction = delta
		} else if direction*delta < 0 {
			return false
		}
	}
	return direction != 0
}

// SampleDistanceWindow returns one nearest input trace per display column.
// Duplicate trace indices are intentional when physical trace spacing is
// wider than the output raster sampling interval.
func (g *CrookedLineGeometry) SampleDistanceWindow(start, end float64, width int) []int64 {
	if g == nil || width < 1 || len(g.TraceIndices) == 0 {
		return nil
	}
	if start < 0 {
		start = 0
	}
	if end <= start || end > g.TotalDistance {
		end = g.TotalDistance
	}
	out := make([]int64, width)
	for x := 0; x < width; x++ {
		distance := start
		if width > 1 {
			distance += float64(x) * (end - start) / float64(width-1)
		}
		out[x], _, _ = g.TraceAtDistance(distance)
	}
	return out
}

type CrookedBuildStats struct {
	FromCache              bool
	ResolvedAutoCache      bool
	ReusedDetectionHeaders bool
	CachePath              string
	Duration               time.Duration
	Workers                int
}

type CrookedBuildResult struct {
	Geometry  *CrookedLineGeometry
	Detection segy.CoordinateDetectResult
	Stats     CrookedBuildStats
}

type crookedCacheFile struct {
	Magic           string
	Version         int
	SourcePath      string
	FileSize        int64
	ModTimeUnixNano int64
	Spec            segy.TraceCoordinateSpec
	Geometry        CrookedLineGeometry
}

var (
	crookedMemoryCache   sync.Map
	crookedCacheCommitMu sync.Mutex
)

func crookedCachePath(path string, size, modTime int64, spec segy.TraceCoordinateSpec) (string, error) {
	directory, err := os.UserCacheDir()
	if err != nil || directory == "" {
		directory = os.TempDir()
	}
	directory = filepath.Join(directory, "Limage", "geometry")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(path)
	key := fmt.Sprintf("%s|%d|%d|%s|%d|%d|%d|%d|%d", abs, size, modTime, spec.Source, spec.XByte, spec.YByte, spec.CDPByte, spec.ScalarByte, spec.UnitsByte)
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(directory, fmt.Sprintf("%x.cidx", sum[:16])), nil
}

func loadCrookedCache(cachePath, sourcePath string, size, modTime int64, spec segy.TraceCoordinateSpec) (*CrookedLineGeometry, bool) {
	if value, ok := crookedMemoryCache.Load(cachePath); ok {
		if geometry, ok := value.(*CrookedLineGeometry); ok && geometry != nil {
			return geometry, true
		}
	}
	f, err := os.Open(cachePath)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	var cache crookedCacheFile
	if err := gob.NewDecoder(f).Decode(&cache); err != nil || cache.Magic != crookedCacheMagic || cache.Version != crookedCacheVersion {
		return nil, false
	}
	if cache.SourcePath != sourcePath || cache.FileSize != size || cache.ModTimeUnixNano != modTime || cache.Spec != spec || len(cache.Geometry.TraceIndices) < 2 {
		return nil, false
	}
	geometry := cache.Geometry
	crookedMemoryCache.Store(cachePath, &geometry)
	return &geometry, true
}

func saveCrookedCache(cachePath, sourcePath string, size, modTime int64, spec segy.TraceCoordinateSpec, geometry *CrookedLineGeometry) {
	if cachePath == "" || geometry == nil {
		return
	}
	crookedMemoryCache.Store(cachePath, geometry)
	// A cache build can be restarted while the previous worker is still
	// winding down (for example after Hide/Show), so PID alone is not a unique
	// temporary name.  Keep each writer isolated until its fully synced gob is
	// ready to replace the shared cache path.
	f, err := os.CreateTemp(filepath.Dir(cachePath), filepath.Base(cachePath)+".*.tmp")
	if err != nil {
		return
	}
	temporary := f.Name()
	// Rename removes the temporary pathname on success; Remove is harmless in
	// that case and guarantees cleanup on every encode/sync/close/rename error.
	defer os.Remove(temporary)
	cache := crookedCacheFile{Magic: crookedCacheMagic, Version: crookedCacheVersion, SourcePath: sourcePath, FileSize: size, ModTimeUnixNano: modTime, Spec: spec, Geometry: *geometry}
	encodeErr := gob.NewEncoder(f).Encode(cache)
	if encodeErr != nil {
		_ = f.Close()
		return
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return
	}
	if err := f.Close(); err != nil {
		return
	}
	// Windows cannot replace an existing destination with os.Rename.  Keep the
	// remove-and-rename pair indivisible across concurrent builders; otherwise
	// one writer can remove the cache another writer just published.
	crookedCacheCommitMu.Lock()
	defer crookedCacheCommitMu.Unlock()
	_ = os.Remove(cachePath)
	_ = os.Rename(temporary, cachePath)
}

func loadSingleDefaultCrookedCache(sourcePath string, size, modTime int64) (*CrookedLineGeometry, segy.TraceCoordinateSpec, string, bool) {
	var found *CrookedLineGeometry
	var foundSpec segy.TraceCoordinateSpec
	var foundPath string
	for _, candidate := range segy.DefaultCoordinateSpecs() {
		cachePath, err := crookedCachePath(sourcePath, size, modTime, candidate)
		if err != nil {
			return nil, segy.TraceCoordinateSpec{}, "", false
		}
		geometry, ok := loadCrookedCache(cachePath, sourcePath, size, modTime, candidate)
		if !ok {
			continue
		}
		if found != nil {
			// Multiple standard candidates have been cached explicitly. Preserve
			// automatic detection semantics instead of guessing between them.
			return nil, segy.TraceCoordinateSpec{}, "", false
		}
		found, foundSpec, foundPath = geometry, candidate, cachePath
	}
	return found, foundSpec, foundPath, found != nil
}

// BuildCrookedCachedProgress selects a standard coordinate source when spec
// is Auto, then scans only trace headers and stores a metadata-only .cidx v1.
func BuildCrookedCachedProgress(file *segy.File, spec segy.TraceCoordinateSpec, workers int, progress func(done, total int)) (CrookedBuildResult, error) {
	start := time.Now()
	if file == nil {
		return CrookedBuildResult{}, errors.New("nil SEG-Y reader")
	}
	stat, err := os.Stat(file.Info.Path)
	if err != nil {
		return CrookedBuildResult{}, err
	}
	var detection segy.CoordinateDetectResult
	var detectedValues []segy.TraceCoordinate
	autoSource := spec.Source == "" || spec.Source == segy.CoordinateAuto
	if autoSource {
		if geometry, cachedSpec, cachePath, ok := loadSingleDefaultCrookedCache(file.Info.Path, stat.Size(), stat.ModTime().UnixNano()); ok {
			if progress != nil {
				progress(1, 1)
			}
			detection.Spec = cachedSpec
			return CrookedBuildResult{Geometry: geometry, Detection: detection, Stats: CrookedBuildStats{
				FromCache: true, ResolvedAutoCache: true, CachePath: cachePath, Duration: time.Since(start),
			}}, nil
		}
	}
	if autoSource || spec.XByte == 0 || spec.YByte == 0 {
		detection, err = file.DetectCoordinateSpec(3000)
		if err != nil {
			return CrookedBuildResult{Detection: detection}, err
		}
		spec = detection.Spec
		if int64(detection.Sampled) == file.Info.TraceCount {
			for _, candidate := range detection.Candidates {
				if candidate.Spec == spec && int64(len(candidate.Coordinates)) == file.Info.TraceCount {
					detectedValues = candidate.Coordinates
					break
				}
			}
		}
	}
	spec, err = segy.NormalizeCoordinateSpec(spec)
	if err != nil {
		return CrookedBuildResult{Detection: detection}, err
	}
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	cachePath, err := crookedCachePath(file.Info.Path, stat.Size(), stat.ModTime().UnixNano(), spec)
	if err != nil {
		return CrookedBuildResult{Detection: detection}, err
	}
	if geometry, ok := loadCrookedCache(cachePath, file.Info.Path, stat.Size(), stat.ModTime().UnixNano(), spec); ok {
		if progress != nil {
			progress(1, 1)
		}
		return CrookedBuildResult{Geometry: geometry, Detection: detection, Stats: CrookedBuildStats{FromCache: true, CachePath: cachePath, Duration: time.Since(start)}}, nil
	}
	values := detectedValues
	if len(values) == 0 {
		values, err = file.ScanTraceCoordinates(spec, workers, progress)
		if err != nil {
			return CrookedBuildResult{Detection: detection}, err
		}
	} else if progress != nil {
		progress(len(values), len(values))
	}
	geometry, err := NewCrookedLine(values, spec)
	if err != nil {
		return CrookedBuildResult{Detection: detection}, err
	}
	saveCrookedCache(cachePath, file.Info.Path, stat.Size(), stat.ModTime().UnixNano(), spec, geometry)
	return CrookedBuildResult{Geometry: geometry, Detection: detection, Stats: CrookedBuildStats{ReusedDetectionHeaders: len(detectedValues) > 0, CachePath: cachePath, Duration: time.Since(start), Workers: workers}}, nil
}

type crookedShape struct {
	ValidRatio, MedianStep, P95Step, TotalDistance, ChordDistance, MaxDeviation float64
	UniqueCount                                                                 int
}

func analyzeCrookedShape(candidate segy.CoordinateCandidate) crookedShape {
	shape := crookedShape{ValidRatio: candidate.ValidRatio, MedianStep: candidate.MedianStep, P95Step: candidate.P95Step, TotalDistance: candidate.TotalDistance, UniqueCount: candidate.UniqueCount}
	valid := make([]segy.TraceCoordinate, 0, len(candidate.Coordinates))
	for _, value := range candidate.Coordinates {
		if value.Valid {
			valid = append(valid, value)
		}
	}
	if len(valid) < 2 {
		return shape
	}
	first, last := valid[0], valid[len(valid)-1]
	dx, dy := last.X-first.X, last.Y-first.Y
	shape.ChordDistance = math.Hypot(dx, dy)
	denominator := dx*dx + dy*dy
	if denominator == 0 {
		for _, value := range valid[1:] {
			shape.MaxDeviation = math.Max(shape.MaxDeviation, math.Hypot(value.X-first.X, value.Y-first.Y))
		}
		return shape
	}
	for _, value := range valid[1 : len(valid)-1] {
		deviation := math.Abs(dy*value.X-dx*value.Y+last.X*first.Y-last.Y*first.X) / math.Sqrt(denominator)
		shape.MaxDeviation = math.Max(shape.MaxDeviation, deviation)
	}
	return shape
}

func clearlyCrooked(candidate segy.CoordinateCandidate) (bool, string) {
	shape := analyzeCrookedShape(candidate)
	if shape.ValidRatio < 0.90 {
		return false, "fewer than 90% of sampled traces have valid X/Y"
	}
	if shape.UniqueCount < 8 || shape.MedianStep <= 0 || shape.TotalDistance <= 0 {
		return false, "too few distinct trajectory positions"
	}
	if shape.P95Step > 10*shape.MedianStep {
		return false, "trajectory contains discontinuous coordinate jumps"
	}
	sinuosity := math.Inf(1)
	if shape.ChordDistance > 0 {
		sinuosity = shape.TotalDistance / shape.ChordDistance
	}
	deviationGate := shape.MaxDeviation >= math.Max(3*shape.MedianStep, 0.005*shape.TotalDistance)
	if sinuosity < 1.01 && !deviationGate {
		return false, "trajectory is effectively straight"
	}
	return true, "valid continuous X/Y trajectory is visibly non-linear"
}
