package segy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func makeExportSynthetic(t *testing.T, path string, bias float32) {
	t.Helper()
	const ns = 32
	const ntr = 6
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 3600)
	binary.BigEndian.PutUint16(header[3216:3218], 2000)
	binary.BigEndian.PutUint16(header[3218:3220], 2000)
	binary.BigEndian.PutUint16(header[3220:3222], ns)
	binary.BigEndian.PutUint16(header[3222:3224], ns)
	binary.BigEndian.PutUint16(header[3224:3226], 5)
	if _, err = f.Write(header); err != nil {
		t.Fatal(err)
	}
	for tr := 0; tr < ntr; tr++ {
		raw := make([]byte, 240+ns*4)
		binary.BigEndian.PutUint16(raw[114:116], ns)
		binary.BigEndian.PutUint16(raw[116:118], 2000)
		binary.BigEndian.PutUint32(raw[188:192], uint32(100+tr/3))
		binary.BigEndian.PutUint32(raw[192:196], uint32(200+tr%3))
		for i := 0; i < ns; i++ {
			v := float32(tr*100+i) + bias
			binary.BigEndian.PutUint32(raw[240+i*4:244+i*4], math.Float32bits(v))
		}
		if _, err = f.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExportSectionWindow(t *testing.T) {
	d := t.TempDir()
	src := d + "/src.sgy"
	out := d + "/out.sgy"
	makeExportSynthetic(t, src, 0)
	s, err := Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ExportSection(out, SectionExportOptions{TraceIndices: []int64{1, 3, 5}, SampleStart: 5, SampleEnd: 14}); err != nil {
		t.Fatal(err)
	}
	e, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e.Info.TraceCount != 3 || e.Info.SamplesPerTrace != 10 || e.Info.FormatCode != 5 {
		t.Fatalf("bad exported info: %+v", e.Info)
	}
	v, err := e.ReadSample(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(v-305) > 1e-6 {
		t.Fatalf("expected 305, got %g", v)
	}
	h, err := e.rawTraceHeader(0)
	if err != nil {
		t.Fatal(err)
	}
	if got := signed16(h[108:110], e.Info.Endian); got != 10 {
		t.Fatalf("delay time expected 10 ms, got %d", got)
	}
}

func TestExportDifferenceSection(t *testing.T) {
	d := t.TempDir()
	aPath, bPath, out := d+"/a.sgy", d+"/b.sgy", d+"/diff.sgy"
	makeExportSynthetic(t, aPath, 0)
	makeExportSynthetic(t, bPath, 2.5)
	a, err := Open(aPath)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(bPath)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tr := []int64{0, 2, 4}
	if err = ExportDifferenceSection(out, a, b, tr, tr, 0, 7, nil); err != nil {
		t.Fatal(err)
	}
	diff, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer diff.Close()
	if diff.Info.TraceCount != 3 || diff.Info.SamplesPerTrace != 8 || diff.Info.FormatCode != 5 {
		t.Fatalf("bad diff info: %+v", diff.Info)
	}
	v, err := diff.ReadSample(2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(v+2.5) > 1e-5 {
		t.Fatalf("expected -2.5, got %g", v)
	}
}

func putExportTestU32(dst []byte, value uint32, endian Endian) {
	if endian == Big {
		binary.BigEndian.PutUint32(dst, value)
	} else {
		binary.LittleEndian.PutUint32(dst, value)
	}
}

func makeRawExportSynthetic(t *testing.T, path string, endian Endian, formatCode int) {
	t.Helper()
	const ns = 12
	const ntr = 4
	bps := bytesPerSample(formatCode)
	if bps == 0 {
		t.Fatalf("unsupported test format %d", formatCode)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 3600)
	for i := 0; i < 3200; i++ {
		header[i] = byte((i*17 + formatCode) % 251)
	}
	putU16(header[3216:3218], 2000, endian)
	putU16(header[3218:3220], 2000, endian)
	putU16(header[3220:3222], ns, endian)
	putU16(header[3222:3224], ns, endian)
	putU16(header[3224:3226], uint16(formatCode), endian)
	if _, err = f.Write(header); err != nil {
		t.Fatal(err)
	}
	for trace := 0; trace < ntr; trace++ {
		raw := make([]byte, 240+ns*bps)
		putExportTestU32(raw[20:24], uint32(7000+trace), endian) // CDP
		putI16(raw[70:72], -100, endian)                         // coordinate scalar
		putExportTestU32(raw[72:76], uint32(284000+trace*25), endian)
		putExportTestU32(raw[76:80], uint32(425000+trace*30), endian)
		putI16(raw[108:110], 7, endian)
		putU16(raw[114:116], ns, endian)
		putU16(raw[116:118], 2000, endian)
		putExportTestU32(raw[188:192], uint32(400+trace), endian)
		putExportTestU32(raw[192:196], uint32(800+trace), endian)
		for i := 0; i < ns*bps; i++ {
			raw[240+i] = byte((trace*43 + i*11 + formatCode*7) % 251)
		}
		if _, err = f.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExportSectionPreservesRawFormatsEndianAndGeometry(t *testing.T) {
	tests := []struct {
		name       string
		endian     Endian
		formatCode int
	}{
		{name: "ibm_big", endian: Big, formatCode: 1},
		{name: "ibm_little", endian: Little, formatCode: 1},
		{name: "ieee_big", endian: Big, formatCode: 5},
		{name: "ieee_little", endian: Little, formatCode: 5},
		{name: "integer32_big", endian: Big, formatCode: 2},
		{name: "integer16_little", endian: Little, formatCode: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			sourcePath := filepath.Join(dir, "source.sgy")
			outputPath := filepath.Join(dir, "cropped.sgy")
			makeRawExportSynthetic(t, sourcePath, tt.endian, tt.formatCode)
			source, err := Open(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			selected := []int64{3, 1}
			if err = source.ExportSection(outputPath, SectionExportOptions{
				TraceIndices: selected,
				SampleStart:  3,
				SampleEnd:    8,
			}); err != nil {
				t.Fatal(err)
			}
			output, err := Open(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			if output.Info.Endian != tt.endian || output.Info.FormatCode != tt.formatCode || output.Info.SamplesPerTrace != 6 {
				t.Fatalf("unexpected output info: %+v", output.Info)
			}

			sourceFile, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			outputFile, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(sourceFile[:3200], outputFile[:3200]) {
				t.Fatal("textual header changed")
			}
			for outputTrace, sourceTrace := range selected {
				sourceHeader, err := source.rawTraceHeader(sourceTrace)
				if err != nil {
					t.Fatal(err)
				}
				outputHeader, err := output.rawTraceHeader(int64(outputTrace))
				if err != nil {
					t.Fatal(err)
				}
				for _, span := range [][2]int{{20, 24}, {70, 80}, {188, 196}} {
					if !bytes.Equal(sourceHeader[span[0]:span[1]], outputHeader[span[0]:span[1]]) {
						t.Fatalf("trace %d geometry bytes %d:%d changed", sourceTrace, span[0], span[1])
					}
				}
				sourceSamples, err := source.rawTraceWindow(sourceTrace, 3, 8)
				if err != nil {
					t.Fatal(err)
				}
				outputSamples, err := output.rawTraceWindow(int64(outputTrace), 0, 5)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(sourceSamples, outputSamples) {
					t.Fatalf("trace %d sample payload was decoded or changed", sourceTrace)
				}
			}
		})
	}
}

func assertNoSectionExportArtifacts(t *testing.T, dir, targetBase string) {
	t.Helper()
	for _, pattern := range []string{
		filepath.Join(dir, "."+targetBase+".export-*"),
		filepath.Join(dir, "."+targetBase+".backup-*"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("incomplete export artifacts remain: %v", matches)
		}
	}
}

func TestExportSectionCancellationPreservesDestinationAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sgy")
	targetPath := filepath.Join(dir, "target.sgy")
	makeRawExportSynthetic(t, sourcePath, Big, 5)
	oldDestination := []byte("existing destination must survive cancellation")
	if err := os.WriteFile(targetPath, oldDestination, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	ctx, cancel := context.WithCancel(context.Background())
	err = source.ExportSection(targetPath, SectionExportOptions{
		TraceIndices: []int64{0, 1, 2, 3},
		SampleStart:  0,
		SampleEnd:    11,
		Context:      ctx,
		Progress: func(done, total int) {
			if done == 1 {
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	got, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, oldDestination) {
		t.Fatal("cancellation changed the existing destination")
	}
	assertNoSectionExportArtifacts(t, dir, filepath.Base(targetPath))
}

func TestExportSectionRejectsSourceOverwrite(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sgy")
	makeRawExportSynthetic(t, sourcePath, Big, 5)
	before, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	aliases := []string{
		sourcePath,
		filepath.Dir(sourcePath) + string(os.PathSeparator) + "." + string(os.PathSeparator) + filepath.Base(sourcePath),
	}
	if runtime.GOOS == "windows" {
		aliases = append(aliases, strings.ToUpper(sourcePath))
	}
	for _, alias := range aliases {
		if err = source.ExportSection(alias, SectionExportOptions{TraceIndices: []int64{0}, SampleStart: 0, SampleEnd: 2}); err == nil {
			t.Fatalf("source overwrite alias was accepted: %q", alias)
		}
	}
	after, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected source overwrite changed the source SEG-Y")
	}
}

func TestExportSectionReplacementFailureRestoresOldDestination(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sgy")
	targetPath := filepath.Join(dir, "target.sgy")
	makeRawExportSynthetic(t, sourcePath, Little, 3)
	oldDestination := []byte("old destination contents")
	if err := os.WriteFile(targetPath, oldDestination, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	injected := errors.New("injected replacement failure")
	originalRename := exportRenameFile
	exportRenameFile = func(oldPath, newPath string) error {
		if strings.Contains(filepath.Base(oldPath), ".export-") && filepath.Clean(newPath) == filepath.Clean(targetPath) {
			return injected
		}
		return os.Rename(oldPath, newPath)
	}
	defer func() { exportRenameFile = originalRename }()

	err = source.ExportSection(targetPath, SectionExportOptions{
		TraceIndices: []int64{0, 2},
		SampleStart:  1,
		SampleEnd:    7,
	})
	if !errors.Is(err, injected) {
		t.Fatalf("expected injected replacement error, got %v", err)
	}
	got, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, oldDestination) {
		t.Fatal("failed replacement did not restore the old destination")
	}
	assertNoSectionExportArtifacts(t, dir, filepath.Base(targetPath))
}

func TestExportSectionCommittedOutputIsSuccessWhenOldBackupCleanupFails(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sgy")
	targetPath := filepath.Join(dir, "target.sgy")
	makeRawExportSynthetic(t, sourcePath, Big, 5)
	oldDestination := []byte("old destination retained as recovery backup")
	if err := os.WriteFile(targetPath, oldDestination, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()

	originalRemove := exportRemoveFile
	backupRemoves := 0
	exportRemoveFile = func(path string) error {
		if strings.Contains(filepath.Base(path), ".backup-") {
			backupRemoves++
			if backupRemoves == 2 { // first call removes the reservation placeholder
				return errors.New("injected backup cleanup failure")
			}
		}
		return os.Remove(path)
	}
	defer func() { exportRemoveFile = originalRemove }()

	if err = source.ExportSection(targetPath, SectionExportOptions{
		TraceIndices: []int64{1, 3},
		SampleStart:  2,
		SampleEnd:    6,
	}); err != nil {
		t.Fatalf("a committed valid export must not be reported as failed: %v", err)
	}
	output, err := Open(targetPath)
	if err != nil {
		t.Fatalf("committed target is not a valid SEG-Y: %v", err)
	}
	if output.Info.TraceCount != 2 || output.Info.SamplesPerTrace != 5 {
		t.Fatalf("unexpected committed target: %+v", output.Info)
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}

	backups, err := filepath.Glob(filepath.Join(dir, "."+filepath.Base(targetPath)+".backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected one recovery backup, got %v", backups)
	}
	backupContents, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backupContents, oldDestination) {
		t.Fatal("recovery backup does not contain the previous destination")
	}
	exportRemoveFile = originalRemove
	if err = os.Remove(backups[0]); err != nil {
		t.Fatal(err)
	}
	assertNoSectionExportArtifacts(t, dir, filepath.Base(targetPath))
}

func TestExportSectionReadFailureLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sgy")
	targetPath := filepath.Join(dir, "target.sgy")
	makeRawExportSynthetic(t, sourcePath, Big, 1)
	source, err := Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	err = source.ExportSection(targetPath, SectionExportOptions{
		TraceIndices: []int64{0, source.Info.TraceCount},
		SampleStart:  0,
		SampleEnd:    4,
	})
	if err == nil {
		t.Fatal("expected out-of-range trace failure")
	}
	if _, statErr := os.Stat(targetPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial destination exists after failure: %v", statErr)
	}
	assertNoSectionExportArtifacts(t, dir, filepath.Base(targetPath))
}
