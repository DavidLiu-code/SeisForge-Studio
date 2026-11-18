package poststack

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func sectionFixture(t *testing.T, regular bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "source.sgy")
	if err := testsegy.Write(src, testsegy.Options{Rows: 2, Cols: 3, Samples: 8, RegularGrid: regular}); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "jobs")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	return src, work
}

func TestProcessSectionCopyGainWindowAndOriginalIdentity(t *testing.T) {
	for _, regular := range []bool{true, false} {
		for _, algorithm := range []string{"copy", "gain"} {
			t.Run(algorithm+map[bool]string{true: "3d", false: "2d"}[regular], func(t *testing.T) {
				src, work := sectionFixture(t, regular)
				before, err := FileSHA256(src)
				if err != nil {
					t.Fatal(err)
				}
				req := SectionRequest{SourcePath: src, TempDir: work, TraceIndices: []int64{5, 0, 2}, SampleStart: 2, SampleEnd: 5, AlgorithmID: algorithm, Gain: 1.5}
				r, err := ProcessSection(context.Background(), req, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer r.Cleanup()
				if err := r.Metadata.Validate(); err != nil || r.Metadata.TimeOriginUS != 4000 || r.Metadata.SamplesPerTrace != 4 {
					t.Fatalf("missing result provenance: %+v %v", r.Metadata, err)
				}
				if !reflect.DeepEqual(r.TraceIndices, req.TraceIndices) {
					t.Fatal("trace order changed")
				}
				for part, path := range []string{r.Part1, r.Part2} {
					f, err := segy.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					if f.Info.TraceCount != 3 || f.Info.SamplesPerTrace != 4 || f.Info.FormatCode != 5 || f.Info.SampleIntervalUS != 2000 {
						t.Fatalf("invalid section metadata: %+v", f.Info)
					}
					for i, tr := range req.TraceIndices {
						v, err := f.ReadTraceWindow(int64(i), 0, 3)
						if err != nil {
							t.Fatal(err)
						}
						for j, got := range v {
							a := float64(tr*100 + int64(j+2))
							gain := 1.0
							if algorithm == "gain" {
								gain = 1.5
							}
							want := a * gain
							if part == 0 {
								want = a - want
							}
							if got != want {
								t.Fatalf("part %d trace %d sample %d: got %g want %g", part, tr, j, got, want)
							}
						}
						h, err := f.ReadTraceHeader(int64(i))
						if err != nil || h.DelayMS != 4 || (regular && (h.Inline != 1000+int32(tr/3) || h.Crossline != 2000+int32(tr%3))) {
							t.Fatalf("geometry/time origin changed: %+v %v", h, err)
						}
					}
					f.Close()
				}
				after, _ := FileSHA256(src)
				if before != after {
					t.Fatal("source was changed")
				}
				owned := r.Dir
				r.Dir = filepath.Dir(src) // public metadata cannot redirect cleanup
				if err := r.Cleanup(); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(owned); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("private result directory leaked")
				}
				if _, err := os.Stat(src); err != nil {
					t.Fatal("cleanup escaped owned directory")
				}
			})
		}
	}
}

func TestProcessSectionCancelAndSourceChangeCleanup(t *testing.T) {
	for _, mutate := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "source-change"}[mutate], func(t *testing.T) {
			src, work := sectionFixture(t, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := SectionRequest{SourcePath: src, TempDir: work, TraceIndices: []int64{0, 1}, SampleStart: 0, SampleEnd: 7, AlgorithmID: "copy"}
			r, err := ProcessSection(ctx, req, func(done, total int) {
				if done != 1 {
					return
				}
				if mutate {
					changed := time.Now().Add(time.Hour)
					if e := os.Chtimes(src, changed, changed); e != nil {
						t.Fatal(e)
					}
				} else {
					cancel()
				}
			})
			if err == nil || r != nil {
				t.Fatalf("expected rejection, got %v %v", r, err)
			}
			if !mutate && !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation, got %v", err)
			}
			entries, _ := os.ReadDir(work)
			if len(entries) != 0 {
				t.Fatalf("private artifacts leaked: %v", entries)
			}
		})
	}
}

func TestProcessSectionGenericResidualAndInvalidInput(t *testing.T) {
	src, work := sectionFixture(t, true)
	a, err := segy.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	traces := []int64{3, 1, 0}
	helper := filepath.Join(t.TempDir(), "residual.sgy")
	if err = a.ExportFloatSection(helper, segy.FloatSectionExportOptions{TraceIndices: traces, SampleStart: 1, Samples: [][]float32{{1, -2, 0}, {-1, 2, 0}, {0, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	req := SectionRequest{SourcePath: src, TempDir: work, TraceIndices: traces, SampleStart: 1, SampleEnd: 3, AlgorithmID: "residual", ResidualPath: helper}
	r, err := ProcessSection(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Cleanup()
	f, err := segy.Open(r.Part2)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := f.ReadTraceWindow(0, 0, 2)
	f.Close()
	if !reflect.DeepEqual(got, []float64{300, 304, 303}) {
		t.Fatalf("A-residual: got %v", got)
	}
	for _, change := range []string{"delay", "nan", "length"} {
		t.Run(change, func(t *testing.T) {
			data, err := os.ReadFile(helper)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "delay":
				binary.BigEndian.PutUint16(data[3600+108:3600+110], 123)
			case "nan":
				binary.BigEndian.PutUint32(data[3840:3844], math.Float32bits(float32(math.NaN())))
			case "length":
				data = append(data, 0)
			}
			bad := filepath.Join(t.TempDir(), "bad.sgy")
			if err := os.WriteFile(bad, data, 0o600); err != nil {
				t.Fatal(err)
			}
			badReq := req
			badReq.ResidualPath = bad
			if result, err := ProcessSection(context.Background(), badReq, nil); err == nil || result != nil {
				t.Fatalf("accepted invalid residual %s", change)
			}
		})
	}
}

func TestProcessSectionInvalidWindowGainAndNonfiniteSource(t *testing.T) {
	src, work := sectionFixture(t, false)
	base := SectionRequest{SourcePath: src, TempDir: work, TraceIndices: []int64{0}, SampleStart: 0, SampleEnd: 7, AlgorithmID: "gain", Gain: 1}
	cases := []SectionRequest{base, base, base, base, base}
	cases[0].Gain = 11
	cases[1].TraceIndices = []int64{6}
	cases[2].SampleEnd = 8
	cases[3].AlgorithmID = "unknown"
	cases[4].AlgorithmID = "cadzow_residual" // retired private adapter never falls back to Copy
	for _, req := range cases {
		if r, err := ProcessSection(context.Background(), req, nil); err == nil || r != nil {
			t.Fatalf("accepted invalid request %+v", req)
		}
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint32(data[3840:3844], math.Float32bits(math.MaxFloat32))
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	base.Gain = 10
	if r, err := ProcessSection(context.Background(), base, nil); err == nil || r != nil {
		t.Fatal("accepted gain float32 overflow")
	}
}

func TestStreamSectionExistingTargetNeverTouched(t *testing.T) {
	src, _ := sectionFixture(t, true)
	a, err := segy.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	path := filepath.Join(t.TempDir(), "existing.sgy")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = a.ExportFloatSectionStream(path, segy.StreamFloatSectionOptions{TraceIndices: []int64{0}, SampleCount: 1, Samples: func(int, int64) ([]float32, error) { return []float32{1}, nil }})
	if err == nil {
		t.Fatal("existing target accepted")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "keep" {
		t.Fatal("existing target changed")
	}
}

func TestStreamSectionFinalPublicationGate(t *testing.T) {
	src, _ := sectionFixture(t, true)
	a, err := segy.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "gated.sgy")
	stale := errors.New("owner generation changed")
	err = a.ExportFloatSectionStream(path, segy.StreamFloatSectionOptions{
		TraceIndices: []int64{0}, SampleCount: 1,
		Samples:       func(int, int64) ([]float32, error) { return []float32{1}, nil },
		BeforePublish: func() error { return stale },
	})
	if !errors.Is(err, stale) {
		t.Fatalf("publication gate: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed publication left artifacts: %v %v", entries, err)
	}
}
