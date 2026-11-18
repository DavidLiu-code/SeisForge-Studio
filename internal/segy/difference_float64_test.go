package segy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestArtifactDifferenceExportFloat64IdentityAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	source, artifactPath := filepath.Join(dir, "A.sgy"), filepath.Join(dir, "B.sfdata")
	makeExportSynthetic(t, source, 0)
	aBytes, _ := os.ReadFile(source)
	if err := os.WriteFile(artifactPath, []byte("immutable artifact identity"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	values := &testFloatSectionData{values: [][]float64{{1e100, math.Pi, 1.0000000000000002, 0}, {-1e100, -math.Pi, 0, 9}}}
	b, err := OpenFloatSection(FloatSectionOptions{SourcePath: source, ValuePath: artifactPath, TraceIndices: []int64{4, 1}, SampleStart: 5, SampleEnd: 8}, values)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	identity, err := b.SourceFileInfo()
	actual, statErr := os.Stat(artifactPath)
	if err != nil || statErr != nil || !os.SameFile(identity, actual) || identity.Size() != actual.Size() {
		t.Fatalf("artifact identity is a virtual FileInfo wrapper: %v/%v", err, statErr)
	}
	export := func(ctx context.Context, path string, progress func(int, int)) error {
		return ExportDifferenceSectionFloat64WindowedContext(ctx, path, a, b, []int64{1, 4}, []int64{1, 0}, 5, 8, 5, progress)
	}
	path := filepath.Join(dir, "delta64.sgy")
	if err := export(context.Background(), path, nil); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if d.Info.FormatCode != 6 || d.Info.SamplesPerTrace != 4 || d.Info.TraceCount != 2 {
		t.Fatalf("difference export narrowed/changed metadata: %+v", d.Info)
	}
	for i, physical := range []int64{1, 4} {
		want, err := a.ReadTraceWindow(physical, 5, 8)
		if err != nil {
			t.Fatal(err)
		}
		for j := range want {
			want[j] -= values.values[1-i][j]
		}
		got, err := d.ReadTraceWindow(int64(i), 0, 3)
		if err != nil || !reflect.DeepEqual(want, got) {
			t.Fatalf("float64 difference was narrowed: want=%v got=%v error=%v", want, got, err)
		}
		ah, _ := a.ReadTraceHeader(physical)
		dh, _ := d.ReadTraceHeader(int64(i))
		if ah.Inline != dh.Inline || ah.Crossline != dh.Crossline || dh.DelayMS != ah.DelayMS+10 {
			t.Fatal("difference header/source order changed")
		}
	}
	d.Close()
	if err := export(context.Background(), path, nil); err == nil {
		t.Fatal("existing difference output was overwritten")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelPath := filepath.Join(dir, "cancelled.sgy")
	if err := export(ctx, cancelPath, func(done, total int) { cancel() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was ignored: %v", err)
	}
	if _, err := os.Stat(cancelPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled difference was published")
	}
	temps, _ := filepath.Glob(filepath.Join(dir, ".algorithm-difference-*"))
	if len(temps) != 0 {
		t.Fatalf("difference temporary files leaked: %v", temps)
	}
	currentA, _ := os.ReadFile(source)
	if !bytes.Equal(aBytes, currentA) {
		t.Fatal("difference export modified A")
	}
}

func TestArtifactDifferenceUsesExactMicrosecondAxis(t *testing.T) {
	dir := t.TempDir()
	source, artifactPath := filepath.Join(dir, "A.sgy"), filepath.Join(dir, "B.sfdata")
	makeExportSynthetic(t, source, 0)
	f, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	info := f.Info
	f.Close()
	raw, _ := os.ReadFile(source)
	binary.BigEndian.PutUint16(raw[3216:3218], 500)
	for trace := int64(0); trace < info.TraceCount; trace++ {
		pos := info.DataStart + trace*info.TraceBytes
		binary.BigEndian.PutUint16(raw[pos+116:pos+118], 500)
	}
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("identity"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenFloatSection(FloatSectionOptions{SourcePath: source, ValuePath: artifactPath, TraceIndices: []int64{0}, SampleStart: 1, SampleEnd: 4}, &testFloatSectionData{values: [][]float64{{0, 0, 0, 0}}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := ValidateDifferenceWindow(a, b, []int64{0}, []int64{0}, 1, 4, 1); err != nil {
		t.Fatalf("exact 500us artifact was rejected: %v", err)
	}
	if err := ValidateDifferenceWindow(a, b, []int64{0}, []int64{0}, 2, 4, 2); err == nil {
		t.Fatal("rounded-ms header hid a 500us misalignment")
	}
	if err := ExportDifferenceSectionFloat64WindowedContext(context.Background(), filepath.Join(dir, "inexact-origin.sgy"), a, b, []int64{0}, []int64{0}, 1, 4, 1, nil); err == nil {
		t.Fatal("fractional-ms SEG-Y export silently changed time origin")
	}
}
