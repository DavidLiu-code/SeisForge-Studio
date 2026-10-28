// Package prestacksession contains the serializable, metadata-only state of
// the Prestack workspace.  It deliberately has no Win32, Reader, Index or
// rendering dependencies; callers rebuild those runtime objects after a
// snapshot has been validated.
package prestacksession

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/prestack"
)

const SchemaVersion = "seisforge.prestack.session.v1"

var (
	ErrInvalidSnapshot = errors.New("invalid prestack session snapshot")
	ErrSnapshotExists  = errors.New("prestack session snapshot already exists")
)

// SourceIdentity identifies the metadata source used by a snapshot.  SHA256
// is optional during the quick restore check and is required by callers that
// need a reproducible render/export.
type SourceIdentity struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	ModTimeNS int64  `json:"mod_time_ns"`
	SHA256    string `json:"sha256,omitempty"`
}

type DisplayState struct {
	Mode             string  `json:"mode"`
	Palette          string  `json:"palette"`
	WiggleDecimation int     `json:"wiggle_decimation"`
	GainPercent      float64 `json:"gain_percent"`
	AGC              bool    `json:"agc"`
	RangeEnabled     bool    `json:"range_enabled"`
	RangeMin         float64 `json:"range_min"`
	RangeMax         float64 `json:"range_max"`
	SampleStart      int     `json:"sample_start"`
	SampleEnd        int     `json:"sample_end"`
	ViewFirst        int64   `json:"view_first"`
	ViewLast         int64   `json:"view_last"`
}

type CompareState struct {
	Enabled        bool                      `json:"enabled"`
	BSource        *SourceIdentity           `json:"b_source,omitempty"`
	BMapping       *prestack.HeaderMapping   `json:"b_mapping,omitempty"`
	Filter         prestack.CompareFilterSet `json:"filter"`
	Mode           string                    `json:"mode"`
	SampleStart    int                       `json:"sample_start"`
	SampleEnd      int                       `json:"sample_end"`
	ViewXMin       float64                   `json:"view_x_min"`
	ViewXMax       float64                   `json:"view_x_max"`
	ViewYMin       float64                   `json:"view_y_min"`
	ViewYMax       float64                   `json:"view_y_max"`
	AxisCompatible bool                      `json:"axis_compatible"`
	AxisReason     string                    `json:"axis_reason,omitempty"`
}

type QCState struct {
	CurrentType string `json:"current_type"`
	CurrentKey  string `json:"current_key"`
}

type GeometryState struct {
	Layers             []string `json:"layers,omitempty"`
	Style              string   `json:"style"`
	Zoom               float64  `json:"zoom"`
	PanX               float64  `json:"pan_x"`
	PanY               float64  `json:"pan_y"`
	SelectedSourceID   int32    `json:"selected_source_id"`
	SelectedReceiverID int32    `json:"selected_receiver_id"`
	LinkActive         bool     `json:"link_active"`
	BoundsValid        bool     `json:"bounds_valid"`
	BoundsXMin         float64  `json:"bounds_x_min"`
	BoundsXMax         float64  `json:"bounds_x_max"`
	BoundsYMin         float64  `json:"bounds_y_min"`
	BoundsYMax         float64  `json:"bounds_y_max"`
}

type MappingState struct {
	Target string `json:"target"`
}

// SessionState is the UI-independent input to Capture.  Runtime pointers,
// generations and derived gather tables intentionally cannot be represented.
type SessionState struct {
	Page      int                      `json:"page"`
	ASource   SourceIdentity           `json:"a_source"`
	AMapping  prestack.HeaderMapping   `json:"a_mapping"`
	Selection prestack.GatherSelection `json:"selection"`
	Display   DisplayState             `json:"display"`
	Compare   CompareState             `json:"compare"`
	QC        QCState                  `json:"qc"`
	Geometry  GeometryState            `json:"geometry"`
	Mapping   MappingState             `json:"mapping"`
}

// Snapshot is the stable on-disk representation.  The field layout is
// deliberately explicit so future runtime fields cannot leak into the file.
type Snapshot struct {
	SchemaVersion string                   `json:"schema_version"`
	Page          int                      `json:"page"`
	A             SourceIdentity           `json:"a"`
	AMapping      prestack.HeaderMapping   `json:"a_mapping"`
	Selection     prestack.GatherSelection `json:"selection"`
	Display       DisplayState             `json:"display"`
	Compare       CompareState             `json:"compare"`
	QC            QCState                  `json:"qc"`
	Geometry      GeometryState            `json:"geometry"`
	Mapping       MappingState             `json:"mapping"`
}

func CapturePrestackSessionSnapshot(state SessionState) (Snapshot, error) {
	s := Snapshot{SchemaVersion: SchemaVersion, Page: state.Page, A: state.ASource, AMapping: state.AMapping,
		Selection: state.Selection, Display: state.Display, Compare: state.Compare,
		QC: state.QC, Geometry: state.Geometry, Mapping: state.Mapping}
	// Force copies of all slices and optional pointers before returning.
	s.Selection.Keys = append([]prestack.GatherKey(nil), state.Selection.Keys...)
	s.Compare = cloneCompare(state.Compare)
	s.Geometry.Layers = append([]string(nil), state.Geometry.Layers...)
	if err := ValidatePrestackSessionSnapshot(s); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

func cloneCompare(c CompareState) CompareState {
	if c.BSource != nil {
		v := *c.BSource
		c.BSource = &v
	}
	if c.BMapping != nil {
		v := *c.BMapping
		c.BMapping = &v
	}
	return c
}

// Clone returns a defensive copy suitable for an asynchronous restore task.
func (s Snapshot) Clone() Snapshot {
	s.Selection.Keys = append([]prestack.GatherKey(nil), s.Selection.Keys...)
	s.Compare = cloneCompare(s.Compare)
	s.Geometry.Layers = append([]string(nil), s.Geometry.Layers...)
	return s
}

// ApplyPrestackSessionSnapshot validates and returns a fresh runtime-neutral
// state.  UI code should open/rebuild readers and indexes after this call.
func ApplyPrestackSessionSnapshot(s Snapshot) (SessionState, error) {
	if err := ValidatePrestackSessionSnapshot(s); err != nil {
		return SessionState{}, err
	}
	return SessionState{Page: s.Page, ASource: s.A, AMapping: s.AMapping, Selection: s.Selection,
		Display: s.Display, Compare: cloneCompare(s.Compare), QC: s.QC,
		Geometry: GeometryState{Layers: append([]string(nil), s.Geometry.Layers...), Style: s.Geometry.Style,
			Zoom: s.Geometry.Zoom, PanX: s.Geometry.PanX, PanY: s.Geometry.PanY,
			SelectedSourceID: s.Geometry.SelectedSourceID, SelectedReceiverID: s.Geometry.SelectedReceiverID,
			LinkActive: s.Geometry.LinkActive, BoundsValid: s.Geometry.BoundsValid,
			BoundsXMin: s.Geometry.BoundsXMin, BoundsXMax: s.Geometry.BoundsXMax,
			BoundsYMin: s.Geometry.BoundsYMin, BoundsYMax: s.Geometry.BoundsYMax},
		Mapping: s.Mapping}, nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func validateSource(id SourceIdentity, name string, optional bool) error {
	if optional && id.Path == "" {
		return nil
	}
	if strings.TrimSpace(id.Path) == "" || id.Size < 0 || id.ModTimeNS < 0 {
		return fmt.Errorf("%w: invalid %s source identity", ErrInvalidSnapshot, name)
	}
	if id.SHA256 != "" && (len(id.SHA256) != 64 || strings.Trim(id.SHA256, "0123456789abcdefABCDEF") != "") {
		return fmt.Errorf("%w: invalid %s SHA-256", ErrInvalidSnapshot, name)
	}
	return nil
}

func validateSelection(s prestack.GatherSelection) error {
	if s.SampleStart < 0 || s.SampleEnd < 0 || (s.SampleEnd > 0 && s.SampleEnd < s.SampleStart) {
		return fmt.Errorf("%w: invalid selection sample range", ErrInvalidSnapshot)
	}
	for _, k := range s.Keys {
		for _, v := range []float64{k.X, k.Y, k.OffsetCenter, k.OffsetMin, k.OffsetMax, k.AzimuthCenter, k.AzimuthMin, k.AzimuthMax, k.CMPBinSize, k.CMPBinOriginX, k.CMPBinOriginY, k.CMPBinCenterX, k.CMPBinCenterY} {
			if !finite(v) {
				return fmt.Errorf("%w: non-finite gather key", ErrInvalidSnapshot)
			}
		}
	}
	return nil
}

func validateDisplay(d DisplayState) error {
	if d.WiggleDecimation < 0 || d.SampleStart < 0 || d.SampleEnd < 0 || (d.SampleEnd > 0 && d.SampleEnd < d.SampleStart) || d.ViewFirst < 0 || d.ViewLast < 0 || (d.ViewLast > 0 && d.ViewLast < d.ViewFirst) {
		return fmt.Errorf("%w: invalid display range", ErrInvalidSnapshot)
	}
	for _, v := range []float64{d.GainPercent, d.RangeMin, d.RangeMax} {
		if !finite(v) {
			return fmt.Errorf("%w: non-finite display value", ErrInvalidSnapshot)
		}
	}
	if d.RangeEnabled && d.RangeMax <= d.RangeMin {
		return fmt.Errorf("%w: invalid amplitude range", ErrInvalidSnapshot)
	}
	return nil
}

func validateCompare(c CompareState) error {
	if c.BSource != nil {
		if err := validateSource(*c.BSource, "B", false); err != nil {
			return err
		}
	}
	if c.SampleStart < 0 || c.SampleEnd < 0 || (c.SampleEnd > 0 && c.SampleEnd < c.SampleStart) {
		return fmt.Errorf("%w: invalid compare sample range", ErrInvalidSnapshot)
	}
	for _, v := range []float64{c.ViewXMin, c.ViewXMax, c.ViewYMin, c.ViewYMax} {
		if !finite(v) {
			return fmt.Errorf("%w: non-finite compare view", ErrInvalidSnapshot)
		}
	}
	if c.ViewXMax != 0 && c.ViewXMax < c.ViewXMin || c.ViewYMax != 0 && c.ViewYMax < c.ViewYMin {
		return fmt.Errorf("%w: invalid compare view range", ErrInvalidSnapshot)
	}
	if c.BMapping != nil {
		if err := c.BMapping.Validate(); err != nil {
			return fmt.Errorf("%w: B mapping: %v", ErrInvalidSnapshot, err)
		}
	}
	return nil
}

func ValidatePrestackSessionSnapshot(s Snapshot) error {
	if s.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema %q", ErrInvalidSnapshot, s.SchemaVersion)
	}
	if s.Page < 0 || s.Page > 4 {
		return fmt.Errorf("%w: invalid page %d", ErrInvalidSnapshot, s.Page)
	}
	if err := validateSource(s.A, "A", false); err != nil {
		return err
	}
	if err := s.AMapping.Validate(); err != nil {
		return fmt.Errorf("%w: A mapping: %v", ErrInvalidSnapshot, err)
	}
	if err := validateSelection(s.Selection); err != nil {
		return err
	}
	if err := validateDisplay(s.Display); err != nil {
		return err
	}
	if err := validateCompare(s.Compare); err != nil {
		return err
	}
	if !finite(s.Geometry.Zoom) || !finite(s.Geometry.PanX) || !finite(s.Geometry.PanY) || s.Geometry.Zoom < 0 {
		return fmt.Errorf("%w: invalid geometry view", ErrInvalidSnapshot)
	}
	for _, v := range []float64{s.Geometry.BoundsXMin, s.Geometry.BoundsXMax, s.Geometry.BoundsYMin, s.Geometry.BoundsYMax} {
		if !finite(v) {
			return fmt.Errorf("%w: non-finite geometry bounds", ErrInvalidSnapshot)
		}
	}
	if s.Geometry.BoundsValid && (s.Geometry.BoundsXMax < s.Geometry.BoundsXMin || s.Geometry.BoundsYMax < s.Geometry.BoundsYMin) {
		return fmt.Errorf("%w: invalid geometry bounds", ErrInvalidSnapshot)
	}
	return nil
}

func Save(path string, s Snapshot, overwrite bool) error {
	if err := ValidatePrestackSessionSnapshot(s); err != nil {
		return err
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: empty snapshot path", ErrInvalidSnapshot)
	}
	path = filepath.Clean(path)
	if _, err := os.Stat(path); err == nil && !overwrite {
		return ErrSnapshotExists
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".prestack-session-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return publishSnapshot(tmpName, path, overwrite)
}

func Load(path string) (Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	defer f.Close()
	var s Snapshot
	dec := json.NewDecoder(f)
	if err := dec.Decode(&s); err != nil {
		return Snapshot{}, fmt.Errorf("%w: decode: %v", ErrInvalidSnapshot, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return Snapshot{}, fmt.Errorf("%w: trailing JSON", ErrInvalidSnapshot)
		}
		return Snapshot{}, fmt.Errorf("%w: trailing data: %v", ErrInvalidSnapshot, err)
	}
	if err := ValidatePrestackSessionSnapshot(s); err != nil {
		return Snapshot{}, err
	}
	return s.Clone(), nil
}
