package prestack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

const acceptanceTraceCount = 5 * 12

func acceptanceOrder(shuffle bool) []int {
	order := make([]int, acceptanceTraceCount)
	for physical := range order {
		order[physical] = physical
		if shuffle {
			// 17 is coprime to 60, so every logical trace occurs exactly once.
			order[physical] = (physical*17 + 7) % acceptanceTraceCount
		}
	}
	return order
}

func acceptanceOptions(shuffle bool, amplitudeScale float32, samples, interval, delay int) testsegy.Options {
	order := acceptanceOrder(shuffle)
	headers := make([]testsegy.TraceHeader, len(order))
	for physical, logical := range order {
		shot, receiver := logical/12, logical%12
		scalar := []int16{0, 10, -10, 2, -2}[shot]
		rawCoordinate := func(value int) int32 {
			if scalar > 0 {
				return int32(value / int(scalar))
			}
			if scalar < 0 {
				return int32(value * -int(scalar))
			}
			return int32(value)
		}
		ry := 100
		if receiver == 0 {
			ry = 0 // A source and receiver at (0,0) are deliberately legal.
		}
		headers[physical] = testsegy.TraceHeader{
			SourceID: int32(1000 + shot), ReceiverID: int32(2000 + receiver),
			CDP: int32(3000 + (shot+receiver)%9), Offset: int32((receiver - 5) * 20),
			SourceX: rawCoordinate(shot * 100), SourceY: 0,
			ReceiverX: rawCoordinate(receiver * 20), ReceiverY: rawCoordinate(ry),
			Scalar: scalar, Units: 1, SampleCount: samples,
			SampleIntervalUS: interval, DelayMS: delay,
		}
	}
	return testsegy.Options{
		Cols: len(order), Samples: samples, SampleIntervalUS: interval, TraceHeaders: headers,
		SampleValue: func(physical, sample int) float32 {
			logical := order[physical]
			if sample < 3 {
				return float32(sample+1) * amplitudeScale
			}
			// The rest is a compact waveform suitable for manual GUI inspection.
			return float32(math.Sin(float64(sample)*0.17+float64(logical)*0.13)) * amplitudeScale
		},
	}
}

func openAcceptanceIndex(t *testing.T, path string, options testsegy.Options, mapping HeaderMapping) (*segy.File, *PrestackIndex) {
	t.Helper()
	if err := testsegy.Write(path, options); err != nil {
		t.Fatal(err)
	}
	reader, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	index, err := BuildIndex(context.Background(), reader, mapping, nil)
	if err != nil {
		t.Fatal(err)
	}
	return reader, index
}

func TestCompareAcceptanceReaderReorderedWaveformsAndSelections(t *testing.T) {
	dir := t.TempDir()
	aPath, bPath := filepath.Join(dir, "A.sgy"), filepath.Join(dir, "B_reordered.sgy")
	a, aIndex := openAcceptanceIndex(t, aPath, acceptanceOptions(false, 1, 256, 2000, 10), DefaultHeaderMapping())
	b, bIndex := openAcceptanceIndex(t, bPath, acceptanceOptions(true, .5, 256, 2000, 10), DefaultHeaderMapping())
	originalA, err := os.ReadFile(aPath)
	if err != nil {
		t.Fatal(err)
	}
	originalB, err := os.ReadFile(bPath)
	if err != nil {
		t.Fatal(err)
	}
	wantB := make(map[int]int64, acceptanceTraceCount)
	for physical, logical := range acceptanceOrder(true) {
		wantB[logical] = int64(physical)
	}
	result := MatchRecords(aIndex.Records, bIndex.Records)
	if result.Strategy != MatchKeySourceReceiverID || len(result.Pairs) != acceptanceTraceCount {
		t.Fatalf("complete reader fixture did not match by stable IDs: %+v", result)
	}
	for i, pair := range result.Pairs {
		if pair.ATrace != int64(i) || pair.BTrace != wantB[i] {
			t.Fatalf("logical trace %d mapped to %+v, expected B=%d", i, pair, wantB[i])
		}
		valuesA, err := a.ReadTraceWindow(pair.ATrace, 0, 6)
		if err != nil {
			t.Fatal(err)
		}
		valuesB, err := b.ReadTraceWindow(pair.BTrace, 0, 6)
		if err != nil {
			t.Fatal(err)
		}
		delta, err := DifferenceValues(valuesA[:3], valuesB[:3])
		if err != nil || !reflect.DeepEqual(delta, []float64{.5, 1, 1.5}) {
			t.Fatalf("physical pair %+v float difference=%v err=%v", pair, delta, err)
		}
		// Unlike the first three samples, these values depend on the logical
		// trace. They independently detect reading the wrong physical B trace.
		for sample := 3; sample <= 6; sample++ {
			want := float32(math.Sin(float64(sample)*0.17 + float64(i)*0.13))
			if valuesA[sample] != float64(want) || valuesB[sample] != float64(want*.5) {
				t.Fatalf("pair %+v sample %d: A=%g B=%g, logical waveform=%g", pair, sample, valuesA[sample], valuesB[sample], want)
			}
		}
	}
	for i, record := range aIndex.Records {
		shot, receiver := i/12, i%12
		if !record.HasSource || !record.HasReceiver || record.SourceX != float64(shot*100) || record.ReceiverX != float64(receiver*20) {
			t.Fatalf("scalar or legal origin was lost: %+v", record)
		}
	}
	for _, kind := range []GatherType{GatherCMP, GatherShot, GatherReceiver, GatherOffset, GatherAzimuth, GatherRaw} {
		selection := GatherSelection{Type: kind, Key: GatherKey{All: true}, Secondary: SecondaryPhysical}
		if kind == GatherRaw {
			selection.Key = GatherKey{Raw: true}
		}
		ga, err := aIndex.Gather(selection)
		if err != nil {
			t.Fatal(err)
		}
		gb, err := bIndex.Gather(selection)
		if err != nil {
			t.Fatal(err)
		}
		if len(ga.TraceIndices) != acceptanceTraceCount || len(gb.TraceIndices) != acceptanceTraceCount {
			t.Fatalf("%s omitted full-range physical traces", kind)
		}
		matched, err := MatchGatherResults(aIndex, ga, bIndex, gb)
		if err != nil || len(matched.Pairs) != acceptanceTraceCount {
			t.Fatalf("%s reordered matching=%+v err=%v", kind, matched, err)
		}
		for i, pair := range matched.Pairs {
			if pair.ATrace != ga.TraceIndices[i] || pair.BTrace != wantB[int(pair.ATrace)] {
				t.Fatalf("%s did not preserve A selection order: %+v", kind, pair)
			}
		}
		keys := aIndex.AvailableGathers(kind)
		if kind != GatherRaw && len(keys) >= 2 {
			selection.Keys = append([]GatherKey(nil), keys[:2]...)
			one, err := aIndex.Gather(selection)
			if err != nil {
				t.Fatal(err)
			}
			for repeat := 0; repeat < 5; repeat++ {
				again, err := aIndex.Gather(selection)
				if err != nil || !reflect.DeepEqual(one, again) {
					t.Fatalf("%s multi-key selection is not deterministic", kind)
				}
			}
		}
	}
	// Reading only seven samples from each selected physical trace must not
	// alter either source. Fixture waveforms stay outside metadata/report APIs.
	for _, source := range []struct {
		path   string
		before []byte
	}{{aPath, originalA}, {bPath, originalB}} {
		after, err := os.ReadFile(source.path)
		if err != nil || sha256.Sum256(source.before) != sha256.Sum256(after) {
			t.Fatalf("read-only compare changed source %s: %v", source.path, err)
		}
	}
}

func acceptanceClassifiedOptions() (testsegy.Options, testsegy.Options) {
	aOptions, bOptions := acceptanceOptions(false, 1, 256, 2000, 10), acceptanceOptions(true, .5, 256, 2000, 10)
	bPhysical := make(map[int]int)
	for physical, logical := range acceptanceOrder(true) {
		bPhysical[logical] = physical
	}
	aOptions.TraceHeaders[1] = aOptions.TraceHeaders[0]
	bOptions.TraceHeaders[bPhysical[1]] = bOptions.TraceHeaders[bPhysical[0]]
	aOptions.TraceHeaders[2] = testsegy.TraceHeader{}
	bOptions.TraceHeaders[bPhysical[2]] = testsegy.TraceHeader{}
	newB := &bOptions.TraceHeaders[bPhysical[59]]
	newB.SourceID, newB.ReceiverID, newB.SourceX, newB.ReceiverX = 90000, 99000, 9000, 9900
	// Distinct IDs and XY alone are insufficient: CDP+signed Offset is an
	// intentional fallback. Change that key too to make a genuinely B-only row.
	newB.CDP, newB.Offset = 90000, 9999
	return aOptions, bOptions
}

func TestCompareAcceptanceReaderClassificationReportsRepeat(t *testing.T) {
	dir := t.TempDir()
	aOptions, bOptions := acceptanceClassifiedOptions()
	aPath, bPath := filepath.Join(dir, "A_classified.sgy"), filepath.Join(dir, "B_classified.sgy")
	_, a := openAcceptanceIndex(t, aPath, aOptions, DefaultHeaderMapping())
	_, b := openAcceptanceIndex(t, bPath, bOptions, DefaultHeaderMapping())
	filters := CompareFilterSet{Matched: true, AOnly: true, BOnly: true, Ambiguous: true, Invalid: true}
	var firstJSON, firstCSV []byte
	var firstRows []CompareLogicalRow
	for repeat := 0; repeat < 10; repeat++ {
		result := MatchRecords(a.Records, b.Records)
		if len(result.Pairs) != 56 || len(result.AOnly) != 1 || len(result.BOnly) != 1 || len(result.AmbiguousA) != 2 || len(result.AmbiguousB) != 2 || len(result.InvalidA) != 1 || len(result.InvalidB) != 1 {
			t.Fatalf("reader classification accounting: %+v", result)
		}
		rows := FilterCompareRows(result, a.Records, b.Records, filters)
		if len(rows) != 64 {
			t.Fatalf("classification rows=%d, want 64", len(rows))
		}
		if defaults := FilterCompareRows(result, a.Records, b.Records, DefaultCompareFilterSet()); len(defaults) != 57 {
			t.Fatalf("default matched+A-only selection=%d, want 57", len(defaults))
		}
		for i := 0; i < acceptanceTraceCount; i++ {
			if !rows[i].HasA || rows[i].ATrace != int64(i) {
				t.Fatalf("A row order was lost at %d: %+v", i, rows[i])
			}
		}
		report := BuildCompareMatchReportWithOptions(result, SampleAxisCompatibility{Compatible: true}, CompareMatchReportOptions{
			APath: aPath, BPath: bPath, Selection: "CMP 全部范围", Primary: "CMP", Secondary: "原始道序",
			SampleWindow: SampleWindow{Start: 0, End: 2}, IncludePairs: true, ARecords: a.Records, BRecords: b.Records,
		})
		jsonData, err := report.JSON()
		if err != nil {
			t.Fatal(err)
		}
		var csvData bytes.Buffer
		if err := WriteCompareMatchReportCSV(&csvData, report); err != nil {
			t.Fatal(err)
		}
		var decoded CompareMatchReport
		if err := json.Unmarshal(jsonData, &decoded); err != nil || !reflect.DeepEqual(decoded, report) {
			t.Fatalf("JSON changed report classifications or metadata: %v", err)
		}
		csvRows, err := csv.NewReader(bytes.NewReader(csvData.Bytes())).ReadAll()
		if err != nil || len(csvRows) != len(report.Rows)+1 {
			t.Fatalf("CSV lost classification rows: rows=%d err=%v", len(csvRows), err)
		}
		classificationColumn := -1
		for i, column := range csvRows[0] {
			if column == "classification" {
				classificationColumn = i
			}
		}
		for i, row := range report.Rows {
			if classificationColumn < 0 || csvRows[i+1][classificationColumn] != row.Classification {
				t.Fatalf("CSV classification or stable row order lost at %d", i)
			}
		}
		for _, forbidden := range []string{"amplitude", "samples", "sample_values", "waveform"} {
			if strings.Contains(string(jsonData), forbidden) || strings.Contains(csvData.String(), forbidden) {
				t.Fatalf("report contains waveform field %q", forbidden)
			}
		}
		if repeat == 0 {
			firstJSON, firstCSV, firstRows = jsonData, append([]byte(nil), csvData.Bytes()...), rows
		} else if !bytes.Equal(firstJSON, jsonData) || !bytes.Equal(firstCSV, csvData.Bytes()) || !reflect.DeepEqual(firstRows, rows) {
			t.Fatal("matching/filter/report changed across identical repeated runs")
		}
	}
}

func TestCompareAcceptanceReaderSampleAxisGate(t *testing.T) {
	dir := t.TempDir()
	_, a := openAcceptanceIndex(t, filepath.Join(dir, "A.sgy"), acceptanceOptions(false, 1, 256, 2000, 10), DefaultHeaderMapping())
	for _, tc := range []struct {
		name                     string
		samples, interval, delay int
		bWindow                  SampleWindow
		reason                   string
	}{
		{"compatible", 256, 2000, 10, SampleWindow{Start: 0, End: 2}, ""},
		{"sample_count", 257, 2000, 10, SampleWindow{Start: 0, End: 2}, "sample count mismatch"},
		{"sample_interval", 256, 4000, 10, SampleWindow{Start: 0, End: 2}, "sample interval mismatch"},
		{"delay_and_origin", 256, 2000, 12, SampleWindow{Start: 0, End: 2}, "delay mismatch; time origin mismatch"},
		{"different_window", 256, 2000, 10, SampleWindow{Start: 1, End: 3}, "sample window mismatch"},
		{"window_outside_axis", 256, 2000, 10, SampleWindow{Start: 0, End: 256}, "sample window mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, b := openAcceptanceIndex(t, filepath.Join(dir, tc.name+".sgy"), acceptanceOptions(true, .5, tc.samples, tc.interval, tc.delay), DefaultHeaderMapping())
			matched := MatchRecords(a.Records, b.Records)
			if len(matched.Pairs) != acceptanceTraceCount {
				t.Fatalf("axis differences must not prevent metadata matching: %+v", matched)
			}
			for _, pair := range matched.Pairs {
				axis := CompareSampleAxesWithWindows(a.Records[pair.AIndex], b.Records[pair.BIndex], SampleWindow{Start: 0, End: 2}, tc.bWindow)
				if axis.Compatible != (tc.reason == "") || axis.Reason != tc.reason {
					t.Fatalf("reader pair %+v: unexpected sample-axis gate %+v", pair, axis)
				}
			}
		})
	}
}

// This opt-in test creates a fresh temporary directory for human GUI
// acceptance. Ordinary test runs skip it and retain no waveform fixtures.
func TestCompareAcceptanceGenerateManualFixtures(t *testing.T) {
	parent := os.Getenv("SEISFORGE_COMPARE_FIXTURE_PARENT")
	if parent == "" {
		t.Skip("set SEISFORGE_COMPARE_FIXTURE_PARENT to retain fresh temporary GUI fixtures")
	}
	dir, err := os.MkdirTemp(parent, "seisforge-compare-")
	if err != nil {
		t.Fatal(err)
	}
	aClassified, bClassified := acceptanceClassifiedOptions()
	for _, fixture := range []struct {
		name    string
		options testsegy.Options
	}{
		{"A.sgy", acceptanceOptions(false, 1, 256, 2000, 10)},
		{"B_reordered_half.sgy", acceptanceOptions(true, .5, 256, 2000, 10)},
		{"B_interval_mismatch.sgy", acceptanceOptions(true, .5, 256, 4000, 10)},
		{"B_count_mismatch.sgy", acceptanceOptions(true, .5, 257, 2000, 10)},
		{"B_delay_mismatch.sgy", acceptanceOptions(true, .5, 256, 2000, 12)},
		{"A_classified.sgy", aClassified},
		{"B_classified.sgy", bClassified},
	} {
		path := filepath.Join(dir, fixture.name)
		reader, idx := openAcceptanceIndex(t, path, fixture.options, DefaultHeaderMapping())
		if len(idx.Records) != acceptanceTraceCount {
			t.Fatalf("unexpected fixture count: %d", len(idx.Records))
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		t.Logf("GUI fixture: %s", path)
	}
	t.Log("GUI NOT EXECUTED: generated fixtures are not visual acceptance evidence")
}
