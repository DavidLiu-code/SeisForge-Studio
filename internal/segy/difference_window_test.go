package segy

import (
	"context"
	"os"
	"testing"
)

func TestDifferenceWindowAlignsCroppedB(t *testing.T) {
	d := t.TempDir()
	makeExportSynthetic(t, d+"/a.sgy", 0)
	a, err := Open(d + "/a.sgy")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.ExportSection(d+"/b.sgy", SectionExportOptions{TraceIndices: []int64{4, 1}, SampleStart: 5, SampleEnd: 14}); err != nil {
		t.Fatal(err)
	}
	b, err := Open(d + "/b.sgy")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	opts := RenderOptions{Width: 10, Height: 12, SampleStart: 7, SampleEnd: 13}
	_, stats, err := RenderTraceDifferencePairsWindowed(a, b, []int64{1, 4}, []int64{1, 0}, opts, 5)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ObservedMin != 0 || stats.ObservedMax != 0 {
		t.Fatalf("copy residual not zero: %+v", stats)
	}
	if stats.SampleStart != 7 || stats.SampleEnd != 13 {
		t.Fatalf("A sample window changed: %+v", stats)
	}
	if _, _, err := RenderTraceDifferencePairsWindowed(a, b, []int64{1, 4}, []int64{1, 0}, opts, 0); err == nil {
		t.Fatal("wrong time origin accepted")
	}
	opts.SampleStart = 0
	if _, _, err := RenderTraceDifferencePairsWindowed(a, b, []int64{1, 4}, []int64{1, 0}, opts, 5); err == nil {
		t.Fatal("window outside B accepted")
	}
}

func TestDifferenceWindowExportRoundTripAndNoOverwrite(t *testing.T) {
	d := t.TempDir()
	makeExportSynthetic(t, d+"/a.sgy", 0)
	a, err := Open(d + "/a.sgy")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.ExportFloatSectionStream(d+"/b.sgy", StreamFloatSectionOptions{TraceIndices: []int64{4, 1}, SampleStart: 5, SampleCount: 10, Samples: func(_ int, trace int64) ([]float32, error) {
		values, e := a.ReadTraceWindow(trace, 5, 14)
		if e != nil {
			return nil, e
		}
		out := make([]float32, len(values))
		for i, v := range values {
			out[i] = float32(v - 2.5)
		}
		return out, nil
	}}); err != nil {
		t.Fatal(err)
	}
	b, err := Open(d + "/b.sgy")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	path := d + "/difference.sgy"
	export := func(ctx context.Context, path string) error {
		return ExportDifferenceSectionWindowedContext(ctx, path, a, b, []int64{1, 4}, []int64{1, 0}, 7, 12, 5, nil)
	}
	if err := export(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Info.TraceCount != 2 || f.Info.SamplesPerTrace != 6 || f.Info.FormatCode != 5 {
		t.Fatalf("unexpected exported metadata: %+v", f.Info)
	}
	for tr := int64(0); tr < 2; tr++ {
		values, e := f.ReadTraceWindow(tr, 0, 5)
		if e != nil {
			t.Fatal(e)
		}
		for _, v := range values {
			if v != 2.5 {
				t.Fatalf("difference=%v", v)
			}
		}
	}
	_ = f.Close()
	if err := export(context.Background(), path); err == nil {
		t.Fatal("existing output overwritten")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := export(ctx, d+"/cancelled.sgy"); err == nil {
		t.Fatal("cancelled export succeeded")
	}
	if _, err := os.Stat(d + "/cancelled.sgy"); !os.IsNotExist(err) {
		t.Fatalf("cancelled output remains: %v", err)
	}
}

func TestDifferenceWindowRejectsAxes(t *testing.T) {
	d := t.TempDir()
	makeExportSynthetic(t, d+"/a.sgy", 0)
	makeExportSynthetic(t, d+"/b.sgy", 1)
	a, err := Open(d + "/a.sgy")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(d + "/b.sgy")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := ValidateDifferenceWindow(a, b, []int64{0}, []int64{0}, 0, 31, 0); err != nil {
		t.Fatal(err)
	}
	b.Info.SampleIntervalUS = 4000
	if err := ValidateDifferenceWindow(a, b, []int64{0}, []int64{0}, 0, 31, 0); err == nil {
		t.Fatal("interval mismatch accepted")
	}
	b.Info.SampleIntervalUS = 2000
	if err := ValidateDifferenceWindow(a, b, []int64{0}, []int64{0}, 0, 32, 0); err == nil {
		t.Fatal("outside sample window accepted")
	}
	if err := ValidateDifferenceWindow(a, b, []int64{0}, nil, 0, 31, 0); err == nil {
		t.Fatal("unmatched traces accepted")
	}
}
