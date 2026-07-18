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

function readFrontmatter(raw) {
  const lines = raw.split(/\r?\n/);
  assert.equal(lines[0], '---', 'expected opening YAML delimiter');
  const end = lines.indexOf('---', 1);
  assert.ok(end > 1, 'expected closing YAML delimiter');

  return Object.fromEntries(
    lines.slice(1, end).map((line) => {
      const separator = line.indexOf(':');
      assert.ok(separator > 0, `invalid frontmatter line: ${line}`);
      return [line.slice(0, separator), line.slice(separator + 1).trim()];
    }),
  );
}

function readSection(body, heading) {
  const start = body.indexOf(`${heading}\n`);
  assert.ok(start >= 0, `missing section: ${heading}`);
  const content = body.slice(start + heading.length + 1);
  const end = content.indexOf('\n## ');
  return (end >= 0 ? content.slice(0, end) : content).trim();
}

function readOrderedRoutes(body) {
  return readSection(body, '## Ordered routes')
    .split('\n')
    .filter((line) => /^\d+\. `/.test(line))
    .map((line) => {
      const opening = line.indexOf('`');
      const closing = line.indexOf('`', opening + 1);
      assert.ok(opening >= 0 && closing > opening, `invalid route line: ${line}`);
      return line.slice(opening + 1, closing);
    });
}

function readRouteTable(body) {
  return readSection(body, '## Route table')
    .split('\n')
    .filter((line) => line.startsWith('| `'))
    .map((line) => {
      const cells = line.slice(1, -1).split('|').map((cell) => cell.trim());
      assert.equal(cells.length, 4, `invalid route table row: ${line}`);
      return {
        route: cells[0].slice(1, -1),
        model: cells[2],
        effort: cells[3],
      };
    });
}

function readJsonExamples(body) {
  return [...body.matchAll(/```json\n([\s\S]*?)\n```/g)].map((match) =>
    JSON.parse(match[1]),
  );
}

function isFullHistory(example) {
  return example.fork_turns === undefined || example.fork_turns === 'all';
}

test('skill metadata describes routing without claiming workflow ownership', () => {
  const { raw } = readPolicy();
  const metadata = readFrontmatter(raw);
  assert.equal(metadata.name, 'goldilocks');
  assert.match(metadata.description, /only for subagents the active workflow has already decided to spawn/i);
  assert.match(metadata.description, /never changes workflow, delegation, task content, or subagent count/i);
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

test('policy preserves ordered classification', () => {
  const { body } = readPolicy();
  assert.deepEqual(readOrderedRoutes(body), ['explore', 'quick', 'build', 'reason', 'deep']);
});

test('policy requires reason for security and operational risk floors', () => {
  const { body } = readPolicy();
  const section = readSection(body, '## Ordered routes');
  assert.ok(section.includes('Security, authorization, privacy, money, or data-loss risk requires at least `reason`.'));
  assert.ok(section.includes('Concurrency, migrations, public API compatibility, and irreversible operations require at least `reason`.'));
});

test('policy sends new cross-module boundaries or abstractions to deep', () => {
  const { body } = readPolicy();
  const section = readSection(body, '## Ordered routes');
  assert.ok(section.includes('New cross-module boundaries or abstractions use `deep`.'));
});

test('policy contains exact per-row route model and effort choices', () => {
  const { body } = readPolicy();
  assert.deepEqual(readRouteTable(body), [
    {
      route: 'quick',
      model: 'Prefer `gpt-5.6-luna`; if Luna is not an allowed explicit override, use `gpt-5.6-terra`; otherwise inherit',
      effort: '`low`',
    },
    {
      route: 'explore',
      model: 'Prefer `gpt-5.6-luna`; if Luna is not allowed, use `gpt-5.6-terra`; otherwise inherit',
      effort: '`low`; with Terra, use `medium` only when broader synthesis is needed',
    },
    {
      route: 'build',
      model: 'Omit `model` and inherit the parent model',
      effort: '`medium`',
    },
    {
      route: 'reason',
      model: 'Prefer `gpt-5.6-sol`; if Sol is unavailable, inherit',
      effort: '`high`',
    },
    {
      route: 'deep',
      model: 'Prefer `gpt-5.6-sol`; if Sol is unavailable, inherit the strongest current configuration',
      effort: '`xhigh`',
    },
  ]);
});

test('policy keeps full-history forks and inherits incompatible compute fields', () => {
  const { body } = readPolicy();
  assert.match(body, /When `fork_turns` is omitted or `"all"`/);
  assert.match(body, /keep the original spawn and omit `model` and `reasoning_effort`/);
  assert.match(body, /Never change `fork_turns` to make routing possible/);
  assert.match(body, /This applies equally to `reason` and `deep`/);

  const examples = readJsonExamples(body);
  const fullHistory = examples.find((example) => example.fork_turns === 'all');
  assert.ok(fullHistory, 'expected a full-history spawn example');
  assert.equal(fullHistory.task_name, 'map_auth_flow');
  assert.ok(!Object.hasOwn(fullHistory, 'model'));
  assert.ok(!Object.hasOwn(fullHistory, 'reasoning_effort'));

  assert.equal(isFullHistory({}), true, 'omitted fork_turns is full-history');
  for (const example of examples) {
    if (!isFullHistory(example)) continue;
    assert.ok(!Object.hasOwn(example, 'model'));
    assert.ok(!Object.hasOwn(example, 'reasoning_effort'));
  }
});

test('policy stays within the context budget and contains concrete spawn examples', () => {
  const { body } = readPolicy();
  assert.ok(body.length < 9000, `policy is ${body.length} characters; expected < 9000`);
  assert.match(body, /"task_name": "fix_typo"/);
  assert.match(body, /"task_name": "implement_handler"/);
  assert.match(body, /"reasoning_effort": "medium"/);
  assert.match(body, /"reasoning_effort": "xhigh"/);
});
