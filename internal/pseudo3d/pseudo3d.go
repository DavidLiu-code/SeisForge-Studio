// Package pseudo3d renders georeferenced 2-D seismic sections as vertical
// XY-time curtains. It deliberately has no Win32, dataset, or SEG-Y lifetime
// state so the existing regular-volume renderer remains untouched.
package pseudo3d

import (
	"errors"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
)

const (
	MaxTextureWidth  = 1024
	MaxTextureHeight = 1536
	TextureBudget    = int64(192 << 20)
	MaxCurtainPoints = 256
)

type Color struct{ R, G, B byte }

type Point struct{ X, Y float64 }

// XYRange is an axis-aligned world-coordinate selection. A zero-value range
// means that the whole project is eligible.
type XYRange struct {
	XMin, XMax float64
	YMin, YMax float64
	Valid      bool
}

// TimeRange is a project-wide absolute time selection in milliseconds. A
// zero-value range means the complete recorded time is eligible.
type TimeRange struct {
	StartMS, EndMS float64
	Valid          bool
}

func (r TimeRange) Normalized() TimeRange {
	if !r.Valid || !finite(r.StartMS) || !finite(r.EndMS) {
		return TimeRange{}
	}
	if r.StartMS > r.EndMS {
		r.StartMS, r.EndMS = r.EndMS, r.StartMS
	}
	if r.EndMS-r.StartMS <= 1e-9 {
		return TimeRange{}
	}
	return r
}

func (r TimeRange) Contains(timeMS float64) bool {
	r = r.Normalized()
	return r.Valid && finite(timeMS) && timeMS >= r.StartMS && timeMS <= r.EndMS
}

// Clamped limits a valid selection to an absolute project time interval.
// An empty intersection is returned as an invalid range.
func (r TimeRange) Clamped(minMS, maxMS float64) TimeRange {
	r = r.Normalized()
	if !r.Valid || !finite(minMS) || !finite(maxMS) || maxMS <= minMS {
		return TimeRange{}
	}
	r.StartMS = math.Max(r.StartMS, minMS)
	r.EndMS = math.Min(r.EndMS, maxMS)
	if r.EndMS-r.StartMS <= 1e-9 {
		return TimeRange{}
	}
	return r
}

// SampleWindow converts the absolute millisecond selection to the nearest
// inclusive SEG-Y sample interval. The returned real time interval is the
// exact interval represented by those samples.
func (r TimeRange) SampleWindow(sampleIntervalUS, sampleCount int) (start, end int, actual TimeRange, ok bool) {
	if sampleIntervalUS <= 0 || sampleCount < 2 {
		return 0, 0, TimeRange{}, false
	}
	maximum := float64(sampleCount-1) * float64(sampleIntervalUS) / 1000
	r = r.Normalized()
	if !r.Valid {
		return 0, sampleCount - 1, TimeRange{StartMS: 0, EndMS: maximum, Valid: true}, true
	}
	if r.EndMS < 0 || r.StartMS > maximum {
		return 0, 0, TimeRange{}, false
	}
	clampedStart, clampedEnd := math.Max(0, r.StartMS), math.Min(maximum, r.EndMS)
	start = clampInt(int(math.Round(clampedStart*1000/float64(sampleIntervalUS))), 0, sampleCount-1)
	end = clampInt(int(math.Round(clampedEnd*1000/float64(sampleIntervalUS))), 0, sampleCount-1)
	if end <= start {
		return 0, 0, TimeRange{}, false
	}
	actual = TimeRange{StartMS: float64(start) * float64(sampleIntervalUS) / 1000,
		EndMS: float64(end) * float64(sampleIntervalUS) / 1000, Valid: true}
	return start, end, actual, true
}

func (r XYRange) Normalized() XYRange {
	if !r.Valid || !finite(r.XMin) || !finite(r.XMax) || !finite(r.YMin) || !finite(r.YMax) {
		return XYRange{}
	}
	if r.XMin > r.XMax {
		r.XMin, r.XMax = r.XMax, r.XMin
	}
	if r.YMin > r.YMax {
		r.YMin, r.YMax = r.YMax, r.YMin
	}
	if r.XMax-r.XMin <= 1e-12 || r.YMax-r.YMin <= 1e-12 {
		return XYRange{}
	}
	return r
}

func (r XYRange) Contains(point Point) bool {
	r = r.Normalized()
	return r.Valid && point.X >= r.XMin && point.X <= r.XMax && point.Y >= r.YMin && point.Y <= r.YMax
}

// TraceRun is one continuous in-range part of a trajectory. TraceIndices and
// Positions contain the real SEG-Y support traces needed by
// RenderTracePositions. Points/U contain only the clipped curtain geometry;
// therefore support traces outside the range can never extend the curtain.
type TraceRun struct {
	TraceIndices      []int64
	Positions         []float64
	Points            []Point
	U                 []float64
	PositionStart     float64
	PositionEnd       float64
	Length            float64
	FirstSupportIndex int
	LastSupportIndex  int
}

// PolylineIntersectsRange performs a metadata-only coarse eligibility test.
// A non-valid range means the full polyline is eligible.
func PolylineIntersectsRange(points []Point, selected XYRange) bool {
	selected = selected.Normalized()
	if !selected.Valid {
		return len(points) >= 2
	}
	for i := 1; i < len(points); i++ {
		if _, _, ok := clipSegmentToRange(points[i-1], points[i], selected); ok {
			return true
		}
	}
	return false
}

// ClipTrajectory clips an acquisition-order trajectory into independent,
// continuous runs. It preserves exact boundary intersections and includes at
// most one outside support trace on either side of every run for amplitude
// interpolation.
func ClipTrajectory(traceIndices []int64, x, y, distance []float64, selected XYRange) ([]TraceRun, error) {
	if len(traceIndices) < 2 || len(traceIndices) != len(x) || len(x) != len(y) || len(x) != len(distance) {
		return nil, errors.New("trajectory coordinates are empty or unmatched")
	}
	selected = selected.Normalized()
	if !selected.Valid {
		points, u, err := DecimateTrajectory(x, y, distance, MaxCurtainPoints)
		if err != nil {
			return nil, err
		}
		return []TraceRun{{
			TraceIndices: append([]int64(nil), traceIndices...), Positions: append([]float64(nil), distance...),
			Points: points, U: u, PositionStart: distance[0], PositionEnd: distance[len(distance)-1],
			Length: distance[len(distance)-1] - distance[0], FirstSupportIndex: 0, LastSupportIndex: len(distance) - 1,
		}}, nil
	}
	for i := range x {
		if !finite(x[i]) || !finite(y[i]) || !finite(distance[i]) || (i > 0 && distance[i] < distance[i-1]) {
			return nil, errors.New("trajectory contains invalid or decreasing coordinates")
		}
	}
	type buildingRun struct {
		points, pointDistance     []float64
		firstSupport, lastSupport int
	}
	var built []buildingRun
	var current *buildingRun
	finish := func() {
		if current != nil && len(current.pointDistance) >= 2 && current.pointDistance[len(current.pointDistance)-1]-current.pointDistance[0] > 1e-12 {
			built = append(built, *current)
		}
		current = nil
	}
	appendVertex := func(run *buildingRun, point Point, position float64) {
		if len(run.pointDistance) > 0 {
			last := len(run.pointDistance) - 1
			lx, ly := run.points[last*2], run.points[last*2+1]
			if math.Abs(lx-point.X)+math.Abs(ly-point.Y)+math.Abs(run.pointDistance[last]-position) <= 1e-10 {
				return
			}
		}
		run.points = append(run.points, point.X, point.Y)
		run.pointDistance = append(run.pointDistance, position)
	}
	for i := 1; i < len(x); i++ {
		a, b := Point{x[i-1], y[i-1]}, Point{x[i], y[i]}
		t0, t1, ok := clipSegmentToRange(a, b, selected)
		if !ok || t1-t0 <= 1e-12 {
			finish()
			continue
		}
		startDistance := distance[i-1] + (distance[i]-distance[i-1])*t0
		endDistance := distance[i-1] + (distance[i]-distance[i-1])*t1
		entry := Point{X: a.X + (b.X-a.X)*t0, Y: a.Y + (b.Y-a.Y)*t0}
		exit := Point{X: a.X + (b.X-a.X)*t1, Y: a.Y + (b.Y-a.Y)*t1}
		if current == nil || math.Abs(current.pointDistance[len(current.pointDistance)-1]-startDistance) > 1e-9 {
			finish()
			current = &buildingRun{firstSupport: i - 1, lastSupport: i}
		} else {
			current.lastSupport = i
		}
		appendVertex(current, entry, startDistance)
		appendVertex(current, exit, endDistance)
		if t1 < 1-1e-12 {
			finish()
		}
	}
	finish()
	runs := make([]TraceRun, 0, len(built))
	for _, source := range built {
		start, end := source.pointDistance[0], source.pointDistance[len(source.pointDistance)-1]
		points := make([]Point, len(source.pointDistance))
		u := make([]float64, len(source.pointDistance))
		for i := range points {
			points[i] = Point{X: source.points[i*2], Y: source.points[i*2+1]}
			u[i] = (source.pointDistance[i] - start) / (end - start)
		}
		points, u = DecimatePath(points, u, MaxCurtainPoints)
		first, last := source.firstSupport, source.lastSupport
		runs = append(runs, TraceRun{
			TraceIndices: append([]int64(nil), traceIndices[first:last+1]...), Positions: append([]float64(nil), distance[first:last+1]...),
			Points: points, U: u, PositionStart: start, PositionEnd: end, Length: end - start,
			FirstSupportIndex: first, LastSupportIndex: last,
		})
	}
	return runs, nil
}

// DecimatePath bounds mesh complexity while preserving exact clipped
// endpoints and normalized texture coordinates.
func DecimatePath(points []Point, u []float64, maximum int) ([]Point, []float64) {
	if len(points) <= maximum || maximum < 2 {
		return append([]Point(nil), points...), append([]float64(nil), u...)
	}
	outPoints, outU := make([]Point, 0, maximum), make([]float64, 0, maximum)
	last := -1
	for i := 0; i < maximum; i++ {
		index := int(math.Round(float64(i) * float64(len(points)-1) / float64(maximum-1)))
		if index == last {
			continue
		}
		outPoints, outU = append(outPoints, points[index]), append(outU, u[index])
		last = index
	}
	return outPoints, outU
}

func clipSegmentToRange(a, b Point, selected XYRange) (float64, float64, bool) {
	t0, t1 := 0.0, 1.0
	dx, dy := b.X-a.X, b.Y-a.Y
	clip := func(p, q float64) bool {
		if math.Abs(p) <= 1e-15 {
			return q >= 0
		}
		r := q / p
		if p < 0 {
			if r > t1 {
				return false
			}
			if r > t0 {
				t0 = r
			}
		} else {
			if r < t0 {
				return false
			}
			if r < t1 {
				t1 = r
			}
		}
		return true
	}
	if !clip(-dx, a.X-selected.XMin) || !clip(dx, selected.XMax-a.X) ||
		!clip(-dy, a.Y-selected.YMin) || !clip(dy, selected.YMax-a.Y) {
		return 0, 0, false
	}
	return t0, t1, t1 >= t0
}

type Bounds struct {
	XMin, XMax float64
	YMin, YMax float64
	TimeMinMS  float64
	TimeMaxMS  float64
	Valid      bool
}

type Texture struct {
	Width, Height int
	Indices       []byte
}

func (t *Texture) Valid() bool {
	return t != nil && t.Width >= 2 && t.Height >= 2 && len(t.Indices) >= t.Width*t.Height
}

// Curtain.Points follow acquisition order and U stores the corresponding
// normalized accumulated-distance coordinate in the texture.
type Curtain struct {
	ID, Name    string
	Points      []Point
	U           []float64
	TimeStartMS float64
	TimeEndMS   float64
	// TimeMaxMS is retained for Phase 4-10 callers and cache fixtures. New
	// callers should populate TimeStartMS and TimeEndMS.
	TimeMaxMS   float64
	Texture     *Texture
	Highlighted bool
	Hovered     bool
}

type Scene struct {
	Curtains []Curtain
	Bounds   Bounds
	Style    DisplayStyle
}

// DisplayStyle controls only guides drawn around the seismic data. It never
// changes texture samples, palette values, camera projection, or depth tests.
type DisplayStyle int

const (
	StyleUnspecified DisplayStyle = iota
	StyleClean
	StyleCIGVis
	StyleInterpretation
	StyleStandard
)

func (style DisplayStyle) Normalized() DisplayStyle {
	if style < StyleClean || style > StyleStandard {
		return StyleStandard
	}
	return style
}

type Camera struct {
	Azimuth, Elevation float64
	FOV, Zoom          float64
	PanX, PanY         float64
	AxisXScale         float64
	AxisYScale         float64
	VerticalScale      float64
}

func DefaultCamera() Camera {
	return Camera{Azimuth: -145, Elevation: 24, FOV: 35, Zoom: 1, AxisXScale: 1, AxisYScale: 1, VerticalScale: .55}
}

func (c Camera) Normalized() Camera {
	if !finite(c.Azimuth) {
		c.Azimuth = -145
	}
	if !finite(c.Elevation) {
		c.Elevation = 24
	}
	c.Elevation = clamp(c.Elevation, -80, 80)
	if !finite(c.FOV) {
		c.FOV = 35
	}
	c.FOV = clamp(c.FOV, 0, 120)
	if !finite(c.Zoom) || c.Zoom <= 0 {
		c.Zoom = 1
	}
	c.Zoom = clamp(c.Zoom, .2, 8)
	if !finite(c.PanX) {
		c.PanX = 0
	}
	if !finite(c.PanY) {
		c.PanY = 0
	}
	if !finite(c.AxisXScale) || c.AxisXScale <= 0 {
		c.AxisXScale = 1
	}
	if !finite(c.AxisYScale) || c.AxisYScale <= 0 {
		c.AxisYScale = 1
	}
	c.AxisXScale = clamp(c.AxisXScale, .10, 8)
	c.AxisYScale = clamp(c.AxisYScale, .10, 8)
	if !finite(c.VerticalScale) || c.VerticalScale <= 0 {
		c.VerticalScale = .55
	}
	c.VerticalScale = clamp(c.VerticalScale, .10, 8)
	return c
}

type TextureRequest struct {
	ID              string
	Traces, Samples int
}

type TextureSize struct{ Width, Height int }

// PlanTextureSizes assigns one-byte indexed textures under a hard retained
// memory budget. It never imposes a line-count limit.
func PlanTextureSizes(requests []TextureRequest, budget int64) map[string]TextureSize {
	result := make(map[string]TextureSize, len(requests))
	if len(requests) == 0 {
		return result
	}
	if budget <= 0 {
		budget = TextureBudget
	}
	perLine := budget / int64(len(requests))
	if perLine < 4 {
		perLine = 4
	}
	for _, request := range requests {
		w := clampInt(request.Traces, 2, MaxTextureWidth)
		h := clampInt(request.Samples, 2, MaxTextureHeight)
		pixels := int64(w) * int64(h)
		if pixels > perLine {
			scale := math.Sqrt(float64(perLine) / float64(pixels))
			w = clampInt(int(math.Floor(float64(w)*scale)), 2, w)
			h = clampInt(int(math.Floor(float64(h)*scale)), 2, h)
			for int64(w)*int64(h) > perLine {
				if w >= h && w > 2 {
					w--
				} else if h > 2 {
					h--
				} else {
					break
				}
			}
		}
		result[request.ID] = TextureSize{Width: w, Height: h}
	}
	return result
}

// PlanTimeWindowTextureSizes preserves the full-record horizontal and
// per-sample vertical density while reducing retained height in proportion to
// the selected sample interval. A shorter time window therefore cannot spend
// the freed memory by silently upscaling itself back toward the global budget.
func PlanTimeWindowTextureSizes(requests []TextureRequest, fullSamples map[string]int, budget int64) map[string]TextureSize {
	fullRequests := make([]TextureRequest, 0, len(requests))
	for _, request := range requests {
		total := fullSamples[request.ID]
		if total < request.Samples {
			total = request.Samples
		}
		fullRequests = append(fullRequests, TextureRequest{ID: request.ID, Traces: request.Traces, Samples: total})
	}
	fullPlans := PlanTextureSizes(fullRequests, budget)
	result := make(map[string]TextureSize, len(requests))
	for _, request := range requests {
		plan := fullPlans[request.ID]
		total := fullSamples[request.ID]
		if total < request.Samples || total < 2 {
			total = request.Samples
		}
		height := plan.Height
		if request.Samples < total {
			height = int(math.Ceil(float64(plan.Height) * float64(request.Samples) / float64(total)))
		}
		maximumHeight := request.Samples
		if maximumHeight > MaxTextureHeight {
			maximumHeight = MaxTextureHeight
		}
		height = clampInt(height, 2, maximumHeight)
		result[request.ID] = TextureSize{Width: plan.Width, Height: height}
	}
	return result
}

// DecimateTrajectory keeps acquisition order, endpoints, and accumulated
// distance texture coordinates while bounding curtain mesh complexity.
func DecimateTrajectory(x, y, distance []float64, maximum int) ([]Point, []float64, error) {
	if len(x) < 2 || len(x) != len(y) || len(x) != len(distance) {
		return nil, nil, errors.New("trajectory coordinates are empty or unmatched")
	}
	if maximum < 2 {
		maximum = 2
	}
	count := len(x)
	if count > maximum {
		count = maximum
	}
	total := distance[len(distance)-1]
	if !finite(total) || total <= 0 {
		return nil, nil, errors.New("trajectory has zero accumulated distance")
	}
	points := make([]Point, 0, count)
	u := make([]float64, 0, count)
	last := -1
	for i := 0; i < count; i++ {
		index := int(math.Round(float64(i) * float64(len(x)-1) / float64(count-1)))
		if index == last {
			continue
		}
		if !finite(x[index]) || !finite(y[index]) || !finite(distance[index]) {
			return nil, nil, errors.New("trajectory contains a non-finite coordinate")
		}
		points = append(points, Point{X: x[index], Y: y[index]})
		u = append(u, clamp(distance[index]/total, 0, 1))
		last = index
	}
	if len(points) < 2 {
		return nil, nil, errors.New("trajectory decimation produced fewer than two points")
	}
	u[0], u[len(u)-1] = 0, 1
	return points, u, nil
}

type RenderStats struct {
	CurtainCount, TexturedCurtains int
	Triangles, Pixels              int
	Bounds                         Bounds
}

// ScreenPoint is expressed in renderer-local pixels. Depth follows the same
// ordering as the z-buffer: a smaller value is closer to the camera.
type ScreenPoint struct {
	X, Y, Depth float64
}

// LinkedCursor is the lightweight, generation-scoped position shared by the
// crooked 2-D section and the pseudo-3-D overlay. It contains navigation
// metadata only; moving the cursor never copies or rebuilds seismic samples.
type LinkedCursor struct {
	ProjectGeneration uint64
	LineID            string
	Position          int
	TraceIndex        int64
	CDP               int32
	HasCDP            bool
	X, Y              float64
	Distance          float64
	Sample            int
	TimeMS            float64
	Valid             bool
}

// PickResult identifies the frontmost textured curtain below one screen
// pixel. U follows the curtain acquisition direction and TimeFraction runs
// from the top (0) to the bottom (1) of the curtain.
type PickResult struct {
	CurtainIndex int
	CurtainID    string
	CurtainName  string
	U            float64
	TimeFraction float64
	TimeMS       float64
	World        Point
	Depth        float64
}

// FrameProjection contains the four XY corners at the top and bottom of the
// time box. Corner order is (xmin,ymin), (xmax,ymin), (xmax,ymax),
// (xmin,ymax).
type FrameProjection struct {
	Base, Top [4]ScreenPoint
	Bounds    Bounds
	Valid     bool
}

// CurtainProjection is used by the Win32 overlay layer to highlight a picked
// curtain without rebuilding its seismic texture.
type CurtainProjection struct {
	Top, Bottom []ScreenPoint
	Valid       bool
}

type vec3 struct{ x, y, z float64 }
type projected struct{ x, y, depth, invDepth float64 }

func add(a, b vec3) vec3           { return vec3{a.x + b.x, a.y + b.y, a.z + b.z} }
func scale(a vec3, s float64) vec3 { return vec3{a.x * s, a.y * s, a.z * s} }
func dot(a, b vec3) float64        { return a.x*b.x + a.y*b.y + a.z*b.z }
func cross(a, b vec3) vec3         { return vec3{a.y*b.z - a.z*b.y, a.z*b.x - a.x*b.z, a.x*b.y - a.y*b.x} }
func length(a vec3) float64        { return math.Sqrt(dot(a, a)) }
func normalize(a vec3) vec3 {
	n := length(a)
	if n <= 1e-12 {
		return vec3{}
	}
	return scale(a, 1/n)
}
func finite(v float64) bool           { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func SceneBounds(curtains []Curtain) Bounds {
	b := Bounds{XMin: math.Inf(1), XMax: math.Inf(-1), YMin: math.Inf(1), YMax: math.Inf(-1),
		TimeMinMS: math.Inf(1), TimeMaxMS: math.Inf(-1)}
	for _, curtain := range curtains {
		if interval, ok := curtainTimeRange(curtain); ok {
			b.TimeMinMS = math.Min(b.TimeMinMS, interval.StartMS)
			b.TimeMaxMS = math.Max(b.TimeMaxMS, interval.EndMS)
		}
		for _, point := range curtain.Points {
			if !finite(point.X) || !finite(point.Y) {
				continue
			}
			b.XMin = math.Min(b.XMin, point.X)
			b.XMax = math.Max(b.XMax, point.X)
			b.YMin = math.Min(b.YMin, point.Y)
			b.YMax = math.Max(b.YMax, point.Y)
			b.Valid = true
		}
	}
	if !finite(b.TimeMinMS) || !finite(b.TimeMaxMS) || b.TimeMaxMS-b.TimeMinMS <= 1e-9 {
		b.TimeMinMS, b.TimeMaxMS = 0, 1
	}
	return b
}

func boundsTimeRange(bounds Bounds) TimeRange {
	start, end := bounds.TimeMinMS, bounds.TimeMaxMS
	if !finite(start) || !finite(end) || end-start <= 1e-9 {
		start, end = 0, bounds.TimeMaxMS
	}
	if !finite(end) || end-start <= 1e-9 {
		start, end = 0, 1
	}
	return TimeRange{StartMS: start, EndMS: end, Valid: true}
}

func curtainTimeRange(curtain Curtain) (TimeRange, bool) {
	interval := TimeRange{StartMS: curtain.TimeStartMS, EndMS: curtain.TimeEndMS, Valid: true}.Normalized()
	if interval.Valid {
		return interval, true
	}
	if finite(curtain.TimeMaxMS) && curtain.TimeMaxMS > 0 {
		return TimeRange{StartMS: 0, EndMS: curtain.TimeMaxMS, Valid: true}, true
	}
	return TimeRange{}, false
}

// TimeFraction maps an absolute millisecond value into the current scene's
// normalized vertical coordinate.
func TimeFraction(bounds Bounds, timeMS float64) float64 {
	interval := boundsTimeRange(bounds)
	return clamp((timeMS-interval.StartMS)/(interval.EndMS-interval.StartMS), 0, 1)
}

// TimeAtFraction is the inverse of TimeFraction.
func TimeAtFraction(bounds Bounds, fraction float64) float64 {
	interval := boundsTimeRange(bounds)
	return interval.StartMS + clamp(fraction, 0, 1)*(interval.EndMS-interval.StartMS)
}

func curtainFractions(curtain Curtain, bounds Bounds) (start, end float64) {
	interval, ok := curtainTimeRange(curtain)
	if !ok {
		return 0, 1
	}
	return TimeFraction(bounds, interval.StartMS), TimeFraction(bounds, interval.EndMS)
}

type projector struct {
	camera                 Camera
	bounds                 Bounds
	right, up, toward      vec3
	distance               float64
	rawCenterX, rawCenterY float64
	scale                  float64
	centerX, centerY       float64
	xySpan                 float64
}

func newProjector(scene Scene, camera Camera, width, height int) projector {
	camera = camera.Normalized()
	bounds := scene.Bounds
	if !bounds.Valid {
		bounds = SceneBounds(scene.Curtains)
	}
	p := projector{camera: camera, bounds: bounds, centerX: float64(width)/2 + camera.PanX, centerY: float64(height)/2 + camera.PanY}
	p.xySpan = math.Max(bounds.XMax-bounds.XMin, bounds.YMax-bounds.YMin)
	if !finite(p.xySpan) || p.xySpan <= 0 {
		p.xySpan = 1
	}
	a := camera.Azimuth * math.Pi / 180
	e := camera.Elevation * math.Pi / 180
	p.toward = normalize(vec3{math.Cos(e) * math.Cos(a), math.Cos(e) * math.Sin(a), -math.Sin(e)})
	worldUp := vec3{0, 0, -1}
	p.right = normalize(cross(p.toward, worldUp))
	if length(p.right) < 1e-9 {
		p.right = vec3{1, 0, 0}
	}
	p.up = normalize(cross(p.right, p.toward))
	radius := .5 * math.Sqrt(camera.AxisXScale*camera.AxisXScale+camera.AxisYScale*camera.AxisYScale+camera.VerticalScale*camera.VerticalScale)
	if camera.FOV > .01 {
		half := clamp(camera.FOV, 1, 120) * math.Pi / 360
		p.distance = radius/math.Tan(half) + radius*1.10
	} else {
		p.distance = math.Inf(1)
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	partialTime := false
	for _, curtain := range scene.Curtains {
		timeStart, timeEnd := curtainFractions(curtain, bounds)
		partialTime = partialTime || timeStart > 1e-9 || timeEnd < 1-1e-9
		for _, point := range curtain.Points {
			for _, timeFraction := range []float64{timeStart, timeEnd} {
				rawX, rawY, _ := p.raw(point, timeFraction)
				minX, maxX = math.Min(minX, rawX), math.Max(maxX, rawX)
				minY, maxY = math.Min(minY, rawY), math.Max(maxY, rawY)
			}
		}
	}
	if partialTime {
		corners := [4]Point{{bounds.XMin, bounds.YMin}, {bounds.XMax, bounds.YMin}, {bounds.XMax, bounds.YMax}, {bounds.XMin, bounds.YMax}}
		for _, corner := range corners {
			for _, timeFraction := range []float64{0, 1} {
				rawX, rawY, _ := p.raw(corner, timeFraction)
				minX, maxX = math.Min(minX, rawX), math.Max(maxX, rawX)
				minY, maxY = math.Min(minY, rawY), math.Max(maxY, rawY)
			}
		}
	}
	if !finite(minX) || maxX-minX < 1e-9 || maxY-minY < 1e-9 {
		minX, maxX, minY, maxY = -.5, .5, -.5, .5
	}
	p.rawCenterX, p.rawCenterY = (minX+maxX)/2, (minY+maxY)/2
	availableW, availableH := math.Max(1, float64(width)*.88), math.Max(1, float64(height)*.86)
	p.scale = math.Min(availableW/math.Max(maxX-minX, 1e-6), availableH/math.Max(maxY-minY, 1e-6)) * camera.Zoom
	return p
}

func (p projector) world(point Point, timeFraction float64) vec3 {
	cx, cy := (p.bounds.XMin+p.bounds.XMax)/2, (p.bounds.YMin+p.bounds.YMax)/2
	return vec3{(point.X - cx) / p.xySpan * p.camera.AxisXScale, (point.Y - cy) / p.xySpan * p.camera.AxisYScale, clamp(timeFraction, 0, 1) * p.camera.VerticalScale}
}

func (p projector) raw(point Point, timeFraction float64) (x, y, depth float64) {
	world := p.world(point, timeFraction)
	x = dot(world, p.right)
	y = -dot(world, p.up)
	q := dot(world, p.toward)
	if p.camera.FOV > .01 {
		depth = math.Max(1e-6, p.distance-q)
		k := p.distance / depth
		x, y = x*k, y*k
	} else {
		depth = -q
	}
	return
}

func (p projector) project(point Point, timeFraction float64) projected {
	x, y, depth := p.raw(point, timeFraction)
	inv := 1.0
	if p.camera.FOV > .01 {
		inv = 1 / math.Max(depth, 1e-6)
	}
	return projected{x: p.centerX + (x-p.rawCenterX)*p.scale, y: p.centerY + (y-p.rawCenterY)*p.scale, depth: depth, invDepth: inv}
}

func screenPoint(point projected) ScreenPoint {
	return ScreenPoint{X: point.x, Y: point.y, Depth: point.depth}
}

// ProjectFrame returns the exact screen-space box used by Render.
func ProjectFrame(scene Scene, camera Camera, width, height int) FrameProjection {
	if width < 2 || height < 2 {
		return FrameProjection{}
	}
	if !scene.Bounds.Valid {
		scene.Bounds = SceneBounds(scene.Curtains)
	}
	if !scene.Bounds.Valid {
		return FrameProjection{}
	}
	p := newProjector(scene, camera, width, height)
	b := scene.Bounds
	corners := [4]Point{{b.XMin, b.YMin}, {b.XMax, b.YMin}, {b.XMax, b.YMax}, {b.XMin, b.YMax}}
	frame := FrameProjection{Bounds: b, Valid: true}
	for i, corner := range corners {
		frame.Base[i] = screenPoint(p.project(corner, 0))
		frame.Top[i] = screenPoint(p.project(corner, 1))
	}
	return frame
}

// ProjectRange projects an arbitrary real-coordinate XY rectangle through the
// current scene projector. It is used for a lightweight AOI resize preview;
// unlike changing Scene.Bounds, it does not move or rescale loaded curtains.
func ProjectRange(scene Scene, camera Camera, width, height int, selected XYRange) FrameProjection {
	selected = selected.Normalized()
	if width < 2 || height < 2 || !selected.Valid {
		return FrameProjection{}
	}
	if !scene.Bounds.Valid {
		scene.Bounds = SceneBounds(scene.Curtains)
	}
	if !scene.Bounds.Valid {
		return FrameProjection{}
	}
	p := newProjector(scene, camera, width, height)
	corners := [4]Point{{selected.XMin, selected.YMin}, {selected.XMax, selected.YMin}, {selected.XMax, selected.YMax}, {selected.XMin, selected.YMax}}
	frame := FrameProjection{Bounds: Bounds{XMin: selected.XMin, XMax: selected.XMax, YMin: selected.YMin, YMax: selected.YMax,
		TimeMinMS: scene.Bounds.TimeMinMS, TimeMaxMS: scene.Bounds.TimeMaxMS, Valid: true}, Valid: true}
	for i, corner := range corners {
		frame.Base[i] = screenPoint(p.project(corner, 0))
		frame.Top[i] = screenPoint(p.project(corner, 1))
	}
	return frame
}

// ProjectTimeRange projects an absolute time interval through the current
// scene projector. Base is the interval start and Top is the interval end.
func ProjectTimeRange(scene Scene, camera Camera, width, height int, selected TimeRange) FrameProjection {
	selected = selected.Normalized()
	if width < 2 || height < 2 || !selected.Valid {
		return FrameProjection{}
	}
	if !scene.Bounds.Valid {
		scene.Bounds = SceneBounds(scene.Curtains)
	}
	if !scene.Bounds.Valid {
		return FrameProjection{}
	}
	available := boundsTimeRange(scene.Bounds)
	selected = selected.Clamped(available.StartMS, available.EndMS)
	if !selected.Valid {
		return FrameProjection{}
	}
	p := newProjector(scene, camera, width, height)
	b := scene.Bounds
	corners := [4]Point{{b.XMin, b.YMin}, {b.XMax, b.YMin}, {b.XMax, b.YMax}, {b.XMin, b.YMax}}
	frame := FrameProjection{Bounds: b, Valid: true}
	frame.Bounds.TimeMinMS, frame.Bounds.TimeMaxMS = selected.StartMS, selected.EndMS
	start, end := TimeFraction(scene.Bounds, selected.StartMS), TimeFraction(scene.Bounds, selected.EndMS)
	for i, corner := range corners {
		frame.Base[i] = screenPoint(p.project(corner, start))
		frame.Top[i] = screenPoint(p.project(corner, end))
	}
	return frame
}

// UnprojectFrameTime maps a screen point to the nearest vertical edge of the
// scene frame and returns its absolute millisecond coordinate. It is intended
// for lightweight time-face dragging, not curtain picking.
func UnprojectFrameTime(scene Scene, camera Camera, width, height int, screenX, screenY float64) (float64, bool) {
	if width < 2 || height < 2 {
		return 0, false
	}
	if !scene.Bounds.Valid {
		scene.Bounds = SceneBounds(scene.Curtains)
	}
	if !scene.Bounds.Valid {
		return 0, false
	}
	p := newProjector(scene, camera, width, height)
	b := scene.Bounds
	corners := [4]Point{{b.XMin, b.YMin}, {b.XMax, b.YMin}, {b.XMax, b.YMax}, {b.XMin, b.YMax}}
	bestFraction, bestDistance := 0.0, math.Inf(1)
	for _, corner := range corners {
		lo, hi := 0.0, 1.0
		distanceAt := func(fraction float64) float64 {
			point := p.project(corner, fraction)
			return math.Hypot(point.x-screenX, point.y-screenY)
		}
		for iteration := 0; iteration < 28; iteration++ {
			left, right := lo+(hi-lo)/3, hi-(hi-lo)/3
			if distanceAt(left) <= distanceAt(right) {
				hi = right
			} else {
				lo = left
			}
		}
		fraction := (lo + hi) / 2
		if distance := distanceAt(fraction); distance < bestDistance {
			bestFraction, bestDistance = fraction, distance
		}
	}
	if !finite(bestDistance) {
		return 0, false
	}
	return TimeAtFraction(scene.Bounds, bestFraction), true
}

// ProjectCurtain returns top and bottom screen-space polylines for a curtain.
func ProjectCurtain(scene Scene, camera Camera, width, height, curtainIndex int) CurtainProjection {
	if width < 2 || height < 2 || curtainIndex < 0 || curtainIndex >= len(scene.Curtains) {
		return CurtainProjection{}
	}
	if !scene.Bounds.Valid {
		scene.Bounds = SceneBounds(scene.Curtains)
	}
	curtain := scene.Curtains[curtainIndex]
	if !scene.Bounds.Valid || len(curtain.Points) < 2 {
		return CurtainProjection{}
	}
	p := newProjector(scene, camera, width, height)
	timeStart, timeEnd := curtainFractions(curtain, scene.Bounds)
	result := CurtainProjection{Top: make([]ScreenPoint, len(curtain.Points)), Bottom: make([]ScreenPoint, len(curtain.Points)), Valid: true}
	for i, point := range curtain.Points {
		result.Top[i] = screenPoint(p.project(point, timeStart))
		result.Bottom[i] = screenPoint(p.project(point, timeEnd))
	}
	return result
}

// ProjectPoint uses exactly the same projector as Render and Pick. The input
// time fraction runs from the top (0) to the bottom (1) of the scene.
func ProjectPoint(scene Scene, camera Camera, width, height int, point Point, timeFraction float64) (ScreenPoint, bool) {
	if width < 2 || height < 2 || !finite(point.X) || !finite(point.Y) || !finite(timeFraction) {
		return ScreenPoint{}, false
	}
	if !scene.Bounds.Valid {
		scene.Bounds = SceneBounds(scene.Curtains)
	}
	if !scene.Bounds.Valid {
		return ScreenPoint{}, false
	}
	projected := screenPoint(newProjector(scene, camera, width, height).project(point, timeFraction))
	return projected, finite(projected.X) && finite(projected.Y) && finite(projected.Depth)
}

// UnprojectTimePlane maps a renderer-local screen pixel to the real XY plane
// at the requested normalized time. It is the analytic inverse of project.
func UnprojectTimePlane(scene Scene, camera Camera, width, height int, screenX, screenY, timeFraction float64) (Point, bool) {
	if width < 2 || height < 2 {
		return Point{}, false
	}
	if !scene.Bounds.Valid {
		scene.Bounds = SceneBounds(scene.Curtains)
	}
	if !scene.Bounds.Valid {
		return Point{}, false
	}
	p := newProjector(scene, camera, width, height)
	if !finite(p.scale) || math.Abs(p.scale) <= 1e-12 {
		return Point{}, false
	}
	rx := p.rawCenterX + (screenX-p.centerX)/p.scale
	ry := p.rawCenterY + (screenY-p.centerY)/p.scale
	wz := clamp(timeFraction, 0, 1) * p.camera.VerticalScale
	a, b := p.right, scale(p.up, -1)
	if p.camera.FOV > .01 {
		if !finite(p.distance) || p.distance <= 1e-12 {
			return Point{}, false
		}
		a = add(a, scale(p.toward, rx/p.distance))
		b = add(b, scale(p.toward, ry/p.distance))
	}
	rhsA, rhsB := rx-a.z*wz, ry-b.z*wz
	determinant := a.x*b.y - a.y*b.x
	if math.Abs(determinant) <= 1e-12 {
		return Point{}, false
	}
	wx := (rhsA*b.y - a.y*rhsB) / determinant
	wy := (a.x*rhsB - rhsA*b.x) / determinant
	cx, cy := (p.bounds.XMin+p.bounds.XMax)/2, (p.bounds.YMin+p.bounds.YMax)/2
	if p.camera.AxisXScale <= 0 || p.camera.AxisYScale <= 0 {
		return Point{}, false
	}
	result := Point{X: cx + wx*p.xySpan/p.camera.AxisXScale, Y: cy + wy*p.xySpan/p.camera.AxisYScale}
	return result, finite(result.X) && finite(result.Y)
}

func sampleTriangleAt(screenX, screenY float64, a, b, c vertex, perspective bool) (u, v, depth float64, ok bool) {
	den := (b.p.y-c.p.y)*(a.p.x-c.p.x) + (c.p.x-b.p.x)*(a.p.y-c.p.y)
	if math.Abs(den) < 1e-12 {
		return 0, 0, 0, false
	}
	w0 := ((b.p.y-c.p.y)*(screenX-c.p.x) + (c.p.x-b.p.x)*(screenY-c.p.y)) / den
	w1 := ((c.p.y-a.p.y)*(screenX-c.p.x) + (a.p.x-c.p.x)*(screenY-c.p.y)) / den
	w2 := 1 - w0 - w1
	if w0 < -1e-7 || w1 < -1e-7 || w2 < -1e-7 {
		return 0, 0, 0, false
	}
	if perspective {
		inv := w0*a.p.invDepth + w1*b.p.invDepth + w2*c.p.invDepth
		if inv <= 1e-12 {
			return 0, 0, 0, false
		}
		depth = 1 / inv
		u = (w0*a.u*a.p.invDepth + w1*b.u*b.p.invDepth + w2*c.u*c.p.invDepth) * depth
		v = (w0*a.v*a.p.invDepth + w1*b.v*b.p.invDepth + w2*c.v*c.p.invDepth) * depth
	} else {
		depth = w0*a.p.depth + w1*b.p.depth + w2*c.p.depth
		u = w0*a.u + w1*b.u + w2*c.u
		v = w0*a.v + w1*b.v + w2*c.v
	}
	return clamp(u, 0, 1), clamp(v, 0, 1), depth, finite(depth)
}

// Pick returns the frontmost textured curtain at a renderer-local pixel.
func Pick(scene Scene, camera Camera, width, height int, screenX, screenY float64) (PickResult, bool) {
	if width < 2 || height < 2 || screenX < 0 || screenX >= float64(width) || screenY < 0 || screenY >= float64(height) {
		return PickResult{}, false
	}
	if !scene.Bounds.Valid {
		scene.Bounds = SceneBounds(scene.Curtains)
	}
	if !scene.Bounds.Valid {
		return PickResult{}, false
	}
	p := newProjector(scene, camera, width, height)
	best := PickResult{Depth: math.Inf(1)}
	found := false
	for curtainIndex, curtain := range scene.Curtains {
		if !curtain.Texture.Valid() || len(curtain.Points) < 2 || len(curtain.Points) != len(curtain.U) {
			continue
		}
		timeStart, timeEnd := curtainFractions(curtain, scene.Bounds)
		interval, intervalOK := curtainTimeRange(curtain)
		if !intervalOK {
			interval = boundsTimeRange(scene.Bounds)
		}
		for i := 0; i+1 < len(curtain.Points); i++ {
			top0 := vertex{p: p.project(curtain.Points[i], timeStart), u: curtain.U[i], v: 0}
			top1 := vertex{p: p.project(curtain.Points[i+1], timeStart), u: curtain.U[i+1], v: 0}
			bottom1 := vertex{p: p.project(curtain.Points[i+1], timeEnd), u: curtain.U[i+1], v: 1}
			bottom0 := vertex{p: p.project(curtain.Points[i], timeEnd), u: curtain.U[i], v: 1}
			triangles := [][3]vertex{{top0, top1, bottom1}, {top0, bottom1, bottom0}}
			for _, triangle := range triangles {
				u, v, depth, ok := sampleTriangleAt(screenX+.5, screenY+.5, triangle[0], triangle[1], triangle[2], p.camera.FOV > .01)
				if !ok || depth >= best.Depth {
					continue
				}
				timeMS := interval.StartMS + v*(interval.EndMS-interval.StartMS)
				world, worldOK := UnprojectTimePlane(scene, camera, width, height, screenX+.5, screenY+.5, TimeFraction(scene.Bounds, timeMS))
				if !worldOK {
					continue
				}
				best = PickResult{CurtainIndex: curtainIndex, CurtainID: curtain.ID, CurtainName: curtain.Name,
					U: u, TimeFraction: v, TimeMS: timeMS, World: world, Depth: depth}
				found = true
			}
		}
	}
	return best, found
}

type vertex struct {
	p    projected
	u, v float64
}

// ErrRenderCanceled is returned by RenderWithCancel when a newer scene or
// camera generation supersedes the work in progress.
var ErrRenderCanceled = errors.New("pseudo-3D render canceled")

// Render returns a top-down BGRA image suitable for StretchDIBits.
func Render(scene Scene, camera Camera, width, height int, palette [256]Color) ([]byte, RenderStats, error) {
	return RenderWithCancel(scene, camera, width, height, palette, nil)
}

// RenderWithCancel renders the same pixels as Render and checks stale at
// curtain boundaries. The callback must be cheap and safe to call from the
// render worker.
func RenderWithCancel(scene Scene, camera Camera, width, height int, palette [256]Color, stale func() bool) ([]byte, RenderStats, error) {
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if limit := height / 64; workers > limit {
		workers = limit
	}
	if workers < 1 || height < 192 || len(scene.Curtains) < 4 {
		workers = 1
	}
	return renderWithWorkers(scene, camera, width, height, palette, stale, workers)
}

// renderWithWorkers divides the output into disjoint horizontal bands. Every
// band traverses curtains and triangles in the same order as the serial
// renderer, so its Z-buffer decisions and final pixels remain identical.
func renderWithWorkers(scene Scene, camera Camera, width, height int, palette [256]Color, stale func() bool, workers int) ([]byte, RenderStats, error) {
	if width < 2 || height < 2 {
		return nil, RenderStats{}, errors.New("invalid pseudo-3D output dimensions")
	}
	if stale != nil && stale() {
		return nil, RenderStats{}, ErrRenderCanceled
	}
	if !scene.Bounds.Valid {
		scene.Bounds = SceneBounds(scene.Curtains)
	}
	image := make([]byte, width*height*4)
	for i := 0; i < len(image); i += 4 {
		image[i], image[i+1], image[i+2] = 250, 248, 245
	}
	stats := RenderStats{CurtainCount: len(scene.Curtains), Bounds: scene.Bounds}
	if !scene.Bounds.Valid {
		return image, stats, nil
	}
	p := newProjector(scene, camera, width, height)
	style := scene.Style.Normalized()
	zbuf := make([]float32, width*height)
	for i := range zbuf {
		zbuf[i] = float32(math.Inf(1))
	}
	if style != StyleClean {
		drawSceneAxes(image, width, height, p)
	}
	type projectedCurtain struct {
		curtain      Curtain
		top, bottom  []projected
		textureValid bool
		drawOutline  bool
		lineColor    Color
		lineWidth    int
	}
	prepared := make([]projectedCurtain, 0, len(scene.Curtains))
	for _, curtain := range scene.Curtains {
		if stale != nil && stale() {
			return nil, RenderStats{}, ErrRenderCanceled
		}
		if len(curtain.Points) >= 2 && len(curtain.Points) == len(curtain.U) && curtain.Texture.Valid() {
			stats.TexturedCurtains++
			stats.Triangles += 2 * (len(curtain.Points) - 1)
		}
		if len(curtain.Points) < 2 || len(curtain.Points) != len(curtain.U) {
			continue
		}
		timeStart, timeEnd := curtainFractions(curtain, scene.Bounds)
		item := projectedCurtain{curtain: curtain, top: make([]projected, len(curtain.Points)), bottom: make([]projected, len(curtain.Points)),
			textureValid: curtain.Texture.Valid()}
		for index, point := range curtain.Points {
			item.top[index], item.bottom[index] = p.project(point, timeStart), p.project(point, timeEnd)
		}
		item.drawOutline = style == StyleStandard || ((style == StyleCIGVis || style == StyleInterpretation) && (curtain.Highlighted || curtain.Hovered))
		item.lineColor, item.lineWidth = Color{R: 148, G: 163, B: 184}, 1
		if curtain.Highlighted {
			item.lineColor, item.lineWidth = Color{R: 37, G: 99, B: 235}, 2
		}
		if curtain.Hovered {
			item.lineColor, item.lineWidth = Color{R: 245, G: 122, B: 22}, 2
		}
		if style == StyleInterpretation && (curtain.Highlighted || curtain.Hovered) {
			item.lineWidth = 3
		}
		prepared = append(prepared, item)
	}
	if workers < 1 {
		workers = 1
	}
	if workers > height {
		workers = height
	}
	pixels := make([]int, workers)
	var canceled atomic.Bool
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		bandMin, bandMax := worker*height/workers, (worker+1)*height/workers-1
		wg.Add(1)
		go func(worker, bandMin, bandMax int) {
			defer wg.Done()
			written := 0
			for _, item := range prepared {
				if canceled.Load() || (stale != nil && stale()) {
					canceled.Store(true)
					return
				}
				curtain := item.curtain
				if item.textureValid {
					for i := 0; i+1 < len(curtain.Points); i++ {
						top0 := vertex{p: item.top[i], u: curtain.U[i], v: 0}
						top1 := vertex{p: item.top[i+1], u: curtain.U[i+1], v: 0}
						bottom1 := vertex{p: item.bottom[i+1], u: curtain.U[i+1], v: 1}
						bottom0 := vertex{p: item.bottom[i], u: curtain.U[i], v: 1}
						written += rasterTriangleBand(image, zbuf, width, height, bandMin, bandMax, top0, top1, bottom1, *curtain.Texture, palette, p.camera.FOV > .01)
						written += rasterTriangleBand(image, zbuf, width, height, bandMin, bandMax, top0, bottom1, bottom0, *curtain.Texture, palette, p.camera.FOV > .01)
					}
				}
				if item.drawOutline {
					for i := 0; i+1 < len(curtain.Points); i++ {
						a, b := item.top[i], item.top[i+1]
						drawLineBand(image, width, height, bandMin, bandMax, int(math.Round(a.x)), int(math.Round(a.y)), int(math.Round(b.x)), int(math.Round(b.y)), item.lineColor, item.lineWidth)
						if (curtain.Highlighted || curtain.Hovered) && item.textureValid {
							a, b = item.bottom[i], item.bottom[i+1]
							drawLineBand(image, width, height, bandMin, bandMax, int(math.Round(a.x)), int(math.Round(a.y)), int(math.Round(b.x)), int(math.Round(b.y)), item.lineColor, item.lineWidth)
						}
					}
				}
			}
			pixels[worker] = written
		}(worker, bandMin, bandMax)
	}
	wg.Wait()
	if canceled.Load() {
		return nil, RenderStats{}, ErrRenderCanceled
	}
	for _, count := range pixels {
		stats.Pixels += count
	}
	return image, stats, nil
}

func drawSceneAxes(image []byte, width, height int, p projector) {
	b := p.bounds
	corners := []Point{{b.XMin, b.YMin}, {b.XMax, b.YMin}, {b.XMax, b.YMax}, {b.XMin, b.YMax}}
	color := Color{R: 203, G: 213, B: 225}
	for i := range corners {
		a, next := corners[i], corners[(i+1)%len(corners)]
		pa, pb := p.project(a, 0), p.project(next, 0)
		drawLine(image, width, height, int(pa.x), int(pa.y), int(pb.x), int(pb.y), color, 1)
	}
	for _, corner := range corners {
		pa, pb := p.project(corner, 0), p.project(corner, 1)
		drawLine(image, width, height, int(pa.x), int(pa.y), int(pb.x), int(pb.y), color, 1)
	}
}

func rasterTriangle(image []byte, zbuf []float32, width, height int, a, b, c vertex, texture Texture, palette [256]Color, perspective bool) int {
	return rasterTriangleBand(image, zbuf, width, height, 0, height-1, a, b, c, texture, palette, perspective)
}

func rasterTriangleBand(image []byte, zbuf []float32, width, height, bandMinY, bandMaxY int, a, b, c vertex, texture Texture, palette [256]Color, perspective bool) int {
	minX := clampInt(int(math.Floor(math.Min(a.p.x, math.Min(b.p.x, c.p.x)))), 0, width-1)
	maxX := clampInt(int(math.Ceil(math.Max(a.p.x, math.Max(b.p.x, c.p.x)))), 0, width-1)
	rawMinY := int(math.Floor(math.Min(a.p.y, math.Min(b.p.y, c.p.y))))
	rawMaxY := int(math.Ceil(math.Max(a.p.y, math.Max(b.p.y, c.p.y))))
	clipMinY, clipMaxY := max(0, bandMinY), min(height-1, bandMaxY)
	if rawMaxY < clipMinY || rawMinY > clipMaxY || clipMaxY < clipMinY {
		return 0
	}
	minY := clampInt(rawMinY, clipMinY, clipMaxY)
	maxY := clampInt(rawMaxY, clipMinY, clipMaxY)
	den := (b.p.y-c.p.y)*(a.p.x-c.p.x) + (c.p.x-b.p.x)*(a.p.y-c.p.y)
	if math.Abs(den) < 1e-12 {
		return 0
	}
	written := 0
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			px, py := float64(x)+.5, float64(y)+.5
			w0 := ((b.p.y-c.p.y)*(px-c.p.x) + (c.p.x-b.p.x)*(py-c.p.y)) / den
			w1 := ((c.p.y-a.p.y)*(px-c.p.x) + (a.p.x-c.p.x)*(py-c.p.y)) / den
			w2 := 1 - w0 - w1
			if w0 < -1e-7 || w1 < -1e-7 || w2 < -1e-7 {
				continue
			}
			u, v, depth := 0.0, 0.0, 0.0
			if perspective {
				inv := w0*a.p.invDepth + w1*b.p.invDepth + w2*c.p.invDepth
				if inv <= 1e-12 {
					continue
				}
				depth = 1 / inv
				u = (w0*a.u*a.p.invDepth + w1*b.u*b.p.invDepth + w2*c.u*c.p.invDepth) * depth
				v = (w0*a.v*a.p.invDepth + w1*b.v*b.p.invDepth + w2*c.v*c.p.invDepth) * depth
			} else {
				depth = w0*a.p.depth + w1*b.p.depth + w2*c.p.depth
				u = w0*a.u + w1*b.u + w2*c.u
				v = w0*a.v + w1*b.v + w2*c.v
			}
			zi := y*width + x
			if float32(depth) >= zbuf[zi] {
				continue
			}
			zbuf[zi] = float32(depth)
			tx := clampInt(int(math.Round(clamp(u, 0, 1)*float64(texture.Width-1))), 0, texture.Width-1)
			ty := clampInt(int(math.Round(clamp(v, 0, 1)*float64(texture.Height-1))), 0, texture.Height-1)
			color := palette[texture.Indices[ty*texture.Width+tx]]
			di := zi * 4
			image[di], image[di+1], image[di+2], image[di+3] = color.B, color.G, color.R, 0
			written++
		}
	}
	return written
}

func drawLineBand(image []byte, width, height, bandMinY, bandMaxY, x0, y0, x1, y1 int, color Color, thickness int) {
	dx, dy := int(math.Abs(float64(x1-x0))), -int(math.Abs(float64(y1-y0)))
	sx, sy := -1, -1
	if x0 < x1 {
		sx = 1
	}
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		for oy := -thickness / 2; oy <= thickness/2; oy++ {
			for ox := -thickness / 2; ox <= thickness/2; ox++ {
				x, y := x0+ox, y0+oy
				if x >= 0 && x < width && y >= 0 && y < height && y >= bandMinY && y <= bandMaxY {
					i := (y*width + x) * 4
					image[i], image[i+1], image[i+2], image[i+3] = color.B, color.G, color.R, 0
				}
			}
		}
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func drawLine(image []byte, width, height, x0, y0, x1, y1 int, color Color, thickness int) {
	dx, dy := int(math.Abs(float64(x1-x0))), -int(math.Abs(float64(y1-y0)))
	sx, sy := -1, -1
	if x0 < x1 {
		sx = 1
	}
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		for oy := -thickness / 2; oy <= thickness/2; oy++ {
			for ox := -thickness / 2; ox <= thickness/2; ox++ {
				x, y := x0+ox, y0+oy
				if x >= 0 && x < width && y >= 0 && y < height {
					i := (y*width + x) * 4
					image[i], image[i+1], image[i+2], image[i+3] = color.B, color.G, color.R, 0
				}
			}
		}
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}
