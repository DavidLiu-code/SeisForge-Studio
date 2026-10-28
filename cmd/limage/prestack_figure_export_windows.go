//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/figurebundle"
)

// Figure jobs are independent of changing view/selection generations: they
// export the immutable frame captured at the click. Owner or dataset changes
// invalidate delivery and cancel the job, including a detached Compare B.
type prestackFigureExportToken struct {
	generation                             int64
	owner                                  uintptr
	ownerToken                             int64
	workspaceGeneration                    uint64
	aDatasetGeneration, bDatasetGeneration int64
	hasB                                   bool
}

type prestackFigureExportJob struct {
	token                                   prestackFigureExportToken
	payload                                 prestackFigurePayload
	reference                               *image.NRGBA
	sourceA, sourceB, executable, directory string
}

type prestackFigureExportResult struct {
	token     prestackFigureExportToken
	directory string
	err       error
}

type prestackFigureExportProgress struct {
	token   prestackFigureExportToken
	percent int
	stage   string
}

var prestackFigureDelivery struct {
	sync.Mutex
	generation int64
	active     prestackFigureExportToken
	cancel     context.CancelFunc
	done       *prestackFigureExportResult
	progress   *prestackFigureExportProgress
}

// Cancellation during the legacy bounded sample renderer drains that frame
// before its reader is closed. A single gate prevents repeated cancel/start
// clicks from accumulating simultaneous sample readers or GDI render jobs.
var prestackFigureWorkerSlot = make(chan struct{}, 1)

func prestackFigureExportRunning() bool {
	prestackFigureDelivery.Lock()
	defer prestackFigureDelivery.Unlock()
	return prestackFigureDelivery.cancel != nil
}

func currentPrestackFigureExportToken() prestackFigureExportToken {
	return prestackFigureExportToken{owner: prestackHwnd, ownerToken: prestackState.ownerToken,
		workspaceGeneration: prestackState.workspaceGeneration, aDatasetGeneration: prestackState.datasetGeneration,
		bDatasetGeneration: prestackState.compareBDatasetGeneration, hasB: prestackState.page == 3 && prestackState.compareBDataset != nil}
}

func prestackFigureExportSourceMatches(captured, current prestackFigureExportToken) bool {
	return captured.owner != 0 && captured.owner == current.owner && captured.ownerToken == current.ownerToken &&
		captured.workspaceGeneration == current.workspaceGeneration && captured.aDatasetGeneration == current.aDatasetGeneration &&
		(!captured.hasB || captured.bDatasetGeneration == current.bDatasetGeneration)
}

func savePrestackFigureDirectory(owner uintptr, defaultName string) string {
	buffer := make([]uint16, 32768)
	copy(buffer, syscall.StringToUTF16(defaultName))
	filter := syscall.StringToUTF16("绘图复现目录 (*.figure)")
	filter = append(filter, syscall.StringToUTF16("*.figure")...)
	filter = append(filter, 0)
	title := u16("新建绘图复现目录（不会覆盖已有目录）")
	extension := u16("figure")
	dialog := OPENFILENAME{LStructSize: uint32(unsafe.Sizeof(OPENFILENAME{})), HwndOwner: owner,
		LpstrFilter: uintptr(unsafe.Pointer(&filter[0])), NFilterIndex: 1, LpstrFile: uintptr(unsafe.Pointer(&buffer[0])), NMaxFile: uint32(len(buffer)),
		LpstrTitle: uintptr(unsafe.Pointer(title)), Flags: OFN_EXPLORER | 0x800 /* OFN_PATHMUSTEXIST */, LpstrDefExt: uintptr(unsafe.Pointer(extension))}
	if selected, _, _ := pGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&dialog))); selected == 0 {
		return ""
	}
	path := syscall.UTF16ToString(buffer)
	if !strings.EqualFold(filepath.Ext(path), ".figure") {
		path += ".figure"
	}
	return path
}

// exportPrestackFigure is a UI-thread start/cancel toggle. No source file is
// scanned on the UI thread; only metadata and the already committed figure
// pixels are frozen before opening the destination dialog.
func exportPrestackFigure() {
	if prestackFigureExportRunning() {
		cancelPrestackFigureExport()
		setPrestackStatus("已请求取消生成；若正在重绘，将等待当前帧结束后释放资源。")
		return
	}
	width, height := clientSize(prestackHwnd)
	payload, err := CapturePrestackFigurePayload(&prestackState, width, height)
	if err != nil {
		message(prestackHwnd, "生成绘图代码", err.Error(), MB_OK|MB_ICONINFORMATION)
		return
	}
	reference, err := PaintPrestackFigurePayload(payload, &prestackState)
	if err != nil {
		message(prestackHwnd, "生成绘图代码", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	token := currentPrestackFigureExportToken()
	sourceA, sourceB := prestackState.dataset.Path, ""
	if payload.HasB {
		sourceB = prestackState.compareBDataset.Path
	}
	executable, err := os.Executable()
	if err != nil {
		message(prestackHwnd, "生成绘图代码", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	directory := savePrestackFigureDirectory(prestackHwnd, prestackExportStem()+".figure")
	if directory == "" {
		return
	}
	if !prestackFigureExportSourceMatches(token, currentPrestackFigureExportToken()) {
		return
	}
	if _, err := os.Lstat(directory); err == nil {
		message(prestackHwnd, "生成绘图代码", "目标已存在。请选择新的 .figure 目录名称；现有文件不会被覆盖。", MB_OK|MB_ICONINFORMATION)
		return
	} else if !os.IsNotExist(err) {
		message(prestackHwnd, "生成绘图代码", err.Error(), MB_OK|MB_ICONERROR)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	prestackFigureDelivery.Lock()
	prestackFigureDelivery.generation++
	token.generation = prestackFigureDelivery.generation
	prestackFigureDelivery.active, prestackFigureDelivery.cancel = token, cancel
	prestackFigureDelivery.done, prestackFigureDelivery.progress = nil, nil
	prestackFigureDelivery.Unlock()
	job := prestackFigureExportJob{token: token, payload: payload, reference: reference, sourceA: sourceA, sourceB: sourceB, executable: executable, directory: directory}
	setText(prestackUI.figureExport, "取消生成")
	setPrestackStatus("正在生成绘图代码：将校验整个源文件，并验证数据重绘与当前图像逐像素一致。")
	go func() {
		err := buildPrestackFigureBundle(ctx, job, func(percent int, stage string) {
			postPrestackFigureProgress(prestackFigureExportProgress{token: token, percent: percent, stage: stage}, nil)
		})
		publishPrestackFigureExport(prestackFigureExportResult{token: token, directory: directory, err: err}, nil)
	}()
}

// Lifecycle callers invoke this on the UI thread before clearing datasets or
// window controls. It only invalidates delivery; the worker owns all cleanup.
func cancelPrestackFigureExport() {
	prestackFigureDelivery.Lock()
	cancel := prestackFigureDelivery.cancel
	prestackFigureDelivery.generation++
	prestackFigureDelivery.active = prestackFigureExportToken{}
	prestackFigureDelivery.cancel = nil
	prestackFigureDelivery.done, prestackFigureDelivery.progress = nil, nil
	prestackFigureDelivery.Unlock()
	if cancel != nil {
		cancel()
	}
	if prestackUI.figureExport != 0 {
		setText(prestackUI.figureExport, "生成绘图代码")
	}
}

// post/publish only compare immutable tokens against a mutex-owned delivery
// registry. Workers never read prestackState, prestackUI or prestackHwnd.
func postPrestackFigureProgress(progress prestackFigureExportProgress, notify func(uintptr) bool) bool {
	prestackFigureDelivery.Lock()
	defer prestackFigureDelivery.Unlock()
	if prestackFigureDelivery.cancel == nil || progress.token != prestackFigureDelivery.active {
		return false
	}
	prestackFigureDelivery.progress = &progress
	posted := false
	if notify != nil {
		posted = notify(progress.token.owner)
	} else {
		result, _, _ := pPostMessageW.Call(progress.token.owner, WM_PRESTACK_FIGURE_PROGRESS, 0, 0)
		posted = result != 0
	}
	if !posted {
		prestackFigureDelivery.progress = nil
	}
	return posted
}

func publishPrestackFigureExport(result prestackFigureExportResult, notify func(uintptr) bool) bool {
	prestackFigureDelivery.Lock()
	defer prestackFigureDelivery.Unlock()
	if prestackFigureDelivery.cancel == nil || result.token != prestackFigureDelivery.active {
		return false
	}
	prestackFigureDelivery.done = &result
	posted := false
	if notify != nil {
		posted = notify(result.token.owner)
	} else {
		r, _, _ := pPostMessageW.Call(result.token.owner, WM_PRESTACK_FIGURE_DONE, 0, 0)
		posted = r != 0
	}
	if !posted {
		prestackFigureDelivery.done = nil
	}
	return posted
}

func receivePrestackFigureProgress() {
	prestackFigureDelivery.Lock()
	progress := prestackFigureDelivery.progress
	prestackFigureDelivery.progress = nil
	current := progress != nil && prestackFigureDelivery.cancel != nil && progress.token == prestackFigureDelivery.active
	prestackFigureDelivery.Unlock()
	if !current || !prestackFigureExportSourceMatches(progress.token, currentPrestackFigureExportToken()) {
		return
	}
	setPrestackStatus(fmt.Sprintf("生成绘图代码 %d%% | %s", progress.percent, progress.stage))
}

func receivePrestackFigureExport() {
	prestackFigureDelivery.Lock()
	result := prestackFigureDelivery.done
	prestackFigureDelivery.done = nil
	current := result != nil && prestackFigureDelivery.cancel != nil && result.token == prestackFigureDelivery.active
	var cancel context.CancelFunc
	if current {
		cancel = prestackFigureDelivery.cancel
		prestackFigureDelivery.cancel = nil
		prestackFigureDelivery.progress = nil
	}
	prestackFigureDelivery.Unlock()
	if cancel != nil {
		cancel()
	}
	if !current || !prestackFigureExportSourceMatches(result.token, currentPrestackFigureExportToken()) {
		return
	}
	setText(prestackUI.figureExport, "生成绘图代码")
	if result.err != nil {
		if errors.Is(result.err, context.Canceled) {
			setPrestackStatus("已取消生成绘图代码；未发布不完整目录。")
			return
		}
		setPrestackStatus("生成绘图代码失败：" + result.err.Error())
		message(prestackHwnd, "生成绘图代码失败", result.err.Error()+"\n\n未发布不完整目录。", MB_OK|MB_ICONERROR)
		return
	}
	setPrestackStatus("绘图代码已生成并通过逐像素校验：" + result.directory)
	message(prestackHwnd, "绘图代码已生成", "数据重绘已与当前图像逐像素核对一致。\n\n"+result.directory+"\n\n运行目录内 render.ps1 可从原始 SEG-Y 重新绘图。不要移动或修改源文件；迁移用法见 README.txt。", MB_OK|MB_ICONINFORMATION)
}

func figureSourceIdentity(ctx context.Context, path string, metadata any) (figurebundle.SourceIdentity, error) {
	var identity figurebundle.SourceIdentity
	abs, err := filepath.Abs(path)
	if err != nil {
		return identity, err
	}
	before, err := os.Stat(abs)
	if err != nil {
		return identity, err
	}
	if !before.Mode().IsRegular() {
		return identity, errors.New("figure source is not a regular file")
	}
	digest, err := figurebundle.HashFile(ctx, abs)
	if err != nil {
		return identity, err
	}
	after, err := os.Stat(abs)
	if err != nil {
		return identity, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return identity, errors.New("source changed while hashing")
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return identity, err
	}
	return figurebundle.SourceIdentity{Path: abs, Size: after.Size(), ModifiedUnixNano: after.ModTime().UnixNano(), SHA256: digest, Metadata: encoded}, nil
}

func verifyFigureSourceIdentity(ctx context.Context, identity figurebundle.SourceIdentity) error {
	current, err := figureSourceIdentity(ctx, identity.Path, nil)
	if err != nil {
		return err
	}
	if current.Size != identity.Size || current.ModifiedUnixNano != identity.ModifiedUnixNano || current.SHA256 != identity.SHA256 {
		return fmt.Errorf("source changed during figure generation: %s", identity.Path)
	}
	return nil
}

// buildPrestackFigureBundle is also exercised synchronously by fixture tests.
// WriteBundle alone publishes the new target atomically; this layer never
// deletes target paths and never passes partially verified content to it.
func buildPrestackFigureBundle(ctx context.Context, job prestackFigureExportJob, progress func(int, string)) error {
	select {
	case prestackFigureWorkerSlot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-prestackFigureWorkerSlot }()
	stage := func(percent int, label string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if progress != nil {
			progress(percent, label)
		}
		return ctx.Err()
	}
	if err := job.payload.validate(); err != nil {
		return err
	}
	if job.reference == nil {
		return errors.New("missing captured reference image")
	}
	if _, err := os.Lstat(job.directory); err == nil {
		return errors.New("figure destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := stage(5, "校验绘图程序 SHA-256"); err != nil {
		return err
	}
	rendererHash, err := figurebundle.HashFile(ctx, job.executable)
	if err != nil {
		return err
	}
	if err := stage(15, "校验完整源文件 A SHA-256（包含振幅样本）"); err != nil {
		return err
	}
	a, err := figureSourceIdentity(ctx, job.sourceA, job.payload.SourceAMetadata)
	if err != nil {
		return err
	}
	var b *figurebundle.SourceIdentity
	if job.payload.HasB {
		if err := stage(30, "校验完整源文件 B SHA-256（包含振幅样本）"); err != nil {
			return err
		}
		value, err := figureSourceIdentity(ctx, job.sourceB, job.payload.SourceBMetadata)
		if err != nil {
			return err
		}
		b = &value
	}
	if err := stage(45, "从 SEG-Y 数据重绘并校验当前帧（取消将等待本帧结束）"); err != nil {
		return err
	}
	replayed, err := RenderPrestackFigure(ctx, job.payload, job.sourceA, job.sourceB)
	if err != nil {
		return err
	}
	if err := stage(70, "逐像素校验数据重绘与当前图像"); err != nil {
		return err
	}
	referenceHash, err := figurebundle.PixelSHA256(job.reference)
	if err != nil {
		return err
	}
	replayHash, err := figurebundle.PixelSHA256(replayed)
	if err != nil {
		return err
	}
	if referenceHash != replayHash || job.reference.Bounds() != replayed.Bounds() {
		return errors.New("data replay does not exactly match the captured frame; refresh the current view and export again")
	}
	if err := stage(80, "再次验证源文件，拒绝生成期间的文件变更"); err != nil {
		return err
	}
	if err := verifyFigureSourceIdentity(ctx, a); err != nil {
		return err
	}
	if b != nil {
		if err := verifyFigureSourceIdentity(ctx, *b); err != nil {
			return err
		}
	}
	currentRendererHash, err := figurebundle.HashFile(ctx, job.executable)
	if err != nil {
		return err
	}
	if currentRendererHash != rendererHash {
		return errors.New("renderer executable changed during figure generation")
	}
	payload, err := json.Marshal(job.payload)
	if err != nil {
		return err
	}
	manifest := figurebundle.BundleManifest{Schema: figurebundle.Schema, RendererSHA256: rendererHash, ApplicationVersion: APP_VERSION,
		SourceA: a, SourceB: b, Payload: payload, PixelSHA256: referenceHash, Width: replayed.Bounds().Dx(), Height: replayed.Bounds().Dy()}
	if err := stage(95, "发布已校验的新绘图复现目录"); err != nil {
		return err
	}
	if err := figurebundle.WriteBundle(ctx, job.directory, manifest, job.reference, job.executable); err != nil {
		return err
	}
	if progress != nil {
		progress(100, "绘图代码生成完成")
	}
	return nil
}
