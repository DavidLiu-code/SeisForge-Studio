package provenance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
)

func TestRecordAtomicRoundtripAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	record := api.AlgorithmRunRecord{Schema: api.RunRecordSchema, RunID: "run-1", AlgorithmID: "demo", AlgorithmVersion: "1.0.0", ProtocolVersion: api.ProtocolVersion, Parameters: map[string]any{"rank": float64(2)}, ManifestSHA256: "manifest", ExecutableSHA256: "exe", InputFingerprint: "input", OutputFingerprint: "output", Status: "success", StartedAt: now, FinishedAt: now.Add(time.Second), DurationMS: 1000}
	path, err := Save(dir, record)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed api.AlgorithmRunRecord
	if err = json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.AlgorithmID != record.AlgorithmID || parsed.InputFingerprint != "input" || parsed.OutputFingerprint != "output" || parsed.DurationMS != 1000 {
		t.Fatal("record lost metadata")
	}
	for _, forbidden := range []string{"samples", "amplitudes", "Reader", "HWND", "token"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("forbidden field %s", forbidden)
		}
	}
	if _, err = Save(dir, record); err == nil {
		t.Fatal("overwrote existing run")
	}
	again, _ := os.ReadFile(path)
	if string(again) != string(data) {
		t.Fatal("existing record changed")
	}
	files, _ := filepath.Glob(filepath.Join(dir, ".run-*.tmp"))
	if len(files) != 0 {
		t.Fatal("temporary record leaked")
	}
	record.RunID = "../escape"
	if _, err = Save(dir, record); err == nil {
		t.Fatal("accepted unsafe run ID")
	}
}
