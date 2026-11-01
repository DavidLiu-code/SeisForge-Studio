# Prestack Batch Replay Architecture

`internal/prestackbatch` is a metadata-only orchestration layer above the
existing Figure Recipe, session snapshot and QC packages. It deliberately does
not own a SEG-Y reader, amplitude buffer, renderer, window handle or index.

`BuildPlan` validates recipe manifests and session snapshots, normalizes source
paths, derives output targets inside one output directory, and assigns stable
job IDs. Figures retain their recorded A/B source identities; QC jobs retain the
session source identity and requested JSON/CSV format. Jobs are sorted by input
path and output path so equivalent inputs produce the same order. QC jobs keep
the optional B source identity from a Compare snapshot as provenance, while the
metadata-only QC export currently summarizes A's selected index.

`Save` writes a versioned JSON plan through a temporary file and atomic publish.
The Windows `--batch-plan` executor rereads source files, verifies the recorded
size/mtime and optional complete SHA-256, and stops on cancellation or the first
failed job. Figure jobs use the existing recipe renderer; QC jobs open A
independently, use `BuildIndexCached`, select the snapshot gather, and export
only QC metadata. No reader, amplitude buffer, HWND, or runtime generation is
serialized into the plan.

The Gather/Compare UI currently captures the runtime-neutral session snapshot
and creates one QC job through `BatchPlanDraft`. Figure capture remains a
separate Figure Recipe operation so the existing pixel-identical renderer and
its cancellation lifecycle are not duplicated in the plan builder.

The same pages expose a background plan runner. It delegates to the existing
executor, owns a cancellable context, and publishes only a result carrying the
window owner, owner token, and batch generation. Closing the workspace or
starting another run invalidates that token and prevents stale status updates.
