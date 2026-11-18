//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	displaycore "github.com/DavidLiu-code/SeisForge-Studio/internal/display"
)

type displayCompareBinding struct {
	Keys                     [3]string
	CanLink, Linked, FollowA bool
	PoststackProfiles        bool
}

type displaySurface struct {
	Title, Identity, StatsVersion, ColorDomain string
	Axes                                       []displaycore.Axis
	Color                                      *displaycore.ColorMapping
	AxisTicks                                  map[string]displaycore.TickPlan
	ColorTicks                                 displaycore.TickPlan
	Regions                                    map[string]RECT
	ColorRegion, Plot                          RECT
	LiveViewport                               bool
	equalTicks, distance                       bool
	applyPanelStyle                            func(displaycore.Settings, bool, int) error
	Compare                                    *displayCompareBinding
	ReadStats                                  func(context.Context) (displaycore.Stats, error)
	Apply                                      func(displaycore.Settings) error
}

type displaySurfaceState struct {
	s                displaySurface
	saved            *displaycore.Settings
	inheritedAxes    []displaycore.Axis // initial poststack B/Delta annotation only; not a color override
	compareAIdentity string             // preserve active panel presentation only within the same input A
	legendShown      bool               // local to this surface identity; never loaded from preferences
	bAxesFollow      bool               // B or difference coordinate inheritance; independent of color linking
	stats            displaycore.Stats
	statsVersion     string
	statsError       string
	mappingError     string // valid statistics can still fail to install a live automatic scale
	pending          bool
	cancel           context.CancelFunc
	seq              uint64
	order            uint64
}

type displayOwnerState struct {
	surfaces map[string]*displaySurfaceState
	links    map[[3]string]displayCompareBinding
	mapping  map[[2]string]displayMappingCache
	editor   *displayEditor
}

type displayMappingStamp struct {
	state   *displaySurfaceState
	version string
	seq     uint64
}

type displayMappingCache struct {
	stamps       [2]displayMappingStamp
	distribution displaycore.RangeDistribution
}

type displayStatsResult struct {
	owner   uintptr
	key     string
	state   *displaySurfaceState
	seq     uint64
	version string
	stats   displaycore.Stats
	err     error
}

var displayOwners = make(map[uintptr]*displayOwnerState)
var displayPaintOrder uint64
var displayAsyncWindow uintptr
var displayStatsQueue struct {
	sync.Mutex
	results []displayStatsResult
}
var displayPrefs struct {
	Version    int
	Views      map[string]displaycore.Settings
	TraceStyle *traceScientificStylePreference `json:",omitempty"`
}
var displayPrefsLoaded bool
var displayCreatePopup = user32.NewProc("CreatePopupMenu")
var displayAppendMenu = user32.NewProc("AppendMenuW")
var displayTrackPopup = user32.NewProc("TrackPopupMenu")
var displayDestroyMenu = user32.NewProc("DestroyMenu")
var displayClientToScreen = user32.NewProc("ClientToScreen")
var displayMoveFile = kernel32.NewProc("MoveFileExW")

const displayRefreshEditorLiveMessage = 0x8073
const displayRepaintCompareLayoutMessage = 0x8074

// Defer updating the dialog: a Compare paint publishes all panels in a batch,
// and changing child controls in the middle of painting is re-entrant.
func displayRefreshEditorLive(owner uintptr) {
	if o := displayOwners[owner]; o != nil {
		if w := o.editor; w != nil && !w.invalid && !w.viewportRefreshPending {
			w.viewportRefreshPending = true
			if ok, _, _ := pPostMessageW.Call(w.handle, displayRefreshEditorLiveMessage, 0, 0); ok == 0 {
				w.viewportRefreshPending = false
			}
		}
	}
}

func displayPrefsPath() string { return filepath.Join(licenseDirectory(), "display_settings_v1.json") }

func displayLoadPrefs() {
	if displayPrefsLoaded {
		return
	}
	displayPrefsLoaded = true
	if data, err := os.ReadFile(displayPrefsPath()); err == nil && len(data) < 2<<20 {
		if json.Unmarshal(data, &displayPrefs) != nil || displayPrefs.Version != 1 {
			displayPrefs.Views = nil
			displayPrefs.TraceStyle = nil
		}
	}
	if displayPrefs.Views == nil {
		displayPrefs.Views = make(map[string]displaycore.Settings)
	}
	displayPrefs.Version = 1
}
func displayWritePrefs() {
	data, err := json.MarshalIndent(displayPrefs, "", "  ")
	if err != nil {
		return
	}
	path := displayPrefsPath()
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), "display-settings-*.tmp")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil && closeErr == nil {
		displayMoveFile.Call(uintptr(unsafe.Pointer(u16(f.Name()))), uintptr(unsafe.Pointer(u16(path))), 0x9)
	}
}
func displayPersist(key string, s displaycore.Settings) {
	displayLoadPrefs()
	p := s.Clone()
	for i := range p.Axes {
		p.Axes[i].View = displaycore.Range{}
		p.Axes[i].ViewEdited = false
		p.Axes[i].Data = displaycore.Range{}
	}
	// Keep style but never apply another file's absolute amplitude boundaries.
	if p.Color != nil {
		p.Color.Range = displaycore.Range{Min: -1, Max: 1}
		displayForgetLegendVisibility(p.Color)
	}
	displayPrefs.Views[key] = p
	displayWritePrefs()
}
func displayMigrateStyle(s displaySurface, old displaycore.Settings) (displaycore.Settings, bool) {
	result := displayDefaults(s)
	modified := displaycore.ValidFontPT(old.PlotTitleFontPT) && old.PlotTitleFontPT != 0
	if modified {
		result.PlotTitleFontPT = old.PlotTitleFontPT
	}
	for i, a := range result.Axes {
		for _, p := range old.Axes {
			if a.ID != p.ID || (a.CanonicalUnit() != p.CanonicalUnit() && !(s.distance && i == 0)) {
				continue
			}
			p = displayAdaptSpatialStyle(p, a)
			p.Data, p.View, p.ViewEdited, p.StyleOnly, p.Down, p.BaseUnit, p.AllowsPoint = a.Data, a.View, false, a.StyleOnly, a.Down, a.CanonicalUnit(), a.AllowsPoint
			if p.StyleOnly {
				p.Reverse = false
			}
			if p.Valid() == nil {
				result.Axes[i] = p
				modified = true
			}
			break
		}
	}
	if result.Color != nil && old.Color != nil {
		m := old.Color.Clone()
		m.Range = result.Color.Range
		displayForgetLegendVisibility(&m)
		if m.Valid() == nil {
			result.Color = &m
			modified = true
		}
	}
	return result, modified
}

func displayDefaults(s displaySurface) displaycore.Settings {
	p := (displaycore.Settings{Axes: s.Axes, Color: s.Color}).Clone()
	displayForgetLegendVisibility(p.Color)
	return p
}

func displayForgetLegendVisibility(m *displaycore.ColorMapping) {
	if m != nil {
		// VisibilitySet remains readable in existing preferences, but a legend
		// shown on one file must no longer make a newly opened file show it.
		m.Hidden, m.VisibilitySet = true, false
	}
}

// A change of spatial representation is not a new axis. Carry its annotation
// style across, but obtain its units, title and bounds from the actual transform.
func displayAdaptSpatialStyle(saved, live displaycore.Axis) displaycore.Axis {
	if saved.CanonicalUnit() != live.CanonicalUnit() {
		saved.Title, saved.Unit, saved.BaseUnit = live.Title, live.Unit, live.BaseUnit
		saved.Step = 0
	}
	if live.BaseUnit == "m" {
		saved.Title, saved.Unit = live.Title, live.Unit
	}
	return saved
}

func displayRegister(owner uintptr, key string, s displaySurface) {
	if owner == 0 || key == "" {
		return
	}
	o := displayOwners[owner]
	if o == nil {
		o = &displayOwnerState{surfaces: make(map[string]*displaySurfaceState), links: make(map[[3]string]displayCompareBinding)}
		displayOwners[owner] = o
	}
	e := o.surfaces[key]
	fresh := e == nil || e.s.Identity != s.Identity
	if !fresh && s.equalTicks && s.Compare != nil && s.Compare.PoststackProfiles &&
		(key == s.Compare.Keys[1] || key == s.Compare.Keys[2]) {
		if a := o.surfaces[s.Compare.Keys[0]]; a != nil {
			fresh = e.compareAIdentity != a.s.Identity
		}
	}
	if fresh || e.s.StatsVersion != s.StatsVersion {
		for scope := range o.mapping {
			if scope[0] == key || scope[1] == key {
				delete(o.mapping, scope)
			}
		}
	}
	if !fresh && e.s.StatsVersion != s.StatsVersion {
		e.mappingError = ""
	}
	if fresh {
		old := e
		if e != nil && e.cancel != nil {
			e.cancel()
		}
		e = &displaySurfaceState{}
		o.surfaces[key] = e
		displayLoadPrefs()
		if b := s.Compare; s.equalTicks && b != nil && b.PoststackProfiles && (key == b.Keys[1] || key == b.Keys[2]) {
			e.bAxesFollow = true
			if a := o.surfaces[b.Keys[0]]; a != nil {
				e.compareAIdentity = a.s.Identity
				if old != nil && old.compareAIdentity == e.compareAIdentity {
					e.legendShown, e.bAxesFollow, e.inheritedAxes = old.legendShown, old.bAxesFollow, old.inheritedAxes
					if old.saved != nil {
						value, ok := displayMigrateStyle(s, *old.saved)
						if ok {
							if value.Color != nil && old.saved.Color != nil && old.s.ColorDomain == s.ColorDomain && old.saved.Color.EffectiveRangePolicy() == displaycore.ManualRange {
								value.Color.Range = old.saved.Color.Range
							}
							e.saved = &value
						}
					}
				} else if e.compareAIdentity != "" {
					e.inheritedAxes = displayIndependentAxes(owner, b.Keys[0])
				}
			}
		}
		if p, ok := displayPrefs.Views[key]; ok && e.saved == nil {
			if value, valid := displayMigrateStyle(s, p); valid {
				e.saved = &value
			}
		}
		if e.saved != nil || len(e.inheritedAxes) > 0 {
			// Registration follows measurement. Redraw once with the appropriate
			// annotation style and font margins, without triggering any processing.
			if owner == compareHwnd {
				pPostMessageW.Call(owner, displayRepaintCompareLayoutMessage, 0, 0)
			} else {
				pInvalidateRect.Call(owner, 0, 0)
			}
		}
	} else if e.s.ColorDomain != s.ColorDomain && s.Color != nil && e.saved != nil && e.saved.Color != nil {
		e.saved.Color.Range = s.Color.Range
		if w := o.editor; w != nil && w.key == key && w.pageColor {
			w.invalid = true
			setText(w.status, "着色值域已更新，请重新选择色标。")
		}
	}
	if s.Compare != nil {
		b := *s.Compare
		if old, ok := o.links[b.Keys]; ok {
			b.Linked, b.FollowA = old.Linked, old.FollowA
		}
		o.links[b.Keys] = b
	}
	displayPaintOrder++
	e.order = displayPaintOrder
	e.s = s
	if o.editor != nil && o.editor.key == key && o.editor.identity != s.Identity {
		o.editor.invalid = true
		setText(o.editor.status, "数据已更新。关闭窗口或从图中重新选择后再编辑。")
	}
	if w := o.editor; w != nil && w.key == key {
		displayRefreshEditorLive(owner)
	}
	if w := o.editor; w != nil && !w.comparePickerPending &&
		(w.pageColor || s.Compare != nil && s.Compare.PoststackProfiles) &&
		(s.Compare != nil || w.compareKeys != ([3]string{})) {
		// A paint registers A/B/Delta in order. Refresh only after the whole
		// frame so an already-open dialog doesn't cache a partial panel set.
		w.comparePickerPending = true
		if posted, _, _ := pPostMessageW.Call(w.handle, displayRefreshComparePickerMessage, 0, 0); posted == 0 {
			w.comparePickerPending = false
		}
	}
	if e.statsVersion != s.StatsVersion && e.pending {
		e.cancel()
		e.pending = false
		e.seq++
	}
	if displayNeedsStats(o, key, e) {
		displayRequestStats(owner, key, e)
	}
}

func displayNeedsStats(o *displayOwnerState, key string, e *displaySurfaceState) bool {
	if e.s.ReadStats == nil {
		return false
	}
	if o.editor != nil && o.editor.key == key && !o.editor.invalid && o.editor.pageColor {
		return true
	}
	if c := e.s.Compare; c != nil {
		b := o.links[c.Keys]
		if b.CanLink && b.FollowA && key == b.Keys[2] {
			// The followed difference never drives A's color mapping.
			return false
		}
		if b.Linked && b.CanLink && (key == b.Keys[0] || key == b.Keys[1]) {
			if b.PoststackProfiles {
				a := o.surfaces[b.Keys[0]]
				return a != nil && a.saved != nil && displayMappingUsesStats(a.saved.Color)
			}
			for _, k := range b.Keys[:2] {
				if p := o.surfaces[k]; p != nil && p.saved != nil && displayMappingUsesStats(p.saved.Color) {
					return true
				}
			}
		}
	}
	return e.saved != nil && displayMappingUsesStats(e.saved.Color)
}

func displayMappingUsesStats(m *displaycore.ColorMapping) bool {
	return m != nil && m.EffectiveRangePolicy() != displaycore.ManualRange
}

func displayMappingScopes(o *displayOwnerState, key string, linked bool) [2]string {
	if o != nil {
		if e := o.surfaces[key]; e != nil {
			if b := e.s.Compare; b != nil && linked && b.CanLink && key != b.Keys[2] {
				return [2]string{b.Keys[0], b.Keys[1]}
			}
		}
	}
	return [2]string{key}
}

// Share the exact same, immutable statistical distribution between the
// editor, automatic mapping and linked A/B. A pending member never silently
// degrades a joint range into a one-sided range.
func displayMappingDistribution(owner uintptr, key string, linked bool) (displaycore.RangeDistribution, bool, error) {
	o := displayOwners[owner]
	if o == nil || o.surfaces[key] == nil {
		return displaycore.RangeDistribution{}, false, fmt.Errorf("当前显示对象已关闭")
	}
	keys := displayMappingScopes(o, key, linked)
	var stamps [2]displayMappingStamp
	var parts []displaycore.Distribution
	for i, k := range keys {
		if k == "" {
			break
		}
		p := o.surfaces[k]
		if p == nil || p.order == 0 {
			return displaycore.RangeDistribution{}, false, fmt.Errorf("共享范围的显示对象尚不可用")
		}
		if p.pending || p.statsVersion != p.s.StatsVersion || p.statsVersion == "" && p.seq == 0 && p.stats.Mapping().Count == 0 {
			if p.s.ReadStats == nil {
				return displaycore.RangeDistribution{}, false, fmt.Errorf("当前对象没有可用的统计区域")
			}
			return displaycore.RangeDistribution{}, false, nil
		}
		if p.statsError != "" {
			return displaycore.RangeDistribution{}, false, fmt.Errorf("范围统计失败：%s", p.statsError)
		}
		stamps[i] = displayMappingStamp{state: p, version: p.statsVersion, seq: p.seq}
		parts = append(parts, p.stats.Mapping())
	}
	if cached, ok := o.mapping[keys]; ok && cached.stamps == stamps {
		return cached.distribution, true, nil
	}
	d := displaycore.BuildRangeDistribution(parts...)
	if o.mapping == nil {
		o.mapping = make(map[[2]string]displayMappingCache)
	}
	o.mapping[keys] = displayMappingCache{stamps: stamps, distribution: d}
	return d, true, nil
}

func displayRequestMappingStats(owner uintptr, key string, linked bool) {
	o := displayOwners[owner]
	if o == nil {
		return
	}
	for _, k := range displayMappingScopes(o, key, linked) {
		if k != "" {
			if e := o.surfaces[k]; e != nil && e.order != 0 {
				displayRequestStats(owner, k, e)
			}
		}
	}
}

// Manual ranges can be applied before (or without) coverage statistics.
// Automatic ranges require current data and must never commit an old range
// while the matching worker is still running.
func displayResolveMappingRange(owner uintptr, key string, m displaycore.ColorMapping, linked bool) (displaycore.RangeResolution, bool, error) {
	d, ready, err := displayMappingDistribution(owner, key, linked)
	if m.EffectiveRangePolicy() == displaycore.ManualRange {
		if !ready {
			d = displaycore.RangeDistribution{}
		}
		r, resolveErr := m.ResolveMappingRange(d)
		return r, true, resolveErr
	}
	if err != nil || !ready {
		return displaycore.RangeResolution{}, false, err
	}
	r, err := m.ResolveMappingRange(d)
	return r, true, err
}

func displayRequestStats(owner uintptr, key string, e *displaySurfaceState) {
	if e.s.ReadStats == nil || e.pending || e.statsVersion == e.s.StatsVersion && e.seq != 0 {
		return
	}
	displayEnsureWindows()
	if displayAsyncWindow == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.seq++
	e.cancel = cancel
	e.pending = true
	e.statsVersion = e.s.StatsVersion
	e.statsError = ""
	r := displayStatsResult{owner: owner, key: key, state: e, seq: e.seq, version: e.s.StatsVersion}
	read, notify := e.s.ReadStats, displayAsyncWindow
	go func() {
		t := time.NewTimer(90 * time.Millisecond)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.stats, r.err = read(ctx)
		if ctx.Err() != nil {
			return
		}
		displayStatsQueue.Lock()
		displayStatsQueue.results = append(displayStatsQueue.results, r)
		displayStatsQueue.Unlock()
		pPostMessageW.Call(notify, displayStatsMessage, 0, 0)
	}()
}
func displayReceiveStats() {
	displayStatsQueue.Lock()
	list := displayStatsQueue.results
	displayStatsQueue.results = nil
	displayStatsQueue.Unlock()
	for _, r := range list {
		o := displayOwners[r.owner]
		if o == nil {
			continue
		}
		e := o.surfaces[r.key]
		if e == nil || e != r.state || e.seq != r.seq || e.s.StatsVersion != r.version {
			continue
		}
		e.pending = false
		if e.cancel != nil {
			e.cancel()
			e.cancel = nil
		}
		e.stats = r.stats
		if r.err != nil {
			e.statsError = r.err.Error()
		} else {
			// Refresh the applied scale and raster before telling the editor
			// what is on screen. The draft may still use a different policy.
			displayRefreshAuto(r.owner, o, r.key)
		}
		if w := o.editor; w != nil && !w.invalid && w.pageColor {
			related := w.key == r.key
			if e := o.surfaces[w.key]; e != nil && e.s.Compare != nil {
				c := e.s.Compare
				if c.CanLink {
					link := displayEditorCompareLinked(w)
					follow := displayEditorColorFollowsA(w, e)
					joint := link && (w.key != c.Keys[2] || follow)
					related = related || (joint && (r.key == c.Keys[0] || r.key == c.Keys[1])) || (follow && r.key == c.Keys[0])
				}
			}
			if related {
				displayEditorSummary(w)
				displayLayoutEditor(w)
				displayEditorRefreshRangeStatus(w)
				displayEditorRefreshFooter(w)
			}
		}
	}
}
func displayRefreshAuto(owner uintptr, o *displayOwnerState, key string) error {
	e := o.surfaces[key]
	if e == nil {
		return nil
	}
	keys := []string{key}
	linked, follow := displayCompareOptions(owner, key)
	if b := e.s.Compare; b != nil && (linked || follow) {
		keys = b.Keys[:]
	}
	type pendingScale struct {
		key    string
		state  *displaySurfaceState
		before *displaycore.Settings
		next   displaycore.Settings
	}
	pending := make([]pendingScale, 0, len(keys))
	adapter, adapterKey := e, key
	for _, k := range keys {
		p := o.surfaces[k]
		if p == nil || p.saved == nil || !displayMappingUsesStats(p.saved.Color) {
			continue
		}
		if b := p.s.Compare; b != nil && b.PoststackProfiles &&
			(linked && k == b.Keys[1] || follow && k == b.Keys[2]) {
			// Only A computes a linked mapping. Neither B nor a following
			// difference may replace it with its private percentile.
			continue
		}
		m := p.saved.Color
		resolved, ready, err := displayResolveMappingRange(owner, k, *m, linked)
		if ready && err == nil && resolved.Range.Valid() && m.Range != resolved.Range {
			next := p.saved.Clone()
			next.Color.Range = resolved.Range
			pending = append(pending, pendingScale{key: k, state: p, before: p.saved, next: next})
			if adapter.s.Apply == nil && p.s.Apply != nil {
				adapter, adapterKey = p, k
			}
		}
	}
	if len(pending) == 0 {
		return nil
	}
	for i := range pending {
		p := &pending[i]
		p.state.saved = &p.next
	}
	// Only a repaint/recolor. Do not replay an earlier viewport edit. A
	// failing adapter must not leave effective legend or summary ahead of pixels.
	if adapter.s.Apply != nil {
		if err := adapter.s.Apply(displayCommittedForApply(owner, adapterKey)); err != nil {
			for _, p := range pending {
				if o.surfaces[p.key] == p.state {
					p.state.saved = p.before
					p.state.mappingError = err.Error()
				}
			}
			return err
		}
	}
	for _, p := range pending {
		p.state.mappingError = ""
	}
	pInvalidateRect.Call(owner, 0, 0)
	return nil
}
func displayCommittedForApply(owner uintptr, key string) displaycore.Settings {
	s, _ := displayCommitted(owner, key)
	for i := range s.Axes {
		s.Axes[i].ViewEdited = false
	}
	return s
}
func displayRangeFor(owner uintptr, key string, linked bool) displaycore.Range {
	o := displayOwners[owner]
	if o == nil {
		return displaycore.Range{}
	}
	e := o.surfaces[key]
	if e == nil {
		return displaycore.Range{}
	}
	keys := displayMappingScopes(o, key, linked)
	var r displaycore.Range
	seen := false
	for _, k := range keys {
		if k == "" {
			continue
		}
		p := o.surfaces[k]
		if p == nil || p.order == 0 {
			return displaycore.Range{}
		}
		if p.pending || p.statsVersion != p.s.StatsVersion || p.statsError != "" {
			return displaycore.Range{}
		}
		d := p.stats.Mapping()
		if d.Count == 0 {
			continue
		}
		if !seen {
			r = d.Range
			seen = true
		} else {
			r.Min = math.Min(r.Min, d.Range.Min)
			r.Max = math.Max(r.Max, d.Range.Max)
		}
	}
	if !seen {
		return displaycore.Range{}
	}
	return r.Expanded()
}

func displayCompareOptions(owner uintptr, key string) (bool, bool) {
	o := displayOwners[owner]
	if o == nil {
		return false, false
	}
	e := o.surfaces[key]
	if e == nil || e.s.Compare == nil {
		return false, false
	}
	b := o.links[e.s.Compare.Keys]
	return b.Linked && b.CanLink, b.FollowA && b.CanLink
}

func displayBAxisLink(owner uintptr, key string) (available, checked bool) {
	o := displayOwners[owner]
	if o == nil {
		return false, false
	}
	e := o.surfaces[key]
	if e == nil || e.s.Compare == nil || !e.s.Compare.PoststackProfiles ||
		(e.s.Compare.Keys[1] != key && e.s.Compare.Keys[2] != key) || o.surfaces[e.s.Compare.Keys[0]] == nil {
		return false, false
	}
	return true, e.bAxesFollow
}

// displayIndependentAxes returns the local style, with the latest physical
// range from the surface. It remains available while B follows A.
func displayIndependentAxes(owner uintptr, key string) []displaycore.Axis {
	o := displayOwners[owner]
	if o == nil || o.surfaces[key] == nil {
		return nil
	}
	e := o.surfaces[key]
	axes := append([]displaycore.Axis(nil), e.s.Axes...)
	styles := e.inheritedAxes
	if e.saved != nil {
		styles = e.saved.Axes
	}
	if len(styles) > 0 {
		for i, live := range axes {
			for _, a := range styles {
				if a.ID != live.ID || (a.CanonicalUnit() != live.CanonicalUnit() && !(e.s.distance && i == 0)) {
					continue
				}
				a = displayAdaptSpatialStyle(a, live)
				if e.s.LiveViewport || !a.ViewEdited {
					a.View, a.ViewEdited = live.View, false
				}
				a.Data, a.Down, a.StyleOnly, a.AllowsPoint = live.Data, live.Down, live.StyleOnly, live.AllowsPoint
				axes[i] = a
				break
			}
		}
	}
	return axes
}

func displayFollowAxisStyle(dst, src []displaycore.Axis) []displaycore.Axis {
	for i, local := range dst {
		for _, a := range src {
			if a.ID != local.ID || a.CanonicalUnit() != local.CanonicalUnit() {
				continue
			}
			// Units, labels, tick/grid formatting and direction are display
			// choices. B's native bounds and physical geometry remain B's.
			a.BaseUnit = local.CanonicalUnit()
			a.Data, a.View, a.Down, a.StyleOnly, a.AllowsPoint = local.Data, local.View, local.Down, local.StyleOnly, local.AllowsPoint
			a.ViewEdited = local.ViewEdited
			if a.StyleOnly {
				a.Reverse = false
			}
			dst[i] = a
			break
		}
	}
	return dst
}

func displayIndependentColor(owner uintptr, key string) *displaycore.ColorMapping {
	o := displayOwners[owner]
	if o == nil || o.surfaces[key] == nil {
		return nil
	}
	e := o.surfaces[key]
	c := e.s.Color
	if e.saved != nil && e.saved.Color != nil {
		c = e.saved.Color
	}
	if c == nil {
		return nil
	}
	m := c.Clone()
	m.Hidden, m.VisibilitySet = !e.legendShown, e.legendShown
	return &m
}

// Display pickers are also reachable when the legend itself is hidden. Reuse
// the current color target on a repeated toolbar click, or prefer A.
func displayOpenToolbarColor(owner uintptr, preferred ...string) bool {
	o := displayOwners[owner]
	if o == nil {
		return false
	}
	if w := o.editor; w != nil && !w.invalid {
		preferred = append([]string{w.key}, preferred...)
	}
	for _, key := range preferred {
		if e := o.surfaces[key]; e != nil && e.order != 0 && e.s.Color != nil {
			displayOpenEditor(owner, key, "", true)
			return true
		}
	}
	return false
}

func displayColorbarVisible(owner uintptr, key string) bool {
	if o := displayOwners[owner]; o != nil {
		if e := o.surfaces[key]; e != nil && e.s.Color != nil {
			return e.legendShown
		}
	}
	return false
}

func displayColorbarReserved(owner uintptr, key string) bool {
	return displayColorbarVisible(owner, key)
}

// Visibility alone is not a custom color mapping: an implicit legend must
// continue to use the range and bytes supplied by the current raster.
func displayEffectiveLegend(owner uintptr, key string, fallback displaycore.ColorMapping) displaycore.ColorMapping {
	if s, explicit := displayCommitted(owner, key); s.Color != nil {
		if explicit {
			return *s.Color
		}
		fallback.Hidden, fallback.VisibilitySet = s.Color.Hidden, s.Color.VisibilitySet
	}
	return fallback
}

func displaySetColorbarVisible(owner uintptr, key, identity string, shown bool) bool {
	o := displayOwners[owner]
	if o == nil {
		return false
	}
	e := o.surfaces[key]
	if e == nil || e.order == 0 || e.s.Color == nil || e.s.Identity != identity {
		return false
	}
	if shown == e.legendShown {
		return true
	}
	e.legendShown = shown
	if owner == compareHwnd {
		invalidateCompareBase()
	}
	pInvalidateRect.Call(owner, 0, 0)
	displayRefreshEditorLive(owner)
	return true
}

// Compare only offers panels that have a real scalar raster in the current
// paint (B awaiting data, or a disabled difference, are not edit targets).
func displayColorTargets(owner uintptr, keys [3]string) (indices []int) {
	o := displayOwners[owner]
	if o == nil {
		return nil
	}
	for i, key := range keys {
		if e := o.surfaces[key]; e != nil && e.order != 0 && e.s.Color != nil {
			indices = append(indices, i)
		}
	}
	return indices
}
func displayCommitted(owner uintptr, key string) (displaycore.Settings, bool) {
	o := displayOwners[owner]
	if o == nil {
		return displaycore.Settings{}, false
	}
	e := o.surfaces[key]
	if e == nil {
		return displaycore.Settings{}, false
	}
	s := displayDefaults(e.s)
	active := e.saved != nil
	if active {
		s = e.saved.Clone()
		if poststackSceneColor(owner, key) && s.Color != nil && e.s.Color != nil {
			*s.Color = poststackLiveColorRange(*s.Color, *e.s.Color)
		}
		if e.s.distance {
			s.Axes = displayIndependentAxes(owner, key)
		}
	}
	if !active && len(e.inheritedAxes) > 0 {
		s.Axes = displayIndependentAxes(owner, key)
	}
	if _, checked := displayBAxisLink(owner, key); checked {
		s.Axes = displayFollowAxisStyle(displayIndependentAxes(owner, key), displayIndependentAxes(owner, e.s.Compare.Keys[0]))
	}
	if s.Color != nil {
		s.Color.Hidden, s.Color.VisibilitySet = !e.legendShown, e.legendShown
	}
	if b := e.s.Compare; b != nil && b.CanLink {
		bound := o.links[b.Keys]
		if (bound.Linked && key == b.Keys[1]) || (bound.FollowA && key == b.Keys[2]) {
			if a := o.surfaces[b.Keys[0]]; a != nil {
				source := a.s.Color
				if a.saved != nil && a.saved.Color != nil {
					source, active = a.saved.Color, true
				}
				if source == nil {
					return s, active
				}
				m := source.Clone()
				if poststackSceneColor(owner, b.Keys[0]) && a.s.Color != nil {
					m = poststackLiveColorRange(m, *a.s.Color)
				}
				if s.Color != nil {
					displayCopyLegendStyle(&m, s.Color)
				}
				s.Color = &m
			}
		}
	}
	return s, active
}

func displayCopyLegendVisibility(to, from *displaycore.ColorMapping) {
	to.Hidden, to.VisibilitySet = from.Hidden, from.VisibilitySet
}

// Following only copies the numeric mapping and gradient. Legend annotation
// and tick presentation (including the unit and visibility) remain local.
func displayCopyLegendStyle(to, from *displaycore.ColorMapping) {
	displayCopyLegendVisibility(to, from)
	to.Title, to.Unit = from.Title, from.Unit
	to.Step, to.Format, to.Digits = from.Step, from.Format, from.Digits
	to.TickLength, to.TickLengthSet = from.TickLength, from.TickLengthSet
	to.Divisions, to.TickFontPT, to.TitleFontPT = from.Divisions, from.TickFontPT, from.TitleFontPT
}

// Materialize the current effective mapping upon unlinking. Keep the visible
// endpoints as a manual starting point instead of running a new one-sided
// percentile that immediately jumps away from the previously shared range.
func displayDetachedColor(effective, local *displaycore.ColorMapping) *displaycore.ColorMapping {
	if effective == nil {
		return nil
	}
	m := effective.Clone()
	if local != nil {
		displayCopyLegendStyle(&m, local)
	}
	m.SetRangePolicy(displaycore.ManualRange)
	return &m
}

func displayAxisFor(owner uintptr, key string, fallback displaycore.Axis) displaycore.Axis {
	s, ok := displayCommitted(owner, key)
	if _, follows := displayBAxisLink(owner, key); !ok && !follows && !displayHasInheritedAxes(owner, key) {
		return fallback
	}
	for _, a := range s.Axes {
		if a.ID == fallback.ID && (a.CanonicalUnit() == fallback.CanonicalUnit() ||
			(oSurfaceHasDistance(owner, key) && (key == mainDisplayKey || a.ID != "time"))) {
			a = displayAdaptSpatialStyle(a, fallback)
			if !a.ViewEdited {
				a.View = fallback.View
			}
			a.Data, a.Down, a.StyleOnly, a.AllowsPoint = fallback.Data, fallback.Down, fallback.StyleOnly, fallback.AllowsPoint
			return a
		}
	}
	return fallback
}

func displayHasInheritedAxes(owner uintptr, key string) bool {
	o := displayOwners[owner]
	return o != nil && o.surfaces[key] != nil && len(o.surfaces[key].inheritedAxes) > 0
}

func oSurfaceHasDistance(owner uintptr, key string) bool {
	o := displayOwners[owner]
	return o != nil && o.surfaces[key] != nil && o.surfaces[key].s.distance
}

func displayBeginPaint(owner uintptr) {
	if o := displayOwners[owner]; o != nil {
		for _, e := range o.surfaces {
			e.order = 0
		}
	}
}
func displayCloseOwner(owner uintptr) {
	if o := displayOwners[owner]; o != nil {
		delete(displayOwners, owner)
		for _, e := range o.surfaces {
			if e.cancel != nil {
				e.cancel()
			}
		}
		if o.editor != nil {
			pDestroyWindow.Call(o.editor.handle)
		}
	}
}
func displayWithin(r RECT, x, y int) bool {
	return r.Right > r.Left && r.Bottom > r.Top && int(r.Left) <= x && x < int(r.Right) && int(r.Top) <= y && y < int(r.Bottom)
}
func displayHit(owner uintptr, x, y int, contextMenu bool) (string, string, bool) {
	o := displayOwners[owner]
	if o == nil {
		return "", "", false
	}
	var order uint64
	key, axis := "", ""
	color := false
	for k, e := range o.surfaces {
		if e.order == 0 || e.order < order {
			continue
		}
		if displayWithin(e.s.ColorRegion, x, y) && e.s.Color != nil && displayColorbarVisible(owner, k) {
			order, key, axis, color = e.order, k, "", true
			continue
		}
		matched := false
		for _, a := range e.s.Axes {
			if displayWithin(e.s.Regions[a.ID], x, y) {
				order, key, axis, color = e.order, k, a.ID, false
				matched = true
				break
			}
		}
		if !matched && contextMenu && displayWithin(e.s.Plot, x, y) {
			order, key, axis, color = e.order, k, "", false
		}
	}
	return key, axis, color
}
func displaySurfaceClick(owner uintptr, x, y int, contextMenu bool) bool {
	key, axis, color := displayHit(owner, x, y, contextMenu)
	if key == "" {
		return false
	}
	if !contextMenu {
		displayOpenEditor(owner, key, axis, color)
		return true
	}
	o := displayOwners[owner]
	e := o.surfaces[key]
	menu, _, _ := displayCreatePopup.Call()
	if menu == 0 {
		return false
	}
	defer displayDestroyMenu.Call(menu)
	add := func(id int, label string) {
		displayAppendMenu.Call(menu, 0, uintptr(id), uintptr(unsafe.Pointer(u16(label))))
	}
	if len(e.s.Axes) > 0 {
		add(1, "编辑坐标…")
	}
	if e.s.Color != nil {
		add(2, "编辑色标…")
		s, _ := displayCommitted(owner, key)
		if s.Color != nil && s.Color.Hidden {
			add(3, "显示色标")
		} else {
			add(3, "隐藏色标")
		}
	}
	p := POINT{X: int32(x), Y: int32(y)}
	displayClientToScreen.Call(owner, uintptr(unsafe.Pointer(&p)))
	command, _, _ := displayTrackPopup.Call(menu, 0x102, uintptr(p.X), uintptr(p.Y), 0, owner, 0)
	switch command {
	case 1, 2:
		displayOpenEditor(owner, key, axis, command == 2)
	case 3:
		displaySetColorbarVisible(owner, key, e.s.Identity, !e.legendShown)
	}
	return true
}
func displayContextMenu(owner, lParam uintptr) bool {
	if uint32(lParam) == 0xffffffff {
		return false
	}
	p := POINT{X: int32(int16(lParam)), Y: int32(int16(lParam >> 16))}
	pScreenToClient.Call(owner, uintptr(unsafe.Pointer(&p)))
	return displaySurfaceClick(owner, int(p.X), int(p.Y), true)
}

func displaySubmit(owner uintptr, key, id string, s displaycore.Settings, linked, follow bool) error {
	return displaySubmitWithBAxisLink(owner, key, id, s, linked, follow, nil)
}

func displaySubmitWithDistance(owner uintptr, key, id string, s displaycore.Settings, linked, follow bool, axesFollow *bool, distance *displayDistanceSettings) error {
	if distance == nil || *distance == displayDistanceSettingsFor(owner) {
		return displaySubmitWithBAxisLink(owner, key, id, s, linked, follow, axesFollow)
	}
	if !oSurfaceHasDistance(owner, key) {
		return fmt.Errorf("当前视图不支持距离横轴")
	}
	if err := displayValidateDistanceSettings(owner, *distance); err != nil {
		return err
	}
	// A numeric edit belongs to the units visible while it was typed, not
	// to the newly selected spatial representation. Resolve that edit first.
	if err := displaySubmitWithBAxisLink(owner, key, id, s, linked, follow, axesFollow); err != nil {
		return err
	}
	if err := displaySetDistanceSettings(owner, *distance); err != nil {
		return err
	}
	displayCompareLayoutCache.valid = false
	displayInvalidateProfile(owner)
	return nil
}

func displaySubmitWithBAxisLink(owner uintptr, key, id string, s displaycore.Settings, linked, follow bool, axesFollow *bool) error {
	o := displayOwners[owner]
	if o == nil {
		return fmt.Errorf("视图已关闭")
	}
	e := o.surfaces[key]
	if e == nil || e.order == 0 || e.s.Identity != id {
		return fmt.Errorf("数据已更新，请重新选择编辑对象")
	}
	if !displaycore.ValidFontPT(s.PlotTitleFontPT) {
		return fmt.Errorf("图标题字号必须在 %d–%d pt 内", displaycore.MinFontPT, displaycore.MaxFontPT)
	}
	before, _ := displayCommitted(owner, key)
	linkedBefore, followBefore := displayCompareOptions(owner, key)
	available, axesBefore := displayBAxisLink(owner, key)
	axesAfter := axesBefore
	if axesFollow != nil {
		if !available {
			return fmt.Errorf("当前面板不能与 A 联动坐标样式")
		}
		axesAfter = *axesFollow
	}
	profileAxesChange := axesBefore != axesAfter
	colorUnchanged := before.Color == nil && s.Color == nil || before.Color != nil && s.Color != nil &&
		before.Color.Hidden == s.Color.Hidden && displayColorMappingEqualExceptVisibility(before.Color, s.Color)
	if profileAxesChange && linkedBefore == linked && followBefore == follow && colorUnchanged &&
		(slices.Equal(before.Axes, s.Axes) || slices.Equal(displayIndependentAxes(owner, key), s.Axes)) {
		// A link-only edit is a repaint, not a viewport or palette edit.
		e.bAxesFollow = axesAfter
		displayInvalidateProfile(owner)
		return nil
	}
	if !profileAxesChange && before.Color != nil && s.Color != nil && before.Color.Hidden != s.Color.Hidden && linkedBefore == linked && followBefore == follow && displayOnlyLegendVisibilityDiffers(before, s) {
		// A visibility switch does not make implicit amplitude scaling explicit
		// or touch the color/viewport adapter. This also avoids triggering I/O.
		displaySetColorbarVisible(owner, key, id, !s.Color.Hidden)
		return nil
	}
	for _, a := range s.Axes {
		if err := a.Valid(); err != nil {
			return err
		}
	}
	if s.Color != nil {
		if err := s.Color.Valid(); err != nil {
			return err
		}
	}
	old := map[string]*displaycore.Settings{key: e.saved}
	legendBefore := e.legendShown
	oldBinding := displayCompareBinding{}
	if c := e.s.Compare; c != nil {
		oldBinding = o.links[c.Keys]
		b := oldBinding
		if c.PoststackProfiles && b.CanLink {
			detach := func(peer string, was, next bool) {
				if !was || next {
					return
				}
				p := o.surfaces[peer]
				if p == nil {
					return
				}
				effective, _ := displayCommitted(owner, peer)
				if key == peer && s.Color != nil {
					if effective.Color != nil && displayColorToneEqual(s.Color, effective.Color) {
						s.Color = displayDetachedColor(effective.Color, s.Color)
					}
					// A changed color strategy/gradient can be explicitly
					// edited in the draft after unlinking.
					return
				}
				if effective.Color != nil {
					old[peer] = p.saved
					local := displayIndependentColor(owner, peer)
					effective.Color = displayDetachedColor(effective.Color, local)
					p.saved = &effective
				}
			}
			detach(c.Keys[1], b.Linked, linked)
			detach(c.Keys[2], b.FollowA, follow)
			b.Linked, b.FollowA = linked, follow
			if s.Color != nil && (key == c.Keys[1] && linked || key == c.Keys[2] && follow) {
				// Following is one-way. Only the local annotations can be
				// committed by a follower; source tone stays with A.
				if local := displayIndependentColor(owner, key); local != nil {
					displayCopyLegendStyle(local, s.Color)
					s.Color = local
				}
			}
		}
		syncMode := func(k string, m *displaycore.ColorMapping) {
			if p := o.surfaces[k]; p != nil && m != nil {
				old[k] = p.saved
				n, _ := displayCommitted(owner, k)
				value := m.Clone()
				if n.Color != nil {
					displayCopyLegendStyle(&value, n.Color)
				}
				n.Color = &value
				p.saved = &n
			}
		}
		if b.CanLink && !c.PoststackProfiles {
			// Copy the effective shared scale before dropping the link.
			if b.Linked && !linked {
				a, _ := displayCommitted(owner, c.Keys[0])
				if key != c.Keys[1] {
					syncMode(c.Keys[1], a.Color)
				}
			}
			if b.FollowA && !follow && key != c.Keys[2] {
				a, _ := displayCommitted(owner, c.Keys[0])
				syncMode(c.Keys[2], a.Color)
			}
			b.Linked, b.FollowA = linked, follow
			if linked && s.Color != nil && key != c.Keys[2] {
				peer := c.Keys[1]
				if key == peer {
					peer = c.Keys[0]
				}
				syncMode(peer, s.Color)
			}
		}
		o.links[c.Keys] = b
	}
	applied := s.Clone()
	if axesAfter || axesBefore && slices.Equal(before.Axes, s.Axes) {
		applied.Axes = displayIndependentAxes(owner, key)
	}
	if e.saved != nil && !e.s.LiveViewport {
		for i, a := range applied.Axes {
			if !a.ViewEdited {
				for _, oldAxis := range e.saved.Axes {
					if oldAxis.ID == a.ID && oldAxis.ViewEdited {
						applied.Axes[i].View = oldAxis.View
						applied.Axes[i].ViewEdited = true
						break
					}
				}
			}
		}
	}
	if e.s.LiveViewport {
		for i := range applied.Axes {
			applied.Axes[i].ViewEdited = false
		}
	}
	e.saved = &applied
	e.bAxesFollow = axesAfter
	if s.Color != nil {
		e.legendShown = !s.Color.Hidden
	}
	styleOnly := e.s.LiveViewport && displayPresentationOnly(before, s) &&
		(e.s.Compare == nil || (oldBinding.Linked == linked && oldBinding.FollowA == follow))
	if e.s.Apply != nil && !styleOnly {
		if err := e.s.Apply(s); err != nil {
			e.legendShown = legendBefore
			e.bAxesFollow = axesBefore
			for k, p := range old {
				o.surfaces[k].saved = p
			}
			if c := e.s.Compare; c != nil {
				o.links[c.Keys] = oldBinding
			}
			return err
		}
	}
	if styleOnly {
		displayInvalidateProfile(owner)
	}
	displayPersist(key, applied)
	pInvalidateRect.Call(owner, 0, 0)
	return nil
}

func displayInvalidateProfile(owner uintptr) {
	if owner == compareHwnd {
		invalidateCompareBase()
	}
	pInvalidateRect.Call(owner, 0, 0)
	displayRefreshEditorLive(owner)
}

func displayOnlyLegendVisibilityDiffers(before, after displaycore.Settings) bool {
	if before.PlotTitleFontPT != after.PlotTitleFontPT || !slices.Equal(before.Axes, after.Axes) || !displayPresentationOnly(before, after) {
		return false
	}
	x, y := before.Color, after.Color
	return displayColorMappingEqualExceptVisibility(x, y)
}

func displayColorMappingEqualExceptVisibility(x, y *displaycore.ColorMapping) bool {
	return x.Title == y.Title && x.Unit == y.Unit && x.Step == y.Step && x.Format == y.Format &&
		x.Digits == y.Digits && x.MajorTickDIP() == y.MajorTickDIP() &&
		x.Divisions == y.Divisions && x.TickFontPT == y.TickFontPT && x.TitleFontPT == y.TitleFontPT &&
		displayColorToneEqual(x, y)
}

func displayColorToneEqual(x, y *displaycore.ColorMapping) bool {
	if x.EffectiveRangePolicy() != y.EffectiveRangePolicy() || x.Range != y.Range || x.Reverse != y.Reverse ||
		x.Symmetric != y.Symmetric || !slices.Equal(x.Stops, y.Stops) {
		return false
	}
	return x.EffectiveRangePolicy() != displaycore.PercentileRange ||
		x.LowPercentile == y.LowPercentile && x.HighPercentile == y.HighPercentile
}

func displayPresentationOnly(before, after displaycore.Settings) bool {
	if len(before.Axes) != len(after.Axes) || (before.Color == nil) != (after.Color == nil) {
		return false
	}
	for i, a := range after.Axes {
		if a.ViewEdited || before.Axes[i].ID != a.ID || before.Axes[i].Reverse != a.Reverse {
			return false
		}
	}
	if x, y := before.Color, after.Color; x != nil {
		return displayColorToneEqual(x, y)
	}
	return true
}

func displayRenderColor(owner uintptr, key string, values []float64, dst []byte) bool {
	s, ok := displayCommitted(owner, key)
	return ok && displayRenderColorMapping(s.Color, values, dst)
}

func displayRenderColorMapping(m *displaycore.ColorMapping, values []float64, dst []byte) bool {
	if m == nil || !m.Range.Valid() || len(dst) != len(values)*4 || len(values) == 0 {
		return false
	}
	var lut [256]displaycore.RGB
	for i := range lut {
		lut[i] = m.ColorAtPosition(float64(i) / 255)
	}
	for i, v := range values {
		c := displaycore.RGB{R: 225, G: 225, B: 225}
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			t := m.Range.Fraction(v)
			if v <= m.Range.Min {
				t = 0
			} else if v >= m.Range.Max {
				t = 1
			}
			if !math.IsNaN(t) {
				c = lut[int(math.Round(t*255))]
			}
		}
		j := 4 * i
		dst[j], dst[j+1], dst[j+2], dst[j+3] = c.B, c.G, c.R, 0
	}
	return true
}

func displayAxisRegions(plot RECT, xID, yID string) map[string]RECT {
	return map[string]RECT{
		xID: {Left: plot.Left, Right: plot.Right, Top: plot.Bottom, Bottom: plot.Bottom + 44},
		yID: {Left: max(int32(0), plot.Left-77), Right: plot.Left, Top: max(int32(0), plot.Top-20), Bottom: plot.Bottom},
	}
}
func displayAxisTitle(a displaycore.Axis) string {
	if a.Unit == "" {
		return a.Title
	}
	if a.Title == "" {
		return a.Unit
	}
	return a.Title + " (" + a.Unit + ")"
}
func displayAxisLabel(a displaycore.Axis, v float64) string {
	return displaycore.FormatNumber(a.DisplayValue(v), a.Format, a.Digits)
}
func displayDrawAxes(dc uintptr, plot RECT, x, y displaycore.Axis) {
	m := displayMetrics(dc, displayDCDPI(dc))
	xp := displayAxisTickPlan(m, x, int(plot.Right-plot.Left), true, nil)
	yp := displayAxisTickPlan(m, y, int(plot.Bottom-plot.Top), false, nil)
	displayDrawAxesPlanned(dc, plot, x, y, m, xp, yp, nil)
	if text := displayAxisWarning(x, y, xp, yp); text != "" {
		displayDrawTickWarning(dc, plot, m, text)
	}
}

func displayDrawAxesPlanned(dc uintptr, plot RECT, x, y displaycore.Axis, m displayPlotMetrics, xp, yp displaycore.TickPlan, position func(float64) float64) {
	if plot.Right <= plot.Left || plot.Bottom <= plot.Top {
		return
	}
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(53, 66, 85))
	old, _, _ := pSelectObject.Call(dc, pen)
	defer func() {
		pSelectObject.Call(dc, old)
		if pen != 0 {
			pDeleteObject.Call(pen)
		}
	}()
	l, t, r, b := int(plot.Left), int(plot.Top), int(plot.Right), int(plot.Bottom)
	gap := max(2, displayPixelDIP(5, m.DPI))
	xPixel := func(v float64) int {
		if position == nil {
			return x.Pixel(v, l, r)
		}
		f := position(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return math.MinInt / 2
		}
		if x.Reverse {
			f = 1 - f
		}
		return l + int(math.Round(f*float64(r-l)))
	}
	xLabels := displayTickLabelVisibility(xp.Ticks, xPixel, func(s string) int { return m.Measure(s).Width }, gap)
	if !x.HideAxis {
		drawLine(dc, l, b, r, b)
		length := displayPixelDIP(x.MajorTickDIP(), m.DPI)
		for i, tick := range xp.Ticks {
			q := xPixel(tick.Value)
			if q < l || q > r {
				continue
			}
			if length > 0 {
				drawLine(dc, q, b, q, b+length)
			}
			if !x.HideLabels && xLabels[i] {
				w := m.Measure(tick.Label).Width
				drawAxisText(dc, tick.Label, q-w/2-1, b+length+gap, q+(w+1)/2+1, b+length+gap+m.Height, DT_CENTER)
			}
		}
		if !x.HideTitle {
			y0 := b + length + gap
			if !x.HideLabels {
				y0 += m.Height + gap
			}
			w := max(r-l, m.Measure(displayAxisTitle(x)).Width+2)
			mid := (l + r) / 2
			drawAxisText(dc, displayAxisTitle(x), mid-w/2, y0, mid+(w+1)/2, y0+m.Height, DT_CENTER)
		}
	}
	if !y.HideAxis {
		drawLine(dc, l, t, l, b)
		y0, y1 := b, t
		if y.Down {
			y0, y1 = t, b
		}
		length := displayPixelDIP(y.MajorTickDIP(), m.DPI)
		yLabels := displayTickLabelVisibility(yp.Ticks, func(v float64) int { return y.Pixel(v, y0, y1) }, func(string) int { return m.Height }, gap)
		for i, tick := range yp.Ticks {
			q := y.Pixel(tick.Value, y0, y1)
			if length > 0 {
				drawLine(dc, l-length, q, l, q)
			}
			if !y.HideLabels && yLabels[i] {
				w := m.Measure(tick.Label).Width
				drawAxisText(dc, tick.Label, l-length-gap-w-2, q-m.Height/2, l-length-gap, q+(m.Height+1)/2, DT_RIGHT)
			}
		}
		if !y.HideTitle {
			w := m.Measure(displayAxisTitle(y)).Width
			titleBottom := t - (m.Height+1)/2 - gap
			drawAxisText(dc, displayAxisTitle(y), l-w-gap, titleBottom-m.Height, l-gap, titleBottom, DT_RIGHT)
		}
	}
}

// Paint the same measured fonts used by the compact seismic layout; explicit
// Latin/Han runs keep Arial on English and numeric glyphs and provide readable
// Chinese even when no GDI font-link entry is available.
func displayMeasuredTextPainter(dc uintptr, font displayPlotFontMetrics, rotate bool) (func(string, int, int), func()) {
	angle := 0
	if rotate {
		angle = 900
	}
	faces := displayNewPlotFonts(font.PT, font.DPI, angle)
	widthFaces := faces
	if rotate {
		widthFaces = displayNewPlotFonts(font.PT, font.DPI, 0)
	}
	oldAlign, _, _ := displaySetTextAlign.Call(dc, 24) // TA_LEFT | TA_BASELINE
	oldBk, _, _ := pSetBkMode.Call(dc, TRANSPARENT)
	painter := func(value string, left, top int) {
		x, y := left, top+font.Ascent
		if rotate {
			x, y = left+font.Ascent, top+font.Measure(value).Width
		}
		faceAt := func(widthFace uintptr) uintptr {
			if !rotate {
				return widthFace
			}
			if widthFace == widthFaces.han && faces.han != 0 {
				return faces.han
			}
			return faces.latin
		}
		widthFaces.runs(value, func(run string, widthFace uintptr) {
			n := utf16.Encode([]rune(run))
			if len(n) == 0 {
				return
			}
			restore := displaySelectPlotFont(dc, faceAt(widthFace))
			displayTextOut.Call(dc, uintptr(x), uintptr(y), uintptr(unsafe.Pointer(&n[0])), uintptr(len(n)))
			restore()
			restore = displaySelectPlotFont(dc, widthFace)
			advance := displayPlotTextExtent(dc, n, font.Height).Width
			restore()
			if rotate {
				y -= advance
			} else {
				x += advance
			}
		})
	}
	return painter, func() {
		if oldAlign != ^uintptr(0) && oldAlign != uintptr(0xffffffff) {
			displaySetTextAlign.Call(dc, oldAlign)
		}
		if oldBk != 0 {
			pSetBkMode.Call(dc, oldBk)
		}
		faces.close()
		if rotate {
			widthFaces.close()
		}
	}
}

func displayDrawMeasuredCompareTitle(dc uintptr, layout displayPlotLayout) {
	p := layout.Plot
	f := layout.Typography.PlotTitle
	if p.Right <= p.Left || p.Bottom <= p.Top || layout.CompareTitle == "" || f.Measure == nil {
		return
	}
	paint, done := displayMeasuredTextPainter(dc, f, false)
	defer done()
	width := f.Measure(layout.CompareTitle).Width
	paint(layout.CompareTitle, (int(p.Left+p.Right)-width)/2, int(p.Top)-f.Height-displayPixelDIP(5, f.DPI))
}

func displayDrawMeasuredCompareAxes(dc uintptr, layout displayPlotLayout, x, y displaycore.Axis, position func(float64) float64) {
	p := layout.Plot
	if p.Right <= p.Left || p.Bottom <= p.Top {
		return
	}
	f := layout.Typography
	gap := max(2, displayPixelDIP(5, f.XTick.DPI))
	l, t, r, b := int(p.Left), int(p.Top), int(p.Right)-1, int(p.Bottom)-1
	if x.BaseUnit == "m" {
		position = nil
	}
	xpixel := func(value float64) int {
		if position == nil {
			return x.Pixel(value, l, r)
		}
		frac := position(value)
		if math.IsInf(frac, 0) || math.IsNaN(frac) {
			return math.MinInt / 2
		}
		if x.Reverse {
			frac = 1 - frac
		}
		return l + int(math.Round(frac*float64(r-l)))
	}
	ypixel := func(value float64) int {
		if y.Down {
			return y.Pixel(value, t, b)
		}
		return y.Pixel(value, b, t)
	}
	xp, yp := layout.AxisTicks[x.ID], layout.AxisTicks[y.ID]
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(53, 66, 85))
	if pen != 0 {
		old, _, _ := pSelectObject.Call(dc, pen)
		defer func() { pSelectObject.Call(dc, old); pDeleteObject.Call(pen) }()
	}
	if !x.HideAxis {
		drawLine(dc, l, b, r+1, b)
		length := displayPixelDIP(displayEqualAxisTickDIP(x), f.XTick.DPI)
		for _, tick := range xp.Ticks {
			q := xpixel(tick.Value)
			if q >= l && q <= r && length > 0 {
				drawLine(dc, q, b, q, b+length+1)
			}
		}
		rows := layout.HorizontalAxis
		if rows.bottom == 0 {
			rows = displayCompareHorizontalRows(displayPlotInput{X: x, Y: y, Typography: f}, xp, yp, layout.ColorTicks, r-l+1)
		}
		if !x.HideLabels {
			shown := displayEqualTickLabelVisibility(xp.Ticks, xpixel, func(s string) int { return f.XTick.Measure(s).Width }, gap)
			paint, done := displayMeasuredTextPainter(dc, f.XTick, false)
			for i, tick := range xp.Ticks {
				q := xpixel(tick.Value)
				if shown[i] && q >= l && q <= r {
					labelTop := b + rows.baseline - f.XTick.Ascent
					if i == len(xp.Ticks)-1 {
						labelTop += rows.stride
					}
					paint(tick.Label, q-f.XTick.Measure(tick.Label).Width/2, labelTop)
				}
			}
			done()
		}
		if !x.HideTitle {
			title := displayAxisTitle(x)
			paint, done := displayMeasuredTextPainter(dc, f.XTitle, false)
			paint(title, (l+r-f.XTitle.Measure(title).Width)/2, b+rows.titleTop)
			done()
		}
	}
	if !y.HideAxis {
		drawLine(dc, l, t, l, b+1)
		length := displayPixelDIP(displayEqualAxisTickDIP(y), f.YTick.DPI)
		for _, tick := range yp.Ticks {
			q := ypixel(tick.Value)
			if q >= t && q <= b && length > 0 {
				drawLine(dc, l-length, q, l, q)
			}
		}
		right := l - length
		if !y.HideLabels {
			right -= displayPixelDIP(displayCompareYLabelGapDIP, f.YTick.DPI)
			shown := displayEqualTickLabelVisibility(yp.Ticks, ypixel, func(string) int { return f.YTick.Height }, gap)
			paint, done := displayMeasuredTextPainter(dc, f.YTick, false)
			for i, tick := range yp.Ticks {
				q := ypixel(tick.Value)
				if shown[i] && q >= t && q <= b {
					paint(tick.Label, right-f.YTick.Measure(tick.Label).Width, q-f.YTick.Height/2)
				}
			}
			done()
			right -= displayCompareLabelWidth(yp, f.YTick)
		}
		if !y.HideTitle {
			title := displayAxisTitle(y)
			paint, done := displayMeasuredTextPainter(dc, f.YTitle, true)
			paint(title, right-gap-f.YTitle.Height, (t+b-f.YTitle.Measure(title).Width)/2)
			done()
		}
	}
}

func displayDrawMeasuredCompareColorbar(dc uintptr, layout displayPlotLayout, c displaycore.ColorMapping) {
	bar, tickArea := layout.ColorGradient, layout.ColorRegion
	if c.Hidden || !c.Range.Valid() || bar.Bottom <= bar.Top || bar.Right <= bar.Left {
		return
	}
	f := layout.Typography
	l, r, t, b := int(bar.Left), int(bar.Right), int(bar.Top), int(bar.Bottom)-1
	for i, n := 0, min(256, int(bar.Bottom-bar.Top)); i < n; i++ {
		cap := int(bar.Bottom - bar.Top)
		y0, y1 := t+i*cap/n, t+(i+1)*cap/n
		displayFill(dc, RECT{Left: int32(l), Top: int32(y0), Right: int32(r), Bottom: int32(y1)}, c.ColorAtPosition(1-float64(i)/float64(max(1, n-1))))
	}
	paint, done := displayMeasuredTextPainter(dc, f.ColorTitle, false)
	paint(layout.LegendTitle, l, int(tickArea.Top))
	done()
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(53, 66, 85))
	if pen != 0 {
		old, _, _ := pSelectObject.Call(dc, pen)
		defer func() { pSelectObject.Call(dc, old); pDeleteObject.Call(pen) }()
	}
	pixel := func(v float64) int { return b - int(math.Round(float64(b-t)*c.Range.Fraction(v))) }
	length := displayPixelDIP(c.MajorTickDIP(), f.ColorTick.DPI)
	gap := displayPixelDIP(displayCompareBarLabelGapDIP, f.ColorTick.DPI)
	plan := layout.ColorTicks
	for _, tick := range plan.Ticks {
		if length > 0 {
			y := pixel(tick.Value)
			drawLine(dc, r, y, r+length, y)
		}
	}
	shown := displayEqualTickLabelVisibility(plan.Ticks, pixel, func(string) int { return f.ColorTick.Height }, gap)
	paint, done = displayMeasuredTextPainter(dc, f.ColorTick, false)
	for i, tick := range plan.Ticks {
		if shown[i] {
			paint(tick.Label, r+length+gap, pixel(tick.Value)-f.ColorTick.Height/2)
		}
	}
	done()
}

func displayAxisWarning(x, y displaycore.Axis, xp, yp displaycore.TickPlan) string {
	if x.HideAxis || x.HideLabels {
		xp = displaycore.TickPlan{}
	}
	if y.HideAxis || y.HideLabels {
		yp = displaycore.TickPlan{}
	}
	for _, msg := range []string{xp.ValueError, yp.ValueError, xp.FormatWarning, yp.FormatWarning, xp.Warning, yp.Warning} {
		if msg != "" {
			return msg
		}
	}
	return ""
}

func displayDrawTickWarning(dc uintptr, plot RECT, m displayPlotMetrics, text string) {
	// Warnings are annotations, not replacement ticks or silent corrections.
	// Keep them inside the plot so no additional axis/title space is stolen.
	metrics := displayMetrics(dc, m.DPI)
	pad := max(2, displayPixelDIP(4, m.DPI))
	if metrics.Measure(text).Width+2*pad > int(plot.Right-plot.Left) {
		text = "刻度需调整"
	}
	w := metrics.Measure(text).Width + 2*pad
	h := metrics.Height + 2*pad
	if w > int(plot.Right-plot.Left) || h > int(plot.Bottom-plot.Top) {
		return
	}
	r := RECT{Left: plot.Left + int32(pad), Top: plot.Top + int32(pad), Right: plot.Left + int32(pad+w), Bottom: plot.Top + int32(pad+h)}
	if r.Right > plot.Right || r.Bottom > plot.Bottom {
		return
	}
	displayFill(dc, r, displaycore.RGB{R: 255, G: 248, B: 222})
	old, _, _ := pSetTextColor.Call(dc, rgbRef(136, 69, 0))
	drawAxisText(dc, text, int(r.Left)+pad, int(r.Top)+pad, int(r.Right)-pad, int(r.Bottom)-pad, DT_LEFT)
	pSetTextColor.Call(dc, old)
}
func displayDrawProjectedAxis(dc uintptr, a displaycore.Axis, x0, y0, x1, y1 int) {
	if a.HideAxis {
		return
	}
	drawLine(dc, x0, y0, x1, y1)
	length := math.Hypot(float64(x1-x0), float64(y1-y0))
	if length < 16 {
		return
	}
	nx, ny := float64(y1-y0)/length, -float64(x1-x0)/length
	m := displayMetrics(dc, displayDCDPI(dc))
	tickLength := displayPixelDIP(a.MajorTickDIP(), m.DPI)
	plan := displayAxisTickPlan(m, a, int(length), true, nil)
	for _, tick := range plan.Ticks {
		t := a.Fraction(tick.Value)
		x, y := x0+int(math.Round(t*float64(x1-x0))), y0+int(math.Round(t*float64(y1-y0)))
		if tickLength > 0 {
			drawLine(dc, x, y, x+int(nx*float64(tickLength)), y+int(ny*float64(tickLength)))
		}
		if !a.HideLabels {
			x += int(nx * float64(tickLength+m.Height))
			y += int(ny * float64(tickLength+m.Height))
			w := m.Measure(tick.Label).Width
			drawAxisText(dc, tick.Label, x-w/2-1, y-m.Height/2, x+(w+1)/2+1, y+(m.Height+1)/2, DT_CENTER)
		}
	}
	if !a.HideTitle {
		x := (x0+x1)/2 + int(nx*31)
		y := (y0+y1)/2 + int(ny*31)
		w := m.Measure(displayAxisTitle(a)).Width
		drawAxisText(dc, displayAxisTitle(a), x-w/2-1, y-m.Height/2, x+(w+1)/2+1, y+(m.Height+1)/2, DT_CENTER)
	}
}
func displayFill(dc uintptr, r RECT, c displaycore.RGB) {
	if r.Right <= r.Left || r.Bottom <= r.Top {
		return
	}
	brush, _, _ := pCreateSolidBrush.Call(rgbRef(c.R, c.G, c.B))
	if brush == 0 {
		return
	}
	pFillRect.Call(dc, uintptr(unsafe.Pointer(&r)), brush)
	pDeleteObject.Call(brush)
}
func displayDrawColorbar(dc uintptr, bar RECT, m displaycore.ColorMapping) {
	metrics := displayMetrics(dc, displayDCDPI(dc))
	t := int(bar.Top) + metrics.Height + (metrics.Height+1)/2 + displayPixelDIP(5, metrics.DPI)
	b := int(bar.Bottom) - (metrics.Height+1)/2
	displayDrawColorbarPlanned(dc, bar, m, metrics, displayColorTickPlan(metrics, m, b-t))
}

func displayDrawColorbarPlanned(dc uintptr, bar RECT, m displaycore.ColorMapping, metrics displayPlotMetrics, plan displaycore.TickPlan) {
	gap := max(2, displayPixelDIP(5, metrics.DPI))
	t, b := int(bar.Top)+metrics.Height+(metrics.Height+1)/2+gap, int(bar.Bottom)-(metrics.Height+1)/2
	if m.Hidden || !m.Range.Valid() || b <= t || bar.Right <= bar.Left {
		return
	}
	l := int(bar.Left)
	r := l + displayPixelDIP(16, metrics.DPI)
	for i := 0; i < min(256, b-t); i++ {
		n := min(256, b-t)
		y0, y1 := t+i*(b-t)/n, t+(i+1)*(b-t)/n
		displayFill(dc, RECT{Left: int32(l), Top: int32(y0), Right: int32(r), Bottom: int32(y1)}, m.ColorAtPosition(1-float64(i)/float64(max(1, n-1))))
	}
	title := displayColorTitle(m)
	drawAxisText(dc, title, l, int(bar.Top), l+metrics.Measure(title).Width+2, int(bar.Top)+metrics.Height, DT_LEFT)
	length := displayPixelDIP(m.MajorTickDIP(), metrics.DPI)
	pen, _, _ := pCreatePen.Call(PS_SOLID, 1, rgbRef(53, 66, 85))
	old, _, _ := pSelectObject.Call(dc, pen)
	defer func() {
		pSelectObject.Call(dc, old)
		if pen != 0 {
			pDeleteObject.Call(pen)
		}
	}()
	for _, tick := range plan.Ticks {
		y := b - int(math.Round(float64(b-t)*m.Range.Fraction(tick.Value)))
		if length > 0 {
			drawLine(dc, r, y, r+length, y)
		}
	}
	for _, label := range displayColorbarLabels(m, plan, metrics, t, b) {
		y := b - int(math.Round(float64(b-t)*m.Range.Fraction(label.Value)))
		drawAxisText(dc, label.Label, r+length+gap, y-metrics.Height/2, r+length+gap+metrics.Measure(label.Label).Width+2, y+(metrics.Height+1)/2, DT_LEFT)
	}
}
