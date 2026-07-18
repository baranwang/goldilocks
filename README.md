<p align="center">
  <img src="assets/logo.webp" alt="Goldilocks logo" width="192">
</p>

<p align="center">
  English · <a href="README.zh-hans.md">简体中文</a> · <a href="README.zh-hant.md">正體中文</a>
</p>

# Goldilocks

**No workflow changes. Just the right model.**

Goldilocks is a lightweight Codex plugin. It steps in only after the existing
workflow has decided to create a subagent, helping select a suitable model and
reasoning effort.

It does not decide whether to create subagents or change tasks and workflows.
Its only responsibility is choosing the model and reasoning effort.

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

Use `/hooks` to review and trust the Goldilocks hook scripts, then start a new
Codex task for the plugin to take effect.

## How it works

`SessionStart` and `SubagentStart` hooks inject a compact policy from
`skills/goldilocks/SKILL.md`. Before an already-planned `spawn_agent` call, the
current agent preserves explicit user choices, checks the tool schema,
classifies the child task, and changes only supported `model` and
`reasoning_effort` fields.

The runtime stays minimal: it uses native POSIX `sh`/`awk` on macOS and Linux,
and PowerShell on Windows. It requires neither Node.js nor Python.

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

## License

MIT
