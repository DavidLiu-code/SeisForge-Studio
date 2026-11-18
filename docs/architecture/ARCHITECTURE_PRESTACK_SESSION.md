# Prestack Session Snapshot Architecture

`internal/prestacksession` is a Win32-free, metadata-only schema for restoring
the Prestack workspace. The schema is `seisforge.prestack.session.v1` and is
kept separate from `volume_view.json`, `crooked_view.json`, `pseudo_view.json`
and the existing index formats.

The snapshot stores A/B source identities, HeaderMapping, GatherSelection,
display and sample ranges, Compare filters/axis status, QC summary, Geometry
layer state and Mapping target. It never stores Readers, Index pointers, HWNDs,
generations, GDI buffers, derived gather tables or samples.

Save uses a temporary file, flush/sync, close and an atomic publication path.
Restore first performs a fast size/mtime check; the reproducible figure export
performs the complete source hash check before rendering or publishing output.
Runtime Readers and indexes are always reopened/rebuilt independently, so a
failed B restore cannot change A.
