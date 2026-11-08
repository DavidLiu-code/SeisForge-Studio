package prestack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCompareFilterAll32CombinationsPreserveReaderSelectionOrder(t *testing.T) {
	dir := t.TempDir()
	aOptions, bOptions := acceptanceClassifiedOptions()
	_, aIndex := openAcceptanceIndex(t, filepath.Join(dir, "A_classified.sgy"), aOptions, DefaultHeaderMapping())
	_, bIndex := openAcceptanceIndex(t, filepath.Join(dir, "B_classified.sgy"), bOptions, DefaultHeaderMapping())

	// Neither selection is physical order. In particular, A physical trace 0
	// is not its first row, and B's unmatched tail is not sorted by trace or
	// category. Reuse the real Reader fixture, not a hand-built match result.
	a, b := make([]PrestackTraceRecord, acceptanceTraceCount), make([]PrestackTraceRecord, acceptanceTraceCount)
	bPhysicalByLogical, bPositionByPhysical := make(map[int]int64), make(map[int64]int)
	for physical, logical := range acceptanceOrder(true) {
		bPhysicalByLogical[logical] = int64(physical)
	}
	for position := range a {
		a[position] = aIndex.Records[(position*19+11)%acceptanceTraceCount]
		b[position] = bIndex.Records[(position*11+23)%acceptanceTraceCount]
		bPositionByPhysical[b[position].TraceNumber] = position
	}
	result := MatchRecords(a, b)
	if len(result.Pairs) != 56 || len(result.AOnly) != 1 || len(result.BOnly) != 1 ||
		len(result.AmbiguousA) != 2 || len(result.AmbiguousB) != 2 || len(result.InvalidA) != 1 || len(result.InvalidB) != 1 {
		t.Fatalf("unexpected classification fixture: %+v", result)
	}
	originalResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	originalA, originalB := append([]PrestackTraceRecord(nil), a...), append([]PrestackTraceRecord(nil), b...)

	// Build an independent oracle from the fixture's known logical mutations.
	// Do not use Allows, LogicalRows, or a previous filtered result as the
	// expected order: those would hide a shared filtering/ordering regression.
	classForLogical := func(logical int, sideB bool) (CompareRowClass, uint32) {
		switch logical {
		case 0, 1:
			return CompareRowAmbiguous, 8
		case 2:
			return CompareRowInvalid, 16
		case 59:
			if sideB {
				return CompareRowBOnly, 4
			}
			return CompareRowAOnly, 2
		default:
			return CompareRowMatched, 1
		}
	}
	bLogicalByPhysical := acceptanceOrder(true)
	for mask := uint32(0); mask < 32; mask++ {
		t.Run(fmt.Sprintf("mask_%05b", mask), func(t *testing.T) {
			filters := CompareFilterSet{Matched: mask&1 != 0, AOnly: mask&2 != 0, BOnly: mask&4 != 0,
				Ambiguous: mask&8 != 0, Invalid: mask&16 != 0}
			var want []CompareLogicalRow
			for position, record := range a {
				logical := int(record.TraceNumber)
				class, bit := classForLogical(logical, false)
				if mask&bit == 0 {
					continue
				}
				row := CompareLogicalRow{Classification: class, ATrace: record.TraceNumber, AIndex: position, HasA: true}
				if class == CompareRowMatched {
					row.BTrace, row.HasB, row.HasKey = bPhysicalByLogical[logical], true, true
					row.BIndex = bPositionByPhysical[row.BTrace]
					row.Key = TraceMatchKey{Strategy: MatchKeySourceReceiverID, SourceID: record.SourceID, ReceiverID: record.ReceiverID}
				}
				want = append(want, row)
			}
			for position, record := range b {
				class, bit := classForLogical(bLogicalByPhysical[int(record.TraceNumber)], true)
				if class == CompareRowMatched || mask&bit == 0 {
					continue
				}
				want = append(want, CompareLogicalRow{Classification: class, BTrace: record.TraceNumber, BIndex: position, HasB: true})
			}
			wantCount := 0
			for _, category := range []struct {
				bit   uint32
				count int
			}{{1, 56}, {2, 1}, {4, 1}, {8, 4}, {16, 2}} {
				if mask&category.bit != 0 {
					wantCount += category.count
				}
			}
			var first []CompareLogicalRow
			for repeat := 0; repeat < 10; repeat++ {
				got := FilterCompareRows(result, a, b, filters)
				if len(got) != wantCount || !reflect.DeepEqual(got, want) {
					t.Fatalf("repeat %d: rows=%d, want %d in exact A/B selection order\ngot: %+v\nwant: %+v", repeat, len(got), wantCount, got, want)
				}
				if repeat == 0 {
					first = append([]CompareLogicalRow(nil), got...)
				} else if !reflect.DeepEqual(got, first) {
					t.Fatalf("repeat %d changed identical filtering output", repeat)
				}
				after, err := json.Marshal(result)
				if err != nil || !bytes.Equal(after, originalResult) {
					t.Fatalf("repeat %d mutated the original match result: %v", repeat, err)
				}
				if !reflect.DeepEqual(a, originalA) || !reflect.DeepEqual(b, originalB) {
					t.Fatalf("repeat %d mutated input metadata selections", repeat)
				}
			}
		})
	}
}
