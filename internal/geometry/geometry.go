// Package geometry defines dataset geometry independently from the workspace
// selected to display it. In particular, Regular3D is a data property and is
// deliberately not a synonym for the 3-D workspace.
package geometry

import (
	"fmt"
	"math"
	"runtime"
	"sort"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

type Kind uint8

const (
	KindUnknown Kind = iota
	KindLine2D
	KindRegular3D
	KindCrookedLine
)

func (k Kind) String() string {
	switch k {
	case KindLine2D:
		return "Line2D"
	case KindRegular3D:
		return "Regular3D"
	case KindCrookedLine:
		return "CrookedLine"
	default:
		return "Unknown"
	}
}

type TraceLocation struct {
	Trace     int64
	Inline    int32
	Crossline int32
	CDP       int32
	X         float64
	Y         float64
	Distance  float64
	HasGrid   bool
	HasXY     bool
	HasCDP    bool
}

type Bounds struct {
	TraceMin, TraceMax         int64
	InlineMin, InlineMax       int32
	CrosslineMin, CrosslineMax int32
	XMin, XMax                 float64
	YMin, YMax                 float64
	HasGrid                    bool
	HasXY                      bool
}

type Geometry interface {
	Kind() Kind
	TraceCount() int64
	TraceLocation(trace int64) (TraceLocation, bool)
	Bounds() Bounds
}

type Line2DGeometry struct {
	Count int64
}

func NewLine2D(traceCount int64) *Line2DGeometry {
	if traceCount < 0 {
		traceCount = 0
	}
	return &Line2DGeometry{Count: traceCount}
}

func (g *Line2DGeometry) Kind() Kind        { return KindLine2D }
func (g *Line2DGeometry) TraceCount() int64 { return g.Count }
func (g *Line2DGeometry) TraceLocation(trace int64) (TraceLocation, bool) {
	if trace < 0 || trace >= g.Count {
		return TraceLocation{}, false
	}
	return TraceLocation{Trace: trace}, true
}
func (g *Line2DGeometry) Bounds() Bounds {
	b := Bounds{}
	if g.Count > 0 {
		b.TraceMax = g.Count - 1
	}
	return b
}

// RegularGridGeometry is a zero-amplitude-copy adapter around the existing
// GeometryIndex. The index and its .lidx v1 cache remain owned by internal/segy.
type RegularGridGeometry struct {
	Index *segy.GeometryIndex
}

func WrapRegularGrid(index *segy.GeometryIndex) (*RegularGridGeometry, error) {
	if index == nil || len(index.InlineValues) == 0 || len(index.CrosslineValues) == 0 {
		return nil, fmt.Errorf("regular grid index is empty")
	}
	return &RegularGridGeometry{Index: index}, nil
}

func (g *RegularGridGeometry) Kind() Kind { return KindRegular3D }
func (g *RegularGridGeometry) TraceCount() int64 {
	if g == nil || g.Index == nil {
		return 0
	}
	return int64(len(g.Index.TraceNumbers))
}
func (g *RegularGridGeometry) TraceLocation(trace int64) (TraceLocation, bool) {
	if g == nil || g.Index == nil || trace < 0 {
		return TraceLocation{}, false
	}
	i := sort.Search(len(g.Index.TraceNumbers), func(i int) bool { return g.Index.TraceNumbers[i] >= trace })
	if i >= len(g.Index.TraceNumbers) || g.Index.TraceNumbers[i] != trace || i >= len(g.Index.RowOfTrace) || i >= len(g.Index.ColOfTrace) {
		return TraceLocation{}, false
	}
	row, col := int(g.Index.RowOfTrace[i]), int(g.Index.ColOfTrace[i])
	if row < 0 || row >= len(g.Index.InlineValues) || col < 0 || col >= len(g.Index.CrosslineValues) {
		return TraceLocation{}, false
	}
	return TraceLocation{Trace: trace, Inline: g.Index.InlineValues[row], Crossline: g.Index.CrosslineValues[col], HasGrid: true}, true
}
func (g *RegularGridGeometry) Bounds() Bounds {
	if g == nil || g.Index == nil {
		return Bounds{}
	}
	legacy := g.Index.Bounds()
	b := Bounds{InlineMin: legacy.InlineMin, InlineMax: legacy.InlineMax, CrosslineMin: legacy.CrosslineMin, CrosslineMax: legacy.CrosslineMax, HasGrid: true}
	if g.Index.TraceCount > 0 {
		b.TraceMax = g.Index.TraceCount - 1
	}
	return b
}
func (g *RegularGridGeometry) TraceAt(inline, crossline int32) (int64, bool) {
	if g == nil || g.Index == nil {
		return 0, false
	}
	return g.Index.TraceAt(inline, crossline)
}

const (
	RecommendationScoreMin      = 75.0
	RecommendationConfidenceMin = 0.58
)

type Detection struct {
	Kind       Kind
	Score      float64
	Confidence float64
	Reason     string
	Legacy     segy.GeometryDetectResult
	Crooked    segy.CoordinateDetectResult
}

func Detect(file *segy.File, maxHeaders int) (Detection, error) {
	if file == nil {
		return Detection{}, fmt.Errorf("nil SEG-Y reader")
	}
	legacy, err := file.DetectGeometryBytes(maxHeaders)
	if err != nil {
		return Detection{Kind: KindLine2D, Reason: err.Error()}, nil
	}
	// Overall confidence also measures whether the winning byte pair is unique.
	// Some valid processing outputs duplicate the same Inline word at two header
	// locations (for example bytes 21 and 193). That makes the byte choice
	// ambiguous without making the data any less three-dimensional. Use the best
	// candidate's own grid confidence for workspace recommendation while keeping
	// the full legacy result so the selected byte pair remains transparent.
	confidence := legacy.Confidence
	if len(legacy.Candidates) > 0 && legacy.Candidates[0].Confidence > confidence {
		confidence = legacy.Candidates[0].Confidence
	}
	d := Detection{Kind: KindLine2D, Score: legacy.Score, Confidence: confidence, Legacy: legacy}
	if d.Score >= RecommendationScoreMin && d.Confidence >= RecommendationConfidenceMin {
		d.Kind = KindRegular3D
		d.Reason = "regular grid confidence threshold met"
		return d, nil
	}
	coordinates, coordinateErr := file.DetectCoordinateSpec(maxHeaders)
	if coordinateErr == nil {
		d.Crooked = coordinates
		for _, candidate := range coordinates.Candidates {
			if candidate.Spec != coordinates.Spec {
				continue
			}
			if ok, reason := clearlyCrooked(candidate); ok {
				d.Kind = KindCrookedLine
				d.Score = math.Max(RecommendationScoreMin, candidate.Score)
				d.Confidence = math.Max(RecommendationConfidenceMin, candidate.ValidRatio)
				d.Reason = reason
				return d, nil
			} else {
				d.Reason = reason
			}
			break
		}
	}
	if d.Reason == "" {
		d.Reason = "regular grid and crooked-line confidence thresholds not met"
	}
	return d, nil
}

type BuildResult struct {
	Geometry  *RegularGridGeometry
	Detection Detection
	Stats     segy.GeometryBuildStats
}

func BuildRegular(file *segy.File, maxHeaders int) (BuildResult, error) {
	detection, err := Detect(file, maxHeaders)
	if err != nil {
		return BuildResult{}, err
	}
	if detection.Kind != KindRegular3D {
		return BuildResult{Detection: detection}, fmt.Errorf("regular 3-D geometry was not detected: %s", detection.Reason)
	}
	idx, stats, err := file.BuildGeometryIndexCached(detection.Legacy.InlineByte, detection.Legacy.CrosslineByte, runtime.NumCPU())
	if err != nil {
		return BuildResult{Detection: detection}, err
	}
	wrapped, err := WrapRegularGrid(idx)
	if err != nil {
		return BuildResult{Detection: detection, Stats: stats}, err
	}
	return BuildResult{Geometry: wrapped, Detection: detection, Stats: stats}, nil
}
