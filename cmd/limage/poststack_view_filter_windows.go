//go:build windows

package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const (
	viewFilterReadyMessage = WM_USER + 244
	viewFilterTimer        = 0x5346
	viewFilterMaxValues    = (128 << 20) / 8
)

type viewFilterKey struct {
	owner                            uintptr
	data                             *compareTimeData // identity only; workers never use the workspace's Reader
	epoch, dataset, geometry, render int64
	aVersion, bVersion               uint64
	a, b                             string
	mode                             int
	line                             int32
	x0, x1                           uint64
	t0, t1, step                     int64
	first, last                      int
}

type viewFilterStyle struct {
	size               [3][2]int
	clientSize         [3][2]int
	mode               segy.DisplayMode
	gain, clip, lo, hi uint64
	agc, limits        bool
	wave               poststackWaveStyle
	waveDPI            int
}

type viewFilterTone struct {
	lo, hi float64
	valid  bool
}

func (s viewFilterStyle) options(i, first, last int) segy.RenderOptions {
	return segy.RenderOptions{Width: s.size[i][0], Height: s.size[i][1], DisplayMode: s.mode,
		GainPercent: math.Float64frombits(s.gain), ClipPercent: math.Float64frombits(s.clip), AGC: s.agc,
		UseValueLimits: s.limits, MinValue: math.Float64frombits(s.lo), MaxValue: math.Float64frombits(s.hi), SampleStart: first, SampleEnd: last}
}

type viewFilterRows struct {
	traces                      []int64
	coords                      []int32
	path                        string
	ns, dt, offset, first, last int
	originUS                    int64 // first displayed trace's actual time origin, in its local axis
	// Ordinary B uses the selected A physical-time interval, intersected
	// with B's own real axis. Generated B has an exact known source offset.
	physicalTimeWindow            bool
	requestFirstUS, requestLastUS int64
	values                        map[int64][]float64
}

func (s *viewFilterRows) read(trace int64, first, last int) ([]float64, error) {
	if s == nil || first < s.first || last < first || last > s.last || s.values[trace] == nil {
		return nil, fmt.Errorf("所选道或时间不在当前视图的可见范围内")
	}
	return s.values[trace][first-s.first : last-s.first+1], nil
}

type viewFilterTarget uint8

const (
	viewFilterA viewFilterTarget = 1 << iota
	viewFilterB
	viewFilterBoth = viewFilterA | viewFilterB
)

func (t viewFilterTarget) includes(side int) bool { return t&(1<<side) != 0 }

// SideFilterState is a value snapshot. A and B have separate base versions and
// settings even when a single operation applies identical parameters to both.
type SideFilterState struct {
	Enabled     bool
	Params      poststack.ViewBandpass
	BaseVersion uint64
}

type viewFilterSnapshot struct {
	key            viewFilterKey
	settings       [2]SideFilterState
	rows           [3]viewFilterRows
	title          [3]string
	first, last    int // A's displayed sample window, not necessarily Delta's
	identities     [2]os.FileInfo
	deltaReason    string
	unequalContext bool
	baseTone       viewFilterTone // shared A/B display range, independent of filter targets
}

type viewFilterRequest struct {
	key              viewFilterKey
	style            viewFilterStyle
	settings         [2]SideFilterState
	force            viewFilterTarget // explicitly applied sides are recalculated from base
	rows             [2]viewFilterRows
	title            [3]string
	pairedA, pairedB []int64
	pairCoords       []int32
	identities       [2]os.FileInfo
	deltaReason      string
	baseTone         viewFilterTone
}

type viewFilterResult struct {
	id       uint64
	request  viewFilterRequest
	snapshot *viewFilterSnapshot
	panels   [3]comparePanel // palette indices only until UI acceptance
	err      error
}

var viewFilter struct {
	path            string // A base path, separate from the transient workspace reader
	active          [2]SideFilterState
	requested       *[2]SideFilterState
	force           viewFilterTarget
	frame           *viewFilterSnapshot
	style           viewFilterStyle
	baseKey         viewFilterKey
	baseStyle       viewFilterStyle
	baseTone        viewFilterTone // last unfiltered Compare frame for the first filter operation
	status          string
	cancel          context.CancelFunc
	id              uint64
	pendingKey      viewFilterKey
	pendingStyle    viewFilterStyle
	pendingSettings [2]SideFilterState
	failed          bool
	scheduled       bool
}

var viewFilterMailbox struct {
	sync.Mutex
	id     uint64
	result *viewFilterResult
}

func viewFilterCurrentKey() viewFilterKey {
	k := viewFilterKey{owner: compareHwnd, data: compareTD, a: compareAPath, mode: compareMode, line: compareLineCoord,
		epoch: atomic.LoadInt64(&poststackEpoch), dataset: atomic.LoadInt64(&compareGen), geometry: atomic.LoadInt64(&compareGeomGen), render: atomic.LoadInt64(&compareSliceGen),
		aVersion: viewFilter.active[0].BaseVersion, bVersion: viewFilter.active[1].BaseVersion,
		x0: math.Float64bits(compareLineXMin), x1: math.Float64bits(compareLineXMax), t0: compareTraceStart, t1: compareTraceEnd,
		step: compareTraceStep, first: compareSampleStart, last: compareSampleEnd}
	if compareWorkspaceActive && compareActiveB() {
		k.b = compareBPath
	}
	return k
}

func viewFilterCurrentStyle() viewFilterStyle {
	s := viewFilterStyle{mode: renderDisplayMode, wave: poststackWaveDisplay.rowsKey(), gain: math.Float64bits(compareMappingGain()), clip: math.Float64bits(clipPercent),
		lo: math.Float64bits(limitMin), hi: math.Float64bits(limitMax), agc: agc, limits: useLimits}
	if s.wave.active() {
		s.waveDPI = displayDPI(compareHwnd)
	}
	for i, r := range comparePanelRectsAll() {
		if i >= len(s.size) {
			break
		}
		w, h := rectWH(r)
		s.clientSize[i] = [2]int{w, h}
		s.size[i] = [2]int{clampInt(w, 2, 1000), clampInt(h, 2, 1000)}
	}
	return s
}

func makeViewFilterRequest(settings [2]SideFilterState, force viewFilterTarget) (viewFilterRequest, error) {
	r := viewFilterRequest{key: viewFilterCurrentKey(), style: viewFilterCurrentStyle(), settings: settings, force: force}
	if viewFilter.baseKey == r.key && viewFilter.baseStyle == r.style {
		r.baseTone = viewFilter.baseTone
	}
	d := compareTD
	if compareMode == 2 {
		return r, fmt.Errorf("时间切片不是时间剖面，请切换到 Inline 或 Crossline")
	}
	if d == nil || d.fa == nil || d.ga == nil || poststackCompareInputPreparing() {
		return r, fmt.Errorf("请等待当前剖面与几何加载完成")
	}
	modeName := "Inline"
	if compareMode == 1 {
		modeName = "Crossline"
	}
	if d.traceOrderFallback {
		modeName = "道序剖面"
	}
	var lines [2]int32
	var aFirstUS, aLastUS int64
	for i, f := range []*segy.File{d.fa, d.fb} {
		if i == 1 && r.key.b == "" {
			break
		}
		g := d.ga
		if i == 1 {
			g = d.gb
		}
		if f == nil || g == nil || (i == 1 && f.Info.Path != r.key.b) {
			if i == 1 && !force.includes(i) {
				r.deltaReason = "B 剖面尚未就绪"
				continue
			}
			return r, fmt.Errorf("请等待 %c 剖面加载完成", 'A'+i)
		}
		identity, err := f.SourceFileInfo()
		if err != nil {
			return r, err
		}
		r.identities[i] = identity
		tr, co, line, err := g.NumericLineTraceNumbers(compareMode, compareLineCoord)
		if err != nil {
			if i == 1 && !force.includes(i) {
				r.deltaReason = err.Error()
				continue
			}
			return r, err
		}
		start, end, step := compareTraceRangeForGeometry(f)
		if end < start {
			tr, co = nil, nil
		} else {
			tr, co = filterPhysicalTraceRange(tr, co, start, end, step)
		}
		tr, co = filterLineRange(tr, co, compareLineXMin, compareLineXMax)
		if len(tr) == 0 {
			if i == 1 && !force.includes(i) {
				r.deltaReason = "B 当前范围没有可见道"
				continue
			}
			return r, fmt.Errorf("%c 当前范围没有可滤波的道", 'A'+i)
		}
		rows := viewFilterRows{traces: tr, coords: co, path: f.Info.Path, ns: f.Info.SamplesPerTrace, dt: f.Info.SampleIntervalUS}
		rows.originUS, err = f.TraceTimeOriginUS(tr[0])
		if err != nil {
			return r, err
		}
		if i == 1 {
			var generated bool
			rows.offset, generated = generatedPoststackSourceStart(rows.path)
			rows.physicalTimeWindow = !generated && rows.dt > 0 && r.rows[0].dt > 0
			rows.requestFirstUS, rows.requestLastUS = aFirstUS, aLastUS
		}
		rows.first, rows.last = viewFilterRowWindow(r.key, rows)
		if rows.last < rows.first {
			if i == 1 && !force.includes(i) {
				r.deltaReason = "B 当前时间窗没有对应样点"
				continue
			}
			return r, fmt.Errorf("%c 当前时间窗没有对应样点", 'A'+i)
		}
		if settings[i].Enabled {
			required, err := settings[i].Params.MinSamples(rows.dt)
			if err != nil {
				return r, fmt.Errorf("%c：%w", 'A'+i, err)
			}
			if rows.ns < required {
				return r, fmt.Errorf("%c：当前滤波设计需要至少 %d 个真实上下文样点，当前记录仅 %d 个", 'A'+i, required, rows.ns)
			}
		}
		if i == 0 {
			aFirstUS = rows.originUS + int64(rows.first)*int64(rows.dt)
			aLastUS = rows.originUS + int64(rows.last)*int64(rows.dt)
		}
		r.rows[i], lines[i] = rows, line
		r.title[i] = fmt.Sprintf("%c  %s %d", 'A'+i, modeName, line)
	}
	if len(r.rows[1].traces) > 0 {
		if !compareDifferenceGeometryKnown(d) {
			r.deltaReason = "独立 A/B 缺少可核实的共同几何"
			return r, nil
		}
		if lines[0] != lines[1] {
			r.deltaReason = "A/B 实际几何线不同"
			return r, nil
		}
		r.pairedA, r.pairedB, r.pairCoords = pairLineTracesByCoord(r.rows[0].traces, r.rows[0].coords, r.rows[1].traces, r.rows[1].coords)
		if len(r.pairedA) == 0 {
			r.deltaReason = "A/B 当前可见范围没有共同几何道"
			return r, nil
		}
		// A coordinate must identify one physical trace on each side.
		for _, rows := range r.rows {
			seen := make(map[int32]struct{}, len(rows.coords))
			for _, co := range rows.coords {
				if _, exists := seen[co]; exists {
					r.deltaReason = "A/B 几何存在重复道，无法确定唯一配对"
					return r, nil
				}
				seen[co] = struct{}{}
			}
		}
		r.title[2] = fmt.Sprintf("差  A−B  %s %d", modeName, lines[0])
	}
	return r, nil
}

func viewFilterRowWindow(k viewFilterKey, row viewFilterRows) (first, last int) {
	if row.physicalTimeWindow && row.dt > 0 {
		first = maxInt(0, int(math.Ceil(float64(row.requestFirstUS-row.originUS)/float64(row.dt))))
		last = minInt(row.ns-1, int(math.Floor(float64(row.requestLastUS-row.originUS)/float64(row.dt))))
		return first, last
	}
	first, last = maxInt(0, k.first-row.offset), row.ns-1
	if k.last >= 0 {
		last = minInt(last, k.last-row.offset)
	}
	return first, last
}

func viewFilterSourceIdentity(f *segy.File, path string, initial os.FileInfo) (os.FileInfo, error) {
	current, err := f.SourceFileInfo()
	if err != nil {
		return nil, err
	}
	pathInfo, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	same := func(a, b os.FileInfo) bool {
		return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
	}
	if !same(current, pathInfo) || (initial != nil && !same(initial, current)) {
		return nil, fmt.Errorf("滤波期间源文件发生变化")
	}
	return current, nil
}

func computeViewFilterSnapshot(ctx context.Context, r viewFilterRequest) (*viewFilterSnapshot, error) {
	return computeViewFilterSnapshotWithCache(ctx, r, nil)
}

func computeViewFilterSnapshotWithCache(ctx context.Context, r viewFilterRequest, previous *viewFilterSnapshot) (*viewFilterSnapshot, error) {
	s := &viewFilterSnapshot{key: r.key, settings: r.settings, title: r.title, deltaReason: r.deltaReason, baseTone: r.baseTone}
	s.rows[0], s.rows[1] = r.rows[0], r.rows[1]
	var files [2]*segy.File
	defer retryRetiredPoststackResults()
	remaining := viewFilterMaxValues
	reserve := func(traces, samples int) error {
		if samples < 1 || traces > remaining/samples {
			return fmt.Errorf("当前滤波视图超过 128 MiB 的显示样点上限，请缩小道数或时间窗")
		}
		remaining -= traces * samples
		return nil
	}
	for i := range files {
		row := &s.rows[i]
		if len(row.traces) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := openPoststackCompareSource(row.path)
		if err != nil {
			return nil, fmt.Errorf("%c：%w", 'A'+i, err)
		}
		files[i] = f
		defer f.Close()
		s.identities[i], err = viewFilterSourceIdentity(f, row.path, r.identities[i])
		if err != nil {
			return nil, err
		}
		if row.ns != f.Info.SamplesPerTrace || row.dt != f.Info.SampleIntervalUS {
			return nil, fmt.Errorf("%c 的采样轴已变化", 'A'+i)
		}
		row.originUS, err = f.TraceTimeOriginUS(row.traces[0])
		if err != nil {
			return nil, err
		}
		row.first, row.last = viewFilterRowWindow(r.key, *row)
		if row.last < row.first {
			return nil, fmt.Errorf("%c 当前时间窗没有可用样点", 'A'+i)
		}
		if err := reserve(len(row.traces), row.last-row.first+1); err != nil {
			return nil, err
		}
	}
	s.first, s.last = s.rows[0].first, s.rows[0].last
	for i, f := range files {
		if f == nil {
			continue
		}
		row := &s.rows[i]
		if !r.force.includes(i) && previous != nil && previous.settings[i] == r.settings[i] &&
			viewFilterSameIdentity(previous.identities[i], s.identities[i]) && viewFilterSameRowWindow(previous.rows[i], *row) {
			row.values, row.originUS = previous.rows[i].values, previous.rows[i].originUS
			continue
		}
		var apply func(context.Context, []float64, int, int) ([]float64, error)
		if r.settings[i].Enabled {
			var err error
			apply, err = poststack.NewTraceWindowFilter(row.dt, r.settings[i].Params)
			if err != nil {
				return nil, fmt.Errorf("%c：%w", 'A'+i, err)
			}
		}
		row.values = make(map[int64][]float64, len(row.traces))
		for _, tr := range row.traces {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if row.values[tr] != nil {
				continue
			}
			var err error
			var values []float64
			if apply != nil {
				var h segy.TraceHeader
				h, err = f.ReadTraceHeader(tr)
				if err == nil && ((h.SampleCount != 0 && h.SampleCount != row.ns) || (h.SampleIntervalUS != 0 && h.SampleIntervalUS != row.dt)) {
					err = fmt.Errorf("采样轴与卷头不一致")
				}
				if err == nil {
					values, err = f.ReadTraceWindow(tr, 0, row.ns-1)
				}
				if err == nil {
					values, err = apply(ctx, values, row.first, row.last)
				}
			} else {
				values, err = f.ReadTraceWindow(tr, row.first, row.last)
			}
			if err != nil {
				return nil, fmt.Errorf("%c 道 %d：%w", 'A'+i, tr+1, err)
			}
			row.values[tr] = values
		}
		if _, err := viewFilterSourceIdentity(f, row.path, s.identities[i]); err != nil {
			return nil, err
		}
	}
	if files[0] != nil && files[1] != nil && s.deltaReason == "" {
		aRow, bRow := &s.rows[0], &s.rows[1]
		first := maxInt(aRow.first, bRow.first+bRow.offset)
		last := minInt(aRow.last, bRow.last+bRow.offset)
		axisErr := validateViewFilterDifference(ctx, files, r.pairedA, r.pairedB, first, last, bRow.offset)
		if axisErr != nil {
			s.deltaReason = "A/B 采样轴不兼容：" + axisErr.Error()
			return s, ctx.Err()
		}
		s.unequalContext = aRow.ns != bRow.ns || bRow.offset != 0
		n := last - first + 1
		if err := reserve(len(r.pairedA), n); err != nil {
			return nil, err
		}
		row := &s.rows[2]
		*row = viewFilterRows{traces: r.pairedA, coords: r.pairCoords, ns: aRow.ns, dt: aRow.dt, first: first, last: last, values: make(map[int64][]float64, len(r.pairedA))}
		var err error
		row.originUS, err = files[0].TraceTimeOriginUS(r.pairedA[0])
		if err != nil {
			return nil, err
		}
		for i, tr := range r.pairedA {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			a, errA := aRow.read(tr, first, last)
			b, errB := bRow.read(r.pairedB[i], first-bRow.offset, last-bRow.offset)
			if errA != nil || errB != nil {
				return nil, fmt.Errorf("A/B 配对样点不完整")
			}
			diff := make([]float64, n)
			for j := range diff {
				diff[j] = a[j] - b[j]
				if math.IsNaN(diff[j]) || math.IsInf(diff[j], 0) {
					*row = viewFilterRows{}
					s.deltaReason = "A−B 数值超出有效范围"
					return s, ctx.Err()
				}
			}
			row.values[tr] = diff
		}
	}
	return s, ctx.Err()
}

func validateViewFilterDifference(ctx context.Context, files [2]*segy.File, a, b []int64, first, last, offset int) error {
	if len(a) == 0 || len(a) != len(b) {
		return fmt.Errorf("A/B 当前可见范围没有共同几何道")
	}
	// An unshifted, longer B can also share a genuine prefix with A. Reuse
	// the strict reader validation in the orientation it accepts.
	if offset == 0 && files[1].Info.SamplesPerTrace > files[0].Info.SamplesPerTrace {
		files[0], files[1], a, b = files[1], files[0], b, a
	}
	for i := 0; i < len(a); i += 256 {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := minInt(i+256, len(a))
		if err := segy.ValidateDifferenceWindow(files[0], files[1], a[i:end], b[i:end], first, last, offset); err != nil {
			return err
		}
	}
	return nil
}

func viewFilterSameIdentity(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func viewFilterSameRowWindow(a, b viewFilterRows) bool {
	return a.path == b.path && a.ns == b.ns && a.dt == b.dt && a.offset == b.offset && a.first == b.first && a.last == b.last &&
		a.originUS == b.originUS && slices.Equal(a.traces, b.traces) && slices.Equal(a.coords, b.coords)
}

func renderViewFilterSnapshot(ctx context.Context, s *viewFilterSnapshot, style viewFilterStyle) ([3]comparePanel, error) {
	var panels [3]comparePanel
	var values [3][]float64
	var waveA poststackWaveSelection
	for i := range panels {
		row := &s.rows[i]
		if len(row.traces) == 0 || style.size[i][0] < 2 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return panels, err
		}
		o := style.options(i, row.first, row.last)
		o.KeepInvalid = true
		if i == 2 {
			// This row is already A_view-B_view in the sample domain. Applying
			// AGC to it again would hide the actual relative residual amplitudes.
			o.AGC = false
		}
		v, st, err := segy.RenderSectionSnapshotValues(row.traces, row.ns, o, func(tr int64, first, last int) ([]float64, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return row.read(tr, first, last)
		})
		if err != nil {
			return panels, err
		}
		values[i] = v
		panels[i] = comparePanel{w: o.Width, h: o.Height, stats: st, displayValues: v, coords: row.coords, title: s.title[i], filtered: i < 2 && s.settings[i].Enabled, viewAxes: true,
			filterX0: float64(row.coords[0]), filterX1: float64(row.coords[len(row.coords)-1]),
			filterT0: float64(row.originUS+int64(row.first)*int64(row.dt)) / 1000, filterT1: float64(row.originUS+int64(row.last)*int64(row.dt)) / 1000}
		if style.wave.active() {
			coords := poststackWaveCoordinates(row.traces, row.coords)
			clientWidth := style.clientSize[i][0]
			if clientWidth <= 0 {
				clientWidth = o.Width
			}
			dpi := style.waveDPI
			if dpi <= 0 {
				dpi = 96
			}
			pick := poststackWaveSelectionForPanel(coords, waveA, i > 0, poststackWaveWidthDIP(clientWidth, dpi), row.last-row.first+1, style.wave)
			if i == 0 {
				waveA = pick
			}
			panels[i].wave = poststackBuildWaveFrameContext(ctx, row.traces, coords, row.first, row.last, row.ns, row.dt, pick, row.read)
			if err := ctx.Err(); err != nil {
				return panels, err
			}
		}
	}
	if len(values[0]) > 0 && len(values[1]) > 0 {
		tone, err := viewFilterSharedBaseTone(ctx, s, style, values)
		if err != nil {
			return panels, err
		}
		s.baseTone = tone
		for i := 0; i < 2; i++ {
			panels[i].stats.MapMin, panels[i].stats.MapMax = tone.lo, tone.hi
			panels[i].indices = segy.MapAmplitudeValues(values[i], tone.lo, tone.hi)
		}
	} else if len(values[0]) > 0 {
		panels[0].indices = segy.MapSectionSnapshot(values[0], &panels[0].stats, style.options(0, 0, 0))
	}
	if len(values[2]) > 0 {
		o := style.options(2, 0, 0)
		abs := make([]float64, len(values[2]))
		for j, v := range values[2] {
			if seismicFinite(v) {
				abs[j] = math.Abs(v)
			}
		}
		sort.Float64s(abs)
		amp := abs[minInt(len(abs)-1, int(float64(len(abs))*(100-o.GainPercent)/100))]
		if o.UseValueLimits && o.MaxValue > o.MinValue {
			amp = math.Max(math.Abs(o.MinValue), math.Abs(o.MaxValue))
		}
		if amp <= 0 || math.IsInf(amp, 0) || math.IsNaN(amp) {
			amp = 1
		}
		panels[2].stats.MapMin, panels[2].stats.MapMax = -amp, amp
		panels[2].indices = segy.MapAmplitudeValues(values[2], -amp, amp)
	}
	return panels, ctx.Err()
}

// Recompute automatic contrast only from the base data in this viewport. A
// filter can change one panel's amplitudes, never the other panel's palette
// mapping. At most two screen-resolution rasters are needed; no extra trace
// volume is retained in the snapshot.
func viewFilterSharedBaseTone(ctx context.Context, s *viewFilterSnapshot, style viewFilterStyle, values [3][]float64) (viewFilterTone, error) {
	if s.baseTone.valid {
		return s.baseTone, nil
	}
	o := style.options(0, 0, 0)
	if o.UseValueLimits && o.MaxValue > o.MinValue {
		return viewFilterTone{o.MinValue, o.MaxValue, true}, nil
	}
	base := [2][]float64{values[0], values[1]}
	for i := range base {
		if !s.settings[i].Enabled {
			continue // the current raster of an unfiltered side is already its base
		}
		row := &s.rows[i]
		var err error
		base[i], err = renderViewFilterBaseValues(ctx, row, s.identities[i], style.options(i, row.first, row.last))
		if err != nil {
			return viewFilterTone{}, fmt.Errorf("%c 基准显示范围：%w", 'A'+i, err)
		}
	}
	lo, hi := poststackSharedSectionRange(base[0], base[1], o.GainPercent, false, 0, 0)
	return viewFilterTone{lo, hi, true}, nil
}

func renderViewFilterBaseValues(ctx context.Context, row *viewFilterRows, identity os.FileInfo, o segy.RenderOptions) ([]float64, error) {
	f, err := openPoststackCompareSource(row.path)
	if err != nil {
		return nil, err
	}
	defer retryRetiredPoststackResults()
	defer f.Close()
	if _, err = viewFilterSourceIdentity(f, row.path, identity); err != nil {
		return nil, err
	}
	v, _, err := segy.RenderSectionSnapshotValues(row.traces, row.ns, o, func(trace int64, first, last int) ([]float64, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return f.ReadTraceWindow(trace, first, last)
	})
	if err != nil {
		return nil, err
	}
	_, err = viewFilterSourceIdentity(f, row.path, identity)
	return v, err
}

func cancelPoststackViewFilter() {
	if viewFilter.cancel != nil {
		viewFilter.cancel()
		viewFilter.cancel = nil
	}
	viewFilter.id++
	viewFilterMailbox.Lock()
	viewFilterMailbox.id = viewFilter.id
	viewFilterMailbox.result = nil
	viewFilterMailbox.Unlock()
	if compareHwnd != 0 {
		pKillTimer.Call(compareHwnd, viewFilterTimer)
	}
	viewFilter.scheduled = false
}

func viewFilterIsPending() bool { return viewFilter.cancel != nil || viewFilter.scheduled }

func syncViewFilterSource() {
	if viewFilter.path != compareAPath {
		resetPoststackViewFilter()
	}
}

func resetPoststackViewFilter() {
	cancelPoststackViewFilter()
	viewFilter.path = compareAPath
	for i, side := range viewFilter.active {
		viewFilter.active[i] = SideFilterState{BaseVersion: side.BaseVersion + 1}
	}
	viewFilter.requested, viewFilter.frame = nil, nil
	viewFilter.baseTone = viewFilterTone{}
	viewFilter.force, viewFilter.status, viewFilter.failed = 0, "", false
}

// A newly selected B is always a new base, including reload of the same path.
// Keep the previous snapshot only as a possible A cache; its key is invalid.
func invalidatePoststackViewFilterB() {
	cancelPoststackViewFilter()
	viewFilter.active[1] = SideFilterState{BaseVersion: viewFilter.active[1].BaseVersion + 1}
	viewFilter.requested = nil
	viewFilter.baseTone = viewFilterTone{}
	viewFilter.force, viewFilter.status, viewFilter.failed = 0, "", false
	if old := viewFilter.frame; old != nil {
		// Previously accepted snapshots may still be read by analysis workers;
		// detach the obsolete B/Delta buffers without mutating their frame.
		if viewFilter.active[0].Enabled && len(old.rows[0].values) > 0 {
			viewFilter.frame = &viewFilterSnapshot{key: old.key, settings: old.settings, first: old.first, last: old.last,
				rows: [3]viewFilterRows{old.rows[0]}, identities: [2]os.FileInfo{old.identities[0]}}
		} else {
			viewFilter.frame = nil
		}
	}
}

func startViewFilterRequest(r viewFilterRequest) {
	cancelPoststackViewFilter()
	viewFilter.failed = false
	ctx, cancel := context.WithCancel(context.Background())
	viewFilter.cancel = cancel
	viewFilter.pendingKey, viewFilter.pendingStyle, viewFilter.pendingSettings = r.key, r.style, r.settings
	viewFilter.status = "正在从基准样点计算视图滤波…"
	setText(cc.status, viewFilter.status)
	syncPoststackSpectrumControls()
	frame := viewFilter.frame
	if frame != nil && frame.key == r.key && viewFilter.style == r.style && frame.baseTone.valid {
		r.baseTone = frame.baseTone
	}
	id := viewFilter.id
	go func() {
		result := &viewFilterResult{id: id, request: r}
		result.snapshot, result.err = computeViewFilterSnapshotWithCache(ctx, r, frame)
		if result.err == nil {
			result.panels, result.err = renderViewFilterSnapshot(ctx, result.snapshot, r.style)
		}
		viewFilterMailbox.Lock()
		if ctx.Err() != nil || viewFilterMailbox.id != id {
			viewFilterMailbox.Unlock()
			return
		}
		viewFilterMailbox.result = result
		viewFilterMailbox.Unlock()
		pPostMessageW.Call(r.key.owner, viewFilterReadyMessage, 0, 0)
	}()
}

func viewFilterFrameLabel(frame *viewFilterSnapshot) string {
	if frame == nil {
		return ""
	}
	var labels []string
	for i, side := range frame.settings {
		if side.Enabled && len(frame.rows[i].traces) > 0 {
			labels = append(labels, fmt.Sprintf("%c %s", 'A'+i, side.Params.Summary()))
		}
	}
	if len(labels) == 0 {
		return ""
	}
	label := "视图滤波：" + strings.Join(labels, " · ")
	if frame.unequalContext {
		label += " · 局部 B 按自身时间上下文处理，差仅取共同范围"
	}
	return label
}

func handleViewFilterReady() {
	viewFilterMailbox.Lock()
	r := viewFilterMailbox.result
	viewFilterMailbox.result = nil
	viewFilterMailbox.Unlock()
	if r == nil || r.id != viewFilter.id {
		return
	}
	if viewFilter.cancel != nil {
		viewFilter.cancel()
		viewFilter.cancel = nil
	}
	if !viewFilterResultCurrent(r, viewFilter.id, viewFilterCurrentKey(), viewFilterCurrentStyle()) {
		if compareMode != 2 {
			scheduleCurrentViewFilter()
		}
		return
	}
	if r.err == nil {
		for i, row := range r.request.rows {
			if row.path == "" || r.request.identities[i] == nil {
				continue
			}
			initial := r.request.identities[i]
			current, err := os.Stat(row.path)
			if err != nil || !os.SameFile(initial, current) || initial.Size() != current.Size() || !initial.ModTime().Equal(current.ModTime()) {
				r.err = fmt.Errorf("%c 源文件在发布前发生变化", 'A'+i)
				break
			}
		}
	}
	if r.err != nil {
		if viewFilter.requested != nil && !viewFilterAnyEnabled(*viewFilter.requested) {
			// Restoration must not leave a filter enabled merely because the
			// display composition exceeded its memory cap or a reader failed.
			// Fall back to the ordinary base renderer; report the composition
			// failure instead of presenting a stale filtered frame as restored.
			viewFilter.active, viewFilter.frame = *viewFilter.requested, nil
			compareA, compareB, compareD = comparePanel{}, comparePanel{}, comparePanel{}
			compareDeltaReason = ""
		}
		viewFilter.requested = nil
		viewFilter.force = 0
		viewFilter.failed = true
		if committedViewFilter() == nil {
			renderCompareLineBase()
			invalidateCompareBase()
			refreshViewFilterAnalysis()
		}
		if poststackPendingBDisplay != nil {
			if len(compareB.bgra) > 0 {
				finishPoststackGeneratedBDisplay(nil)
			} else {
				finishPoststackGeneratedBDisplay(r.err)
			}
		}
		viewFilter.status = "未应用滤波：" + r.err.Error()
		setText(cc.status, viewFilter.status)
		syncPoststackSpectrumControls()
		return
	}
	for i := range r.panels {
		r.panels[i].bgra = comparePaletteBGRA(r.panels[i].indices)
	}
	compareA, compareB, compareD = r.panels[0], r.panels[1], r.panels[2]
	seismicRefreshCompareColors()
	compareMapMin, compareMapMax = compareA.stats.MapMin, compareA.stats.MapMax
	compareDeltaReason = r.snapshot.deltaReason
	viewFilter.frame, viewFilter.style = r.snapshot, r.request.style
	viewFilter.active = r.request.settings
	viewFilter.requested = nil
	viewFilter.force = 0
	viewFilter.status = viewFilterFrameLabel(r.snapshot)
	if viewFilter.status == "" {
		viewFilter.status = "已恢复所选视图的基准数据"
	}
	if r.snapshot.deltaReason != "" && r.request.key.b != "" && r.request.style.size[2][0] > 0 {
		viewFilter.status += " · 差未计算：" + r.snapshot.deltaReason
	}
	setText(cc.status, viewFilter.status)
	invalidateCompareBase()
	syncPoststackSpectrumControls()
	if poststackPendingBDisplay != nil && len(compareB.bgra) > 0 {
		viewFilter.status = viewFilterStatusForBandpassResult(viewFilter.status, poststackPendingBDisplay)
		finishPoststackGeneratedBDisplay(nil)
	}
	setText(cc.status, viewFilter.status)
	refreshViewFilterSpectrum()
	refreshViewFilterTraceAnalysis()
}

func viewFilterStatusForBandpassResult(status string, result *generatedPoststackResult) string {
	if result == nil || result.Preview || result.Record.AlgorithmID != poststack.BandpassAlgorithmID {
		return status
	}
	if p := viewFilterSideSettings()[0]; p != nil && !viewFilter.active[1].Enabled && compareTD != nil && compareTD.fa != nil {
		formal, err := poststack.BandpassParameters(result.Record.Parameters, compareTD.fa.Info.SampleIntervalUS)
		if err == nil && formal.Canonical() == p.Canonical() {
			status += " · 正式 B 使用相同滤波；差可接近零。在频谱中可仅恢复 A。"
		}
	}
	return status
}

func viewFilterResultCurrent(r *viewFilterResult, id uint64, key viewFilterKey, style viewFilterStyle) bool {
	return r != nil && r.id == id && r.request.key == key && r.request.style == style
}

func committedViewFilter() *viewFilterSnapshot {
	if viewFilter.frame != nil && viewFilter.frame.key == viewFilterCurrentKey() {
		return viewFilter.frame
	}
	return nil
}

func applyPoststackViewFilterTarget(target viewFilterTarget, p poststack.ViewBandpass) error {
	syncViewFilterSource()
	if err := validateViewFilterTarget(target); err != nil {
		return err
	}
	settings := withViewFilterTarget(viewFilter.active, target, true, p)
	r, err := makeViewFilterRequest(settings, target)
	if err != nil {
		return err
	}
	viewFilter.requested, viewFilter.force = &settings, target
	startViewFilterRequest(r)
	return nil
}

func validateViewFilterTarget(target viewFilterTarget) error {
	if target == 0 || target & ^viewFilterBoth != 0 {
		return fmt.Errorf("请选择有效的滤波对象")
	}
	if target.includes(1) && (!compareWorkspaceActive || !compareActiveB() || compareTD == nil || compareTD.fb == nil || compareTD.gb == nil) {
		return fmt.Errorf("B 尚未就绪，只能选择 A")
	}
	return nil
}

func withViewFilterTarget(states [2]SideFilterState, target viewFilterTarget, enabled bool, p poststack.ViewBandpass) [2]SideFilterState {
	for i := range states {
		if target.includes(i) {
			states[i].Enabled, states[i].Params = enabled, p
		}
	}
	return states
}

func restorePoststackViewFilterTarget(target viewFilterTarget) error {
	syncViewFilterSource()
	if err := validateViewFilterTarget(target); err != nil {
		return err
	}
	settings := withViewFilterTarget(viewFilter.active, target, false, poststack.ViewBandpass{})
	// Cancel before changing UI/state, including restoration while Apply is in flight.
	cancelPoststackViewFilter()
	viewFilter.requested, viewFilter.force, viewFilter.failed = nil, 0, false
	if compareMode != 2 && !poststackCompareInputPreparing() && (viewFilterAnyEnabled(settings) || (compareWorkspaceActive && compareActiveB())) {
		r, err := makeViewFilterRequest(settings, 0)
		if err != nil {
			return err
		}
		viewFilter.requested = &settings
		startViewFilterRequest(r)
		return nil
	}
	viewFilter.active = settings
	viewFilter.frame = nil
	viewFilter.status = "已恢复所选视图的基准数据"
	if compareMode != 2 && !poststackCompareInputPreparing() {
		renderCompareLineBase()
	}
	setText(cc.status, viewFilter.status)
	syncPoststackSpectrumControls()
	refreshViewFilterAnalysis()
	return nil
}

func viewFilterSideSettings() [2]*poststack.ViewBandpass {
	var result [2]*poststack.ViewBandpass
	for i, side := range viewFilter.active {
		if side.Enabled {
			p := side.Params
			result[i] = &p
		}
	}
	return result
}

func viewFilterAnyEnabled(settings [2]SideFilterState) bool {
	return settings[0].Enabled || settings[1].Enabled
}

func viewFilterDesiredSettings() [2]SideFilterState {
	if viewFilter.requested != nil {
		return *viewFilter.requested
	}
	return viewFilter.active
}

func viewFilterWanted() *poststack.ViewBandpass {
	for i, side := range viewFilterDesiredSettings() {
		if side.Enabled && (i == 0 || (compareWorkspaceActive && compareActiveB())) {
			return &side.Params
		}
	}
	return nil
}

// Only selection-changing render calls debounce; mouse motion does not filter.
func scheduleCurrentViewFilter() bool {
	syncViewFilterSource()
	settings := viewFilterDesiredSettings()
	k, style := viewFilterCurrentKey(), viewFilterCurrentStyle()
	// After restoring both sides, keep using the common-window compositor
	// for this pair when panning/zooming. The legacy raw renderer otherwise
	// requires a local B to cover all of A's newly selected time window.
	composite := viewFilter.frame != nil && viewFilterSameComparePair(viewFilter.frame.key, k)
	if compareMode == 2 || (viewFilterWanted() == nil && viewFilter.requested == nil && !composite) {
		return false
	}
	if compareTD == nil || poststackCompareInputPreparing() {
		return false
	}
	if frame := committedViewFilter(); frame != nil && frame.settings == settings && style == viewFilter.style && viewFilter.force == 0 {
		return true
	}
	if viewFilter.pendingKey == k && viewFilter.pendingStyle == style && viewFilter.pendingSettings == settings && (viewFilterIsPending() || viewFilter.failed) {
		return !viewFilter.failed || committedViewFilter() != nil
	}
	cancelPoststackViewFilter()
	viewFilter.failed = false
	viewFilter.status = "等待更新当前视图滤波…"
	if committedViewFilter() == nil {
		compareA, compareB, compareD = comparePanel{}, comparePanel{}, comparePanel{}
		compareDeltaReason = ""
		invalidateCompareBase()
	}
	viewFilter.pendingKey, viewFilter.pendingStyle, viewFilter.pendingSettings = k, style, settings
	setText(cc.status, viewFilter.status)
	syncPoststackSpectrumControls()
	viewFilter.scheduled = true
	if timer, _, _ := pSetTimer.Call(compareHwnd, viewFilterTimer, 100, 0); timer == 0 {
		runScheduledViewFilter()
	}
	return true
}

func viewFilterSameComparePair(old, current viewFilterKey) bool {
	return current.b != "" && old.owner == current.owner && old.a == current.a && old.b == current.b &&
		old.aVersion == current.aVersion && old.bVersion == current.bVersion
}

func runScheduledViewFilter() {
	pKillTimer.Call(compareHwnd, viewFilterTimer)
	if !viewFilter.scheduled {
		return
	} // a queued WM_TIMER may survive KillTimer
	viewFilter.scheduled = false
	if compareMode != 2 && !poststackCompareInputPreparing() {
		settings := viewFilterDesiredSettings()
		r, err := makeViewFilterRequest(settings, viewFilter.force)
		if err == nil {
			startViewFilterRequest(r)
			return
		}
		viewFilter.pendingKey, viewFilter.pendingStyle = viewFilterCurrentKey(), viewFilterCurrentStyle()
		viewFilter.failed = true
		viewFilter.requested = nil
		viewFilter.force = 0
		if committedViewFilter() == nil {
			renderCompareLineBase()
			refreshViewFilterAnalysis()
		}
		if poststackPendingBDisplay != nil {
			if len(compareB.bgra) > 0 {
				finishPoststackGeneratedBDisplay(nil)
			} else {
				finishPoststackGeneratedBDisplay(err)
			}
		}
		viewFilter.status = "未应用滤波：" + err.Error()
		setText(cc.status, viewFilter.status)
		syncPoststackSpectrumControls()
	}
}

func poststackViewFilterDialogInfo() (object, scope, status string, dtUS int) {
	object = "当前叠后剖面"
	if compareWorkspaceActive {
		object = "对比剖面"
	}
	status = viewFilter.status
	if status == "" {
		status = "未启用视图滤波"
	}
	scope, dtUS = poststackViewFilterSideScope(0)
	if viewFilterAnyEnabled(viewFilter.active) && strings.HasPrefix(status, "未应用") {
		if committedViewFilter() != nil {
			scope += "（当前画面保留上一次已生效的状态）"
		} else {
			scope += "（当前范围暂显示基准数据）"
		}
	}
	return
}

func poststackViewFilterSideScope(side int) (scope string, dtUS int) {
	if compareMode == 2 {
		return "时间切片不支持逐道时间滤波；请切换到 Inline 或 Crossline。", 0
	}
	if compareTD == nil || compareTD.fa == nil || compareTD.ga == nil || poststackCompareInputPreparing() {
		return "等待剖面和几何就绪。", 0
	}
	d := compareTD
	f, g := d.fa, d.ga
	if side == 1 {
		f, g = d.fb, d.gb
		if !compareWorkspaceActive || !compareActiveB() || f == nil || g == nil {
			return "B 尚未就绪。", 0
		}
	}
	tr, co, line, err := g.LineTraceNumbers(compareMode, compareLineCoord)
	if err != nil {
		return err.Error(), 0
	}
	lo, hi, step := compareTraceRangeForGeometry(f)
	if hi < lo {
		return "当前范围无可见道。", 0
	}
	tr, co = filterPhysicalTraceRange(tr, co, lo, hi, step)
	tr, co = filterLineRange(tr, co, compareLineXMin, compareLineXMax)
	if len(tr) == 0 {
		return "当前范围无可见道。", 0
	}
	row := viewFilterRows{ns: f.Info.SamplesPerTrace, dt: f.Info.SampleIntervalUS}
	row.originUS, err = f.TraceTimeOriginUS(tr[0])
	if err != nil {
		return err.Error(), 0
	}
	key := viewFilterCurrentKey()
	if side == 1 {
		var generated bool
		row.offset, generated = generatedPoststackSourceStart(f.Info.Path)
		if !generated && row.dt > 0 && d.fa.Info.SampleIntervalUS > 0 {
			// Use the same first visible A trace and time extent as the render
			// request, even before its B samples have been computed.
			atr, aco, _, e := d.ga.LineTraceNumbers(compareMode, compareLineCoord)
			if e == nil {
				start, end, step := compareTraceRangeForGeometry(d.fa)
				atr, aco = filterPhysicalTraceRange(atr, aco, start, end, step)
				atr, _ = filterLineRange(atr, aco, compareLineXMin, compareLineXMax)
				if len(atr) > 0 {
					origin, e := d.fa.TraceTimeOriginUS(atr[0])
					if e == nil {
						a := viewFilterRows{ns: d.fa.Info.SamplesPerTrace, dt: d.fa.Info.SampleIntervalUS, originUS: origin}
						first, last := viewFilterRowWindow(key, a)
						row.physicalTimeWindow = true
						row.requestFirstUS, row.requestLastUS = viewFilterSampleTime(a, first), viewFilterSampleTime(a, last)
					}
				}
			}
		}
	}
	first, last := viewFilterRowWindow(key, row)
	if last < first {
		return "当前时间窗没有对应样点。", 0
	}
	dir := "Inline"
	if compareMode == 1 {
		dir = "Crossline"
	}
	if d.traceOrderFallback {
		dir = "道序"
	}
	return fmt.Sprintf("%s %d · %d 道（%d–%d）\r\n首道时间 %.3f–%.3f ms", dir, line, len(tr), co[0], co[len(co)-1],
		float64(viewFilterSampleTime(row, first))/1000, float64(viewFilterSampleTime(row, last))/1000), row.dt
}

func viewFilterSampleTime(row viewFilterRows, sample int) int64 {
	return row.originUS + int64(sample)*int64(row.dt)
}

func viewFilterDisplayedSamples() (int, int) {
	return viewFilterDisplayedSamplesForPanel(0)
}

func viewFilterDisplayedSamplesForPanel(panel int) (int, int) {
	if s := committedViewFilter(); s != nil && panel >= 0 && panel < len(s.rows) {
		row := &s.rows[panel]
		if len(row.traces) > 0 {
			return row.first + row.offset, row.last + row.offset
		}
	}
	return compareSampleStart, compareSampleEnd
}

func viewFilterTimeMS(panel int, y float64) (float64, bool) {
	if s := committedViewFilter(); s != nil && panel >= 0 && panel < len(s.rows) {
		row := &s.rows[panel]
		if len(row.traces) > 0 {
			return (float64(row.originUS) + (y-float64(row.offset))*float64(row.dt)) / 1000, true
		}
	}
	return 0, false
}

// View hit-testing uses a local panel index plus its generated offset. The
// linked crosshair stores canonical A-relative samples instead, preserving
// physical time even for an independently browsable incompatible B axis.
func viewFilterAWorldSample(panel int, y float64) float64 {
	if s := committedViewFilter(); panel == 1 && s != nil {
		a, b := &s.rows[0], &s.rows[1]
		if a.dt > 0 && b.dt > 0 && len(b.traces) > 0 {
			return (float64(b.originUS-a.originUS) + (y-float64(b.offset))*float64(b.dt)) / float64(a.dt)
		}
	}
	return y
}

func viewFilterLocalPanelSample(panel int, y float64) float64 {
	if s := committedViewFilter(); panel == 1 && s != nil {
		a, b := &s.rows[0], &s.rows[1]
		if a.dt > 0 && b.dt > 0 && len(b.traces) > 0 {
			return (float64(a.originUS-b.originUS)+y*float64(a.dt))/float64(b.dt) + float64(b.offset)
		}
	}
	return y
}

func viewFilterPanelRange(panel int) (float64, float64) {
	if s := committedViewFilter(); s != nil && panel >= 0 && panel < len(s.rows) {
		co := s.rows[panel].coords
		if len(co) > 0 {
			return float64(co[0]), float64(co[len(co)-1])
		}
	}
	return compareLineXMin, compareLineXMax
}

func viewFilterHover(panel int, x, y float64) string {
	s := committedViewFilter()
	if s == nil || panel < 0 || panel >= len(s.rows) {
		return ""
	}
	row := &s.rows[panel]
	tr, _, ok := nearestCompareLineTrace(row.traces, row.coords, x)
	if !ok {
		return ""
	}
	local := int(math.Round(y)) - row.offset
	v, err := row.read(tr, local, local)
	if err != nil {
		return ""
	}
	label := "未滤波振幅"
	if panel == 2 {
		label = "A−B 振幅"
	} else if s.settings[panel].Enabled {
		label = "滤波振幅"
	}
	return fmt.Sprintf(" | %s %.6g", label, v[0])
}

func attachViewFilterTraceSelection(selection traceAnalysisSelection) traceAnalysisSelection {
	selection.FromPoststackView = true
	s := committedViewFilter()
	if s == nil {
		return selection
	}
	selection.UnfilteredContext = selection.Context
	if label := viewFilterFrameLabel(s); label != "" {
		selection.Context += " | " + label
	}
	if selection.Difference && s.deltaReason != "" {
		selection.Context += " | 差未计算：" + s.deltaReason
		selection.Difference = false
	}
	for i := range selection.Targets {
		t := &selection.Targets[i]
		j := 0
		if t.Role == "B" {
			j = 1
		}
		row := &s.rows[j]
		if row.path != "" && strings.EqualFold(filepath.Clean(row.path), filepath.Clean(t.Path)) && row.values[t.Trace] != nil {
			t.SampleStart, t.SampleEnd = row.first, row.last
			if selection.Difference && len(s.rows[2].traces) > 0 {
				t.SampleStart, t.SampleEnd = s.rows[2].first-row.offset, s.rows[2].last-row.offset
			}
			t.MarkerSample = clampInt(t.MarkerSample, t.SampleStart, t.SampleEnd)
			if s.settings[j].Enabled {
				t.Filtered = row
			} else {
				selection.Context += fmt.Sprintf(" | %c 未滤波", 'A'+j)
			}
		}
	}
	return selection
}

func refreshViewFilterAnalysis() { refreshViewFilterSpectrum(); refreshViewFilterTraceAnalysis() }

func refreshViewFilterTraceAnalysis() {
	if traceAnalysisHwnd == 0 || !traceAnalysisSelectionCurrent.FromPoststackView {
		return
	}
	if traceAnalysisSelectionCurrent.Anchor.Owner != 0 {
		refreshScientificTraceFromCompare()
		return
	}
	old := traceAnalysisSelectionCurrent
	if len(old.Targets) == 0 {
		return
	}
	for _, t := range old.Targets {
		if t.Path != compareAPath && t.Path != compareBPath {
			pDestroyWindow.Call(traceAnalysisHwnd)
			return
		}
	}
	base := old.Context
	if old.UnfilteredContext != "" {
		base = old.UnfilteredContext
	}
	old.Context, old.UnfilteredContext = base, ""
	for i := range old.Targets {
		t := &old.Targets[i]
		t.Filtered = nil
		if compareTD != nil && t.HasGeometry {
			var file *segy.File
			var g *segy.GeometryIndex
			if t.Path == compareAPath {
				file, g = compareTD.fa, compareTD.ga
			} else {
				file, g = compareTD.fb, compareTD.gb
			}
			if file != nil && g != nil {
				if trace, ok := g.TraceAt(t.Inline, t.Crossline); ok {
					marker := t.MarkerSample
					if t.Role == "B" && compareMode != 2 {
						offset, _ := generatedPoststackSourceStart(t.Path)
						marker += offset
					}
					if target, err := compareAnalysisTarget(t.Role, file, trace, marker, t.Inline, t.Crossline); err == nil {
						*t = target
					}
				}
			}
		}
	}
	traceAnalysisSelectionCurrent = attachViewFilterTraceSelection(old)
	startTraceAnalysisLoad()
}
