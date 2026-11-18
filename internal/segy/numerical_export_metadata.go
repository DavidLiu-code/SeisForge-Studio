package segy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

const numericalMetadataTag = "SEISFORGE NUMERICAL EXPORT V2 "
const numericalLegacyTag = "SEISFORGE NUMERICAL EXPORT "

// NumericalExportMetadata records the header mapping actually used by the
// exporter, not a redetection of the selected subsection. TraceOrder means
// that no IL/XL coordinates were established for this section.
type NumericalExportMetadata struct {
	OriginalStem  string `json:"original_stem,omitempty"`
	InlineByte    int    `json:"inline_byte,omitempty"`
	CrosslineByte int    `json:"crossline_byte,omitempty"`
	TraceOrder    bool   `json:"trace_order,omitempty"`
}

func (m NumericalExportMetadata) HasGeometry() bool {
	a, b := m.InlineByte, m.CrosslineByte
	return !m.TraceOrder && a >= 1 && a <= 237 && b >= 1 && b <= 237 && a != b
}

func numericalSafeStem(stem string) string {
	if stem == "" || stem == "." || stem == ".." || strings.ContainsAny(stem, `/\:*?"<>|`) || strings.ContainsFunc(stem, unicode.IsControl) {
		return ""
	}
	return stem
}

func numericalStemFromFilename(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	if !NumericalSEGYSuffix(name) {
		return ""
	}
	name = filepath.Base(name)
	return numericalSafeStem(strings.TrimSuffix(name, filepath.Ext(name)))
}

func numericalMetadataBlock(m NumericalExportMetadata, provenance string) ([]byte, error) {
	payload, _ := json.Marshal(m) // fixed fields with no failing encoders
	encoded := base64.RawStdEncoding.EncodeToString(payload)
	if len(encoded) > 39*76 {
		return nil, fmt.Errorf("numerical SEG-Y provenance exceeds one textual header")
	}
	block := bytes.Repeat([]byte(" "), 3200)
	card := func(i int, s string) {
		r := block[i*80 : (i+1)*80]
		copy(r, fmt.Sprintf("C%2d ", i+1))
		copy(r[4:], s)
	}
	card(0, numericalMetadataTag+strconv.Itoa(len(encoded)))
	i := 1
	for ; len(encoded) > 0; i++ {
		n := min(76, len(encoded))
		card(i, encoded[:n])
		encoded = encoded[n:]
	}
	if provenance != "" {
		description := "PROCESSING " + strconv.QuoteToASCII(provenance)
		for ; i < 40 && len(description) > 0; i++ {
			n := min(76, len(description))
			card(i, description[:n])
			description = description[n:]
		}
	}
	return block, nil
}

func parseNumericalMetadata(block []byte) (NumericalExportMetadata, bool) {
	var m NumericalExportMetadata
	if len(block) != 3200 || !bytes.HasPrefix(block, []byte("C 1 "+numericalMetadataTag)) {
		return m, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(block[4+len(numericalMetadataTag) : 80])))
	if err != nil || n < 1 || n > 39*76 {
		return m, false
	}
	encoded := make([]byte, 0, n)
	for i := 1; n > 0; i++ {
		if !bytes.Equal(block[i*80:i*80+4], []byte(fmt.Sprintf("C%2d ", i+1))) {
			return m, false
		}
		chunk := min(76, n)
		encoded = append(encoded, block[i*80+4:i*80+4+chunk]...)
		n -= chunk
	}
	payload, err := base64.RawStdEncoding.Strict().DecodeString(string(encoded))
	if err != nil || json.Unmarshal(payload, &m) != nil || ((m.InlineByte != 0 || m.CrosslineByte != 0) && !m.HasGeometry()) {
		return NumericalExportMetadata{}, false
	}
	m.OriginalStem = numericalSafeStem(m.OriginalStem)
	return m, true
}

func parseLegacyNumericalStem(block []byte) (stem string, marked bool) {
	if !bytes.HasPrefix(block, []byte("C 1 "+numericalLegacyTag)) || bytes.HasPrefix(block, []byte("C 1 "+numericalMetadataTag)) {
		return "", false
	}
	var full strings.Builder
	for i := 0; i < 40 && bytes.Equal(block[i*80:i*80+4], []byte(fmt.Sprintf("C%2d ", i+1))); i++ {
		full.Write(block[i*80+4 : (i+1)*80])
	}
	details, err := strconv.Unquote(strings.TrimSpace(strings.TrimPrefix(full.String(), numericalLegacyTag)))
	if err != nil {
		return "", true
	}
	role, _, _ := strings.Cut(details, ";")
	_, afterA, ok := strings.Cut(details, "; A=")
	a, afterB, okB := strings.Cut(afterA, "; B=")
	if !ok || !okB {
		return "", true
	}
	if role == "B" {
		b, _, end := strings.Cut(afterB, "; file version ")
		if end {
			if original := numericalStemFromFilename(b); original != "" {
				return original, true
			}
		}
	}
	return numericalStemFromFilename(a), true
}

func (s *File) numericalMetadataScan() {
	if s == nil || s.f == nil || s.Info.ExtendedTextHeaders <= 0 {
		return
	}
	// Inspect only textual headers, never sample data. The normal path finds
	// the current record in the last block. Legacy exports copied predecessor
	// blocks, so the earliest identifiable record supplies their stable stem.
	var block [3200]byte
	for i := s.Info.ExtendedTextHeaders - 1; i >= 0; i-- {
		if _, err := s.f.ReadAt(block[:], 3600+int64(i)*3200); err != nil {
			continue
		}
		if m, ok := parseNumericalMetadata(block[:]); ok {
			if i == s.Info.ExtendedTextHeaders-1 {
				s.numericalMetadata = m
				return
			}
			if s.numericalMetadata.OriginalStem == "" {
				s.numericalMetadata.OriginalStem = m.OriginalStem
			}
			continue // inherited mapping is not necessarily the current mapping
		}
		if stem, marked := parseLegacyNumericalStem(block[:]); marked {
			if i == s.Info.ExtendedTextHeaders-1 {
				s.numericalLegacy = true
			}
			if stem != "" {
				s.numericalMetadata.OriginalStem = stem
			}
		}
	}
}

func (s *File) NumericalExportMetadata() NumericalExportMetadata {
	if s == nil {
		return NumericalExportMetadata{}
	}
	s.numericalMetadataOnce.Do(s.numericalMetadataScan)
	return s.numericalMetadata
}

// NumericalOriginalStem returns a trusted SeisForge export lineage stem, or
// the actual SEG-Y filename stem when it carries no recognizable lineage.
func (s *File) NumericalOriginalStem() string {
	if s == nil {
		return ""
	}
	if m := s.NumericalExportMetadata(); m.OriginalStem != "" {
		return m.OriginalStem
	}
	return numericalStemFromFilename(s.Info.Path)
}

// DetectPoststackSectionGeometryBytes prefers an exported explicit mapping.
// For an older SeisForge section, Byte 1 or 5 mirroring trace order cannot by
// itself identify a spatial axis. A legacy single-line 189/21 candidate with
// ordered distinct IDs is reported as heuristic, not proven geometry.
func (s *File) DetectPoststackSectionGeometryBytes(maxHeaders int) (GeometryDetectResult, error) {
	m := s.NumericalExportMetadata()
	if m.HasGeometry() {
		return GeometryDetectResult{InlineByte: m.InlineByte, CrosslineByte: m.CrosslineByte, Confidence: 1, Score: 100}, nil
	}
	if m.TraceOrder {
		return GeometryDetectResult{}, fmt.Errorf("this numerical export records trace order, not an Inline/Crossline geometry; choose manual header bytes to override")
	}
	r, err := s.DetectGeometryBytes(maxHeaders)
	if !s.numericalLegacy || (err == nil && (r.InlineByte != 189 || (r.CrosslineByte != 1 && r.CrosslineByte != 5))) {
		return r, err
	}
	samples, sampleErr := s.readGeometrySamples(maxHeaders)
	if sampleErr != nil || len(samples) < 4 {
		return r, err
	}
	line := traceI32(samples[0].hdr[188:192], s.Info.Endian)
	if line == 0 {
		return r, err
	}
	// Only repair the known legacy signature when the presumed spatial word
	// is distinct, monotonic, and not itself either physical trace counter.
	x0 := traceI32(samples[0].hdr[20:24], s.Info.Endian)
	seqA0 := traceI32(samples[0].hdr[0:4], s.Info.Endian)
	seqB0 := traceI32(samples[0].hdr[4:8], s.Info.Endian)
	xprev, xDirection := x0, int64(0)
	prevTrace := int64(-1)
	seqA, seqB := true, true
	for _, sample := range samples {
		if sample.trace <= prevTrace { // sampled windows may overlap
			continue
		}
		if traceI32(sample.hdr[188:192], s.Info.Endian) != line || traceI32(sample.hdr[192:196], s.Info.Endian) != 0 {
			return r, err
		}
		x := traceI32(sample.hdr[20:24], s.Info.Endian)
		if prevTrace >= 0 {
			d := int64(x) - int64(xprev)
			if d == 0 || (xDirection != 0 && ((xDirection < 0) != (d < 0))) {
				return r, err
			}
			xDirection = d
		}
		prevTrace = sample.trace
		xprev = x
		seqA = seqA && int64(traceI32(sample.hdr[0:4], s.Info.Endian))-int64(seqA0) == sample.trace-samples[0].trace
		seqB = seqB && int64(traceI32(sample.hdr[4:8], s.Info.Endian))-int64(seqB0) == sample.trace-samples[0].trace
	}
	if !(seqA || seqB) || x0 == seqA0 || (seqB && x0 == seqB0) {
		return r, err
	}
	return GeometryDetectResult{InlineByte: 189, CrosslineByte: 21, Score: r.Score, Confidence: min(r.Confidence, .6), Ambiguous: true, LabelsHeuristic: true, SampleHeaders: len(samples), Candidates: r.Candidates}, nil
}
