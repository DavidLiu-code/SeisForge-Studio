package prestack

import (
	"math"
	"testing"
)

func TestAcquisitionPointsFallbackToCoordinatesOnIDConflict(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{
		{TraceNumber: 0, SourceID: 7, SourceX: 100, SourceY: 10, HasSource: true, ReceiverID: 11, ReceiverX: 200, ReceiverY: 20, HasReceiver: true, Offset: 100, HasOffset: true},
		{TraceNumber: 1, SourceID: 7, SourceX: 110, SourceY: 10, HasSource: true, ReceiverID: 11, ReceiverX: 200, ReceiverY: 20, HasReceiver: true, Offset: 110, HasOffset: true},
		{TraceNumber: 2, SourceID: 8, SourceX: 100, SourceY: 10, HasSource: true, ReceiverID: 12, ReceiverX: 210, ReceiverY: 20, HasReceiver: true, Offset: 120, HasOffset: true},
	}}
	idx.buildAcquisition()
	if !idx.SourceUsesCoordinates {
		t.Fatal("source ID conflict was not detected")
	}
	sources := idx.SourcePoints()
	if len(sources) != 2 || !sources[0].Key.Coordinate {
		t.Fatalf("source points did not fall back to XY: %+v", sources)
	}
	if len(idx.ReceiverPoints()) != 2 {
		t.Fatalf("receiver points=%d, want 2", len(idx.ReceiverPoints()))
	}
	association, ok := idx.AcquisitionAssociation(GatherShot, sources[0].Key)
	if !ok || len(association.TraceIndices) != 2 || association.TraceIndices[0] != 0 || len(association.CounterpartIndices) != 2 {
		t.Fatalf("source association=%+v ok=%v", association, ok)
	}
	link, ok := idx.SourceReceiverAssociation(1)
	if !ok || link.SourcePoint != 1 || link.ReceiverPoint != 0 {
		t.Fatalf("trace link=%+v ok=%v", link, ok)
	}
}

func TestAcquisitionPointNearestAndScreenPick(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{
		{TraceNumber: 0, SourceID: 1, SourceX: 0, SourceY: 0, HasSource: true},
		{TraceNumber: 1, SourceID: 2, SourceX: 10, SourceY: 0, HasSource: true},
		{TraceNumber: 2, SourceID: 3, SourceX: 100, SourceY: 0, HasSource: true},
	}}
	idx.buildAcquisition()
	nearest, ok := idx.NearestAcquisitionPoint(GatherShot, 8.9, 0.5, 2)
	if !ok || nearest.Point.ID != 2 || math.Abs(nearest.DistancePx-math.Hypot(1.1, .5)) > 1e-9 {
		t.Fatalf("nearest=%+v ok=%v", nearest, ok)
	}
	projected, ok := idx.PickAcquisitionPoint(GatherShot, 110, 50, AcquisitionScreenTransform{OriginX: 100, OriginY: 50, ScaleX: 1, ScaleY: -1}, 1)
	if !ok || projected.Point.ID != 2 || projected.DistancePx != 0 {
		t.Fatalf("screen pick=%+v ok=%v", projected, ok)
	}
	if _, ok := idx.NearestAcquisitionPoint(GatherShot, 8.9, 0.5, .5); ok {
		t.Fatal("nearest point outside tolerance was accepted")
	}
}
