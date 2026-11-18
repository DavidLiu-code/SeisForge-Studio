//go:build windows

package main

// UI-owned orchestration. Numerical/file workers receive copied requests and
// communicate through a bounded, generation-checked mailbox, never controls.
import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/registry"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/algorithms/runner"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/poststack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
)

type poststackViewStamp struct {
	Owner                                              uintptr
	Epoch, Dataset, Geometry, Render                   int64
	A, B                                               string
	Mode, First, Last, Width, Height                   int
	Line                                               int32
	TraceFirst, TraceLast, TraceStep                   int64
	InlineByte, CrosslineByte                          int
	TraceOrder                                         bool
	X0, X1, Gain, Clip, Minimum, Maximum               float64
	Palette                                            int
	AGC, Limits, Workspace                             bool
	Display                                            string
	Algorithm, ModuleFingerprint, ParameterFingerprint string
}

func capturePoststackViewStamp() poststackViewStamp {
	var rc RECT
	if compareHwnd != 0 {
		pGetClientRect.Call(compareHwnd, uintptr(unsafe.Pointer(&rc)))
	}
	stamp := poststackViewStamp{Owner: compareHwnd, Epoch: atomic.LoadInt64(&poststackEpoch),
		Dataset: atomic.LoadInt64(&compareGen), Geometry: atomic.LoadInt64(&compareGeomGen), Render: atomic.LoadInt64(&compareSliceGen),
		A: compareAPath, B: compareBPath, Mode: compareMode, First: compareSampleStart, Last: compareSampleEnd,
		Width: int(rc.Right - rc.Left), Height: int(rc.Bottom - rc.Top), Line: compareLineCoord,
		X0: compareLineXMin, X1: compareLineXMax, Gain: gainPercent, Clip: clipPercent, Minimum: limitMin, Maximum: limitMax,
		Palette: comparePaletteIndex, AGC: agc, Limits: useLimits, Workspace: compareWorkspaceActive, Display: fmt.Sprint(renderDisplayMode)}
	stamp.TraceFirst, stamp.TraceLast, stamp.TraceStep = compareTraceStart, compareTraceEnd, compareTraceStep
	if d := compareTD; d != nil {
		if d.fa != nil {
			stamp.TraceFirst, stamp.TraceLast, stamp.TraceStep = compareTraceRangeForGeometry(d.fa)
		}
		if d.ga != nil {
			stamp.InlineByte, stamp.CrosslineByte = d.ga.InlineByte, d.ga.CrosslineByte
		}
		stamp.TraceOrder = d.traceOrderFallback
	}
	if module, values, err := readAlgorithmParameters(); err == nil {
		stamp.Algorithm = module.Descriptor().ID
		stamp.ModuleFingerprint = module.Fingerprint()
		stamp.ParameterFingerprint = algorithmParameterFingerprint(values)
	}
	return stamp
}

// Algorithms receive the original samples and trace order, not the rendered
// frame. Compare's render/request generations, raster size, and display style
// can change (or be observed in a different DPI context) without changing it.
func poststackJobContextChange(before, now poststackViewStamp, export bool) string {
	sameBound := func(a, b float64) bool { return a == b || (math.IsNaN(a) && math.IsNaN(b)) }
	switch {
	case before.Owner != now.Owner || before.Epoch != now.Epoch || before.Workspace != now.Workspace:
		return "对比工作区"
	case before.A != now.A || before.B != now.B:
		return "A/B 文件"
	case before.InlineByte != now.InlineByte || before.CrosslineByte != now.CrosslineByte || before.TraceOrder != now.TraceOrder:
		return "道头映射"
	case before.Mode != now.Mode || before.Line != now.Line:
		return "剖面方向或线号"
	case before.First != now.First || before.Last != now.Last:
		return "时间窗"
	case before.TraceFirst != now.TraceFirst || before.TraceLast != now.TraceLast || before.TraceStep != now.TraceStep ||
		!sameBound(before.X0, now.X0) || !sameBound(before.X1, now.X1):
		return "道范围"
	case before.Algorithm != now.Algorithm || before.ModuleFingerprint != now.ModuleFingerprint || before.ParameterFingerprint != now.ParameterFingerprint:
		return "算法模块或参数"
	case export && before != now:
		return "导出上下文"
	default:
		return ""
	}
}

type generatedPoststackResult struct {
	Section      *poststack.SectionResult
	Algorithm    string
	DisplayName  string
	Line         int32
	Params       poststack.Params
	Name1, Name2 string
	At           time.Time
	Module       registry.Module
	Record       api.AlgorithmRunRecord
	RecordPath   string
	Preview      bool // temporary B; never a formal result-list entry
	retired      atomic.Bool
}

func (r *generatedPoststackResult) displayName() string {
	if r.DisplayName != "" {
		return r.DisplayName
	}
	return r.Algorithm
}

func (r *generatedPoststackResult) ownsBProduct(path string) bool {
	return r != nil && r.Section != nil && path != "" && r.Section.Part2 == path
}

func poststackResultStatus(r *generatedPoststackResult, stage string) string {
	name, list := "处理完成", "已加入正式结果"
	if r.Preview {
		name, list = "预览完成", "尚未加入正式结果"
	}
	return name + " · " + stage + " · " + list
}

func poststackResultScope(r *generatedPoststackResult) string {
	s := r.Section
	m := s.Metadata
	direction := "道序剖面"
	switch m.Direction {
	case "inline":
		direction = fmt.Sprintf("Inline %d", r.Line)
	case "crossline":
		direction = fmt.Sprintf("Crossline %d", r.Line)
	}
	ns := s.SampleEnd - s.SampleStart + 1
	scope := fmt.Sprintf("%s · %d 道 × %d 样点", direction, len(s.TraceIndices), ns)
	if m.SampleIntervalUS > 0 && ns > 0 {
		last := m.TimeOriginUS + int64(ns-1)*int64(m.SampleIntervalUS)
		scope += fmt.Sprintf(" · 首道 %.6g–%.6g ms", float64(m.TimeOriginUS)/1000, float64(last)/1000)
	}
	return scope
}

type poststackUIEvent struct {
	Source     *poststack.SectionResult // non-owning identity guard for export acceptance
	Generation int64
	Token      poststack.JobToken
	Stamp      poststackViewStamp
	Percent    int
	Message    string
	Done       bool
	Result     *generatedPoststackResult
	Err        error
	Export     bool
	Exported   []poststackOwnedExport
	Attempt    poststackAlgorithmAttempt
	worker     *poststackWorker
}

// Only the UI thread changes cancellation/close intent. The worker owns the
// completion channel and bounded log queue, and never touches HWND controls.
type poststackWorker struct {
	generation    int64
	owner         uintptr
	epoch         int64
	dialog        uintptr
	stamp         poststackViewStamp
	export        bool
	contextChange string // UI-owned explanation, retained until cleanup acknowledges cancellation
	completed     chan poststackUIEvent
	cancelling    bool
	closeAfter    bool
	closeCompare  bool
	quiet         bool
	logMu         sync.Mutex
	logs          []string
	recordPath    string
}

func (w *poststackWorker) log(message, recordPath string) {
	if w == nil {
		return
	}
	w.logMu.Lock()
	defer w.logMu.Unlock()
	if message != "" {
		const maxMessage = 4096
		if len(message) > maxMessage {
			end := maxMessage
			for end > 0 && !utf8.ValidString(message[:end]) {
				end--
			}
			message = message[:end] + "…"
		}
		if len(w.logs) >= 128 {
			copy(w.logs, w.logs[len(w.logs)-127:])
			w.logs = w.logs[:127]
		}
		w.logs = append(w.logs, message)
	}
	if recordPath != "" {
		w.recordPath = recordPath
	}
}

func flushPoststackWorkerLog(w *poststackWorker) {
	if w == nil {
		return
	}
	w.logMu.Lock()
	lines, path := w.logs, w.recordPath
	w.logs, w.recordPath = nil, ""
	w.logMu.Unlock()
	if len(lines) > 0 || path != "" {
		setAlgorithmLog(strings.Join(lines, "\r\n"), path)
	}
}

type poststackOwnedExport struct {
	Path     string
	Identity os.FileInfo
}

func (o poststackOwnedExport) remove() {
	if o.Identity == nil {
		return
	}
	if now, err := os.Stat(o.Path); err == nil && os.SameFile(o.Identity, now) {
		_ = os.Remove(o.Path)
	}
}

var (
	retiredPoststackMu         sync.Mutex
	retiredPoststackSections   []*poststack.SectionResult
	cadzowCancel               context.CancelFunc
	cadzowGen                  int64
	poststackEpoch             int64
	poststackProcessingSession = poststack.NewSession("poststack-compare")
	poststackMailboxMu         sync.Mutex
	poststackMailbox           *poststackUIEvent
	generatedPoststackMu       sync.RWMutex
	generatedPoststackResults  []*generatedPoststackResult
	activePoststackWorker      *poststackWorker          // UI-thread ownership only
	poststackPendingBDisplay   *generatedPoststackResult // UI-thread B prepare acknowledgement
)

// A background B-index/render worker may still own an independent Reader.
// Keep failed removals reachable until its close path retries them on Windows.
func retirePoststackSection(section *poststack.SectionResult) {
	if section == nil {
		return
	}
	retiredPoststackMu.Lock()
	defer retiredPoststackMu.Unlock()
	if err := section.Cleanup(); err == nil {
		return
	}
	for _, r := range retiredPoststackSections {
		if r == section {
			return
		}
	}
	retiredPoststackSections = append(retiredPoststackSections, section)
}

func retryRetiredPoststackResults() {
	retiredPoststackMu.Lock()
	defer retiredPoststackMu.Unlock()
	remaining := retiredPoststackSections[:0]
	for _, r := range retiredPoststackSections {
		if err := r.Cleanup(); err != nil {
			remaining = append(remaining, r)
		}
	}
	for i := len(remaining); i < len(retiredPoststackSections); i++ {
		retiredPoststackSections[i] = nil
	}
	retiredPoststackSections = remaining
}

func validatePoststackEventSource(e *poststackUIEvent) error {
	if e.Result != nil && e.Result.Section != nil {
		if e.Result.Module.Fingerprint() != "" {
			if err := e.Result.Module.Verify(); err != nil {
				return err
			}
		}
		return e.Result.Section.SourceUnchanged()
	}
	if e.Source != nil {
		return e.Source.SourceUnchanged()
	}
	return nil
}

func disposePoststackEvent(e *poststackUIEvent) {
	if e != nil && e.Result != nil && e.Result.Section != nil {
		retirePoststackSection(e.Result.Section)
	}
	if e != nil {
		for _, owned := range e.Exported {
			owned.remove()
		}
	}
}

func postPoststackEvent(e poststackUIEvent) {
	poststackMailboxMu.Lock()
	if e.Generation != atomic.LoadInt64(&cadzowGen) || !poststackProcessingSession.Accept(e.Token) {
		poststackMailboxMu.Unlock()
		disposePoststackEvent(&e)
		return
	}
	if poststackMailbox != nil && poststackMailbox.Done && !e.Done {
		poststackMailboxMu.Unlock()
		return
	}
	poststackMailbox = &e
	poststackMailboxMu.Unlock()
	posted, _, _ := pPostMessageW.Call(e.Stamp.Owner, WM_CADZOW_DONE, 0, 0)
	if posted == 0 {
		poststackMailboxMu.Lock()
		if poststackMailbox == &e {
			poststackMailbox = nil
		}
		poststackMailboxMu.Unlock()
		disposePoststackEvent(&e)
	}
}

func setPoststackTaskControls(running bool) {
	controls := []uintptr{cz.start, cz.preview, cz.algorithm, cz.timeout}
	for _, p := range cz.parameters {
		controls = append(controls, p.Value, p.End, p.Browse)
	}
	for _, h := range controls {
		if h != 0 {
			pEnableWindow.Call(h, uintptr(boolToInt(!running)))
		}
	}
	if cz.cancel != 0 {
		pEnableWindow.Call(cz.cancel, uintptr(boolToInt(running && !poststackTaskCancelling())))
	}
	refreshPoststackResultControls()
	updateCadzowAlgorithmUI()
}

func poststackTaskCancelling() bool {
	return activePoststackWorker != nil && activePoststackWorker.cancelling
}

func poststackTaskBusy() bool { return cadzowCancel != nil }

// Rendering the same view again is not an input change. Callers may avoid
// cancelling a running algorithm for harmless WM_SIZE/repaint notifications.
func poststackInputViewChanged() bool {
	w := activePoststackWorker
	if w == nil || w.cancelling {
		return false
	}
	w.contextChange = poststackJobContextChange(w.stamp, capturePoststackViewStamp(), w.export)
	return w.contextChange != ""
}

func beginPoststackJob() (context.Context, poststackUIEvent) {
	stamp := capturePoststackViewStamp()
	poststackProcessingSession.UpdateContext(poststack.JobToken{OwnerHWND: stamp.Owner, OwnerToken: "poststack-compare",
		WorkspaceGeneration: uint64(stamp.Epoch), DatasetGeneration: uint64(stamp.Epoch), BDatasetGeneration: uint64(stamp.Dataset),
		AMappingGeneration: uint64(stamp.Geometry), BMappingGeneration: uint64(stamp.Geometry), SelectionGeneration: uint64(stamp.Render),
		CompareGeneration: uint64(stamp.Dataset), RenderGeneration: uint64(stamp.Render), ResizeGeneration: uint64(stamp.Epoch),
		SampleFirst: stamp.First, SampleLast: stamp.Last, PanelWidth: stamp.Width, PanelHeight: stamp.Height, CompareMode: stamp.Mode})
	ctx, token := poststackProcessingSession.BeginJob()
	cadzowCancel = func() { poststackProcessingSession.Cancel() }
	e := poststackUIEvent{Generation: atomic.AddInt64(&cadzowGen, 1), Token: token, Stamp: stamp}
	activePoststackWorker = &poststackWorker{generation: e.Generation, owner: stamp.Owner, epoch: stamp.Epoch, dialog: cadzowHwnd, stamp: stamp, completed: make(chan poststackUIEvent, 1)}
	e.worker = activePoststackWorker
	algorithmFailure = algorithmFailureNotice{}
	resetAlgorithmLog()
	setPoststackTaskControls(true)
	setCompareBusy(1)
	setCompareProgress(0, true)
	return ctx, e
}

func cancelCadzowTask() {
	atomic.AddInt64(&cadzowGen, 1)
	if cadzowCancel != nil {
		cadzowCancel()
		if activePoststackWorker != nil {
			activePoststackWorker.cancelling = true
			activePoststackWorker.quiet = true
		} else {
			// Compatibility for cancellation-only callers with no owned worker.
			cadzowCancel = nil
			setCompareBusy(-1)
			setCompareProgress(0, false)
		}
	}
	poststackMailboxMu.Lock()
	pending := poststackMailbox
	poststackMailbox = nil
	poststackMailboxMu.Unlock()
	disposePoststackEvent(pending)
	setPoststackTaskControls(cadzowCancel != nil)
	pollPoststackWorkerCompletion()
}

// Cancel acknowledges only after the worker's process, Readers and temporary
// capture have finished cleanup. Closing the dialog is deferred to that ack;
// no UI thread waits on the external process.
func requestPoststackCancel(closeWindow bool) {
	traceAlgorithmWindowLayout("请求取消")
	if activePoststackWorker == nil {
		cancelCadzowTask()
		if closeWindow {
			finishAlgorithmDialogClose()
		}
		return
	}
	w := activePoststackWorker
	w.cancelling = true
	w.closeAfter = w.closeAfter || closeWindow
	atomic.AddInt64(&cadzowGen, 1)
	if cadzowCancel != nil {
		cadzowCancel()
	}
	poststackMailboxMu.Lock()
	pending := poststackMailbox
	poststackMailbox = nil
	poststackMailboxMu.Unlock()
	disposePoststackEvent(pending)
	setPoststackTaskControls(true)
	setAlgorithmStatus("正在取消，等待算法进程和临时文件释放…", -1)
	pollPoststackWorkerCompletion()
}

// Compare must keep its message loop alive until the worker acknowledges that
// the process tree, Readers and temporary directories have been released.
// Returning true tells WM_CLOSE to defer destroying the owner window.
func requestPoststackCompareClose(owner uintptr) bool {
	w := activePoststackWorker
	if w == nil || w.owner != owner {
		return false
	}
	w.closeCompare = true
	w.cancelling = true
	w.quiet = true
	cancelCadzowTask()
	return true
}

// Called by the worker only AFTER all of its deferred cleanup has run. Unlike
// progress, this channel deliberately survives generation invalidation so the
// UI can acknowledge cancellation without accepting the cancelled result.
var (
	poststackIsWindow        = user32.NewProc("IsWindow")
	poststackIsWindowVisible = user32.NewProc("IsWindowVisible")
)

func completePoststackWorker(e poststackUIEvent) {
	e.Done = true
	if e.worker == nil {
		postPoststackEvent(e)
		return
	}
	e.worker.completed <- e
	var posted uintptr
	// A null HWND posts to a thread queue and HWND_BROADCAST reaches unrelated
	// windows; neither can acknowledge ownership of this task's result.
	if exists, _, _ := poststackIsWindow.Call(e.Stamp.Owner); exists != 0 {
		posted, _, _ = pPostMessageW.Call(e.Stamp.Owner, WM_CADZOW_DONE, 0, 0)
	}
	if posted == 0 {
		// The owner may have closed. No result ownership is left waiting for a
		// message that can no longer be delivered; cleanup is idempotent.
		select {
		case pending := <-e.worker.completed:
			disposePoststackEvent(&pending)
			pending.Result, pending.Exported = nil, nil
			pending.Err = errors.Join(pending.Err, fmt.Errorf("处理窗口已关闭，结果未加载"))
			e.worker.completed <- pending
		default:
			// The UI has already consumed/validated this completion.
		}
	}
}

// UI-thread poll also handles a completion whose owner window has been closed.
// Reopening the parameter window may call this before enabling its controls.
func pollPoststackWorkerCompletion() bool {
	w := activePoststackWorker
	if w == nil {
		return false
	}
	var e poststackUIEvent
	select {
	case e = <-w.completed:
	default:
		return false
	}
	activePoststackWorker = nil
	currentOwner := w.owner == compareHwnd && w.epoch == atomic.LoadInt64(&poststackEpoch)
	if w.cancelling {
		traceAlgorithmWindowLayout("取消完成")
		disposePoststackEvent(&e)
		if cadzowCancel != nil {
			cadzowCancel = nil
			if w.owner == compareHwnd {
				setCompareBusy(-1)
				setCompareProgress(0, false)
			}
		}
		setPoststackTaskControls(false)
		if currentOwner {
			flushPoststackWorkerLog(w)
			if !w.quiet {
				status := "已取消；A 和原有 B 保持不变"
				if e.Err != nil && !errors.Is(e.Err, context.Canceled) {
					status += "；清理提示：" + e.Err.Error()
				}
				setAlgorithmStatus(status, 0)
			}
		}
		if w.closeAfter && w.owner == compareHwnd && w.dialog == cadzowHwnd {
			finishAlgorithmDialogClose()
		}
		if w.closeCompare && w.owner == compareHwnd {
			// The second WM_CLOSE sees no active worker and can safely destroy
			// Compare/the hidden main owner without terminating cleanup early.
			if exists, _, _ := poststackIsWindow.Call(w.owner); exists != 0 {
				pPostMessageW.Call(w.owner, WM_CLOSE, 0, 0)
			}
		} else if (w.quiet || !currentOwner) && cadzowHwnd != 0 {
			showPoststackContextDiscard(w.stamp, w.contextChange)
			if visible, _, _ := poststackIsWindowVisible.Call(cadzowHwnd); visible != 0 {
				syncPoststackParameterContext()
			}
		}
		return true
	}
	if !currentOwner {
		disposePoststackEvent(&e)
		cadzowCancel = nil
		if w.owner == compareHwnd {
			setCompareBusy(-1)
			setCompareProgress(0, false)
			showPoststackContextDiscard(w.stamp, w.contextChange)
		}
		setPoststackTaskControls(false)
		return true
	}
	flushPoststackWorkerLog(w)
	handlePoststackCompletedEvent(&e)
	return true
}

// Flush only paint messages for the changed native controls. In particular do
// not pump the message loop during validation/authorization: that would allow
// a second Run or owner-close command to re-enter this preflight.
func setPoststackImmediateStatus(message string, percent int) {
	setAlgorithmStatus(message, percent)
	for _, h := range []uintptr{cz.status, cz.progress} {
		if h != 0 {
			pInvalidateRect.Call(h, 0, 0)
			pUpdateWindow.Call(h)
		}
	}
}

func startCadzowTask(preview bool) {
	pollPoststackWorkerCompletion()
	if preview {
		traceAlgorithmWindowLayout("请求预览")
	} else {
		traceAlgorithmWindowLayout("请求正式运行")
	}
	defer func() {
		if poststackTaskBusy() {
			traceAlgorithmWindowLayout("启动命令返回：有后台任务")
		} else {
			traceAlgorithmWindowLayout("启动命令返回：未提交任务")
		}
	}()
	if cadzowCancel != nil {
		if poststackTaskCancelling() {
			setAlgorithmStatus("正在取消，等待算法进程和临时文件释放…", -1)
		} else {
			setAlgorithmStatus("处理任务仍在运行，请等待完成或取消运行", -1)
		}
		return
	}
	setPoststackImmediateStatus("正在检查当前窗口和算法参数…", -1)
	if compareTD == nil || compareTD.fa == nil || compareTD.ga == nil || !compareWorkspaceActive {
		setAlgorithmStatus("请进入比并等待 A 图像完成", -1)
		return
	}
	if poststackCompareInputPreparing() {
		setAlgorithmStatus("正在准备 A/B 几何，请等待完成后再运行", -1)
		return
	}
	module, parameters, err := readAlgorithmParameters()
	if err != nil {
		setAlgorithmStatus(err.Error(), -1)
		return
	}
	if err = module.Verify(); err != nil {
		setAlgorithmStatus("模块发生变化，请刷新并重新授权："+err.Error(), -1)
		return
	}
	traces, _, actualLine := cadzowCurrentProfile()
	if len(traces) == 0 || compareMode == 2 {
		setAlgorithmStatus("请选择有有效道的 Inline 或 Crossline 时间剖面。", -1)
		return
	}
	ns := compareSampleEnd - compareSampleStart + 1
	attempt := newPoststackAlgorithmAttempt(module, parameters, len(traces), ns)
	var bandpass poststack.ViewBandpass
	if module.IsBuiltin() {
		if module.Descriptor().ID != poststack.BandpassAlgorithmID {
			setAlgorithmStatus("未知内置算法，未启动处理", -1)
			return
		}
		bandpass, err = poststack.BandpassParameters(parameters, compareTD.fa.Info.SampleIntervalUS)
		if err != nil {
			setAlgorithmStatus(err.Error(), -1)
			return
		}
		minimum, err := bandpass.MinSamples(compareTD.fa.Info.SampleIntervalUS)
		if err != nil {
			setAlgorithmStatus(err.Error(), -1)
			return
		}
		if compareTD.fa.Info.SamplesPerTrace < minimum {
			setAlgorithmStatus(fmt.Sprintf("当前滤波设计至少需要 %d 个真实上下文样点；实际 %d", minimum, compareTD.fa.Info.SamplesPerTrace), -1)
			return
		}
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(getText(cz.timeout)))
	if err != nil || minutes < 1 || minutes > 1440 {
		setAlgorithmStatus("任务超时必须为 1..1440 分钟", -1)
		return
	}
	if !module.IsBuiltin() && !poststackModuleTrust[module.Fingerprint()] {
		authorizationStamp, authorizationDialog := capturePoststackViewStamp(), cadzowHwnd
		setPoststackImmediateStatus("等待外部模块授权；尚未启动处理", -1)
		text := fmt.Sprintf("将运行你安装的外部模块：\n%s\n\nEXE SHA-256:\n%s\nManifest SHA-256:\n%s\n\n外部程序不是沙箱，可访问你的账户文件。只授权你信任且有权使用的模块。", module.ExecutablePath(), module.ExecutableSHA256(), module.ManifestSHA256())
		answer, _, _ := pMessageBoxW.Call(cadzowHwnd, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16("运行模块授权"))), MB_YESNO|0x20|0x100|0x10000) // default No; foreground owned authorization
		if answer != IDYES {
			setPoststackImmediateStatus("未授权外部模块；未启动处理", 0)
			return
		}
		if !poststackAuthorizationContextValid(authorizationStamp, authorizationDialog) {
			setPoststackImmediateStatus("授权期间输入或窗口上下文已变化，请重新运行；未启动处理", 0)
			return
		}
		poststackModuleTrust[module.Fingerprint()] = true
	}
	desc := module.Descriptor()
	sourceIdentity, err := compareTD.fa.SourceFileInfo()
	if err != nil {
		setAlgorithmStatus("A 身份不可用："+err.Error(), -1)
		return
	}
	ilByte, xlByte := compareTD.ga.InlineByte, compareTD.ga.CrosslineByte
	req := poststack.SectionRequest{SourcePath: compareAPath, TraceIndices: append([]int64(nil), traces...), SampleStart: compareSampleStart, SampleEnd: compareSampleEnd, AlgorithmID: desc.ID, Direction: "inline"}
	if compareMode == 1 {
		req.Direction = "crossline"
	}
	if compareTD.traceOrderFallback {
		req.Direction = "trace_order"
	}
	at := time.Now()
	name1, name2 := algorithmOutputNames(req.SourcePath, desc.ID, parameters, at)
	params := poststack.Params{Values: map[string]float64{}}
	for k, v := range parameters {
		switch n := v.(type) {
		case float64:
			params.Values[k] = n
		case int64:
			params.Values[k] = float64(n)
		}
	}
	ctx, event := beginPoststackJob()
	event.Attempt = attempt
	// The public task timeout replaces any algorithm-specific timeout choice.
	ctx, timeoutCancel := context.WithTimeout(ctx, time.Duration(minutes)*time.Minute)
	captureLabel := "捕获当前可见剖面"
	setAlgorithmStatus(captureLabel, 0)
	go func() {
		e := event
		defer func() {
			if e.Err == nil {
				e.Err = ctx.Err()
			}
			timeoutCancel()
			if e.Err != nil && e.Result != nil {
				disposePoststackEvent(&e)
				e.Result = nil
			}
			if e.Err != nil {
				e.worker.log(e.Err.Error(), "")
			}
			completePoststackWorker(e)
		}()
		lastPercent := 0
		report := func(p int, text string) {
			if p >= 0 {
				lastPercent = maxInt(lastPercent, minInt(p, 100))
			}
			e.worker.log(text, "")
			pr := event
			pr.Percent, pr.Message = lastPercent, text
			postPoststackEvent(pr)
		}
		tempDir, err := os.MkdirTemp("", "seisforge-algorithm-capture-")
		if err != nil {
			e.Err = err
			return
		}
		defer func() {
			if cleanupErr := os.RemoveAll(tempDir); cleanupErr != nil {
				e.Err = errors.Join(e.Err, fmt.Errorf("清理输入临时目录：%w", cleanupErr))
			}
		}()
		inputPath := filepath.Join(tempDir, "input.sfdata")
		currentIdentity, statErr := os.Stat(req.SourcePath)
		if statErr != nil || !os.SameFile(sourceIdentity, currentIdentity) || sourceIdentity.Size() != currentIdentity.Size() || !sourceIdentity.ModTime().Equal(currentIdentity.ModTime()) {
			e.Err = fmt.Errorf("A 在捕获前发生变化，请重新打开 A")
			return
		}
		input, err := poststack.CaptureAlgorithmSectionMapped(ctx, req, inputPath, ilByte, xlByte, func(done, total int) {
			if total > 0 {
				report(done*15/total, captureLabel)
			}
		})
		var result *poststack.SectionResult
		var record api.AlgorithmRunRecord
		var recordPath string
		if err == nil {
			request := api.RunRequest{ProtocolVersion: api.ProtocolVersion, RunID: fmt.Sprintf("poststack-%d-%d", at.UnixNano(), event.Generation), AlgorithmID: desc.ID, AlgorithmVersion: desc.Version, Parameters: parameters}
			progress := func(p api.ProgressEvent) {
				if p.Type == "log" {
					e.worker.log(p.Message, "")
					pr := event
					pr.Percent = -1
					postPoststackEvent(pr)
					return
				}
				message := p.Message
				if message == "" {
					switch p.Type {
					case "started":
						message = "算法进程已启动"
					case "progress":
						message = "算法处理中"
					case "result":
						message = "算法输出已生成，正在校验"
					}
				}
				report(poststackAlgorithmProgress(p), message)
			}
			options := runner.Options{AuthorizedFingerprint: module.Fingerprint(), Timeout: time.Duration(minutes) * time.Minute}
			var run *runner.Result
			var runErr error
			if module.IsBuiltin() {
				run, runErr = runner.RunBuiltin(ctx, module, request, inputPath, options,
					func(ctx context.Context, _ api.RunRequest, captured, output string, notify api.ProgressReporter) (runner.BuiltinExecutionInfo, error) {
						contextInfo, filterErr := input.WriteBandpassArtifact(ctx, captured, output, bandpass, func(done, total int) {
							if notify != nil && total > 0 {
								notify(api.ProgressEvent{Type: "progress", Value: float64(done) / float64(total), Message: "从基准 A 逐道时间滤波；完整时间上下文"})
							}
						})
						return runner.BuiltinExecutionInfo{InputContextFingerprint: contextInfo.Fingerprint, ProcessingScope: &contextInfo.Scope, VerifySource: input.SourceUnchanged}, filterErr
					}, progress)
			} else {
				run, runErr = runner.Run(ctx, module, request, inputPath, options, progress)
			}
			err = runErr
			if run != nil {
				defer func() {
					if cleanupErr := run.Cleanup(); cleanupErr != nil {
						e.Err = errors.Join(e.Err, fmt.Errorf("清理算法临时目录：%w", cleanupErr))
					}
				}()
				record = run.Record
				recordPath = run.RecordPath
				e.worker.log(run.RecordWarning, recordPath)
				if err == nil {
					err = input.SourceUnchanged()
				}
				if err == nil {
					err = module.Verify()
				}
				if err == nil {
					residual := ""
					for _, out := range run.Outputs {
						if out.Role == "residual" {
							residual = out.Path
							if !filepath.IsAbs(residual) {
								residual = filepath.Join(run.Directory, residual)
							}
							break
						}
					}
					report(90, "校验输出坐标、采样轴和真实道序")
					result, err = input.AcceptResult(ctx, run.Directory, run.OutputPath, residual)
					if err == nil {
						run.DetachDirectory()
					}
				}
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil && result == nil {
			err = fmt.Errorf("算法未返回可验证的 B 结果")
		}
		if err != nil && result != nil {
			if cleanupErr := result.Cleanup(); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("清理未接受的结果：%w", cleanupErr))
				retirePoststackSection(result)
			}
			result = nil
		}
		e.Err = err
		if result != nil {
			e.Result = &generatedPoststackResult{Section: result, Algorithm: desc.ID, DisplayName: attempt.Name, Line: actualLine,
				Params: params, Name1: name1, Name2: name2, At: at, Module: module, Record: record, RecordPath: recordPath, Preview: preview}
		}
	}()
}

func poststackAuthorizationContextValid(stamp poststackViewStamp, dialog uintptr) bool {
	return dialog == cadzowHwnd && compareTD != nil && compareTD.fa != nil && compareTD.ga != nil &&
		compareWorkspaceActive && !poststackCompareInputPreparing() && poststackJobContextChange(stamp, capturePoststackViewStamp(), false) == ""
}

// Log/result messages carry no percentage; treating their default Value=0 as
// progress would move an 80% task backwards to 15%.
func poststackAlgorithmProgress(p api.ProgressEvent) int {
	if p.Type != "progress" {
		return -1
	}
	return 15 + int(p.Value*70)
}

func handleCadzowDone() {
	if pollPoststackWorkerCompletion() {
		return
	}
	poststackMailboxMu.Lock()
	e := poststackMailbox
	poststackMailbox = nil
	poststackMailboxMu.Unlock()
	if e == nil {
		return
	}
	change := poststackJobContextChange(e.Stamp, capturePoststackViewStamp(), e.Export)
	if e.Generation != atomic.LoadInt64(&cadzowGen) || change != "" || !poststackProcessingSession.Accept(e.Token) {
		disposePoststackEvent(e)
		if e.Generation == atomic.LoadInt64(&cadzowGen) {
			if e.worker != nil && e.worker == activePoststackWorker {
				e.worker.contextChange = change
			}
			cancelCadzowTask()
		}
		return
	}
	if !e.Done {
		flushPoststackWorkerLog(e.worker)
		if e.Percent >= 0 {
			setCompareProgress(e.Percent, true)
		}
		if e.Message != "" {
			setAlgorithmStatus(e.Message, e.Percent)
		}
		return
	}
	handlePoststackCompletedEvent(e)
}

func handlePoststackCompletedEvent(e *poststackUIEvent) {
	change := poststackJobContextChange(e.Stamp, capturePoststackViewStamp(), e.Export)
	if e.Generation != atomic.LoadInt64(&cadzowGen) || change != "" || !poststackProcessingSession.Accept(e.Token) {
		disposePoststackEvent(e)
		if e.Generation == atomic.LoadInt64(&cadzowGen) || (e.worker != nil && activePoststackWorker == nil) {
			cancelCadzowTask()
			showPoststackContextDiscard(e.Stamp, change)
		}
		return
	}
	// The active job alone owns the busy count. A stale completion can never
	// reset the controls/cancellation function belonging to its successor.
	cadzowCancel = nil
	setCompareBusy(-1)
	setPoststackTaskControls(false)
	if e.Err == nil {
		e.Err = validatePoststackEventSource(e)
	}
	if e.Err == nil && !e.Export && (e.Result == nil || e.Result.Section == nil) {
		e.Err = errors.New("算法结束但没有可验证的 B 结果")
	}
	if e.Err != nil {
		setCompareProgress(0, false)
		traceAlgorithmWindowLayout("处理失败")
		if errors.Is(e.Err, context.Canceled) {
			poststackProcessingSession.Cancel()
		} else {
			// Fail already releases this job's cancellation handle. Calling the
			// generic cancel afterwards would overwrite StatusFailed with cancelled.
			poststackProcessingSession.Fail(e.Token)
		}
		disposePoststackEvent(e)
		retained := compareActiveB() && e.Stamp.B == compareBPath
		setPoststackAlgorithmFailure(e.Attempt, e.Err, retained, false, e.Export)
		return
	}
	setCompareProgress(100, false)
	if e.Export {
		poststackProcessingSession.Finish(e.Token)
		setAlgorithmStatus(e.Message, 100)
		setText(cc.status, e.Message)
		revealAlgorithmResults()
		return
	}
	r := e.Result
	if !acceptPoststackResultMetadata(e.Token, r) {
		disposePoststackEvent(e)
		return
	}
	generatedPoststackMu.Lock()
	generatedPoststackResults = append(generatedPoststackResults, r)
	generatedPoststackMu.Unlock()
	loadGeneratedPoststackResult(r)
	refreshPoststackResultChoices()
	refreshPoststackOutputPreview()
	setAlgorithmStatus(poststackResultStatus(r, "正在加载 B 剖面"), 100)
	revealAlgorithmResults()
}

// A discarded result must not silently turn the same input's panel back into
// "Ready". A new A, however, must never inherit the prior file's task status.
// The caller has already established that this event does not own a successor.
func showPoststackContextDiscard(stamp poststackViewStamp, change string) {
	if stamp.Owner != compareHwnd {
		return
	}
	if stamp.A == compareAPath {
		status := "输入上下文已变化"
		if change != "" {
			status += "（" + change + "）"
		}
		setAlgorithmStatus(status+"；本次处理已取消，A 和原有 B 保持不变", 0)
	} else {
		algorithmStatus, algorithmProgress, algorithmStatusSource = "", 0, compareAPath
		algorithmFailure = algorithmFailureNotice{}
		resetAlgorithmLog()
	}
}

func acceptPoststackResultMetadata(token poststack.JobToken, r *generatedPoststackResult) bool {
	if r == nil || r.Section == nil {
		return false
	}
	if r.Preview {
		return poststackProcessingSession.Finish(token)
	}
	s := r.Section
	return poststackProcessingSession.Complete(token, poststack.ResultRef{ID: r.Name2, AlgorithmID: r.Algorithm, Path: s.Part2, ResidualPath: s.Part1, SourcePath: s.SourcePath, SourceSize: s.SourceSize, SourceModTimeNS: s.SourceModTimeNS, TraceIndices: s.TraceIndices, Params: r.Params, Metadata: s.Metadata, Window: poststack.SampleWindow{First: s.SampleStart, Last: s.SampleEnd + 1}, CreatedAt: r.At})
}

func loadGeneratedPoststackResult(r *generatedPoststackResult) {
	if r == nil || r.Section == nil {
		return
	}
	loadGeneratedPoststackProduct(r, r.Section.Part2)
}

func loadGeneratedPoststackProduct(r *generatedPoststackResult, path string) {
	if !r.ownsBProduct(path) || r.retired.Load() {
		return
	}
	hadVisibleB, showDifference := compareActiveB(), compareShowDiff
	cancelCadzowTask()
	invalidatePoststackViewFilterB()
	invalidateCompareTimeData() // closes the old independent B Reader first
	retirePoststackPreviewsExcept(r)
	compareWorkspaceActive = true
	compareBPath = path
	poststackPendingBDisplay = r
	compareB, compareD = comparePanel{}, comparePanel{}
	compareBVisible = true
	compareShowDiff = !hadVisibleB || showDifference
	// B owns its capture window; it never redefines A's live viewport. A
	// history result can cover fewer traces/samples, so the renderer uses
	// the actual B coverage instead of enlarging or resetting A's selection.
	pSendMessageW.Call(cc.compare, BM_SETCHECK, BST_CHECKED, 0)
	pSendMessageW.Call(cc.diff, BM_SETCHECK, uintptr(boolToInt(compareShowDiff)), 0)
	layoutComparePathLabels()
	invalidateCompareBase()
	// The artifact was captured with A's current (possibly manual) mapping.
	// Re-detecting here would silently replace that mapping after generation.
	startPrepareTimeCompare()
	traceAlgorithmWindowLayout("加载结果 B")
}

// Only the accepted B preparation/render callback may confirm that a generated
// result is actually visible. A replaced/closed B cannot update the dialog.
func finishPoststackGeneratedBDisplay(err error) {
	r := poststackPendingBDisplay
	if r == nil {
		return
	}
	poststackPendingBDisplay = nil
	if r.retired.Load() || !r.ownsBProduct(compareBPath) {
		return
	}
	if err != nil {
		traceAlgorithmWindowLayout("结果 B 加载失败")
		setAlgorithmStatus("结果已生成，但 B 显示失败："+err.Error(), 100)
		setAlgorithmLog(err.Error(), r.RecordPath)
		return
	}
	if len(compareB.bgra) == 0 {
		setAlgorithmStatus("结果已生成，B 图像尚未完成", 100)
		return
	}
	setAlgorithmStatus(poststackResultStatus(r, "B 剖面已显示"), 100)
	if r.Record.AlgorithmID == poststack.BandpassAlgorithmID {
		note := "B 未施加视图滤波。"
		if p := viewFilterSideSettings()[0]; p != nil && compareTD != nil && compareTD.fa != nil {
			formal, err := poststack.BandpassParameters(r.Record.Parameters, compareTD.fa.Info.SampleIntervalUS)
			if err == nil && p.Canonical() == formal.Canonical() {
				note += "A 已用相同滤波，差值可能接近零；可在频谱中仅恢复 A。"
			}
		}
		setAlgorithmLog(note, r.RecordPath)
		setText(cc.status, "算法处理：B 剖面已显示；"+note)
	}
	traceAlgorithmWindowLayout("结果 B 已显示")
}

type poststackResultChoice struct {
	result      *generatedPoststackResult
	path        string
	formalIndex int
}

func poststackResultChoices() []poststackResultChoice {
	var choices []poststackResultChoice
	for i, r := range formalPoststackResults() {
		if r.Section == nil {
			continue
		}
		choices = append(choices, poststackResultChoice{r, r.Section.Part2, i})
	}
	return choices
}

func refreshPoststackResultChoices() {
	if cz.results == 0 {
		return
	}
	pSendMessageW.Call(cz.results, CB_RESETCONTENT, 0, 0)
	pSendMessageW.Call(cz.results, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16("选择正式结果…"))))
	choices := poststackResultChoices()
	selected := -1
	for row, choice := range choices {
		r := choice.result
		label := fmt.Sprintf("%d. %s · %s", choice.formalIndex+1, r.At.Format("15:04:05"), r.displayName())
		pSendMessageW.Call(cz.results, CB_ADDSTRING, 0, uintptr(unsafe.Pointer(u16(label))))
		if choice.path == compareBPath {
			selected = row
		}
	}
	pSendMessageW.Call(cz.results, CB_SETCURSEL, uintptr(selected+1), 0)
	hasFormal := len(choices) > 0
	layoutChanged := hasFormal != cz.hasFormal
	cz.hasFormal = hasFormal
	refreshPoststackResultControls()
	refreshPoststackOutputPreview()
	if layoutChanged {
		layoutCadzowWindow("正式结果可用性改变")
	}
}

func refreshPoststackResultControls() {
	if cz.results == 0 {
		return
	}
	pEnableWindow.Call(cz.results, uintptr(boolToInt(cz.hasFormal && cadzowCancel == nil)))
	pEnableWindow.Call(cz.exportResults, uintptr(boolToInt(activeGeneratedPoststackResult() != nil && cadzowCancel == nil)))
}

func activeGeneratedPoststackResult() *generatedPoststackResult {
	generatedPoststackMu.RLock()
	defer generatedPoststackMu.RUnlock()
	for _, r := range generatedPoststackResults {
		if r.ownsBProduct(compareBPath) && !r.retired.Load() {
			return r
		}
	}
	return nil
}

func formalPoststackResults() []*generatedPoststackResult {
	generatedPoststackMu.RLock()
	defer generatedPoststackMu.RUnlock()
	var results []*generatedPoststackResult
	for _, r := range generatedPoststackResults {
		if !r.Preview && !r.retired.Load() {
			results = append(results, r)
		}
	}
	return results
}

// The Compare Reader has already been closed by the caller. Any old worker
// Reader gets the same deferred cleanup path as formal B results.
func retirePoststackPreviewsExcept(keep *generatedPoststackResult) {
	generatedPoststackMu.Lock()
	var retired []*generatedPoststackResult
	kept := generatedPoststackResults[:0]
	for _, r := range generatedPoststackResults {
		if r.Preview && r != keep {
			r.retired.Store(true)
			retired = append(retired, r)
		} else {
			kept = append(kept, r)
		}
	}
	for i := len(kept); i < len(generatedPoststackResults); i++ {
		generatedPoststackResults[i] = nil
	}
	generatedPoststackResults = kept
	generatedPoststackMu.Unlock()
	for _, r := range retired {
		retirePoststackSection(r.Section)
	}
}

func selectPoststackResult() {
	i, _, _ := pSendMessageW.Call(cz.results, CB_GETCURSEL, 0, 0)
	results := poststackResultChoices()
	if i == 0 || i > uintptr(len(results)) {
		return
	}
	i-- // row zero is an instructional placeholder, not a result
	choice := results[i]
	r := choice.result
	if err := r.Section.SourceUnchanged(); err != nil {
		setAlgorithmStatus("A 文件发生变化，拒绝加载旧 B："+err.Error(), -1)
		return
	}
	loadGeneratedPoststackProduct(r, choice.path)
	poststackProcessingSession.SelectResult(choice.formalIndex)
	refreshPoststackOutputPreview()
	setAlgorithmStatus("已切换 B："+r.displayName(), -1)
}

func generatedPoststackSourceWindow(path string) (int, int, bool) {
	generatedPoststackMu.RLock()
	defer generatedPoststackMu.RUnlock()
	for _, r := range generatedPoststackResults {
		if r.ownsBProduct(path) && !r.retired.Load() {
			return r.Section.SampleStart, r.Section.SampleEnd, true
		}
	}
	return 0, 0, false
}
func generatedPoststackSourceStart(path string) (int, bool) {
	first, _, ok := generatedPoststackSourceWindow(path)
	return first, ok
}
func generatedPoststackSourceEnd(path string) (int, bool) {
	_, last, ok := generatedPoststackSourceWindow(path)
	return last, ok
}

func generatedPoststackTraceOrder(path string) []int64 {
	generatedPoststackMu.RLock()
	defer generatedPoststackMu.RUnlock()
	for _, r := range generatedPoststackResults {
		if r.ownsBProduct(path) && !r.retired.Load() {
			return append([]int64(nil), r.Section.TraceIndices...)
		}
	}
	return nil
}

// Explicit only for a host-owned, validated result. Ordinary SEG-Y paths still
// use their original reader; arbitrary .sfdata files are never autodetected.
func openPoststackCompareSource(path string) (*segy.File, error) {
	generatedPoststackMu.RLock()
	for _, r := range generatedPoststackResults {
		if r.Section.Part2 == path || (r.Section.Part1 != "" && r.Section.Part1 == path) {
			generatedPoststackMu.RUnlock()
			if r.retired.Load() {
				return nil, fmt.Errorf("generated B has been released")
			}
			return poststack.OpenSectionResult(r.Section, path)
		}
	}
	generatedPoststackMu.RUnlock()
	return segy.Open(path)
}

func isPoststackArtifactResult(path string) bool {
	generatedPoststackMu.RLock()
	defer generatedPoststackMu.RUnlock()
	for _, r := range generatedPoststackResults {
		if !r.retired.Load() && (r.Section.Part2 == path || (r.Section.Part1 != "" && r.Section.Part1 == path)) {
			return strings.EqualFold(filepath.Ext(path), ".sfdata")
		}
	}
	return false
}

// The legacy SEG-Y writer rounds the raw origin and the additional crop
// independently. Both must be integral milliseconds; checking only their sum
// would turn 500us + 500us into an incorrect 2ms output origin.
func poststackArtifactSEGYCropExact(originUS int64, first, intervalUS int) bool {
	return first >= 0 && intervalUS > 0 && originUS%1000 == 0 &&
		(int64(first%1000)*int64(intervalUS%1000))%1000 == 0
}

// Caller must close B's Reader first. Only uniquely-owned temporary directories
// are removed; exported paths and user-selected B files are never deleted.
func releaseGeneratedPoststackResults() {
	poststackPendingBDisplay = nil
	cancelCadzowTask()
	atomic.AddInt64(&poststackEpoch, 1)
	generatedPoststackMu.Lock()
	old := generatedPoststackResults
	generatedPoststackResults = nil
	generatedPoststackMu.Unlock()
	for _, r := range old {
		r.retired.Store(true)
		retirePoststackSection(r.Section)
	}
	poststackProcessingSession.ClearResults()
	retryRetiredPoststackResults()
	refreshPoststackResultChoices()
}
