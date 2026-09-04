//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	appcore "github.com/DavidLiu-code/SeisForge-Studio/internal/app"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/dataset"
	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	projectcore "github.com/DavidLiu-code/SeisForge-Studio/internal/project"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
	workspacecore "github.com/DavidLiu-code/SeisForge-Studio/internal/workspace"
)

var (
	application                     *appcore.Application
	phase1VolumeDataset             *dataset.SeismicDataset
	phase1VolumeWorkspaceGeneration uint64
	phase1ManagerClosingVolume      bool
)

func initializePhase1Application() error {
	application = appcore.NewDefault(writeWorkspaceTrace)
	for _, adapter := range []workspacecore.Workspace{
		&homeWorkspaceAdapter{},
		&lineWorkspaceAdapter{kind: workspacecore.Kind2D},
		&volumeWorkspaceAdapter{},
		&crookedWorkspaceAdapter{},
	} {
		if err := application.Workspaces.Register(adapter); err != nil {
			return err
		}
	}
	return application.Workspaces.SetInitial(workspacecore.KindHome)
}

func writeWorkspaceTrace(event workspacecore.TraceEvent) {
	if err := os.MkdirAll(licenseDirectory(), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(licenseDirectory(), "workspace_trace.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	errorText := "-"
	if event.Err != nil {
		errorText = redactWorkspaceError(event.Err)
	}
	_, _ = fmt.Fprintf(f, "%s workspace=%s previous=%s dataset=%q geometry=%s score=%.3f confidence=%.3f generation=%d project_lines=%d action=%s error=%q\n",
		time.Now().Format("2006-01-02 15:04:05.000"), event.Workspace, event.Previous, event.Dataset, event.Geometry, event.GeometryScore, event.GeometryConfidence, event.Generation, event.ProjectLines, event.Action, errorText)
}

func redactWorkspaceError(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Op + ": " + pathErr.Err.Error()
	}
	parts := strings.Fields(strings.ReplaceAll(strings.ReplaceAll(err.Error(), "\r", " "), "\n", " "))
	for i, part := range parts {
		if strings.ContainsAny(part, `\`+"/") {
			parts[i] = "[path]"
		}
	}
	return strings.Join(parts, " ")
}

type homeWorkspaceAdapter struct{}

func (*homeWorkspaceAdapter) Kind() workspacecore.Kind { return workspacecore.KindHome }
func (*homeWorkspaceAdapter) Open(workspacecore.OpenRequest) error {
	return errors.New("Home does not open seismic data")
}
func (*homeWorkspaceAdapter) Show() error {
	updateCompareWelcomeState()
	if compareHwnd != 0 {
		pShowWindow.Call(compareHwnd, SW_SHOW)
		pUpdateWindow.Call(compareHwnd)
		pSetForeground.Call(compareHwnd)
	}
	return nil
}
func (*homeWorkspaceAdapter) Hide() error {
	if compareHwnd != 0 {
		pShowWindow.Call(compareHwnd, SW_HIDE)
	}
	return nil
}
func (*homeWorkspaceAdapter) Close() error { return nil }

type lineWorkspaceAdapter struct {
	kind workspacecore.Kind
}

func (w *lineWorkspaceAdapter) Kind() workspacecore.Kind { return w.kind }
func (w *lineWorkspaceAdapter) Open(request workspacecore.OpenRequest) error {
	traceWindow := request.EffectiveTraceRange()
	sampleWindow := request.EffectiveSampleRange()
	oldTraceStart, oldTraceEnd, oldTraceStep := traceStart, traceEnd, traceStep
	oldSampleStart, oldSampleEnd := sampleStart, sampleEnd
	oldGain, oldUseLimits := gainPercent, useLimits

	traceStart, traceEnd, traceStep = traceWindow.Start, traceWindow.End, 1
	sampleStart, sampleEnd = int(sampleWindow.Start), int(sampleWindow.End)
	gainPercent, useLimits = 0, false
	if !loadSelectedFile(request.Dataset.Path) {
		traceStart, traceEnd, traceStep = oldTraceStart, oldTraceEnd, oldTraceStep
		sampleStart, sampleEnd = oldSampleStart, oldSampleEnd
		gainPercent, useLimits = oldGain, oldUseLimits
		return errors.New("legacy 2-D adapter could not render the requested dataset")
	}
	storeOriginRange()
	setZoomMode(false)
	workspaceSyncAFromMain()
	activeWorkspaceMode = phase1LegacyMode(w.kind)
	applyPhase1CompareWorkspaceTitle(w.kind)
	return nil
}
func (w *lineWorkspaceAdapter) Show() error {
	applyPhase1CompareWorkspaceTitle(w.kind)
	updateCompareWelcomeState()
	if compareHwnd != 0 {
		pShowWindow.Call(compareHwnd, SW_SHOW)
		pUpdateWindow.Call(compareHwnd)
		pSetForeground.Call(compareHwnd)
	}
	return nil
}
func (*lineWorkspaceAdapter) Hide() error {
	if compareHwnd != 0 {
		pShowWindow.Call(compareHwnd, SW_HIDE)
	}
	return nil
}
func (*lineWorkspaceAdapter) Close() error { return nil }

func applyPhase1CompareWorkspaceTitle(kind workspacecore.Kind) {
	if compareHwnd == 0 {
		return
	}
	title := APP_NAME + " v" + APP_VERSION + " [x64] - 二维剖面"
	if kind == workspacecore.KindCrooked {
		title = APP_NAME + " v" + APP_VERSION + " [x64] - 弯线工作区"
		setText(cc.status, "弯线工作区：Phase 1 继续按 SEG-Y 道序使用二维兼容显示。")
	}
	pSetWindowTextW.Call(compareHwnd, uintptr(unsafe.Pointer(u16(title))))
}

type volumeWorkspaceAdapter struct{}

type crookedWorkspaceAdapter struct{}

func (*crookedWorkspaceAdapter) Kind() workspacecore.Kind { return workspacecore.KindCrooked }
func (*crookedWorkspaceAdapter) OpenEmpty(request workspacecore.EmptyOpenRequest) error {
	if !createCrookedWindowShellDeferred() {
		return errors.New("could not create the empty Crooked 2-D workspace shell")
	}
	resetCrookedToEmpty(request.Generation)
	activeWorkspaceMode = workspaceModeCrooked
	return nil
}
func (*crookedWorkspaceAdapter) Open(request workspacecore.OpenRequest) error {
	p, err := projectcore.FromDataset(request.Dataset)
	if err != nil {
		return err
	}
	return (&crookedWorkspaceAdapter{}).OpenProject(workspacecore.ProjectOpenRequest{Project: p, Generation: request.Generation})
}
func (*crookedWorkspaceAdapter) OpenProject(request workspacecore.ProjectOpenRequest) error {
	if !createCrookedWindowShellDeferred() {
		return errors.New("could not create the Crooked 2-D workspace shell")
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if !startCrookedProject(request.Project, request.Generation) {
		return errors.New("could not start asynchronous crooked-line preparation")
	}
	activeWorkspaceMode = workspaceModeCrooked
	return nil
}
func showCrookedWorkspace() error {
	if crookedHwnd == 0 {
		return errors.New("Crooked 2-D workspace shell is not available")
	}
	if crookedState.project != nil {
		resumeCrookedProjectPreparation()
		if source := crookedActiveSectionPrepareDataset(); source != nil {
			startCrookedPrepare(source, crookedState.workspaceGeneration, crookedState.sampleStart, crookedState.sampleEnd)
		} else if crookedState.geometry != nil && len(crookedState.bgra) == 0 && !crookedState.rendering {
			startCrookedRender()
		}
	} else if crookedState.dataset != nil {
		if crookedState.geometry == nil && !crookedState.loading {
			startCrookedPrepare(crookedState.dataset, crookedState.workspaceGeneration, crookedState.sampleStart, crookedState.sampleEnd)
		} else if crookedState.geometry != nil && len(crookedState.bgra) == 0 && !crookedState.rendering {
			startCrookedRender()
		}
	}
	showTopLevelWindowRestored(crookedHwnd)
	return nil
}
func (*crookedWorkspaceAdapter) Show() error { return showCrookedWorkspace() }
func (*crookedWorkspaceAdapter) Hide() error {
	if crookedHwnd != 0 {
		pShowWindow.Call(crookedHwnd, SW_HIDE)
		if pseudoHwnd != 0 {
			pShowWindow.Call(pseudoHwnd, SW_HIDE)
		}
		suspendCrookedPreparationForHide()
	}
	return nil
}
func (*crookedWorkspaceAdapter) Close() error {
	if crookedHwnd != 0 {
		crookedManagerClosing = true
		pDestroyWindow.Call(crookedHwnd)
		crookedManagerClosing = false
	}
	return nil
}

func (*volumeWorkspaceAdapter) Kind() workspacecore.Kind { return workspacecore.Kind3D }
func (*volumeWorkspaceAdapter) Open(request workspacecore.OpenRequest) error {
	fullSamples := request.SampleRange == nil
	if !createVolumeWindowShellDeferred(fullSamples) {
		return errors.New("could not create the 3-D workspace shell")
	}
	if fullSamples {
		originValid = false
		originSampleStart, originSampleEnd = 0, -1
	} else {
		samples := request.EffectiveSampleRange()
		originValid = true
		originSampleStart, originSampleEnd = int(samples.Start), int(samples.End)
	}
	phase1VolumeDataset = request.Dataset
	phase1VolumeWorkspaceGeneration = request.Generation
	if !loadVolumePathIntoShell(request.Dataset.Path, fullSamples) {
		phase1VolumeDataset = nil
		return errors.New("could not start asynchronous 3-D geometry preparation")
	}
	activeWorkspaceMode = workspaceMode3D
	return nil
}
func (*volumeWorkspaceAdapter) Show() error {
	if volumeHwnd == 0 {
		return errors.New("3-D workspace shell is not available")
	}
	if compareHwnd != 0 {
		pShowWindow.Call(compareHwnd, SW_HIDE)
		volumeHidCompare = true
	}
	forceVolume3DViewState()
	pShowWindow.Call(volumeHwnd, SW_SHOW)
	pUpdateWindow.Call(volumeHwnd)
	pSetForeground.Call(volumeHwnd)
	return nil
}
func (*volumeWorkspaceAdapter) Hide() error {
	if volumeHwnd != 0 {
		pShowWindow.Call(volumeHwnd, SW_HIDE)
		hideVolumeIndexProgress(0)
		// A result produced after the workspace has been left must not mutate a
		// hidden scene. Existing worker results already carry volumeGen.
		atomic.AddInt64(&volumeGen, 1)
	}
	return nil
}
func (*volumeWorkspaceAdapter) Close() error {
	if volumeHwnd != 0 {
		phase1ManagerClosingVolume = true
		pDestroyWindow.Call(volumeHwnd)
		phase1ManagerClosingVolume = false
	}
	return nil
}

func openApplicationPath(path string, legacyMode int) bool {
	if application == nil {
		if err := initializePhase1Application(); err != nil {
			message(compareHwnd, APP_NAME, err.Error(), MB_OK|MB_ICONERROR)
			return false
		}
	}
	kind := phase1WorkspaceKind(legacyMode)
	data, err := application.OpenPath(kind, path)
	if err != nil {
		owner := compareHwnd
		if volumeHwnd != 0 {
			owner = volumeHwnd
		}
		message(owner, APP_NAME+" - Open error", err.Error(), MB_OK|MB_ICONERROR)
		return false
	}
	resolved := application.Workspaces.ActiveKind()
	activeWorkspaceMode = phase1LegacyMode(resolved)
	rememberRecentSegy(data.Path, activeWorkspaceMode)
	return true
}

func openCurrentDatasetInVolume() bool {
	if application == nil {
		return false
	}
	data := application.CurrentDataset()
	if (data == nil || (sf != nil && !strings.EqualFold(filepath.Clean(data.Path), filepath.Clean(sf.Info.Path)))) && sf != nil {
		opened, err := application.Data.Open(sf.Info.Path)
		if err != nil {
			message(compareHwnd, "三维数据体", err.Error(), MB_OK|MB_ICONERROR)
			return false
		}
		if err := application.AdoptDataset(workspacecore.Kind2D, opened); err != nil {
			message(compareHwnd, "三维数据体", err.Error(), MB_OK|MB_ICONERROR)
			return false
		}
		data = opened
	}
	if data == nil {
		return false
	}
	options := []workspacecore.OpenOption{}
	if sampleStart >= 0 && sampleEnd >= sampleStart && sampleEnd < data.Metadata.SamplesPerTrace {
		options = append(options, workspacecore.WithSampleRange(sampleStart, sampleEnd))
	}
	if err := application.OpenDataset(workspacecore.Kind3D, data, options...); err != nil {
		message(compareHwnd, "三维数据体", err.Error(), MB_OK|MB_ICONERROR)
		return false
	}
	activeWorkspaceMode = workspaceMode3D
	rememberRecentSegy(data.Path, workspaceMode3D)
	return true
}

func phase1AdoptLegacyDataset(path string, mode int) {
	if application == nil || path == "" {
		return
	}
	data, err := application.Data.Open(path)
	if err != nil {
		writeWorkspaceTrace(workspacecore.TraceEvent{Action: "legacy_adopt_failed", Workspace: phase1WorkspaceKind(mode), Dataset: filepath.Base(path), Generation: application.Workspaces.Generation(), Err: err})
		return
	}
	kind := phase1WorkspaceKind(mode)
	if kind == workspacecore.KindAuto || kind == workspacecore.Kind3D {
		kind = workspacecore.Kind2D
	}
	if err := application.AdoptDataset(kind, data); err != nil {
		writeWorkspaceTrace(workspacecore.TraceEvent{Action: "legacy_adopt_failed", Workspace: kind, Dataset: data.Basename(), Generation: application.Workspaces.Generation(), Err: err})
	}
}

func phase1WorkspaceKind(mode int) workspacecore.Kind {
	switch mode {
	case workspaceMode2D:
		return workspacecore.Kind2D
	case workspaceMode3D:
		return workspacecore.Kind3D
	case workspaceModeCrooked:
		return workspacecore.KindCrooked
	default:
		return workspacecore.KindAuto
	}
}

func phase1LegacyMode(kind workspacecore.Kind) int {
	switch kind {
	case workspacecore.Kind3D:
		return workspaceMode3D
	case workspacecore.KindCrooked:
		return workspaceModeCrooked
	default:
		return workspaceMode2D
	}
}

func phase1VolumeResultCurrent() bool {
	return application == nil || application.Workspaces.IsCurrent(workspacecore.Kind3D, phase1VolumeWorkspaceGeneration)
}

func phase1AttachVolumeGeometry(path string, index *segy.GeometryIndex) {
	if phase1VolumeDataset == nil || index == nil || !strings.EqualFold(filepath.Clean(path), filepath.Clean(phase1VolumeDataset.Path)) {
		return
	}
	if wrapped, err := geometrycore.WrapRegularGrid(index); err == nil {
		phase1VolumeDataset.SetGeometry(wrapped)
		writeWorkspaceTrace(workspacecore.TraceEvent{Action: "geometry_ready", Workspace: workspacecore.Kind3D, Dataset: phase1VolumeDataset.Basename(), Geometry: geometrycore.KindRegular3D, Generation: phase1VolumeWorkspaceGeneration})
	}
}

func phase1TraceVolumeError(err error) {
	datasetName := ""
	geometryKind := geometrycore.KindUnknown
	if phase1VolumeDataset != nil {
		datasetName = phase1VolumeDataset.Basename()
		if phase1VolumeDataset.Geometry() != nil {
			geometryKind = phase1VolumeDataset.Geometry().Kind()
		}
	}
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: "geometry_failed", Workspace: workspacecore.Kind3D, Dataset: datasetName, Geometry: geometryKind, Generation: phase1VolumeWorkspaceGeneration, Err: err})
}

func phase1TraceVolumeIndexStart(path string) {
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: "geometry_index_begin", Workspace: workspacecore.Kind3D, Dataset: filepath.Base(path), Geometry: geometrycore.KindUnknown, Generation: phase1VolumeWorkspaceGeneration})
}

func phase1TraceVolumeIndexReady(path string, stats segy.GeometryBuildStats) {
	action := "geometry_index_ready"
	if stats.FromCache {
		action = "geometry_index_cache_hit"
	}
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: action, Workspace: workspacecore.Kind3D, Dataset: filepath.Base(path), Geometry: geometrycore.KindRegular3D, Generation: phase1VolumeWorkspaceGeneration})
}

func phase1TraceVolumeDiscarded() {
	datasetName := ""
	if phase1VolumeDataset != nil {
		datasetName = phase1VolumeDataset.Basename()
	}
	writeWorkspaceTrace(workspacecore.TraceEvent{Action: "async_result_discarded", Workspace: workspacecore.Kind3D, Dataset: datasetName, Generation: phase1VolumeWorkspaceGeneration})
}

func phase1NotifyVolumeClosed() {
	phase1VolumeDataset = nil
	if application != nil && !phase1ManagerClosingVolume {
		_ = application.Workspaces.NotifyClosed(workspacecore.Kind3D)
	}
}
