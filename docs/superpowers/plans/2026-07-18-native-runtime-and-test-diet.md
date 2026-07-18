# Goldilocks Native Runtime and Test Diet Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove Goldilocks' user-facing Node.js runtime dependency, rename its npm and Codex marketplace identities, and keep only tests that protect real installed behavior.

**Architecture:** Codex continues to load one policy from `SKILL.md` at `SessionStart` and `SubagentStart`. macOS/Linux use a POSIX `sh` + `awk` hook; Windows uses PowerShell. Node.js remains optional contributor tooling for one compact cross-platform runtime test file and is not required to use the plugin.

**Tech Stack:** Codex lifecycle hooks, POSIX `sh`/`awk`, Windows PowerShell, Node's built-in test runner for contributor tests, Codex plugin and skill validators, isolated `CODEX_HOME` smoke tests.

## Global Constraints

- npm package name: `@baranwang/goldilocks`; keep `private: true`.
- Marketplace name: `goldilocks`; Codex install identity: `goldilocks@goldilocks`.
- Plugin slug remains `goldilocks`.
- Runtime uses only Codex-native `PLUGIN_ROOT`; do not use or mention `CLAUDE_PLUGIN_ROOT` in runtime files.
- Users must not need Node.js, Python, `jq`, or a compiled sidecar.
- macOS/Linux runtime may use only `/bin/sh` and POSIX `awk`; Windows runtime may use Windows PowerShell.
- Preserve `SessionStart` and `SubagentStart`, the matcher, timeout, status message, output envelope, workflow-invariance policy, and fail-open behavior.
- Runtime performs no network access and writes no state.
- Delete README/license/package/policy wording contract tests; retain only compact runtime/integration tests plus official validators and isolated smoke.

---

## Planned File Map

- Create: `plugins/goldilocks/hooks/inject-router.sh` — POSIX policy loader and JSON emitter.
- Create: `plugins/goldilocks/hooks/inject-router.ps1` — Windows policy loader and JSON emitter.
- Modify: `plugins/goldilocks/hooks/hooks.json` — dispatch to the native script for each platform.
- Delete: `plugins/goldilocks/hooks/inject-router.js` — removes Node from plugin runtime.
- Rewrite: `tests/inject-router.test.js` — one compact native-runtime test surface.
- Delete: `tests/package-contract.test.js`.
- Delete: `tests/policy-contract.test.js`.
- Delete: `tests/docs-contract.test.js`.
- Modify: `.agents/plugins/marketplace.json` — marketplace name/display name.
- Modify: `package.json` — scoped npm package name.
- Modify: `README.md` — new install identity and runtime requirements.

### Task 1: Replace the Node Hook with Native Platform Scripts

**Files:**
- Create: `plugins/goldilocks/hooks/inject-router.sh`
- Create: `plugins/goldilocks/hooks/inject-router.ps1`
- Modify: `plugins/goldilocks/hooks/hooks.json`
- Delete: `plugins/goldilocks/hooks/inject-router.js`
- Rewrite: `tests/inject-router.test.js`

**Interfaces:**
- Consumes: `PLUGIN_ROOT`, event argument `SessionStart` or `SubagentStart`, and `skills/goldilocks/SKILL.md`.
- Produces: one JSON object with `hookSpecificOutput.hookEventName` and `hookSpecificOutput.additionalContext`, or silent exit `0`.

- [ ] **Step 1: Replace the old test file with a failing native-runtime test**

Use `apply_patch` to replace `tests/inject-router.test.js` with:

```js
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = path.join(__dirname, '..');
const pluginRoot = path.join(root, 'plugins', 'goldilocks');
const hooksPath = path.join(pluginRoot, 'hooks', 'hooks.json');
const shScript = path.join(pluginRoot, 'hooks', 'inject-router.sh');
const psScript = path.join(pluginRoot, 'hooks', 'inject-router.ps1');

function cleanEnv(extra = {}) {
  const env = { ...process.env };
  delete env.PLUGIN_ROOT;
  return { ...env, ...extra };
}

function readHooks() {
  return JSON.parse(fs.readFileSync(hooksPath, 'utf8')).hooks;
}

function runNative(eventName, env = {}) {
  const command = process.platform === 'win32' ? 'powershell.exe' : '/bin/sh';
  const args = process.platform === 'win32'
    ? ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', psScript, eventName]
    : [shScript, eventName];
  return spawnSync(command, args, { env: cleanEnv(env), encoding: 'utf8' });
}

function runConfigured(eventName, env = {}) {
  const hook = readHooks()[eventName][0].hooks[0];
  const command = process.platform === 'win32' ? 'powershell.exe' : '/bin/sh';
  const args = process.platform === 'win32'
    ? ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command', hook.commandWindows]
    : ['-c', hook.command];
  return spawnSync(command, args, { env: cleanEnv(env), encoding: 'utf8' });
}

function makePluginRoot(t, skillText) {
  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'goldilocks-native-'));
  t.after(() => fs.rmSync(tempRoot, { recursive: true, force: true }));
  fs.mkdirSync(path.join(tempRoot, 'hooks'), { recursive: true });
  fs.mkdirSync(path.join(tempRoot, 'skills', 'goldilocks'), { recursive: true });
  fs.copyFileSync(shScript, path.join(tempRoot, 'hooks', 'inject-router.sh'));
  fs.copyFileSync(psScript, path.join(tempRoot, 'hooks', 'inject-router.ps1'));
  fs.writeFileSync(path.join(tempRoot, 'skills', 'goldilocks', 'SKILL.md'), skillText);
  return tempRoot;
}

test('native scripts inject valid policy and reject malformed frontmatter', (t) => {
  const tempRoot = makePluginRoot(
    t,
    '\uFEFF---\r\nname: goldilocks\r\n---\r\n\r\nPOLICY BODY\r\n',
  );

  for (const eventName of ['SessionStart', 'SubagentStart']) {
    let result = runNative(eventName, { PLUGIN_ROOT: tempRoot });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(JSON.parse(result.stdout), {
      hookSpecificOutput: {
        hookEventName: eventName,
        additionalContext: 'POLICY BODY',
      },
    });

    fs.writeFileSync(
      path.join(tempRoot, 'skills', 'goldilocks', 'SKILL.md'),
      '---\nname: goldilocks\nBROKEN\n',
    );
    result = runNative(eventName, { PLUGIN_ROOT: tempRoot });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, '');
  }
});

test('configured hooks run both events and fail open silently', (t) => {
  const tempRoot = makePluginRoot(t, '---\n---\nPOLICY BODY\n');

  for (const eventName of ['SessionStart', 'SubagentStart']) {
    let result = runConfigured(eventName, { PLUGIN_ROOT: tempRoot });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(JSON.parse(result.stdout).hookSpecificOutput.hookEventName, eventName);

    result = runConfigured(eventName);
    assert.equal(result.status, 0);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, '');

    const emptyRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'goldilocks-empty-'));
    t.after(() => fs.rmSync(emptyRoot, { recursive: true, force: true }));
    result = runConfigured(eventName, { PLUGIN_ROOT: emptyRoot });
    assert.equal(result.status, 0);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, '');
  }
});

test('real policy is injected identically within the context budget', () => {
  const contexts = [];
  for (const eventName of ['SessionStart', 'SubagentStart']) {
    const result = runNative(eventName, { PLUGIN_ROOT: pluginRoot });
    assert.equal(result.status, 0, result.stderr);
    const output = JSON.parse(result.stdout);
    assert.equal(output.hookSpecificOutput.hookEventName, eventName);
    contexts.push(output.hookSpecificOutput.additionalContext);
  }
  assert.equal(contexts[0], contexts[1]);
  assert.ok(contexts[0].length < 9000);
  assert.match(contexts[0], /Only after the active workflow has already decided to call `spawn_agent`/);
});

test('hook config uses native Codex-only runtime files', () => {
  const hooks = readHooks();
  assert.deepEqual(Object.keys(hooks).sort(), ['SessionStart', 'SubagentStart']);
  assert.equal(hooks.SessionStart[0].matcher, 'startup|resume|clear|compact');

  for (const eventName of ['SessionStart', 'SubagentStart']) {
    const hook = hooks[eventName][0].hooks[0];
    assert.match(hook.command, /PLUGIN_ROOT/);
    assert.match(hook.command, /inject-router\.sh/);
    assert.doesNotMatch(hook.command, /node|python/i);
    assert.match(hook.commandWindows, /PLUGIN_ROOT/);
    assert.match(hook.commandWindows, /inject-router\.ps1/);
    assert.doesNotMatch(hook.commandWindows, /node|python/i);
  }

  assert.equal(fs.existsSync(shScript), true);
  assert.equal(fs.existsSync(psScript), true);
});
```

- [ ] **Step 2: Run the focused test and confirm RED**

Run:

```bash
rtk node --test tests/inject-router.test.js
```

Expected: FAIL because `inject-router.sh` and `inject-router.ps1` do not exist and `hooks.json` still references Node.

- [ ] **Step 3: Create the POSIX runtime**

Create `plugins/goldilocks/hooks/inject-router.sh`:

```sh
#!/bin/sh

event=${1-}
case "$event" in
  SessionStart|SubagentStart) ;;
  *) exit 0 ;;
esac

root=${PLUGIN_ROOT-}
[ -n "$root" ] || exit 0
skill=$root/skills/goldilocks/SKILL.md
[ -r "$skill" ] || exit 0

LC_ALL=C awk -v event="$event" '
function escape_json(value, output, index, char) {
  output = ""
  for (index = 1; index <= length(value); index++) {
    char = substr(value, index, 1)
    if (char == "\\") output = output "\\\\"
    else if (char == "\"") output = output "\\\""
    else if (char == "\t") output = output "\\t"
    else output = output char
  }
  return output
}
{
  sub(/\r$/, "", $0)
}
NR == 1 {
  sub(/^\357\273\277/, "", $0)
}
NR == 1 && $0 == "---" {
  opened = 1
  in_frontmatter = 1
  next
}
in_frontmatter && $0 == "---" {
  closed = 1
  in_frontmatter = 0
  next
}
in_frontmatter {
  next
}
{
  lines[++count] = $0
}
END {
  if (opened && !closed) exit 0

  start = 1
  while (start <= count && lines[start] ~ /^[[:space:]]*$/) start++
  end = count
  while (end >= start && lines[end] ~ /^[[:space:]]*$/) end--
  if (end < start) exit 0

  sub(/^[[:space:]]+/, "", lines[start])
  sub(/[[:space:]]+$/, "", lines[end])

  body = ""
  for (index = start; index <= end; index++) {
    if (index > start) body = body "\\n"
    body = body escape_json(lines[index])
  }

  printf "{\"hookSpecificOutput\":{\"hookEventName\":\"%s\",\"additionalContext\":\"%s\"}}", escape_json(event), body
}
' "$skill" 2>/dev/null || exit 0

exit 0
```

- [ ] **Step 4: Create the Windows runtime**

Create `plugins/goldilocks/hooks/inject-router.ps1`:

```powershell
param([string]$EventName)

$ErrorActionPreference = 'Stop'

try {
  if ($EventName -notin @('SessionStart', 'SubagentStart')) { exit 0 }

  $root = $env:PLUGIN_ROOT
  if ([string]::IsNullOrWhiteSpace($root)) { exit 0 }

  $skillPath = Join-Path $root 'skills\goldilocks\SKILL.md'
  if (-not (Test-Path -LiteralPath $skillPath -PathType Leaf)) { exit 0 }

  $raw = [IO.File]::ReadAllText($skillPath)
  if ($raw.Length -gt 0 -and $raw[0] -eq [char]0xFEFF) {
    $raw = $raw.Substring(1)
  }

  $lines = [regex]::Split($raw, "\r?\n")
  $bodyLines = $lines

  if ($lines.Count -gt 0 -and $lines[0] -eq '---') {
    $closing = -1
    for ($index = 1; $index -lt $lines.Count; $index++) {
      if ($lines[$index] -eq '---') {
        $closing = $index
        break
      }
    }
    if ($closing -lt 0) { exit 0 }
    if ($closing + 1 -ge $lines.Count) { exit 0 }
    $bodyLines = $lines[($closing + 1)..($lines.Count - 1)]
  }

  $body = ($bodyLines -join "`n").Trim()
  if ([string]::IsNullOrWhiteSpace($body)) { exit 0 }

  $payload = @{
    hookSpecificOutput = @{
      hookEventName = $EventName
      additionalContext = $body
    }
  }
  [Console]::Out.Write(($payload | ConvertTo-Json -Compress -Depth 3))
} catch {}

exit 0
```

- [ ] **Step 5: Point hooks.json at the native scripts**

Keep the existing events, matcher, timeouts, and status messages. Replace each POSIX command with the event-specific form:

```sh
root="${PLUGIN_ROOT:-}"; if [ -n "$root" ] && [ -r "$root/hooks/inject-router.sh" ]; then output=$(/bin/sh "$root/hooks/inject-router.sh" SessionStart 2>/dev/null) && [ -n "$output" ] && printf '%s' "$output"; fi; exit 0
```

Use `SubagentStart` for the second event.

Replace each `commandWindows` with the event-specific form:

```powershell
$root=$env:PLUGIN_ROOT; if ($root) { $script=Join-Path $root 'hooks\inject-router.ps1'; $powershell=Get-Command powershell.exe -ErrorAction SilentlyContinue; if ($powershell -and (Test-Path -LiteralPath $script -PathType Leaf)) { try { $output=& $powershell.Source -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $script SessionStart 2>$null; if ($LASTEXITCODE -eq 0 -and $output) { [Console]::Out.Write(($output -join [Environment]::NewLine)) } } catch {} } }; exit 0
```

Use `SubagentStart` for the second event.

- [ ] **Step 6: Remove the Node runtime and verify GREEN**

Delete `plugins/goldilocks/hooks/inject-router.js`, then run:

```bash
rtk node --test tests/inject-router.test.js
rtk python3 "$HOME/.codex/skills/.system/plugin-creator/scripts/validate_plugin.py" plugins/goldilocks
rtk python3 "$HOME/.codex/skills/.system/skill-creator/scripts/quick_validate.py" plugins/goldilocks/skills/goldilocks
rtk git diff --check
```

Expected: native runtime tests PASS, both validators exit `0`, and diff check is clean.

- [ ] **Step 7: Commit the native runtime**

```bash
rtk git add plugins/goldilocks/hooks tests/inject-router.test.js
rtk git commit -m "feat: replace Node hook with native scripts"
```

### Task 2: Rename Identities, Remove Low-Value Tests, and Re-Smoke

**Files:**
- Modify: `.agents/plugins/marketplace.json`
- Modify: `package.json`
- Modify: `README.md`
- Delete: `tests/package-contract.test.js`
- Delete: `tests/policy-contract.test.js`
- Delete: `tests/docs-contract.test.js`

**Interfaces:**
- Consumes: native runtime from Task 1 and the existing plugin/skill manifests.
- Produces: npm identity `@baranwang/goldilocks`, marketplace `goldilocks`, install identity `goldilocks@goldilocks`, and a minimal validation workflow.

- [ ] **Step 1: Apply the exact identity changes**

Set `.agents/plugins/marketplace.json` top-level metadata to:

```json
{
  "name": "goldilocks",
  "interface": {
    "displayName": "Goldilocks"
  }
}
```

Preserve the existing single plugin entry unchanged.

Set `package.json`:

```json
{
  "name": "@baranwang/goldilocks",
  "version": "0.1.0",
  "private": true,
  "description": "Same workflow. Right-sized Codex subagents.",
  "license": "MIT",
  "scripts": {
    "test": "node --test tests/*.test.js"
  }
}
```

- [ ] **Step 2: Remove low-value contract tests**

Delete:

```text
tests/package-contract.test.js
tests/policy-contract.test.js
tests/docs-contract.test.js
```

Do not replace them with new static wording tests.

- [ ] **Step 3: Update README identity and runtime documentation**

Make these exact changes:

```markdown
codex plugin marketplace add ./
codex plugin add goldilocks@goldilocks
```

State that installed runtime uses POSIX `sh`/`awk` on macOS/Linux and PowerShell on Windows. State that users do not need Node.js or Python; Node.js is only needed by contributors who run `npm test`.

Remove any claim that the runtime is implemented with the Node.js standard library. Keep the workflow boundary, five route summary, hook trust step, validators, and MIT license unchanged.

- [ ] **Step 4: Run the reduced static verification**

Run:

```bash
rtk npm test
rtk python3 "$HOME/.codex/skills/.system/plugin-creator/scripts/validate_plugin.py" plugins/goldilocks
rtk python3 "$HOME/.codex/skills/.system/skill-creator/scripts/quick_validate.py" plugins/goldilocks/skills/goldilocks
rtk rg -n "goldilocks-router|goldilocks@goldilocks-router|inject-router\.js|CLAUDE_PLUGIN_ROOT" .agents package.json README.md plugins tests
rtk git diff --check
```

Expected: four runtime tests PASS; both validators exit `0`; the forbidden-term search has no matches; diff check is clean.

- [ ] **Step 5: Install and verify in an isolated Codex home**

Run one guarded shell block so credentials are always deleted:

```bash
rtk zsh -lc '
set -eu
REAL_CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
TEST_ROOT="$(mktemp -d)"
TEST_HOME="$TEST_ROOT/codex"
cleanup() { rm -rf "$TEST_ROOT"; }
trap cleanup EXIT
mkdir -p "$TEST_HOME"
test -f "$REAL_CODEX_HOME/auth.json"
cp "$REAL_CODEX_HOME/auth.json" "$TEST_HOME/auth.json"

CODEX_HOME="$TEST_HOME" codex plugin marketplace add "$PWD" --json
CODEX_HOME="$TEST_HOME" codex plugin add goldilocks@goldilocks --json
CODEX_HOME="$TEST_HOME" codex plugin list --json > "$TEST_ROOT/plugins.json"

CODEX_HOME="$TEST_HOME" codex exec --json --dangerously-bypass-hook-trust -C "$PWD" \
  "Do not use subagents. Do not modify files. Reply with exactly NO_SPAWN." \
  > "$TEST_ROOT/no-spawn.jsonl"

CODEX_HOME="$TEST_HOME" codex exec --json --dangerously-bypass-hook-trust -C "$PWD" \
  "Do not modify files. The workflow has already decided to use exactly one subagent. Spawn exactly one subagent to locate the Workflow Invariance Contract in docs/superpowers/specs/2026-07-18-goldilocks-design.md, wait for it, and summarize one sentence." \
  > "$TEST_ROOT/one-spawn.jsonl"

node - "$TEST_ROOT/plugins.json" "$TEST_ROOT/no-spawn.jsonl" "$TEST_ROOT/one-spawn.jsonl" <<"NODE"
const fs = require("node:fs");
const [pluginsPath, noSpawnPath, oneSpawnPath] = process.argv.slice(2);
const readEvents = (file) => fs.readFileSync(file, "utf8").trim().split("\n").filter(Boolean).map(JSON.parse);
const completedSpawns = (events) => events.filter((event) =>
  event.type === "item.completed" &&
  event.item?.type === "collab_tool_call" &&
  event.item?.tool === "spawn_agent"
);
const plugins = JSON.parse(fs.readFileSync(pluginsPath, "utf8"));
const installed = plugins.installed?.find((plugin) => plugin.pluginId === "goldilocks@goldilocks");
if (!installed?.installed || !installed?.enabled) process.exit(1);
const noSpawn = readEvents(noSpawnPath);
const oneSpawn = readEvents(oneSpawnPath);
if (!noSpawn.some((event) => event.item?.type === "agent_message" && event.item?.text === "NO_SPAWN")) process.exit(1);
if (completedSpawns(noSpawn).length !== 0) process.exit(1);
const spawns = completedSpawns(oneSpawn);
if (spawns.length !== 1) process.exit(1);
NODE
'
```

Expected: marketplace name is `goldilocks`; installed/enabled identity is `goldilocks@goldilocks`; no-spawn count is `0`; one-spawn completed count is `1`; the temporary home and copied auth are deleted without being read or printed. Current Codex CLI JSONL does not expose a receiver field on completed `spawn_agent` calls.

- [ ] **Step 6: Commit identity and test diet**

```bash
rtk git add .agents/plugins/marketplace.json package.json README.md tests docs/superpowers/plans/2026-07-18-native-runtime-and-test-diet.md
rtk git commit -m "chore: simplify Goldilocks packaging and tests"
rtk git status --short --branch
```

Expected: commit succeeds and working tree is clean.

## Final Verification Checklist

- [ ] The installed plugin identity is `goldilocks@goldilocks`.
- [ ] `package.json.name` is `@baranwang/goldilocks` and `private` remains true.
- [ ] Runtime hook files contain no Node.js, Python, or Claude compatibility dependency.
- [ ] macOS/Linux use the POSIX script; Windows uses the PowerShell script.
- [ ] Only `tests/inject-router.test.js` remains.
- [ ] `npm test` passes the compact runtime suite.
- [ ] Plugin and skill validators pass.
- [ ] Isolated no-spawn and one-spawn smoke checks pass.
- [ ] Temporary credentials and JSONL files are deleted.
