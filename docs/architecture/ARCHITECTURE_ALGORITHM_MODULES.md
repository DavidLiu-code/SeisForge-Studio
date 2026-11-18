# External algorithm modules and private implementation isolation

## Scope and trust boundary

SeisForge is a native Windows x64 Go/Win32 host. Public source contains protocol definitions, validation, module discovery, an external-process runner, artifact readers/writers, provenance and a manifest-driven parameter panel. It does not import private algorithm implementations or use Go `plugin.Open`.

The first UI entry processes only the selected visible poststack section and time window. The artifact protocol can represent 3-D data, but this does not enable full-volume processing in the UI. A remains read-only. The host owns all Readers, indices, geometries, palette state and A/B/difference rendering.

An external module is executable code trusted by the user. Windows Job Objects provide process-tree lifetime management, **not a sandbox** against a malicious executable. Trusted modules can access resources available to the current Windows user. Do not install untrusted modules.

## Public components

| Component | Responsibility |
| --- | --- |
| `pkg/algorithmapi` | Versioned manifest, parameter descriptors, requests, progress and metadata-only run records usable by an external Go module |
| `pkg/algorithmapi/artifact` | Portable `.sfdata` v1 streaming read/write and validation |
| `internal/algorithms/registry` | Passive manifest discovery, immutable snapshots, version/path checks and executable/manifest fingerprints |
| `internal/algorithms/runner` | Authorization, subprocess lifecycle, progress, artifact acceptance and owned temporary directories |
| `internal/algorithms/provenance` | Atomic, metadata-only run-record publication |
| `cmd/algorithm-noop` | Explicitly installed public Copy example, never an unknown-ID fallback |
| Host poststack integration | Captures A's current window, accepts B, preserves real trace/geometry mapping and rejects stale generations |

An application registry instance is the source for discovery, UI descriptors and run validation. Unknown, missing or incompatible algorithm IDs are errors; they must not run Copy by default.

## Module installation and authorization

The default discovery directory is:

```text
%LOCALAPPDATA%\Limage\algorithms\<module-id>\manifest.json
```

The user may explicitly add another module directory. The registry reads only the manifest and hashes the executable; it never executes during discovery, searches the seismic file's directory, or launches an executable from `PATH`.

The manifest defines `id`, `version`, `name`, `category`, `description`, `protocol_version`, `input_kinds`, `output_kinds`, `supports_progress`, `supports_cancel`, `parameters`, `executable` and `minimum_host_version`. The protocol is `seisforge.algorithm.v1`.

`executable` must be relative to and remain inside the module directory after symlink resolution. Duplicate IDs make conflicting modules unavailable; no map-order winner is selected. Missing module directories are a normal empty installation. Public builds must compile and start without them.

The user confirms the canonical module location, manifest SHA-256 and EXE SHA-256 before execution. Authorization is keyed by the combined fingerprint. The runner rechecks bytes and paths immediately before launching and before accepting a result. A changed manifest or executable requires rediscovery and renewed authorization. `--describe` is executable code and is subject to the same trust check and a five-second timeout.

For a public Copy example, build `./cmd/algorithm-noop` into a user-owned module directory as `algorithm-noop.exe`. Its explicit `--describe` command produces the matching descriptor. Save that descriptor as `manifest.json` only after deciding to trust this locally built example. The example is not silently installed or used as a fallback.

## Parameters and file protocol

Supported schema types are `integer`, `number`, `boolean`, `string`, `enum`, `file`, `range` and recursively nested `group`. Descriptors carry defaults, min/max/step, units and enumeration choices. Unknown parameter names, incompatible types, NaN/Inf, invalid limits and excessive nesting are rejected before process launch. The Win32 panel is generated from this schema rather than from algorithm names.

The command contract is direct argv, not a shell command:

```text
algorithm.exe --describe
algorithm.exe --run --request request.json --input input.sfdata --output output.sfdata
```

`request.json` carries run/algorithm/protocol identity, normalized parameters and the input fingerprint, not sample arrays, HWNDs, Readers or the original SEG-Y path. Explicit file parameters are copied by streaming into the task directory (including nested groups) and replaced with task-local paths in the request.

`.sfdata` v1 uses explicit binary blocks and little-endian float32 or float64. Two-dimensional order is `[trace,sample]`; three-dimensional order is `[inline,crossline,sample]`, with sample contiguous. The format includes dimensions, axis semantics, sample interval/time origin/range, physical trace identities, real coordinates, validity masks, lengths and checksums. Large sample/coordinate/mask arrays are not encoded in JSON. Open validates truncation, overflow, lengths, checksums and finite valid samples. Reads are windowed and output writes are streamed.

The first B acceptance contract requires matching shape, trace order, coordinates and sampling axes. It does not silently interpolate, reorder or truncate a module result. The host's artifact-backed B Reader preserves float64 and A's selected real geometry instead of forcing a temporary float32 SEG-Y or resetting Inline to 1.

Before creating a task or launching a process, the runner derives `section2d` or `volume3d` from the verified artifact dimensions and axis semantics. Both manifest `input_kinds` and `output_kinds` must support that kind because this host requires shape-preserving results. Actual returned artifacts are checked against `output_kinds` again before compatibility validation. Advertising another kind does not silently enable 3-D processing in the current section-only UI.

## Execution, cancellation and ownership

Each run gets its own unique temporary directory and a generated run ID. The runner copies only the captured input section, not the source seismic volume. It uses a 30-minute default timeout, overridable through task options.

On Windows, the process starts suspended, is assigned to a private kill-on-close Job Object, then resumes. Assignment failure kills and waits for the suspended process. Cancellation, timeout and owner destruction terminate the owned tree and wait for the Job's active-process count to reach zero before temporary cleanup (bounded at five seconds; timeout/cleanup failures are reported). Waiting only for the parent launcher is not sufficient when child workers still hold files. Unrelated application processes are not terminated.

Stdout uses JSON Lines events `started`, `progress`, `log` and `result`. Line length, total stdout and stderr retention are bounded. Invalid events terminate the task. A bounded progress queue decouples stdout draining from the host's message posting. The host's progress callback must return promptly.

Acceptance requires all of:

1. Successful exit and one successful result event.
2. Primary path equal to the requested `output.sfdata`; all extra output paths contained in the task directory.
3. Full artifact checksum, data and compatibility validation.
4. Unchanged input artifact and authorized module fingerprints.
5. The host's current owner/workspace/dataset/processing/result/compare/render tokens and dimensions still match.

On error, cancellation or staleness, Readers close and the owned task directory is removed. A successful `runner.Result` owns the directory until `Cleanup()` or an explicit `DetachDirectory()` transfers ownership to a B result. Do not clean a directory while B has an open Reader. Replacing or closing B releases that Reader before cleanup.

`AlgorithmRunRecord` includes algorithm ID/version, normalized parameters, module hashes, input/output fingerprints, output roles, status, timestamps, elapsed milliseconds and error. Default storage is the user cache under `Limage/algorithm_runs/v1`. Record publication uses a synced unique temporary file and atomic create-if-absent; an existing run record is not overwritten. The record never embeds amplitudes, sample arrays, Readers, credentials or UI objects.

## Public build and private module boundary

Public builds require no private package, local `replace`, submodule, build tag, EXE or model. Local/private operation uses the same host binary plus separately installed, explicitly authorized modules. Private files are never copied into a Git worktree for discovery or build.

External implementations and their inputs, dependencies, and licensing remain outside the public source tree. Each module must explicitly declare its output roles. The host validates the result and uses its primary output as B; when both traces are present, the Compare renderer computes A−B from the aligned view samples. It does not infer arbitrary external residual conventions or simulate a missing module with Copy.

## Release protection and existing history

Run:

```powershell
.\scripts\check_public_tree.ps1 -VerifyBuild
.\scripts\check_public_tree_test.ps1
```

The audit inspects tracked and staged paths, dependency replacements, private imports/tags and all objects reachable from `HEAD` and `main` by default. It blocks private directories, numerical-source names, executable/native dependencies, weights and runtime artifacts. `-HistoryRefs` must enumerate **all** refs that will be made public; do not omit a ref to conceal an alert. These rules are a defense-in-depth policy, not a substitute for code review: renamed or disguised private code may require human review.

`-VerifyBuild` exports the selected committed ref to a unique system temporary directory, builds Windows GUI without private tags, and removes only that exact generated directory. A dirty worktree is reported: this verifies the committed archive, not uncommitted changes. Existing security findings block this build gate rather than implying a release is safe.

Scan release refs together with all reachable history before a public upload. A clean working tree does not establish clean history, and `.gitignore` cannot change existing commits. Keep private refs outside the release ancestry instead of rewriting established public refs.
