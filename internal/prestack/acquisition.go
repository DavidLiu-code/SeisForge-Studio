package prestack

// Acquisition metadata is deliberately separate from amplitude rendering.
// It gives geometry viewers a stable Source/Receiver association model while
// keeping physical trace order and all raw header values intact.

import (
	"math"
	"sort"
)

// AcquisitionPoint is one unique source or receiver location.  TraceIndices
// contains zero-based physical SEG-Y trace numbers in acquisition order.
// CounterpartIndices contains unique point indexes on the opposite side and
// is useful for highlighting an entire shot/receiver fan from a geometry map.
type AcquisitionPoint struct {
	Key                GatherKey
	ID                 int32
	X, Y               float64
	HasCoordinates     bool
	TraceIndices       []int64
	RecordIndices      []int
	CounterpartIndices []int
	// Role-specific aliases for CounterpartIndices. Source points populate
	// ReceiverIndices; receiver points populate SourceIndices.
	ReceiverIndices []int
	SourceIndices   []int
	OffsetRange     ValueRange
	AzimuthRange    ValueRange
}

// SourcePoint and ReceiverPoint are semantic aliases for callers that want
// the acquisition role visible in their API or UI code.
type SourcePoint = AcquisitionPoint
type ReceiverPoint = AcquisitionPoint

// SourceReceiverLink associates one physical trace with both acquisition
// points. Point indexes are stable for the lifetime of a PrestackIndex.
type SourceReceiverLink struct {
	// TraceIndex/SourceIndex/ReceiverIndex are the stable zero-based indexes
	// exposed by the acquisition API. The legacy-named fields below are kept
	// for compatibility with the first MVP callers.
	TraceIndex                 int64
	SourceIndex, ReceiverIndex int
	TraceNumber                int64
	SourcePoint                int
	ReceiverPoint              int
	SourceID, ReceiverID       int32
	SourceX, SourceY           float64
	ReceiverX, ReceiverY       float64
	MidpointX, MidpointY       float64
	Offset, Azimuth            float64
}

// AcquisitionAssociation is the selected point plus the physical traces and
// opposite-side points that belong to it.
type AcquisitionAssociation struct {
	Point              AcquisitionPoint
	TraceIndices       []int64
	RecordIndices      []int
	CounterpartIndices []int
}

// AcquisitionScreenTransform maps world XY to screen coordinates.  The
// origin is the screen position of world (0,0); use a negative ScaleY for a
// conventional top-left screen coordinate system.
type AcquisitionScreenTransform struct {
	OriginX, OriginY float64
	ScaleX, ScaleY   float64
}

func (t AcquisitionScreenTransform) Project(x, y float64) (float64, float64) {
	return t.OriginX + x*t.ScaleX, t.OriginY + y*t.ScaleY
}

// AcquisitionPick is returned by PickAcquisitionPoint and includes the
// screen-space distance used for hit testing.
type AcquisitionPick struct {
	Kind       GatherType
	PointIndex int
	Point      AcquisitionPoint
	DistancePx float64
}

type acquisitionSpatialIndex struct {
	cellSize float64
	buckets  map[[2]int][]int
}

func acquisitionXYKey(kind GatherType, r PrestackTraceRecord, coordinate bool) (GatherKey, bool) {
	if kind == GatherShot {
		if coordinate {
			if !r.HasSource || !finiteXY(r.SourceX, r.SourceY) {
				return GatherKey{}, false
			}
			return GatherKey{Coordinate: true, X: r.SourceX, Y: r.SourceY}, true
		}
		if r.SourceID == 0 {
			return GatherKey{}, false
		}
		return GatherKey{ID: r.SourceID}, true
	}
	if kind == GatherReceiver {
		if coordinate {
			if !r.HasReceiver || !finiteXY(r.ReceiverX, r.ReceiverY) {
				return GatherKey{}, false
			}
			return GatherKey{Coordinate: true, X: r.ReceiverX, Y: r.ReceiverY}, true
		}
		if r.ReceiverID == 0 {
			return GatherKey{}, false
		}
		return GatherKey{ID: r.ReceiverID}, true
	}
	return GatherKey{}, false
}

func finiteXY(x, y float64) bool {
	return !math.IsNaN(x) && !math.IsInf(x, 0) && !math.IsNaN(y) && !math.IsInf(y, 0)
}

// buildAcquisition must be called after buildTables has populated HasSource,
// HasReceiver and ReceiverUsesCoordinates. It is intentionally independent
// of BuildIndex/decodeRecord so alternate index builders can reuse it.
func (p *PrestackIndex) buildAcquisition() {
	if p == nil {
		return
	}
	p.sourcePoints = nil
	p.receiverPoints = nil
	p.sourcePointByTrace = make([]int, len(p.Records))
	p.receiverPointByTrace = make([]int, len(p.Records))
	for i := range p.Records {
		p.sourcePointByTrace[i], p.receiverPointByTrace[i] = -1, -1
	}
	p.SourceUsesCoordinates = acquisitionIDConflicts(p.Records, GatherShot) || !acquisitionHasIDs(p.Records, GatherShot)
	// Existing gather-table logic may intentionally force receiver XY when a
	// channel-like Receiver ID is not globally stable. Keep that decision.
	receiverCoordinates := p.ReceiverUsesCoordinates || acquisitionIDConflicts(p.Records, GatherReceiver) || !acquisitionHasIDs(p.Records, GatherReceiver)
	p.sourcePoints = p.buildAcquisitionPoints(GatherShot, p.SourceUsesCoordinates)
	p.receiverPoints = p.buildAcquisitionPoints(GatherReceiver, receiverCoordinates)
	// Populate the counterpart lists once so SourcePoints/ReceiverPoints are
	// useful on their own; AcquisitionAssociation still recomputes defensively
	// from the trace maps when a caller asks for a specific key.
	for i := range p.Records {
		s, r := p.sourcePointByTrace[i], p.receiverPointByTrace[i]
		if s >= 0 && r >= 0 {
			appendUniqueInt(&p.sourcePoints[s].CounterpartIndices, r)
			appendUniqueInt(&p.sourcePoints[s].ReceiverIndices, r)
			appendUniqueInt(&p.receiverPoints[r].CounterpartIndices, s)
			appendUniqueInt(&p.receiverPoints[r].SourceIndices, s)
		}
	}
	for i := range p.sourcePoints {
		sort.Ints(p.sourcePoints[i].CounterpartIndices)
		sort.Ints(p.sourcePoints[i].ReceiverIndices)
	}
	for i := range p.receiverPoints {
		sort.Ints(p.receiverPoints[i].CounterpartIndices)
		sort.Ints(p.receiverPoints[i].SourceIndices)
	}
	p.sourceSpatial = makeAcquisitionSpatialIndex(p.sourcePoints)
	p.receiverSpatial = makeAcquisitionSpatialIndex(p.receiverPoints)
}

func appendUniqueInt(dst *[]int, value int) {
	for _, current := range *dst {
		if current == value {
			return
		}
	}
	*dst = append(*dst, value)
}

func acquisitionIDConflicts(records []PrestackTraceRecord, kind GatherType) bool {
	locations := make(map[int32][2]float64)
	seenIDs := make(map[int32]bool)
	for _, r := range records {
		var id int32
		var x, y float64
		var valid bool
		if kind == GatherShot {
			id, x, y, valid = r.SourceID, r.SourceX, r.SourceY, r.HasSource && finiteXY(r.SourceX, r.SourceY)
		} else {
			id, x, y, valid = r.ReceiverID, r.ReceiverX, r.ReceiverY, r.HasReceiver && finiteXY(r.ReceiverX, r.ReceiverY)
		}
		if id == 0 {
			continue
		}
		seenIDs[id] = true
		if !valid {
			continue
		}
		if old, ok := locations[id]; ok && (old[0] != x || old[1] != y) {
			return true
		}
		locations[id] = [2]float64{x, y}
	}
	_ = seenIDs
	return false
}

func acquisitionHasIDs(records []PrestackTraceRecord, kind GatherType) bool {
	seen := false
	for _, r := range records {
		if kind == GatherShot {
			if !r.HasSource {
				continue
			}
			seen = true
			if r.SourceID == 0 {
				return false
			}
		} else {
			if !r.HasReceiver {
				continue
			}
			seen = true
			if r.ReceiverID == 0 {
				return false
			}
		}
	}
	return seen
}

func (p *PrestackIndex) buildAcquisitionPoints(kind GatherType, coordinate bool) []AcquisitionPoint {
	groups := make(map[GatherKey]int)
	var points []AcquisitionPoint
	for i := range p.Records {
		r := p.Records[i]
		key, ok := acquisitionXYKey(kind, r, coordinate)
		if !ok {
			continue
		}
		pointIndex, exists := groups[key]
		if !exists {
			pointIndex = len(points)
			x, y, hasCoordinates := key.X, key.Y, key.Coordinate
			if !hasCoordinates {
				if kind == GatherShot {
					x, y, hasCoordinates = r.SourceX, r.SourceY, r.HasSource && finiteXY(r.SourceX, r.SourceY)
				} else {
					x, y, hasCoordinates = r.ReceiverX, r.ReceiverY, r.HasReceiver && finiteXY(r.ReceiverX, r.ReceiverY)
				}
			}
			point := AcquisitionPoint{Key: key, ID: key.ID, X: x, Y: y, HasCoordinates: hasCoordinates}
			points = append(points, point)
			groups[key] = pointIndex
		}
		point := &points[pointIndex]
		point.TraceIndices = append(point.TraceIndices, r.TraceNumber)
		point.RecordIndices = append(point.RecordIndices, i)
		if r.HasOffset {
			point.OffsetRange.add(r.Offset)
		}
		if r.HasAzimuth {
			point.AzimuthRange.add(r.Azimuth)
		}
		if kind == GatherShot {
			p.sourcePointByTrace[i] = pointIndex
		} else {
			p.receiverPointByTrace[i] = pointIndex
		}
	}
	// Preserve physical acquisition order for point membership, then sort
	// points by their stable key only when constructing a spatial view would
	// otherwise depend on map iteration. Record-to-point indexes are rebuilt by
	// the caller's arrays below, so no exposed index changes after this point.
	return points
}

func makeAcquisitionSpatialIndex(points []AcquisitionPoint) acquisitionSpatialIndex {
	idx := acquisitionSpatialIndex{buckets: make(map[[2]int][]int)}
	if len(points) == 0 {
		return idx
	}
	minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	valid := 0
	for _, point := range points {
		if !point.HasCoordinates {
			continue
		}
		minX, maxX = math.Min(minX, point.X), math.Max(maxX, point.X)
		minY, maxY = math.Min(minY, point.Y), math.Max(maxY, point.Y)
		valid++
	}
	if valid == 0 {
		return idx
	}
	span := math.Max(maxX-minX, maxY-minY)
	if span <= 0 || math.IsInf(span, 0) {
		span = 1
	}
	idx.cellSize = span / 32
	if idx.cellSize <= 0 {
		idx.cellSize = 1
	}
	for i, point := range points {
		if !point.HasCoordinates {
			continue
		}
		cell := [2]int{int(math.Floor(point.X / idx.cellSize)), int(math.Floor(point.Y / idx.cellSize))}
		idx.buckets[cell] = append(idx.buckets[cell], i)
	}
	return idx
}

func (idx acquisitionSpatialIndex) candidates(x, y, radius float64, total int) []int {
	if idx.cellSize <= 0 || len(idx.buckets) == 0 || radius <= 0 {
		out := make([]int, total)
		for i := range out {
			out[i] = i
		}
		return out
	}
	cx, cy := int(math.Floor(x/idx.cellSize)), int(math.Floor(y/idx.cellSize))
	span := int(math.Ceil(radius/idx.cellSize)) + 1
	seen := make(map[int]struct{})
	for dx := -span; dx <= span; dx++ {
		for dy := -span; dy <= span; dy++ {
			for _, point := range idx.buckets[[2]int{cx + dx, cy + dy}] {
				seen[point] = struct{}{}
			}
		}
	}
	out := make([]int, 0, len(seen))
	for point := range seen {
		out = append(out, point)
	}
	return out
}

func (p *PrestackIndex) acquisitionPoints(kind GatherType) []AcquisitionPoint {
	if p == nil {
		return nil
	}
	if kind == GatherShot {
		return p.sourcePoints
	}
	if kind == GatherReceiver {
		return p.receiverPoints
	}
	return nil
}

func cloneAcquisitionPoint(point AcquisitionPoint) AcquisitionPoint {
	point.TraceIndices = append([]int64(nil), point.TraceIndices...)
	point.RecordIndices = append([]int(nil), point.RecordIndices...)
	point.CounterpartIndices = append([]int(nil), point.CounterpartIndices...)
	point.ReceiverIndices = append([]int(nil), point.ReceiverIndices...)
	point.SourceIndices = append([]int(nil), point.SourceIndices...)
	return point
}

// AcquisitionPoints returns a defensive copy of source or receiver points.
func (p *PrestackIndex) AcquisitionPoints(kind GatherType) []AcquisitionPoint {
	points := p.acquisitionPoints(kind)
	out := make([]AcquisitionPoint, len(points))
	for i := range points {
		out[i] = cloneAcquisitionPoint(points[i])
	}
	return out
}

func (p *PrestackIndex) SourcePoints() []SourcePoint     { return p.AcquisitionPoints(GatherShot) }
func (p *PrestackIndex) ReceiverPoints() []ReceiverPoint { return p.AcquisitionPoints(GatherReceiver) }

// AcquisitionAssociation resolves a point key and returns all physical
// traces and opposite acquisition points associated with it.
func (p *PrestackIndex) AcquisitionAssociation(kind GatherType, key GatherKey) (AcquisitionAssociation, bool) {
	if p == nil {
		return AcquisitionAssociation{}, false
	}
	points := p.acquisitionPoints(kind)
	for _, point := range points {
		if point.Key != key {
			continue
		}
		point = cloneAcquisitionPoint(point)
		seen := make(map[int]struct{})
		for _, recordIndex := range point.RecordIndices {
			if recordIndex < 0 || recordIndex >= len(p.Records) {
				continue
			}
			counterpart := -1
			if recordIndex < len(p.receiverPointByTrace) {
				counterpart = p.receiverPointByTrace[recordIndex]
			}
			if kind == GatherReceiver {
				counterpart = -1
				if recordIndex < len(p.sourcePointByTrace) {
					counterpart = p.sourcePointByTrace[recordIndex]
				}
			}
			if counterpart >= 0 {
				seen[counterpart] = struct{}{}
			}
		}
		point.CounterpartIndices = point.CounterpartIndices[:0]
		for counterpart := range seen {
			point.CounterpartIndices = append(point.CounterpartIndices, counterpart)
		}
		sort.Ints(point.CounterpartIndices)
		if kind == GatherShot {
			point.ReceiverIndices = append(point.ReceiverIndices[:0], point.CounterpartIndices...)
		} else {
			point.SourceIndices = append(point.SourceIndices[:0], point.CounterpartIndices...)
		}
		return AcquisitionAssociation{Point: point, TraceIndices: append([]int64(nil), point.TraceIndices...), RecordIndices: append([]int(nil), point.RecordIndices...), CounterpartIndices: append([]int(nil), point.CounterpartIndices...)}, true
	}
	return AcquisitionAssociation{}, false
}

// SourceReceiverAssociation resolves a physical zero-based trace number.
func (p *PrestackIndex) SourceReceiverAssociation(trace int64) (SourceReceiverLink, bool) {
	if p == nil || trace < 0 || trace >= int64(len(p.Records)) {
		return SourceReceiverLink{}, false
	}
	record := int(trace)
	if record >= len(p.sourcePointByTrace) || record >= len(p.receiverPointByTrace) {
		return SourceReceiverLink{}, false
	}
	source, receiver := p.sourcePointByTrace[record], p.receiverPointByTrace[record]
	if source < 0 && receiver < 0 {
		return SourceReceiverLink{}, false
	}
	r := p.Records[record]
	return SourceReceiverLink{TraceIndex: trace, SourceIndex: source, ReceiverIndex: receiver,
		TraceNumber: trace, SourcePoint: source, ReceiverPoint: receiver,
		SourceID: r.SourceID, ReceiverID: r.ReceiverID,
		SourceX: r.SourceX, SourceY: r.SourceY, ReceiverX: r.ReceiverX, ReceiverY: r.ReceiverY,
		MidpointX: r.MidpointX, MidpointY: r.MidpointY, Offset: r.Offset, Azimuth: r.Azimuth}, true
}

// NearestAcquisitionPoint returns the nearest XY point. maxDistance <= 0
// means unbounded. Points without coordinates are never picked.
func (p *PrestackIndex) NearestAcquisitionPoint(kind GatherType, x, y, maxDistance float64) (AcquisitionPick, bool) {
	points := p.acquisitionPoints(kind)
	if len(points) == 0 || !finiteXY(x, y) {
		return AcquisitionPick{}, false
	}
	spatial := p.receiverSpatial
	if kind == GatherShot {
		spatial = p.sourceSpatial
	}
	best, bestDistance := -1, math.Inf(1)
	for _, index := range spatial.candidates(x, y, maxDistance, len(points)) {
		point := points[index]
		if !point.HasCoordinates {
			continue
		}
		distance := math.Hypot(point.X-x, point.Y-y)
		if distance < bestDistance {
			best, bestDistance = index, distance
		}
	}
	if best < 0 || (maxDistance > 0 && bestDistance > maxDistance) {
		return AcquisitionPick{}, false
	}
	return AcquisitionPick{Kind: kind, PointIndex: best, Point: cloneAcquisitionPoint(points[best]), DistancePx: bestDistance}, true
}

// PickAcquisitionPoint performs nearest-point hit testing in screen pixels;
// only points inside tolerance are accepted. Scale may be negative for a
// flipped Y screen axis. A non-positive tolerance means unbounded nearest.
func (p *PrestackIndex) PickAcquisitionPoint(kind GatherType, screenX, screenY float64, transform AcquisitionScreenTransform, tolerance float64) (AcquisitionPick, bool) {
	points := p.acquisitionPoints(kind)
	if len(points) == 0 || math.IsNaN(screenX) || math.IsNaN(screenY) {
		return AcquisitionPick{}, false
	}
	spatial := p.receiverSpatial
	if kind == GatherShot {
		spatial = p.sourceSpatial
	}
	// Convert a pixel tolerance to a conservative world radius for the
	// spatial candidate lookup. The final comparison remains in pixel space.
	worldRadius := 0.0
	if tolerance > 0 {
		sx, sy := math.Abs(transform.ScaleX), math.Abs(transform.ScaleY)
		if sx > 0 && sy > 0 {
			worldRadius = tolerance / math.Min(sx, sy)
		}
	}
	worldX, worldY := screenX, screenY
	if transform.ScaleX != 0 {
		worldX = (screenX - transform.OriginX) / transform.ScaleX
	}
	if transform.ScaleY != 0 {
		worldY = (screenY - transform.OriginY) / transform.ScaleY
	}
	best, bestDistance := -1, math.Inf(1)
	for _, index := range spatial.candidates(worldX, worldY, worldRadius, len(points)) {
		point := points[index]
		if !point.HasCoordinates {
			continue
		}
		px, py := transform.Project(point.X, point.Y)
		distance := math.Hypot(px-screenX, py-screenY)
		if distance < bestDistance {
			best, bestDistance = index, distance
		}
	}
	if best < 0 || (tolerance > 0 && bestDistance > tolerance) {
		return AcquisitionPick{}, false
	}
	return AcquisitionPick{Kind: kind, PointIndex: best, Point: cloneAcquisitionPoint(points[best]), DistancePx: bestDistance}, true
}
