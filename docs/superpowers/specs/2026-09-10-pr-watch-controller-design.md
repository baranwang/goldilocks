# Evidence-driven PR watcher controller

Date: 2026-09-10

Status: specification requested after design discussion and Desktop capability probes; implementation has not started.

This supersedes the lifecycle registration, checkpoint, and handoff portions of
[the September 9 design](2026-09-09-pr-watch-go-hooks-design.md). It retains the
existing GitHub collection, pending-event, notification, and quiet-window semantics.
Implementation plan: [PR watcher controller](../plans/2026-09-10-pr-watch-controller.md).

## 1. Problem and outcome

The reported watcher read its reference, ran version/help/status commands, and
finished without registering or polling. The parent nevertheless reported that
monitoring had started. An isolated initial collection found all 16 unresolved
review threads and prepared three notification parts. Collection was functioning;
the failure was the orchestration contract.

Today `cmd/goldilocks/watcher.go` protects only registrations created by the child.
`Checkpoint` increments progress on request, and `Finish` accepts an
`--execution-ended` assertion. The parent skill forbids the parent from checking
state. Consequently, an unregistered child falls outside both the startup check
and the stop hook. Rephrasing the handoff alone leaves those failure modes intact.

The required outcome is a persistent, inspectable controller that owns the next
action and requires evidence for startup, delivery, and termination. A child that
does nothing leaves an incomplete startup record; it never produces a successful
monitoring status.

## 2. Global constraints

- Build with Go 1.25.6; retain `go 1.25.0` and `toolchain go1.25.6` in go.mod.
- Add no third-party Go dependencies and no installed-user Node, Python, or Go requirement.
- Support managed PR monitoring on macOS and Linux only; keep Windows routing hooks and launchers functional.
- Retain the shell/PowerShell lazy launchers, checksum verification, and GoReleaser v2.18.1 packaging.
- Keep Go tests beside their packages as `*_test.go`; do not introduce `__tests__` directories.
- Default to a 60-second polling interval and a 30-second observed quiet window; both accept explicit positive overrides.
- Use at most two automatic stop-hook continuations without measurable progress; never reset this budget on a status read or agent assertion.
- Store directories with mode 0700 and state/ticket files with mode 0600 on POSIX systems.
- Never use transcript parsing, spawn ordering, or child-authored success text as lifecycle authority.
- Do not install a daemon, create an additional sidebar task, or create a recurring automation as part of this change.
- Monitoring does not authorize GitHub comments, review resolution, code pushes, or merges.
- Every implementation commit must include `Co-authored-by: Codex <noreply@openai.com>`.

## 3. Verified capability envelope

The user supplied controlled experiment `E9C9F86B-41BB-4C4E-BCB2-0C74C2350820`
on Desktop 26.903.61454, bundle build 8378, bundled CLI 0.153.4, macOS,
multi-agent v2. This repository contains the reported findings, not an independent
verification of the other computer's raw files.

| Capability | Evidence and design consequence |
| --- | --- |
| SubagentStart | Actual payload contained parent `session_id`, child UUID `agent_id`, and `turn_id` before the child's first observed tool result. Use it to attest parent/child membership. |
| Spawn correlation | `spawn_agent` returned only `task_name`; the start event contained no common call ID or handle. A startup ticket is required to associate a watch with a particular child. |
| App delivery | Actual `mcp__codex_app__send_message_to_thread` PostToolUse contained `agent_id`, `tool_use_id`, `tool_input.threadId`, exact prompt, and raw MCP result with `isError:false`. Parent received the matching marker from that UUID. |
| Stop recovery | One real block produced another final answer in the same UUID and turn; `stop_hook_active` changed from false to true. This establishes one continuation, not an unlimited service. |
| Isolation | An unrelated child received no continuation. |
| Loading | Trusting newly added hooks did not activate them in the running App; a full restart did in this experiment. Installation requires observed hook execution, not just files and trust hashes. |
| Unverified | Idle parent wakeup, shutdown/sleep recovery, and repeated continuation of the new controller remain release gates. |

The earlier [runtime record](../../verification/pr-watch-runtime.md) reports two
successive continuations in fresh ephemeral sessions using the Desktop binary.
Retain that narrower evidence; repeat the test with the installed candidate in
the real Desktop instead of treating it as validation of the new controller.

## 4. Architecture and ownership

This is one subsystem and one implementation plan: the state machine, hook
adapter, and instructions share one protocol and must ship together.

```mermaid
sequenceDiagram
    participant P as Parent task
    participant G as Go controller
    participant C as Watcher child
    participant H as Codex hooks
    participant A as App message tool
    P->>G: start(PR), persist starting intent
    G-->>P: watch ID and ticket path
    P->>C: spawn with explicit execute instruction and ticket
    H->>G: SubagentStart(parent, child UUID)
    C->>G: advance(ticket, runtime child UUID)
    G->>G: bind, collect, freeze first event
    G-->>C: send exact part to parent
    C->>A: send_message_to_thread
    H->>G: PostToolUse exact send and accepted result
    C->>G: advance(ticket)
    G->>G: commit fully accepted event; enter next poll
    P->>G: status; require ready + live execution
    H->>G: on premature stop, choose recovery or fault
```

- Go owns polling, observed quiet time, state transitions, prepared messages,
  acceptance receipts, continuation budgets, and failure diagnoses.
- The child is an executor: run `advance`, wait on its real execution handle,
  send the returned exact prompt to its returned destination, repeat.
- Hooks normalize real runtime events, write evidence, and request bounded
  recovery. They perform no GitHub reads, App sends, or nested agent creation.
- The parent creates the intent, checks startup, records reception, and handles
  review findings under the user's existing authorization. It does not poll GitHub.

### 4.1 Stable identity and locations

`PR.Key` remains the existing canonical-URL hash used in notification footers.
`watch_id` is a new random UUID for a monitoring generation, not a replacement
for PR identity. A watch belongs to exactly one parent and one canonical PR.

The controller root is `$CODEX_HOME/goldilocks/pr-watch`, falling back to
`~/.codex/goldilocks/pr-watch`. Resolve it to an absolute path once. Hook and CLI
use the same resolver; do not derive it from a child's possibly changed cwd.
Tests inject a root into the Go controller constructor instead of changing HOME.

```text
ROOT/parents/PARENT_UUID/PR_KEY.json            # State v4 with Control
ROOT/parents/PARENT_UUID/PR_KEY.lock            # exclusive poll execution lock
ROOT/parents/PARENT_UUID/PR_KEY.state.lock      # short transaction lock
ROOT/parents/PARENT_UUID/PR_KEY.stop            # durable user stop request
ROOT/parents/PARENT_UUID/PR_KEY.yield           # end only the current execution
ROOT/parents/PARENT_UUID/PR_KEY.ticket          # startup credential, 0600
ROOT/parents/PARENT_UUID/starts/CHILD_UUID.json  # observed start membership
```

Different parents monitoring the same PR have independent delivery baselines.
Repeated start for the same parent and PR returns the existing watch. A finished
generation is archived, including its reception history, before an explicit
`--reopened` start creates a new generation. Never infer reopen from green CI.

The parent runtime UUID comes from `CODEX_THREAD_ID`, not a CLI override. Child
identity also comes from its own runtime environment. A ticket contains parent,
canonical PR, watch ID, and a 32-byte random nonce. Only its SHA-256 digest lives
in Control. The ticket is not printed in messages or diagnostics. Ticket paths
are resolved and constrained to the controller root; symlink escapes are rejected.
These checks prevent accidental cross-watch association; a shared filesystem and
shell are not a sandbox against a deliberately malicious local agent.

SubagentStart records membership only while that parent has an unbound starting
intent. It does not assign the child to any watch. Binding requires both the
ticket and a matching, observed parent/child membership. Two concurrent spawns
may arrive in either order without affecting binding. A ticket can bind once;
repeat calls by the same child are idempotent, and a different child is rejected.
A per-parent `bindings.lock` serializes child assignment: one child UUID cannot
bind to two active watches. Acquire it before the per-watch state lock and never
in the reverse order; atomic state files provide the ownership lookup.
Membership must be newer than the intent; remove unused records older than 24
hours during start/status operations. A starting intent older than 10 minutes
becomes `needs_attention` on the next controller or relevant hook invocation.
No background timer is implied. Bound tickets remain usable by the bound child.

### 4.2 Managed state and transactions

Add State version 4 with a required, non-null `control` object for managed
watches. Preserve State v2/v3 for the standalone `pr-watch` helper. Control holds:

- identity, parent UUID, bound child UUID, original cwd, ticket digest, generation;
- stage, effective interval/quiet values, initial successful event ID and readiness;
- worker execution ID, heartbeat timestamp, and optional observed host handle;
- persisted quiet deadline and read-failure counters needed for resumption;
- outbox event identity, exact prepared prompts, part hashes, accepted call IDs;
- parent reception records keyed by watch/event/part, with no assertion of business processing;
- progress sequence, stop-continuation budget, failure code/detail, and stop-notice state.

Use one atomic state replacement for the business snapshot and its control
transitions. Do not maintain a second registration database that can disagree
with the delivery baseline. Existing `Store.Observe`, `Freeze`, `Prepare`, and
`Ack` remain the domain operations; transaction mode defers their intermediate
Save calls until the outer transaction commits once.

The existing `.lock` remains the exclusive worker lock, also excluding old CLI
mutators. Managed mutations use `.state.lock`, reread and validate the latest
state, apply one transaction, fsync the temporary file, rename, and fsync the
parent directory on supported POSIX systems. Hook writes must not wait on the
long-lived worker lock. Never hold the state lock during network calls, sleeping,
App tool calls, or another hook. No stale mkdir lock protocol is retained.

Read-only status does not rewrite v2/v3 files. Unknown versions, corrupt JSON,
invalid UUIDs, mismatched PR identities, negative durations, malformed receipts,
and impossible Control/business combinations fail without rewriting the file.
The public standalone `pr-watch` CLI rejects v4 mutations with an instruction to
use `watcher`; its status command may inspect v4. Managed stop uses its own path.

### 4.3 State transitions

| Stage | Entry evidence | Permitted next work |
| --- | --- | --- |
| `starting` | Parent intent durably created | Wait for membership and binding; parent may diagnose failure |
| `initializing` | Ticket bound to observed child | Initial read, error handling, initial multipart delivery |
| `running` | Initial successful event fully App-accepted and a later poll execution has entered | Poll, collect, deliver later events |
| `stopping` | Explicit parent stop request persisted | Finish pending delivery, stage stopped without new reads, clean up |
| `needs_attention` | Expired startup, missing receipts, corrupt state, stalled execution, or exhausted recovery | Expose diagnosis; preserve evidence; explicit resume only |
| `finished` | Terminal event fully accepted, pending/collecting empty, original worker released | No polling or automatic restart |

Stage is not a liveness claim. Status reports both saved stage and current
worker evidence. `ready` is historical startup completion; `active` requires a
fresh worker heartbeat and its exclusive lock, or a live child in an expected
delivery window confirmed by the parent's runtime tools. A last-saved `running`
value alone is insufficient. Status uses `polling`, `delivery_pending`,
`awaiting_binding`, `idle`, or `unknown` as its current activity description.

Before initial acceptance, an error event may be delivered but does not satisfy
startup. If the first read finds merged/closed, deliver the terminal event and
finish without ever claiming running. If the PR changes continuously immediately
after startup, entering the next poll still establishes readiness; a second
quiet window is not a startup prerequisite.

## 5. CLI and child action contract

Parent-facing commands:

```text
goldilocks watcher start --pr URL [--interval 60] [--quiet-seconds 30]
goldilocks watcher status --pr URL
goldilocks watcher stop --pr URL
goldilocks watcher resume --pr URL [--replace]
goldilocks watcher start --pr URL --reopened
goldilocks watcher received --pr URL --event-id ID --part N
goldilocks watcher import --pr URL --state-file ABSOLUTE_PATH
```

Every parent command derives the parent UUID from runtime env. `start` returns
the absolute ticket/state paths, watch ID, and binding information; the parent
resolves the installed launcher path from the skill location. Only a new or
unbound watch requests a spawn. `resume` preserves all evidence and requires the worker lock to be free.
By default it retains the verified child binding and ticket; resuming the same
child does not require another SubagentStart event. `resume --replace` rotates
the ticket, clears the child binding, and requires a new observed handshake.
An unbound failed startup also needs a fresh ticket and new child. No automatic
replacement agent is created. An existing child's handle is advisory context;
the controller's identity is its UUID.

Child-facing commands:

```text
goldilocks watcher advance --ticket-file ABSOLUTE_PATH [--inspect]
goldilocks watcher yield --ticket-file ABSOLUTE_PATH
```

`advance` is the only normal child workflow command. It authenticates and binds,
reconciles accepted delivery, then either runs a poll loop in the foreground or
returns one JSON action. No output during unchanged polling; no agent-controlled
ack/checkpoint/finish command. The host may yield an execution session handle;
the child waits on that exact handle in chunks of at most 60 seconds.

Action schema version 1:

```json
{
  "schema_version": 1,
  "action": "send",
  "watch_id": "00000000-0000-4000-8000-000000000010",
  "event_id": "00000000-0000-4000-8000-000000000020",
  "part": 1,
  "parts": 3,
  "thread_id": "00000000-0000-4000-8000-000000000001",
  "prompt": "Exact frozen notification text",
  "retry_after_seconds": 0,
  "reason": ""
}
```

The action enum is `send`, `wait`, `finished`, `attention`. `send` contains a prompt, destination, and positive part/parts. `attention`
contains the parent destination and a deterministic failure-report prompt, but
no business-event part number. `wait` means an existing worker
must be awaited, a receipt is still uncertain, or a retry delay applies. It is
not an instruction to start a second worker. `finished` requires all completion
conditions. `attention` includes a concrete code and preserved-state location. The child
attempts its supplied failure report through the App tool once, then ends and
reports the send outcome. Failure reporting cannot acknowledge or replace pending
PR evidence; an unavailable App channel leaves the durable fault discoverable
on the next parent/controller activity.
Reject unknown schema versions/actions instead of guessing what to execute.

`advance --inspect` performs ticket-scoped inspection without polling or
offering a message. It reports `worker_running` or `worker_released` in a wait
action, or a saved attention/finished condition. Use it to observe yield cleanup
without invoking the parent-scoped status command from a child.

`yield` is exceptional recovery for a lost host execution handle. It requests
only the recorded execution to stop at a safe point, preserving pending and
collecting state. It must not synthesize a user stop or kill a PID. Wait until
the worker lock is released before a new `advance`. Network calls retain their
45-second timeout; an execution heartbeat is renewed every five seconds even
during a network call. A heartbeat older than 90 seconds is uncertain/stalled,
not proof of termination; a held lock always prevents replacement.

## 6. Collection and delivery

### 6.1 Keep the established SOP

- Initial observation includes existing unresolved threads, reviews and CI.
- No change means remain in the Go wait loop without notifications.
- New changes reset the quiet deadline. Freeze only after a successful,
  unchanged observation whose read began at or after that deadline.
- Do not treat elapsed time during errors, shutdown, or sleep as observed quiet.
  Restarting a collecting batch begins a new full quiet interval.
- Merge/close returns immediately, including when comment access fails.
- Green CI and merge-readiness never terminate an open watch.
- Three consecutive read failures produce an error event; preserve collecting
  observations and the successful baseline. Retain the existing bounded backoff.
- Stop preserves and delivers an existing pending event before `stopped`; perform
  no further GitHub reads after the stop request is observed.

Extract one poll iteration from `Watch` and share it between the legacy loop and
managed `advance`. Do not copy its branches into a second collector.

### 6.2 Outbox and receipts

Before returning `send`, freeze the existing prepared prompt bytes in state.
Retain all comment text, authors, source URLs, locations, head/review commit IDs,
and already prepared supplemental evidence. The standalone helper retains
its `Prepare(extra)` API; this controller does not add a new log-fetching workflow.
Never regenerate already prepared messages.
For new managed events append the generation ID to the footer before freezing;
imported legacy prompts remain byte-for-byte unchanged.

The receipt matcher checks all of: actual child UUID, known parent membership,
canonical tool name, destination UUID, exact prepared prompt/hash, current event,
part, and a successful raw MCP result whose returned thread ID matches the
parent. Missing `isError` is allowed only as the MCP default false with a valid
matching success payload; explicit true, malformed content, empty result, wrong
target, or truncation never counts as acceptance. Preserve `tool_use_id` for
idempotency; conflicting reuse is an error. Previously accepted receipts remain valid after
an explicit child replacement; sender identity is checked at ingestion, not
retroactively rewritten. Only one unaccepted part is offered
at a time. Unrelated tool calls are ignored without storing their contents.

PostToolUse first persists the acceptance. The next `advance` commits the
business acknowledgment only when every part has a receipt; both the new
baseline and acceptance history are saved in one transaction. A replay after a
crash between receipt and ack completes that transaction without re-sending.

If App accepted a send but the hook receipt was lost, certainty is impossible.
Wait five seconds for a receipt, then re-offer the identical part. Permit at
most three offers per part before `needs_attention`; do not clear evidence or
advance the baseline. This is at-least-once delivery with duplicate handling,
not exactly-once processing. Offers and repeated status reads do not count as
progress. A newly accepted part does.

The parent records each actually received part through `received`, using the
trusted source UUID and event footer to check attribution first. The command
verifies the event/part exists in retained delivery history. It returns
`new_part`, `duplicate`, or `event_complete`. This is an explicit parent
acknowledgment, not a hook-observed fact and not proof of business processing.
Wait for all parts before acting. Keep event IDs/part hashes and reception
records for the generation; accepted prompt bodies may be released after the
event is acknowledged. Keep current pending prompts until acceptance completes.
Parent acknowledgment does not block the next poll or startup readiness, avoiding
deadlock when the parent is busy. Never mark a fix/reply/merge as processed here.

## 7. Hooks and recovery

Keep `SessionStart` routing injection. Extend `SubagentStart` to record membership
while preserving routing context. Add `PostToolUse` for the App send tool and a
parent `Stop` handler; retain `SubagentStop` with the new controller semantics.

At SubagentStop, find only the exact bound UUID. An unbound child is left alone;
the parent's starting intent is responsible for exposing missing registration.
For a bound nonterminal watch, compute the next action from saved state. If a
worker is alive, require waiting on its original execution. If delivery is
pending, require `advance` and exact delivery. If terminal acceptance exists,
require verified cleanup. Hooks never run the long poll themselves.

Do not return early just because `stop_hook_active` is true. It describes prior
continuation, not current completion. Two continuations with no progress are
allowed; the next stop enters `needs_attention`, sets an internal yield request,
and emits a visible diagnostic. Do not report cleanup complete until the worker
lock is free. Progress is binding, a completed successful poll cycle (even if
unchanged), a newly accepted part, business ack, or actual execution end.
Heartbeats, retry offers, hook invocations, and claimed checkpoints are not progress.

The parent Stop handler scans only that parent's watches. It blocks an incomplete
startup or an unreported fault with a concrete status/recovery/report instruction,
using the same two-continuation no-progress limit. It does not keep a healthy
parent turn open for the whole PR lifetime. `status` marks the current diagnostic
as surfaced to the parent without advancing progress; this bounds repeated fault
notifications but does not prove the parent told the user. Deadline checks also
run on parent stop. No hook can guarantee execution after a hard interruption.

Malformed/unrelated hook input does not block arbitrary agents. Relevant errors
produce stderr diagnostics and a bounded `systemMessage`; corrupt state stays
untouched. Hook input remains capped at 1 MiB. An oversized or unrecognized
delivery result cannot create a receipt; ordinary retry/fault logic exposes it.
Receipt handlers perform short, bounded state-lock retries for up to one second;
failure to persist is explicit and never converted to success.

## 8. Compatibility, migration, and rollout

- Keep standalone `pr-watch watch/prepare/message/ack/status/stop` for v2/v3 data.
- Replace the skill's old `watcher register/checkpoint/finish/fail` path. Those
  names return a precise migration error and never mutate managed state.
- Preserve legacy registration files; do not silently infer their parent/PR
  ownership or delete them. `import` requires an explicit source path and a free
  original worker lock. It copies a validated v2/v3 state into the parent's v4
  store and preserves pending event IDs, prompt bytes, and collecting observations.
  The source remains unchanged. Source path and digest are retained for audit.
- Importing an already acknowledged baseline creates a new initial snapshot event
  after existing pending delivery, so the new parent receives a complete baseline.
  Existing collecting observations are delivered before this refresh; none are discarded.
- Existing active legacy monitors must stop or finish before import. Old binaries
  reject v4 rather than rewriting it; old rollback uses the untouched legacy file.
- A normal start cannot overwrite an existing watch. Resume never erases pending
  data. A reopened generation requires a finished, fully cleaned previous one.
- Update all three README languages and both PR-watch instruction files together.
  Explicitly remove the rule forbidding parent startup/status commands.
- Retain release artifact naming and platform matrix. Changed hook definitions
  need user trust and, on the tested build, full App restart. Validate actual
  candidate hook events afterward. Do not write trust hashes or bypass review.

## 9. Acceptance and release gates

| ID | Required assertion | Plan task |
| --- | --- | --- |
| A1 | No registration/CLI work after spawn cannot become running; parent sees incomplete startup | 2, 6, 7 |
| A2 | Concurrent parents, PRs and children cannot cross-bind; duplicate start is idempotent | 1, 2 |
| A3 | Legacy event IDs, prepared strings, large numeric IDs and collecting batches survive import/restart | 1, 8 |
| A4 | Hooks can write while polling waits; duplicate worker excluded; failed transactions preserve disk | 1, 3 |
| A5 | Existing 16 unresolved threads appear in initial multipart event; unchanged polls remain quiet | 3, 7, 9 |
| A6 | Wrong target/body/child, failed MCP result, duplicate call IDs, and missing hook cannot falsely ack | 4, 5 |
| A7 | Crash after acceptance/before ack resumes without loss; missing acceptance retries identical bytes | 4 |
| A8 | Parent duplicates and out-of-order parts are handled without asserting business processing | 4, 7 |
| A9 | Repeated premature stops are bounded; real poll/receipt progress resets the budget; status does not | 6, 9 |
| A10 | Pending-before-stop, terminal delivery, worker cleanup, stalled heartbeat and lost handle are correct | 3, 6 |
| A11 | No hook/workflow affects unrelated agents or adds GitHub write authorization | 5, 6, 7 |
| A12 | Installed candidate, real idle-parent wakeup and a real event after 20 quiet minutes pass | 9 |
| A13 | Trusted/restarted hook loading, no-language-runtime launcher smoke, existing packaging checks pass | 5, 9 |

Offline tests use fake clocks and the existing fake GitHub command technique;
network/GitHub writes are not part of unit tests. Runtime tests need an explicitly
authorized disposable PR and test destination. Do not reuse the originally
reported user's PR as a fixture without authorization.

Candidate acceptance runs at least 25 minutes, includes a real change after
20 quiet minutes, starts with an idle parent for delivery, and exercises two
successive same-child continuations. Inject premature stop before bind, during a
live execution, between message parts, and after acceptance before next advance.
Record build/version, actual identities, exact hook results and pass/fail/unknown.
API/UI interruption and App shutdown remain explicitly separate observations.

Offline implementation may complete before those live gates. Publishing or
claiming unattended monitoring ready may not. A failed platform capability is
reported as unsupported/needs attention; no hidden fallback daemon or fabricated
receipt substitutes for it.

## 10. Non-goals and limits

No new GitHub collector, database dependency, MCP server, universal event bus,
model-routing redesign, Windows poller, or full business-processing ledger.
No promise of execution during machine sleep, App shutdown, quota exhaustion,
or hard cancellation. Durable files enable subsequent recovery; they do not
schedule it. Hook failures can be detected on later controller/parent activity,
but nothing here guarantees a message from a process that is no longer running.
