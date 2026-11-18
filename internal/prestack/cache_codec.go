package prestack

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/segy"
)

const (
	cacheMagic                 = "SFPIDX\r\n"
	cacheFormatVersion  uint32 = 1
	cacheDecodeVersion  uint32 = 1 // bump on any scan/decode semantic change
	cachePrefixSize            = 36
	cacheRecordSize            = 116 // fixed fields plus a bounded UTF-8 error string
	cacheMaxHeaderError        = 4096
)

// Explicit little-endian schema, not gob/JSON or an image of a Go struct.
func encodeCacheIdentity(id segy.MetadataIdentity, m HeaderMapping) []byte {
	var b bytes.Buffer
	put32 := func(v uint32) { _ = binary.Write(&b, binary.LittleEndian, v) }
	put64 := func(v int64) { _ = binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { put32(uint32(len(s))); b.WriteString(s) }
	put32(cacheFormatVersion)
	put32(cacheDecodeVersion)
	str(id.Path)
	put64(id.FileSize)
	put64(id.ModTimeUnixNano)
	i := id.Info
	for _, v := range []int64{int64(i.Endian), int64(i.SampleIntervalUS), int64(i.SamplesPerTrace), int64(i.FormatCode), int64(i.BytesPerSample), int64(i.ExtendedTextHeaders), i.DataStart, i.TraceBytes, i.TraceCount} {
		put64(v)
	}
	str(string(id.TextEncoding))
	b.Write(id.Fingerprint[:])
	for _, v := range []int{m.SourceIDByte, m.ReceiverIDByte, m.CDPByte, m.OffsetByte, m.SourceXByte, m.SourceYByte, m.ReceiverXByte, m.ReceiverYByte, m.CDPXByte, m.CDPYByte, m.InlineByte, m.CrosslineByte, m.ScalarByte, m.UnitsByte} {
		put32(uint32(v))
	}
	return b.Bytes()
}

func encodeCacheRecord(r PrestackTraceRecord, b []byte) {
	at := 0
	u16 := func(v uint16) { binary.LittleEndian.PutUint16(b[at:], v); at += 2 }
	u32 := func(v uint32) { binary.LittleEndian.PutUint32(b[at:], v); at += 4 }
	u64 := func(v uint64) { binary.LittleEndian.PutUint64(b[at:], v); at += 8 }
	u64(uint64(r.TraceNumber))
	for _, v := range []int32{r.SourceID, r.ReceiverID, r.CDP, r.TraceInField, r.Inline, r.Crossline} {
		u32(uint32(v))
	}
	for _, v := range []float64{r.SourceX, r.SourceY, r.ReceiverX, r.ReceiverY, r.MidpointX, r.MidpointY, r.HeaderOffset} {
		u64(math.Float64bits(v))
	}
	u16(uint16(r.CoordinateUnits))
	u16(uint16(r.CoordinateScalar))
	for _, v := range []int{r.DelayMS, r.SampleIntervalUS, r.SampleCount, r.HeaderSampleCount, r.HeaderSampleIntervalUS} {
		u32(uint32(int32(v)))
	}
	var flags uint16
	for bit, v := range []bool{r.HeaderValid, r.CoordinateScalarValid, r.CoordinateScalarError, r.HasHeaderOffset, r.HasCDP, r.HasOffset} {
		if v {
			flags |= 1 << bit
		}
	}
	u16(flags)
	u16(uint16(len(r.HeaderError)))
}

func decodeCacheRecord(b []byte, r *PrestackTraceRecord) (int, error) {
	at := 0
	u16 := func() uint16 { v := binary.LittleEndian.Uint16(b[at:]); at += 2; return v }
	u32 := func() uint32 { v := binary.LittleEndian.Uint32(b[at:]); at += 4; return v }
	u64 := func() uint64 { v := binary.LittleEndian.Uint64(b[at:]); at += 8; return v }
	r.TraceNumber = int64(u64())
	for _, v := range []*int32{&r.SourceID, &r.ReceiverID, &r.CDP, &r.TraceInField, &r.Inline, &r.Crossline} {
		*v = int32(u32())
	}
	for _, v := range []*float64{&r.SourceX, &r.SourceY, &r.ReceiverX, &r.ReceiverY, &r.MidpointX, &r.MidpointY, &r.HeaderOffset} {
		*v = math.Float64frombits(u64())
		if math.IsNaN(*v) || math.IsInf(*v, 0) {
			return 0, errors.New("non-finite cached header field")
		}
	}
	r.CoordinateUnits, r.CoordinateScalar = int16(u16()), int16(u16())
	for _, v := range []*int{&r.DelayMS, &r.SampleIntervalUS, &r.SampleCount, &r.HeaderSampleCount, &r.HeaderSampleIntervalUS} {
		*v = int(int32(u32()))
	}
	flags := u16()
	if flags & ^uint16(63) != 0 {
		return 0, errors.New("unknown cached record flags")
	}
	for bit, v := range []*bool{&r.HeaderValid, &r.CoordinateScalarValid, &r.CoordinateScalarError, &r.HasHeaderOffset, &r.HasCDP, &r.HasOffset} {
		*v = flags&(1<<bit) != 0
	}
	r.Offset = r.HeaderOffset
	length := int(u16())
	if length > cacheMaxHeaderError || r.SampleCount < 0 || r.SampleIntervalUS < 0 || r.HeaderSampleCount < 0 || r.HeaderSampleIntervalUS < 0 {
		return 0, errors.New("invalid cached header lengths/axis")
	}
	return length, nil
}

func readMetadataCache(ctx context.Context, path string, identity []byte, info segy.Info, mapping HeaderMapping, limit int64) (*PrestackIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cache unavailable: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > limit || st.Size() < cachePrefixSize+sha256.Size {
		return nil, errors.New("cache size outside limit")
	}
	// The entire claimed payload must fit the actual file before any record
	// allocation. Header count also has to equal the open Reader's trace count.
	hash := sha256.New()
	br := bufio.NewReaderSize(f, 256<<10)
	checked := io.TeeReader(br, hash)
	var prefix [cachePrefixSize]byte
	if _, err = io.ReadFull(checked, prefix[:]); err != nil {
		return nil, err
	}
	if string(prefix[:8]) != cacheMagic || binary.LittleEndian.Uint32(prefix[8:]) != cacheFormatVersion || binary.LittleEndian.Uint32(prefix[12:]) != cacheDecodeVersion {
		return nil, errors.New("unknown cache magic/format/decoder version")
	}
	idLen := binary.LittleEndian.Uint32(prefix[16:])
	count := binary.LittleEndian.Uint64(prefix[20:])
	bodyLen := binary.LittleEndian.Uint64(prefix[28:])
	if idLen != uint32(len(identity)) || idLen > 1<<20 || count != uint64(info.TraceCount) || count > uint64(limit/cacheRecordSize) || bodyLen > uint64(limit) || bodyLen < count*cacheRecordSize || uint64(st.Size()) != cachePrefixSize+uint64(idLen)+bodyLen+sha256.Size {
		return nil, errors.New("cache identity/count/payload length mismatch")
	}
	storedIdentity := make([]byte, int(idLen))
	if _, err = io.ReadFull(checked, storedIdentity); err != nil {
		return nil, err
	}
	if !bytes.Equal(identity, storedIdentity) {
		return nil, errors.New("cache source/mapping identity mismatch")
	}
	p := &PrestackIndex{SourcePath: info.Path, Mapping: mapping, BinarySampleCount: info.SamplesPerTrace, BinarySampleIntervalUS: info.SampleIntervalUS, Records: make([]PrestackTraceRecord, int(count))}
	body := &io.LimitedReader{R: checked, N: int64(bodyLen)}
	var fixed [cacheRecordSize]byte
	for i := range p.Records {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if _, err = io.ReadFull(body, fixed[:]); err != nil {
			return nil, err
		}
		r := &p.Records[i]
		length, e := decodeCacheRecord(fixed[:], r)
		if e != nil {
			return nil, e
		}
		if r.TraceNumber != int64(i) {
			return nil, errors.New("cache physical order mismatch")
		}
		if length > 0 {
			text := make([]byte, length)
			if _, err = io.ReadFull(body, text); err != nil {
				return nil, err
			}
			r.HeaderError = string(text)
		}
	}
	if body.N != 0 {
		return nil, errors.New("cache payload has trailing records")
	}
	var digest [sha256.Size]byte
	if _, err = io.ReadFull(br, digest[:]); err != nil {
		return nil, err
	}
	if !bytes.Equal(hash.Sum(nil), digest[:]) {
		return nil, errors.New("cache SHA-256 mismatch")
	}
	if _, err = br.ReadByte(); err != io.EOF {
		return nil, errors.New("cache trailing bytes")
	}
	return p, nil
}

type pendingMetadataCache struct{ temporary, target string }

func (p *pendingMetadataCache) discard() {
	if p.temporary != "" {
		_ = os.Remove(p.temporary)
	}
}

func (p *pendingMetadataCache) publish(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := publishMetadataCache(ctx, p.temporary, p.target); err != nil {
		return err
	}
	p.temporary = ""
	return nil
}

func prepareMetadataCache(ctx context.Context, path string, identity []byte, records []PrestackTraceRecord, limit int64) (pending *pendingMetadataCache, err error) {
	if len(identity) > 1<<20 {
		return nil, errors.New("cache identity too long")
	}
	bodyLen := int64(len(records)) * cacheRecordSize
	for i := range records {
		if i%4096 == 0 {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
		}
		if len(records[i].HeaderError) > cacheMaxHeaderError {
			return nil, errors.New("cache header error text too long")
		}
		bodyLen += int64(len(records[i].HeaderError))
		if bodyLen > limit {
			return nil, errors.New("cache exceeds file size limit; using memory index")
		}
	}
	total := int64(cachePrefixSize+len(identity)+sha256.Size) + bodyLen
	if total > limit {
		return nil, errors.New("cache exceeds file size limit; using memory index")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".sfmeta-v1-*.tmp")
	if err != nil {
		return nil, err
	}
	p := &pendingMetadataCache{temporary: f.Name(), target: path}
	defer func() {
		_ = f.Close()
		if err != nil {
			p.discard()
		}
	}()
	w := bufio.NewWriterSize(f, 256<<10)
	hash := sha256.New()
	checked := io.MultiWriter(w, hash)
	var prefix [cachePrefixSize]byte
	copy(prefix[:8], cacheMagic)
	binary.LittleEndian.PutUint32(prefix[8:], cacheFormatVersion)
	binary.LittleEndian.PutUint32(prefix[12:], cacheDecodeVersion)
	binary.LittleEndian.PutUint32(prefix[16:], uint32(len(identity)))
	binary.LittleEndian.PutUint64(prefix[20:], uint64(len(records)))
	binary.LittleEndian.PutUint64(prefix[28:], uint64(bodyLen))
	if _, err = checked.Write(prefix[:]); err != nil {
		return nil, err
	}
	if _, err = checked.Write(identity); err != nil {
		return nil, err
	}
	var fixed [cacheRecordSize]byte
	for i := range records {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		encodeCacheRecord(records[i], fixed[:])
		if _, err = checked.Write(fixed[:]); err != nil {
			return nil, err
		}
		if _, err = io.WriteString(checked, records[i].HeaderError); err != nil {
			return nil, err
		}
	}
	if _, err = w.Write(hash.Sum(nil)); err != nil {
		return nil, err
	}
	if err = w.Flush(); err != nil {
		return nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	return p, nil
}
