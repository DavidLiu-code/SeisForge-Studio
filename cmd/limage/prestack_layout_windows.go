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

const (
	prestackMinClientWidth      = 640
	prestackMinClientHeight     = 480
	prestackToolbarScrollHeight = 22
)

// Only the native controls use a virtual width. Seismic/QC/Compare content
// always uses the actual client width; scrolling never changes a gather,
// sample window, physical-trace mapping or render generation.
type prestackToolbarViewport struct {
	CanvasWidth, ExtraTop, MaxScroll int
	ScrollRect                       layoutRect
}

func prestackToolbarViewportForSize(width, page int) prestackToolbarViewport {
	width = maxLayoutInt(1, width)
	controlWidth := 1100 // compact Gather and Raw toolbars, including bin controls
	switch page {
	case 1:
		controlWidth = 670
	case 2:
		controlWidth = 610
	case 3:
		controlWidth = 710
	case 4:
		controlWidth = 160
	}
	v := prestackToolbarViewport{CanvasWidth: maxLayoutInt(width, controlWidth)}
	v.MaxScroll = v.CanvasWidth - width
	if v.MaxScroll > 0 {
		v.ExtraTop = prestackToolbarScrollHeight
		v.ScrollRect = layoutRect{Left: 10, Top: 72, Right: maxLayoutInt(11, width-10), Bottom: 90}
	}
	return v
}

func prestackToolbarScrollPosition(width, page, position, request, trackPosition int) int {
	v := prestackToolbarViewportForSize(width, page)
	switch request {
	case 0: // SB_LINELEFT
		position -= 32
	case 1: // SB_LINERIGHT
		position += 32
	case 2: // SB_PAGELEFT
		position -= maxLayoutInt(32, width/2)
	case 3: // SB_PAGERIGHT
		position += maxLayoutInt(32, width/2)
	case 4, 5: // SB_THUMBPOSITION / SB_THUMBTRACK
		position = trackPosition
	case 6: // SB_LEFT
		position = 0
	case 7: // SB_RIGHT
		position = v.MaxScroll
	}
	return minLayoutInt(v.MaxScroll, maxLayoutInt(0, position))
}

func prestackPanelLayoutForSize(width, height, page int) prestackPanelLayout {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	// Keep the established two rows. A narrow viewport adds only a thin native
	// horizontal scrollbar between the tab strip and the controls.
	extraTop := prestackToolbarViewportForSize(width, page).ExtraTop
	sceneTop := 119
	if page == 0 {
		sceneTop = 156
	}
	sceneTop += extraTop
	contentTop := 114 + extraTop
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
		ToolbarTop:    76 + extraTop,
		ToolbarBottom: 132 + extraTop,
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
