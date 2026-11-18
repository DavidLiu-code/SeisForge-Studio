package prestack

import (
	"math"
	"testing"
)

func TestAvailableCMPGathersConfiguredUsesMinimumXYOrigin(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{
		{TraceNumber: 0, HasMidpoint: true, MidpointX: 105, MidpointY: 202},
		{TraceNumber: 1, HasMidpoint: true, MidpointX: 119, MidpointY: 206},
		{TraceNumber: 2, HasMidpoint: true, MidpointX: 125, MidpointY: 202},
		{TraceNumber: 3, HasMidpoint: false, MidpointX: 999, MidpointY: 999},
	}}
	keys := idx.AvailableCMPGathersConfigured(CMPBinConfig{Size: 20})
	if len(keys) != 2 {
		t.Fatalf("keys=%v", keys)
	}
	if keys[0].CMPBinXIndex != 0 || keys[0].CMPBinYIndex != 0 || keys[1].CMPBinXIndex != 1 || keys[1].CMPBinYIndex != 0 {
		t.Fatalf("keys order/indices=%v", keys)
	}
	if keys[0].CMPBinOriginX != 105 || keys[0].CMPBinOriginY != 202 {
		t.Fatalf("default origin=(%g,%g), want (105,202)", keys[0].CMPBinOriginX, keys[0].CMPBinOriginY)
	}
	if keys[0].CMPBinCenterX != 115 || keys[0].CMPBinCenterY != 212 {
		t.Fatalf("center=(%g,%g)", keys[0].CMPBinCenterX, keys[0].CMPBinCenterY)
	}
}

func TestCMPBinGatherPreservesPhysicalOrderAndSupportsExplicitOrigin(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{
		{TraceNumber: 8, HasMidpoint: true, MidpointX: 0, MidpointY: 0},
		{TraceNumber: 3, HasMidpoint: true, MidpointX: 19.9, MidpointY: 0},
		{TraceNumber: 5, HasMidpoint: true, MidpointX: 20.1, MidpointY: 0},
		{TraceNumber: 11, HasMidpoint: true, MidpointX: 0, MidpointY: 20.1},
	}}
	cfg := CMPBinConfig{Size: 20, OriginX: 0, OriginY: 0}
	keys := idx.AvailableCMPGathersConfigured(cfg)
	if len(keys) != 3 {
		t.Fatalf("keys=%v", keys)
	}
	result, err := idx.Gather(GatherSelection{Type: GatherCMP, Key: keys[0], CMPBin: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TraceIndices) != 2 || result.TraceIndices[0] != 8 || result.TraceIndices[1] != 3 {
		t.Fatalf("physical order=%v", result.TraceIndices)
	}
	// Reconstructing the selection with only the key must still work.
	var xBin GatherKey
	for _, key := range keys {
		if key.CMPBinXIndex == 1 && key.CMPBinYIndex == 0 {
			xBin = key
		}
	}
	reconstructed, err := idx.Gather(GatherSelection{Type: GatherCMP, Key: xBin})
	if err != nil {
		t.Fatal(err)
	}
	if len(reconstructed.TraceIndices) != 1 || reconstructed.TraceIndices[0] != 5 {
		t.Fatalf("reconstructed=%v", reconstructed.TraceIndices)
	}
	if reconstructed.Selection.CMPBin.Size != 20 {
		t.Fatalf("normalized selection=%+v", reconstructed.Selection.CMPBin)
	}
}

func TestCMPBinDisabledRetainsExactCMPKeys(t *testing.T) {
	idx := &PrestackIndex{Records: []PrestackTraceRecord{{TraceNumber: 0, HasMidpoint: true, MidpointX: 1, MidpointY: 1}}}
	idx.tables[GatherCMP].keys = []GatherKey{{ID: 10}}
	keys := idx.AvailableCMPGathersConfigured(CMPBinConfig{Size: 0})
	if len(keys) != 1 || keys[0].ID != 10 || keys[0].CMPBin {
		t.Fatalf("exact keys=%v", keys)
	}
	keys = idx.AvailableCMPGathersConfigured(CMPBinConfig{Size: math.NaN()})
	if len(keys) != 1 || keys[0].ID != 10 {
		t.Fatalf("NaN exact keys=%v", keys)
	}
}
