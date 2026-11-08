// Package prestackbatch defines deterministic, metadata-only batch jobs for
// reproducible Prestack figures and QC reports. It does not read amplitudes or
// own SEG-Y readers; execution is deliberately left to the existing renderer
// and QC exporters.
package prestackbatch

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/figurebundle"
	prestacksession "github.com/DavidLiu-code/SeisForge-Studio/internal/prestacksession"
)

const SchemaVersion = "seisforge.prestack.batch.v1"

var (
	ErrInvalidPlan = errors.New("invalid prestack batch plan")
	ErrPlanExists  = errors.New("prestack batch plan already exists")
)

type JobKind string

const (
	JobFigure JobKind = "figure"
	JobQC     JobKind = "qc"
)

type ReportFormat string

const (
	ReportJSON ReportFormat = "json"
	ReportCSV  ReportFormat = "csv"
)

type SourceIdentity struct {
	Path             string `json:"path"`
	Size             int64  `json:"size"`
	ModifiedUnixNano int64  `json:"modified_unix_nano"`
	SHA256           string `json:"sha256,omitempty"`
}

type FigureJob struct {
	ID      string          `json:"id"`
	Recipe  string          `json:"recipe"`
	Output  string          `json:"output"`
	Result  string          `json:"result"`
	SourceA SourceIdentity  `json:"source_a"`
	SourceB *SourceIdentity `json:"source_b,omitempty"`
}

type QCJob struct {
	ID      string          `json:"id"`
	Session string          `json:"session"`
	Source  SourceIdentity  `json:"source"`
	SourceB *SourceIdentity `json:"source_b,omitempty"`
	Output  string          `json:"output"`
	Format  ReportFormat    `json:"format"`
}

type Plan struct {
	SchemaVersion      string      `json:"schema_version"`
	ApplicationVersion string      `json:"application_version"`
	CreatedUTC         string      `json:"created_utc"`
	OutputDirectory    string      `json:"output_directory"`
	Figures            []FigureJob `json:"figures,omitempty"`
	QCReports          []QCJob     `json:"qc_reports,omitempty"`
}

type FigureInput struct {
	Recipe string
	Output string
	Result string
}

type QCInput struct {
	Session string
	Output  string
	Format  ReportFormat
}

type Input struct {
	ApplicationVersion string
	OutputDirectory    string
	Figures            []FigureInput
	QCReports          []QCInput
}

func BuildPlan(in Input) (Plan, error) {
	if strings.TrimSpace(in.ApplicationVersion) == "" {
		return Plan{}, fmt.Errorf("%w: missing application version", ErrInvalidPlan)
	}
	root, err := filepath.Abs(filepath.Clean(strings.TrimSpace(in.OutputDirectory)))
	if err != nil || strings.TrimSpace(in.OutputDirectory) == "" {
		return Plan{}, fmt.Errorf("%w: invalid output directory", ErrInvalidPlan)
	}
	if filepath.Dir(root) == root {
		return Plan{}, fmt.Errorf("%w: output directory cannot be a filesystem root", ErrInvalidPlan)
	}
	plan := Plan{SchemaVersion: SchemaVersion, ApplicationVersion: in.ApplicationVersion,
		CreatedUTC: time.Now().UTC().Format(time.RFC3339Nano), OutputDirectory: root}
	for _, input := range in.Figures {
		recipePath, err := filepath.Abs(filepath.Clean(strings.TrimSpace(input.Recipe)))
		if err != nil || strings.TrimSpace(input.Recipe) == "" {
			return Plan{}, fmt.Errorf("%w: invalid figure recipe path", ErrInvalidPlan)
		}
		manifest, err := figurebundle.ReadManifest(recipePath)
		if err != nil {
			return Plan{}, fmt.Errorf("%w: read figure recipe %s: %v", ErrInvalidPlan, recipePath, err)
		}
		output, err := planTarget(root, input.Output, fmt.Sprintf("figure-%03d.png", len(plan.Figures)+1))
		if err != nil {
			return Plan{}, err
		}
		result, err := planTarget(root, input.Result, filepath.Base(output)+".result.json")
		if err != nil {
			return Plan{}, err
		}
		job := FigureJob{ID: fmt.Sprintf("figure-%03d", len(plan.Figures)+1), Recipe: recipePath, Output: output, Result: result,
			SourceA: sourceIdentity(manifest.SourceA)}
		if manifest.SourceB != nil {
			b := sourceIdentity(*manifest.SourceB)
			job.SourceB = &b
		}
		plan.Figures = append(plan.Figures, job)
	}
	for _, input := range in.QCReports {
		sessionPath, err := filepath.Abs(filepath.Clean(strings.TrimSpace(input.Session)))
		if err != nil || strings.TrimSpace(input.Session) == "" {
			return Plan{}, fmt.Errorf("%w: invalid session path", ErrInvalidPlan)
		}
		snapshot, err := prestacksession.Load(sessionPath)
		if err != nil {
			return Plan{}, fmt.Errorf("%w: read session %s: %v", ErrInvalidPlan, sessionPath, err)
		}
		format := input.Format
		if format == "" {
			format = ReportJSON
		}
		if format != ReportJSON && format != ReportCSV {
			return Plan{}, fmt.Errorf("%w: unsupported QC format %q", ErrInvalidPlan, format)
		}
		ext := ".json"
		if format == ReportCSV {
			ext = ".csv"
		}
		output, err := planTarget(root, input.Output, fmt.Sprintf("qc-%03d%s", len(plan.QCReports)+1, ext))
		if err != nil {
			return Plan{}, err
		}
		job := QCJob{ID: fmt.Sprintf("qc-%03d", len(plan.QCReports)+1), Session: sessionPath,
			Source: sourceIdentityFromSession(snapshot.A), Output: output, Format: format}
		if snapshot.Compare.BSource != nil {
			b := sourceIdentityFromSession(*snapshot.Compare.BSource)
			job.SourceB = &b
		}
		plan.QCReports = append(plan.QCReports, job)
	}
	sort.SliceStable(plan.Figures, func(i, j int) bool {
		if plan.Figures[i].Recipe != plan.Figures[j].Recipe {
			return plan.Figures[i].Recipe < plan.Figures[j].Recipe
		}
		return plan.Figures[i].Output < plan.Figures[j].Output
	})
	sort.SliceStable(plan.QCReports, func(i, j int) bool {
		if plan.QCReports[i].Session != plan.QCReports[j].Session {
			return plan.QCReports[i].Session < plan.QCReports[j].Session
		}
		return plan.QCReports[i].Output < plan.QCReports[j].Output
	})
	for i := range plan.Figures {
		plan.Figures[i].ID = fmt.Sprintf("figure-%03d", i+1)
	}
	for i := range plan.QCReports {
		plan.QCReports[i].ID = fmt.Sprintf("qc-%03d", i+1)
	}
	if err := Validate(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func sourceIdentity(source figurebundle.SourceIdentity) SourceIdentity {
	return SourceIdentity{Path: filepath.Clean(source.Path), Size: source.Size, ModifiedUnixNano: source.ModifiedUnixNano, SHA256: strings.ToLower(source.SHA256)}
}

func sourceIdentityFromSession(source prestacksession.SourceIdentity) SourceIdentity {
	return SourceIdentity{Path: filepath.Clean(source.Path), Size: source.Size, ModifiedUnixNano: source.ModTimeNS, SHA256: strings.ToLower(source.SHA256)}
}

func planTarget(root, requested, fallback string) (string, error) {
	name := strings.TrimSpace(requested)
	if name == "" {
		name = fallback
	}
	if filepath.IsAbs(name) || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("%w: output target must be relative", ErrInvalidPlan)
	}
	target := filepath.Clean(filepath.Join(root, name))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: output escapes output directory", ErrInvalidPlan)
	}
	return target, nil
}

func Validate(p Plan) error {
	if p.SchemaVersion != SchemaVersion || strings.TrimSpace(p.ApplicationVersion) == "" || strings.TrimSpace(p.OutputDirectory) == "" {
		return fmt.Errorf("%w: invalid schema or plan identity", ErrInvalidPlan)
	}
	root, err := filepath.Abs(filepath.Clean(p.OutputDirectory))
	if err != nil || filepath.Dir(root) == root {
		return fmt.Errorf("%w: invalid output directory", ErrInvalidPlan)
	}
	seen := map[string]struct{}{}
	seenIDs := map[string]struct{}{}
	checkID := func(id string) error {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("%w: empty job id", ErrInvalidPlan)
		}
		if _, ok := seenIDs[id]; ok {
			return fmt.Errorf("%w: duplicate job id %q", ErrInvalidPlan, id)
		}
		seenIDs[id] = struct{}{}
		return nil
	}
	checkTarget := func(target string) error {
		if strings.TrimSpace(target) == "" {
			return fmt.Errorf("%w: empty output target", ErrInvalidPlan)
		}
		abs, err := filepath.Abs(filepath.Clean(target))
		if err != nil {
			return fmt.Errorf("%w: invalid output target", ErrInvalidPlan)
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: output escapes output directory", ErrInvalidPlan)
		}
		key := strings.ToLower(abs)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%w: duplicate output target", ErrInvalidPlan)
		}
		seen[key] = struct{}{}
		return nil
	}
	for _, job := range p.Figures {
		if job.ID == "" || job.Recipe == "" || job.SourceA.Path == "" {
			return fmt.Errorf("%w: incomplete figure job", ErrInvalidPlan)
		}
		if err := checkID(job.ID); err != nil {
			return err
		}
		if err := validateSource(job.SourceA); err != nil {
			return err
		}
		if job.SourceB != nil {
			if err := validateSource(*job.SourceB); err != nil {
				return err
			}
		}
		if err := checkTarget(job.Output); err != nil {
			return err
		}
		if err := checkTarget(job.Result); err != nil {
			return err
		}
	}
	for _, job := range p.QCReports {
		if job.ID == "" || job.Session == "" || job.Source.Path == "" || (job.Format != ReportJSON && job.Format != ReportCSV) {
			return fmt.Errorf("%w: incomplete QC job", ErrInvalidPlan)
		}
		if err := checkID(job.ID); err != nil {
			return err
		}
		if err := checkTarget(job.Output); err != nil {
			return err
		}
		if err := validateSource(job.Source); err != nil {
			return err
		}
		if job.SourceB != nil {
			if err := validateSource(*job.SourceB); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSource(source SourceIdentity) error {
	if strings.TrimSpace(source.Path) == "" || source.Size < 0 || source.ModifiedUnixNano < 0 {
		return fmt.Errorf("%w: invalid source identity", ErrInvalidPlan)
	}
	if source.SHA256 != "" {
		if len(source.SHA256) != 64 {
			return fmt.Errorf("%w: invalid source SHA-256", ErrInvalidPlan)
		}
		if _, err := hex.DecodeString(source.SHA256); err != nil {
			return fmt.Errorf("%w: invalid source SHA-256", ErrInvalidPlan)
		}
	}
	return nil
}

func (p Plan) Clone() Plan {
	p.Figures = append([]FigureJob(nil), p.Figures...)
	p.QCReports = append([]QCJob(nil), p.QCReports...)
	for i := range p.QCReports {
		if p.QCReports[i].SourceB != nil {
			b := *p.QCReports[i].SourceB
			p.QCReports[i].SourceB = &b
		}
	}
	for i := range p.Figures {
		if p.Figures[i].SourceB != nil {
			b := *p.Figures[i].SourceB
			p.Figures[i].SourceB = &b
		}
	}
	return p
}

func Save(path string, plan Plan, overwrite bool) error {
	if err := Validate(plan); err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: empty plan path", ErrInvalidPlan)
	}
	path = filepath.Clean(path)
	if _, err := os.Stat(path); err == nil && !overwrite {
		return ErrPlanExists
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".prestack-batch-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(append(b, '\n')); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return publishPlan(tmpName, path, overwrite)
}

func Load(path string) (Plan, error) {
	f, err := os.Open(path)
	if err != nil {
		return Plan{}, err
	}
	defer f.Close()
	var plan Plan
	dec := json.NewDecoder(io.LimitReader(f, 32<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("%w: decode: %v", ErrInvalidPlan, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Plan{}, fmt.Errorf("%w: trailing JSON", ErrInvalidPlan)
	}
	if err := Validate(plan); err != nil {
		return Plan{}, err
	}
	return plan.Clone(), nil
}
