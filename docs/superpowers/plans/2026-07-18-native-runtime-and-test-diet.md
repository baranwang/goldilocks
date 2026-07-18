# Goldilocks Native Runtime and Test Diet — Final State

## Goal

Goldilocks uses native platform hooks with no package manifest or local test
harness. The repository is validated by the official Codex validators and an
isolated package-less installation check.

## Architecture

Codex loads the policy in `SKILL.md` at `SessionStart` and `SubagentStart`.
macOS and Linux use POSIX `sh` with `awk`; Windows uses PowerShell. The runtime
uses only Codex-native `PLUGIN_ROOT`, makes no network requests, and writes no
state.

## Constraints

- Marketplace name: `goldilocks`.
- Codex installation identity: `goldilocks@goldilocks`.
- The plugin slug remains `goldilocks`.
- Keep the existing event matcher, timeout, status message, output envelope,
  workflow-invariance policy, and fail-open behavior.
- Do not add a package manifest, local test harness, test script, CI,
  dependency, or replacement test system.
- Runtime requires neither Node.js nor Python. Python is used only for the
  official validators.

## Final File Shape

- `plugins/goldilocks/hooks/inject-router.sh` is the POSIX policy loader.
- `plugins/goldilocks/hooks/inject-router.ps1` is the Windows policy loader.
- `plugins/goldilocks/hooks/hooks.json` dispatches both lifecycle events.
- `package.json` and the `tests/` directory do not exist.

## Completed History

Native hooks replaced the former JavaScript runtime in commit `8443a1d`.
The final package manifest and local test harness were removed in
`11bd78e`. The isolated installation verification was narrowed to marketplace
add, plugin add, and enabled-status checks in `f661606`.

## Current Validation

- Run the official plugin validator for `plugins/goldilocks`.
- Run the official skill validator for `plugins/goldilocks/skills/goldilocks`.
- Run `git diff --check`.
- In a fresh temporary `CODEX_HOME`, copy `auth.json` without reading or
  printing it; add this repository as a marketplace; install
  `goldilocks@goldilocks`; and confirm the entry is enabled.
- Use an unconditional cleanup trap for the temporary home and copied
  credentials. No Codex spawn session is required.
