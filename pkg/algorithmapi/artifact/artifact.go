// Package artifact implements the public .sfdata v1 interchange format.
// Arrays and per-trace geometry are binary blocks, never JSON sample arrays.
package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
)

const Version uint32 = 1
const Float32 = "float32"
const Float64 = "float64"
const headerSize = 192
const traceSize = 48
const maxMetadata = 1 << 20
const maxTraces = 20_000_000

var magic = [8]byte{'S', 'F', 'D', 'A', 'T', 'A', '1', 0}

type Metadata struct {
	DType            string   `json:"dtype"`
	Shape            []uint64 `json:"shape"`
	Axes             []string `json:"axes"`
	SampleIntervalUS int64    `json:"sample_interval_us"`
	TimeOriginUS     int64    `json:"time_origin_us"`
	SampleStart      int64    `json:"sample_start"`
}
type TraceMetadata struct {
	TraceID           int64
	Inline, Crossline int32
	X, Y              float64
	TimeOriginUS      int64
	CoordinateValid   bool
}
type ReadTraceFunc func(trace int64) (values []float64, valid []bool, err error)

type File struct {
	f                      *os.File
	meta                   Metadata
	traces                 []TraceMetadata
	count, samples         int64
	maskOffset, dataOffset int64
	width                  int64
	fingerprint            string
	maskHash               [32]byte
}
type layout struct{ count, samples, width, elements, geometry, mask, data, total int64 }

func validate(m Metadata) (layout, error) {
	var l layout
	if m.DType == Float32 {
		l.width = 4
	} else if m.DType == Float64 {
		l.width = 8
	} else {
		return l, errors.New("unsupported artifact dtype")
	}
	if len(m.Shape) != 2 && len(m.Shape) != 3 {
		return l, errors.New("artifact must have two or three dimensions")
	}
	if len(m.Axes) != len(m.Shape) {
		return l, errors.New("axis/shape rank mismatch")
	}
	expected := []string{"trace", "sample"}
	if len(m.Shape) == 3 {
		expected = []string{"inline", "crossline", "sample"}
	}
	if !reflect.DeepEqual(m.Axes, expected) {
		return l, errors.New("unsupported artifact axis semantics")
	}
	product := uint64(1)
	for _, n := range m.Shape {
		if n == 0 || n > math.MaxInt64 || product > math.MaxInt64/n {
			return l, errors.New("artifact shape overflow")
		}
		product *= n
	}
	l.samples = int64(m.Shape[len(m.Shape)-1])
	l.elements = int64(product)
	l.count = l.elements / l.samples
	if l.count > maxTraces {
		return l, errors.New("artifact trace metadata exceeds supported limit")
	}
	if l.samples > math.MaxInt32 {
		return l, errors.New("artifact trace window exceeds supported limit")
	}
	if m.SampleIntervalUS <= 0 || m.SampleStart < 0 || l.samples > math.MaxInt64/m.SampleIntervalUS {
		return l, errors.New("invalid artifact sample axis")
	}
	if m.SampleStart > math.MaxInt64-(l.samples-1) || m.SampleStart+l.samples-1 > math.MaxInt64/m.SampleIntervalUS {
		return l, errors.New("artifact sample time overflow")
	}
	lastTimeOffset := (m.SampleStart + l.samples - 1) * m.SampleIntervalUS
	if m.TimeOriginUS > math.MaxInt64-lastTimeOffset {
		return l, errors.New("artifact time origin overflow")
	}
	if l.elements > math.MaxInt64/l.width {
		return l, errors.New("artifact data length overflow")
	}
	l.geometry = l.count * traceSize
	l.mask = l.elements
	l.data = l.elements * l.width
	if l.geometry > math.MaxInt64-l.mask || l.geometry+l.mask > math.MaxInt64-l.data-maxMetadata-headerSize {
		return l, errors.New("artifact length overflow")
	}
	return l, nil
}
func encodeTrace(t TraceMetadata) ([traceSize]byte, error) {
	var b [traceSize]byte
	if t.TraceID < 0 {
		return b, errors.New("negative physical trace identifier")
	}
	if t.CoordinateValid && (!finite(t.X) || !finite(t.Y)) {
		return b, errors.New("non-finite valid coordinate")
	}
	// Invalid coordinates are canonical zeros, never NaN/Inf metadata payloads.
	if !t.CoordinateValid {
		t.X = 0
		t.Y = 0
	}
	binary.LittleEndian.PutUint64(b[0:8], uint64(t.TraceID))
	binary.LittleEndian.PutUint32(b[8:12], uint32(t.Inline))
	binary.LittleEndian.PutUint32(b[12:16], uint32(t.Crossline))
	binary.LittleEndian.PutUint64(b[16:24], math.Float64bits(t.X))
	binary.LittleEndian.PutUint64(b[24:32], math.Float64bits(t.Y))
	binary.LittleEndian.PutUint64(b[32:40], uint64(t.TimeOriginUS))
	if t.CoordinateValid {
		b[40] = 1
	}
	return b, nil
}
func decodeTrace(b []byte) (TraceMetadata, error) {
	t := TraceMetadata{TraceID: int64(binary.LittleEndian.Uint64(b[0:8])), Inline: int32(binary.LittleEndian.Uint32(b[8:12])), Crossline: int32(binary.LittleEndian.Uint32(b[12:16])), X: math.Float64frombits(binary.LittleEndian.Uint64(b[16:24])), Y: math.Float64frombits(binary.LittleEndian.Uint64(b[24:32])), TimeOriginUS: int64(binary.LittleEndian.Uint64(b[32:40])), CoordinateValid: b[40] == 1}
	if b[40] > 1 || !bytes.Equal(b[41:48], make([]byte, 7)) {
		return t, errors.New("invalid trace flags")
	}
	want, err := encodeTrace(t)
	if err != nil {
		return t, err
	}
	if !bytes.Equal(want[:], b) {
		return t, errors.New("non-canonical trace metadata")
	}
	return t, nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Write creates a new artifact without replacing an existing path. It retains
// only one trace's samples at a time; caller owns the input callback buffers.
func Write(ctx context.Context, path string, m Metadata, traces []TraceMetadata, read ReadTraceFunc) (err error) {
	if ctx == nil {
		return errors.New("nil context")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	l, err := validate(m)
	if err != nil {
		return err
	}
	if int64(len(traces)) != l.count {
		return errors.New("trace metadata count mismatch")
	}
	if read == nil {
		return errors.New("missing sample reader")
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(mb) > maxMetadata {
		return errors.New("artifact metadata too large")
	}
	l.total = headerSize + int64(len(mb)) + l.geometry + l.mask + l.data
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sfdata-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(name) }()
	if err = tmp.Truncate(l.total); err != nil {
		return err
	}
	geomOffset := int64(headerSize + len(mb))
	maskOffset := geomOffset + l.geometry
	dataOffset := maskOffset + l.mask
	if _, err = tmp.WriteAt(mb, headerSize); err != nil {
		return err
	}
	gh, mh, dh := sha256.New(), sha256.New(), sha256.New()
	for i, t := range traces {
		if err = ctx.Err(); err != nil {
			return err
		}
		b, e := encodeTrace(t)
		if e != nil {
			return e
		}
		if _, err = tmp.WriteAt(b[:], geomOffset+int64(i)*traceSize); err != nil {
			return err
		}
		_, _ = gh.Write(b[:])
	}
	for i := int64(0); i < l.count; i++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		values, valid, e := read(i)
		if e != nil {
			return e
		}
		if int64(len(values)) != l.samples || valid != nil && len(valid) != len(values) {
			return fmt.Errorf("trace %d sample/mask count mismatch", i)
		}
		if len(values) > int(^uint(0)>>1)/int(l.width) {
			return errors.New("artifact trace buffer overflows address space")
		}
		mask := make([]byte, len(values))
		data := make([]byte, len(values)*int(l.width))
		for j, v := range values {
			ok := valid == nil || valid[j]
			if ok {
				mask[j] = 1
				if !finite(v) {
					return fmt.Errorf("trace %d sample %d is non-finite", i, j)
				}
			} else {
				v = 0
			}
			if l.width == 4 {
				f := float32(v)
				if ok && (!finite(float64(f))) {
					return errors.New("float32 sample overflow")
				}
				binary.LittleEndian.PutUint32(data[j*4:], math.Float32bits(f))
			} else {
				binary.LittleEndian.PutUint64(data[j*8:], math.Float64bits(v))
			}
		}
		if _, err = tmp.WriteAt(mask, maskOffset+i*l.samples); err != nil {
			return err
		}
		if _, err = tmp.WriteAt(data, dataOffset+i*l.samples*l.width); err != nil {
			return err
		}
		_, _ = mh.Write(mask)
		_, _ = dh.Write(data)
	}
	var h [headerSize]byte
	copy(h[:8], magic[:])
	binary.LittleEndian.PutUint32(h[8:12], Version)
	binary.LittleEndian.PutUint32(h[12:16], uint32(len(mb)))
	binary.LittleEndian.PutUint64(h[16:24], uint64(l.count))
	binary.LittleEndian.PutUint64(h[24:32], uint64(l.samples))
	binary.LittleEndian.PutUint64(h[32:40], uint64(l.geometry))
	binary.LittleEndian.PutUint64(h[40:48], uint64(l.mask))
	binary.LittleEndian.PutUint64(h[48:56], uint64(l.data))
	ms := sha256.Sum256(mb)
	copy(h[56:88], ms[:])
	copy(h[88:120], gh.Sum(nil))
	copy(h[120:152], mh.Sum(nil))
	copy(h[152:184], dh.Sum(nil))
	if _, err = tmp.WriteAt(h[:], 0); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Hard-link publication is atomic and fails if a target exists, including on
	// Windows NTFS. Source and target are intentionally on the same volume.
	return os.Link(name, path)
}

func Open(path string) (*File, error) { return OpenContext(context.Background(), path) }
func OpenContext(ctx context.Context, path string) (result *File, err error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
		}
	}()
	var h [headerSize]byte
	if _, err = io.ReadFull(f, h[:]); err != nil {
		return nil, err
	}
	if !bytes.Equal(h[:8], magic[:]) || binary.LittleEndian.Uint32(h[8:12]) != Version {
		return nil, errors.New("unsupported sfdata magic/version")
	}
	if !bytes.Equal(h[184:], make([]byte, 8)) {
		return nil, errors.New("invalid reserved header")
	}
	ml := binary.LittleEndian.Uint32(h[12:16])
	if ml == 0 || ml > maxMetadata {
		return nil, errors.New("invalid metadata length")
	}
	mb := make([]byte, ml)
	if _, err = io.ReadFull(f, mb); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(mb)
	if !bytes.Equal(sum[:], h[56:88]) {
		return nil, errors.New("metadata checksum mismatch")
	}
	var m Metadata
	d := json.NewDecoder(bytes.NewReader(mb))
	d.DisallowUnknownFields()
	if err = d.Decode(&m); err != nil {
		return nil, err
	}
	var tail any
	if d.Decode(&tail) != io.EOF {
		return nil, errors.New("metadata trailing data")
	}
	l, err := validate(m)
	if err != nil {
		return nil, err
	}
	l.total = headerSize + int64(ml) + l.geometry + l.mask + l.data
	for i, v := range []int64{l.count, l.samples, l.geometry, l.mask, l.data} {
		if binary.LittleEndian.Uint64(h[16+i*8:24+i*8]) != uint64(v) {
			return nil, errors.New("artifact block length mismatch")
		}
	}
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() != l.total {
		return nil, errors.New("artifact file length mismatch")
	}
	geomOffset := int64(headerSize) + int64(ml)
	maskOffset := geomOffset + l.geometry
	dataOffset := maskOffset + l.mask
	for i, block := range [][2]int64{{geomOffset, l.geometry}, {maskOffset, l.mask}, {dataOffset, l.data}} {
		s, e := hashSection(ctx, f, block[0], block[1])
		if e != nil {
			return nil, e
		}
		if !bytes.Equal(s, h[88+i*32:120+i*32]) {
			return nil, errors.New("artifact block checksum mismatch")
		}
	}
	traces := make([]TraceMetadata, int(l.count))
	var tb [traceSize]byte
	for i := range traces {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if _, err = f.ReadAt(tb[:], geomOffset+int64(i)*traceSize); err != nil {
			return nil, err
		}
		traces[i], err = decodeTrace(tb[:])
		if err != nil {
			return nil, err
		}
	}
	r := &File{f: f, meta: m, traces: traces, count: l.count, samples: l.samples, width: l.width, maskOffset: maskOffset, dataOffset: dataOffset}
	copy(r.maskHash[:], h[120:152])
	// Validation is windowed as well: even a single very long trace cannot force
	// a full-volume allocation during validation.
	for t := int64(0); t < l.count; t++ {
		for s := int64(0); s < l.samples; s += 8192 {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			end := s + 8192
			if end > l.samples {
				end = l.samples
			}
			if _, _, err = r.ReadTraceWindow(t, s, end); err != nil {
				return nil, err
			}
		}
	}
	whole, e := hashSection(ctx, f, 0, l.total)
	if e != nil {
		return nil, e
	}
	r.fingerprint = hex.EncodeToString(whole)
	return r, nil
}
func hashSection(ctx context.Context, f *os.File, start, length int64) ([]byte, error) {
	h := sha256.New()
	r := io.NewSectionReader(f, start, length)
	buf := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, e := r.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
	}
	return h.Sum(nil), nil
}

func (f *File) Metadata() Metadata {
	m := f.meta
	m.Shape = append([]uint64(nil), m.Shape...)
	m.Axes = append([]string(nil), m.Axes...)
	return m
}
func (f *File) TraceMetadata() []TraceMetadata { return append([]TraceMetadata(nil), f.traces...) }
func (f *File) TraceCount() int64              { return f.count }
func (f *File) SamplesPerTrace() int64         { return f.samples }
func (f *File) Fingerprint() string            { return f.fingerprint }
func (f *File) Close() error                   { return f.f.Close() }
func (f *File) ReadTraceWindow(trace, start, end int64) ([]float64, []bool, error) {
	if trace < 0 || trace >= f.count || start < 0 || end < start || end > f.samples {
		return nil, nil, errors.New("artifact sample window out of bounds")
	}
	n := end - start
	if n > int64(int(^uint(0)>>1))/f.width {
		return nil, nil, errors.New("artifact window too large")
	}
	mask := make([]byte, int(n))
	data := make([]byte, int(n*f.width))
	offset := trace*f.samples + start
	if _, e := f.f.ReadAt(mask, f.maskOffset+offset); e != nil {
		return nil, nil, e
	}
	if _, e := f.f.ReadAt(data, f.dataOffset+offset*f.width); e != nil {
		return nil, nil, e
	}
	v := make([]float64, int(n))
	ok := make([]bool, int(n))
	for i := range v {
		if mask[i] > 1 {
			return nil, nil, errors.New("invalid sample validity flag")
		}
		ok[i] = mask[i] == 1
		if f.width == 4 {
			v[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:])))
		} else {
			v[i] = math.Float64frombits(binary.LittleEndian.Uint64(data[i*8:]))
		}
		if ok[i] && !finite(v[i]) {
			return nil, nil, errors.New("non-finite valid artifact sample")
		}
		if !ok[i] && v[i] != 0 {
			return nil, nil, errors.New("invalid sample must be canonical zero")
		}
	}
	return v, ok, nil
}

// CheckCompatibility enforces the first-version host contract: no resampling,
// reordering, geometry mutation or validity-mask changes may be hidden by B.
// Precision may increase/decrease explicitly via DType; it is never converted here.
func CheckCompatibility(a, b *File) error {
	am, bm := a.Metadata(), b.Metadata()
	am.DType = ""
	bm.DType = ""
	if !reflect.DeepEqual(am, bm) {
		return errors.New("artifact shape or sample axis changed")
	}
	if !reflect.DeepEqual(a.traces, b.traces) {
		return errors.New("artifact trace order or geometry changed")
	}
	if a.maskHash != b.maskHash {
		return errors.New("artifact valid mask changed")
	}
	return nil
}
