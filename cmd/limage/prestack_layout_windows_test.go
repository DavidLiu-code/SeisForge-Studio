//go:build windows

package main

import "testing"

func TestPrestackPanelLayoutKeepsContentInsideFooter(t *testing.T) {
	for _, tc := range []struct {
		w, h int
	}{
		{1360, 880}, {1680, 1089}, {1120, 700}, {640, 420}, {1, 1},
	} {
		for _, page := range []int{0, 1, 2, 3, 4} {
			l := prestackPanelLayoutForSize(tc.w, tc.h, page)
			if l.Content.Left < 0 || l.Content.Top < 0 || l.Content.Right > maxLayoutInt(1, tc.w) || l.Content.Bottom > maxLayoutInt(1, tc.h) {
				t.Fatalf("content escapes client %dx%d page %d: %+v", tc.w, tc.h, page, l.Content)
			}
			if l.Scene.Left < 0 || l.Scene.Top < 0 || l.Scene.Right > maxLayoutInt(1, tc.w) || l.Scene.Bottom > maxLayoutInt(1, tc.h) {
				t.Fatalf("scene escapes client %dx%d page %d: %+v", tc.w, tc.h, page, l.Scene)
			}
			if tc.h > 200 && l.Footer.Top < l.Content.Bottom && page == 4 {
				t.Fatalf("QC content overlaps footer %dx%d: content=%+v footer=%+v", tc.w, tc.h, l.Content, l.Footer)
			}
			if l.Content.width() < 1 || l.Content.height() < 1 || l.Scene.width() < 1 || l.Scene.height() < 1 {
				t.Fatalf("degenerate layout %dx%d page %d: %+v", tc.w, tc.h, page, l)
			}
		}
	}
}

func TestPrestackPanelLayoutGatherHasStableTwoRows(t *testing.T) {
	wide := prestackPanelLayoutForSize(1360, 880, 0)
	narrow := prestackPanelLayoutForSize(1120, 700, 0)
	if wide.Scene.Top != narrow.Scene.Top || wide.ToolbarTop != narrow.ToolbarTop || wide.ToolbarBottom != narrow.ToolbarBottom {
		t.Fatalf("gather rows changed with width: wide=%+v narrow=%+v", wide, narrow)
	}
}
