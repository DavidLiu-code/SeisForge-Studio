package prestack

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

func copyScannedIndex(p *PrestackIndex) *PrestackIndex {
	return &PrestackIndex{SourcePath: p.SourcePath, Mapping: p.Mapping,
		Records:           append([]PrestackTraceRecord(nil), p.Records...),
		BinarySampleCount: p.BinarySampleCount, BinarySampleIntervalUS: p.BinarySampleIntervalUS}
}

func TestIndexScanAndRebuildPreserveRawMidpointAndDerivedSemantics(t *testing.T) {
	reader, err := segy.Open(prestackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	mapping := DefaultHeaderMapping()
	var scanProgress []IndexProgress
	scanned, err := scanIndex(nil, reader, mapping, func(p IndexProgress) { scanProgress = append(scanProgress, p) })
	if err != nil {
		t.Fatal(err)
	}
	// Record 1 deliberately has different mapped CDP midpoint and computed
	// Source/Receiver midpoint in this real-Reader fixture.
	raw := scanned.Records[1]
	if raw.MidpointX != 1025 || raw.MidpointY != 2005 || raw.HasMidpoint ||
		len(scanned.Bins) != 0 || len(scanned.sourcePoints) != 0 || len(scanned.Warnings) != 0 ||
		!reflect.DeepEqual(scanned.tables, [3]gatherTable{}) || scanned.quality.TotalTraces != 0 {
		t.Fatalf("scan stage already derived/overwrote metadata: record=%+v", raw)
	}
	for _, p := range scanProgress {
		if p.Stage != "扫描道头" {
			t.Fatalf("scan stage reported derived completion: %+v", p)
		}
	}
	want, err := BuildIndex(context.Background(), reader, mapping, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, scannedCount := range []int64{int64(len(scanned.Records)), 0, 0} {
		fresh := copyScannedIndex(scanned)
		var progress []IndexProgress
		if err := rebuildIndex(nil, fresh, scannedCount, func(p IndexProgress) { progress = append(progress, p) }); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fresh, want) {
			t.Fatal("fresh rebuild changed records/gathers/QC/associations relative to BuildIndex")
		}
		if fresh.Records[1].MidpointX == raw.MidpointX || scanned.Records[1] != raw {
			t.Fatal("derived midpoint missing or scan-stage snapshot was mutated")
		}
		if len(progress) != 2 || progress[0].Stage != "建立道集" || progress[1].Stage != "完成" ||
			progress[1].Done != int64(len(scanned.Records)) || progress[1].Scanned != scannedCount {
			t.Fatalf("cold/hit progress lost actual scanned count: %+v", progress)
		}
		for _, kind := range []GatherType{GatherCMP, GatherShot, GatherReceiver, GatherOffset, GatherAzimuth, GatherRaw} {
			selection := GatherSelection{Type: kind, Key: GatherKey{All: kind != GatherRaw}}
			gotGather, gotErr := fresh.Gather(selection)
			wantGather, wantErr := want.Gather(selection)
			if gotErr != nil || wantErr != nil || !reflect.DeepEqual(gotGather, wantGather) {
				t.Fatalf("%s changed after metadata rebuild: %v / %v", kind, gotErr, wantErr)
			}
		}
	}
	// If Source/Receiver fields are intentionally absent, the raw mapped CDP
	// midpoint must remain available as the existing valid fallback geometry.
	fallback := copyScannedIndex(scanned)
	fallback.Mapping.SourceXByte, fallback.Mapping.SourceYByte = 0, 0
	fallback.Mapping.ReceiverXByte, fallback.Mapping.ReceiverYByte = 0, 0
	if err := rebuildIndex(nil, fallback, 0, nil); err != nil {
		t.Fatal(err)
	}
	if !fallback.Records[1].HasMidpoint || fallback.Records[1].MidpointX != raw.MidpointX || fallback.Records[1].MidpointY != raw.MidpointY {
		t.Fatal("raw CDP fallback midpoint lost between scan and derived rebuild")
	}
}

func TestIndexStagesCancellationDoesNotPublishCompletion(t *testing.T) {
	reader, err := segy.Open(prestackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := scanIndex(ctx, reader, DefaultHeaderMapping(), nil); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan published index: %v / %v", result != nil, err)
	}
	scanned, err := scanIndex(nil, reader, DefaultHeaderMapping(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cancelInitially := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelInitially {
			cancel()
		}
		finished := false
		err := rebuildIndex(ctx, copyScannedIndex(scanned), 0, func(p IndexProgress) {
			if p.Stage == "建立道集" {
				cancel()
			}
			finished = finished || p.Stage == "完成"
		})
		cancel()
		if !errors.Is(err, context.Canceled) || finished {
			t.Fatalf("cancelled rebuild reported completion: initial=%v err=%v", cancelInitially, err)
		}
	}
}
