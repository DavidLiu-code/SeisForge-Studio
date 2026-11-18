//go:build windows

package main

import (
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segyanalysis"
)

func TestTraceScientificStatsTableScopeSNRAndMissingCells(t *testing.T) {
	a := segyanalysis.TraceInput{Role: "A", StartUS: 800000, IntervalUS: 2000, Samples: make([]float64, 351)}
	b := segyanalysis.TraceInput{Role: "B", StartUS: 800000, IntervalUS: 2000, Samples: make([]float64, 351)}
	for i := range a.Samples {
		a.Samples[i], b.Samples[i] = float64(i%3)-1, (float64(i%3)-1)*.5
	}
	s := segyanalysis.BuildTraceSnapshot([]segyanalysis.TraceInput{a, b}, true)
	r := &traceAnalysisResult{snapshot: &s, selection: traceAnalysisSelection{Context: "IL 1 / XL 2 · A 已滤波"}}
	d := makeTraceStatsTable(r)
	if !strings.Contains(d.snr, "0.00 dB · 351 对有效样点") || !strings.Contains(d.snr, "B：干净参考") || !strings.Contains(d.context, "0.8–1.5 s") || !strings.Contains(d.identity, "A 已滤波") || !strings.Contains(d.note, referenceSNRHelp) {
		t.Fatalf("lost SNR context: %+v", d)
	}
	copyText := traceScientificStatsText(r)
	if !strings.Contains(copyText, "指标\tA\tB\tA−B") || !strings.Contains(copyText, "样点数\t351\t351\t351") || strings.Count(copyText, "Nyquist") != 1 {
		t.Fatalf("copy did not preserve compact comparison: %s", copyText)
	}
	r.diffNote = "B 在此 IL/XL 没有对应道"
	s = segyanalysis.BuildTraceSnapshot([]segyanalysis.TraceInput{a}, true)
	copyText = traceScientificStatsText(r)
	if !strings.Contains(copyText, "—（B 在此 IL/XL 没有对应道）") || !strings.Contains(copyText, "样点数\t351\t—\t—") {
		t.Fatalf("missing B looked like data: %s", copyText)
	}
}
