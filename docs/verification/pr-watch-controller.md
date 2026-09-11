# PR watch controller candidate verification

Date: 2026-09-11

Candidate `0.3.0` was built from this worktree with Go 1.25.6 and GoReleaser
v2.18.1. The checks below are retained offline evidence; no tag, publication,
upload, GitHub write, installed plugin, hook trust change, or Desktop task was
created by this run. Status is limited to `passed`, `failed`, or `not_verified`.

| Acceptance ID | Status | Candidate/build/platform | Actual evidence | Limitation |
| --- | --- | --- | --- | --- |
| A1 | passed | v0.3.0 snapshot; Go 1.25.6; macOS arm64 | `internal/prwatch/controller_cli_test.go`: `TestControllerCLIStartDoesNotClaimRunning`; `internal/prwatch/lifecycle_test.go`: `TestParentSeesChildThatNeverRegistered`; `go test ./...` | Desktop no-bind startup probe remains unverified. |
| A2 | passed | v0.3.0 snapshot; macOS arm64 | `internal/prwatch/controller_test.go`: duplicate/concurrent start and ticket-binding tests; `internal/prwatch/cli_test.go`: `TestBuiltWatcherControllerConcurrentStartAndReceiptLifecycle` | No live concurrent Desktop parents or children. |
| A3 | passed | v0.3.0 snapshot; macOS arm64 | `internal/prwatch/migration_test.go`: `TestImportPreservesV3CollectingAndLargeIDs`; `internal/prwatch/state_test.go`: `TestV2PendingAndManifestSurviveUpgrade`; full test suite | Offline import/restart fixtures only. |
| A4 | passed | v0.3.0 snapshot; macOS arm64 | `internal/prwatch/controller_test.go`: transaction rollback tests; `internal/prwatch/advance_test.go`: worker exclusion, stop overlap, and cancellation tests; race suite | No installed hook write while a real poll waits. |
| A5 | passed | v0.3.0 snapshot; macOS arm64 | `internal/prwatch/cli_test.go`: `TestBuiltWatcherControllerConcurrentStartAndReceiptLifecycle` builds 16 unresolved threads, checks multipart bodies, and requires a next poll before readiness; `internal/prwatch/watch_test.go`: quiet-window tests | Fake GitHub command and local binary; no real PR. |
| A6 | passed | v0.3.0 snapshot; macOS arm64 | `internal/prwatch/delivery_test.go`: exact destination/body, failed result, duplicate call ID, and missing-receipt tests; `internal/prwatch/runtime_test.go`: exact receipt validation | App transport is simulated by unit fixtures. |
| A7 | passed | v0.3.0 snapshot; macOS arm64 | `internal/prwatch/delivery_test.go`: `TestAcceptedEventCommitsAtomicallyAfterRestart`, `TestOfferRetriesAreDelayedAndBounded`, and outer-transaction rollback coverage | No real App crash/restart sequence. |
| A8 | passed | v0.3.0 snapshot; macOS arm64 | `internal/prwatch/delivery_test.go`: `TestReceiveDeduplicatesParentPartsWithoutTransportReceipt`; multipart restart tests | Parent delivery is a local protocol fixture. |
| A9 | passed | v0.3.0 snapshot; macOS arm64 | `internal/prwatch/lifecycle_test.go`: `TestRepeatedChildStopsUseEvidenceBudget`, `TestChildBudgetResetsOnlyForAcceptedEvidence`, and parent/child budget tests; full race suite | No real successive Desktop Stop callbacks. |
| A10 | passed | v0.3.0 snapshot; macOS arm64 | `internal/prwatch/lifecycle_test.go`: pending-before-stop and terminal cleanup tests; `internal/prwatch/advance_test.go`: yield, heartbeat, lock, and cancellation tests | Lost-handle and stop-pending cases were not driven by an installed runtime. |
| A11 | passed | v0.3.0 snapshot; macOS arm64 | `cmd/goldilocks/main_test.go`: `TestUnrelatedRuntimeEventDoesNotCreateControllerState`; `scripts/verify_test.go`: `TestControllerHooksHavePortableLaunchers`; no GitHub write path in offline suite | Real hook trust scope and unrelated live agents were not exercised. |
| A12 | not_verified | v0.3.0 candidate; macOS arm64 cache prepared | Candidate cache smoke completed from an extracted tree; no installed candidate or idle-parent wakeup was available | Requires an explicitly authorized disposable PR, idle Desktop parent, and a real change after more than 20 quiet minutes. |
| A13 | not_verified | v0.3.0 snapshot; six targets cross-built on macOS arm64 | `go test ./scripts`; GoReleaser snapshot; `go run ./scripts/verify.go` reported six formats/checksums and five-event native smoke; `scripts/launcher_test.go` covers cache/checksum/stdio behavior | Candidate hooks were not trusted/restarted in Desktop; non-native targets were cross-built only. |

## Candidate artifacts

The source-controlled manifest is `scripts/SHA256SUMS`; GoReleaser produced the
same entries at `dist/SHA256SUMS`. The six candidate asset digests are:

| Asset | SHA-256 |
| --- | --- |
| `goldilocks-Darwin-arm64` | `d6284d50495f8f7615a6d94b1ba23ecc7882a25695642285b8b30b4349ebbc9b` |
| `goldilocks-Darwin-x86_64` | `70fe02ae56c8db6477bad9d91b5bdce6c53ffa1362714aa83b0cf204406ced72` |
| `goldilocks-Linux-aarch64` | `0760d393321a41000868c38070dd9f701e49140107ef18b6964e89c06000636d` |
| `goldilocks-Linux-x86_64` | `06f2d5979599a2bb5bb52995a59eae89947a774c2758944961216c3ef7b9d69b` |
| `goldilocks-Windows-AMD64.exe` | `a3c9f880517cde1bbb1594cbb80dc67b374dd3b9f815b8f05bcb2794b37129fb` |
| `goldilocks-Windows-ARM64.exe` | `6ea273343be51ed0cc0703e6508a6e9e9689bbd4e4e97134bb03c7ccb3445e8b` |

The checksum manifest SHA-256 is
`0666559abf4ac094d7fc1a1605cdaf613519d82fed353b5fbc225fb11c8a6b19`.

An extracted candidate was staged with the Darwin arm64 binary preseeded at
the launcher's supported cache layout `bin/0.3.0/Darwin-arm64/goldilocks`.
The cached file mode was `0700`, its digest matched the first entry above, and
`goldilocks.sh --version` returned `0.3.0` from another working directory with
the PATH restricted to system tools. The five hook commands were then invoked
through that launcher with an isolated `CODEX_HOME`: SessionStart and
SubagentStart injected the routing context, while PostToolUse, SubagentStop,
and Stop produced empty output and left the isolated controller root absent.

## Execution and cleanup record

Offline commands and retained results:

```text
GOTOOLCHAIN=go1.25.6 go test ./...                 PASS
GOTOOLCHAIN=go1.25.6 go test -race ./...          PASS
GOTOOLCHAIN=go1.25.6 goreleaser release --snapshot --clean  PASS
GOTOOLCHAIN=go1.25.6 go run ./scripts/verify.go PASS
git diff --check                                  PASS
```

The bundled executable used for the snapshot was
`.superpowers/sdd/2026-09-10-pr-watch-controller/tools/goreleaser` (v2.18.1).
No test agents were created. The extracted candidate and cache paths are
retained only in the local Task 9 report outside this commit; they are not
installed. No temporary test hook source, trust hash, audit history, installed
0.2.0 file, or user controller state was changed. The verifier removed its own
isolated smoke root. Because the candidate hooks were not installed or trusted,
no restart was performed; a full Desktop restart is required after the user
reviews and trusts the candidate hook definitions.

The remaining A12/A13 runtime actions are to install the preseeded candidate
without changing the existing 0.2.0 files, review and trust the five hook
definitions, restart Desktop, and use an explicitly authorized disposable PR
plus an idle local parent to run the bounded lifecycle and long-quiet delivery
matrix. Those gates are intentionally not claimed by this offline candidate.
