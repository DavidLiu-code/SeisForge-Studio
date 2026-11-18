package prestack

import "testing"

func TestAzimuthGatherBinsAndMissingValues(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{
		{TraceNumber: 0, Azimuth: 10.2, HasAzimuth: true},
		{TraceNumber: 1, Azimuth: 10.4, HasAzimuth: true},
		{TraceNumber: 2, Azimuth: 11.4, HasAzimuth: true},
		{TraceNumber: 3, Azimuth: 22, HasAzimuth: true},
		{TraceNumber: 4},
	}}
	keys := idx.AvailableGathers(GatherAzimuth)
	if len(keys) != 3 {
		t.Fatalf("azimuth keys=%d, want 3", len(keys))
	}
	if !keys[0].AzimuthBin || keys[0].AzimuthCenter != 10 {
		t.Fatalf("first azimuth key=%+v", keys[0])
	}
	if got := keys[0].String(); got != "Azimuth 10 ±0.5°" {
		t.Fatalf("key string=%q", got)
	}
	result, err := idx.Gather(GatherSelection{Type: GatherAzimuth, Key: keys[0]})
	if err != nil {
		t.Fatal(err)
	}
	assertTraceOrder(t, result.TraceIndices, 0, 1)
	if result.AzimuthRange.Min != 10.2 || result.AzimuthRange.Max != 10.4 {
		t.Fatalf("range=%+v", result.AzimuthRange)
	}
}

func TestAzimuthPrimaryAllRangeGroupsThenSecondary(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{
		{TraceNumber: 0, Azimuth: 22.1, HasAzimuth: true, Offset: 40, HasOffset: true},
		{TraceNumber: 1, Azimuth: 10.7, HasAzimuth: true, Offset: 30, HasOffset: true},
		{TraceNumber: 2, Azimuth: 10.2, HasAzimuth: true, Offset: 10, HasOffset: true},
		{TraceNumber: 3, Azimuth: 22.4, HasAzimuth: true, Offset: 20, HasOffset: true},
	}}
	result, err := idx.Gather(GatherSelection{Type: GatherAzimuth, Key: GatherKey{All: true}, Secondary: SecondaryOffset})
	if err != nil {
		t.Fatal(err)
	}
	// Primary azimuth bins are ordered first; each bin then uses Offset.
	assertTraceOrder(t, result.TraceIndices, 2, 1, 3, 0)
}

func TestAzimuthConfiguredWidth(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{
		{TraceNumber: 0, Azimuth: 10.2, HasAzimuth: true},
		{TraceNumber: 1, Azimuth: 15.1, HasAzimuth: true},
	}}
	keys := idx.AvailableGathersAzimuthConfigured(10)
	if len(keys) != 2 || keys[0].AzimuthCenter != 10 || keys[1].AzimuthCenter != 20 {
		t.Fatalf("configured keys=%+v", keys)
	}
	result, err := idx.Gather(GatherSelection{Type: GatherAzimuth, Key: keys[0], AzimuthBinSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	assertTraceOrder(t, result.TraceIndices, 0)
}
