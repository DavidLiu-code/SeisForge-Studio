package pseudocache

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	geometrycore "github.com/DavidLiu-code/SeisForge-Studio/internal/geometry"
	pseudo3dcore "github.com/DavidLiu-code/SeisForge-Studio/internal/pseudo3d"
)

const (
	Version         = 1
	DefaultMaxBytes = int64(1 << 30)
	fileExtension   = ".ptx"
	maxMetadataSize = int64(64 << 20)
)

var fileMagic = [16]byte{'L', 'I', 'M', 'A', 'G', 'E', '-', 'P', 'S', 'E', 'U', 'D', 'O', '-', 'T', 'X'}

type CacheKey struct {
	SourcePath            string
	FileSize              int64
	ModTimeUnixNano       int64
	DataStart, TraceBytes int64
	TraceCount            int64
	SamplesPerTrace       int
	SampleIntervalUS      int
	FormatCode            int
	BytesPerSample        int
	Endian                int
	CoordinateSource      string
	XByte, YByte          int
	CDPByte               int
	ScalarByte            int
	UnitsByte             int
	NavigationFingerprint string
	// CanonicalGeometryFingerprint identifies the final world-coordinate
	// trajectory used to place the curtain.  It is intentionally separate
	// from NavigationFingerprint: DAT navigation can stay unchanged while a
	// header-coordinate calibration (scale/translation) or trace mapping
	// changes the canonical geometry.
	CanonicalGeometryFingerprint string
	AlgorithmVersion             int
	Width, Height                int
	GainPercent                  float64
	ClipPercent                  float64
	AGC                          bool
	DisplayMode                  int
	SampleStart, SampleEnd       int
}

func (key CacheKey) normalized() CacheKey {
	if absolute, err := filepath.Abs(key.SourcePath); err == nil {
		key.SourcePath = absolute
	}
	key.SourcePath = strings.ToLower(filepath.Clean(key.SourcePath))
	if key.AlgorithmVersion <= 0 {
		key.AlgorithmVersion = 1
	}
	return key
}

func (key CacheKey) Digest() (string, error) {
	encoded, err := json.Marshal(key.normalized())
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:16]), nil
}

type Entry struct {
	Geometry                   geometrycore.CrookedLineGeometry
	Points                     []pseudo3dcore.Point
	U                          []float64
	TraceIndices               []int64
	Positions                  []float64
	PositionStart, PositionEnd float64
	Length, Multiplier         float64
	CalibrationAccepted        bool
	Width, Height              int
	Indices                    []byte
}

func (entry Entry) Valid() bool {
	return entry.Width >= 2 && entry.Height >= 2 && len(entry.Indices) == entry.Width*entry.Height &&
		len(entry.Geometry.TraceIndices) >= 2 && len(entry.Points) >= 2 && len(entry.Points) == len(entry.U) &&
		len(entry.TraceIndices) >= 2 && len(entry.TraceIndices) == len(entry.Positions) && entry.PositionEnd > entry.PositionStart
}

type recordMetadata struct {
	KeyDigest string
	Entry     Entry
}

type Cache struct {
	Root     string
	MaxBytes int64
	mu       sync.Mutex
}

func New(root string, maxBytes int64) (*Cache, error) {
	if root == "" {
		base, err := os.UserCacheDir()
		if err != nil || base == "" {
			base = os.TempDir()
		}
		root = filepath.Join(base, "Limage", "pseudo_textures")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Cache{Root: root, MaxBytes: maxBytes}, nil
}

func (cache *Cache) pathFor(key CacheKey) (string, string, error) {
	if cache == nil || cache.Root == "" {
		return "", "", errors.New("nil pseudo texture cache")
	}
	digest, err := key.Digest()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(cache.Root, digest+fileExtension), digest, nil
}

func encodeMetadata(digest string, entry Entry) ([]byte, error) {
	metadataEntry := entry
	metadataEntry.Indices = nil
	var buffer bytes.Buffer
	if err := gob.NewEncoder(&buffer).Encode(recordMetadata{KeyDigest: digest, Entry: metadataEntry}); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func decodeEntry(path, digest string) (Entry, error) {
	file, err := os.Open(path)
	if err != nil {
		return Entry{}, err
	}
	defer file.Close()
	var magic [16]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil || magic != fileMagic {
		return Entry{}, errors.New("invalid pseudo texture cache magic")
	}
	var version uint32
	var metadataLength, payloadLength uint64
	var checksum uint32
	for _, value := range []any{&version, &metadataLength, &payloadLength, &checksum} {
		if err := binary.Read(file, binary.LittleEndian, value); err != nil {
			return Entry{}, err
		}
	}
	if version != Version || metadataLength == 0 || metadataLength > uint64(maxMetadataSize) || payloadLength == 0 || payloadLength > uint64(DefaultMaxBytes) {
		return Entry{}, errors.New("invalid pseudo texture cache header")
	}
	metadataBytes := make([]byte, int(metadataLength))
	if _, err := io.ReadFull(file, metadataBytes); err != nil {
		return Entry{}, err
	}
	var metadata recordMetadata
	if err := gob.NewDecoder(bytes.NewReader(metadataBytes)).Decode(&metadata); err != nil || metadata.KeyDigest != digest {
		return Entry{}, errors.New("invalid pseudo texture cache metadata")
	}
	payload := make([]byte, int(payloadLength))
	if _, err := io.ReadFull(file, payload); err != nil || crc32.ChecksumIEEE(payload) != checksum {
		return Entry{}, errors.New("invalid pseudo texture cache checksum")
	}
	metadata.Entry.Indices = payload
	if !metadata.Entry.Valid() {
		return Entry{}, errors.New("invalid pseudo texture cache entry")
	}
	return metadata.Entry, nil
}

func (cache *Cache) Load(key CacheKey) (Entry, bool, error) {
	path, digest, err := cache.pathFor(key)
	if err != nil {
		return Entry{}, false, err
	}
	// Cache records are immutable after their atomic rename. Concurrent loads
	// therefore do not need the writer/pruner mutex and can saturate the two
	// pseudo line workers on a warm project open.
	entry, err := decodeEntry(path, digest)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Entry{}, false, nil
		}
		_ = os.Remove(path)
		return Entry{}, false, nil
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return entry, true, nil
}

func (cache *Cache) Store(key CacheKey, entry Entry) error {
	if !entry.Valid() {
		return errors.New("invalid pseudo texture cache entry")
	}
	path, digest, err := cache.pathFor(key)
	if err != nil {
		return err
	}
	metadata, err := encodeMetadata(digest, entry)
	if err != nil {
		return err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return cache.pruneLocked()
	}
	file, err := os.CreateTemp(cache.Root, digest+"-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	_ = file.Chmod(0o600)
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(temporary)
		}
	}()
	for _, value := range []any{fileMagic, uint32(Version), uint64(len(metadata)), uint64(len(entry.Indices)), crc32.ChecksumIEEE(entry.Indices)} {
		if err := binary.Write(file, binary.LittleEndian, value); err != nil {
			return err
		}
	}
	if _, err := file.Write(metadata); err != nil {
		return err
	}
	if _, err := file.Write(entry.Indices); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		// Another Limage process may have committed the same immutable key.
		// In that case its completed record wins and this temporary is simply
		// discarded; no partial destination is ever exposed.
		if _, statErr := os.Stat(path); statErr == nil {
			_ = os.Remove(temporary)
			ok = true
			return cache.pruneLocked()
		}
		return err
	}
	ok = true
	return cache.pruneLocked()
}

func (cache *Cache) Prune() error {
	if cache == nil {
		return errors.New("nil pseudo texture cache")
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.pruneLocked()
}

func (cache *Cache) pruneLocked() error {
	entries, err := os.ReadDir(cache.Root)
	if err != nil {
		return err
	}
	type candidate struct {
		path    string
		size    int64
		modTime time.Time
	}
	files := make([]candidate, 0, len(entries))
	total := int64(0)
	for _, directoryEntry := range entries {
		path := filepath.Join(cache.Root, directoryEntry.Name())
		if directoryEntry.IsDir() {
			continue
		}
		if strings.HasSuffix(directoryEntry.Name(), ".tmp") {
			_ = os.Remove(path)
			continue
		}
		if !strings.HasSuffix(strings.ToLower(directoryEntry.Name()), fileExtension) {
			continue
		}
		info, err := directoryEntry.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		files = append(files, candidate{path: path, size: info.Size(), modTime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	for _, file := range files {
		if total <= cache.MaxBytes {
			break
		}
		if err := os.Remove(file.path); err == nil {
			total -= file.size
		}
	}
	return nil
}
