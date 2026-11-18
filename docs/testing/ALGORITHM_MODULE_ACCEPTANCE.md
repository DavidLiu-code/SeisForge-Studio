# Public algorithm module verification

The public host can be built without installed modules. Its automated suite
uses synthetic artifacts and temporary examples. The test executables are
generated locally and are not release assets.

```powershell
go test ./pkg/algorithmapi/... ./internal/algorithms/... ./internal/poststack/... -count=1
go test ./cmd/limage -count=1 -run 'Test(Algorithm|PoststackPlugin|PoststackBandpass)'
.\scripts\check_public_tree_test.ps1
```

The tests cover manifest and parameter validation, explicit authorization,
input/output identity and checksums, module timeout/cancellation, owned process
cleanup, provenance publication, and the rejection of unknown algorithm IDs.
The synthetic Win32 integration exercises a public Copy module through the
existing Compare result flow. To verify an explicitly authorized external
module, use the optional `TestAuthorizedInstalledModuleIntegration` with a
user-supplied manifest and matching fingerprint; it skips when not configured.
Passing it is a protocol smoke test, not a substitute for independent
scientific validation.

The clean-archive release gate is documented in
[the architecture and release notes](../architecture/ARCHITECTURE_ALGORITHM_MODULES.md).
Native Win32 tests, actual desktop interactions and external module execution
must be reported separately.
