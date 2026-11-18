package prestack

// CompareRowClass is the side-independent classification used by the Compare
// viewer.  The match engine keeps side-specific slices (AmbiguousA,
// InvalidB, and so on); the viewer can present those as one filter category
// without losing the original side in HasA/HasB.
type CompareRowClass uint8

const (
	CompareRowMatched CompareRowClass = iota
	CompareRowAOnly
	CompareRowBOnly
	CompareRowAmbiguous
	CompareRowInvalid
)

func (c CompareRowClass) String() string {
	switch c {
	case CompareRowMatched:
		return "matched"
	case CompareRowAOnly:
		return "a_only"
	case CompareRowBOnly:
		return "b_only"
	case CompareRowAmbiguous:
		return "ambiguous"
	case CompareRowInvalid:
		return "invalid"
	default:
		return "unknown"
	}
}

// CompareFilterSet controls which logical rows are rendered.  It is a value
// type so UI code can atomically replace filters while a worker is running.
type CompareFilterSet struct {
	Matched   bool
	AOnly     bool
	BOnly     bool
	Ambiguous bool
	Invalid   bool
}

// DefaultCompareFilterSet keeps A complete while showing matched B rows.  It
// deliberately includes A-only rows so a failed B match never hides A data.
func DefaultCompareFilterSet() CompareFilterSet {
	return CompareFilterSet{Matched: true, AOnly: true}
}

func (f CompareFilterSet) Allows(class CompareRowClass) bool {
	switch class {
	case CompareRowMatched:
		return f.Matched
	case CompareRowAOnly:
		return f.AOnly
	case CompareRowBOnly:
		return f.BOnly
	case CompareRowAmbiguous:
		return f.Ambiguous
	case CompareRowInvalid:
		return f.Invalid
	default:
		return false
	}
}

// CompareLogicalRow is a metadata-only row in stable display order. AIndex
// and BIndex are positions in the input selections; -1 means that side is not
// present. No sample or amplitude data is retained.
type CompareLogicalRow struct {
	Classification CompareRowClass
	ATrace         int64
	BTrace         int64
	AIndex         int
	BIndex         int
	HasA           bool
	HasB           bool
	Key            TraceMatchKey
	HasKey         bool
}

// FilterCompareRows returns rows in A selection order, followed by B-only
// rows in B selection order. It uses the positions carried by matched pairs,
// and reconstructs unmatched positions from the supplied metadata records.
// Records are headers only; callers must not pass sample arrays here.
func FilterCompareRows(result CompareMatchResult, a, b []PrestackTraceRecord, filters CompareFilterSet) []CompareLogicalRow {
	if !filters.Matched && !filters.AOnly && !filters.BOnly && !filters.Ambiguous && !filters.Invalid {
		return nil
	}
	pairByA := make(map[int]TraceMatchPair, len(result.Pairs))
	for _, pair := range result.Pairs {
		pairByA[pair.AIndex] = pair
	}
	set := func(values []int64) map[int64]bool {
		out := make(map[int64]bool, len(values))
		for _, value := range values {
			out[value] = true
		}
		return out
	}
	ambA, ambB := set(result.AmbiguousA), set(result.AmbiguousB)
	invA, invB := set(result.InvalidA), set(result.InvalidB)
	onlyA, onlyB := set(result.AOnly), set(result.BOnly)
	rows := make([]CompareLogicalRow, 0, len(a)+len(b))
	for i, record := range a {
		trace := traceNumber(record, i)
		if pair, ok := pairByA[i]; ok {
			if filters.Allows(CompareRowMatched) {
				rows = append(rows, CompareLogicalRow{Classification: CompareRowMatched, ATrace: pair.ATrace, BTrace: pair.BTrace,
					AIndex: i, BIndex: pair.BIndex, HasA: true, HasB: true, Key: pair.Key, HasKey: true})
			}
			continue
		}
		class := CompareRowAOnly
		switch {
		case ambA[trace]:
			class = CompareRowAmbiguous
		case invA[trace]:
			class = CompareRowInvalid
		case !onlyA[trace]:
			// A record may have been removed from a tentative match after a
			// duplicate was discovered. Treat it as ambiguous rather than
			// silently dropping it.
			class = CompareRowAmbiguous
		}
		if filters.Allows(class) {
			rows = append(rows, CompareLogicalRow{Classification: class, ATrace: trace, AIndex: i, HasA: true})
		}
	}
	for i, record := range b {
		trace := traceNumber(record, i)
		class := CompareRowBOnly
		switch {
		case ambB[trace]:
			class = CompareRowAmbiguous
		case invB[trace]:
			class = CompareRowInvalid
		case !onlyB[trace]:
			// Matched B rows are already represented beside their A row.
			continue
		}
		if filters.Allows(class) {
			rows = append(rows, CompareLogicalRow{Classification: class, BTrace: trace, BIndex: i, HasB: true})
		}
	}
	return rows
}

// LogicalRows is a lightweight fallback for callers that only have a match
// result. Matched rows remain in A order and unmatched rows retain the result
// slice order. FilterCompareRows should be preferred when header records are
// available because it can preserve exact selection positions.
func (r CompareMatchResult) LogicalRows(filters CompareFilterSet) []CompareLogicalRow {
	rows := make([]CompareLogicalRow, 0, len(r.Pairs)+len(r.AOnly)+len(r.BOnly)+len(r.AmbiguousA)+len(r.AmbiguousB)+len(r.InvalidA)+len(r.InvalidB))
	for _, p := range r.Pairs {
		if filters.Allows(CompareRowMatched) {
			rows = append(rows, CompareLogicalRow{Classification: CompareRowMatched, ATrace: p.ATrace, BTrace: p.BTrace, AIndex: p.AIndex, BIndex: p.BIndex, HasA: true, HasB: true, Key: p.Key, HasKey: true})
		}
	}
	appendSide := func(class CompareRowClass, traces []int64, isA bool) {
		if !filters.Allows(class) {
			return
		}
		for _, trace := range traces {
			row := CompareLogicalRow{Classification: class, AIndex: -1, BIndex: -1}
			if isA {
				row.ATrace, row.HasA = trace, true
			} else {
				row.BTrace, row.HasB = trace, true
			}
			rows = append(rows, row)
		}
	}
	appendSide(CompareRowAOnly, r.AOnly, true)
	appendSide(CompareRowBOnly, r.BOnly, false)
	appendSide(CompareRowAmbiguous, r.AmbiguousA, true)
	appendSide(CompareRowAmbiguous, r.AmbiguousB, false)
	appendSide(CompareRowInvalid, r.InvalidA, true)
	appendSide(CompareRowInvalid, r.InvalidB, false)
	return rows
}
