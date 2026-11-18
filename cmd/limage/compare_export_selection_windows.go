//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const (
	compareExportA = iota
	compareExportB
	compareExportDifference
)

const (
	compareExportVisible = iota
	compareExportLine
	compareExportAll
	compareExportRectangle
	compareExportManual
)

var compareExportNames = [...]string{"A", "B", "A−B"}
var compareExportScopeNames = [...]string{"当前可见范围", "当前完整剖面", "对象全部可用范围", "矩形选区", "手动范围"}

type compareExportSource struct {
	path         string
	identity     os.FileInfo
	geometry     *segy.GeometryIndex // immutable after geometry publication
	physical     [3]int64
	dt, samples  int
	sourceOrder  []int64
	description  string
	originalStem string
	processTag   string
}

type compareExportBounds struct {
	x0, x1, t0, t1 float64 // original spatial coordinate; absolute microseconds
}

type compareExportCapture struct {
	key                     viewFilterKey
	settings                [2]SideFilterState
	sources                 [2]compareExportSource
	frame                   *viewFilterSnapshot
	spatial                 compareExportBounds
	rectangle               *compareExportBounds
	hasGeometry, traceOrder bool
	axis, axisUnits         string
	actualLine              int32
}

type compareExportSelection struct {
	kind, scope int
	effective   bool
	manual      compareExportBounds
}

type compareExportPick struct {
	trace  int64
	il, xl int32
	mapped bool
}

type compareExportRow struct {
	pick  compareExportPick
	trace [2]segy.NumericalExportTrace
}

type compareExportPlan struct {
	request                 compareExportSelection
	capture                 compareExportCapture
	rows                    []compareExportRow
	samples, dt, headerSide int
	t0, t1                  int64
	first, last             int32
	il0, il1, xl0, xl1      int32
	mapped                  int
	size                    int64
	summary, provenance     string
}

func numericExportKey() viewFilterKey {
	k := viewFilterCurrentKey()
	k.render = 0 // palette, DPI and raster geometry do not identify data
	return k
}

func captureNumericalCompareExport() (compareExportCapture, error) {
	c := compareExportCapture{key: numericExportKey(), settings: viewFilter.active}
	d := compareTD
	if d == nil || d.fa == nil || d.ga == nil || poststackCompareInputPreparing() {
		return c, errors.New("请先打开 A，并等待当前剖面加载完成")
	}
	if c.key.mode == 2 {
		return c, errors.New("Time Slice 是平面切片；请切换到 Inline 或 Crossline 后导出时间剖面")
	}
	c.hasGeometry, c.traceOrder = compareDifferenceGeometryKnown(d), d.traceOrderFallback
	c.axis, c.axisUnits = "Inline", "Crossline"
	if c.key.mode == 1 {
		c.axis, c.axisUnits = "Crossline", "Inline"
	}
	if c.traceOrder {
		c.axis, c.axisUnits = "道序剖面", "道序坐标"
	}
	for i, f := range [2]*segy.File{d.fa, d.fb} {
		if f == nil || (i == 1 && (c.key.b == "" || c.key.b != f.Info.Path || d.gb == nil)) {
			continue
		}
		id, err := f.SourceFileInfo()
		if err != nil {
			if i == 0 {
				return c, err
			}
			continue
		}
		g := d.ga
		if i == 1 {
			g = d.gb
		}
		tr0, tr1, step := compareTraceRangeForGeometry(f)
		root := f.NumericalOriginalStem()
		if root == "" {
			root = numericalExportInputStem(f.Info.Path)
		}
		c.sources[i] = compareExportSource{path: f.Info.Path, identity: id, geometry: g, originalStem: root,
			physical: [3]int64{tr0, tr1, step}, dt: f.Info.SampleIntervalUS, samples: f.Info.SamplesPerTrace}
		if i == 1 {
			c.sources[i].sourceOrder = generatedPoststackTraceOrder(f.Info.Path)
			c.sources[i].description = compareExportBDescription(f.Info.Path)
			if tag, fromA := compareExportGeneratedTag(f.Info.Path, d.fa.Info.Path, f.Info.SampleIntervalUS); fromA {
				c.sources[i].originalStem = c.sources[0].originalStem
				c.sources[i].processTag = tag
			}
		}
	}
	// Use the un-decimated physical line used by formal processing, not a
	// rendering panel. Its first selected trace is the section time reference.
	tr, _, line, err := compareExportLineTraces(c, 0, compareExportVisible, compareExportBounds{
		x0: math.Float64frombits(c.key.x0), x1: math.Float64frombits(c.key.x1),
	})
	if err != nil {
		// An empty viewport should not prevent selecting the complete line
		// or the whole object. The initial visible request still reports it.
		tr, _, line, err = compareExportLineTraces(c, 0, compareExportLine, compareExportBounds{})
	}
	if err != nil {
		return c, err
	}
	origin, err := d.fa.TraceTimeOriginUS(tr[0])
	if err != nil {
		return c, err
	}
	c.actualLine = line
	lo, hi := max(0, c.key.first), c.key.last
	if hi < 0 {
		hi = c.sources[0].samples - 1
	}
	hi = min(hi, c.sources[0].samples-1)
	t := func(sample int) float64 { return float64(origin) + float64(sample)*float64(c.sources[0].dt) }
	c.spatial = compareExportBounds{math.Float64frombits(c.key.x0), math.Float64frombits(c.key.x1), t(lo), t(hi)}
	for i := len(compareAnnotations) - 1; i >= 0; i-- {
		a := compareAnnotations[i]
		if a.Mode == c.key.mode && a.Kind == 0 && a.Line == line && a.Source == c.key.a && a.Geometry == c.key.geometry {
			c.rectangle = &compareExportBounds{min(a.X0, a.X1), max(a.X0, a.X1), tFloat(origin, c.sources[0].dt, min(a.Y0, a.Y1)), tFloat(origin, c.sources[0].dt, max(a.Y0, a.Y1))}
			break
		}
	}
	if frame := viewFilter.frame; frame != nil {
		k := frame.key
		k.render = 0
		if k == c.key && frame.settings == c.settings {
			c.frame = frame
		}
	}
	return c, nil
}

func compareExportGeneratedTag(path, aPath string, dt int) (string, bool) {
	generatedPoststackMu.RLock()
	defer generatedPoststackMu.RUnlock()
	for _, r := range generatedPoststackResults {
		if r.retired.Load() || !r.ownsBProduct(path) || !strings.EqualFold(r.Section.SourcePath, aPath) {
			continue
		}
		if r.Algorithm == poststack.BandpassAlgorithmID {
			if parameters, err := poststack.BandpassParameters(r.Record.Parameters, dt); err == nil {
				return numericalExportFilterTag(parameters), true
			}
		}
		tag := r.Algorithm
		if i := strings.LastIndexAny(tag, "./"); i >= 0 && i+1 < len(tag) {
			tag = tag[i+1:]
		}
		return cleanNumericalExportStem(tag, 16), true
	}
	return "", false
}

func compareExportBDescription(path string) string {
	generatedPoststackMu.RLock()
	defer generatedPoststackMu.RUnlock()
	for _, r := range generatedPoststackResults {
		if !r.retired.Load() && r.ownsBProduct(path) {
			kind := "正式生成"
			if r.Preview {
				kind = "临时预览"
			}
			return kind + "：" + r.displayName()
		}
	}
	return "已加载文件"
}

func tFloat(origin int64, dt int, sample float64) float64 {
	return float64(origin) + float64(dt)*sample
}

func compareExportLineTraces(c compareExportCapture, side, scope int, bounds compareExportBounds) ([]int64, []int32, int32, error) {
	s := c.sources[side]
	if s.geometry == nil {
		return nil, nil, 0, fmt.Errorf("%c 未加载或几何尚未就绪", 'A'+side)
	}
	tr, coords, line, err := s.geometry.NumericLineTraceNumbers(c.key.mode, c.key.line)
	if err != nil {
		return nil, nil, line, err
	}
	if scope == compareExportVisible {
		p := s.physical
		if p[1] < p[0] {
			tr, coords = nil, nil
		} else {
			tr, coords = filterPhysicalTraceRange(tr, coords, p[0], p[1], p[2])
		}
	}
	if scope != compareExportLine {
		tr, coords = filterLineRange(tr, coords, bounds.x0, bounds.x1)
	}
	if len(tr) == 0 {
		return nil, nil, line, fmt.Errorf("%c 在请求的空间范围内没有真实道", 'A'+side)
	}
	return tr, coords, line, nil
}

func compareExportPicks(ctx context.Context, c compareExportCapture, side, scope int, bounds compareExportBounds, count int64) ([]compareExportPick, int32, error) {
	if scope != compareExportAll {
		tr, coords, line, err := compareExportLineTraces(c, side, scope, bounds)
		if err != nil {
			return nil, line, err
		}
		out := make([]compareExportPick, len(tr))
		for i, id := range tr {
			p := compareExportPick{id, line, coords[i], true}
			if c.key.mode == 1 {
				p.il, p.xl = p.xl, p.il
			}
			out[i] = p
		}
		return out, line, nil
	}
	if count > int64(maximumNumericalExportRows) {
		return nil, 0, fmt.Errorf("该对象包含 %d 道，超过本次导出元数据索引上限 %d；请按剖面或范围导出", count, maximumNumericalExportRows)
	}
	out := make([]compareExportPick, int(count))
	for i := range out {
		out[i].trace = int64(i)
	}
	g := c.sources[side].geometry
	for i, tr := range g.TraceNumbers {
		if i%2048 == 0 && ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		if tr < 0 || tr >= count || i >= len(g.RowOfTrace) || i >= len(g.ColOfTrace) {
			continue
		}
		il, xl := int(g.RowOfTrace[i]), int(g.ColOfTrace[i])
		if il >= 0 && il < len(g.InlineValues) && xl >= 0 && xl < len(g.CrosslineValues) {
			out[tr] = compareExportPick{tr, g.InlineValues[il], g.CrosslineValues[xl], true}
		}
	}
	return out, 0, nil
}

const maximumNumericalExportRows = 4_000_000 // bounded plan memory, independent of sample count

func compareExportPairs(a, b []compareExportPick, originalB []int64) ([][2]compareExportPick, bool) {
	lookup := make(map[[2]int32]int, len(b))
	base := make(map[int64]int, len(originalB))
	seen := make(map[[2]int32]int, len(a))
	for i, v := range b {
		if v.mapped {
			key := [2]int32{v.il, v.xl}
			if _, exists := lookup[key]; exists {
				lookup[key] = -1
			} else {
				lookup[key] = i
			}
		}
		if v.trace >= 0 && v.trace < int64(len(originalB)) {
			base[originalB[v.trace]] = i
		}
	}
	for _, v := range a {
		seen[[2]int32{v.il, v.xl}]++
	}
	rows := make([][2]compareExportPick, 0, min(len(a), len(b)))
	ambiguous := false
	for _, v := range a {
		key := [2]int32{v.il, v.xl}
		var i int
		var ok bool
		if len(originalB) != 0 {
			// Host-created B records the original physical A identity. This is
			// stronger than a potentially duplicated IL/XL number.
			i, ok = base[v.trace]
		} else if v.mapped {
			i, ok = lookup[key]
			if ok && (i < 0 || seen[key] != 1) {
				ambiguous = true
				continue
			}
		}
		if ok && b[i].il == v.il && b[i].xl == v.xl && b[i].mapped == v.mapped {
			rows = append(rows, [2]compareExportPick{v, b[i]})
		}
	}
	return rows, ambiguous
}

func planNumericalCompareExport(ctx context.Context, c compareExportCapture, q compareExportSelection) (compareExportPlan, error) {
	p := compareExportPlan{request: q, capture: c, t0: math.MaxInt64, t1: math.MinInt64, first: math.MaxInt32, last: math.MinInt32,
		il0: math.MaxInt32, il1: math.MinInt32, xl0: math.MaxInt32, xl1: math.MinInt32}
	if q.kind < 0 || q.kind >= len(compareExportNames) || q.scope < 0 || q.scope >= len(compareExportScopeNames) {
		return p, errors.New("导出对象或范围无效")
	}
	bounds := c.spatial
	rangeRestricted := q.scope == compareExportVisible || q.scope == compareExportRectangle || q.scope == compareExportManual
	switch q.scope {
	case compareExportRectangle:
		if c.rectangle == nil {
			return p, errors.New("当前线没有有效矩形选区")
		}
		bounds = *c.rectangle
	case compareExportManual:
		bounds = q.manual
	}
	if q.kind == compareExportB {
		p.headerSide = 1
	}
	if q.kind == compareExportDifference && !c.hasGeometry {
		return p, errors.New("A/B 缺少可核实的共同几何，差值不可用")
	}
	var files [2]*segy.File
	var picks [2][]compareExportPick
	var lines [2]int32
	for i, s := range c.sources {
		if (i == 0 && q.kind == compareExportB) || (i == 1 && q.kind == compareExportA) {
			continue
		}
		if s.path == "" || s.geometry == nil {
			return p, fmt.Errorf("%c 未加载或几何尚未就绪", 'A'+i)
		}
		f, err := openPoststackCompareSource(s.path)
		if err != nil {
			return p, err
		}
		defer f.Close()
		files[i] = f
		if _, err = viewFilterSourceIdentity(f, s.path, s.identity); err != nil {
			return p, err
		}
		picks[i], lines[i], err = compareExportPicks(ctx, c, i, q.scope, bounds, f.Info.TraceCount)
		if err != nil {
			return p, err
		}
	}
	p.dt = files[p.headerSide].Info.SampleIntervalUS
	if p.dt <= 0 {
		return p, errors.New("采样间隔无效")
	}
	if q.kind == compareExportDifference && (files[1].Info.SampleIntervalUS != p.dt || (q.scope != compareExportAll && lines[1] != lines[0])) {
		return p, errors.New("A/B 的真实剖面线或采样间隔不一致，不能导出差值")
	}
	var pairs [][2]compareExportPick
	if q.kind == compareExportDifference {
		var amb bool
		pairs, amb = compareExportPairs(picks[0], picks[1], c.sources[1].sourceOrder)
		if amb {
			return p, errors.New("独立 A/B 存在重复几何号，不能仅凭编号确认真实道配对")
		}
		if len(pairs) == 0 {
			return p, errors.New("没有可核实的共同物理道，差值不可用")
		}
	} else {
		picks := picks[p.headerSide]
		pairs = make([][2]compareExportPick, len(picks))
		for i, pick := range picks {
			pairs[i][p.headerSide] = pick
		}
	}
	if q.scope == compareExportLine && q.kind != compareExportA && len(c.sources[1].sourceOrder) != 0 {
		a, _, _, e := compareExportLineTraces(c, 0, compareExportLine, bounds)
		if e == nil && (len(pairs) < len(a) || c.sources[1].samples < c.sources[0].samples || lines[1] != c.actualLine) {
			return p, errors.New("当前生成的 B 仅覆盖局部剖面。要导出完整处理剖面，请先处理完整范围；也可选择“对象全部可用范围”导出已生成部分")
		}
	}
	if len(pairs) > maximumNumericalExportRows {
		return p, errors.New("导出道数超过元数据索引上限")
	}
	p.rows = make([]compareExportRow, 0, len(pairs))
	for _, pair := range pairs {
		if err := ctx.Err(); err != nil {
			return p, err
		}
		row := compareExportRow{pick: pair[p.headerSide]}
		var origin [2]int64
		var n [2]int
		for i, f := range files {
			if f == nil {
				continue
			}
			id := pair[i].trace
			hdr, err := f.ReadTraceHeader(id)
			if err != nil {
				return p, err
			}
			origin[i], err = f.TraceTimeOriginUS(id)
			if err != nil {
				return p, err
			}
			n[i] = f.Info.SamplesPerTrace
			if hdr.SampleCount > 0 {
				n[i] = min(n[i], hdr.SampleCount)
			}
			if hdr.SampleIntervalUS != 0 && hdr.SampleIntervalUS != p.dt {
				return p, fmt.Errorf("%c 道 %d 的头内采样间隔不同，不能写成固定采样轴", 'A'+i, id+1)
			}
			row.trace[i].Trace = id
		}
		s := p.headerSide
		left, right := origin[s], origin[s]+int64(n[s]-1)*int64(p.dt)
		if q.kind == compareExportDifference {
			if (origin[0]-origin[1])%int64(p.dt) != 0 {
				return p, fmt.Errorf("A/B 的真实时间起点未对齐到同一采样网格（A 道 %d）", pair[0].trace+1)
			}
			left, right = max(left, origin[1]), min(right, origin[1]+int64(n[1]-1)*int64(p.dt))
		}
		if left > right {
			continue
		}
		timeMin, timeMax := float64(left), float64(right)
		if rangeRestricted {
			timeMin, timeMax = max(timeMin, bounds.t0), min(timeMax, bounds.t1)
		}
		first, last, ok := segy.ExportTimeIndices(timeMin, timeMax, origin[s], p.dt, n[s])
		if !ok {
			continue
		}
		start, end := origin[s]+int64(first)*int64(p.dt), origin[s]+int64(last)*int64(p.dt)
		if start%1000 != 0 || start/1000 < math.MinInt16 || start/1000 > math.MaxInt16 {
			return p, fmt.Errorf("道 %d 的 %.9g ms 无法用 SEG-Y Delay 精确表示；请选择可表示的起点", row.pick.trace+1, float64(start)/1000)
		}
		for i, f := range files {
			if f == nil {
				continue
			}
			row.trace[i].First = int((start - origin[i]) / int64(p.dt))
			if q.effective && c.settings[i].Enabled {
				if c.frame == nil {
					return p, fmt.Errorf("%c 的当前滤波结果尚未就绪；请等待并重新打开导出", 'A'+i)
				}
				r := &c.frame.rows[i]
				a := row.trace[i]
				if _, err := r.read(a.Trace, a.First, a.First+last-first); err != nil {
					return p, fmt.Errorf("%c 的已缓存处理结果不覆盖请求范围。请生成对应范围的结果、缩小范围，或明确选择基础数据", 'A'+i)
				}
			}
		}
		ns := last - first + 1
		if p.samples == 0 {
			p.samples = ns
		} else if p.samples != ns {
			return p, errors.New("真实时间交集产生不同的逐道样点数；当前固定长度 SEG-Y 不填零或删道，请缩小时间范围")
		}
		p.t0, p.t1 = min(p.t0, start), max(p.t1, end)
		if row.pick.mapped {
			p.mapped++
			across := row.pick.xl
			if c.key.mode == 1 {
				across = row.pick.il
			}
			p.first, p.last = min(p.first, across), max(p.last, across)
			p.il0, p.il1, p.xl0, p.xl1 = min(p.il0, row.pick.il), max(p.il1, row.pick.il), min(p.xl0, row.pick.xl), max(p.xl1, row.pick.xl)
		}
		p.rows = append(p.rows, row)
	}
	if len(p.rows) == 0 {
		return p, errors.New("请求范围与实际数据没有可导出的共同样点")
	}
	if len(p.rows) != len(pairs) && q.kind != compareExportDifference {
		return p, errors.New("请求的时间窗口不覆盖部分所选道；不会静默删道，请调整为这些道共有的时间范围")
	}
	p.size = files[p.headerSide].Info.DataStart + 3200 + int64(len(p.rows))*int64(240+p.samples*8)
	phase := "基础数据（保留 A/B 自身来源）"
	stageA, stageB := "基准", "基准"
	if c.sources[1].description != "" {
		stageB += "（" + c.sources[1].description + "）"
	}
	if q.effective {
		phase = "当前有效结果"
		for i, name := range []*string{&stageA, &stageB} {
			if c.settings[i].Enabled {
				*name += " + 视图滤波：" + c.settings[i].Params.Summary()
			}
		}
	}
	state := "A：" + stageA
	if q.kind == compareExportB {
		state = "B：" + stageB
	} else if q.kind == compareExportDifference {
		state += "；B：" + stageB
	}
	p.provenance = fmt.Sprintf("%s; scope %d; stage selected=%t; filters %s; A=%s; B=%s; file version %d/%d; dt=%d us; ns=%d; traces=%d; nonfinite=NaN",
		[]string{"A", "B", "A-B"}[q.kind], q.scope, q.effective, state,
		filepath.Base(c.sources[0].path), filepath.Base(c.sources[1].path), c.key.aVersion, c.key.bVersion, p.dt, p.samples, len(p.rows))
	actual := fmt.Sprintf("%s %d · %s %d–%d", c.axis, lines[p.headerSide], c.axisUnits, p.first, p.last)
	if q.scope == compareExportAll {
		actual = fmt.Sprintf("Inline %d–%d · Crossline %d–%d", p.il0, p.il1, p.xl0, p.xl1)
		if p.mapped < len(p.rows) {
			actual += fmt.Sprintf(" · %d 道无有效几何（按物理道保留）", len(p.rows)-p.mapped)
			if p.mapped == 0 {
				actual = fmt.Sprintf("物理道 1–%d（无可用几何）", len(p.rows))
			}
		}
	}
	if len(c.sources[1].sourceOrder) > 0 && q.kind != compareExportA {
		actual += " · B 为已生成覆盖"
	}
	request := compareExportScopeNames[q.scope]
	if rangeRestricted {
		request += fmt.Sprintf(" · %s %.9g–%.9g · %.9g–%.9g ms", c.axisUnits, bounds.x0, bounds.x1, bounds.t0/1000, bounds.t1/1000)
	}
	limits := ""
	if q.scope == compareExportManual {
		_, coords, _, e := compareExportLineTraces(c, p.headerSide, compareExportLine, bounds)
		if e == nil && len(coords) != 0 {
			firstSource, lastSource := coords[0], coords[len(coords)-1]
			limits = fmt.Sprintf("\r\n当前线可用空间：%s %d–%d；时间实际交集见上方。", c.axisUnits, firstSource, lastSource)
		}
	}
	p.summary = fmt.Sprintf("对象：%s · %s（%s）\r\n请求：%s\r\n实际：%s · %.9g–%.9g ms\r\n%d 道 × %d 样点/道 · t₀ %.9g ms · dt %.6g ms · IEEE float64 SEG-Y · 约 %.2f MiB",
		compareExportNames[q.kind], phase, state, request, actual, float64(p.t0)/1000, float64(p.t1)/1000,
		len(p.rows), p.samples, float64(p.t0)/1000, float64(p.dt)/1000, float64(p.size)/(1<<20)) + limits
	return p, nil
}

func executeNumericalCompareExport(ctx context.Context, path string, p compareExportPlan, progress func(int, int)) error {
	return executeNumericalCompareExportWithTarget(ctx, path, p, nil, progress)
}

func executeNumericalCompareExportWithTarget(ctx context.Context, path string, p compareExportPlan, replace os.FileInfo, progress func(int, int)) error {
	var files [2]*segy.File
	q, c := p.request, p.capture
	if replace != nil {
		for _, source := range c.sources {
			if source.identity != nil && os.SameFile(replace, source.identity) {
				return errors.New("输出文件不能覆盖当前输入 A 或 B")
			}
		}
	}
	for i, s := range c.sources {
		if (i == 0 && q.kind == compareExportB) || (i == 1 && q.kind == compareExportA) {
			continue
		}
		f, err := openPoststackCompareSource(s.path)
		if err != nil {
			return err
		}
		defer f.Close()
		files[i] = f
		if _, err := viewFilterSourceIdentity(f, s.path, s.identity); err != nil {
			return err
		}
	}
	checkCurrent := func() error {
		for i, f := range files {
			if f != nil {
				if _, err := viewFilterSourceIdentity(f, c.sources[i].path, c.sources[i].identity); err != nil {
					return err
				}
			}
		}
		return nil
	}
	read := func(side int, t segy.NumericalExportTrace) ([]float64, error) {
		if q.effective && c.settings[side].Enabled {
			return c.frame.rows[side].read(t.Trace, t.First, t.First+p.samples-1)
		}
		return files[side].ReadTraceWindow(t.Trace, t.First, t.First+p.samples-1)
	}
	traces := make([]segy.NumericalExportTrace, len(p.rows))
	for i, row := range p.rows {
		traces[i] = row.trace[p.headerSide]
	}
	source := c.sources[p.headerSide]
	il, xl := 0, 0
	if source.geometry != nil {
		il, xl = source.geometry.InlineByte, source.geometry.CrosslineByte
	}
	traceOrder := source.geometry != nil && il == 0 && xl == 0
	return files[p.headerSide].ExportNumericalSection(ctx, path, segy.NumericalSectionExportOptions{
		Traces: traces, SampleCount: p.samples, Provenance: p.provenance, Progress: progress, CheckCurrent: checkCurrent,
		GeometryInlineByte: il, GeometryCrosslineByte: xl, TraceOrder: traceOrder, OriginalStem: source.originalStem, ReplaceExisting: replace,
		ReadValues: func(i int) ([]float64, error) {
			row := p.rows[i]
			a, err := read(p.headerSide, row.trace[p.headerSide])
			if err != nil || q.kind != compareExportDifference {
				return a, err
			}
			b, err := read(1, row.trace[1])
			if err != nil {
				return nil, err
			}
			out := make([]float64, p.samples)
			for j, v := range a {
				out[j] = v - b[j]
			}
			return out, nil
		},
	})
}

func parseCompareExportBounds(text [4]string) (compareExportBounds, error) {
	var p [4]float64
	for i, raw := range text {
		v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return compareExportBounds{}, fmt.Errorf("%s：请输入完整的有限数字", [4]string{"空间起点", "空间终点", "时间起点（ms）", "时间终点（ms）"}[i])
		}
		p[i] = v
	}
	if p[0] > p[1] {
		return compareExportBounds{}, errors.New("空间起点不能大于终点")
	}
	if p[2] > p[3] || math.Abs(p[2]) > 1e12 || math.Abs(p[3]) > 1e12 {
		return compareExportBounds{}, errors.New("时间起点不能晚于终点；时间单位为绝对毫秒")
	}
	return compareExportBounds{p[0], p[1], p[2] * 1000, p[3] * 1000}, nil
}
