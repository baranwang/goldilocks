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

function readJsonExamples(body) {
  return [...body.matchAll(/```json\n([\s\S]*?)\n```/g)].map((match) =>
    JSON.parse(match[1]),
  );
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

  for (const example of examples) {
    if (example.fork_turns !== 'all') continue;
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
