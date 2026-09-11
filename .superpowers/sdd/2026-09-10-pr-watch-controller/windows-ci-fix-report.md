# Windows CI fix

## Root cause

`NewController` rejected Windows before parsing controller protocol requests. That made routing and controller tests report the platform error instead of their intended input, cancellation, or retired-action errors.

## Changes

- Removed the platform guard from `NewController`, so callers can construct a controller for validation and routing.
- Added the managed-platform guard to `Controller.RunCLI` after argument, runtime identity, and context validation. Windows therefore cannot enter managed polling or state mutations.
- Added Windows-only regression tests for protocol validation and the launcher entry point.
- Marked POSIX-only stateful fixtures explicitly skipped on Windows now that construction itself is supported.

## Verification

- `GOTOOLCHAIN=auto go test -count=1 ./...`
- `GOTOOLCHAIN=auto go vet ./...`
- `GOTOOLCHAIN=auto GOOS=windows GOARCH=amd64 go test -c ./internal/prwatch`
- `GOTOOLCHAIN=auto GOOS=windows GOARCH=amd64 go test -c ./cmd/goldilocks`
- `git diff --check`

Cross-compiled test binaries were not executed on macOS; Windows CI should run the new build-tagged tests.
