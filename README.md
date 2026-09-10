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

One bundled Go CLI provides hooks, PR monitoring, and watcher registration.
No Python or Go installation is required; online PR reads use authenticated gh.
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

Ask to monitor a PR, or use `$pr-watch`. The main task receives CI, comments,
and review evidence; its child owns polling, delivery, acknowledgements, and
cleanup until merge, closure, or an explicit stop. The default poll interval is
60 seconds. Monitoring does not authorize posting, pushing, or merging.

Standalone skills contain instructions only and require a compatible CLI.
Without loaded, trusted hooks, the child skips watcher registration and reports
missing lifecycle protection. No compatible CLI means monitoring cannot start.
Local monitoring cannot guarantee continuation during machine sleep, app exit,
hard interruption, or quota exhaustion. Tool waits can consume tokens; this is
not a zero-cost daemon.

## License

MIT
