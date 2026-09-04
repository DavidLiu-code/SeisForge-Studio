package geometry

import (
	"path/filepath"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/testsegy"
)

func TestLine2DGeometry(t *testing.T) {
	g := NewLine2D(10)
	if g.Kind() != KindLine2D || g.TraceCount() != 10 {
		t.Fatalf("unexpected line geometry: kind=%v traces=%d", g.Kind(), g.TraceCount())
	}
	if location, ok := g.TraceLocation(9); !ok || location.Trace != 9 || location.HasGrid {
		t.Fatalf("unexpected trace location: %+v, %v", location, ok)
	}
	if _, ok := g.TraceLocation(10); ok {
		t.Fatal("out-of-range trace was accepted")
	}
	if bounds := g.Bounds(); bounds.TraceMin != 0 || bounds.TraceMax != 9 || bounds.HasGrid {
		t.Fatalf("unexpected line bounds: %+v", bounds)
	}
}

func TestRegularGridWrapperTraceAtAndBounds(t *testing.T) {
	index := &segy.GeometryIndex{
		TraceCount: 4, InlineValues: []int32{100, 101}, CrosslineValues: []int32{200, 201},
		TraceNumbers: []int64{0, 1, 2, 3}, RowOfTrace: []int32{0, 0, 1, 1}, ColOfTrace: []int32{0, 1, 0, 1},
	}
	g, err := WrapRegularGrid(index)
	if err != nil {
		t.Fatal(err)
	}
	if trace, ok := g.TraceAt(101, 200); !ok || trace != 2 {
		t.Fatalf("TraceAt returned trace=%d ok=%v", trace, ok)
	}
	location, ok := g.TraceLocation(3)
	if !ok || location.Inline != 101 || location.Crossline != 201 || !location.HasGrid {
		t.Fatalf("unexpected location: %+v ok=%v", location, ok)
	}
	bounds := g.Bounds()
	if !bounds.HasGrid || bounds.InlineMin != 100 || bounds.InlineMax != 101 || bounds.CrosslineMin != 200 || bounds.CrosslineMax != 201 {
		t.Fatalf("unexpected regular bounds: %+v", bounds)
	}
}

func TestDetectDistinguishesRegularAndLineData(t *testing.T) {
	dir := t.TempDir()
	regularPath := filepath.Join(dir, "regular.sgy")
	linePath := filepath.Join(dir, "line.sgy")
	if err := testsegy.Write(regularPath, testsegy.Options{Rows: 8, Cols: 8, Samples: 4, RegularGrid: true}); err != nil {
		t.Fatal(err)
	}
	if err := testsegy.Write(linePath, testsegy.Options{Rows: 8, Cols: 8, Samples: 4}); err != nil {
		t.Fatal(err)
	}
	regularFile, err := segy.Open(regularPath)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := Detect(regularFile, 1600)
	_ = regularFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if regular.Kind != KindRegular3D || regular.Score < RecommendationScoreMin || regular.Confidence < RecommendationConfidenceMin {
		t.Fatalf("regular data not recommended as 3-D: %+v", regular)
	}
	lineFile, err := segy.Open(linePath)
	if err != nil {
		t.Fatal(err)
	}
	line, err := Detect(lineFile, 1600)
	_ = lineFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if line.Kind != KindLine2D {
		t.Fatalf("non-grid data was misclassified: %+v", line)
	}
}

func TestDetectRecognizesEquivalentDuplicateInlineWords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duplicate-inline.sgy")
	if err := testsegy.Write(path, testsegy.Options{
		Rows: 36, Cols: 120, Samples: 1, RegularGrid: true,
		InlineByte: 193, CrosslineByte: 25, DuplicateInlineByte: 21,
	}); err != nil {
		t.Fatal(err)
	}
	f, err := segy.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	detected, err := Detect(f, 3000)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if detected.Kind != KindRegular3D || detected.Score < RecommendationScoreMin || detected.Confidence < RecommendationConfidenceMin {
		t.Fatalf("equivalent duplicated Inline words suppressed a valid 3-D recommendation: %+v", detected)
	}
	if len(detected.Legacy.Candidates) < 2 {
		t.Fatalf("fixture did not create the intended equivalent byte candidates: %+v", detected.Legacy)
	}
}

func TestGeometryAndWorkspaceKindsRemainIndependent(t *testing.T) {
	if KindRegular3D == Kind(2) && KindLine2D == KindRegular3D {
		t.Fatal("geometry kinds collapsed")
	}
}
