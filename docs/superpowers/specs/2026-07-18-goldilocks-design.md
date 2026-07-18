# Goldilocks — Codex Subagent Model Router Design

Date: 2026-07-18

## Summary

Build a small Codex plugin named **Goldilocks** that teaches every agent to choose a subagent model and reasoning effort before calling `spawn_agent`. The plugin follows Ponytail's implementation pattern: a real `SKILL.md` is the single human-readable policy source, while `SessionStart` and `SubagentStart` hooks inject that policy automatically. Normal routing does not require the user or agent to invoke the skill explicitly.

Goldilocks does not replace, add, remove, or reorder the user's existing workflow. It activates only at the point where an agent has already decided to delegate work to a subagent. Its sole responsibility is to help that agent choose a right-sized child model and reasoning effort.

The first version contains no classifier service, model catalog, MCP server, installer, telemetry, persistent state, workflow engine, or `PreToolUse` enforcement. The parent agent already has the task context needed for semantic classification, so adding a second model call or a keyword-based classifier would add cost while reducing judgment quality.

## Positioning

- Display name: `Goldilocks`
- Plugin slug: `goldilocks`
- Tagline: **Same workflow. Right-sized subagents.**
- Chinese positioning: **工作流不变，子代理刚刚好。**

The name is intentionally retained despite another Codex workflow plugin using Goldilocks. This project is differentiated by its deliberately narrow scope: it is not a workflow replacement, process-depth router, or Superpowers alternative. Public distribution must use clear marketplace qualification, repository description, and the tagline above to reduce confusion.

## Goals

- Reduce subagent cost and latency by selecting the lowest-capability route that is still sufficient for the task.
- Let the parent agent classify tasks semantically from their full context.
- Route by both task type and difficulty, not only by file count or keywords.
- Preserve explicit user choices for model and reasoning effort.
- Preserve the user's existing workflow exactly; Goldilocks must not decide whether planning, TDD, debugging, review, verification, worktrees, or other workflow steps should run.
- Work across Codex installations where the models exposed by `spawn_agent` differ.
- Stay small enough to understand, audit, and maintain as a personal Codex plugin.

## Non-goals

- Reproduce OmO/LazyCodex's provider discovery, fallback catalogs, role framework, workflow engine, MCP tools, installer, telemetry, or evidence system.
- Replace Superpowers or any other workflow, skill collection, agent harness, or project instructions.
- Decide whether a task should use brainstorming, planning, TDD, debugging, review, verification, or any other development process.
- Cause an agent to spawn a subagent when its existing workflow would not have spawned one.
- Call a separate model or router subagent before every spawn.
- Parse task text with hard-coded keyword scores.
- Guarantee routing through a blocking hook in the first version.
- Select non-Codex providers or maintain a cross-provider capability database.
- Persist per-session modes or expose a configuration UI in the first version.

## Source Findings

The design is based on current Codex behavior and the referenced projects:

- Codex lifecycle hooks can inject additional developer context at `SessionStart` and `SubagentStart`. `SessionStart` context is thread-local, so subagents need their own injection when nested delegation is possible. See the official [Hooks documentation](https://learn.chatgpt.com/docs/hooks).
- Codex supports custom subagent model and `model_reasoning_effort` settings. The exact models exposed to a `spawn_agent` call can vary, so the policy must inspect the current tool schema rather than assume every model slug is valid. See the official [Subagents documentation](https://learn.chatgpt.com/docs/agent-configuration/subagents).
- Ponytail registers a real skill and uses lifecycle hooks to read the skill body and inject it automatically. Its always-on behavior does not depend on Codex deciding to invoke the skill. See [DietrichGebert/ponytail](https://github.com/DietrichGebert/ponytail).
- OmO/LazyCodex separates light exploration and implementation from high-difficulty reasoning. Its Codex agents use Luna for exploration and lower/medium workers, while the high worker uses Sol. Its broader category system is valuable as a routing reference, but its provider catalogs and role framework are intentionally out of scope. See [code-yeongyu/lazycodex](https://github.com/code-yeongyu/lazycodex) and [code-yeongyu/oh-my-openagent](https://github.com/code-yeongyu/oh-my-openagent).

## Plugin Structure

```text
goldilocks/
├── .codex-plugin/
│   └── plugin.json
├── skills/
│   └── goldilocks/
│       └── SKILL.md
└── hooks/
    ├── hooks.json
    └── inject-router.js
```

The plugin uses the default `hooks/hooks.json` discovery path. It does not need a manifest-level hooks entry.

### `.codex-plugin/plugin.json`

The manifest declares:

- plugin name and version;
- the `skills/` directory;
- concise interface metadata describing automatic subagent model routing.

It does not declare an MCP server, app, assets, or unsupported fields.

### `skills/goldilocks/SKILL.md`

This file is the policy source of truth. It contains:

- the routing objective;
- the ordered classification rubric;
- the five route definitions;
- model capability fallback rules;
- explicit-override precedence;
- positive and negative examples;
- exact `spawn_agent` parameter examples.

The skill is registered normally so users can inspect or invoke it explicitly. Automatic routing does not wait for explicit invocation.

### `hooks/hooks.json`

The hook configuration registers the same zero-dependency script for:

- `SessionStart`, matching `startup|resume|clear|compact`;
- `SubagentStart`, matching every subagent type.

### `hooks/inject-router.js`

The script:

1. resolves the plugin root from `PLUGIN_ROOT`, with `CLAUDE_PLUGIN_ROOT` as a compatibility fallback;
2. reads `skills/goldilocks/SKILL.md`;
3. strips YAML frontmatter;
4. emits the body as `hookSpecificOutput.additionalContext` for the requested event;
5. exits successfully without output if the policy file cannot be read or parsed.

The script uses only Node.js standard-library modules. It does not access the network, transcripts, project files, or user configuration, and it writes no state.

## Runtime Flow

```text
Codex starts root thread
  -> SessionStart hook injects the router policy
  -> root agent receives a task
  -> the existing workflow independently decides whether delegation is appropriate
  -> before spawn_agent, root agent classifies the subtask
  -> root agent checks the current spawn_agent schema
  -> root agent sets supported model/reasoning parameters
  -> Codex starts the child
  -> SubagentStart injects the same policy into the child
  -> child follows the policy if it is allowed to delegate again
```

No additional model call occurs during routing. If the existing workflow never calls `spawn_agent`, Goldilocks has no operational effect beyond its small injected policy context.

## Workflow Invariance Contract

Goldilocks applies only after the active agent has independently decided to call `spawn_agent`. Its injected instructions must begin with this boundary and must never:

- recommend spawning or not spawning a subagent;
- change the number of subagents;
- split, merge, reorder, or rewrite delegated tasks;
- select or suppress workflow skills;
- introduce planning, TDD, debugging, review, verification, worktree, or branch-management steps;
- change `message`, `task_name`, `fork_turns`, permissions, sandboxing, or tool access for cost reasons.

Goldilocks may influence only the child configuration fields that control model capacity and reasoning intensity, initially `model` and `reasoning_effort`. If a future Codex version exposes another purely computational field such as service tier, supporting it requires an explicit design update rather than implicit expansion.

This contract is more important than cost savings. If routing cannot be applied without changing workflow semantics, Goldilocks must preserve the original spawn behavior.

## Classification Rubric

The agent applies these checks in order before every `spawn_agent` call:

1. If the user explicitly specified the child model or reasoning effort, preserve that choice.
2. Do not change the current workflow or the decision to delegate; classify only the already-planned child task.
3. If the task is read-only and its primary output is finding, locating, comparing, or summarizing information, choose `explore`.
4. If the task is unambiguous, mechanical, local, and low-risk, choose `quick`.
5. If the task follows established project patterns for an ordinary implementation, fix, or test, choose `build`.
6. If the task requires root-cause diagnosis, correctness or security review, significant planning, or difficult edge-case reasoning, choose `reason`.
7. If the task requires architecture, migration, concurrency design, cross-module restructuring, extensive autonomous exploration, or resolution of substantial ambiguity, choose `deep`.

The policy tells the agent to choose the lowest sufficient route, not the lowest route at any cost.

### Mandatory escalation

- Security, authorization, privacy, money, or data-loss risk: at least `reason`.
- Concurrency, migrations, public API compatibility, or irreversible operations: at least `reason`.
- A new boundary or abstraction spanning multiple modules: `deep`.
- Small file count is never sufficient evidence for a low route.

### Cost-preserving de-escalation

- A large number of files does not imply deep reasoning when the change is mechanical.
- A large repository does not by itself promote a read-only search above `explore`.
- A failed first attempt does not justify escalation unless the failure reveals greater reasoning complexity.
- When uncertain between adjacent routes, choose the lower route for low-risk work and the higher route when failure cost is high.

## Route Table

| Route | Typical work | Preferred model behavior | Reasoning effort |
|---|---|---|---|
| `quick` | Typos, formatting, explicit one-spot changes, mechanical transformations | Prefer `gpt-5.6-luna`; if Luna is not an allowed explicit override, use `gpt-5.6-terra` | `low` |
| `explore` | Read-only code search, repository mapping, summarization, evidence gathering | Prefer `gpt-5.6-luna`; if Luna is not an allowed explicit override, use `gpt-5.6-terra` | `low`; use `medium` with Terra when broader synthesis is needed |
| `build` | Routine implementation or fixes following existing patterns | Omit `model` and inherit the parent model | `medium` |
| `reason` | Debugging, code review, security analysis, planning, difficult tests and edge cases | Prefer `gpt-5.6-sol`; if unavailable, inherit the parent model | `high` |
| `deep` | Architecture, migrations, concurrency, cross-module redesign, highly ambiguous autonomous work | Prefer `gpt-5.6-sol`; if unavailable, use the strongest model explicitly allowed by the current tool schema or inherit the parent | `xhigh` |

The skill must state: use only model values exposed by the current `spawn_agent` schema. Never invent or assume a model slug from memory.

## Precedence Rules

From highest to lowest priority:

1. Explicit user instruction for a child model or reasoning effort.
2. A task-specific constraint from applicable repository instructions or a named workflow skill.
3. The router's classification and route table.
4. Parent-model inheritance when the preferred explicit model is unavailable.

The router never silently replaces an explicit user selection to save cost.

## Hook Output and Context Budget

The injected policy should remain concise, targeting roughly 1,200 tokens and staying below Codex's approximately 2,500-token model-visible hook-output threshold. Examples should be short and chosen to clarify boundary cases rather than exhaustively enumerate tasks.

The injected body should not include the skill's YAML frontmatter, installation instructions, implementation notes, or long rationale.

## Failure Behavior

- Injection failure is fail-open: Codex and subagents continue without router context.
- The hook never blocks session or subagent startup.
- Unsupported model values are avoided through schema inspection instructions, not runtime provider probing.
- If a model does not support the preferred effort, the agent selects the closest supported effort without changing task scope.
- The first version does not retry or rewrite malformed `spawn_agent` calls through `PreToolUse`.

Fail-open behavior is appropriate because routing is a cost optimization, not a security boundary.

## Validation

### Static validation

- Validate the plugin manifest and directory structure with the Codex plugin validator.
- Validate the skill structure with the skill validator.
- Confirm the hook configuration references files inside the plugin root.

### Unit tests

Use Node's built-in test runner to cover:

- frontmatter removal;
- `SessionStart` JSON output;
- `SubagentStart` JSON output;
- Unicode policy content;
- missing or unreadable skill file fail-open behavior;
- Unix and Windows plugin-root environment variables;
- output size staying below the chosen context budget.

The `SKILL.md` contract test must also confirm that the injected policy explicitly says routing begins only after the delegation decision and forbids changing workflow, task decomposition, task content, and subagent count.

### Isolated Codex smoke test

Install the local plugin into an isolated `CODEX_HOME` and verify:

1. the plugin loads and its hooks can be reviewed and trusted;
2. the router policy appears in root-thread context;
3. the router policy appears in subagent context;
4. representative `quick`, `explore`, `build`, `reason`, and `deep` tasks produce the expected `spawn_agent` parameters;
5. `build` omits the model and uses `medium` effort;
6. an explicit user model selection wins;
7. Luna routes fall back when Luna is absent from the current spawn schema;
8. disabling or breaking the hook does not block Codex.
9. representative prompts do not cause Goldilocks to add, remove, or restructure subagent calls compared with the same workflow without Goldilocks; only supported model and reasoning fields may differ.

## Deferred Extensions

Only add these after observed need:

- a `PreToolUse` guard that rejects unclassified spawns;
- persistent `off`, `conservative`, or `aggressive` modes;
- a separate `routes.json` for shared or frequently edited mappings;
- route-decision logging and diagnostics;
- additional categories such as visual or writing when they map to meaningfully different models or prompting behavior;
- provider discovery or fallback chains.

## Acceptance Criteria

The design is successfully implemented when:

- installing and enabling the plugin automatically injects the routing policy into root agents and subagents;
- users do not need to invoke the skill for normal operation;
- the plugin never changes whether or how the existing workflow performs planning, implementation, testing, review, verification, or delegation;
- the plugin has no operational routing effect unless the existing workflow calls `spawn_agent`;
- for an already-planned spawn, Goldilocks changes only supported computational configuration fields and leaves task content, decomposition, count, context forking, permissions, and tools unchanged;
- agents classify before each `spawn_agent` call and use schema-supported parameters;
- `quick` and `explore` prefer Luna with capability-aware fallback;
- `build` always inherits the parent model with medium reasoning;
- `reason` and `deep` prefer Sol at higher reasoning levels;
- explicit user model choices are preserved;
- no extra classifier model call, MCP server, installer, telemetry, or persistent state is introduced;
- hook failure does not prevent Codex from working.
