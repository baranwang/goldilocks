# Task 3 report

Implemented the shared polling cycle and managed worker boundary.

- `ApplyPoll` now owns one read result; standalone `Watch` uses it without changing its public API.
- `PollManaged` owns the nonblocking worker lock, persisted execution/heartbeat state, stop/yield/pending ordering, quiet-cycle restart, progress accounting, and one-second cancellation-aware waits.
- `Yield` marks the authorized active execution ended; `WorkerStatus` reports the lock holder, execution ID, and a 90-second heartbeat freshness window.
- Added managed regressions for quiet observation, duplicate-worker exclusion, stop/read overlap, heartbeat persistence, yield preservation, cancellation cleanup, restart quiet windows, and status freshness.

Validation run with `GOTOOLCHAIN=go1.25.6`:

```text
go test ./internal/prwatch -run 'TestManagedQuiet|TestManagedWorker|TestManagedStop|TestManagedHeartbeat|TestManagedYield|TestManagedCancellation|TestManagedRestart|TestManagedNew|TestWorkerStatus' -count=1
ok github.com/baranwang/goldilocks/internal/prwatch

go test -race ./internal/prwatch -count=1
ok github.com/baranwang/goldilocks/internal/prwatch

go test ./... -count=1
ok github.com/baranwang/goldilocks/cmd/goldilocks
ok github.com/baranwang/goldilocks/internal/prwatch
ok github.com/baranwang/goldilocks/scripts
```
