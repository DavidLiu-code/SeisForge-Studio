//go:build windows

package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const displayDistanceReadyMessage = WM_USER + 246

type displayDistanceSettings struct {
	Enabled, Manual bool
	SpacingM        float64
}

type displayDistanceKey struct {
	file     *segy.File
	geometry *segy.GeometryIndex
	mode     int
	line     int32
}

type displayDistanceState struct {
	path             string
	enabled          bool
	manual           [3]displayDistanceSettings
	key              displayDistanceKey
	version          uint64
	fallback, actual *displaycore.DistanceReference
	manualReference  *displaycore.DistanceReference
	manualSpacing    float64
	gridStep         float64
	first, last      float64
	fallbackIDs      []float64
	source           os.FileInfo
	actualSource     string
	cancel           context.CancelFunc
	requested        bool
}

type displayDistanceResult struct {
	version uint64
	actual  *displaycore.DistanceReference
	source  string
}

var displayDistances = make(map[uintptr]*displayDistanceState)
var displayDistanceVersion uint64
var displayDistancePendingMu sync.Mutex
var displayDistancePending = make(map[uintptr]displayDistanceResult)

func displayDistanceCurrentKey(owner uintptr) displayDistanceKey {
	if owner == 0 {
		return displayDistanceKey{}
	}
	if owner == compareHwnd {
		if compareMode == 2 {
			return displayDistanceKey{}
		}
		if compareTD != nil && !compareTD.traceOrderFallback {
			return displayDistanceKey{file: compareTD.fa, geometry: compareTD.ga, mode: compareMode, line: compareLineCoord}
		}
		if compareTD != nil {
			return displayDistanceKey{file: compareTD.fa, mode: 2}
		}
		// Loading/replacing B may temporarily detach the pair of readers; A's
		// distance reference must not jump back to 20 m during that transition.
		if s := displayDistances[owner]; s != nil && s.path == compareAPath && (s.key.mode == 2 || s.key.mode == compareMode && s.key.line == compareLineCoord) {
			return s.key
		}
		if sf != nil && sf.Info.Path == compareAPath {
			return displayDistanceKey{file: sf, mode: 2}
		}
		return displayDistanceKey{}
	}
	if owner == hwnd {
		return displayDistanceKey{file: sf, mode: 2}
	}
	return displayDistanceKey{}
}

func displayDistanceCanUse(owner uintptr) bool {
	key := displayDistanceCurrentKey(owner)
	return key.file != nil && key.file.Info.TraceCount > 1
}

func displayDistanceStateFor(owner uintptr, key displayDistanceKey) *displayDistanceState {
	if key.file == nil || owner == 0 {
		return nil
	}
	s := displayDistances[owner]
	if s == nil || s.path != key.file.Info.Path {
		displayCloseDistance(owner)
		s = &displayDistanceState{path: key.file.Info.Path}
		displayDistances[owner] = s
	}
	return s
}

func displayDistanceSettingsFor(owner uintptr) displayDistanceSettings {
	key := displayDistanceCurrentKey(owner)
	s := displayDistanceStateFor(owner, key)
	if s == nil {
		if previous := displayDistances[owner]; previous != nil && previous.cancel != nil {
			previous.cancel()
			displayDistanceVersion++
			previous.version, previous.requested, previous.cancel = displayDistanceVersion, false, nil
		}
		return displayDistanceSettings{SpacingM: displaycore.DefaultDistanceSpacingM}
	}
	set := s.manual[key.mode]
	set.Enabled = s.enabled
	if set.SpacingM <= 0 {
		set.SpacingM = displaycore.DefaultDistanceSpacingM
	}
	return set
}

func displayValidateDistanceSettings(owner uintptr, settings displayDistanceSettings) error {
	key := displayDistanceCurrentKey(owner)
	s := displayDistanceStateFor(owner, key)
	if s == nil {
		return fmt.Errorf("当前剖面没有可用的空间横轴")
	}
	if settings.Manual && (settings.SpacingM <= 0 || math.IsNaN(settings.SpacingM) || math.IsInf(settings.SpacingM, 0)) {
		return fmt.Errorf("手动道间距必须为有限正数（m）")
	}
	if settings.Enabled && displayReferenceFor(owner, key, settings) == nil {
		return fmt.Errorf("当前剖面没有足够的中心坐标，或手动间距超出可显示范围")
	}
	return nil
}

func displaySetDistanceSettings(owner uintptr, settings displayDistanceSettings) error {
	if err := displayValidateDistanceSettings(owner, settings); err != nil {
		return err
	}
	key := displayDistanceCurrentKey(owner)
	s := displayDistanceStateFor(owner, key)
	s.enabled = settings.Enabled
	s.manual[key.mode] = settings
	return nil
}

func displayDistanceSource(owner uintptr) string {
	return displayDistanceSourceWithSettings(owner, displayDistanceSettingsFor(owner))
}

func displayDistanceSourceWithSettings(owner uintptr, settings displayDistanceSettings) string {
	if settings.Manual {
		return "手动规则间距"
	}
	key := displayDistanceCurrentKey(owner)
	s := displayDistanceStateFor(owner, key)
	if s != nil {
		displayReferenceFor(owner, key, settings)
		if s.actual != nil {
			return s.actualSource
		}
		if s.cancel != nil {
			return "默认 20 m（正在读取几何）"
		}
	}
	return "默认 20 m（规则间距假设）"
}

func displayDistanceFallback(first, last, grid, spacing float64, ids []float64) *displaycore.DistanceReference {
	if grid > 0 {
		return displaycore.RegularDistance(first, last, grid, spacing)
	}
	return displaycore.RankDistance(ids, spacing)
}

func displayDistanceSameKeyLine(a, b displayDistanceKey) bool {
	if a.file == nil || b.file == nil || a.mode != b.mode || a.line != b.line || a.file.Info != b.file.Info {
		return false
	}
	if a.geometry == nil || b.geometry == nil {
		return a.geometry == b.geometry
	}
	x, y := a.geometry, b.geometry
	return x.InlineByte == y.InlineByte && x.CrosslineByte == y.CrosslineByte && x.ModTimeUnixNano == y.ModTimeUnixNano &&
		x.SourcePath == y.SourcePath && x.FileSize == y.FileSize && x.TraceCount == y.TraceCount
}

func displayReferenceFor(owner uintptr, key displayDistanceKey, settings displayDistanceSettings) *displaycore.DistanceReference {
	s := displayDistanceStateFor(owner, key)
	if s == nil {
		return nil
	}
	if owner == compareHwnd && s.key != key && displayDistanceSameKeyLine(s.key, key) {
		same := true
		if s.source != nil {
			source, _ := key.file.SourceFileInfo()
			same = displayDistanceSameSource(s.source, source)
		}
		if same {
			// A fresh handle after B-only reload does not redefine A coordinates.
			s.key = key
		}
	}
	if s.key != key {
		if s.cancel != nil {
			s.cancel()
		}
		displayDistanceVersion++
		s.key, s.version = key, displayDistanceVersion
		s.fallback, s.actual, s.manualReference, s.cancel, s.fallbackIDs, s.source = nil, nil, nil, nil, nil, nil
		s.actualSource = ""
		s.requested = false
		s.first, s.last, s.gridStep = 1, float64(key.file.Info.TraceCount), 1
		if g := key.geometry; g != nil {
			_, coords, _, err := g.LineTraceNumbers(key.mode, key.line)
			if err != nil || len(coords) < 2 {
				return nil
			}
			ids := g.CrosslineValues
			if key.mode == 1 {
				ids = g.InlineValues
			}
			s.first, s.last = float64(coords[0]), float64(coords[len(coords)-1])
			s.gridStep = displaycore.KnownGridStep(ids)
			if s.gridStep == 0 {
				s.fallbackIDs = make([]float64, len(coords))
				for i, v := range coords {
					s.fallbackIDs[i] = float64(v)
				}
			}
		}
		s.fallback = displayDistanceFallback(s.first, s.last, s.gridStep, displaycore.DefaultDistanceSpacingM, s.fallbackIDs)
	}
	if s.fallback == nil {
		return nil
	}
	if settings.Manual {
		if settings.SpacingM == displaycore.DefaultDistanceSpacingM {
			return s.fallback
		}
		if s.manualReference == nil || s.manualSpacing != settings.SpacingM {
			s.manualSpacing = settings.SpacingM
			s.manualReference = displayDistanceFallback(s.first, s.last, s.gridStep, settings.SpacingM, s.fallbackIDs)
		}
		return s.manualReference
	}
	if !s.requested {
		s.requested = true
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		path, version := s.path, s.version
		expected, _ := key.file.SourceFileInfo()
		s.source = expected
		go func() {
			var source string
			actual := readDisplayDistanceReference(ctx, path, key.geometry, key.mode, key.line, key.file.Info.TraceCount, expected, &source)
			displayDistancePendingMu.Lock()
			if ctx.Err() == nil && displayDistancePending[owner].version < version {
				displayDistancePending[owner] = displayDistanceResult{version, actual, source}
				pPostMessageW.Call(owner, displayDistanceReadyMessage, 0, 0)
			}
			displayDistancePendingMu.Unlock()
		}()
	}
	if s.actual != nil {
		return s.actual
	}
	return s.fallback
}

// Read only the coordinate headers of the complete selected A line. An
// independent reader and generation ensure this never touches display I/O or
// installs an outdated mapping after the dataset or orientation changes.
func displayDistanceSameSource(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Size() == b.Size() && a.ModTime() == b.ModTime() && os.SameFile(a, b)
}

var displayDeclaredSpacingLine = regexp.MustCompile(`(?i)^(?:C\s*[0-9]{1,2}\s+)?(INLINE|CROSSLINE)\s+SPACING\s*[:=]\s*([+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:E[+-]?[0-9]+)?)\s+(M|FT|METER|METERS|METRE|METRES|FEET|FOOT)\s*$`)

// SEG-Y has no standard directional spacing field. Only an explicitly named,
// unit-bearing text declaration is accepted; conflicting cards are ignored.
func displayDeclaredSpacing(headers []segy.TextHeader, mode int) (float64, bool) {
	if mode < 0 || mode > 1 {
		return 0, false
	}
	direction := [2]string{"CROSSLINE", "INLINE"}[mode]
	value := 0.0
	for _, h := range headers {
		for _, line := range h.Lines {
			m := displayDeclaredSpacingLine.FindStringSubmatch(strings.TrimSpace(line))
			if len(m) == 0 || !strings.EqualFold(m[1], direction) {
				continue
			}
			v, err := strconv.ParseFloat(m[2], 64)
			if err != nil || !seismicFinite(v) || v <= 0 {
				continue
			}
			switch strings.ToUpper(m[3]) {
			case "FT", "FEET", "FOOT":
				v *= 0.3048
			}
			if v <= 0 {
				continue
			}
			if value > 0 && math.Abs(value-v) > 1e-9*math.Max(value, v) {
				return 0, false
			}
			value = v
		}
	}
	return value, value > 0
}

func readDisplayDistanceGeometry(ctx context.Context, path string, g *segy.GeometryIndex, mode int, line int32, count int64, expected os.FileInfo) *displaycore.DistanceReference {
	return readDisplayDistanceReference(ctx, path, g, mode, line, count, expected, nil)
}

func readDisplayDistanceReference(ctx context.Context, path string, g *segy.GeometryIndex, mode int, line int32, count int64, expected os.FileInfo, source *string) *displaycore.DistanceReference {
	f, err := segy.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if f.Info.TraceCount != count || ctx.Err() != nil {
		return nil
	}
	initial, err := f.SourceFileInfo()
	if err != nil || expected != nil && !displayDistanceSameSource(expected, initial) {
		return nil
	}
	unchanged := func() bool {
		final, err := f.SourceFileInfo()
		byPath, pathErr := os.Stat(path)
		return err == nil && pathErr == nil && displayDistanceSameSource(initial, final) && displayDistanceSameSource(initial, byPath) && ctx.Err() == nil
	}
	var traces []int64
	var ids []int32
	gridStep := 1.0
	if g != nil {
		traces, ids, _, err = g.LineTraceNumbers(mode, line)
		if err != nil {
			return nil
		}
		count = int64(len(traces))
		directionIDs := g.CrosslineValues
		if mode == 1 {
			directionIDs = g.InlineValues
		}
		gridStep = displaycore.KnownGridStep(directionIDs)
		if count > 1 && f.Info.ExtendedTextHeaders <= 64 {
			if headers, err := f.ReadTextHeaders(); err == nil {
				if spacing, ok := displayDeclaredSpacing(headers, mode); ok {
					var positions []float64
					if gridStep <= 0 {
						positions = make([]float64, len(ids))
						for i, id := range ids {
							positions[i] = float64(id)
						}
					}
					ref := displayDistanceFallback(float64(ids[0]), float64(ids[len(ids)-1]), gridStep, spacing, positions)
					if ref != nil && unchanged() {
						if source != nil {
							*source = "来自数据（文本卷头方向间距）"
						}
						return ref
					}
				}
			}
		}
	}
	// A raw dataset without a bounded geometry line may have millions of
	// traces. Defer to the documented spacing assumption in that case.
	if count < 2 || count > 200_000 {
		return nil
	}
	header, err := f.ReadBinaryHeader()
	metresPerUnit := 1.0
	if err != nil || header.MeasurementSystem != 1 && header.MeasurementSystem != 2 {
		return nil
	}
	if header.MeasurementSystem == 2 {
		metresPerUnit = 0.3048
	}
	native, distances := make([]float64, int(count)), make([]float64, int(count))
	var prev segy.TraceCoordinate
	for i := range native {
		if ctx.Err() != nil {
			return nil
		}
		t := int64(i)
		native[i] = float64(i + 1)
		if traces != nil {
			t, native[i] = traces[i], float64(ids[i])
		}
		p, err := f.ReadTraceCoordinate(t, segy.DefaultCoordinateSpec())
		if err != nil || !p.Valid || p.Units != 1 {
			return nil
		}
		if i > 0 {
			delta := math.Hypot(p.X-prev.X, p.Y-prev.Y) * metresPerUnit
			if delta <= 0 || math.IsInf(delta, 0) || math.IsNaN(delta) {
				return nil
			}
			distances[i] = distances[i-1] + delta
		}
		prev = p
	}
	if !unchanged() {
		return nil
	}
	if source != nil {
		*source = "来自几何（CDP XY）"
	}
	if gridStep > 0 {
		end := len(native) - 1
		spacing := distances[end] / ((native[end] - native[0]) / gridStep)
		uniform := true
		for i, distance := range distances {
			expected := (native[i] - native[0]) / gridStep * spacing
			if math.Abs(distance-expected) > math.Max(1e-8, distances[end]*1e-10) {
				uniform = false
				break
			}
		}
		if uniform {
			return displaycore.RegularDistance(native[0], native[end], gridStep, spacing)
		}
	}
	return displaycore.IrregularDistance(native, distances)
}

func displayDistanceReady(owner uintptr) {
	displayDistancePendingMu.Lock()
	r, ok := displayDistancePending[owner]
	delete(displayDistancePending, owner)
	displayDistancePendingMu.Unlock()
	s := displayDistances[owner]
	if !ok || s == nil || s.version != r.version || s.key != displayDistanceCurrentKey(owner) {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.cancel, s.actual, s.actualSource = nil, r.actual, r.source
	displayCompareLayoutCache.valid = false
	if owner == compareHwnd {
		invalidateCompareBase()
	} else {
		pInvalidateRect.Call(owner, 0, 0)
	}
	displayRefreshEditorLive(owner)
}

func displayCloseDistance(owner uintptr) {
	if s := displayDistances[owner]; s != nil && s.cancel != nil {
		s.cancel()
	}
	delete(displayDistances, owner)
	displayDistancePendingMu.Lock()
	delete(displayDistancePending, owner)
	displayDistancePendingMu.Unlock()
}

func displayDistanceReference(owner uintptr) *displaycore.DistanceReference {
	settings := displayDistanceSettingsFor(owner)
	if !settings.Enabled {
		return nil
	}
	return displayReferenceFor(owner, displayDistanceCurrentKey(owner), settings)
}

func displayDistanceEffectiveSpacing(owner uintptr, settings displayDistanceSettings) (float64, bool) {
	if settings.Manual {
		return settings.SpacingM, settings.SpacingM > 0 && !math.IsNaN(settings.SpacingM) && !math.IsInf(settings.SpacingM, 0)
	}
	key := displayDistanceCurrentKey(owner)
	s := displayDistanceStateFor(owner, key)
	if s == nil {
		return 0, false
	}
	displayReferenceFor(owner, key, settings)
	if s.actual != nil {
		m := s.actual.GridSpacingM()
		return m, m > 0
	}
	return displaycore.DefaultDistanceSpacingM, true
}

func displayDistanceAxis(owner uintptr, axis displaycore.Axis) displaycore.Axis {
	return displayDistanceAxisWithSettings(owner, axis, displayDistanceSettingsFor(owner))
}

func displayDistanceAxisWithSettings(owner uintptr, axis displaycore.Axis, settings displayDistanceSettings) displaycore.Axis {
	if !settings.Enabled || axis.BaseUnit == "m" || axis.StyleOnly && !axis.AllowsPoint {
		return axis
	}
	switch axis.ID {
	case "trace", "inline", "crossline":
	default:
		return axis
	}
	d := displayReferenceFor(owner, displayDistanceCurrentKey(owner), settings)
	data, dataOK := displayDistanceRange(d, axis.Data)
	view, viewOK := displayDistanceRange(d, axis.View)
	if !dataOK || !viewOK {
		return axis
	}
	axis.Title, axis.BaseUnit, axis.Unit = "距离", "m", displaycore.DistanceUnit(view)
	axis.Data, axis.View = data, view
	return axis
}

func displayDistanceToNative(owner uintptr, axis displaycore.Axis, value float64) (float64, bool) {
	return displayDistanceToNativeWithSettings(owner, axis, value, displayDistanceSettingsFor(owner))
}

func displayDistanceToNativeWithSettings(owner uintptr, axis displaycore.Axis, value float64, settings displayDistanceSettings) (float64, bool) {
	if !displayAxisIsDistance(axis) {
		return value, !math.IsNaN(value) && !math.IsInf(value, 0)
	}
	return displayReferenceFor(owner, displayDistanceCurrentKey(owner), settings).Native(value)
}

func displayAxisIsDistance(axis displaycore.Axis) bool {
	return axis.BaseUnit == "m" && (axis.ID == "trace" || axis.ID == "inline" || axis.ID == "crossline")
}

func displayDistanceRange(d *displaycore.DistanceReference, native displaycore.Range) (displaycore.Range, bool) {
	a, oka := d.Metres(native.Min)
	b, okb := d.Metres(native.Max)
	r := displaycore.Range{Min: a, Max: b}
	return r, oka && okb && (r.Valid() || r.IsPoint())
}

func displayCompareDistanceNativeBounds(panel int) displaycore.Range {
	a, b := viewFilterPanelRange(panel)
	if p := seismicComparePanel(panel); p != nil && len(p.coords) > 0 {
		a, b = float64(p.coords[0]), float64(p.coords[len(p.coords)-1])
	}
	if compareTD == nil {
		if p := seismicComparePanel(panel); p != nil {
			first, step := p.stats.TraceStart, maxInt64(1, p.stats.TraceStep)
			last := first + (p.stats.TraceEnd-first)/step*step
			a, b = float64(first+1), float64(last+1)
		}
	}
	return displaycore.Range{Min: a, Max: b}
}

// A's full visible interval is the common coordinate frame. External B files
// can extend beyond it, but their extra data must not stretch the overlap.
func displayCompareVisibleDistanceBounds(panel int) (displaycore.Range, bool) {
	b := displayCompareDistanceNativeBounds(panel)
	if panel > 0 {
		a := displayCompareDistanceNativeBounds(0)
		b.Min, b.Max = math.Max(a.Min, b.Min), math.Min(a.Max, b.Max)
	}
	return b, b.Min <= b.Max
}

func displayCompareNativeAtFraction(panel int, f float64) (float64, bool) {
	d := displayDistanceReference(compareHwnd)
	native, present := displayCompareVisibleDistanceBounds(panel)
	if !present {
		return 0, false
	}
	if d != nil && native.Min == native.Max {
		_, valid := d.Metres(native.Min)
		return native.Min, valid
	}
	m, ok := displayDistanceRange(d, native)
	if !ok {
		return 0, false
	}
	return d.Native(m.At(f))
}

func displayCompareDistanceFraction(panel int, native float64) (float64, bool) {
	d := displayDistanceReference(compareHwnd)
	bounds, present := displayCompareVisibleDistanceBounds(panel)
	m, ok := displayDistanceRange(d, bounds)
	pos, ok2 := d.Metres(native)
	if !present || !ok || !ok2 {
		return 0, false
	}
	if m.IsPoint() {
		return 0.5, pos == m.Min
	}
	return m.Fraction(pos), true
}

func displayCompareDistancePan(x0, x1, lower, upper float64, dx, width int) (float64, float64, bool) {
	d := displayDistanceReference(compareHwnd)
	current, viewOK := displayDistanceRange(d, displaycore.Range{Min: x0, Max: x1})
	data, boundsOK := displayDistanceRange(d, displaycore.Range{Min: lower, Max: upper})
	if !viewOK || !boundsOK || width < 2 {
		return 0, 0, false
	}
	delta := -float64(dx) * (current.Max - current.Min) / float64(width-1)
	next := displaycore.DragRange(data, current, displaycore.DragWhole, delta)
	a, loOK := d.Native(next.Min)
	b, hiOK := d.Native(next.Max)
	return a, b, loOK && hiOK
}

func displayDistanceStatus(owner uintptr, native float64) string {
	if pos, ok := displayDistanceReference(owner).Metres(native); ok {
		var bounds displaycore.Range
		if owner == compareHwnd {
			bounds = displayCompareDistanceNativeBounds(0)
		} else {
			bounds = displayMainDistanceNativeBounds()
		}
		r, _ := displayDistanceRange(displayDistanceReference(owner), bounds)
		if displaycore.DistanceUnit(r) == "km" {
			return fmt.Sprintf(" | 距离 %.6g km", pos/1000)
		}
		return fmt.Sprintf(" | 距离 %.6g m", pos)
	}
	return ""
}

func displayMainDistanceNativeBounds() displaycore.Range {
	first, step := lastStats.TraceStart, maxInt64(1, lastStats.TraceStep)
	last := first + (lastStats.TraceEnd-first)/step*step
	return displaycore.Range{Min: float64(first + 1), Max: float64(last + 1)}
}

func displayMainNativeAtFraction(f float64) (float64, bool) {
	d := displayDistanceReference(hwnd)
	m, ok := displayDistanceRange(d, displayMainDistanceNativeBounds())
	if !ok {
		return 0, false
	}
	return d.Native(m.At(f))
}

// Reproject the existing desktop raster only. Neither rendered amplitudes
// nor input/export traces are changed. Negative columns are unobserved grid
// positions and must not be filled by a neighbouring trace.
func displayDistanceWarp(pixels []byte, width, height int, sourceFraction func(float64) (float64, bool)) []byte {
	if width < 2 || height < 1 || len(pixels)/height/4 < width {
		return pixels
	}
	cols := make([]int, width)
	changed := false
	for x := range cols {
		src, ok := sourceFraction(float64(x) / float64(width-1))
		if !ok || math.IsNaN(src) || math.IsInf(src, 0) || src < 0 || src > 1 {
			cols[x] = -1
		} else {
			cols[x] = int(math.Round(src * float64(width-1)))
		}
		changed = changed || cols[x] != x
	}
	if !changed {
		return pixels
	}
	out := make([]byte, width*height*4)
	for y := 0; y < height; y++ {
		row := y * width * 4
		for x, sx := range cols {
			dst := row + x*4
			if sx < 0 {
				out[dst], out[dst+1], out[dst+2], out[dst+3] = 255, 255, 255, 255
				continue
			}
			src := row + sx*4
			copy(out[dst:dst+4], pixels[src:src+4])
		}
	}
	return out
}

func displayPaintMainDistance(dc uintptr, r RECT, pixels []byte, w, h int, rx, ry bool) bool {
	d := displayDistanceReference(hwnd)
	native := displayMainDistanceNativeBounds()
	distance, ok := displayDistanceRange(d, native)
	if !ok || distance.IsPoint() {
		return false
	}
	pixels = displayDistanceWarp(pixels, w, h, func(f float64) (float64, bool) {
		n, ok := d.Native(distance.At(f))
		return native.Fraction(n), ok
	})
	seismicPlot(dc, r, pixels, w, h, rx, ry)
	return true
}

func displayCompareGridGap(coords []int32, n, step, rank float64) (float64, bool) {
	if step <= 0 || len(coords) < 2 {
		return rank, true
	}
	scaled := rank * float64(len(coords)-1)
	i := min(max(int(math.Floor(scaled)), 0), len(coords)-2)
	a, b := float64(coords[i]), float64(coords[i+1])
	if b-a <= step {
		return rank, true
	}
	if n-a <= step/2 {
		return float64(i) / float64(len(coords)-1), true
	}
	if b-n <= step/2 {
		return float64(i+1) / float64(len(coords)-1), true
	}
	return 0, false
}

func displayCompareDistancePixelAllowed(panel int, native float64) bool {
	d := displayDistanceReference(compareHwnd)
	s := displayDistances[compareHwnd]
	p := seismicComparePanel(panel)
	if d == nil || s == nil || p == nil || len(p.coords) < 2 {
		return true
	}
	fraction, ok := seismicAcrossFraction(p.coords, native)
	if !ok {
		return false
	}
	_, allowed := displayCompareGridGap(p.coords, native, s.gridStep, fraction)
	return allowed
}

func displayPaintCompareDistance(dc uintptr, r RECT, p comparePanel, panel int, rx, ry bool) bool {
	d := displayDistanceReference(compareHwnd)
	if d == nil {
		return false
	}
	native, present := displayCompareVisibleDistanceBounds(panel)
	if !present {
		return true
	}
	m, ok := displayDistanceRange(d, native)
	if !ok {
		return native.Min != native.Max
	}
	original := displayCompareDistanceNativeBounds(panel)
	if original.IsPoint() {
		return false // already a one-trace raster; no horizontal warp is needed
	}
	pixels := displayDistanceWarp(p.bgra, p.w, p.h, func(f float64) (float64, bool) {
		n, ok := d.Native(m.At(f))
		if !ok || len(p.coords) < 2 {
			return original.Fraction(n), ok
		}
		rank, ok := seismicAcrossFraction(p.coords, n)
		if !ok {
			return 0, false
		}
		return displayCompareGridGap(p.coords, n, displayDistances[compareHwnd].gridStep, rank)
	})
	seismicPlot(dc, r, pixels, p.w, p.h, rx, ry)
	return true
}
