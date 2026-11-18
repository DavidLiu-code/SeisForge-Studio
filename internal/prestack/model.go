// Package prestack provides metadata-only gather and acquisition geometry
// indexes. It does not reuse the post-stack one-trace-per-bin GeometryIndex,
// own a SEG-Y Reader, store amplitudes, or write a cache or source file.
package prestack

import (
	"fmt"
	"math"
)

type GatherType uint8

const (
	GatherCMP GatherType = iota
	GatherShot
	GatherReceiver
	// GatherOffset groups physical traces into configurable offset bins.  Keep
	// the values above stable: they are used by the existing UI and Recent
	// workspace routing.
	GatherOffset
	// GatherRaw is the ungrouped physical file order.  It deliberately comes
	// after the historical gather values so persisted/UI enum values remain
	// compatible with CMP, Shot, Receiver and Common Offset.
	GatherRaw
	// GatherAzimuth groups records by a stable azimuth bin.  It is appended
	// after the historical values so persisted/UI enum values remain
	// compatible with CMP, Shot, Receiver, Common Offset and Raw.
	GatherAzimuth
)

const DefaultOffsetBinSize = 20.0

// DefaultAzimuthBinSize is the angular width (degrees) used by the primary
// Azimuth gather.  A fixed one-degree bin keeps the first-level category
// useful without introducing another mandatory UI parameter; callers that
// need a different width can use AvailableGathersAzimuthConfigured.
const DefaultAzimuthBinSize = 1.0

// CMPBinConfig describes the optional midpoint XY binning used by CMP/Bin
// gathers.  Size is the edge length of the square bin in the source
// coordinate units.  When OriginX and OriginY are both zero, the index
// derives the origin from the minimum finite midpoint coordinates in the
// records.  The configuration is deliberately in-memory only; it is not part
// of any persisted index format.
// A non-positive Size disables XY binning and preserves the historical
// exact-CMP/IL/XL/CDP gather behavior.
type CMPBinConfig struct {
	Size    float64
	OriginX float64
	OriginY float64
}

// GatherCMPBin is the explicit name used by mapping pages; it aliases the
// CMP/Bin gather without introducing another index table.
const GatherCMPBin = GatherCMP

func (k GatherType) String() string {
	switch k {
	case GatherCMP:
		return "CMP"
	case GatherShot:
		return "Shot"
	case GatherReceiver:
		return "Receiver"
	case GatherOffset:
		return "Common Offset"
	case GatherRaw:
		return "Raw Trace Order"
	case GatherAzimuth:
		return "Azimuth"
	default:
		return "Unknown"
	}
}

type SortMode uint8

const (
	SortPhysical SortMode = iota
	SortOffset
	SortAbsoluteOffset
	SortAzimuth
)

const SortAbsOffset = SortAbsoluteOffset

// SecondarySortMode controls ordering inside the primary gather category.
// SecondaryUnset preserves the historical zero-value GatherSelection
// behavior for callers compiled against earlier versions.  UI callers should
// set an explicit mode (normally SecondaryPhysical) when they want the
// all-range result grouped by its GatherType.
type SecondarySortMode uint8

const (
	SecondaryUnset SecondarySortMode = iota
	SecondaryPhysical
	SecondaryOffset
	SecondaryAbsoluteOffset
	SecondaryAzimuth
	SecondaryCMP
	SecondaryShot
	SecondaryReceiver
	SecondaryCommonOffset
)

func (m SecondarySortMode) String() string {
	switch m {
	case SecondaryPhysical:
		return "原始道序"
	case SecondaryOffset:
		return "Offset"
	case SecondaryAbsoluteOffset:
		return "|Offset|"
	case SecondaryAzimuth:
		return "Azimuth"
	case SecondaryCMP:
		return "CMP / CDP"
	case SecondaryShot:
		return "Shot / FFID"
	case SecondaryReceiver:
		return "Receiver"
	case SecondaryCommonOffset:
		return "Common Offset"
	default:
		return ""
	}
}

// SecondarySortOptions returns the UI order for the explicit second-level
// sorting choices.  SecondaryUnset is intentionally omitted because it is a
// compatibility sentinel rather than a user-facing option.
func SecondarySortOptions() []SecondarySortMode {
	return []SecondarySortMode{SecondaryPhysical, SecondaryOffset, SecondaryAbsoluteOffset,
		SecondaryAzimuth, SecondaryCMP, SecondaryShot, SecondaryReceiver, SecondaryCommonOffset}
}

// SecondarySortModeFromIndex converts the zero-based UI option index to the
// corresponding explicit mode without exposing the compatibility sentinel.
func SecondarySortModeFromIndex(index int) (SecondarySortMode, bool) {
	options := SecondarySortOptions()
	if index < 0 || index >= len(options) {
		return SecondaryUnset, false
	}
	return options[index], true
}

type AxisMode uint8

const (
	AxisTrace AxisMode = iota
	AxisOffset
)

// HeaderMapping uses signed 32-bit words for identifiers/coordinates and
// signed 16-bit words for Scalar/Units. All byte positions are SEG-Y 1-based.
// A zero position explicitly disables a field; DefaultHeaderMapping provides
// the standard Rev 0/1 layout. Offset is NOT coordinate-scaled.
type HeaderMapping struct {
	SourceIDByte, ReceiverIDByte int
	CDPByte, OffsetByte          int
	SourceXByte, SourceYByte     int
	ReceiverXByte, ReceiverYByte int
	CDPXByte, CDPYByte           int
	InlineByte, CrosslineByte    int
	ScalarByte, UnitsByte        int
}

func DefaultHeaderMapping() HeaderMapping {
	return HeaderMapping{SourceIDByte: 9, ReceiverIDByte: 13, CDPByte: 21, OffsetByte: 37,
		SourceXByte: 73, SourceYByte: 77, ReceiverXByte: 81, ReceiverYByte: 85,
		CDPXByte: 181, CDPYByte: 185, InlineByte: 189, CrosslineByte: 193, ScalarByte: 71, UnitsByte: 89}
}

func (m HeaderMapping) Validate() error {
	for _, f := range []struct {
		name            string
		position, width int
	}{
		{"SourceID", m.SourceIDByte, 4}, {"ReceiverID", m.ReceiverIDByte, 4},
		{"CDP", m.CDPByte, 4}, {"Offset", m.OffsetByte, 4},
		{"SourceX", m.SourceXByte, 4}, {"SourceY", m.SourceYByte, 4},
		{"ReceiverX", m.ReceiverXByte, 4}, {"ReceiverY", m.ReceiverYByte, 4},
		{"CDPX", m.CDPXByte, 4}, {"CDPY", m.CDPYByte, 4},
		{"Inline", m.InlineByte, 4}, {"Crossline", m.CrosslineByte, 4},
		{"Scalar", m.ScalarByte, 2}, {"Units", m.UnitsByte, 2},
	} {
		if f.position < 0 || f.position+f.width-1 > 240 {
			return fmt.Errorf("%s byte must be 0 (disabled) or 1..%d", f.name, 241-f.width)
		}
	}
	for _, pair := range [][2]int{{m.SourceXByte, m.SourceYByte}, {m.ReceiverXByte, m.ReceiverYByte}, {m.CDPXByte, m.CDPYByte}, {m.InlineByte, m.CrosslineByte}} {
		if (pair[0] == 0) != (pair[1] == 0) {
			return fmt.Errorf("paired coordinate fields must both be enabled or disabled")
		}
	}
	return nil
}

// A record contains no slice or map and keeps one metadata copy per physical
// trace. TraceNumber is zero-based; display it as TraceNumber+1 in the UI.
type PrestackTraceRecord struct {
	TraceNumber                                                        int64
	SourceID, ReceiverID, CDP, TraceInField, Inline, Crossline         int32
	SourceX, SourceY, ReceiverX, ReceiverY, MidpointX, MidpointY       float64
	HeaderOffset, ComputedOffset, Offset, Azimuth                      float64
	CoordinateUnits, CoordinateScalar                                  int16
	DelayMS, SampleIntervalUS, SampleCount                             int
	HasSource, HasReceiver, HasMidpoint, HasComputedOffset, HasAzimuth bool
	HasHeaderOffset, HasCDP, HasOffset                                 bool
	// HeaderValid is false when the 240-byte header could not be read or a
	// critical structural field was invalid.  The record is retained in its
	// physical position so QC can report the problem without changing trace
	// numbering.
	HeaderValid                               bool
	HeaderError                               string
	HeaderSampleCount, HeaderSampleIntervalUS int
	CoordinateScalarValid                     bool
	CoordinateScalarError                     bool
}

// GatherKey is comparable. CMP uses either a real IL/XL pair (Grid=true), or
// the original CDP value. Shot/Receiver always use ID. Common Offset keys use
// an integer bin identity plus the configured display edges.
type GatherKey struct {
	Inline, Crossline, ID int32
	Grid                  bool
	Coordinate            bool
	// All selects the complete physical record range for the requested
	// gather type. It is a synthetic UI key and is intentionally not part of
	// the compact per-key tables.
	All bool
	// Raw identifies the single synthetic key used by the ungrouped physical
	// file-order selection. It is not persisted in any existing gather table.
	Raw  bool
	X, Y float64
	// OffsetBin identifies a key generated by the Common Offset gather.  The
	// integer bin index is the stable identity; the remaining fields describe
	// the configured bin edges for display and diagnostics.
	OffsetBin      bool
	OffsetBinIndex int64
	OffsetCenter   float64
	OffsetMin      float64
	OffsetMax      float64
	// AzimuthBin identifies a primary Azimuth gather.  The integer index is
	// the stable identity; the remaining values describe the configured
	// angular interval for display and diagnostics.
	AzimuthBin      bool
	AzimuthBinIndex int64
	AzimuthCenter   float64
	AzimuthMin      float64
	AzimuthMax      float64
	// CMPBin identifies a midpoint-XY CMP bin.  The integer indices are the
	// stable key; the size/origin/centre fields retain the configuration and
	// display metadata needed when a caller reconstructs a selection without
	// retaining the original CMPBinConfig separately.
	CMPBin        bool
	CMPBinXIndex  int64
	CMPBinYIndex  int64
	CMPBinSize    float64
	CMPBinOriginX float64
	CMPBinOriginY float64
	CMPBinCenterX float64
	CMPBinCenterY float64
}

func (k GatherKey) String() string {
	if k.All {
		return "全部范围"
	}
	if k.Raw {
		return "Raw Trace Order"
	}
	if k.OffsetBin {
		half := (k.OffsetMax - k.OffsetMin) / 2
		if !(half > 0) || math.IsInf(half, 0) || math.IsNaN(half) {
			half = DefaultOffsetBinSize / 2
		}
		return fmt.Sprintf("Offset %g ±%g", k.OffsetCenter, half)
	}
	if k.AzimuthBin {
		half := (k.AzimuthMax - k.AzimuthMin) / 2
		if !(half > 0) || math.IsInf(half, 0) || math.IsNaN(half) {
			half = DefaultAzimuthBinSize / 2
		}
		return fmt.Sprintf("Azimuth %g ±%g°", k.AzimuthCenter, half)
	}
	if k.CMPBin {
		half := k.CMPBinSize / 2
		if !(half > 0) || math.IsNaN(half) || math.IsInf(half, 0) {
			half = 0
		}
		return fmt.Sprintf("CMP XY (%d,%d) %g / %g ±%g", k.CMPBinXIndex, k.CMPBinYIndex, k.CMPBinCenterX, k.CMPBinCenterY, half)
	}
	if k.Coordinate {
		return fmt.Sprintf("XY %g / %g", k.X, k.Y)
	}
	if k.Grid {
		return fmt.Sprintf("IL %d / XL %d", k.Inline, k.Crossline)
	}
	return fmt.Sprintf("%d", k.ID)
}

type GatherSelection struct {
	Type GatherType
	Key  GatherKey
	// Keys optionally selects several concrete bins/gathers at once. When
	// non-empty, Key is treated as a synthetic all-range marker for sorting and
	// display, while the records are limited to the listed keys. The slice is
	// metadata-only and keeps the caller's requested key order before the
	// normal primary/secondary ordering is applied.
	Keys []GatherKey
	Sort SortMode
	// Secondary is the explicit second-level ordering selected by the viewer.
	// SecondaryUnset retains the historical behavior, while any explicit mode
	// also enables primary grouping for an all-range selection.
	Secondary SecondarySortMode
	Axis      AxisMode
	// OffsetBinSize is used only for GatherOffset.  Values <= 0 or non-finite
	// values use DefaultOffsetBinSize, making zero-value selections safe.
	OffsetBinSize float64
	// AzimuthBinSize is used only for GatherAzimuth. Values <= 0 or non-finite
	// values use DefaultAzimuthBinSize, making zero-value selections safe.
	AzimuthBinSize float64
	// CMPBin carries an optional midpoint-XY bin configuration for
	// GatherCMP.  Size <= 0 leaves the existing exact CMP grouping intact.
	CMPBin CMPBinConfig
	// RawTraceStart/RawTraceEnd describe a zero-based half-open physical trace
	// range when Type is GatherRaw.  A zero end means the file end, making the
	// zero-value selection naturally mean the complete file.  Negative values
	// are clamped to zero; reversed endpoints are normalized by Gather.
	RawTraceStart, RawTraceEnd int64
	// SampleStart/SampleEnd are an optional zero-based inclusive sample window
	// carried with a raw selection for callers that want one immutable
	// selection object.  SEG-Y rendering still owns sample-window validation;
	// these fields are not used to group traces.
	SampleStart, SampleEnd int
}

// RawTraceRange is a zero-based half-open physical trace interval.
type RawTraceRange struct {
	Start, End int64
}

// RawTraceSelection is the UI-independent form of an ungrouped physical
// trace selection. TraceEnd is exclusive and all trace numbers are zero
// based. SampleEnd is inclusive, matching segy.RenderOptions.
type RawTraceSelection struct {
	TraceStart, TraceEnd   int64
	SampleStart, SampleEnd int
}

// NormalizeRawTraceRange clamps and normalizes a raw range to [start,end),
// where end is exclusive. End <= 0 means the complete file. It is exported
// so UI code can validate text-box input without duplicating boundary rules.
func NormalizeRawTraceRange(total, start, end int64) (int64, int64, bool) {
	if total <= 0 {
		return 0, 0, false
	}
	if start < 0 {
		start = 0
	}
	if end <= 0 {
		end = total
	}
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	if start > end {
		start, end = end, start
	}
	return start, end, start < end
}

type ValueRange struct {
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Valid bool    `json:"valid"`
}

func (r *ValueRange) add(v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	if !r.Valid {
		r.Min, r.Max, r.Valid = v, v, true
		return
	}
	if v < r.Min {
		r.Min = v
	}
	if v > r.Max {
		r.Max = v
	}
}

type XYBounds struct {
	XMin, XMax, YMin, YMax float64
	Valid                  bool
}

func (b *XYBounds) add(x, y float64) {
	if !b.Valid {
		b.XMin, b.XMax, b.YMin, b.YMax, b.Valid = x, x, y, y, true
		return
	}
	if x < b.XMin {
		b.XMin = x
	}
	if x > b.XMax {
		b.XMax = x
	}
	if y < b.YMin {
		b.YMin = y
	}
	if y > b.YMax {
		b.YMax = y
	}
}

type GatherResult struct {
	Selection                 GatherSelection
	TraceIndices              []int64
	Positions                 []float64
	OffsetRange, AzimuthRange ValueRange
	// RawTraceStart/RawTraceEnd retain the normalized physical range for raw
	// selections, allowing a renderer/status bar to report it without
	// reverse-engineering a downsampled TraceIndices slice.
	RawTraceStart, RawTraceEnd int64
}

type Bin struct {
	Key                       GatherKey
	X, Y                      float64
	HasCoordinates            bool
	Fold                      int
	OffsetRange, AzimuthRange ValueRange
}

type FoldStatistics struct {
	Bins   int     `json:"bins"`
	Traces int     `json:"traces"`
	Min    int     `json:"min"`
	Max    int     `json:"max"`
	Mean   float64 `json:"mean"`
}

// DistributionBin is one adaptive histogram bucket.  CurrentCount is used by
// QC reports to highlight the currently selected gather without changing the
// file-wide distribution stored on PrestackIndex.
type DistributionBin struct {
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
	Count        int     `json:"count"`
	CurrentCount int     `json:"current_count,omitempty"`
}

// Distribution is a compact, metadata-only histogram.  Values are never
// copied into the index; only bucket boundaries and counts are retained.
type Distribution struct {
	Min  float64           `json:"min"`
	Max  float64           `json:"max"`
	Bins []DistributionBin `json:"bins"`
}

// QualityStats contains file and gather quality counters.  It intentionally
// contains no amplitude values or sample arrays and is safe to serialize in a
// QC report.
type QualityStats struct {
	TotalTraces                int            `json:"total_traces"`
	IndexedTraces              int            `json:"indexed_traces"`
	ValidHeaderTraces          int            `json:"valid_header_traces"`
	InvalidHeaderTraces        int            `json:"invalid_header_traces"`
	MissingCDP                 int            `json:"missing_cdp"`
	MissingFFID                int            `json:"missing_ffid"`
	MissingSource              int            `json:"missing_source"`
	MissingReceiver            int            `json:"missing_receiver"`
	MissingOffset              int            `json:"missing_offset"`
	CoordinateScalarErrors     int            `json:"coordinate_scalar_errors"`
	SampleCountInconsistent    int            `json:"sample_count_inconsistent"`
	SampleIntervalInconsistent int            `json:"sample_interval_inconsistent"`
	CMPCount                   int            `json:"cmp_count"`
	ShotCount                  int            `json:"shot_count"`
	ReceiverCount              int            `json:"receiver_count"`
	CommonOffsetBinCount       int            `json:"common_offset_bin_count"`
	Fold                       FoldStatistics `json:"fold"`
	OffsetRange                ValueRange     `json:"offset_range"`
	AzimuthRange               ValueRange     `json:"azimuth_range"`
	FoldDistribution           Distribution   `json:"fold_distribution"`
	OffsetDistribution         Distribution   `json:"offset_distribution"`
	AzimuthDistribution        Distribution   `json:"azimuth_distribution"`
}

// GatherQCSummary describes the active gather at report time.
type GatherQCSummary struct {
	Type string `json:"type"`
	Key  string `json:"key"`
	// All marks the synthetic full-file selection. Its physical trace count
	// must not be presented as a single gather Fold.
	All                bool       `json:"all"`
	PhysicalTraceCount int        `json:"physical_trace_count"`
	Fold               int        `json:"fold"`
	OffsetRange        ValueRange `json:"offset_range"`
	AzimuthRange       ValueRange `json:"azimuth_range"`
}

// QCReport is the stable, metadata-only export model used by the UI.  JSON
// tags are kept explicit so reports remain readable outside Go.
type QCReport struct {
	Version       string          `json:"version"`
	SourcePath    string          `json:"source_path"`
	Quality       QualityStats    `json:"quality"`
	CurrentGather GatherQCSummary `json:"current_gather"`
}

type IndexProgress struct {
	Done, Scanned, Total int64
	Stage                string
}

// gatherTable is a CSR-style compact gather index. Ordered record indices
// belong to one flat allocation, not a map of per-gather trace slices.
type gatherTable struct {
	keys    []GatherKey
	starts  []int
	records []int
}

type PrestackIndex struct {
	SourcePath                string
	Mapping                   HeaderMapping
	Records                   []PrestackTraceRecord
	Bins                      []Bin
	Bounds                    XYBounds
	OffsetRange, AzimuthRange ValueRange
	Warnings                  []string
	UsesGrid                  bool
	ReceiverUsesCoordinates   bool
	// SourceUsesCoordinates is true when a Source/Shot identifier is reused
	// for multiple physical locations (or when only coordinates are mapped).
	// In that case acquisition associations use the complete XY coordinate as
	// their key instead of silently merging unrelated shots.
	SourceUsesCoordinates  bool
	BinarySampleCount      int
	BinarySampleIntervalUS int
	tables                 [3]gatherTable
	fold                   FoldStatistics
	quality                QualityStats

	// Acquisition point arrays and trace lookup tables are built from the same
	// metadata records as the gather CSR tables.  They never own amplitude
	// data; slices returned by the public accessors are defensive copies.
	sourcePoints         []AcquisitionPoint
	receiverPoints       []AcquisitionPoint
	sourcePointByTrace   []int
	receiverPointByTrace []int
	sourceSpatial        acquisitionSpatialIndex
	receiverSpatial      acquisitionSpatialIndex
}

func (p *PrestackIndex) FoldStats() FoldStatistics {
	if p == nil {
		return FoldStatistics{}
	}
	return p.fold
}

// Future extension contracts, deliberately unused by the MVP viewer.
type GatherMatcher interface {
	Match(a, b GatherResult) ([]TracePair, error)
}
type TracePair struct{ A, B int64 }
type RebinConfig struct{ OriginX, OriginY, InlineSpacing, CrosslineSpacing, RotationDegrees float64 }
