//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
)

func TestNumericalExportLayoutAlignsSpaceAndTimeBelowScope(t *testing.T) {
	for _, dpi := range []int{96, 120, 144} {
		for _, widthDIP := range []float64{430, 505, 680, 860} {
			width := displayPixelDIP(widthDIP, dpi)
			g := numericalExportRangeLayout(width, dpi)
			if g.edits[0].Top <= g.scope.Bottom || g.timeLabel.Left <= g.edits[1].Right || g.timeLabel.Top != g.spaceLabel.Top {
				t.Fatalf("DPI %d width %v: expected space and time together below scope: %+v", dpi, widthDIP, g)
			}
			for i, edit := range g.edits {
				if edit.Top != g.edits[0].Top || edit.Bottom != g.edits[0].Bottom || g.labels[i].Top != g.spaceLabel.Top {
					t.Fatalf("DPI %d width %v: field %d does not share the row and label baseline: %+v", dpi, widthDIP, i, g)
				}
			}
			if g.format.Top != g.scope.Top || g.format.Bottom != g.scope.Bottom || g.formatLabel.Top != g.scopeLabel.Top {
				t.Fatalf("DPI %d width %v: format selector is not aligned with scope: %+v", dpi, widthDIP, g)
			}
			boxes := append([]RECT{g.availability, g.scopeLabel, g.scope, g.stageLabel, g.stage, g.formatLabel, g.format, g.spaceLabel, g.timeLabel, g.fieldStatus}, g.objects[:]...)
			boxes = append(append(boxes, g.labels[:]...), g.edits[:]...)
			for i, a := range boxes {
				if a.Left < 0 || a.Right > int32(width) || a.Top < 0 || a.Bottom > int32(g.previewTop) || a.Right <= a.Left || a.Bottom <= a.Top {
					t.Fatalf("DPI %d width %v: range control %d out of bounds: %+v", dpi, widthDIP, i, a)
				}
				for j, b := range boxes[:i] {
					if a.Left < b.Right && b.Left < a.Right && a.Top < b.Bottom && b.Top < a.Bottom {
						t.Fatalf("DPI %d width %v: range controls %d and %d overlap: %+v / %+v", dpi, widthDIP, i, j, a, b)
					}
				}
			}
		}
	}
}

func TestNumericalExportNameUsesResolvedSamplesAndSourceStem(t *testing.T) {
	plan := compareExportPlan{request: compareExportSelection{kind: compareExportA, scope: compareExportManual,
		manual: compareExportBounds{t0: 899900, t1: 1499999}}, first: 184, last: 2456, t0: 900000, t1: 1498000, mapped: 2273}
	plan.capture.sources[0] = compareExportSource{path: `E:\data\line_view_visible_A-B_IL1870_XL184-2456_T800-1500ms.sgy`, originalStem: "line_view_visible"}
	stem, desc := numericalExportNameParts(plan)
	if stem != "line_view_visible" || desc != "A_XL184-2456_T900-1498ms" {
		t.Fatalf("name used prior output or requested rather than resolved endpoints: %q %q", stem, desc)
	}
	plan.request.kind, plan.request.effective, plan.capture.key.mode = compareExportB, true, 1
	plan.capture.sources[1] = compareExportSource{path: `C:\temp\output.sfdata`, originalStem: "line_view_visible", processTag: "copy"}
	plan.capture.settings[1].Enabled = true
	plan.capture.settings[1].Params = poststack.ViewBandpass{Response: poststack.FilterBandstop, Method: poststack.FilterButterworth,
		LowHz: 20, HighHz: 162.5, Order: 4, Phase: poststack.FilterZeroPhase}
	stem, desc = numericalExportNameParts(plan)
	if stem != "line_view_visible" || desc != "B_copy+VBS20-162p5HzBW4Z_IL184-2456_T900-1498ms" {
		t.Fatalf("name should describe selected B/source/filter/range: %q %q", stem, desc)
	}
	plan.request.effective = false
	_, desc = numericalExportNameParts(plan)
	if desc != "B_copy+Base_IL184-2456_T900-1498ms" || numericalExportTimeToken(-850) != "-0p85" {
		t.Fatalf("base vs current stage or signed fractional time lost: %q", desc)
	}
}

func TestNumericalExportNameCollisionAndWindowsValidation(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "example_A_XL184-2456_T900-1498ms.sgy")
	for i, suffix := range []string{"", "_001", "_002"} {
		path, err := availableNumericalExportPath(base)
		want := strings.TrimSuffix(base, ".sgy") + suffix + ".sgy"
		if err != nil || path != want {
			t.Fatalf("collision %d: %q, %v; want %q", i, path, err, want)
		}
		if err := os.WriteFile(path, []byte("intentionally occupied"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []string{"NUL.sgy", "LPT².segy", "name?.sgy", "bad.bmp", `C:local.sgy`, strings.Repeat("名", 240) + ".sgy"} {
		if path, err := resolveNumericalExportPath(invalid, dir); err == nil {
			t.Errorf("accepted invalid output %q as %q", invalid, path)
		}
	}
	if path, err := resolveNumericalExportPath("合法_file_A_view_visible", dir); err != nil || path != filepath.Join(dir, "合法_file_A_view_visible.sgy") {
		t.Fatalf("valid user stem changed: %q, %v", path, err)
	}
}
