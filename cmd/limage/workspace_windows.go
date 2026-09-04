//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const WM_DROPFILES = 0x0233

// These values are a persisted JSON contract. Keep them explicit and in a
// separate declaration from unrelated constants so iota cannot shift them.
const (
	workspaceMode2D      = 1
	workspaceMode3D      = 2
	workspaceModeCrooked = 3
)

type recentSegyFile struct {
	Path       string   `json:"path"`
	Mode       int      `json:"mode"`
	OpenedUnix int64    `json:"opened_unix"`
	Paths      []string `json:"paths,omitempty"`
}

type homeStateFile struct {
	Version       int              `json:"version"`
	LastWorkspace int              `json:"last_workspace"`
	Recent        []recentSegyFile `json:"recent"`
}

type homeLayout struct {
	cards        [3]RECT
	drop         RECT
	recentHeader RECT
	clearRecent  RECT
	recentRows   []RECT
}

var (
	pDragAcceptFiles = shell32.NewProc("DragAcceptFiles")
	pDragQueryFileW  = shell32.NewProc("DragQueryFileW")
	pDragFinish      = shell32.NewProc("DragFinish")

	pendingWorkspaceMode int
	activeWorkspaceMode  = workspaceMode2D

	startHomeState       homeStateFile
	startHomeStateLoaded bool
	startHomeHover       = -1 // 0..2 cards, 3 drop zone, 4 clear, 10+n row, 30+n remove
	startHomePressed     = -1
	startHomeDragActive  bool
	startHomeFonts       [5]uintptr
)

func setPendingWorkspaceMode(mode int) {
	if mode < workspaceMode2D || mode > workspaceModeCrooked {
		mode = workspaceMode2D
	}
	pendingWorkspaceMode = mode
}

func clearPendingWorkspaceMode() { pendingWorkspaceMode = 0 }

func resetHome3DTrace() {
	_ = os.MkdirAll(licenseDirectory(), 0700)
	_ = os.WriteFile(licenseDirectory()+`\home3d_trace.log`, nil, 0600)
}

func home3DTracef(format string, args ...any) {
	_ = os.MkdirAll(licenseDirectory(), 0700)
	f, err := os.OpenFile(licenseDirectory()+`\home3d_trace.log`, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05.000"), fmt.Sprintf(format, args...))
}

func acceptSegyDrops(h uintptr) {
	if h != 0 {
		pDragAcceptFiles.Call(h, 1)
	}
}

func isSegyPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	return ext == ".sgy" || ext == ".segy"
}

func droppedSegyPaths(hdrop uintptr) []string    { return droppedWorkspacePaths(hdrop, false) }
func droppedCrookedPaths(hdrop uintptr) []string { return droppedWorkspacePaths(hdrop, true) }

func droppedWorkspacePaths(hdrop uintptr, allowDirectories bool) []string {
	if hdrop == 0 {
		return nil
	}
	defer pDragFinish.Call(hdrop)
	count, _, _ := pDragQueryFileW.Call(hdrop, 0xffffffff, 0, 0)
	out := make([]string, 0, int(count))
	for i := uintptr(0); i < count; i++ {
		n, _, _ := pDragQueryFileW.Call(hdrop, i, 0, 0)
		if n == 0 {
			continue
		}
		buf := make([]uint16, int(n)+1)
		pDragQueryFileW.Call(hdrop, i, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		p := syscall.UTF16ToString(buf)
		if st, err := os.Stat(p); err == nil {
			if st.IsDir() && allowDirectories {
				out = append(out, p)
			} else if !st.IsDir() && isSegyPath(p) {
				out = append(out, p)
			}
		}
	}
	return out
}

func validateSegyForDrop(path string, owner uintptr) bool {
	f, err := segy.Open(path)
	if err != nil {
		message(owner, "拖放 SEG-Y", "无法打开：\n\n"+path+"\n\n"+err.Error(), MB_OK|MB_ICONERROR)
		return false
	}
	f.Close()
	return true
}

func homeStatePath() string { return licenseDirectory() + `\start_center.json` }

func loadHomeState() {
	if startHomeStateLoaded {
		return
	}
	startHomeStateLoaded = true
	startHomeState = homeStateFile{Version: 1, LastWorkspace: workspaceMode2D}
	if b, err := os.ReadFile(homeStatePath()); err == nil {
		var d homeStateFile
		if json.Unmarshal(b, &d) == nil && d.Version == 1 {
			startHomeState = d
		}
	}
	// Remove missing entries and keep the list compact. This prevents a moved
	// project from leaving dead rows on the Start Center forever.
	clean := make([]recentSegyFile, 0, len(startHomeState.Recent))
	for _, r := range startHomeState.Recent {
		if r.Mode == workspaceModeCrooked && len(r.Paths) > 1 {
			paths := make([]string, 0, len(r.Paths))
			for _, path := range r.Paths {
				if st, err := os.Stat(path); err == nil && !st.IsDir() && isSegyPath(path) {
					paths = append(paths, path)
				}
			}
			if len(paths) > 0 {
				r.Paths = paths
				clean = append(clean, r)
			}
		} else if st, err := os.Stat(r.Path); err == nil && ((r.Mode == workspaceModeCrooked && st.IsDir()) || (!st.IsDir() && isSegyPath(r.Path))) {
			clean = append(clean, r)
		}
		if len(clean) >= 5 {
			break
		}
	}
	startHomeState.Recent = clean
	if startHomeState.LastWorkspace < workspaceMode2D || startHomeState.LastWorkspace > workspaceModeCrooked {
		startHomeState.LastWorkspace = workspaceMode2D
	}
}

func rememberRecentCrookedProject(path string, paths []string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	loadHomeState()
	entry := recentSegyFile{Path: filepath.Clean(path), Mode: workspaceModeCrooked, OpenedUnix: time.Now().Unix()}
	if len(paths) > 1 {
		entry.Paths = make([]string, 0, len(paths))
		for _, item := range paths {
			if isSegyPath(item) {
				entry.Paths = append(entry.Paths, filepath.Clean(item))
			}
		}
	}
	key := strings.ToLower(entry.Path + "|" + strings.Join(entry.Paths, "|"))
	out := []recentSegyFile{entry}
	for _, r := range startHomeState.Recent {
		other := strings.ToLower(filepath.Clean(r.Path) + "|" + strings.Join(r.Paths, "|"))
		if other == key {
			continue
		}
		out = append(out, r)
		if len(out) >= 5 {
			break
		}
	}
	startHomeState.LastWorkspace = workspaceModeCrooked
	startHomeState.Recent = out
	saveHomeState()
}

func saveHomeState() {
	loadHomeState()
	b, err := json.MarshalIndent(startHomeState, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(licenseDirectory(), 0700) != nil {
		return
	}
	_ = os.WriteFile(homeStatePath(), append(b, '\n'), 0600)
}

func rememberRecentSegy(path string, mode int) {
	if path == "" || !isSegyPath(path) {
		return
	}
	loadHomeState()
	if mode < workspaceMode2D || mode > workspaceModeCrooked {
		mode = workspaceMode2D
	}
	canon := filepath.Clean(path)
	out := []recentSegyFile{{Path: canon, Mode: mode, OpenedUnix: time.Now().Unix()}}
	for _, r := range startHomeState.Recent {
		if strings.EqualFold(filepath.Clean(r.Path), canon) {
			continue
		}
		out = append(out, r)
		if len(out) >= 5 {
			break
		}
	}
	startHomeState.LastWorkspace = mode
	startHomeState.Recent = out
	saveHomeState()
}

func workspaceModeName(mode int) string {
	switch mode {
	case workspaceMode3D:
		return "三维"
	case workspaceModeCrooked:
		return "弯线"
	default:
		return "二维"
	}
}

func recentDateLabel(ts int64) string {
	if ts <= 0 {
		return ""
	}
	now := time.Now()
	t := time.Unix(ts, 0)
	y0, m0, d0 := now.Date()
	y1, m1, d1 := t.Date()
	if y0 == y1 && m0 == m1 && d0 == d1 {
		return "今天"
	}
	yesterday := now.AddDate(0, 0, -1)
	y2, m2, d2 := yesterday.Date()
	if y2 == y1 && m2 == m1 && d2 == d1 {
		return "昨天"
	}
	if y0 == y1 {
		return fmt.Sprintf("%d/%d", int(m1), d1)
	}
	return fmt.Sprintf("%d/%d/%d", y1, int(m1), d1)
}

// recommendWorkspaceMode intentionally uses only trace headers. A strong IL/XL
// grid is enough to recommend 3-D; otherwise the safe default is 2-D. Crooked
// line recommendation will be upgraded when the dedicated XY-geometry map is
// introduced, so a non-grid line is never falsely forced into crooked mode.
func recommendWorkspaceMode(path string) int {
	f, err := segy.Open(path)
	if err != nil {
		return workspaceMode2D
	}
	defer f.Close()
	d, err := geometrycore.Detect(f, 3000)
	if err == nil && d.Kind == geometrycore.KindRegular3D {
		return workspaceMode3D
	}
	return workspaceMode2D
}

func setComparePathDirect(which int, path string) bool {
	if compareHwnd == 0 || path == "" || !validateSegyForDrop(path, compareHwnd) {
		return false
	}
	if which == 0 {
		compareAPath = path
		compareBPath = ""
		compareBVisible = false
		compareShowDiff = false
		compareZoomToggleValid = false
		if cc.compare != 0 {
			pSendMessageW.Call(cc.compare, BM_SETCHECK, 0, 0)
		}
		if cc.diff != 0 {
			pSendMessageW.Call(cc.diff, BM_SETCHECK, 0, 0)
			pEnableWindow.Call(cc.diff, 0)
		}
	} else {
		if path == compareAPath {
			message(compareHwnd, "拖放 SEG-Y", "A 与 B 不能使用同一个文件。", MB_OK|MB_ICONINFORMATION)
			return false
		}
		compareBPath = path
		compareBVisible = true
		compareZoomToggleValid = false
		if cc.compare != 0 {
			pSendMessageW.Call(cc.compare, BM_SETCHECK, BST_CHECKED, 0)
		}
		if cc.diff != 0 {
			pEnableWindow.Call(cc.diff, 1)
		}
	}
	layoutComparePathLabels()
	compareAutoGeom = false
	invalidateCompareTimeData()
	updateCompareWelcomeState()
	if compareAPath != "" {
		startAutoDetectGeometry()
	}
	return true
}

func completeWorkspaceOpen() {
	mode := pendingWorkspaceMode
	if mode == 0 {
		mode = activeWorkspaceMode
		if mode == 0 {
			mode = workspaceMode2D
		}
	}
	pendingWorkspaceMode = 0
	activeWorkspaceMode = mode
	if sf != nil {
		rememberRecentSegy(sf.Info.Path, mode)
		phase1AdoptLegacyDataset(sf.Info.Path, mode)
	}

	// Keep the legacy pending-workspace path compatible with callers that load a
	// range first and only then request Volume. The explicit Home 3-D entry does
	// not use this branch; it creates and loads a Volume shell directly.
	if mode == workspaceMode3D && sf != nil {
		home3DTracef("completeWorkspaceOpen: mode=3D sf=%s", filepath.Base(sf.Info.Path))
		if compareHwnd != 0 {
			pShowWindow.Call(compareHwnd, SW_HIDE)
		}
		volumeFocusPanel = -1
		volumeHoverAxis = -1
		volume3D = true
		showVolumeWindow() // exact same function used by the established “体” button
		if volumeHwnd != 0 {
			forceVolume3DViewState()
			pShowWindow.Call(volumeHwnd, SW_SHOW)
			pUpdateWindow.Call(volumeHwnd)
			pSetForeground.Call(volumeHwnd)
			home3DTracef("completeWorkspaceOpen: Volume created hwnd=%#x volume3D=%v focus=%d", volumeHwnd, volume3D, volumeFocusPanel)
			return
		}
		home3DTracef("completeWorkspaceOpen: Volume creation FAILED")
		// If the Volume top-level window could not be created, keep the loaded
		// dataset accessible in the 2-D workspace rather than leaving a blank app.
	}

	updateCompareWelcomeState()
	if compareHwnd != 0 {
		switch mode {
		case workspaceModeCrooked:
			pSetWindowTextW.Call(compareHwnd, uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" [x64] - 弯线工作区"))))
			setText(cc.status, "弯线工作区：按 SEG-Y 道序展开剖面；可继续使用自动几何识别、比/差、频谱与标注。")
		case workspaceMode3D:
			pSetWindowTextW.Call(compareHwnd, uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" [x64] - 三维数据体"))))
		default:
			pSetWindowTextW.Call(compareHwnd, uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" [x64] - 二维剖面"))))
		}
	}
}

func openHomeFile(path string, requestedMode int) {
	if path == "" {
		return
	}
	if requestedMode == workspaceModeCrooked {
		if stat, err := os.Stat(path); err == nil && stat.IsDir() {
			openCrookedProjectFolder(path)
			return
		}
	}
	if !isSegyPath(path) {
		return
	}
	// Home, Recent and the drop zone all enter through the same Application
	// router. KindAuto is resolved using the frozen 75/.58 thresholds.
	openApplicationPath(path, requestedMode)
}

func chooseHome3DFile() {
	if compareHwnd == 0 {
		return
	}
	if p := openDataDialog(compareHwnd); p != "" {
		openHomeFile(p, workspaceMode3D)
	}
}

func chooseHomeFile(mode int) {
	if compareHwnd == 0 {
		return
	}
	if mode == workspaceMode3D {
		chooseHome3DFile()
		return
	}
	if mode == workspaceModeCrooked {
		openEmptyCrookedWorkspace(compareHwnd)
		return
	}
	if p := openDataDialog(compareHwnd); p != "" {
		openHomeFile(p, mode)
	}
}

func handleWorkspaceDrop(hdrop uintptr, targetMode int) {
	handleWorkspaceDropPaths(droppedSegyPaths(hdrop), targetMode)
}

func handleWorkspaceDropPaths(paths []string, targetMode int) {
	if len(paths) == 0 {
		owner := compareHwnd
		if volumeHwnd != 0 {
			owner = volumeHwnd
		}
		message(owner, "拖放 SEG-Y", "请拖入 .sgy 或 .segy 文件。", MB_OK|MB_ICONINFORMATION)
		return
	}
	if targetMode == workspaceModeCrooked {
		if len(paths) == 1 {
			if stat, err := os.Stat(paths[0]); err == nil && stat.IsDir() {
				openCrookedProjectFolder(paths[0])
			} else {
				openCrookedProjectPaths(paths)
			}
		} else {
			openCrookedProjectPaths(paths)
		}
		return
	}
	filtered := paths[:0]
	for _, path := range paths {
		if isSegyPath(path) {
			filtered = append(filtered, path)
		}
	}
	paths = filtered
	if len(paths) == 0 {
		return
	}

	// Dropping onto the 3-D window loads A when the Home-created shell is still
	// empty; once A is ready, the same gesture keeps the established "drop B"
	// comparison behavior.
	if volumeHwnd != 0 && targetMode == workspaceMode3D {
		path := paths[0]
		if !volumeScenes[0].valid && volumeF == nil {
			openApplicationPath(path, workspaceMode3D)
			return
		}
		if !validateSegyForDrop(path, volumeHwnd) {
			return
		}
		if volumeScenes[0].valid && path == volumeScenes[0].path {
			message(volumeHwnd, "拖放 SEG-Y", "该文件已经是当前 3D 的 A 数据。", MB_OK|MB_ICONINFORMATION)
			return
		}
		compareBPath = path
		startVolumePrepareForSide(path, 1)
		return
	}

	if compareAPath == "" || sf == nil {
		if !openApplicationPath(paths[0], targetMode) {
			return
		}
		targetMode = activeWorkspaceMode
		// Dropping two files at once is a convenient A/B compare shortcut.
		if len(paths) >= 2 {
			if setComparePathDirect(1, paths[1]) && targetMode == workspaceMode3D && volumeHwnd != 0 {
				startVolumePrepareForSide(paths[1], 1)
			}
		}
		if len(paths) > 2 {
			message(compareHwnd, "拖放 SEG-Y", "已使用前两个 SEG-Y 作为 A/B；其余文件未加载。", MB_OK|MB_ICONINFORMATION)
		}
		return
	}

	// A already exists: the next dropped SEG-Y becomes B instead of silently
	// replacing the current reference dataset.
	setComparePathDirect(1, paths[0])
	if len(paths) > 1 {
		message(compareHwnd, "拖放 SEG-Y", "当前已有 A，因此仅将第一个拖入文件作为 B 加载。", MB_OK|MB_ICONINFORMATION)
	}
}

func compareWorkspaceHandles() []uintptr {
	return []uintptr{
		cc.open, cc.save, cc.palette, cc.fixed, cc.mark, cc.data, cc.params, cc.zoom, cc.origin,
		cc.mail, cc.update, cc.about, cc.compare, cc.diff, cc.volume, cc.spectrum, cc.export, cc.traceInspect,
		cc.aLabel, cc.bLabel, cc.modeLabel, cc.mode, cc.slider, cc.timeEdit, cc.prev, cc.next, cc.timeUnit,
		cc.refresh, cc.close, cc.status, cc.ilLabel, cc.xlLabel, cc.ilByte, cc.xlByte,
		cc.autoGeom, cc.swapGeom, cc.crosshair, cc.rectTool, cc.ellipseTool, cc.lineTool, cc.clearAnn,
	}
}

func setControlsVisible(handles []uintptr, visible bool) {
	cmd := uintptr(SW_HIDE)
	if visible {
		cmd = SW_SHOW
	}
	for _, h := range handles {
		if h != 0 {
			pShowWindow.Call(h, cmd)
		}
	}
}

func updateCompareWelcomeState() {
	if compareHwnd == 0 {
		return
	}
	welcome := compareAPath == ""
	setControlsVisible(compareWorkspaceHandles(), !welcome)
	if welcome {
		setCompareTraceInspectMode(false)
		loadHomeState()
		startHomeHover = -1
		startHomePressed = -1
		startHomeDragActive = false
		pSetWindowTextW.Call(compareHwnd, uintptr(unsafe.Pointer(u16(APP_NAME+" v"+APP_VERSION+" [x64] - 地震浏览与对比工作台"))))
		if cc.progress != 0 {
			pShowWindow.Call(cc.progress, SW_HIDE)
		}
	} else {
		showTimeControls(true)
		layoutComparePathLabels()
		layoutCompareStatusProgress()
	}
	destroyCompareBaseCache()
	invalidateCompareBase()
}

func layoutCompareWelcomeControls() {
	// The Start Center is painted directly into the parent window. Keeping the
	// whole surface in one GDI scene avoids the old "Windows Button" appearance
	// and makes hover/card/drop-zone states consistent.
	if compareHwnd != 0 && compareAPath == "" {
		invalidateCompareBase()
	}
}

func ensureStartHomeFonts() {
	if startHomeFonts[0] != 0 {
		return
	}
	face := uintptr(unsafe.Pointer(u16("Microsoft YaHei UI")))
	// hero, section/card title, body, small, brand
	startHomeFonts[0], _, _ = pCreateFontW.Call(30, 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, face)
	startHomeFonts[1], _, _ = pCreateFontW.Call(19, 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, face)
	startHomeFonts[2], _, _ = pCreateFontW.Call(15, 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, face)
	startHomeFonts[3], _, _ = pCreateFontW.Call(13, 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, face)
	startHomeFonts[4], _, _ = pCreateFontW.Call(20, 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, face)
}

func releaseStartHomeFonts() {
	for i, h := range startHomeFonts {
		if h != 0 {
			pDeleteObject.Call(h)
			startHomeFonts[i] = 0
		}
	}
}

func computeStartHomeLayout() homeLayout {
	cw, ch := clientSize(compareHwnd)
	contentW := minInt(1060, maxInt(780, cw-92))
	if contentW > cw-48 {
		contentW = maxInt(660, cw-48)
	}
	left := maxInt(24, (cw-contentW)/2)
	gap := 20
	cardW := (contentW - 2*gap) / 3
	cardH := 194
	cardY := 138
	var l homeLayout
	for i := 0; i < 3; i++ {
		x := left + i*(cardW+gap)
		l.cards[i] = RECT{Left: int32(x), Top: int32(cardY), Right: int32(x + cardW), Bottom: int32(cardY + cardH)}
	}
	dropY := cardY + cardH + 18
	l.drop = RECT{Left: int32(left), Top: int32(dropY), Right: int32(left + contentW), Bottom: int32(dropY + 116)}
	headerY := dropY + 128
	l.recentHeader = RECT{Left: int32(left), Top: int32(headerY), Right: int32(left + contentW), Bottom: int32(headerY + 28)}
	l.clearRecent = RECT{Left: int32(left + contentW - 78), Top: int32(headerY + 2), Right: int32(left + contentW), Bottom: int32(headerY + 26)}
	rowH := 49
	maxRows := minInt(5, len(startHomeState.Recent))
	if ch > 0 {
		fit := (ch - (headerY + 31) - 14) / rowH
		if fit < maxRows {
			maxRows = maxInt(0, fit)
		}
	}
	l.recentRows = make([]RECT, maxRows)
	for i := 0; i < maxRows; i++ {
		y := headerY + 31 + i*rowH
		l.recentRows[i] = RECT{Left: int32(left), Top: int32(y), Right: int32(left + contentW), Bottom: int32(y + rowH - 3)}
	}
	return l
}

func drawHomeText(hdc uintptr, font uintptr, color uintptr, text string, r RECT, flags uintptr) {
	old, _, _ := pSelectObject.Call(hdc, font)
	pSetTextColor.Call(hdc, color)
	pSetBkMode.Call(hdc, TRANSPARENT)
	pDrawTextW.Call(hdc, uintptr(unsafe.Pointer(u16(text))), uintptr(^uint32(0)), uintptr(unsafe.Pointer(&r)), flags)
	pSelectObject.Call(hdc, old)
}

func drawHomeRoundRect(hdc uintptr, r RECT, fill, border uintptr, radius int) {
	brush, _, _ := pCreateSolidBrush.Call(fill)
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, border)
	oldBrush, _, _ := pSelectObject.Call(hdc, brush)
	oldPen, _, _ := pSelectObject.Call(hdc, pen)
	pRoundRect.Call(hdc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom), uintptr(radius), uintptr(radius))
	pSelectObject.Call(hdc, oldBrush)
	pSelectObject.Call(hdc, oldPen)
	pDeleteObject.Call(brush)
	pDeleteObject.Call(pen)
}

func drawHomeLine(hdc uintptr, x0, y0, x1, y1 int, color uintptr, width int) {
	pen, _, _ := pCreatePen.Call(PS_SOLID, uintptr(width), color)
	old, _, _ := pSelectObject.Call(hdc, pen)
	pMoveToEx.Call(hdc, uintptr(x0), uintptr(y0), 0)
	pLineTo.Call(hdc, uintptr(x1), uintptr(y1))
	pSelectObject.Call(hdc, old)
	pDeleteObject.Call(pen)
}

func drawHomeDashedRect(hdc uintptr, r RECT, color uintptr) {
	const dash, gap = 8, 5
	for x := int(r.Left) + 10; x < int(r.Right)-10; x += dash + gap {
		x1 := minInt(x+dash, int(r.Right)-10)
		drawHomeLine(hdc, x, int(r.Top), x1, int(r.Top), color, 1)
		drawHomeLine(hdc, x, int(r.Bottom)-1, x1, int(r.Bottom)-1, color, 1)
	}
	for y := int(r.Top) + 10; y < int(r.Bottom)-10; y += dash + gap {
		y1 := minInt(y+dash, int(r.Bottom)-10)
		drawHomeLine(hdc, int(r.Left), y, int(r.Left), y1, color, 1)
		drawHomeLine(hdc, int(r.Right)-1, y, int(r.Right)-1, y1, color, 1)
	}
}

func drawHomeIcon(hdc uintptr, kind, cx, cy int, color uintptr) {
	switch kind {
	case 0: // 2-D section: frame + seismic wiggles
		drawHomeLine(hdc, cx-25, cy-18, cx+25, cy-18, color, 2)
		drawHomeLine(hdc, cx-25, cy-18, cx-25, cy+18, color, 2)
		drawHomeLine(hdc, cx-25, cy+18, cx+25, cy+18, color, 2)
		for row := -10; row <= 10; row += 10 {
			xs := []int{-20, -12, -4, 4, 12, 20}
			for i := 0; i < len(xs)-1; i++ {
				y0 := cy + row
				y1 := cy + row
				if i%2 == 0 {
					y0 -= 3
					y1 += 3
				} else {
					y0 += 3
					y1 -= 3
				}
				drawHomeLine(hdc, cx+xs[i], y0, cx+xs[i+1], y1, color, 1)
			}
		}
	case 1: // cube
		drawHomeLine(hdc, cx-19, cy-10, cx+10, cy-10, color, 2)
		drawHomeLine(hdc, cx+10, cy-10, cx+10, cy+19, color, 2)
		drawHomeLine(hdc, cx+10, cy+19, cx-19, cy+19, color, 2)
		drawHomeLine(hdc, cx-19, cy+19, cx-19, cy-10, color, 2)
		drawHomeLine(hdc, cx-19, cy-10, cx-7, cy-22, color, 2)
		drawHomeLine(hdc, cx+10, cy-10, cx+22, cy-22, color, 2)
		drawHomeLine(hdc, cx+10, cy+19, cx+22, cy+7, color, 2)
		drawHomeLine(hdc, cx-7, cy-22, cx+22, cy-22, color, 2)
		drawHomeLine(hdc, cx+22, cy-22, cx+22, cy+7, color, 2)
	case 2: // crooked line
		pts := [][2]int{{-27, 11}, {-14, 4}, {-9, -11}, {5, -15}, {13, -3}, {27, -10}}
		for i := 0; i < len(pts)-1; i++ {
			drawHomeLine(hdc, cx+pts[i][0], cy+pts[i][1], cx+pts[i+1][0], cy+pts[i+1][1], color, 2)
		}
		brush, _, _ := pCreateSolidBrush.Call(color)
		old, _, _ := pSelectObject.Call(hdc, brush)
		for _, p := range pts {
			pEllipse.Call(hdc, uintptr(cx+p[0]-2), uintptr(cy+p[1]-2), uintptr(cx+p[0]+3), uintptr(cy+p[1]+3))
		}
		pSelectObject.Call(hdc, old)
		pDeleteObject.Call(brush)
	}
}

func drawHomeDropIcon(hdc uintptr, cx, cy int, color uintptr) {
	// Minimal document + downward arrow. It remains legible at low DPI and does
	// not depend on icon-font availability.
	drawHomeLine(hdc, cx-13, cy-12, cx+8, cy-12, color, 1)
	drawHomeLine(hdc, cx-13, cy-12, cx-13, cy+11, color, 1)
	drawHomeLine(hdc, cx-13, cy+11, cx+13, cy+11, color, 1)
	drawHomeLine(hdc, cx+13, cy+11, cx+13, cy-7, color, 1)
	drawHomeLine(hdc, cx+8, cy-12, cx+13, cy-7, color, 1)
	drawHomeLine(hdc, cx+8, cy-12, cx+8, cy-7, color, 1)
	drawHomeLine(hdc, cx+8, cy-7, cx+13, cy-7, color, 1)
	drawHomeLine(hdc, cx, cy-4, cx, cy+5, color, 2)
	drawHomeLine(hdc, cx, cy+5, cx-4, cy+1, color, 2)
	drawHomeLine(hdc, cx, cy+5, cx+4, cy+1, color, 2)
}

func drawStartCenter(hdc uintptr) {
	ensureStartHomeFonts()
	loadHomeState()
	cw, _ := clientSize(compareHwnd)
	l := computeStartHomeLayout()

	brandR := RECT{Left: 42, Top: 22, Right: 320, Bottom: 50}
	drawHomeText(hdc, startHomeFonts[4], rgbRef(31, 41, 55), APP_NAME, brandR, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	verR := RECT{Left: int32(cw - 142), Top: 26, Right: int32(cw - 42), Bottom: 46}
	drawHomeText(hdc, startHomeFonts[3], rgbRef(156, 163, 175), "v"+APP_VERSION+"  x64", verR, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)

	heroR := RECT{Left: 0, Top: 59, Right: int32(cw), Bottom: 96}
	drawHomeText(hdc, startHomeFonts[0], rgbRef(17, 24, 39), "开始使用 "+APP_NAME, heroR, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	subR := RECT{Left: 0, Top: 98, Right: int32(cw), Bottom: 124}
	drawHomeText(hdc, startHomeFonts[2], rgbRef(75, 85, 99), "选择工作区，或直接打开一个 SEG-Y 文件", subR, DT_CENTER|DT_VCENTER|DT_SINGLELINE)

	titles := []string{"二维剖面", "三维数据体", "弯曲测线"}
	descs := []string{"查看单条或二维地震剖面", "IL / XL / Time 三维联动浏览", "Crooked-line 地震数据浏览"}
	fits := []string{"适合：常规 2D 数据浏览", "适合：规则三维体数据", "适合：非规则测线 / 弯线数据"}
	for i, r := range l.cards {
		hover := startHomeHover == i
		pressed := startHomePressed == i && hover
		if hover && !pressed {
			shadow := RECT{Left: r.Left + 2, Top: r.Top + 3, Right: r.Right + 2, Bottom: r.Bottom + 3}
			drawHomeRoundRect(hdc, shadow, rgbRef(242, 244, 247), rgbRef(242, 244, 247), 12)
		}
		fill := rgbRef(255, 255, 255)
		border := rgbRef(229, 231, 235)
		if hover {
			fill = rgbRef(248, 251, 255)
			border = rgbRef(96, 165, 250)
		}
		if pressed {
			fill = rgbRef(239, 246, 255)
			border = rgbRef(59, 130, 246)
		}
		drawHomeRoundRect(hdc, r, fill, border, 12)
		off := 0
		if pressed {
			off = 1
		}
		cx := int((r.Left + r.Right) / 2)
		drawHomeIcon(hdc, i, cx, int(r.Top)+43+off, rgbRef(37, 99, 235))
		titleR := RECT{Left: r.Left + 18, Top: r.Top + 72 + int32(off), Right: r.Right - 18, Bottom: r.Top + 101 + int32(off)}
		drawHomeText(hdc, startHomeFonts[1], rgbRef(31, 41, 55), titles[i], titleR, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		descR := RECT{Left: r.Left + 16, Top: r.Top + 105 + int32(off), Right: r.Right - 16, Bottom: r.Top + 130 + int32(off)}
		drawHomeText(hdc, startHomeFonts[3], rgbRef(75, 85, 99), descs[i], descR, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		fitR := RECT{Left: r.Left + 14, Top: r.Top + 130 + int32(off), Right: r.Right - 14, Bottom: r.Top + 153 + int32(off)}
		drawHomeText(hdc, startHomeFonts[3], rgbRef(156, 163, 175), fits[i], fitR, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		openColor := rgbRef(37, 99, 235)
		if hover {
			openColor = rgbRef(29, 78, 216)
		}
		openR := RECT{Left: r.Left + 20, Top: r.Bottom - 36 + int32(off), Right: r.Right - 20, Bottom: r.Bottom - 10 + int32(off)}
		drawHomeText(hdc, startHomeFonts[2], openColor, "打开  →", openR, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		if startHomeState.LastWorkspace == i+1 {
			tag := RECT{Left: r.Right - 70, Top: r.Top + 10, Right: r.Right - 12, Bottom: r.Top + 29}
			drawHomeRoundRect(hdc, tag, rgbRef(247, 250, 255), rgbRef(229, 238, 252), 8)
			drawHomeText(hdc, startHomeFonts[3], rgbRef(96, 125, 170), "上次使用", tag, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		}
	}

	// The drop zone is deliberately the second interaction center. A dashed
	// outline makes its role obvious without competing with the three cards.
	dropHover := startHomeHover == 3 || startHomeDragActive
	dropPressed := startHomePressed == 3 && startHomeHover == 3
	dropFill := rgbRef(250, 251, 252)
	dropBorder := rgbRef(203, 213, 225)
	if dropHover {
		dropFill = rgbRef(241, 247, 255)
		dropBorder = rgbRef(96, 165, 250)
	}
	if dropPressed {
		dropFill = rgbRef(232, 242, 255)
		dropBorder = rgbRef(59, 130, 246)
	}
	drawHomeRoundRect(hdc, l.drop, dropFill, dropFill, 10)
	drawHomeDashedRect(hdc, l.drop, dropBorder)
	cx := int((l.drop.Left + l.drop.Right) / 2)
	drawHomeDropIcon(hdc, cx, int(l.drop.Top)+22, rgbRef(96, 125, 170))
	dropMain := RECT{Left: l.drop.Left + 24, Top: l.drop.Top + 34, Right: l.drop.Right - 24, Bottom: l.drop.Top + 59}
	drawHomeText(hdc, startHomeFonts[1], rgbRef(55, 65, 81), "将 SEG-Y 文件拖到这里即可打开", dropMain, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	dropSub := RECT{Left: l.drop.Left + 24, Top: l.drop.Top + 57, Right: l.drop.Right - 24, Bottom: l.drop.Top + 78}
	drawHomeText(hdc, startHomeFonts[3], rgbRef(107, 114, 128), ".sgy    .segy", dropSub, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	button := RECT{Left: (l.drop.Left+l.drop.Right)/2 - 79, Top: l.drop.Bottom - 32, Right: (l.drop.Left+l.drop.Right)/2 + 79, Bottom: l.drop.Bottom - 7}
	btnFill := rgbRef(255, 255, 255)
	if dropHover {
		btnFill = rgbRef(248, 251, 255)
	}
	drawHomeRoundRect(hdc, button, btnFill, rgbRef(147, 197, 253), 7)
	drawHomeText(hdc, startHomeFonts[3], rgbRef(37, 99, 235), "选择 SEG-Y 文件", button, DT_CENTER|DT_VCENTER|DT_SINGLELINE)

	drawHomeText(hdc, startHomeFonts[1], rgbRef(55, 65, 81), "最近打开", l.recentHeader, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
	if len(startHomeState.Recent) > 0 {
		clearColor := rgbRef(156, 163, 175)
		if startHomeHover == 4 {
			clearColor = rgbRef(75, 85, 99)
		}
		drawHomeText(hdc, startHomeFonts[3], clearColor, "清空记录", l.clearRecent, DT_RIGHT|DT_VCENTER|DT_SINGLELINE)
	}
	if len(l.recentRows) == 0 {
		empty := RECT{Left: l.recentHeader.Left, Top: l.recentHeader.Bottom + 8, Right: l.recentHeader.Right, Bottom: l.recentHeader.Bottom + 48}
		drawHomeText(hdc, startHomeFonts[3], rgbRef(156, 163, 175), "尚无最近打开文件。直接拖入 SEG-Y 即可开始。", empty, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		return
	}
	for i, rr := range l.recentRows {
		rowHover := startHomeHover == 10+i
		removeHover := startHomeHover == 30+i
		if rowHover || removeHover {
			drawHomeRoundRect(hdc, rr, rgbRef(247, 250, 255), rgbRef(226, 235, 248), 7)
		}
		r := startHomeState.Recent[i]
		nameR := RECT{Left: rr.Left + 16, Top: rr.Top + 3, Right: rr.Right - 275, Bottom: rr.Top + 25}
		pathR := RECT{Left: rr.Left + 16, Top: rr.Top + 23, Right: rr.Right - 275, Bottom: rr.Bottom - 2}
		modeR := RECT{Left: rr.Right - 260, Top: rr.Top, Right: rr.Right - 180, Bottom: rr.Bottom}
		dateR := RECT{Left: rr.Right - 175, Top: rr.Top, Right: rr.Right - 112, Bottom: rr.Bottom}
		openR := RECT{Left: rr.Right - 105, Top: rr.Top, Right: rr.Right - 46, Bottom: rr.Bottom}
		removeR := RECT{Left: rr.Right - 37, Top: rr.Top + 6, Right: rr.Right - 10, Bottom: rr.Bottom - 6}
		drawHomeText(hdc, startHomeFonts[2], rgbRef(55, 65, 81), shortPath(filepath.Base(r.Path), 58), nameR, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		drawHomeText(hdc, startHomeFonts[3], rgbRef(156, 163, 175), shortPath(filepath.Dir(r.Path), 78), pathR, DT_LEFT|DT_VCENTER|DT_SINGLELINE)
		drawHomeText(hdc, startHomeFonts[3], rgbRef(107, 114, 128), workspaceModeName(r.Mode), modeR, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		drawHomeText(hdc, startHomeFonts[3], rgbRef(156, 163, 175), recentDateLabel(r.OpenedUnix), dateR, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		openColor := rgbRef(96, 125, 170)
		if rowHover {
			openColor = rgbRef(37, 99, 235)
		}
		drawHomeText(hdc, startHomeFonts[3], openColor, "打开 →", openR, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
		removeColor := rgbRef(190, 195, 203)
		if removeHover {
			removeColor = rgbRef(107, 114, 128)
		}
		drawHomeText(hdc, startHomeFonts[3], removeColor, "×", removeR, DT_CENTER|DT_VCENTER|DT_SINGLELINE)
	}
}

func startHomeHitAt(x, y int) int {
	if compareAPath != "" || compareHwnd == 0 {
		return -1
	}
	l := computeStartHomeLayout()
	for i, r := range l.cards {
		if comparePointInRect(x, y, r) {
			return i
		}
	}
	if comparePointInRect(x, y, l.drop) {
		return 3
	}
	if len(startHomeState.Recent) > 0 && comparePointInRect(x, y, l.clearRecent) {
		return 4
	}
	for i, r := range l.recentRows {
		removeR := RECT{Left: r.Right - 40, Top: r.Top, Right: r.Right, Bottom: r.Bottom}
		if comparePointInRect(x, y, removeR) {
			return 30 + i
		}
		if comparePointInRect(x, y, r) {
			return 10 + i
		}
	}
	return -1
}

func setStartHomeCursor(hit int) {
	id := uintptr(IDC_ARROW)
	if hit >= 0 {
		id = IDC_HAND
	}
	if c, _, _ := pLoadCursorW.Call(0, id); c != 0 {
		pSetCursor.Call(c)
	}
}

func updateStartHomeHover(x, y int) bool {
	h := startHomeHitAt(x, y)
	setStartHomeCursor(h)
	if h == startHomeHover {
		return false
	}
	startHomeHover = h
	invalidateCompareBase()
	return true
}

func removeRecentAt(index int) {
	loadHomeState()
	if index < 0 || index >= len(startHomeState.Recent) {
		return
	}
	startHomeState.Recent = append(startHomeState.Recent[:index], startHomeState.Recent[index+1:]...)
	saveHomeState()
	startHomeHover = -1
	startHomePressed = -1
	invalidateCompareBase()
}

func clearRecentHistory() {
	loadHomeState()
	startHomeState.Recent = nil
	saveHomeState()
	startHomeHover = -1
	startHomePressed = -1
	invalidateCompareBase()
}

func activateStartHomeHit(hit int) bool {
	switch {
	case hit >= 0 && hit <= 2:
		chooseHomeFile(hit + 1)
		return true
	case hit == 3:
		chooseHomeFile(0)
		return true
	case hit == 4:
		clearRecentHistory()
		return true
	case hit >= 10 && hit < 15:
		i := hit - 10
		if i < len(startHomeState.Recent) {
			entry := startHomeState.Recent[i]
			if entry.Mode == workspaceModeCrooked && len(entry.Paths) > 1 {
				openCrookedProjectPaths(entry.Paths)
			} else {
				openHomeFile(entry.Path, entry.Mode)
			}
			return true
		}
	case hit >= 30 && hit < 35:
		removeRecentAt(hit - 30)
		return true
	}
	return false
}

func beginStartHomePress(x, y int) bool {
	h := startHomeHitAt(x, y)
	if h < 0 {
		return false
	}
	startHomePressed = h
	startHomeHover = h
	invalidateCompareBase()
	return true
}

func endStartHomePress(x, y int) bool {
	if startHomePressed < 0 {
		return false
	}
	pressed := startHomePressed
	hit := startHomeHitAt(x, y)
	startHomePressed = -1
	startHomeHover = hit
	invalidateCompareBase()
	if pressed == hit {
		return activateStartHomeHit(pressed)
	}
	return true
}

func activateStartHomeAt(x, y int) bool {
	return activateStartHomeHit(startHomeHitAt(x, y))
}
