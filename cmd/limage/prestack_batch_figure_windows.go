//go:build windows

package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/figurebundle"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestackbatch"
)

type prestackBatchPlanFigureToken struct {
	owner                                                        uintptr
	ownerToken                                                   int64
	workspaceGeneration                                          uint64
	aDatasetGeneration, bDatasetGeneration                       int64
	aMappingGeneration, bMappingGeneration                       int64
	selectionGeneration, compareModeGeneration, resizeGeneration int64
	page, width, height                                          int
	generation                                                   int64
}

type prestackBatchPlanFigureResult struct {
	token               prestackBatchPlanFigureToken
	planPath, figureDir string
	err                 error
}

var prestackBatchPlanFigureDone = make(chan prestackBatchPlanFigureResult, 2)

type prestackBatchPlanFigureJob struct {
	token                             prestackBatchPlanFigureToken
	planPath, sessionPath, root, stem string
	payload                           prestackFigurePayload
	reference                         *image.NRGBA
	figure                            prestackFigureExportJob
}

func currentPrestackBatchPlanFigureToken(generation int64, width, height int) prestackBatchPlanFigureToken {
	return prestackBatchPlanFigureToken{
		owner: prestackHwnd, ownerToken: prestackState.ownerToken,
		workspaceGeneration:   prestackState.workspaceGeneration,
		aDatasetGeneration:    prestackState.datasetGeneration,
		bDatasetGeneration:    prestackState.compareBDatasetGeneration,
		aMappingGeneration:    prestackState.mappingGeneration,
		bMappingGeneration:    prestackState.compareBMappingGeneration,
		selectionGeneration:   prestackState.selectionGeneration,
		compareModeGeneration: prestackState.compareModeGeneration,
		resizeGeneration:      prestackState.resizeGeneration,
		page:                  prestackState.page, width: width, height: height,
		generation: generation,
	}
}

func startPrestackBatchPlanFigure(planPath, sessionPath, root, stem string) bool {
	if prestackState.batchPlanCancel != nil {
		setPrestackStatus("正在生成批处理计划绘图任务，请等待或关闭窗口取消。")
		return true
	}
	width, height := clientSize(prestackHwnd)
	payload, err := CapturePrestackFigurePayload(&prestackState, width, height)
	if err != nil {
		setPrestackStatus(fmt.Sprintf("批处理计划已保存（QC）；未加入绘图任务：%s", err))
		return false
	}
	reference, err := PaintPrestackFigurePayload(payload, &prestackState)
	if err != nil {
		setPrestackStatus(fmt.Sprintf("批处理计划已保存（QC）；未加入绘图任务：%s", err))
		return false
	}
	figureDir := filepath.Join(root, stem+".figure")
	if _, err := os.Lstat(figureDir); err == nil {
		setPrestackStatus("批处理计划已保存（QC）；绘图目录已存在，请另存计划。")
		return false
	} else if !os.IsNotExist(err) {
		setPrestackStatus("批处理计划已保存（QC）；检查绘图目录失败：" + err.Error())
		return false
	}
	executable, err := os.Executable()
	if err != nil {
		setPrestackStatus("批处理计划已保存（QC）；无法定位绘图程序：" + err.Error())
		return false
	}
	gen := atomic.AddInt64(&prestackGeneration, 1)
	token := currentPrestackBatchPlanFigureToken(gen, width, height)
	ctx, cancel := context.WithCancel(context.Background())
	prestackState.batchPlanCancel, prestackState.batchPlanGeneration = cancel, gen
	setText(prestackUI.batchPlan, "取消绘图计划")
	setPrestackStatus("正在生成并校验批处理计划绘图任务…")
	job := prestackBatchPlanFigureJob{
		token: token, planPath: planPath, sessionPath: sessionPath, root: root, stem: stem,
		payload: payload, reference: reference,
		figure: prestackFigureExportJob{
			token: currentPrestackFigureExportToken(), payload: payload, reference: reference,
			sourceA: prestackState.dataset.Path, executable: executable, directory: figureDir,
		},
	}
	if payload.HasB && prestackState.compareBDataset != nil {
		job.figure.sourceB = prestackState.compareBDataset.Path
	}
	go func() {
		err := buildPrestackBatchPlanFigure(ctx, job)
		if err == nil && ctx.Err() != nil {
			_ = os.RemoveAll(job.figure.directory)
			err = ctx.Err()
		}
		result := prestackBatchPlanFigureResult{token: token, planPath: planPath, figureDir: job.figure.directory, err: err}
		select {
		case prestackBatchPlanFigureDone <- result:
		default:
		}
		if token.owner != 0 {
			pPostMessageW.Call(token.owner, WM_PRESTACK_BATCH_PLAN_DONE, uintptr(token.generation), 0)
		}
	}()
	return true
}

func buildPrestackBatchPlanFigure(ctx context.Context, job prestackBatchPlanFigureJob) error {
	return buildPrestackFigureBundle(ctx, job.figure, nil)
}

func finalizePrestackBatchPlanFigure(result prestackBatchPlanFigureResult) error {
	base, err := prestackbatch.Load(result.planPath)
	if err != nil {
		return err
	}
	root := filepath.Dir(result.planPath)
	stem := strings.TrimSuffix(filepath.Base(result.planPath), filepath.Ext(result.planPath))
	draft := prestackbatch.NewDraft(APP_VERSION, root)
	for _, job := range base.QCReports {
		draft.AddQC(job.Session, filepath.Base(job.Output), job.Format)
	}
	draft.AddFigure(filepath.Join(result.figureDir, figurebundle.RecipeFilename), stem+".png", stem+".figure.result.json")
	plan, err := draft.Build()
	if err != nil {
		return err
	}
	return prestackbatch.Save(result.planPath, plan, true)
}

func cancelPrestackBatchPlanFigure() {
	if prestackState.batchPlanCancel != nil {
		prestackState.batchPlanCancel()
		prestackState.batchPlanCancel = nil
		prestackState.batchPlanGeneration = atomic.AddInt64(&prestackGeneration, 1)
		if prestackUI.batchPlan != 0 {
			setText(prestackUI.batchPlan, "保存批处理计划")
		}
	}
}

func receivePrestackBatchPlanFigure() {
	for {
		select {
		case result := <-prestackBatchPlanFigureDone:
			w, h := clientSize(prestackHwnd)
			valid := prestackState.batchPlanCancel != nil && result.token.owner == prestackHwnd &&
				result.token.ownerToken == prestackState.ownerToken &&
				result.token.generation == prestackState.batchPlanGeneration &&
				result.token.workspaceGeneration == prestackState.workspaceGeneration &&
				result.token.aDatasetGeneration == prestackState.datasetGeneration &&
				result.token.bDatasetGeneration == prestackState.compareBDatasetGeneration &&
				result.token.aMappingGeneration == prestackState.mappingGeneration &&
				result.token.bMappingGeneration == prestackState.compareBMappingGeneration &&
				result.token.selectionGeneration == prestackState.selectionGeneration &&
				result.token.compareModeGeneration == prestackState.compareModeGeneration &&
				result.token.resizeGeneration == prestackState.resizeGeneration &&
				result.token.page == prestackState.page &&
				result.token.width == w && result.token.height == h
			if !valid {
				if result.err == nil {
					_ = os.RemoveAll(result.figureDir)
				}
				continue
			}
			prestackState.batchPlanCancel = nil
			setText(prestackUI.batchPlan, "保存批处理计划")
			if result.err != nil {
				_ = os.RemoveAll(result.figureDir)
				setPrestackStatus("批处理计划已保存（QC）；绘图任务失败：" + result.err.Error())
			} else {
				if err := finalizePrestackBatchPlanFigure(result); err != nil {
					_ = os.RemoveAll(result.figureDir)
					setPrestackStatus("批处理计划已保存（QC）；绘图任务失败：" + err.Error())
				} else {
					setPrestackStatus("批处理计划已更新：已加入 Figure + QC 任务。")
				}
			}
		default:
			return
		}
	}
}
