# Prestack Mapping Profile Architecture

`internal/mappingprofile` is a Win32-free metadata layer for named A/B
`HeaderMapping` choices. It intentionally sits above `internal/prestack` and
does not own a SEG-Y Reader, `PrestackIndex`, cache entry, HWND, generation or
amplitude buffer.

The versioned file contains an explicit schema, profile name, A/B target,
mapping bytes and deterministic quality summaries. Saving uses a temporary
file followed by flush/sync, close and an atomic publication step. Loading
validates the schema, names, mappings, ranges and duplicate field summaries,
then returns defensive copies.

The UI can use a profile as a candidate mapping, but must still build an index
with the active Reader and increment the normal dataset/mapping generations.
Replacing a B profile therefore cannot mutate A state or reuse stale B
results. The format is separate from `.lidx`, `.cidx`, `.pidx`, `.ptx` and
session snapshots.
