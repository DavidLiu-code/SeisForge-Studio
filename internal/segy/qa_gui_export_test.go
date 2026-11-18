package segy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
	"github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi/artifact"
)

func qaGUIFileSHA256(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func TestQAGUIExportBandDifferenceReadback(t *testing.T) {
	paths := []string{os.Getenv("SEISFORGE_QA_GUI_EXPORT_A"), os.Getenv("SEISFORGE_QA_GUI_EXPORT_B"), os.Getenv("SEISFORGE_QA_GUI_EXPORT_DIFFERENCE")}
	sourcePath := os.Getenv("SEISFORGE_QA_GUI_SOURCE")
	if sourcePath == "" || paths[0] == "" || paths[1] == "" || paths[2] == "" {
		t.Skip("set SEISFORGE_QA_GUI_SOURCE and actual GUI EXPORT_A, EXPORT_B, EXPORT_DIFFERENCE paths")
	}
	sourceHash := qaGUIFileSHA256(t, sourcePath)
	files := make([]*File, len(paths))
	for side, path := range paths {
		f, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		files[side] = f
		if f.Info.TraceCount != 8 || f.Info.SamplesPerTrace != 351 || f.Info.SampleIntervalUS != 2000 || f.Info.FormatCode != 6 {
			t.Fatalf("side %d GUI output is not 8×351 dt2ms IEEE64: %+v", side, f.Info)
		}
	}
	var moduleOutput, moduleInput *artifact.File
	if path := os.Getenv("SEISFORGE_QA_GUI_ARTIFACT_B"); path != "" {
		var err error
		moduleOutput, err = artifact.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer moduleOutput.Close()
		moduleInput, err = artifact.Open(filepath.Join(filepath.Dir(path), "input.sfdata"))
		if err != nil {
			t.Fatal(err)
		}
		defer moduleInput.Close()
		if err := artifact.CheckCompatibility(moduleInput, moduleOutput); err != nil {
			t.Fatalf("recorded B artifact changed input geometry/axis/mask: %v", err)
		}
		meta := moduleOutput.Metadata()
		if moduleOutput.TraceCount() != 8 || moduleOutput.SamplesPerTrace() != 351 || meta.TimeOriginUS+meta.SampleStart*meta.SampleIntervalUS != 800000 || meta.SampleIntervalUS != 2000 {
			t.Fatalf("recorded B artifact does not identify the same GUI section: %+v", meta)
		}
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(path), "request.json"))
		if err != nil {
			t.Fatal(err)
		}
		var request algorithmapi.RunRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
		if request.AlgorithmID != "seisforge.bandpass.zero_phase" || request.InputFingerprint != moduleInput.Fingerprint() || request.Parameters["low_hz"] != 20.0 || request.Parameters["high_hz"] != 162.5 || request.Parameters["order"] != 4.0 || request.Parameters["phase"] != "zero_phase" || request.Parameters["response"] != "legacy_bandpass" {
			t.Fatalf("artifact request does not identify the observed GUI processing run: %+v", request)
		}
		t.Logf("observed artifact path %s; actual run ID %s; algorithm %s version %s; parameters %+v; input fingerprint %s; output fingerprint %s", path, request.RunID, request.AlgorithmID, request.AlgorithmVersion, request.Parameters, moduleInput.Fingerprint(), moduleOutput.Fingerprint())
	}
	var compared, changed int
	var maxDifference float64
	var manualReferenceEnergy, manualResidualEnergy float64
	var manualSamples int
	for trace := int64(0); trace < 8; trace++ {
		var values [3][]float64
		for side, f := range files {
			header, err := f.ReadTraceHeader(trace)
			if err != nil {
				t.Fatal(err)
			}
			if header.Inline != 1001 || header.Crossline != 2000+int32(trace) || header.DelayMS != 800 || header.SampleCount != 351 || header.SampleIntervalUS != 2000 {
				t.Fatalf("side %d trace %d changed actual IL/XL/order/t0/dt/ns: %+v", side, trace, header)
			}
			values[side], err = f.ReadTraceWindow(trace, 0, 350)
			if err != nil || len(values[side]) != 351 {
				t.Fatalf("incomplete side %d trace %d: %v", side, trace, err)
			}
		}
		if moduleOutput != nil {
			geometry := moduleOutput.TraceMetadata()[trace]
			if geometry.Inline != 1001 || geometry.Crossline != 2000+int32(trace) || geometry.TimeOriginUS != 800000 {
				t.Fatalf("artifact geometry disagrees with exported B: %+v", geometry)
			}
			for _, reference := range []struct {
				artifact *artifact.File
				side     int
			}{{moduleInput, 0}, {moduleOutput, 1}} {
				expected, valid, err := reference.artifact.ReadTraceWindow(trace, 0, 351)
				if err != nil {
					t.Fatal(err)
				}
				for sample := range expected {
					if !valid[sample] || math.Float64bits(expected[sample]) != math.Float64bits(values[reference.side][sample]) {
						t.Fatalf("actual module artifact side %d / trace %d / sample %d does not bit-match GUI SEG-Y", reference.side, trace, sample)
					}
				}
			}
		}
		for sample := 0; sample < 351; sample++ {
			a, b, delta := values[0][sample], values[1][sample], values[2][sample]
			if math.IsNaN(a) || math.IsNaN(b) || math.IsNaN(delta) || math.IsInf(a, 0) || math.IsInf(b, 0) || math.IsInf(delta, 0) || math.Float64bits(delta) != math.Float64bits(a-b) {
				t.Fatalf("actual GUI delta trace %d sample %d = %.17g; independently read A−B = %.17g−%.17g", trace, sample, delta, a, b)
			}
			if a != b {
				changed++
			}
			maxDifference = math.Max(maxDifference, math.Abs(delta))
			// The observed GUI analysis selects XL2003 and absolute
			// 0.9–1.2 s: native 800 ms + k·2 ms gives k=50..200,
			// inclusive. Compute the reference/noise energies directly,
			// not via the application's SNR/statistics implementation.
			if trace == 3 && sample >= 50 && sample <= 200 {
				manualReferenceEnergy += b * b
				residual := a - b
				manualResidualEnergy += residual * residual
				manualSamples++
			}
			compared++
		}
	}
	if changed == 0 {
		t.Fatal("processing fixture produced no changed samples; this does not exercise nonzero GUI difference export")
	}
	manualSNR := 10 * math.Log10(manualReferenceEnergy/manualResidualEnergy)
	if manualSamples != 151 || fmt.Sprintf("%.2f", manualSNR) != "16.05" {
		t.Fatalf("observed GUI XL2003 manual 0.9–1.2s result differs: %d samples, sumB²=%.17g, sum(A−B)²=%.17g, SNR=%.17g dB (UI expected 16.05)", manualSamples, manualReferenceEnergy, manualResidualEnergy, manualSNR)
	}
	t.Logf("GUI manual analysis independent formula: IL1001 XL2003, k[50:201] = 900–1200ms inclusive, %d samples; ΣB²=%.17g Σ(A−B)²=%.17g; 10log10 ratio=%.17g dB, rounds to observed UI 16.05dB", manualSamples, manualReferenceEnergy, manualResidualEnergy, manualSNR)
	if after := qaGUIFileSHA256(t, sourcePath); after != sourceHash {
		t.Fatalf("source changed during this read-only check: %s -> %s", sourceHash, after)
	}
	t.Logf("actual GUI A/B/A−B files independently reopened: each 8×351, IL1001, XL2000–2007, t0800ms dt2ms; all %d differences bit-exact A−B, %d changed samples, max|A−B| %.17g", compared, changed, maxDifference)
	for side, path := range paths {
		t.Logf("side %d actual file %s SHA-256 %s", side, path, qaGUIFileSHA256(t, path))
	}
	t.Logf("source hash unchanged only for this readback interval (not a pre-GUI claim): %s", sourceHash)
}

// This opt-in check consumes files actually saved through the candidate GUI.
// It neither invokes the exporter nor obtains its expected samples from the
// exporter, renderer, a shared array, or a previously opened Reader.
func TestQAGUIExportAReadback(t *testing.T) {
	sourcePath := os.Getenv("SEISFORGE_QA_GUI_SOURCE")
	exportPath := os.Getenv("SEISFORGE_QA_GUI_EXPORT_A")
	if sourcePath == "" || exportPath == "" {
		t.Skip("set SEISFORGE_QA_GUI_SOURCE and SEISFORGE_QA_GUI_EXPORT_A to candidate GUI files")
	}
	before := qaGUIFileSHA256(t, sourcePath)
	if expected := os.Getenv("SEISFORGE_QA_GUI_SOURCE_SHA256"); expected != "" && !strings.EqualFold(before, expected) {
		t.Fatalf("source changed from the pre-GUI fingerprint: got %s, expected %s", before, expected)
	}
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	output, err := Open(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if output.Info.TraceCount != 8 || output.Info.SamplesPerTrace != 351 || output.Info.SampleIntervalUS != 2000 || output.Info.FormatCode != 6 {
		t.Fatalf("actual GUI output must be 8×351, dt 2000 us, IEEE64: %+v", output.Info)
	}
	if source.Info.SamplesPerTrace != 351 || source.Info.SampleIntervalUS != 2000 {
		t.Fatalf("source sampling axis differs from the GUI acceptance fixture: %+v", source.Info)
	}
	type location struct{ il, xl int32 }
	sourceTraces := make(map[location]int64)
	for trace := int64(0); trace < source.Info.TraceCount; trace++ {
		header, err := source.ReadTraceHeader(trace)
		if err != nil {
			t.Fatal(err)
		}
		key := location{header.Inline, header.Crossline}
		if _, duplicate := sourceTraces[key]; duplicate {
			t.Fatalf("fixture has ambiguous actual geometry %+v", key)
		}
		sourceTraces[key] = trace
	}
	var compared int
	for trace := int64(0); trace < output.Info.TraceCount; trace++ {
		header, err := output.ReadTraceHeader(trace)
		if err != nil {
			t.Fatal(err)
		}
		key := location{1001, 2000 + int32(trace)}
		if header.Inline != key.il || header.Crossline != key.xl || header.DelayMS != 800 || header.SampleCount != 351 || header.SampleIntervalUS != 2000 {
			t.Fatalf("actual output trace %d has wrong geometry or axis: %+v", trace, header)
		}
		sourceTrace, found := sourceTraces[key]
		if !found {
			t.Fatalf("output geometry %+v has no actual source trace", key)
		}
		sourceHeader, err := source.ReadTraceHeader(sourceTrace)
		if err != nil {
			t.Fatal(err)
		}
		if sourceHeader.DelayMS != header.DelayMS || sourceHeader.SourceX != header.SourceX || sourceHeader.SourceY != header.SourceY || sourceHeader.GroupX != header.GroupX || sourceHeader.GroupY != header.GroupY || sourceHeader.CoordinateScalar != header.CoordinateScalar || sourceHeader.CoordinateUnits != header.CoordinateUnits {
			t.Fatalf("trace %+v changed sampling origin or coordinate metadata", key)
		}
		// Read both files independently through real Readers, not the arrays
		// captured while the candidate was rendering/exporting.
		expected, err := source.ReadTraceWindow(sourceTrace, 0, 350)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := output.ReadTraceWindow(trace, 0, 350)
		if err != nil {
			t.Fatal(err)
		}
		if len(expected) != 351 || len(actual) != 351 {
			t.Fatal("Reader returned an incomplete trace")
		}
		for sample := range expected {
			if math.Float64bits(actual[sample]) != math.Float64bits(expected[sample]) {
				t.Fatalf("GUI output %+v sample %d at %d ms = %.17g, source physical trace %d = %.17g", key, sample, 800+2*sample, actual[sample], sourceTrace, expected[sample])
			}
			compared++
		}
	}
	after := qaGUIFileSHA256(t, sourcePath)
	if before != after {
		t.Fatalf("source fingerprint changed during readback: %s -> %s", before, after)
	}
	t.Logf("ACTUAL GUI FILE %s: independently reopened 8×351 (%d exact amplitudes), IL1001 XL2000–2007 in source order, t0=800 ms dt=2 ms last=1500 ms, format6 IEEE64", exportPath, compared)
	t.Logf("source SHA-256 before/after unchanged: %s; output SHA-256 %s", after, qaGUIFileSHA256(t, exportPath))
}
