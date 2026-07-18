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
