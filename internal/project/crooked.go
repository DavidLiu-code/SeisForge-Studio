// Package project groups seismic datasets without introducing UI state or
// long-lived SEG-Y readers.  It is deliberately small so a crooked project
// can later be consumed by a 3-D curtain/fusion workspace.
package project

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

type NavigationPoint struct {
	SP       float64
	Trace    int64
	X, Y     float64
	MinTrace int64
	MaxTrace int64
}

type CrookedProjectLine struct {
	ID         string
	Name       string
	Path       string
	Dataset    *dataset.SeismicDataset
	Navigation []NavigationPoint
	OpenError  string
}

func (l *CrookedProjectLine) Valid() bool { return l != nil && l.Dataset != nil && l.OpenError == "" }

type CrookedProject struct {
	Root           string
	NavigationPath string
	Lines          []*CrookedProjectLine
}

func FromDataset(data *dataset.SeismicDataset) (*CrookedProject, error) {
	if data == nil {
		return nil, errors.New("project dataset is nil")
	}
	line := &CrookedProjectLine{ID: strings.ToLower(filepath.Clean(data.Path)), Name: deriveLineName(data.Path), Path: data.Path, Dataset: data}
	return &CrookedProject{Root: filepath.Dir(data.Path), Lines: []*CrookedProjectLine{line}}, nil
}

func (p *CrookedProject) ValidLineCount() int {
	if p == nil {
		return 0
	}
	n := 0
	for _, line := range p.Lines {
		if line.Valid() {
			n++
		}
	}
	return n
}

func (p *CrookedProject) NavigationLineCount() int {
	if p == nil {
		return 0
	}
	n := 0
	for _, line := range p.Lines {
		if len(line.Navigation) >= 2 {
			n++
		}
	}
	return n
}

// ParseNavigation reads the whitespace separated SLGGZ-style navigation
// format: lineName, SP, trace, X, Y, minTrace, maxTrace.
func ParseNavigation(r io.Reader) (map[string][]NavigationPoint, error) {
	if r == nil {
		return nil, errors.New("navigation reader is nil")
	}
	lines := make(map[string][]NavigationPoint)
	scanner := bufio.NewScanner(r)
	// Navigation exports can contain long comment/header lines.
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) < 7 {
			return nil, fmt.Errorf("navigation line %d has %d columns, want at least 7", lineNumber, len(fields))
		}
		sp, err1 := strconv.ParseFloat(fields[1], 64)
		trace, err2 := strconv.ParseInt(fields[2], 10, 64)
		x, err3 := strconv.ParseFloat(fields[3], 64)
		y, err4 := strconv.ParseFloat(fields[4], 64)
		minTrace, err5 := strconv.ParseInt(fields[5], 10, 64)
		maxTrace, err6 := strconv.ParseInt(fields[6], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || err6 != nil || !finite(x) || !finite(y) {
			return nil, fmt.Errorf("navigation line %d contains an invalid numeric value", lineNumber)
		}
		name := strings.TrimSpace(fields[0])
		if name == "" {
			return nil, fmt.Errorf("navigation line %d has no line name", lineNumber)
		}
		lines[name] = append(lines[name], NavigationPoint{SP: sp, Trace: trace, X: x, Y: y, MinTrace: minTrace, MaxTrace: maxTrace})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, errors.New("navigation file contains no data rows")
	}
	for name := range lines {
		sort.SliceStable(lines[name], func(i, j int) bool { return lines[name][i].Trace < lines[name][j].Trace })
	}
	return lines, nil
}

func OpenFolder(manager *dataset.Manager, root string) (*CrookedProject, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("project folder is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project folder: %w", err)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("read project folder: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".sgy" || ext == ".segy" {
			paths = append(paths, filepath.Join(abs, entry.Name()))
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("project folder contains no SEG-Y files")
	}
	return openPaths(manager, paths, abs)
}

func OpenPaths(manager *dataset.Manager, paths []string) (*CrookedProject, error) {
	return openPaths(manager, paths, commonDirectory(paths))
}

func openPaths(manager *dataset.Manager, paths []string, root string) (*CrookedProject, error) {
	if manager == nil {
		manager = dataset.NewManager()
	}
	unique := make([]string, 0, len(paths))
	seen := make(map[string]bool)
	for _, path := range paths {
		abs, err := filepath.Abs(strings.TrimSpace(path))
		if err != nil || abs == "" {
			continue
		}
		abs = filepath.Clean(abs)
		key := strings.ToLower(abs)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, abs)
		}
	}
	if len(unique) == 0 {
		return nil, errors.New("project contains no paths")
	}
	sort.SliceStable(unique, func(i, j int) bool { return naturalLess(filepath.Base(unique[i]), filepath.Base(unique[j])) })

	navigationPath, navigation := discoverNavigation(root, unique)
	project := &CrookedProject{Root: root, NavigationPath: navigationPath, Lines: make([]*CrookedProjectLine, 0, len(unique))}
	valid := 0
	for _, path := range unique {
		name := matchNavigationName(path, navigation)
		if name == "" {
			name = deriveLineName(path)
		}
		line := &CrookedProjectLine{ID: strings.ToLower(filepath.Clean(path)), Name: name, Path: path}
		if points := navigation[name]; len(points) > 0 {
			line.Navigation = append([]NavigationPoint(nil), points...)
		}
		data, err := manager.Open(path)
		if err != nil {
			line.OpenError = err.Error()
		} else {
			line.Dataset = data
			valid++
		}
		project.Lines = append(project.Lines, line)
	}
	if valid == 0 {
		return nil, errors.New("project contains no valid SEG-Y datasets")
	}
	return project, nil
}

func discoverNavigation(root string, paths []string) (string, map[string][]NavigationPoint) {
	if root == "" {
		root = commonDirectory(paths)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", nil
	}
	type candidate struct {
		path  string
		lines map[string][]NavigationPoint
		score int
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".dat") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		lines, parseErr := ParseNavigation(f)
		_ = f.Close()
		if parseErr != nil {
			continue
		}
		score := 0
		for _, path := range paths {
			if matchNavigationName(path, lines) != "" {
				score++
			}
		}
		candidates = append(candidates, candidate{path: path, lines: lines, score: score})
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return naturalLess(filepath.Base(candidates[i].path), filepath.Base(candidates[j].path))
	})
	if candidates[0].score == 0 {
		return "", nil
	}
	return candidates[0].path, candidates[0].lines
}

func matchNavigationName(path string, lines map[string][]NavigationPoint) string {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	best := ""
	for name := range lines {
		lower := strings.ToLower(name)
		if base == lower || strings.HasSuffix(base, "-"+lower) || strings.HasSuffix(base, "_"+lower) || strings.HasSuffix(base, " "+lower) {
			if len(name) > len(best) {
				best = name
			}
		}
	}
	return best
}

func deriveLineName(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	const knownPrefix = "SGY-pstm-post-BH3-for-jiaoda-"
	if strings.HasPrefix(strings.ToLower(name), strings.ToLower(knownPrefix)) {
		return name[len(knownPrefix):]
	}
	return name
}

func commonDirectory(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	first, err := filepath.Abs(paths[0])
	if err != nil {
		return ""
	}
	directory := filepath.Dir(first)
	for _, path := range paths[1:] {
		abs, err := filepath.Abs(path)
		if err != nil || !strings.EqualFold(filepath.Dir(abs), directory) {
			return ""
		}
	}
	return directory
}

type CoordinateCalibration struct {
	// Multiplier and OffsetX/OffsetY describe the display-coordinate
	// transform
	//
	//     display = header*Multiplier + Offset
	//
	// The offsets are important for survey exports whose SEG-Y and DAT files
	// use the same local geometry but a different coordinate origin.
	Multiplier    float64
	OffsetX       float64
	OffsetY       float64
	Residual      float64
	Accepted      bool
	Matches       int
	Inliers       int
	FailureReason string
}

// InferCoordinateCalibration aligns SEG-Y trace-header coordinates with DAT
// navigation by CDP/trace.  A DAT file is only used to infer a decimal scale
// and a constant translation; the per-trace shape always remains the SEG-Y
// header geometry.
//
// Candidate scales are scored after removing the median X/Y translation.
// This is equivalent to comparing centred trajectories, while the median and
// median point residual make the result insensitive to a minority of bad DAT
// picks.  No rotation or local warp is fitted.
func InferCoordinateCalibration(g *geometry.CrookedLineGeometry, navigation []NavigationPoint) CoordinateCalibration {
	result := CoordinateCalibration{Multiplier: 1, Residual: math.Inf(1)}
	pairs := calibrationPairs(g, navigation)
	result.Matches = len(pairs)
	if len(pairs) < 2 {
		result.FailureReason = "insufficient coordinate matches"
		return result
	}
	candidates := []float64{0.001, 0.01, 0.1, 1, 10, 100, 1000}
	type score struct {
		multiplier       float64
		offsetX, offsetY float64
		residual         float64
		inliers          int
	}
	lineLength := navigationLength(navigation)
	// Two-point navigation is common in the supplied projects.  Its endpoint
	// trace/CDP association is necessarily coarser than a dense navigation
	// export, so allow a one-percent shape residual.  Dense navigation keeps
	// the stricter half-percent limit.
	relativeTolerance := 0.005
	if len(pairs) == 2 {
		relativeTolerance = 0.01
	}
	threshold := math.Max(5, lineLength*relativeTolerance)
	scores := make([]score, 0, len(candidates))
	for _, multiplier := range candidates {
		xOffsets := make([]float64, 0, len(pairs))
		yOffsets := make([]float64, 0, len(pairs))
		for _, pair := range pairs {
			xOffsets = append(xOffsets, pair.navX-pair.headerX*multiplier)
			yOffsets = append(yOffsets, pair.navY-pair.headerY*multiplier)
		}
		offsetX := medianFloat64(xOffsets)
		offsetY := medianFloat64(yOffsets)
		errors := make([]float64, 0, len(pairs))
		for _, pair := range pairs {
			errors = append(errors, math.Hypot(
				pair.headerX*multiplier+offsetX-pair.navX,
				pair.headerY*multiplier+offsetY-pair.navY,
			))
		}
		residual := medianFloat64(errors)
		inliers := 0
		for _, value := range errors {
			if value <= threshold {
				inliers++
			}
		}
		scores = append(scores, score{
			multiplier: multiplier,
			offsetX:    offsetX,
			offsetY:    offsetY,
			residual:   residual,
			inliers:    inliers,
		})
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].residual != scores[j].residual {
			return scores[i].residual < scores[j].residual
		}
		if scores[i].inliers != scores[j].inliers {
			return scores[i].inliers > scores[j].inliers
		}
		// Prefer the least invasive scale only when the robust scores are
		// exactly tied.
		return math.Abs(math.Log10(scores[i].multiplier)) < math.Abs(math.Log10(scores[j].multiplier))
	})
	best := scores[0]
	result.Multiplier = best.multiplier
	result.OffsetX = best.offsetX
	result.OffsetY = best.offsetY
	result.Residual = best.residual
	result.Inliers = best.inliers

	minimumInliers := (len(pairs) + 1) / 2
	if minimumInliers < 2 {
		minimumInliers = 2
	}
	if best.residual > threshold || best.inliers < minimumInliers {
		result.FailureReason = fmt.Sprintf("coordinate residual %.3f exceeds tolerance %.3f", best.residual, threshold)
		return result
	}

	unambiguous := best.residual <= 1e-9
	if len(scores) > 1 && !unambiguous {
		second := scores[1]
		separation := math.Max(1, threshold*0.25)
		unambiguous = second.residual >= best.residual*2 && second.residual-best.residual >= separation
	}
	if !unambiguous {
		result.FailureReason = "decimal coordinate multiplier is ambiguous"
		return result
	}
	result.Accepted = true
	return result
}

// InferCoordinateMultiplier is retained for source compatibility.  The
// returned calibration now also contains the constant X/Y translation and
// diagnostics; new callers should apply it with TransformGeometry.
func InferCoordinateMultiplier(g *geometry.CrookedLineGeometry, navigation []NavigationPoint) CoordinateCalibration {
	return InferCoordinateCalibration(g, navigation)
}

func medianFloat64(values []float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[middle-1] + sorted[middle]) / 2
	}
	return sorted[middle]
}

type calibrationPair struct{ headerX, headerY, navX, navY float64 }

func calibrationPairs(g *geometry.CrookedLineGeometry, navigation []NavigationPoint) []calibrationPair {
	if g == nil || len(g.X) < 2 || len(navigation) < 2 || len(g.CDP) != len(g.X) || len(g.HasCDP) != len(g.X) {
		return nil
	}
	nav := append([]NavigationPoint(nil), navigation...)
	sort.SliceStable(nav, func(i, j int) bool { return nav[i].Trace < nav[j].Trace })
	indices := make([]int, 0, len(nav))
	// Match every DAT control point, including both endpoints, to the closest
	// valid SEG-Y CDP.  Geometry arrays are ordered by physical trace order,
	// not necessarily by CDP: a reversed or folded/non-monotonic line therefore
	// cannot use the first and last array entries as navigation endpoints.
	// Restrict candidates to the navigation trace range so the matched CDP can
	// be evaluated on the DAT polyline below.
	for _, point := range nav {
		best, bestDelta := -1, int64(math.MaxInt64)
		for i := range g.CDP {
			if !g.HasCDP[i] {
				continue
			}
			cdp := int64(g.CDP[i])
			if cdp < nav[0].Trace || cdp > nav[len(nav)-1].Trace {
				continue
			}
			delta := cdp - point.Trace
			if delta < 0 {
				delta = -delta
			}
			if delta < bestDelta {
				best, bestDelta = i, delta
			}
		}
		if best >= 0 {
			indices = append(indices, best)
		}
	}
	seen := make(map[int]bool)
	pairs := make([]calibrationPair, 0, len(indices))
	for _, index := range indices {
		if seen[index] {
			continue
		}
		seen[index] = true
		x, y, ok := interpolateNavigation(nav, int64(g.CDP[index]))
		if ok {
			pairs = append(pairs, calibrationPair{headerX: g.X[index], headerY: g.Y[index], navX: x, navY: y})
		}
	}
	return pairs
}

func interpolateNavigation(points []NavigationPoint, trace int64) (float64, float64, bool) {
	if len(points) < 2 || trace < points[0].Trace || trace > points[len(points)-1].Trace {
		return 0, 0, false
	}
	i := sort.Search(len(points), func(i int) bool { return points[i].Trace >= trace })
	if i == 0 || points[i].Trace == trace {
		return points[i].X, points[i].Y, true
	}
	a, b := points[i-1], points[i]
	if b.Trace == a.Trace {
		return b.X, b.Y, true
	}
	f := float64(trace-a.Trace) / float64(b.Trace-a.Trace)
	return a.X + f*(b.X-a.X), a.Y + f*(b.Y-a.Y), true
}

func navigationLength(points []NavigationPoint) float64 {
	length := 0.0
	for i := 1; i < len(points); i++ {
		length += math.Hypot(points[i].X-points[i-1].X, points[i].Y-points[i-1].Y)
	}
	return length
}

func ScaleGeometry(g *geometry.CrookedLineGeometry, multiplier float64) (*geometry.CrookedLineGeometry, error) {
	return transformGeometry(g, multiplier, 0, 0)
}

// TransformGeometry returns a calibrated copy without mutating the raw
// header geometry kept in the .cidx/memory cache.
func TransformGeometry(g *geometry.CrookedLineGeometry, calibration CoordinateCalibration) (*geometry.CrookedLineGeometry, error) {
	return transformGeometry(g, calibration.Multiplier, calibration.OffsetX, calibration.OffsetY)
}

func transformGeometry(g *geometry.CrookedLineGeometry, multiplier, offsetX, offsetY float64) (*geometry.CrookedLineGeometry, error) {
	if g == nil || multiplier <= 0 || !finite(multiplier) || !finite(offsetX) || !finite(offsetY) {
		return nil, errors.New("invalid geometry scale")
	}
	values := make([]segy.TraceCoordinate, 0, len(g.TraceIndices))
	for i, trace := range g.TraceIndices {
		value := segy.TraceCoordinate{Trace: trace, X: g.X[i]*multiplier + offsetX, Y: g.Y[i]*multiplier + offsetY, Valid: true, Units: g.CoordinateUnits}
		if i < len(g.CDP) && i < len(g.HasCDP) && g.HasCDP[i] {
			value.CDP, value.HasCDP = g.CDP[i], true
		}
		values = append(values, value)
	}
	return geometry.NewCrookedLine(values, g.Spec)
}

func naturalLess(a, b string) bool {
	ara, arb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	for ia, ib := 0, 0; ia < len(ara) && ib < len(arb); {
		if unicode.IsDigit(ara[ia]) && unicode.IsDigit(arb[ib]) {
			ja, jb := ia, ib
			for ja < len(ara) && unicode.IsDigit(ara[ja]) {
				ja++
			}
			for jb < len(arb) && unicode.IsDigit(arb[jb]) {
				jb++
			}
			na := strings.TrimLeft(string(ara[ia:ja]), "0")
			nb := strings.TrimLeft(string(arb[ib:jb]), "0")
			if na == "" {
				na = "0"
			}
			if nb == "" {
				nb = "0"
			}
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			ia, ib = ja, jb
			continue
		}
		if ara[ia] != arb[ib] {
			return ara[ia] < arb[ib]
		}
		ia++
		ib++
	}
	return len(ara) < len(arb)
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
