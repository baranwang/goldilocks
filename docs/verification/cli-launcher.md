# CLI launcher verification

The plugin now calls `scripts/goldilocks.sh` on macOS/Linux and
`scripts/goldilocks.ps1` on Windows. The launchers select the plugin manifest
version and native platform, download one executable from the matching GitHub
Release, verify the source-controlled `scripts/SHA256SUMS`, and atomically place
it in a version/platform cache. All hook and PR watch business logic remains Go.

The source tree no longer tracks `bin/`. `scripts/build.go` generates release
assets and the pinned checksums using Go 1.25.6. CI rebuilds and rejects checksum
changes; pushing a matching `v*` tag triggers verified asset publication.

## Local checks — 2026-09-10, macOS arm64, Go 1.25.6

Passed:

- `go test -race ./...`: existing business regressions plus real launcher tests.
- `go run ./scripts/build.go`: six release formats/hashes and native warm-cache
  execution of all three configured hook commands, from a different working
  directory and plugin/cache paths containing spaces.
- Launcher tests replace only the external curl download with a native fixture.
  They exercise failed downloads, bad downloaded checksums, concurrent cold
  starts, warm offline reuse, corrupted-cache rejection/recovery, arguments
  containing spaces/quotes/backslashes/empty strings, raw stdin/stdout/stderr,
  and the child's nonzero exit status.
- Invalid pinned metadata fails before hook execution, even with a populated
  cache. Business tests and their fixture data remain unchanged.

## Cross-platform fixes verified during CI

The Windows native launcher test caught missing `Get-FileHash` module discovery,
PowerShell `-Command` mapping nonzero statuses to 1, and `$null` being converted
to an empty string for `File.Replace`. The launcher now uses .NET SHA-256,
explicit outer status propagation, and `[NullString]::Value`. These launcher
regressions and native hook smoke passed on Windows at `04abdc0`.

Windows then exposed different build hashes from CRLF Go source checkout.
A temporary local reproduction produced the identical failing Linux-amd64 hash
`ba4a1e1d0acd6bd09bb331b50498171767da45f9ecd105db3f8e6bd6380bef6f`;
restoring only Go sources to LF reproduced the pinned
`7dd3b26a9aeeb694505dd5ecd88a56d0069064326374c2674f9f3ead07fa9fcf`.
`.gitattributes` now pins Go source to LF; checksum comparison remains strict.

The release workflow reuses the complete three-OS CI matrix and can publish
only after all jobs pass. `actionlint` validates both workflows.

## Remaining boundaries

- Windows native launcher/stdio checks and Linux checks are included in CI;
  their remote result must be confirmed before claiming those platforms passed.
- No `v0.2.0` GitHub Release has been published by this change. Real first-use
  download cannot succeed until the matching verified assets are public. Local
  fixtures do not establish GitHub Release availability or network reachability.
- Existing live App lifecycle/long-running watcher acceptance remains pending.
  Installing the plugin does not automatically trust its changed hooks.
- POSIX uses a portable mkdir lock with normal-exit/signal cleanup. SIGKILL can
  leave a stale lock; timeout diagnoses its path. Remove it only after confirming
  no downloader is active. Windows file locks are released by the OS.
- A plain deletion does not purge binaries from earlier Git history. This PR's
  final tree contains no binaries; no repository-wide history rewrite is done.
