package dataset

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestOpenMetadataAndIndependentReaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 8, Cols: 8, Samples: 6, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	data, err := NewManager().Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(data.Path) {
		t.Fatalf("dataset path is not absolute: %q", data.Path)
	}
	if data.Metadata.TraceCount != 64 || data.Metadata.SamplesPerTrace != 6 || data.Metadata.SampleIntervalUS != 2000 || data.Metadata.FormatCode != 5 {
		t.Fatalf("unexpected metadata: %+v", data.Metadata)
	}
	if unsafe.Sizeof(*data) > 1024 {
		t.Fatalf("dataset descriptor unexpectedly large: %d bytes", unsafe.Sizeof(*data))
	}
	r1, err := data.OpenReader()
	if err != nil {
		t.Fatal(err)
	}
	r2, err := data.OpenReader()
	if err != nil {
		_ = r1.Close()
		t.Fatal(err)
	}
	if r1 == r2 {
		t.Fatal("OpenReader returned the same reader")
	}
	if err := r1.Close(); err != nil {
		t.Fatal(err)
	}
	value, err := r2.ReadSample(7, 3)
	if err != nil {
		t.Fatalf("closing reader 1 invalidated reader 2: %v", err)
	}
	if value != 703 {
		t.Fatalf("unexpected sample: %v", value)
	}
	if err := r2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenClosesValidationReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "close-check.sgy")
	moved := filepath.Join(dir, "moved.sgy")
	if err := testsegy.Write(path, testsegy.Options{Rows: 2, Cols: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager().Open(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, moved); err != nil {
		t.Fatalf("validation file is still held open: %v", err)
	}
}

func TestOpenRejectsCorruptSEGAndStoresOptionalGeometry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.sgy")
	if err := os.WriteFile(path, []byte("not seg-y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager().Open(path); err == nil {
		t.Fatal("expected corrupt SEG-Y to be rejected")
	}
	data := &SeismicDataset{}
	line := geometry.NewLine2D(12)
	data.SetGeometry(line)
	if data.Geometry() != line {
		t.Fatal("optional geometry was not retained")
	}
}
