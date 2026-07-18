const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = path.join(__dirname, '..');
const script = path.join(root, 'plugins', 'goldilocks', 'hooks', 'inject-router.js');
const hooksPath = path.join(root, 'plugins', 'goldilocks', 'hooks', 'hooks.json');

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

function readHooks() {
  return JSON.parse(fs.readFileSync(hooksPath, 'utf8')).hooks;
}

function configuredCommand(eventName) {
  return readHooks()[eventName][0].hooks[0].command;
}

function runConfiguredHook(eventName, env = {}) {
  return spawnSync('/bin/sh', ['-c', configuredCommand(eventName)], {
    env: cleanEnv(env),
    encoding: 'utf8',
  });
}

function makePluginRoot(t, policyBody = 'POLICY BODY') {
  const pluginRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'goldilocks-hook-'));
  t.after(() => fs.rmSync(pluginRoot, { recursive: true, force: true }));
  const skillDir = path.join(pluginRoot, 'skills', 'goldilocks');
  fs.mkdirSync(skillDir, { recursive: true });
  const hookDir = path.join(pluginRoot, 'hooks');
  fs.mkdirSync(hookDir, { recursive: true });
  fs.copyFileSync(script, path.join(hookDir, 'inject-router.js'));
  fs.writeFileSync(
    path.join(skillDir, 'SKILL.md'),
    `\uFEFF---\nname: goldilocks\ndescription: test\n---\n\n${policyBody}\n`,
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

test('stripFrontmatter accepts empty valid LF and CRLF frontmatter', () => {
  const { stripFrontmatter } = require('../plugins/goldilocks/hooks/inject-router');
  assert.equal(stripFrontmatter('---\n---\nPolicy\n'), 'Policy');
  assert.equal(stripFrontmatter('\uFEFF---\r\n---\r\nPolicy\r\n'), 'Policy');
});

test('stripFrontmatter preserves plain policy text without frontmatter', () => {
  const { stripFrontmatter } = require('../plugins/goldilocks/hooks/inject-router');
  assert.equal(stripFrontmatter('\uFEFFPlain policy text\n'), 'Plain policy text');
});

test('stripFrontmatter rejects opened but unterminated or malformed frontmatter', () => {
  const { stripFrontmatter } = require('../plugins/goldilocks/hooks/inject-router');
  assert.equal(stripFrontmatter('---\nname: goldilocks\nPolicy body\n'), '');
  assert.equal(stripFrontmatter('\uFEFF---\r\nname: goldilocks\r\n--\r\nPolicy body\r\n'), '');
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

for (const eventName of ['SessionStart', 'SubagentStart']) {
  test(`${eventName} configured POSIX command prefers PLUGIN_ROOT`, (t) => {
    const pluginRoot = makePluginRoot(t, 'PRIMARY POLICY');
    const compatibilityRoot = makePluginRoot(t, 'FALLBACK POLICY');
    const result = runConfiguredHook(eventName, {
      PLUGIN_ROOT: pluginRoot,
      CLAUDE_PLUGIN_ROOT: compatibilityRoot,
    });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stderr, '');
    assert.equal(
      JSON.parse(result.stdout).hookSpecificOutput.additionalContext,
      'PRIMARY POLICY',
    );
  });

  test(`${eventName} configured POSIX command supports CLAUDE_PLUGIN_ROOT fallback`, (t) => {
    const pluginRoot = makePluginRoot(t);
    const result = runConfiguredHook(eventName, { CLAUDE_PLUGIN_ROOT: pluginRoot });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stderr, '');
    assert.deepEqual(JSON.parse(result.stdout), {
      hookSpecificOutput: {
        hookEventName: eventName,
        additionalContext: 'POLICY BODY',
      },
    });
  });

  test(`${eventName} configured POSIX command fails open silently`, (t) => {
    let result = runConfiguredHook(eventName);
    assert.equal(result.status, 0);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, '');

    const emptyRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'goldilocks-empty-'));
    t.after(() => fs.rmSync(emptyRoot, { recursive: true, force: true }));
    result = runConfiguredHook(eventName, { PLUGIN_ROOT: emptyRoot });
    assert.equal(result.status, 0);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, '');

    const pluginRoot = makePluginRoot(t);
    result = runConfiguredHook(eventName, { PLUGIN_ROOT: pluginRoot, PATH: '' });
    assert.equal(result.status, 0);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, '');

    const failingBin = fs.mkdtempSync(path.join(os.tmpdir(), 'goldilocks-bin-'));
    t.after(() => fs.rmSync(failingBin, { recursive: true, force: true }));
    const failingNode = path.join(failingBin, 'node');
    fs.writeFileSync(
      failingNode,
      '#!/bin/sh\nprintf leaked-stdout\nprintf leaked-stderr >&2\nexit 1\n',
      { mode: 0o755 },
    );
    result = runConfiguredHook(eventName, { PLUGIN_ROOT: pluginRoot, PATH: failingBin });
    assert.equal(result.status, 0);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, '');
  });
}

for (const eventName of ['SessionStart', 'SubagentStart']) {
  test(`${eventName} invalid skill frontmatter fails open silently`, (t) => {
    const pluginRoot = makePluginRoot(t);
    const skillPath = path.join(pluginRoot, 'skills', 'goldilocks', 'SKILL.md');
    fs.writeFileSync(skillPath, '---\nname: goldilocks\nPolicy body\n');

    const result = runHook(eventName, { PLUGIN_ROOT: pluginRoot });
    assert.equal(result.status, 0);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, '');
  });
}

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
  const hooks = JSON.parse(fs.readFileSync(hooksPath, 'utf8'));
  assert.deepEqual(Object.keys(hooks), ['hooks']);
  assert.deepEqual(Object.keys(hooks.hooks).sort(), ['SessionStart', 'SubagentStart']);
  assert.equal(hooks.hooks.SessionStart[0].matcher, 'startup|resume|clear|compact');
  assert.match(hooks.hooks.SessionStart[0].hooks[0].command, /inject-router\.js" SessionStart/);
  assert.match(hooks.hooks.SubagentStart[0].hooks[0].command, /inject-router\.js" SubagentStart/);
});

test('both hook wrappers support both roots and encode silent fail-open behavior', () => {
  const hooks = readHooks();

  for (const eventName of ['SessionStart', 'SubagentStart']) {
    const hook = hooks[eventName][0].hooks[0];
    assert.match(hook.command, /PLUGIN_ROOT/);
    assert.match(hook.command, /CLAUDE_PLUGIN_ROOT/);
    assert.match(hook.command, /command -v node/);
    assert.match(hook.command, /2>\/dev\/null/);
    assert.match(hook.command, /exit 0/);

    assert.match(hook.commandWindows, /PLUGIN_ROOT/);
    assert.match(hook.commandWindows, /CLAUDE_PLUGIN_ROOT/);
    assert.match(hook.commandWindows, /Get-Command node/);
    assert.match(hook.commandWindows, /Test-Path/);
    assert.match(hook.commandWindows, /try/);
    assert.match(hook.commandWindows, /catch/);
    assert.match(hook.commandWindows, /exit 0/);
  }
});
