# Goldilocks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a lightweight Codex plugin that preserves the active workflow and only right-sizes `model` and `reasoning_effort` when an agent has already decided to call `spawn_agent`.

**Architecture:** The repository contains a local Codex marketplace and one plugin under `plugins/goldilocks/`. A single zero-dependency Node hook reads the registered `SKILL.md`, strips frontmatter, and injects the same routing policy at `SessionStart` and `SubagentStart`; the policy—not JavaScript heuristics—guides the active agent's semantic choice.

**Tech Stack:** Codex plugin manifest and lifecycle hooks, Markdown `SKILL.md`, CommonJS on the Node.js standard library, Node's built-in test runner, Python-based Codex plugin/skill validators, isolated `CODEX_HOME` smoke tests.

## Global Constraints

- Product name: `Goldilocks`; plugin slug: `goldilocks`; marketplace qualifier: `goldilocks-router`.
- Tagline: `Same workflow. Right-sized subagents.` Chinese positioning: `工作流不变，子代理刚刚好。`
- Goldilocks starts only after the active workflow has independently decided to call `spawn_agent`.
- Never recommend, suppress, add, remove, split, merge, reorder, or rewrite subagent work.
- Never change `message`, `task_name`, `fork_turns`, subagent count, permissions, sandboxing, tools, or workflow skills.
- Initially influence only supported `model` and `reasoning_effort` fields.
- Explicit user or applicable workflow choices for child model/effort always win.
- No classifier model call, MCP server, model catalog, provider discovery, installer, telemetry, persistent state, configuration UI, or `PreToolUse` enforcement.
- Hook failures are silent and fail open.
- Runtime code has no third-party dependencies and performs no network access or writes.
- Injected policy body must stay below 9,000 characters and target roughly 1,200 model tokens.

---

## Planned File Map

- `.agents/plugins/marketplace.json` — repository-local marketplace named `goldilocks-router`, pointing at `plugins/goldilocks`.
- `plugins/goldilocks/.codex-plugin/plugin.json` — minimal installable Codex plugin metadata.
- `plugins/goldilocks/hooks/hooks.json` — `SessionStart` and `SubagentStart` lifecycle wiring.
- `plugins/goldilocks/hooks/inject-router.js` — frontmatter stripping, policy loading, fail-open hook output.
- `plugins/goldilocks/skills/goldilocks/SKILL.md` — the sole semantic routing policy and workflow-invariance contract.
- `tests/package-contract.test.js` — marketplace and manifest contract tests.
- `tests/inject-router.test.js` — hook runtime and hook configuration tests.
- `tests/policy-contract.test.js` — routing table, precedence, context budget, and workflow-invariance tests.
- `tests/docs-contract.test.js` — public positioning and local install documentation tests.
- `package.json` — dependency-free test command; marked private to avoid npm namespace collision.
- `README.md` — user-facing purpose, non-goals, routes, installation, and development commands.
- `LICENSE` — MIT license for Goldilocks contributors.

### Task 1: Scaffold the Repository Marketplace and Minimal Plugin Package

**Files:**
- Create: `.agents/plugins/marketplace.json`
- Create: `plugins/goldilocks/.codex-plugin/plugin.json`
- Create: `package.json`
- Create: `tests/package-contract.test.js`

**Interfaces:**
- Consumes: `docs/superpowers/specs/2026-07-18-goldilocks-design.md` naming, scope, and packaging requirements.
- Produces: marketplace identity `goldilocks-router`, plugin root `plugins/goldilocks`, plugin identity `goldilocks@0.1.0`, and the `npm test` entry point used by later tasks.

- [ ] **Step 1: Write the failing marketplace and manifest contract test**

Create `tests/package-contract.test.js`:

```js
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const root = path.join(__dirname, '..');

function readJson(relativePath) {
  return JSON.parse(fs.readFileSync(path.join(root, relativePath), 'utf8'));
}

test('repository marketplace exposes the uniquely qualified Goldilocks plugin', () => {
  const marketplace = readJson('.agents/plugins/marketplace.json');
  assert.equal(marketplace.name, 'goldilocks-router');
  assert.equal(marketplace.interface.displayName, 'Goldilocks Router');
  assert.equal(marketplace.plugins.length, 1);

  const entry = marketplace.plugins[0];
  assert.equal(entry.name, 'goldilocks');
  assert.deepEqual(entry.source, {
    source: 'local',
    path: './plugins/goldilocks',
  });
  assert.deepEqual(entry.policy, {
    installation: 'AVAILABLE',
    authentication: 'ON_INSTALL',
  });
  assert.equal(entry.category, 'Developer Tools');
});

test('plugin manifest is minimal and does not claim workflow or tool capabilities', () => {
  const manifest = readJson('plugins/goldilocks/.codex-plugin/plugin.json');
  assert.equal(manifest.name, 'goldilocks');
  assert.equal(manifest.version, '0.1.0');
  assert.equal(manifest.description, 'Same workflow. Right-sized subagents.');
  assert.equal(manifest.skills, './skills/');
  assert.equal(manifest.interface.displayName, 'Goldilocks');
  assert.equal(manifest.interface.category, 'Developer Tools');
  assert.deepEqual(manifest.interface.capabilities, ['Instructions', 'Lifecycle hooks']);
  assert.equal('mcpServers' in manifest, false);
  assert.equal('apps' in manifest, false);
  assert.equal('hooks' in manifest, false, 'default hooks/hooks.json discovery should be used');
});

test('development package is private and dependency-free', () => {
  const pkg = readJson('package.json');
  assert.equal(pkg.name, 'goldilocks-codex-router');
  assert.equal(pkg.private, true);
  assert.equal(pkg.scripts.test, 'node --test tests/*.test.js');
  assert.deepEqual(pkg.dependencies, undefined);
  assert.deepEqual(pkg.devDependencies, undefined);
});
```

- [ ] **Step 2: Run the contract test and confirm it fails before scaffolding**

Run:

```bash
node --test tests/package-contract.test.js
```

Expected: FAIL with `ENOENT` for `.agents/plugins/marketplace.json` or `plugins/goldilocks/.codex-plugin/plugin.json`.

- [ ] **Step 3: Generate the canonical plugin/marketplace skeleton**

Run from the repository root:

```bash
python3 "$HOME/.codex/skills/.system/plugin-creator/scripts/create_basic_plugin.py" \
  goldilocks \
  --path plugins \
  --marketplace-path .agents/plugins/marketplace.json \
  --marketplace-name goldilocks-router \
  --with-marketplace
```

Expected: creates `plugins/goldilocks/.codex-plugin/plugin.json` and `.agents/plugins/marketplace.json` without writing outside this repository.

- [ ] **Step 4: Replace the scaffold metadata with the exact marketplace contract**

Set `.agents/plugins/marketplace.json` to:

```json
{
  "name": "goldilocks-router",
  "interface": {
    "displayName": "Goldilocks Router"
  },
  "plugins": [
    {
      "name": "goldilocks",
      "source": {
        "source": "local",
        "path": "./plugins/goldilocks"
      },
      "policy": {
        "installation": "AVAILABLE",
        "authentication": "ON_INSTALL"
      },
      "category": "Developer Tools"
    }
  ]
}
```

Set `plugins/goldilocks/.codex-plugin/plugin.json` to:

```json
{
  "name": "goldilocks",
  "version": "0.1.0",
  "description": "Same workflow. Right-sized subagents.",
  "license": "MIT",
  "keywords": [
    "codex",
    "subagents",
    "model-routing",
    "reasoning-effort"
  ],
  "skills": "./skills/",
  "interface": {
    "displayName": "Goldilocks",
    "shortDescription": "Right-size Codex subagents without changing workflow",
    "longDescription": "Keeps the active workflow intact and guides agents to choose supported model and reasoning settings only when a subagent is already being spawned.",
    "developerName": "Goldilocks contributors",
    "category": "Developer Tools",
    "capabilities": [
      "Instructions",
      "Lifecycle hooks"
    ],
    "defaultPrompt": [
      "Explain how Goldilocks routes an already-planned subagent."
    ],
    "brandColor": "#D4A72C"
  }
}
```

Create `package.json`:

```json
{
  "name": "goldilocks-codex-router",
  "version": "0.1.0",
  "private": true,
  "description": "Same workflow. Right-sized Codex subagents.",
  "license": "MIT",
  "scripts": {
    "test": "node --test tests/*.test.js"
  }
}
```

- [ ] **Step 5: Run the package contract test and plugin validator**

Run:

```bash
node --test tests/package-contract.test.js
python3 "$HOME/.codex/skills/.system/plugin-creator/scripts/validate_plugin.py" plugins/goldilocks
```

Expected: all three Node subtests PASS; plugin validator exits `0` and reports the plugin valid.

- [ ] **Step 6: Commit the package skeleton**

```bash
git add .agents/plugins/marketplace.json plugins/goldilocks/.codex-plugin/plugin.json package.json tests/package-contract.test.js
git commit -m "chore: scaffold Goldilocks plugin package"
```

### Task 2: Implement Fail-Open Lifecycle Policy Injection

**Files:**
- Create: `plugins/goldilocks/hooks/hooks.json`
- Create: `plugins/goldilocks/hooks/inject-router.js`
- Create: `tests/inject-router.test.js`

**Interfaces:**
- Consumes: plugin root from `PLUGIN_ROOT` or compatibility fallback `CLAUDE_PLUGIN_ROOT`; event name from CLI argument `SessionStart` or `SubagentStart`; policy text at `skills/goldilocks/SKILL.md`.
- Produces: `stripFrontmatter(text): string`, `buildHookOutput(eventName, additionalContext): object`, and a CLI that emits Codex `hookSpecificOutput.additionalContext` JSON or silent success.

- [ ] **Step 1: Write the failing hook runtime tests**

Create `tests/inject-router.test.js`:

```js
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = path.join(__dirname, '..');
const script = path.join(root, 'plugins', 'goldilocks', 'hooks', 'inject-router.js');

function cleanEnv(extra = {}) {
  const env = { ...process.env };
  delete env.PLUGIN_ROOT;
  delete env.CLAUDE_PLUGIN_ROOT;
  return { ...env, ...extra };
}

function runHook(eventName, env = {}) {
  return spawnSync(process.execPath, [script, eventName], {
    env: cleanEnv(env),
    encoding: 'utf8',
  });
}

function makePluginRoot(t) {
  const pluginRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'goldilocks-hook-'));
  t.after(() => fs.rmSync(pluginRoot, { recursive: true, force: true }));
  const skillDir = path.join(pluginRoot, 'skills', 'goldilocks');
  fs.mkdirSync(skillDir, { recursive: true });
  fs.writeFileSync(
    path.join(skillDir, 'SKILL.md'),
    '\uFEFF---\nname: goldilocks\ndescription: test\n---\n\nPOLICY BODY\n',
  );
  return pluginRoot;
}

test('stripFrontmatter removes BOM and YAML metadata', () => {
  const { stripFrontmatter } = require('../plugins/goldilocks/hooks/inject-router');
  assert.equal(
    stripFrontmatter('\uFEFF---\r\nname: goldilocks\r\n---\r\n\r\nPolicy\r\n'),
    'Policy',
  );
});

test('buildHookOutput uses Codex hookSpecificOutput shape only', () => {
  const { buildHookOutput } = require('../plugins/goldilocks/hooks/inject-router');
  assert.deepEqual(buildHookOutput('SessionStart', 'Policy'), {
    hookSpecificOutput: {
      hookEventName: 'SessionStart',
      additionalContext: 'Policy',
    },
  });
});

test('SessionStart loads the policy through PLUGIN_ROOT', (t) => {
  const pluginRoot = makePluginRoot(t);
  const result = runHook('SessionStart', { PLUGIN_ROOT: pluginRoot });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), {
    hookSpecificOutput: {
      hookEventName: 'SessionStart',
      additionalContext: 'POLICY BODY',
    },
  });
});

test('SubagentStart supports CLAUDE_PLUGIN_ROOT compatibility fallback', (t) => {
  const pluginRoot = makePluginRoot(t);
  const result = runHook('SubagentStart', { CLAUDE_PLUGIN_ROOT: pluginRoot });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).hookSpecificOutput.hookEventName, 'SubagentStart');
});

test('missing roots, missing policy, and unsupported events fail open silently', (t) => {
  let result = runHook('SessionStart');
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, '');

  const emptyRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'goldilocks-empty-'));
  t.after(() => fs.rmSync(emptyRoot, { recursive: true, force: true }));
  result = runHook('SessionStart', { PLUGIN_ROOT: emptyRoot });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, '');

  const pluginRoot = makePluginRoot(t);
  result = runHook('UserPromptSubmit', { PLUGIN_ROOT: pluginRoot });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, '');
});

test('hooks.json registers only SessionStart and SubagentStart', () => {
  const hooks = JSON.parse(
    fs.readFileSync(path.join(root, 'plugins', 'goldilocks', 'hooks', 'hooks.json'), 'utf8'),
  );
  assert.deepEqual(Object.keys(hooks.hooks).sort(), ['SessionStart', 'SubagentStart']);
  assert.equal(hooks.hooks.SessionStart[0].matcher, 'startup|resume|clear|compact');
  assert.match(hooks.hooks.SessionStart[0].hooks[0].command, /inject-router\.js" SessionStart$/);
  assert.match(hooks.hooks.SubagentStart[0].hooks[0].command, /inject-router\.js" SubagentStart$/);
});
```

- [ ] **Step 2: Run the hook tests and confirm they fail before implementation**

Run:

```bash
node --test tests/inject-router.test.js
```

Expected: FAIL with `MODULE_NOT_FOUND` for `plugins/goldilocks/hooks/inject-router.js` or `ENOENT` for `hooks.json`.

- [ ] **Step 3: Implement the minimal zero-dependency hook runtime**

Create `plugins/goldilocks/hooks/inject-router.js`:

```js
#!/usr/bin/env node

const fs = require('node:fs');
const path = require('node:path');

const SUPPORTED_EVENTS = new Set(['SessionStart', 'SubagentStart']);

function stripFrontmatter(text) {
  return String(text)
    .replace(/^\uFEFF/, '')
    .replace(/^---\r?\n[\s\S]*?\r?\n---\r?\n?/, '')
    .trim();
}

function buildHookOutput(eventName, additionalContext) {
  return {
    hookSpecificOutput: {
      hookEventName: eventName,
      additionalContext,
    },
  };
}

function loadPolicy(pluginRoot) {
  const skillPath = path.join(pluginRoot, 'skills', 'goldilocks', 'SKILL.md');
  return stripFrontmatter(fs.readFileSync(skillPath, 'utf8'));
}

function main(eventName = process.argv[2], env = process.env, stdout = process.stdout) {
  if (!SUPPORTED_EVENTS.has(eventName)) return false;

  const pluginRoot = env.PLUGIN_ROOT || env.CLAUDE_PLUGIN_ROOT;
  if (!pluginRoot) return false;

  try {
    const policy = loadPolicy(pluginRoot);
    if (!policy) return false;
    stdout.write(JSON.stringify(buildHookOutput(eventName, policy)));
    return true;
  } catch {
    return false;
  }
}

if (require.main === module) {
  main();
}

module.exports = {
  buildHookOutput,
  loadPolicy,
  main,
  stripFrontmatter,
};
```

- [ ] **Step 4: Register both lifecycle events through default hook discovery**

Create `plugins/goldilocks/hooks/hooks.json`:

```json
{
  "description": "Inject Goldilocks subagent sizing policy without changing workflow.",
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup|resume|clear|compact",
        "hooks": [
          {
            "type": "command",
            "command": "node \"${PLUGIN_ROOT}/hooks/inject-router.js\" SessionStart",
            "commandWindows": "if (Get-Command node -ErrorAction SilentlyContinue) { node \"$env:PLUGIN_ROOT\\hooks\\inject-router.js\" SessionStart }",
            "timeout": 5,
            "statusMessage": "Loading Goldilocks routing..."
          }
        ]
      }
    ],
    "SubagentStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "node \"${PLUGIN_ROOT}/hooks/inject-router.js\" SubagentStart",
            "commandWindows": "if (Get-Command node -ErrorAction SilentlyContinue) { node \"$env:PLUGIN_ROOT\\hooks\\inject-router.js\" SubagentStart }",
            "timeout": 5,
            "statusMessage": "Loading Goldilocks routing..."
          }
        ]
      }
    ]
  }
}
```

- [ ] **Step 5: Run hook tests and the full test command**

Run:

```bash
node --test tests/inject-router.test.js
npm test
```

Expected: hook tests PASS; all repository tests PASS.

- [ ] **Step 6: Validate the plugin and commit the hook runtime**

```bash
python3 "$HOME/.codex/skills/.system/plugin-creator/scripts/validate_plugin.py" plugins/goldilocks
git add plugins/goldilocks/hooks tests/inject-router.test.js
git commit -m "feat: inject Goldilocks routing policy"
```

Expected: validator exits `0`; commit contains no policy text yet beyond test fixtures.

### Task 3: Define the Workflow-Invariant Routing Policy

**Files:**
- Create: `plugins/goldilocks/skills/goldilocks/SKILL.md`
- Create: `tests/policy-contract.test.js`

**Interfaces:**
- Consumes: `stripFrontmatter(text)` from `plugins/goldilocks/hooks/inject-router.js`.
- Produces: one injected agent policy containing the hard boundary, schema-aware precedence, five semantic routes, and exact `spawn_agent` examples.

- [ ] **Step 1: Write the failing policy contract tests**

Create `tests/policy-contract.test.js`:

```js
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const { stripFrontmatter } = require('../plugins/goldilocks/hooks/inject-router');

const skillPath = path.join(
  __dirname,
  '..',
  'plugins',
  'goldilocks',
  'skills',
  'goldilocks',
  'SKILL.md',
);

function readPolicy() {
  const raw = fs.readFileSync(skillPath, 'utf8');
  return { raw, body: stripFrontmatter(raw) };
}

test('skill metadata describes routing without claiming workflow ownership', () => {
  const { raw } = readPolicy();
  assert.match(raw, /^---\nname: goldilocks\n/);
  assert.match(raw, /only for subagents the active workflow has already decided to spawn/i);
  assert.match(raw, /never changes workflow,\s+delegation, task content, or subagent count/i);
});

test('policy begins with the workflow-invariance boundary', () => {
  const { body } = readPolicy();
  const boundary = 'Only after the active workflow has already decided to call `spawn_agent`';
  assert.ok(body.indexOf(boundary) >= 0);
  assert.ok(body.indexOf(boundary) < 500, 'boundary must appear before routing advice');
  assert.match(body, /Never decide whether to delegate/);
  assert.match(body, /Never change `message`, `task_name`, `fork_turns`, permissions, sandboxing, tools, or subagent count/);
  assert.match(body, /Change only supported `model` and `reasoning_effort` fields/);
});

test('policy preserves explicit choices and requires schema-aware values', () => {
  const { body } = readPolicy();
  assert.match(body, /Explicit user or applicable workflow choices always win/);
  assert.match(body, /Read the current `spawn_agent` tool definition/);
  assert.match(body, /Never invent a model slug or effort value from memory/);
  assert.match(body, /Do not call another model or subagent to classify the task/);
});

test('policy contains all five exact routes', () => {
  const { body } = readPolicy();
  assert.match(body, /\| `quick` \|[\s\S]*`gpt-5\.6-luna`[\s\S]*`gpt-5\.6-terra`[\s\S]*`low` \|/);
  assert.match(body, /\| `explore` \|[\s\S]*`gpt-5\.6-luna`[\s\S]*`gpt-5\.6-terra`[\s\S]*`low` or `medium` \|/);
  assert.match(body, /\| `build` \|[\s\S]*Omit `model`[\s\S]*`medium` \|/);
  assert.match(body, /\| `reason` \|[\s\S]*`gpt-5\.6-sol`[\s\S]*`high` \|/);
  assert.match(body, /\| `deep` \|[\s\S]*`gpt-5\.6-sol`[\s\S]*`xhigh` \|/);
});

test('policy stays within the context budget and contains concrete spawn examples', () => {
  const { body } = readPolicy();
  assert.ok(body.length < 9000, `policy is ${body.length} characters; expected < 9000`);
  assert.match(body, /"task_name": "fix_typo"/);
  assert.match(body, /"task_name": "implement_handler"/);
  assert.match(body, /"reasoning_effort": "medium"/);
  assert.match(body, /"reasoning_effort": "xhigh"/);
});
```

- [ ] **Step 2: Run the policy tests and confirm they fail before the skill exists**

Run:

```bash
node --test tests/policy-contract.test.js
```

Expected: FAIL with `ENOENT` for `plugins/goldilocks/skills/goldilocks/SKILL.md`.

- [ ] **Step 3: Write the complete routing skill**

Create `plugins/goldilocks/skills/goldilocks/SKILL.md`:

````markdown
---
name: goldilocks
description: >
  Right-sizes model and reasoning effort only for subagents the active workflow
  has already decided to spawn. Use when Codex is about to call spawn_agent or
  when the user asks to inspect Goldilocks routing. Never changes workflow,
  delegation, task content, or subagent count.
---

# Goldilocks

Same workflow. Right-sized subagents.

## Hard boundary

Only after the active workflow has already decided to call `spawn_agent`, choose the lowest-capability child configuration that is sufficient for that already-planned task.

Never decide whether to delegate. Never recommend or suppress delegation. Never split, merge, reorder, rewrite, add, or remove child tasks. Never select workflow skills or introduce planning, TDD, debugging, review, verification, worktrees, or branch steps.

Never change `message`, `task_name`, `fork_turns`, permissions, sandboxing, tools, or subagent count. Change only supported `model` and `reasoning_effort` fields. If routing cannot be applied without changing workflow semantics, preserve the original spawn.

## Before an already-planned spawn

1. Explicit user or applicable workflow choices always win. Preserve an explicitly chosen child model or effort.
2. Read the current `spawn_agent` tool definition. Its allowed `model` and `reasoning_effort` values are authoritative.
3. Classify only the already-planned child task with the ordered rules below.
4. Set only schema-supported `model` and `reasoning_effort` values. Omit an unsupported field.
5. Do not call another model or subagent to classify the task. Never invent a model slug or effort value from memory.

## Ordered classification

1. `explore`: read-only finding, locating, comparing, mapping, evidence gathering, or summarizing.
2. `quick`: unambiguous, mechanical, local, and low-risk work.
3. `build`: routine implementation, repair, or tests that follow established project patterns.
4. `reason`: root-cause diagnosis, correctness/security review, significant planning, or difficult edge cases.
5. `deep`: architecture, migration, concurrency, cross-module restructuring, substantial ambiguity, or long autonomous exploration.

Choose the lowest sufficient route. Small file count never proves low difficulty. Many files do not prove deep difficulty when the work is mechanical.

Security, authorization, privacy, money, or data-loss risk requires at least `reason`. Concurrency, migrations, public API compatibility, and irreversible operations require at least `reason`. New cross-module boundaries or abstractions use `deep`.

Do not upgrade because a first attempt failed unless the failure reveals greater reasoning complexity. When adjacent routes are both plausible, use the lower route for low-risk work and the higher route when failure cost is high.

## Route table

| Route | Typical child task | Preferred model behavior | Effort |
|---|---|---|---|
| `quick` | Typo, formatting, explicit one-spot change, mechanical transformation | Prefer `gpt-5.6-luna`; if Luna is not an allowed explicit override, use `gpt-5.6-terra`; otherwise inherit | `low` |
| `explore` | Read-only code search, repository mapping, summarization, evidence gathering | Prefer `gpt-5.6-luna`; if Luna is not allowed, use `gpt-5.6-terra`; otherwise inherit | `low` or `medium` |
| `build` | Routine implementation or fix following existing patterns | Omit `model` and inherit the parent model | `medium` |
| `reason` | Debugging, review, security analysis, planning, difficult tests or edge cases | Prefer `gpt-5.6-sol`; if Sol is unavailable, inherit | `high` |
| `deep` | Architecture, migration, concurrency, cross-module redesign, highly ambiguous autonomous work | Prefer `gpt-5.6-sol`; if Sol is unavailable, inherit the strongest current configuration | `xhigh` |

If the preferred effort is unsupported, use the closest supported effort without changing task scope.

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

Deep task when Sol is supported:

```json
{
  "task_name": "design_migration",
  "message": "Analyze the already-scoped migration and return the requested design.",
  "fork_turns": "all",
  "model": "gpt-5.6-sol",
  "reasoning_effort": "xhigh"
}
```

Goldilocks does not alter any omitted or existing non-compute argument.
````

- [ ] **Step 4: Run the policy contract, skill validator, and full suite**

Run:

```bash
node --test tests/policy-contract.test.js
python3 "$HOME/.codex/skills/.system/skill-creator/scripts/quick_validate.py" plugins/goldilocks/skills/goldilocks
npm test
```

Expected: policy tests PASS; skill validator exits `0`; all repository tests PASS.

- [ ] **Step 5: Verify actual hook output contains exactly the skill body**

Run:

```bash
PLUGIN_ROOT="$PWD/plugins/goldilocks" \
  node plugins/goldilocks/hooks/inject-router.js SessionStart \
  > /tmp/goldilocks-session-start.json

node -e '
const fs = require("node:fs");
const out = JSON.parse(fs.readFileSync("/tmp/goldilocks-session-start.json", "utf8"));
if (out.hookSpecificOutput.hookEventName !== "SessionStart") process.exit(1);
if (!out.hookSpecificOutput.additionalContext.includes("Same workflow. Right-sized subagents.")) process.exit(1);
if (out.additionalContext !== undefined) process.exit(1);
'
```

Expected: both commands exit `0`; output uses only `hookSpecificOutput.additionalContext`.

- [ ] **Step 6: Commit the semantic policy**

```bash
git add plugins/goldilocks/skills/goldilocks/SKILL.md tests/policy-contract.test.js
git commit -m "feat: define Goldilocks routing policy"
```

### Task 4: Document, Validate, and Smoke-Test the Installable Plugin

**Files:**
- Create: `README.md`
- Create: `LICENSE`
- Create: `tests/docs-contract.test.js`

**Interfaces:**
- Consumes: marketplace identity from Task 1, runtime behavior from Task 2, route policy from Task 3.
- Produces: exact local installation commands, development/validation commands, public scope boundary, and an isolated Codex verification record.

- [ ] **Step 1: Write the failing documentation contract test**

Create `tests/docs-contract.test.js`:

```js
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const root = path.join(__dirname, '..');

test('README leads with the narrow positioning and exact install identity', () => {
  const readme = fs.readFileSync(path.join(root, 'README.md'), 'utf8');
  assert.match(readme, /Same workflow\. Right-sized subagents\./);
  assert.match(readme, /工作流不变，子代理刚刚好。/);
  assert.match(readme, /does not decide whether to use subagents/i);
  assert.match(readme, /codex plugin marketplace add \.\/|codex plugin marketplace add \$PWD/);
  assert.match(readme, /codex plugin add goldilocks@goldilocks-router/);
  assert.match(readme, /quick[\s\S]*explore[\s\S]*build[\s\S]*reason[\s\S]*deep/);
  assert.match(readme, /npm test/);
});

test('repository includes an MIT license for Goldilocks contributors', () => {
  const license = fs.readFileSync(path.join(root, 'LICENSE'), 'utf8');
  assert.match(license, /^MIT License/);
  assert.match(license, /Copyright \(c\) 2026 Goldilocks contributors/);
  assert.match(license, /THE SOFTWARE IS PROVIDED "AS IS"/);
});
```

- [ ] **Step 2: Run the documentation contract and confirm it fails**

Run:

```bash
node --test tests/docs-contract.test.js
```

Expected: FAIL with `ENOENT` for `README.md` or `LICENSE`.

- [ ] **Step 3: Write the concise user-facing README**

Create `README.md`:

````markdown
# Goldilocks

**Same workflow. Right-sized subagents.**

**工作流不变，子代理刚刚好。**

Goldilocks is a small Codex plugin that helps an agent choose a suitable child model and reasoning effort only after the active workflow has already decided to call `spawn_agent`.

It does not decide whether to use subagents. It does not add planning, TDD, debugging, review, verification, worktrees, or any other process. It does not change task content, decomposition, child count, context forking, permissions, or tools.

## How it works

`SessionStart` and `SubagentStart` hooks inject one compact policy from `skills/goldilocks/SKILL.md`. Before an already-planned spawn, the active agent preserves explicit choices, checks the current tool schema, classifies the child task, and changes only supported `model` and `reasoning_effort` fields.

| Route | Intended work | Default behavior |
|---|---|---|
| `quick` | Mechanical, local, low-risk | Prefer Luna/low; fall back to Terra/low |
| `explore` | Read-only search and synthesis | Prefer Luna/low; fall back to Terra/medium |
| `build` | Routine implementation using existing patterns | Inherit parent model/medium |
| `reason` | Debugging, review, security, difficult edge cases | Prefer Sol/high; otherwise inherit/high |
| `deep` | Architecture, migrations, concurrency, cross-module ambiguity | Prefer Sol/xhigh; otherwise inherit/xhigh |

Only values exposed by the current `spawn_agent` schema are used. Explicit user and workflow choices always win.

## Local installation

From this repository:

```bash
codex plugin marketplace add ./
codex plugin add goldilocks@goldilocks-router
```

Open `/hooks`, review and trust the Goldilocks hooks, then start a new Codex task.

## Development

```bash
npm test
python3 "$HOME/.codex/skills/.system/plugin-creator/scripts/validate_plugin.py" plugins/goldilocks
python3 "$HOME/.codex/skills/.system/skill-creator/scripts/quick_validate.py" plugins/goldilocks/skills/goldilocks
```

Runtime code uses only the Node.js standard library and does not access the network or write state.

## Name

Goldilocks means “not too much, not too little—just right.” This project is narrowly focused on subagent compute selection and is not affiliated with other projects using the Goldilocks name.

## License

MIT
````

- [ ] **Step 4: Add the MIT license**

Create `LICENSE`:

```text
MIT License

Copyright (c) 2026 Goldilocks contributors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

- [ ] **Step 5: Run all static tests and validators**

Run:

```bash
npm test
python3 "$HOME/.codex/skills/.system/plugin-creator/scripts/validate_plugin.py" plugins/goldilocks
python3 "$HOME/.codex/skills/.system/skill-creator/scripts/quick_validate.py" plugins/goldilocks/skills/goldilocks
git diff --check
```

Expected: all tests PASS; both validators exit `0`; `git diff --check` prints nothing.

- [ ] **Step 6: Install into an isolated Codex home**

Run:

```bash
REAL_CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
GOLDILOCKS_TEST_ROOT="$(mktemp -d)"
GOLDILOCKS_TEST_HOME="$GOLDILOCKS_TEST_ROOT/codex"
cleanup_goldilocks_test() {
  rm -rf "$GOLDILOCKS_TEST_ROOT"
}
trap cleanup_goldilocks_test EXIT
mkdir -p "$GOLDILOCKS_TEST_HOME"
test -f "$REAL_CODEX_HOME/auth.json"
cp "$REAL_CODEX_HOME/auth.json" "$GOLDILOCKS_TEST_HOME/auth.json"

CODEX_HOME="$GOLDILOCKS_TEST_HOME" codex plugin marketplace add "$PWD" --json
CODEX_HOME="$GOLDILOCKS_TEST_HOME" codex plugin add goldilocks@goldilocks-router
CODEX_HOME="$GOLDILOCKS_TEST_HOME" codex plugin list
```

Expected: marketplace add reports `goldilocks-router`; plugin add succeeds; plugin list includes enabled `goldilocks@goldilocks-router`. Do not print or inspect the copied `auth.json`.

- [ ] **Step 7: Verify Goldilocks does not create delegation**

Run:

```bash
CODEX_HOME="$GOLDILOCKS_TEST_HOME" codex exec \
  --json \
  --dangerously-bypass-hook-trust \
  -C "$PWD" \
  "Do not use subagents. Do not modify files. Reply with exactly NO_SPAWN." \
  > "$GOLDILOCKS_TEST_ROOT/no-spawn.jsonl"

rg -n 'NO_SPAWN' "$GOLDILOCKS_TEST_ROOT/no-spawn.jsonl"
if rg -q 'spawn_agent' "$GOLDILOCKS_TEST_ROOT/no-spawn.jsonl"; then
  echo 'unexpected spawn_agent call' >&2
  exit 1
fi
```

Expected: the JSONL contains `NO_SPAWN`; the guard exits `0` because no `spawn_agent` event exists.

- [ ] **Step 8: Verify one already-planned delegation is right-sized without restructuring**

Run:

```bash
CODEX_HOME="$GOLDILOCKS_TEST_HOME" codex exec \
  --json \
  --dangerously-bypass-hook-trust \
  -C "$PWD" \
  "Do not modify files. The workflow has already decided to use exactly one subagent. Spawn exactly one subagent to locate the Workflow Invariance Contract in docs/superpowers/specs/2026-07-18-goldilocks-design.md, wait for it, and summarize one sentence. Preserve the task, child count, task_name, message, fork_turns, permissions, and tools; Goldilocks may choose only model and reasoning_effort." \
  > "$GOLDILOCKS_TEST_ROOT/one-explore-spawn.jsonl"

rg -n 'spawn_agent|reasoning_effort|gpt-5\.6-(luna|terra|sol)' \
  "$GOLDILOCKS_TEST_ROOT/one-explore-spawn.jsonl"
```

Expected: exactly one child is spawned for the requested read-only task; the spawn remains semantically identical to the prompt and uses an allowed `explore` configuration—Luna/low when available, otherwise Terra/low-or-medium, otherwise inherited model with supported low-or-medium effort.

- [ ] **Step 9: Remove isolated credentials and commit documentation**

Run:

```bash
rm -rf "$GOLDILOCKS_TEST_ROOT"
trap - EXIT
git add README.md LICENSE tests/docs-contract.test.js
git commit -m "docs: document Goldilocks installation and boundaries"
git status --short --branch
```

Expected: temporary copied credentials are deleted; commit succeeds; working tree is clean.

## Final Verification Checklist

- [ ] `npm test` passes from the repository root.
- [ ] `validate_plugin.py plugins/goldilocks` exits `0`.
- [ ] `quick_validate.py plugins/goldilocks/skills/goldilocks` exits `0`.
- [ ] `git diff --check` prints nothing.
- [ ] The installed plugin is `goldilocks@goldilocks-router`, not the unrelated `goldilocks@goldilocks-local` project.
- [ ] Hook output uses `hookSpecificOutput.additionalContext` and never top-level `additionalContext`.
- [ ] A no-subagent prompt produces no `spawn_agent` event.
- [ ] An exactly-one-subagent prompt produces one semantically unchanged child task with only supported compute fields selected.
- [ ] The isolated `CODEX_HOME` and copied `auth.json` are deleted after smoke testing.
