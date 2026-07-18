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
  assert.match(readme, /explore[^\n]*Prefer Luna\/low; fall back to Terra\/low, or Terra\/medium only for broader synthesis/i);
  assert.match(readme, /npm test/);
});

test('repository includes an MIT license for Goldilocks contributors', () => {
  const license = fs.readFileSync(path.join(root, 'LICENSE'), 'utf8');
  assert.match(license, /^MIT License/);
  assert.match(license, /Copyright \(c\) 2026 Goldilocks contributors/);
  assert.match(license, /THE SOFTWARE IS PROVIDED "AS IS"/);
});
