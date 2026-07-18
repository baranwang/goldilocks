---
name: goldilocks
description: Use when Codex is about to call spawn_agent or inspect Goldilocks routing. Right-sizes model and reasoning effort only for subagents the active workflow has already decided to spawn. Never changes workflow, delegation, task content, or subagent count.
---

# Goldilocks

Same workflow. Right-sized subagents.

## Hard boundary

Only after the active workflow has already decided to call `spawn_agent`, choose the lowest-capability child configuration sufficient for that already-planned task.

Never decide whether to delegate. Never recommend or suppress delegation. Never split, merge, reorder, rewrite, add, or remove child tasks. Never select workflow skills or introduce planning, TDD, debugging, review, verification, worktrees, or branch steps.

Never change `message`, `task_name`, `fork_turns`, permissions, sandboxing, tools, or subagent count. Change only supported `model` and `reasoning_effort` fields. If routing cannot be applied without changing workflow semantics, preserve the original spawn.

## Route an already-planned spawn

1. Explicit user or applicable workflow choices always win. Preserve an explicitly chosen child model or effort when supported.
2. Read the current `spawn_agent` tool definition. Treat its allowed values and compatibility rules as authoritative.
3. Classify only the already-planned child task using the ordered routes below.
4. Set only schema-supported `model` and `reasoning_effort` values. Omit an unsupported or incompatible field.
5. Do not call another model or subagent to classify the task. Never invent a model slug or effort value from memory.

When `fork_turns` is omitted or `"all"`, and the current tool definition disallows compute overrides for full-history forks, keep the original spawn and omit `model` and `reasoning_effort`. Never change `fork_turns` to make routing possible. This applies equally to `reason` and `deep`.

## Ordered routes

1. `explore`: read-only finding, locating, comparing, mapping, evidence gathering, or summarizing.
2. `quick`: unambiguous, mechanical, local, low-risk work.
3. `build`: routine implementation, repair, or tests following established project patterns.
4. `reason`: root-cause diagnosis, correctness or security review, significant planning, or difficult edge cases.
5. `deep`: architecture, migration, concurrency, cross-module restructuring, substantial ambiguity, or long autonomous exploration.

Choose the lowest sufficient route. File count alone does not determine difficulty. Security, authorization, privacy, money, or data-loss risk requires at least `reason`. Concurrency, migrations, public API compatibility, and irreversible operations require at least `reason`. New cross-module boundaries or abstractions use `deep`. Upgrade after a failed attempt only when the failure reveals greater reasoning complexity. Resolve adjacent routes downward for low-risk work and upward when failure cost is high.

## Route table

| Route | Typical child task | Preferred model behavior | Effort |
|---|---|---|---|
| `quick` | Typo, formatting, explicit one-spot change, mechanical transformation | Prefer `gpt-5.6-luna`; if Luna is not an allowed explicit override, use `gpt-5.6-terra`; otherwise inherit | `low` |
| `explore` | Read-only code search, repository mapping, summarization, evidence gathering | Prefer `gpt-5.6-luna`; if Luna is not allowed, use `gpt-5.6-terra`; otherwise inherit | `low`; with Terra, use `medium` only when broader synthesis is needed |
| `build` | Routine implementation or fix following existing patterns | Omit `model` and inherit the parent model | `medium` |
| `reason` | Debugging, review, security analysis, planning, difficult tests or edge cases | Prefer `gpt-5.6-sol`; if Sol is unavailable, inherit | `high` |
| `deep` | Architecture, migration, concurrency, cross-module redesign, highly ambiguous autonomous work | Prefer `gpt-5.6-sol`; if Sol is unavailable, inherit the strongest current configuration | `xhigh` |

If the preferred effort is unsupported, use the closest supported effort without changing task scope.
For `explore`, `low` remains preferred; use `medium` only when broader synthesis is needed and Terra is the selected schema-supported fallback.

## Exact argument examples

Quick task when Luna is supported:

```json
{
  "task_name": "fix_typo",
  "message": "Correct the named typo only.",
  "fork_turns": "none",
  "model": "gpt-5.6-luna",
  "reasoning_effort": "low"
}
```

If Luna is not listed but Terra is listed, keep every other field identical and use `"model": "gpt-5.6-terra"`.

Routine build task—inherit the parent model:

```json
{
  "task_name": "implement_handler",
  "message": "Implement the already-specified handler using the existing pattern.",
  "fork_turns": "none",
  "reasoning_effort": "medium"
}
```

Deep task with an already-planned non-full-history fork when Sol and xhigh are supported:

```json
{
  "task_name": "design_migration",
  "message": "Analyze the already-scoped migration and return the requested design.",
  "fork_turns": "none",
  "model": "gpt-5.6-sol",
  "reasoning_effort": "xhigh"
}
```

Full-history task—preserve the fork and inherit compute:

```json
{
  "task_name": "map_auth_flow",
  "message": "Read the repository and report where authentication state enters, changes, and is consumed. Do not edit files.",
  "fork_turns": "all"
}
```

Goldilocks does not alter any omitted or existing non-compute argument.
