# Task 8 report

Implemented legacy migration and explicit lifecycle boundaries in the assigned worktree.

## TDD evidence

The first migration test was run before adding `Controller.Import`:

```text
$ rtk proxy env GOTOOLCHAIN=go1.25.6 go test ./internal/prwatch -run 'TestImport' -count=1
# github.com/baranwang/goldilocks/internal/prwatch [github.com/baranwang/goldilocks/internal/prwatch.test]
internal/prwatch/migration_test.go:33:14: c.Import undefined (type *Controller has no field or method Import)
FAIL	github.com/baranwang/goldilocks/internal/prwatch [build failed]
FAIL
```

After implementing the minimum migration path and refresh branch, the focused tests passed:

```text
$ rtk proxy env GOTOOLCHAIN=go1.25.6 go test ./internal/prwatch -run 'TestImport|TestApplyPollRefresh|TestStartReopened' -count=1
ok  	github.com/baranwang/goldilocks/internal/prwatch	1.045s
```

## Verification

```text
$ rtk proxy env GOTOOLCHAIN=go1.25.6 go test ./internal/prwatch -count=1
ok  	github.com/baranwang/goldilocks/internal/prwatch	21.384s

$ rtk proxy env GOTOOLCHAIN=go1.25.6 go test -race ./internal/prwatch -run 'TestImport|TestManaged|TestTicket' -count=1
ok  	github.com/baranwang/goldilocks/internal/prwatch	3.828s

$ rtk proxy env GOTOOLCHAIN=go1.25.6 go test ./... -count=1
ok  	github.com/baranwang/goldilocks/cmd/goldilocks	0.691s
ok  	github.com/baranwang/goldilocks/internal/prwatch	21.384s
ok  	github.com/baranwang/goldilocks/scripts	5.873s

$ rtk proxy env GOTOOLCHAIN=go1.25.6 go vet ./...
# no output; exit 0

$ rtk proxy env GOTOOLCHAIN=go1.25.6 GOOS=windows GOARCH=amd64 go build ./...
# no output; exit 0

$ git diff --check
# no output; exit 0
```

The migration tests cover pending prompt preservation, v3 collecting state, large numeric evidence IDs, source locking and PR matching, acknowledged baseline refresh, terminal source rejection, managed mutation guards, explicit reopen cleanup, conflicting archives, and late receipt lookup in archived generations.

Remaining behavior relies on the existing controller lifecycle for crash recovery and worker lock ownership; no external user state, hooks, plugins, or recurring tasks were changed.

## Fix round 1

The two P1 regressions were first reproduced with focused tests:

```text
$ rtk proxy env GOTOOLCHAIN=go1.25.6 go test ./internal/prwatch -run 'TestReceiveRejectsActive|TestReopenRecoversCandidate' -count=1
--- FAIL: TestReceiveRejectsActiveAndArchivedDuplicateEvent
    duplicate active/archive event was attributed: <nil>
--- FAIL: TestReopenRecoversCandidateTicketAfterArchive
    archive already contains conflicting bytes
FAIL
```

The fixes scan retained history while holding the parent lock before mutating the active generation, reject active/archive duplicate event IDs as ambiguous, and recover a valid replacement ticket left after an interrupted reopen once the old archive is durable. The focused regressions and full verification then passed:

```text
$ rtk proxy env GOTOOLCHAIN=go1.25.6 go test ./internal/prwatch -run 'TestReceiveRejectsActive|TestReopenRecoversCandidate' -count=1
ok   github.com/baranwang/goldilocks/internal/prwatch

$ rtk proxy env GOTOOLCHAIN=go1.25.6 go test ./internal/prwatch -count=1
ok   github.com/baranwang/goldilocks/internal/prwatch

$ rtk proxy env GOTOOLCHAIN=go1.25.6 go test -race ./internal/prwatch -run 'TestImport|TestManaged|TestTicket' -count=1
ok   github.com/baranwang/goldilocks/internal/prwatch

$ rtk proxy env GOTOOLCHAIN=go1.25.6 go test ./... -count=1
ok   github.com/baranwang/goldilocks/cmd/goldilocks
ok   github.com/baranwang/goldilocks/internal/prwatch
ok   github.com/baranwang/goldilocks/scripts

$ rtk proxy env GOTOOLCHAIN=go1.25.6 go vet ./...
# no output; exit 0

$ rtk proxy env GOTOOLCHAIN=go1.25.6 GOOS=windows GOARCH=amd64 go build ./...
# no output; exit 0

$ git diff --check
# no output; exit 0
```
