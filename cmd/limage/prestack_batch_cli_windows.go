//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/figurebundle"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestackbatch"
	prestacksession "github.com/DavidLiu-code/SeisForge-Studio/internal/prestacksession"
	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

type prestackBatchCLIOptions struct {
	plan      string
	overwrite bool
}

// handlePrestackBatchCommand runs before any HWND, activation UI, or live
// workspace is created.  The plan is metadata-only; each figure/QC job owns
// its reader and closes it before the next job starts.
func handlePrestackBatchCommand(args []string) (handled bool, exitCode int) {
	for _, arg := range args {
		if arg == "--batch-plan" || strings.HasPrefix(arg, "--batch-plan=") {
			handled = true
			break
		}
	}
	if !handled {
		return false, 0
	}
	o, err := parsePrestackBatchCommand(args)
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err = runPrestackBatchPlan(ctx, o)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Batch plan failed:", err)
		return true, 1
	}
	return true, 0
}

func parsePrestackBatchCommand(args []string) (prestackBatchCLIOptions, error) {
	var o prestackBatchCLIOptions
	f := flag.NewFlagSet("batch-plan", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.plan, "batch-plan", "", "prestack batch plan JSON")
	f.BoolVar(&o.overwrite, "overwrite", false, "replace existing figure/QC outputs")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 || strings.TrimSpace(o.plan) == "" {
		return o, errors.New("use --batch-plan plan.json [--overwrite]")
	}
	return o, nil
}

func runPrestackBatchPlan(ctx context.Context, options prestackBatchCLIOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	plan, err := prestackbatch.Load(options.plan)
	if err != nil {
		return err
	}
	for _, job := range plan.Figures {
		if err := ctx.Err(); err != nil {
			return err
		}
		run := prestackFigureCLIOptions{recipe: job.Recipe, output: job.Output, result: job.Result, overwrite: options.overwrite, sourceA: job.SourceA.Path}
		if job.SourceB != nil {
			run.sourceB = job.SourceB.Path
		}
		if _, err := runPrestackFigureCommand(ctx, run, authorizePrestackFigureCommand); err != nil {
			return fmt.Errorf("figure %s: %w", job.ID, err)
		}
	}
	for _, job := range plan.QCReports {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := runPrestackBatchQCJob(ctx, job, options.overwrite); err != nil {
			return fmt.Errorf("QC %s: %w", job.ID, err)
		}
	}
	return nil
}

func runPrestackBatchQCJob(ctx context.Context, job prestackbatch.QCJob, overwrite bool) error {
	if _, err := os.Stat(job.Output); err == nil && !overwrite {
		return fmt.Errorf("destination already exists (use --overwrite): %s", job.Output)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	snapshot, err := prestacksession.Load(job.Session)
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	if !strings.EqualFold(filepath.Clean(snapshot.A.Path), filepath.Clean(job.Source.Path)) {
		return errors.New("session source identity differs from batch plan")
	}
	if err := verifyPrestackBatchSource(ctx, snapshot.A.Path, job.Source); err != nil {
		return fmt.Errorf("source A identity: %w", err)
	}
	reader, err := segy.Open(snapshot.A.Path)
	if err != nil {
		return fmt.Errorf("open source A: %w", err)
	}
	defer reader.Close()
	idx, _, err := prestack.BuildIndexCached(ctx, reader, snapshot.AMapping, prestack.CacheOptions{}, nil)
	if err != nil {
		return fmt.Errorf("build metadata index: %w", err)
	}
	selection := snapshot.Selection
	if selection.Type == prestack.GatherRaw && selection.Key == (prestack.GatherKey{}) {
		selection.Key = prestack.GatherKey{Raw: true}
	}
	gather, err := idx.Gather(selection)
	if err != nil {
		return fmt.Errorf("select current gather: %w", err)
	}
	report := prestack.BuildQCReport(idx, gather)
	if err := os.MkdirAll(filepath.Dir(job.Output), 0o755); err != nil {
		return err
	}
	switch job.Format {
	case prestackbatch.ReportCSV:
		if err := report.WriteCSV(job.Output); err != nil {
			return err
		}
	case prestackbatch.ReportJSON:
		if err := report.WriteJSON(job.Output); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported QC report format %q", job.Format)
	}
	return nil
}

// verifyPrestackBatchSource is intentionally stricter than a normal UI
// restore. A batch plan is a reproducibility contract: if the source moved,
// changed size/mtime, or has a recorded hash mismatch, do not publish a QC
// report. BuildIndexCached performs its own before/after identity gate while
// scanning; this check closes the gap before the Reader is opened.
func verifyPrestackBatchSource(ctx context.Context, path string, want prestackbatch.SourceIdentity) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("source is not a regular file")
	}
	if info.Size() != want.Size || (want.ModifiedUnixNano > 0 && info.ModTime().UnixNano() != want.ModifiedUnixNano) {
		return errors.New("source size or modification time differs from the batch plan")
	}
	if strings.TrimSpace(want.SHA256) != "" {
		sum, err := figurebundle.HashFile(ctx, path)
		if err != nil {
			return err
		}
		if !strings.EqualFold(sum, want.SHA256) {
			return errors.New("source SHA-256 differs from the batch plan")
		}
	}
	return nil
}
