<p align="center">
  <img src="assets/logo.webp" alt="Goldilocks logo" width="192">
</p>

<p align="center">
  English · <a href="README.zh-hans.md">简体中文</a> · <a href="README.zh-hant.md">正體中文</a>
</p>

# Goldilocks

**No workflow changes. Just the right model.**

Goldilocks is a lightweight Codex plugin for model routing and PR monitoring.
Its model-routing skill steps in only after the existing workflow has decided
to create a subagent, helping select a suitable model and reasoning effort.

The `model-routing` skill does not decide whether to create subagents or change
tasks and workflows. Its only responsibility is choosing model and reasoning
effort. The `pr-watch` skill delegates continuous PR monitoring and evidence
delivery to a child.

## Why Goldilocks

Goldilocks was inspired by
[oh-my-openagent](https://github.com/code-yeongyu/oh-my-openagent). I liked its
approach to assigning models and compute on demand, but
[LazyCodex](https://github.com/code-yeongyu/lazycodex)'s complete solution felt
heavier than my use case needed.

I currently use [Superpowers](https://github.com/obra/superpowers) as my primary
workflow and want to preserve its existing rhythm. Goldilocks extracts just one
core capability: once the workflow decides to create a subagent, choose the
compute configuration that best matches the task. This keeps the existing
process intact, avoids using expensive large models for simple tasks, reduces
unnecessary high-cost subagent calls, and helps conserve Codex subscription
quota.

## Installation

Run in a terminal:

```bash
codex plugin marketplace add baranwang/goldilocks
codex plugin add goldilocks@goldilocks
```

Review and trust the installed hooks, then use a task that loaded that version.
Use `/hooks` to inspect the hook definitions.

## How it works

`SessionStart` and `SubagentStart` hooks inject a compact policy from
`skills/model-routing/SKILL.md`. Before an already-planned `spawn_agent` call, the
current agent preserves explicit user choices, checks the tool schema,
classifies the child task, and changes only supported `model` and
`reasoning_effort` fields.

One Go CLI provides hooks, PR monitoring, and the managed watcher controller. A thin
shell/PowerShell launcher downloads the matching `v<plugin-version>` GitHub
Release binary on first use, checks its SHA-256 against `scripts/SHA256SUMS`,
and caches it under `PLUGIN_DATA`. Subsequent calls reuse the verified cache.
Outside plugin hooks, the fallback is `${XDG_CACHE_HOME:-$HOME/.cache}/goldilocks`
on macOS/Linux and `%LOCALAPPDATA%/goldilocks` on Windows.

No Node, Python, or Go installation is required. First use needs HTTPS access
to GitHub Releases and curl (curl.exe on modern Windows); macOS/Linux also use
sha256sum or shasum. Online PR reads use authenticated gh. Failed downloads or
checksum mismatches produce an error; retry once the release/network is available.
Hook timeout is 150 seconds to allow a cold download; cache hits execute directly.
Review and trust changed hooks before using them.
Changes are delivered after 30 seconds of observed quiet by default.
PR monitoring supports macOS/Linux in this release; Windows covers hooks.

| Route | Intended work | Default behavior |
|---|---|---|
| `quick` | Mechanical, local, low-risk tasks | Prefer Luna/low; if unavailable, use Terra/low; otherwise inherit the parent model |
| `explore` | Read-only code search and synthesis | Prefer Luna/low; if unavailable, use Terra/low; otherwise inherit the parent model. With Terra, use medium only for broader synthesis |
| `build` | Routine implementation following existing patterns | Inherit the parent model with medium effort |
| `reason` | Debugging, code review, security checks, and difficult edge cases | Prefer Sol/high; otherwise inherit the parent model with high effort |
| `deep` | Architecture, refactoring and migration, concurrency, and cross-module complexity | Prefer Sol/xhigh; otherwise inherit the parent model with xhigh effort |

Goldilocks uses only values exposed by the current `spawn_agent` schema and
does not assume a fixed model catalog. Explicit user configuration and existing
workflow settings always take precedence. If `fork_turns` is omitted or set to
`"all"`, bringing in the full conversation history, and the current interface
does not allow compute overrides, Goldilocks keeps `fork_turns` unchanged and
inherits the existing configuration. It never changes context forking to force
model routing.

## PR monitoring

Ask to monitor a PR, or use `$pr-watch`. The parent first runs `watcher start`,
which records a starting intent and returns a ticket; it does not claim a
running poller. The child executes `watcher advance --ticket-file ...`, sends
each exact action prompt through the App message tool, and repeats until the
controller returns `finished` or `attention`. The parent reports active only
after status shows `ready=true` and a live poll or confirmed delivery window.
The default poll interval is 60 seconds and observed quiet window is 30 seconds.
Monitoring does not authorize posting, pushing, or merging.

The parent validates each received event's runtime UUID, PR URL, watch/event ID,
and part number before acting. It waits for every part, skips duplicates, and
rereads the current PR/head before code changes. `watcher stop` is a request;
stopped is reported only after the child has delivered pending evidence and
status confirms `finished` plus `cleanup_confirmed=true`.

Existing v2/v3 standalone state is not silently reused by the managed
controller. Stop the old execution, then use the later
`watcher import --pr URL --state-file PATH` migration command after verifying that the PR has
reopened. A managed state must use `watcher start/status/stop/resume/received`
and the child `advance/yield` actions; the old registration, checkpoint,
finish, and fail commands are retired with an explicit migration error.

Standalone skills contain instructions only and require a compatible CLI.
Without loaded and trusted hooks, report missing lifecycle protection; hook
files and trust changes require a full Desktop restart before their execution
can be relied on. No compatible CLI means monitoring cannot start. Local
monitoring has no tested continuity guarantee during machine sleep, app exit,
hard interruption, or quota exhaustion. Tool waits can consume tokens; this is
not a zero-cost daemon.

## Binary releases

[GoReleaser](https://goreleaser.com/) v2.18.1 builds and publishes the six
executables using `.goreleaser.yaml` and Go 1.25.6. Build outputs live in ignored
`dist/`; executables are not tracked in Git. To build locally and refresh the
pinned checksums after a Go source or plugin-version change:

```bash
GOLDILOCKS_VERSION=$(jq -r .version .codex-plugin/plugin.json) goreleaser release --snapshot --clean
cp dist/SHA256SUMS scripts/SHA256SUMS
GOTOOLCHAIN=go1.25.6 go run ./scripts/verify.go
```

Commit the checksum update with the source/version change. CI builds snapshots
on Linux, macOS, and Windows, verifies all six assets against the committed
checksums, and exercises the native launcher and all three hook commands.

After validation, push a tag matching the plugin version (for example,
`v0.2.0`). The Release workflow waits for all three systems, then GoReleaser
builds and publishes the executables and `SHA256SUMS`. A post-build hook checks
each executable against the pinned manifest before publication; a mismatch
aborts the release. Snapshots never publish and allow checksum regeneration.
Publish the assets before distributing the matching plugin version. Releases
must remain available and immutable for that version; there is no `latest`
fallback.

POSIX downloads use a directory lock and clean it up on normal failure or
interruption. A hard-killed downloader can leave `download.lock`; after checking
that no downloader is active, remove that specific lock directory and retry.
Windows uses an OS-managed file lock, released when the process exits.

## License

MIT
