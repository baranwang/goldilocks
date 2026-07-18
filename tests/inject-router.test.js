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
    fs.writeFileSync(
      path.join(tempRoot, 'skills', 'goldilocks', 'SKILL.md'),
      '\uFEFF---\r\nname: goldilocks\r\n---\r\n\r\nPOLICY BODY\r\n',
    );
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
