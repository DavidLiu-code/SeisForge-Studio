package prestack

import (
	"math"
	"sort"
)

// GatherGroupKey is the comparable, metadata-only key used by the two-level
// gather ordering.  Invalid records sort after valid groups and retain their
// physical trace order.
type GatherGroupKey struct {
	Key   GatherKey
	Valid bool
}

// primaryGatherKey derives the natural primary group from one trace without
// requiring an index.  PrestackIndex.primaryGatherKeyForSelection below adds
// the index's coordinate/ID stability decisions and configured bin widths.
// This small pure helper is useful to callers/tests that only have records.
func primaryGatherKey(record PrestackTraceRecord, kind GatherType) GatherGroupKey {
	switch kind {
	case GatherCMP:
		if record.Inline != 0 || record.Crossline != 0 {
			return GatherGroupKey{Key: GatherKey{Inline: record.Inline, Crossline: record.Crossline, Grid: true}, Valid: true}
		}
		if record.HasCDP {
			return GatherGroupKey{Key: GatherKey{ID: record.CDP}, Valid: true}
		}
		if record.HasMidpoint && finiteXY(record.MidpointX, record.MidpointY) {
			return GatherGroupKey{Key: GatherKey{Coordinate: true, X: record.MidpointX, Y: record.MidpointY}, Valid: true}
		}
	case GatherShot:
		if record.SourceID != 0 {
			return GatherGroupKey{Key: GatherKey{ID: record.SourceID}, Valid: true}
		}
		if record.HasSource && finiteXY(record.SourceX, record.SourceY) {
			return GatherGroupKey{Key: GatherKey{Coordinate: true, X: record.SourceX, Y: record.SourceY}, Valid: true}
		}
	case GatherReceiver:
		if record.ReceiverID != 0 {
			return GatherGroupKey{Key: GatherKey{ID: record.ReceiverID}, Valid: true}
		}
		if record.HasReceiver && finiteXY(record.ReceiverX, record.ReceiverY) {
			return GatherGroupKey{Key: GatherKey{Coordinate: true, X: record.ReceiverX, Y: record.ReceiverY}, Valid: true}
		}
	case GatherOffset:
		if validOffsetRecord(record) {
			idx := offsetBinIndex(record.Offset, DefaultOffsetBinSize)
			return GatherGroupKey{Key: offsetBinKey(idx, DefaultOffsetBinSize), Valid: true}
		}
	case GatherAzimuth:
		if validAzimuthRecord(record) {
			idx := azimuthBinIndex(record.Azimuth, DefaultAzimuthBinSize)
			return GatherGroupKey{Key: azimuthBinKey(idx, DefaultAzimuthBinSize), Valid: true}
		}
	}
	return GatherGroupKey{}
}

// compareSecondary compares two records using the metadata available in the
// records themselves.  It intentionally uses the default Common Offset bin
// width; configured-width callers use the PrestackIndex method below.
func compareSecondary(a, b PrestackTraceRecord, mode SecondarySortMode) int {
	return compareSecondaryForSelection(nil, a, b, mode, GatherSelection{})
}

func compareOptionalFloat(av, bv float64, ah, bh bool, absolute bool) int {
	if ah != bh {
		if ah {
			return -1
		}
		return 1
	}
	if !ah {
		return 0
	}
	if absolute {
		av, bv = math.Abs(av), math.Abs(bv)
	}
	if av < bv {
		return -1
	}
	if av > bv {
		return 1
	}
	return 0
}

func compareGroupKey(a, b GatherGroupKey) int {
	if a.Valid != b.Valid {
		if a.Valid {
			return -1
		}
		return 1
	}
	if !a.Valid {
		return 0
	}
	if keyLess(a.Key, b.Key) {
		return -1
	}
	if keyLess(b.Key, a.Key) {
		return 1
	}
	return 0
}

func (p *PrestackIndex) primaryGatherKeyForSelection(r PrestackTraceRecord, kind GatherType, selection GatherSelection) GatherGroupKey {
	switch kind {
	case GatherCMP:
		if selection.CMPBin.Size > 0 && !math.IsNaN(selection.CMPBin.Size) && !math.IsInf(selection.CMPBin.Size, 0) {
			cfg, ok := normalizeCMPSelectionConfig(p, selection)
			if ok {
				x, y, valid := cmpBinIndices(r, cfg)
				if valid {
					return GatherGroupKey{Key: makeCMPBinKey(x, y, cfg), Valid: true}
				}
				return GatherGroupKey{}
			}
		}
		if p != nil && p.UsesGrid {
			return GatherGroupKey{Key: GatherKey{Inline: r.Inline, Crossline: r.Crossline, Grid: true}, Valid: r.Inline != 0 || r.Crossline != 0}
		}
		if r.HasCDP {
			return GatherGroupKey{Key: GatherKey{ID: r.CDP}, Valid: true}
		}
		if r.HasMidpoint && finiteXY(r.MidpointX, r.MidpointY) {
			return GatherGroupKey{Key: GatherKey{Coordinate: true, X: r.MidpointX, Y: r.MidpointY}, Valid: true}
		}
	case GatherShot:
		if p != nil && p.SourceUsesCoordinates {
			return GatherGroupKey{Key: GatherKey{Coordinate: true, X: r.SourceX, Y: r.SourceY}, Valid: r.HasSource && finiteXY(r.SourceX, r.SourceY)}
		}
		return GatherGroupKey{Key: GatherKey{ID: r.SourceID}, Valid: r.SourceID != 0}
	case GatherReceiver:
		if p != nil && p.ReceiverUsesCoordinates {
			return GatherGroupKey{Key: GatherKey{Coordinate: true, X: r.ReceiverX, Y: r.ReceiverY}, Valid: r.HasReceiver && finiteXY(r.ReceiverX, r.ReceiverY)}
		}
		return GatherGroupKey{Key: GatherKey{ID: r.ReceiverID}, Valid: r.ReceiverID != 0}
	case GatherOffset:
		width := selection.OffsetBinSize
		if width <= 0 || math.IsNaN(width) || math.IsInf(width, 0) {
			width = DefaultOffsetBinSize
		}
		if validOffsetRecord(r) {
			idx := offsetBinIndex(r.Offset, width)
			return GatherGroupKey{Key: offsetBinKey(idx, width), Valid: true}
		}
	case GatherAzimuth:
		width := normalizeAzimuthBinSize(selection.AzimuthBinSize)
		if validAzimuthRecord(r) {
			idx := azimuthBinIndex(r.Azimuth, width)
			return GatherGroupKey{Key: azimuthBinKey(idx, width), Valid: true}
		}
	}
	return GatherGroupKey{}
}

func (p *PrestackIndex) secondaryGroupKey(r PrestackTraceRecord, mode SecondarySortMode, selection GatherSelection) GatherGroupKey {
	switch mode {
	case SecondaryCMP:
		return p.primaryGatherKeyForSelection(r, GatherCMP, selection)
	case SecondaryShot:
		return p.primaryGatherKeyForSelection(r, GatherShot, selection)
	case SecondaryReceiver:
		return p.primaryGatherKeyForSelection(r, GatherReceiver, selection)
	case SecondaryCommonOffset:
		return p.primaryGatherKeyForSelection(r, GatherOffset, selection)
	default:
		return GatherGroupKey{}
	}
}

func compareSecondaryForSelection(p *PrestackIndex, a, b PrestackTraceRecord, mode SecondarySortMode, selection GatherSelection) int {
	switch mode {
	case SecondaryPhysical, SecondaryUnset:
		return 0
	case SecondaryOffset:
		return compareOptionalFloat(a.Offset, b.Offset, validOffsetRecord(a), validOffsetRecord(b), false)
	case SecondaryAbsoluteOffset:
		return compareOptionalFloat(a.Offset, b.Offset, validOffsetRecord(a), validOffsetRecord(b), true)
	case SecondaryAzimuth:
		return compareOptionalFloat(a.Azimuth, b.Azimuth, a.HasAzimuth && finite(a.Azimuth), b.HasAzimuth && finite(b.Azimuth), false)
	case SecondaryCMP, SecondaryShot, SecondaryReceiver, SecondaryCommonOffset:
		return compareGroupKey(p.secondaryGroupKey(a, mode, selection), p.secondaryGroupKey(b, mode, selection))
	default:
		return 0
	}
}

func secondaryFromSort(sortMode SortMode) SecondarySortMode {
	switch sortMode {
	case SortOffset:
		return SecondaryOffset
	case SortAbsoluteOffset:
		return SecondaryAbsoluteOffset
	case SortAzimuth:
		return SecondaryAzimuth
	default:
		return SecondaryUnset
	}
}

// sortGatherRecords applies the requested second-level order.  An explicit
// Secondary mode also enables natural primary grouping when the synthetic
// all-range key is selected.  The legacy Sort field remains supported for
// existing callers and does not implicitly change their all-range order.
func (p *PrestackIndex) sortGatherRecords(records []int, selection GatherSelection) {
	// Raw mode is explicitly physical file order; no derived secondary mode
	// may reorder it.
	if selection.Type == GatherRaw {
		return
	}
	secondary := selection.Secondary
	legacy := false
	if secondary == SecondaryUnset && selection.Sort != SortPhysical {
		secondary = secondaryFromSort(selection.Sort)
		legacy = true
	}
	if secondary == SecondaryUnset && !selection.Key.All {
		return
	}
	groupPrimary := selection.Key.All && !legacy && secondary != SecondaryUnset
	if !groupPrimary && secondary == SecondaryUnset {
		return
	}
	sort.SliceStable(records, func(i, j int) bool {
		a, b := p.Records[records[i]], p.Records[records[j]]
		if groupPrimary {
			if c := compareGroupKey(p.primaryGatherKeyForSelection(a, selection.Type, selection), p.primaryGatherKeyForSelection(b, selection.Type, selection)); c != 0 {
				return c < 0
			}
		}
		if secondary != SecondaryUnset {
			if c := compareSecondaryForSelection(p, a, b, secondary, selection); c != 0 {
				return c < 0
			}
		}
		return a.TraceNumber < b.TraceNumber
	})
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
