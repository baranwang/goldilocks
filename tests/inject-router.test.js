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
