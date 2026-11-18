package segy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type metadataReadAtFunc func([]byte, int64) (int, error)

func (f metadataReadAtFunc) ReadAt(p []byte, offset int64) (int, error) { return f(p, offset) }

func metadataFixture(t *testing.T, traces, extended int) string {
	t.Helper()
	headers := make([][]byte, traces)
	for i := range headers {
		headers[i] = make([]byte, 240)
		putHeaderI32(headers[i], 9, 1, int32(i+1), Big)
		putHeaderU16(headers[i], 115, 1, 4, Big)
		putHeaderU16(headers[i], 117, 1, 2000, Big)
	}
	texts := make([][]byte, extended)
	for i := range texts {
		texts[i] = makeASCIITextHeader("C 1 EXTENDED METADATA")
	}
	return writeHeaderFixture(t, Big, makeCP037TextHeader("C 1 IDENTITY TEST"), texts, nil, headers, 4)
}

func metadataOpenFixture(t *testing.T, path string) *File {
	t.Helper()
	f, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestMetadataIdentityUniformHeaderOnlyReads(t *testing.T) {
	for _, count := range []int64{0, 1, 2, 63, 64, 65, 68432, 1<<62 - 1} {
		indices := metadataTraceSamples(count)
		if len(indices) != int(min(count, 64)) {
			t.Fatalf("trace count %d sampled %d headers", count, len(indices))
		}
		if count == 0 {
			continue
		}
		if indices[0] != 0 || indices[len(indices)-1] != count-1 {
			t.Fatalf("trace count %d lost first/last support: %v", count, indices)
		}
		for i := 1; i < len(indices); i++ {
			if indices[i] <= indices[i-1] {
				t.Fatal("duplicate or overflowing metadata support")
			}
		}
	}

	f := metadataOpenFixture(t, metadataFixture(t, 65, 2))
	id, err := f.MetadataIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := metadataCanonicalPath(f.f, f.Info.Path)
	if err != nil {
		t.Fatal(err)
	}
	if id.HeaderReads != 64 || id.Path != wantPath || id.Info.Path != wantPath || id.TextEncoding != TextEncodingEBCDICCP037 || id.FileSize != f.Info.FileSize {
		t.Fatalf("unexpected identity: %+v", id)
	}
	var offsets []int64
	bytesRead, calls := 0, 0
	reader := metadataReadAtFunc(func(raw []byte, offset int64) (int, error) {
		calls++
		bytesRead += len(raw)
		if offset >= f.Info.DataStart {
			if len(raw) != 240 || (offset-f.Info.DataStart)%f.Info.TraceBytes != 0 {
				t.Fatalf("fingerprint attempted amplitude/unbounded read at %d, length %d", offset, len(raw))
			}
			offsets = append(offsets, (offset-f.Info.DataStart)/f.Info.TraceBytes)
		} else if offset+int64(len(raw)) > f.Info.DataStart {
			t.Fatal("reel-header read extended into trace data")
		}
		return f.f.ReadAt(raw, offset)
	})
	digest, reads, encoding, err := metadataFingerprint(context.Background(), reader, f.Info, f.metadataOpenBinary)
	if err != nil || digest != id.Fingerprint || reads != 64 || encoding != id.TextEncoding || calls != 68 || bytesRead != 3600+2*3200+64*240 {
		t.Fatalf("header-only fingerprint: reads/calls/bytes=%d/%d/%d err=%v", reads, calls, bytesRead, err)
	}
	if !reflect.DeepEqual(offsets, metadataTraceSamples(f.Info.TraceCount)) || f.mappingTried || len(f.mappingData) != 0 {
		t.Fatal("fingerprint used wrong support or mapped the amplitude file")
	}
	if err := f.CheckMetadataIdentity(nil, id); err != nil {
		t.Fatal(err)
	}
	// The metadata API neither closes its caller's Reader nor changes Info.
	if _, err := f.ReadSample(0, 0); err != nil || f.Info.Path != f.metadataOpenInfo.Path {
		t.Fatalf("identity changed ordinary Reader ownership/Info: %v", err)
	}
	alias := metadataOpenFixture(t, filepath.Join(filepath.Dir(f.Info.Path), ".", filepath.Base(f.Info.Path)))
	again, err := alias.MetadataIdentity(nil)
	if err != nil || again != id {
		t.Fatalf("equivalent source path changed identity: %v", err)
	}
}

func metadataMutateByte(t *testing.T, path string, offset int64, timestamp time.Time) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var raw [1]byte
	if _, err := f.ReadAt(raw[:], offset); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	raw[0] ^= 1
	_, writeErr := f.WriteAt(raw[:], offset)
	syncErr, closeErr := f.Sync(), f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		t.Fatalf("fixture mutation failed: %v / %v / %v", writeErr, syncErr, closeErr)
	}
	if err := os.Chtimes(path, timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataIdentityChangedHeadersAndSamplingLimit(t *testing.T) {
	for _, part := range []string{"text", "extended text", "first", "middle", "last", "binary", "sample", "unsampled header"} {
		t.Run(part, func(t *testing.T) {
			f := metadataOpenFixture(t, metadataFixture(t, 65, 1))
			id, err := f.MetadataIdentity(nil)
			if err != nil {
				t.Fatal(err)
			}
			offset := int64(0)
			switch part {
			case "extended text":
				offset = 3600
			case "first":
				offset = f.Info.DataStart + 8
			case "middle":
				offset = f.Info.DataStart + metadataTraceSamples(65)[32]*f.Info.TraceBytes + 8
			case "last":
				offset = f.Info.DataStart + 64*f.Info.TraceBytes + 8
			case "binary":
				offset = 3230 // non-layout word, still part of the opening snapshot
			case "sample":
				offset = f.Info.DataStart + 240
			case "unsampled header":
				offset = f.Info.DataStart + 63*f.Info.TraceBytes + 8
			}
			metadataMutateByte(t, f.Info.Path, offset, f.metadataOpenStat.ModTime())
			err = f.CheckMetadataIdentity(nil, id)
			if part == "sample" || part == "unsampled header" {
				// Explicitly document the bounded fingerprint's limitation. A
				// cache bypass is required for edits hiding both stat changes
				// and all sampled metadata changes; no integrity claim is made.
				if err != nil {
					t.Fatalf("non-read bytes changed the header fingerprint: %v", err)
				}
			} else if !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("%s mutation accepted: %v", part, err)
			}
			if part == "binary" {
				if _, err := f.MetadataIdentity(nil); !errors.Is(err, ErrSourceChanged) {
					t.Fatalf("preexisting Reader accepted a changed binary header: %v", err)
				}
				fresh := metadataOpenFixture(t, f.Info.Path)
				newID, err := fresh.MetadataIdentity(nil)
				if err != nil || newID == id {
					t.Fatalf("reopened changed file not independently identifiable: %v", err)
				}
			}
		})
	}
}

func TestMetadataIdentityRejectsChangedOpeningSnapshot(t *testing.T) {
	for _, change := range []string{"mtime", "size", "layout", "path replacement", "closed"} {
		t.Run(change, func(t *testing.T) {
			f := metadataOpenFixture(t, metadataFixture(t, 2, 0))
			switch change {
			case "mtime":
				stamp := f.metadataOpenStat.ModTime().Add(3 * time.Second)
				if err := os.Chtimes(f.Info.Path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			case "size":
				if err := os.Truncate(f.Info.Path, f.Info.FileSize-1); err != nil {
					t.Fatal(err)
				}
			case "layout":
				f.Info.TraceBytes++
			case "path replacement":
				// Point the internal live-path probe at a different inode/file
				// with identical length and timestamp. Avoid platform-dependent
				// rename-over-open-handle support while exercising SameFile.
				replacement := metadataFixture(t, 2, 0)
				stamp := f.metadataOpenStat.ModTime()
				if err := os.Chtimes(replacement, stamp, stamp); err != nil {
					t.Fatal(err)
				}
				f.metadataOpenPath = replacement
			case "closed":
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.MetadataIdentity(nil)
			if change == "closed" {
				if err == nil {
					t.Fatalf("closed Reader error lost: %v", err)
				}
			} else if !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("%s Reader was accepted: %v", change, err)
			}
		})
	}
}

type metadataHookContext struct {
	context.Context
	calls int
	hook  func(int)
}

func (c *metadataHookContext) Err() error {
	c.calls++
	c.hook(c.calls)
	return c.Context.Err()
}

func TestMetadataIdentityCancellationAndChangeDuringRead(t *testing.T) {
	f := metadataOpenFixture(t, metadataFixture(t, 65, 3))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.MetadataIdentity(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled identity continued: %v", err)
	}
	for _, cancelAt := range []int{3, 6} { // extended text and first trace header
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		reader := metadataReadAtFunc(func(raw []byte, offset int64) (int, error) {
			calls++
			n, err := f.f.ReadAt(raw, offset)
			if calls == cancelAt {
				cancel()
			}
			return n, err
		})
		_, _, _, err := metadataFingerprint(ctx, reader, f.Info, f.metadataOpenBinary)
		cancel()
		if !errors.Is(err, context.Canceled) || calls != cancelAt {
			t.Fatalf("cancel at %d: calls=%d err=%v", cancelAt, calls, err)
		}
	}
	readFailure := errors.New("test metadata I/O failure")
	_, _, _, err := metadataFingerprint(context.Background(), metadataReadAtFunc(func([]byte, int64) (int, error) {
		return 0, readFailure
	}), f.Info, f.metadataOpenBinary)
	if !errors.Is(err, readFailure) {
		t.Fatalf("metadata I/O failure hidden: %v", err)
	}
	_, _, _, err = metadataFingerprint(context.Background(), metadataReadAtFunc(func(raw []byte, _ int64) (int, error) {
		return len(raw) - 1, nil
	}), f.Info, f.metadataOpenBinary)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short read accepted: %v", err)
	}
	mutated := false
	hook := &metadataHookContext{Context: context.Background(), hook: func(call int) {
		if call == 4 { // after the first text block, before remaining reads
			stamp := f.metadataOpenStat.ModTime().Add(5 * time.Second)
			if err := os.Chtimes(f.Info.Path, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			mutated = true
		}
	}}
	if _, err := f.MetadataIdentity(hook); !mutated || !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("source changed during identity but result published: mutated=%v err=%v", mutated, err)
	}
}
