package segy

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrSourceChanged means an open Reader no longer describes its source path,
// opening layout, or the expected metadata snapshot. Such a Reader must not
// publish an index/cache result; open the source again before retrying.
var ErrSourceChanged = errors.New("SEG-Y source changed")

// MetadataIdentity is a comparable, read-only identity for metadata caches.
// Fingerprint hashes the raw supported reel-header region (primary text,
// binary, extended text) plus at most 64 uniformly spaced base trace headers,
// including the first and last. It never reads amplitude bytes and is NOT a
// whole-file integrity proof: same-size/restored-mtime changes to unsampled
// trace headers or samples can be undetectable. Offer a cache bypass to users.
// HeaderReads counts only 240-byte trace-header reads, not reel-header blocks.
type MetadataIdentity struct {
	Path            string
	FileSize        int64
	ModTimeUnixNano int64
	Info            Info
	Fingerprint     [sha256.Size]byte
	HeaderReads     int
	TextEncoding    TextEncoding
}

func (s *File) captureMetadataOpen(stat os.FileInfo, binaryHeader []byte) {
	s.metadataOpenStat, s.metadataOpenInfo = stat, s.Info
	// Capture an absolute path now: later working-directory changes must not
	// make a relative path name another file. Preserve ordinary Open behavior
	// if normalization fails; the optional metadata API reports that failure.
	s.metadataOpenPath, _ = filepath.Abs(s.Info.Path)
	copy(s.metadataOpenBinary[:], binaryHeader)
}

func metadataSameSnapshot(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// metadataSourceSnapshot verifies both the retained handle and the live path.
// Info is public for legacy callers; reject a modified layout rather than use
// its offsets to fingerprint unrelated bytes (potentially amplitudes).
func (s *File) metadataSourceSnapshot(ctx context.Context) (os.FileInfo, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if s == nil || s.f == nil {
		return nil, "", errors.New("nil SEG-Y reader")
	}
	if s.metadataOpenStat == nil || s.metadataOpenPath == "" || s.Info != s.metadataOpenInfo {
		return nil, "", fmt.Errorf("%w: reader opening layout unavailable or modified", ErrSourceChanged)
	}
	current, err := s.f.Stat()
	if err != nil {
		return nil, "", err
	}
	pathStat, err := os.Stat(s.metadataOpenPath)
	if err != nil {
		return nil, "", fmt.Errorf("%w: source path: %w", ErrSourceChanged, err)
	}
	if !metadataSameSnapshot(s.metadataOpenStat, current) || !metadataSameSnapshot(current, pathStat) {
		return nil, "", fmt.Errorf("%w: file identity, size or modification time differs", ErrSourceChanged)
	}
	path, err := metadataCanonicalPath(s.f, s.metadataOpenPath)
	if err != nil {
		return nil, "", err
	}
	return current, path, ctx.Err()
}

// MetadataIdentity performs bounded, header-only reads using this Reader's
// fixed-length layout. The caller owns the Reader and must not close it during
// this operation. All returned data are values; no handles/buffers escape.
func (s *File) MetadataIdentity(ctx context.Context) (MetadataIdentity, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	before, path, err := s.metadataSourceSnapshot(ctx)
	if err != nil {
		return MetadataIdentity{}, err
	}
	fingerprint, reads, encoding, err := metadataFingerprint(ctx, s.f, s.metadataOpenInfo, s.metadataOpenBinary)
	if err != nil {
		return MetadataIdentity{}, err
	}
	after, afterPath, err := s.metadataSourceSnapshot(ctx)
	if err != nil {
		return MetadataIdentity{}, err
	}
	if path != afterPath || !metadataSameSnapshot(before, after) {
		return MetadataIdentity{}, fmt.Errorf("%w: changed while reading metadata", ErrSourceChanged)
	}
	info := s.metadataOpenInfo
	info.Path = path
	return MetadataIdentity{Path: path, FileSize: after.Size(), ModTimeUnixNano: after.ModTime().UnixNano(),
		Info: info, Fingerprint: fingerprint, HeaderReads: reads, TextEncoding: encoding}, nil
}

// CheckMetadataIdentity repeats the bounded header fingerprint as well as
// path/handle/opening checks. Unlike a stat-only check this detects sampled
// header edits even when size and modification time have been restored.
func (s *File) CheckMetadataIdentity(ctx context.Context, expected MetadataIdentity) error {
	current, err := s.MetadataIdentity(ctx)
	if err != nil {
		return err
	}
	if current != expected {
		return fmt.Errorf("%w: metadata fingerprint or layout differs", ErrSourceChanged)
	}
	return nil
}

func metadataTraceSamples(total int64) []int64 {
	if total <= 0 {
		return nil
	}
	n := min(total, 64)
	indices := make([]int64, int(n))
	if n == 1 {
		return indices
	}
	// Quotient/remainder formulation avoids multiplication overflow for large
	// files while retaining deterministic floor-spaced first/last support.
	q, r := (total-1)/(n-1), (total-1)%(n-1)
	for i := range indices {
		indices[i] = int64(i)*q + int64(i)*r/(n-1)
	}
	return indices
}

func metadataFingerprint(ctx context.Context, reader io.ReaderAt, info Info, openBinary [400]byte) ([sha256.Size]byte, int, TextEncoding, error) {
	var digest [sha256.Size]byte
	if info.DataStart < 3600 || info.DataStart > info.FileSize || (info.DataStart-3600)%3200 != 0 ||
		info.TraceBytes <= 240 || info.TraceCount <= 0 || info.TraceCount > (info.FileSize-info.DataStart)/info.TraceBytes {
		return digest, 0, TextEncodingUnknown, errors.New("invalid SEG-Y metadata layout")
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("SeisForge SEG-Y metadata fingerprint v1\x00"))
	read := func(raw []byte, offset int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := reader.ReadAt(raw, offset)
		if err != nil {
			return err
		}
		if n != len(raw) {
			return io.ErrUnexpectedEOF
		}
		_, _ = hash.Write(raw)
		return ctx.Err()
	}
	var text [3200]byte
	if err := read(text[:], 0); err != nil {
		return digest, 0, TextEncodingUnknown, err
	}
	encoding := DetectTextEncoding(text[:])
	var reel [400]byte
	if err := read(reel[:], 3200); err != nil {
		return digest, 0, encoding, err
	}
	if reel != openBinary {
		return digest, 0, encoding, fmt.Errorf("%w: binary header differs from Reader opening snapshot", ErrSourceChanged)
	}
	for offset := int64(3600); offset < info.DataStart; offset += int64(len(text)) {
		if err := read(text[:], offset); err != nil {
			return digest, 0, encoding, err
		}
	}
	var raw [240]byte
	var index [8]byte
	reads := 0
	for _, trace := range metadataTraceSamples(info.TraceCount) {
		binary.LittleEndian.PutUint64(index[:], uint64(trace))
		_, _ = hash.Write(index[:])
		if err := read(raw[:], info.DataStart+trace*info.TraceBytes); err != nil {
			return digest, reads, encoding, err
		}
		reads++
	}
	copy(digest[:], hash.Sum(nil))
	return digest, reads, encoding, ctx.Err()
}
