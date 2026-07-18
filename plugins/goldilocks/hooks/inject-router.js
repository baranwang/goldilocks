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
