//go:build windows

package main

import (
	"testing"

	prestackcore "github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

func TestPrestackMultiKeyCommandIDsAndRanges(t *testing.T) {
	keys := []prestackcore.GatherKey{{All: true}, {ID: 100}, {ID: 102}, {ID: 105}, {ID: 110}}
	got, err := parsePrestackMultiKeyCommand("100,105-110", keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != 100 || got[1].ID != 105 || got[2].ID != 110 {
		t.Fatalf("keys=%v", got)
	}
	got, err = parsePrestackMultiKeyCommand("#2,#4-#5", keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != 100 || got[1].ID != 105 || got[2].ID != 110 {
		t.Fatalf("positions=%v", got)
	}
}

func TestPrestackMultiKeyCommandOffsetAndCMPXY(t *testing.T) {
	keys := []prestackcore.GatherKey{
		{All: true},
		{OffsetBin: true, OffsetCenter: -40},
		{OffsetBin: true, OffsetCenter: 0},
		{OffsetBin: true, OffsetCenter: 40},
	}
	got, err := parsePrestackMultiKeyCommand("-40--1,40", keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].OffsetCenter != -40 || got[1].OffsetCenter != 40 {
		t.Fatalf("offset=%v", got)
	}
	cmp := []prestackcore.GatherKey{{CMPBin: true, CMPBinXIndex: 1, CMPBinYIndex: 2}}
	got, err = parsePrestackMultiKeyCommand("1:2", cmp)
	if err != nil || len(got) != 1 || got[0].CMPBinXIndex != 1 || got[0].CMPBinYIndex != 2 {
		t.Fatalf("cmp=%v err=%v", got, err)
	}
}

func TestPrestackMultiKeyCommandAzimuth(t *testing.T) {
	keys := []prestackcore.GatherKey{
		{All: true},
		{AzimuthBin: true, AzimuthCenter: 10},
		{AzimuthBin: true, AzimuthCenter: 20},
		{AzimuthBin: true, AzimuthCenter: 30},
	}
	got, err := parsePrestackMultiKeyCommand("10-20", keys)
	if err != nil || len(got) != 2 || got[0].AzimuthCenter != 10 || got[1].AzimuthCenter != 20 {
		t.Fatalf("azimuth=%v err=%v", got, err)
	}
}
