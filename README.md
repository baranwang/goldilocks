# Goldilocks

**Same workflow. Right-sized subagents.**

**工作流不变，子代理刚刚好。**

Goldilocks is a small Codex plugin that helps an agent choose a suitable child
model and reasoning effort only after the active workflow has already decided
to call `spawn_agent`.

It does not decide whether to use subagents. It does not add planning, TDD,
debugging, review, verification, worktrees, or any other process. It does not
change task content, decomposition, child count, context forking, permissions,
or tools.

## How it works

`SessionStart` and `SubagentStart` hooks inject one compact policy from
`skills/goldilocks/SKILL.md`. Before an already-planned spawn, the active agent
preserves explicit choices, checks the current tool schema, classifies the child
task, and changes only supported `model` and `reasoning_effort` fields.

| Route | Intended work | Default behavior |
|---|---|---|
| `quick`（快速） | Mechanical, local, low-risk | Prefer Luna/low; fall back to Terra/low |
| `explore`（探索） | Read-only search and synthesis | Prefer Luna/low; fall back to Terra/medium |
| `build`（实现） | Routine implementation using existing patterns | Inherit parent model/medium |
| `reason`（推理） | Debugging, review, security, difficult edge cases | Prefer Sol/high; otherwise inherit/high |
| `deep`（深度） | Architecture, migrations, concurrency, cross-module ambiguity | Prefer Sol/xhigh; otherwise inherit/xhigh |

Only values exposed by the current `spawn_agent` schema are used; Goldilocks
does not assume a fixed model catalog. Explicit user and workflow choices always
win. If an omitted or `"all"` `fork_turns` full-history spawn cannot accept
compute overrides, Goldilocks keeps `fork_turns` unchanged and inherits compute;
it never changes the fork to force routing.

## Local installation

From this repository:

```bash
codex plugin marketplace add ./
codex plugin add goldilocks@goldilocks-router
```

Open `/hooks`, review and trust the Goldilocks hooks, then start a new Codex
task.

## Development

```bash
npm test
python3 "$HOME/.codex/skills/.system/plugin-creator/scripts/validate_plugin.py" plugins/goldilocks
python3 "$HOME/.codex/skills/.system/skill-creator/scripts/quick_validate.py" plugins/goldilocks/skills/goldilocks
```

Runtime code uses only the Node.js standard library and does not access the
network or write state.

## Name

Goldilocks means “not too much, not too little—just right.” This project is
narrowly focused on subagent compute selection and is not affiliated with other
projects using the Goldilocks name.

## License

MIT
