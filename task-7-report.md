# Task 7 implementer report

## Implemented

- Added `(*Controller).RunCLI` with action-specific parsers for `start`, `status`, `stop`, `resume`, `received`/`receive`, `advance`, `yield`, and the explicit unsupported `import` boundary.
- Parent actions derive their namespace from the supplied runtime UUID; child actions authenticate the runtime UUID against the managed ticket. Explicit zero, negative, NaN, and infinite polling values are rejected while omitted values retain the 60-second/30-second defaults.
- Added ticket-scoped `advance --inspect` and `Controller.Inspect`, which reports `worker_running` or `worker_released` without polling, offering, or incrementing progress. Attention actions include the parent destination and deterministic failure-report prompt.
- Managed advance now propagates context cancellation/deadline errors so the main adapter can return exit code 130 instead of converting cancellation into a false attention success.
- Replaced the old registration/checkpoint/finish/fail adapter with the thin context-aware `RunWatcherCLI`; watcher commands now map cancellation to 130 and lock conflicts to 3 in the main CLI. Retired lifecycle names return the exact migration message.
- Replaced stale watcher tests with managed-controller and adapter assertions.
- Rewrote the parent and child PR watcher protocols around controller actions, exact App delivery, receipt validation, bounded recovery, inspect/yield behavior, and verified cleanup. Updated English and Chinese README copies with startup/active distinction, legacy import boundary, hook trust/restart requirement, and unverified runtime continuity limits.

## TDD evidence

Initial RED command, before `RunCLI` existed:

```text
rtk proxy env GOTOOLCHAIN=go1.25.6 go test ./internal/prwatch -run 'TestControllerCLI' -count=1
# c.RunCLI undefined (expected missing controller RunCLI)
```

Focused GREEN command:

```text
rtk proxy env GOTOOLCHAIN=go1.25.6 go test ./internal/prwatch -run 'TestControllerCLI' -count=1 -v
PASS (all controller CLI tests)
```

## Verification

```text
rtk proxy env GOTOOLCHAIN=go1.25.6 go test ./... -count=1
ok cmd/goldilocks
ok internal/prwatch
ok scripts

rtk proxy env GOTOOLCHAIN=go1.25.6 go test ./internal/prwatch -run 'TestControllerAdvancePropagatesCancellation|TestControllerCLI' -count=1
PASS

The focused CLI set also covers an already-cancelled context returning `context.Canceled` with no JSON output.

rtk proxy env GOTOOLCHAIN=go1.25.6 go test -race ./internal/prwatch ./cmd/goldilocks -count=1 -timeout=180s
ok internal/prwatch
ok cmd/goldilocks

rtk proxy env GOTOOLCHAIN=go1.25.6 go vet ./...
exit 0

rtk proxy env GOOS=windows GOARCH=amd64 GOTOOLCHAIN=go1.25.6 go test -c -o /tmp/goldilocks-prwatch-task7.test.exe ./internal/prwatch
rtk proxy env GOOS=windows GOARCH=amd64 GOTOOLCHAIN=go1.25.6 go test -c -o /tmp/goldilocks-cmd-task7.test.exe ./cmd/goldilocks
exit 0

git diff --check
exit 0
```

No installed Codex state, hooks, plugins, App messages, GitHub calls, or user-owned tasks were touched. Desktop trust/restart, idle wake, sleep, shutdown, hard-interruption, and real PR acceptance remain Task 9 runtime gates.
