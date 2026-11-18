# Poststack processing acceptance checklist

Use synthetic A/B sections to verify that opening the algorithm window does not
start a job, preview produces a replaceable B but does not add a formal result,
and a validated formal run is added and displays B. Compare A/B and A−B in the
actual common geometry and absolute sample window. Verify exported B and A−B
by reopening their files, and confirm the original A remains unchanged.

Exercise B replacement, selection of a prior result, cancellation, source or
view changes during running, and owner closure. Confirm that invalid or stale
results do not replace a valid B and owned temporary artifacts are released
only after Readers close. Verify narrow-window controls and that opening or
closing auxiliary dialogs does not alter the owner window's state.

Reproducible gates:

```powershell
go test ./... -count=1
git diff --check
go test -c -o "$env:TEMP\limage.windows.test.exe" ./cmd/limage
go build -trimpath '-ldflags=-s -w -H=windowsgui' -o "$env:TEMP\SeisForgeStudio_candidate_x64.exe" ./cmd/limage
```

Record the build's SHA-256 and confirm PE Subsystem 2. Distinguish native
Win32 test coverage from completed desktop interactions, excluded cases and
failures. Never include real seismic sources, environment paths, external
workers, result caches or credentials in published evidence.
