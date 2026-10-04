//go:build windows

package main

// prestackPanelLayout is the geometry contract shared by the native control
// layout and the parent paint paths.  Keeping these rectangles in one pure
// helper prevents a resize from leaving QC charts or a seismic frame under a
// footer control.  Coordinates are client pixels.
type prestackPanelLayout struct {
	ToolbarTop, ToolbarBottom int
	Content                   layoutRect
	Scene                     layoutRect
	Footer                    layoutRect
}

// layoutRect is deliberately independent of RECT so the layout contract can
// be tested without creating a Win32 window.
type layoutRect struct{ Left, Top, Right, Bottom int }

func (r layoutRect) width() int  { return r.Right - r.Left }
func (r layoutRect) height() int { return r.Bottom - r.Top }

func prestackPanelLayoutForSize(width, height, page int) prestackPanelLayout {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	// Native controls occupy the first 69 client pixels. Gather adds exactly
	// two 24/25px toolbar rows; all other pages start below the tab strip.
	sceneTop := 119
	if page == 0 {
		sceneTop = 156
	}
	contentTop := 114
	footerTop := height - 42
	if footerTop < contentTop+1 {
		footerTop = contentTop + 1
	}
	if footerTop > height {
		footerTop = height
	}
	contentLeft, contentRight := 20, width-20
	if contentRight <= contentLeft {
		contentLeft, contentRight = 0, width
	}
	sceneBottom := height - 62
	if sceneBottom < sceneTop+1 {
		sceneBottom = sceneTop + 1
	}
	if sceneBottom > height {
		sceneBottom = height
	}
	sceneLeft, sceneRight := 62, width-24
	if sceneRight <= sceneLeft {
		sceneLeft, sceneRight = 0, width
	}
	if sceneTop >= height {
		sceneTop = 0
	}
	return prestackPanelLayout{
		ToolbarTop:    76,
		ToolbarBottom: 131,
		Content:       layoutRect{Left: contentLeft, Top: minLayoutInt(contentTop, maxLayoutInt(0, height-1)), Right: contentRight, Bottom: maxLayoutInt(1, footerTop)},
		Scene:         layoutRect{Left: sceneLeft, Top: sceneTop, Right: sceneRight, Bottom: maxLayoutInt(1, sceneBottom)},
		Footer:        layoutRect{Left: 10, Top: maxLayoutInt(0, height-29), Right: width, Bottom: height},
	}
}

func maxLayoutInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minLayoutInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func prestackLayoutRECT(r layoutRect) RECT {
	return RECT{Left: int32(r.Left), Top: int32(r.Top), Right: int32(r.Right), Bottom: int32(r.Bottom)}
}
