# PR Watch runtime evidence and release gates

Date: 2026-09-10

This document records the runtime gate for the unified Go CLI migration. It is
evidence for implementation tasks, not proof that production lifecycle
protection or end-to-end PR delivery is complete. `passed` means the named
behavior was observed in the retained development records. `not_verified`
means the behavior remains a release gate.

The public labels `parent-A`, `child-A`, and so on replace private runtime
identifiers. Equality claims below were checked against the exact identifiers
in the retained raw records.

## Capability matrix

| Check | Result | Evidence scope |
| --- | --- | --- |
| Baseline helper | passed | pinned source, 20 unittest cases |
| Full Go migration suite | passed | Go 1.25.6, macOS arm64, all packages plus PR-watch tests |
| Race-enabled lifecycle package | passed | `go test -race ./cmd/goldilocks` |
| Six bundled formats and checksums | passed | rebuilt, format-checked, and matched `SHA256SUMS` |
| Packaged macOS arm64 execution | passed | extracted candidate, different cwd, PATH without Go or Python |
| Trusted hook discovery | passed | desktop binary 0.153.4, project hooks |
| Fresh v2 SubagentStart/Stop | passed | ephemeral session using desktop binary |
| Child env UUID equals hook agent_id | passed | exact equality in captured records |
| Two successive continuations | passed | both tested children: false, true, true |
| Original running handle reused | passed | same handle, same token, exit 0 |
| Pending fixture unchanged | passed | both continuation SHA-256 checks match |
| API interruption | passed | running to interrupted; no stop hook; fixture process released separately |
| Current already-running Desktop task reload | not_verified | new probe absent in first old-task child |
| Desktop UI cancellation | not_verified | separate from API interruption |
| Idle App parent wakeup | not_verified | no App test messages sent |
| Real event after 20+ quiet minutes | not_verified | no long-running PR integration yet |
| Candidate installation and trusted-hook loading | not_verified | candidate was archived, not installed |
| Cross-platform installed execution | not_verified | five non-native binaries were cross-built only |

## Evidence boundary

The historical lifecycle baseline is Goldilocks commit
`588d0ee51b0ae327191b7a66e4f17c05f86f4315` and migration source
`baranwang/skills` commit `7417f5545a3aa070216113299a1ffd9b543a7669`.
The pinned source contains 20 PR watcher unittest cases, and the retained
baseline run reports all 20 passing. These Python tests are a behavioral
reference only.

The retained records establish the following anonymized observations:

- `parent-A` / `child-A`: a fresh v2 session delivered `SubagentStart` and
  `SubagentStop`. The child's environment UUID exactly equals both hook
  `agent_id` values; each hook `session_id` exactly equals `parent-A`.
- `parent-B` / `child-B`: the three ordered Stop records have
  `stop_hook_active=false,true,true`. Both continuations kept the original
  running execution handle and token; the execution later exited 0.
- `parent-B` / `child-C`: the three ordered Stop records also have
  `stop_hook_active=false,true,true`. Both continuation checks matched the
  prepared manifest SHA-256; the fixture was not rewritten. Its
  acknowledgement was simulated locally and no App message was sent.
- `parent-C` / `child-D`: API interruption changed the child from `running` to
  `interrupted`, did not automatically resume it, and emitted no
  `SubagentStop`. The fixture process was released separately and verified
  stopped.

The API interruption result does not cover Desktop UI cancellation or prove
automatic watcher cleanup after a hard interruption. An earlier failed
cancellation fixture is excluded because it did not reach readiness.

## Local candidate verification

The local verification ran on 2026-09-10 in the `Asia/Shanghai` time zone. It
covered candidate version 0.2.0 with Go 1.25.6 on macOS arm64 and Codex CLI
0.153.4. The existing installed marketplace package remained at 0.1.0; it was
not treated as the candidate and its install, cache, hooks, and trust settings
were not changed.

The following commands passed against the staged candidate source:

```text
go test -race ./cmd/goldilocks
go test ./... ./__tests__/pr-watch
go run ./scripts/build.go
git diff --exit-code -- bin
git diff --check
```

The full Go suite covers the migrated PR state, GitHub collection, message
preparation, CLI, built-binary polling, and watcher lifecycle packages. The
build regenerated six static binaries, checked their Mach-O, ELF, and PE
formats, and reproduced the sorted checksum manifest without changing the
staged binaries.

Focused state regressions reject JSON `null` for the required `finished` and
prepared-message `prompt` scalars while preserving the rejected state file,
nullable fields, and existing v2 prompt strings byte-for-byte.

The staged tree was exported with `git archive` and extracted into a clean
candidate directory. The extracted manifest reported 0.2.0; all six archived
binaries matched its archived `SHA256SUMS`. The Darwin arm64 binary reported
0.2.0 from another working directory with a runtime PATH containing neither
Go nor Python. The three real command strings from `hooks/hooks.json` were
then exercised with synthetic SessionStart, SubagentStart, and unregistered
SubagentStop payloads. The first two injected the routing instructions and the
unregistered stop produced no output. These payloads are packaging smoke
inputs, not Codex runtime hook acceptance.

The Darwin x86-64, Linux arm64/x86-64, and Windows arm64/x86-64 artifacts were
cross-built and format/checksum verified. They were not executed on their
target platforms and remain `not_verified`. Windows PR polling is unsupported
in this release even though the Windows lifecycle hook launcher is bundled.

## Runtime versions and feature flags

The following values were inspected on 2026-09-09 and refreshed where shown
by the 2026-09-10 local candidate run. Process inspection
confirmed that the Desktop task used the app-bundled `codex` executable; its
machine-specific absolute path is intentionally omitted.

| Command | Relevant output |
| --- | --- |
| `codex --version` | `codex-cli 0.153.4` |
| `codex features list` | `hooks stable true`; `multi_agent stable true`; `multi_agent_v2 stable false`; `plugin_hooks removed false` |
| `go version` | `go version go1.25.6 darwin/arm64` (refreshed 2026-09-10) |
| `gh --version` | `gh version 2.96.0 (2026-07-02)` |

The CLI default `multi_agent_v2=false` can differ from Desktop behavior. The
accepted lifecycle evidence used the same Desktop binary in fresh ephemeral
sessions with `multi_agent_v2` explicitly enabled. No v1 run is counted as v2
evidence. A read-only hook listing found the enabled, trusted project
`SubagentStart` and `SubagentStop` handlers with no errors or warnings.

The Go build baseline remains 1.25.6 with `CGO_ENABLED=0`, `-trimpath`, and
`-ldflags='-s -w'`. Installed users must not need Python or Go. Online PR reads
still require an authenticated GitHub CLI with access to the target PR.

## Release gate and failure exit

Task 8 must not enable production lifecycle protection on any target runtime
that cannot trigger the v2 hooks, identify the original child, and resume that
same child. Offline implementation and pure skill behavior may proceed, but
the failed gate must be recorded and the design returned for specification
revision.

A daemon, an additional sidebar task, or unbounded follow-up restarts cannot
substitute for this gate. A replacement poller also cannot prove that the
original child resumed.

The remaining live acceptance work requires an explicitly authorized test PR,
an actual idle App parent task, and a candidate installation whose hooks have
been reviewed and trusted. It includes two runs of each premature-final
window, normal completion, duplicate-watch reuse, same-cwd PR isolation,
stopping/failure/interruption recovery, Desktop UI cancellation, idle App
wakeup, complete multipart delivery and acknowledgement, and a real event
after more than 20 quiet minutes within an observation lasting at least 25
minutes. Each supported target platform also needs installed execution from a
path containing spaces and the wrong/missing-architecture error checks. Until
those checks run, they stay `not_verified`; local fixtures, synthetic hook
payloads, successful cross-builds, and metadata parsing cannot promote them to
`passed`.

## Go migration coverage map

The 20 Python cases below define the pinned behavioral assertions now covered
by Go `*_test.go` files under `__tests__/pr-watch`. Hook registration and
lifecycle continuation coverage lives with the CLI in
`cmd/goldilocks/watcher_test.go`. Neither the test runner nor an installed user
needs Python.

| Original `test_` suffix | Go file | Assertion retained |
| --- | --- | --- |
| `initial_reports_existing_blockers_and_green_open_does_not_stop` | `watch_test.go` | Initial blockers are reported; green CI on an open PR keeps watching. |
| `pending_event_survives_restart_until_successful_delivery` | `state_test.go` | `event_id` is stable; wrong ack is rejected; repeated ack is idempotent. |
| `new_and_edited_comments_and_thread_replies_are_delivered` | `github_test.go` | New and edited comments plus inline replies appear in the delta. |
| `merge_and_close_are_retried_then_finish_after_ack` | `state_test.go` | A pending terminal event can be redelivered and becomes finished only after ack. |
| `error_does_not_advance_snapshot_and_recovery_is_reported` | `watch_test.go` | An error does not advance the successful baseline; recovery is visible. |
| `pr_identity_reuses_state_and_separates_repositories` | `state_test.go` | Canonical URL reuses state; host, repository, and PR number remain isolated. |
| `exclusive_lock_blocks_a_second_poller` | `state_test.go` | The lock is exclusive and a second poller fails explicitly. |
| `stop_command_ends_watcher_without_contacting_github` | `cli_test.go` | Stop followed by watch performs no network request and finishes after ack. |
| `cancellation_preserves_pending_delivery_before_stopped_event` | `state_test.go` | The original pending delivery precedes the stopped event. |
| `a_later_comment_review_does_not_clear_requested_changes` | `github_test.go` | A later `COMMENTED` review does not clear `CHANGES_REQUESTED`. |
| `prepared_message_parts_are_identical_after_partial_send_and_restart` | `message_test.go` | Restart after partial delivery does not rewrite the prepared manifest. |
| `notifications_keep_comment_evidence_without_raw_event_json` | `message_test.go` | Author, original text, location, head, and review commit are retained without a raw JSON envelope. |
| `notifications_show_changed_checks_and_removed_feedback` | `message_test.go` | Unchanged CI is omitted; a thread reports only that it is no longer unresolved. |
| `notification_parts_preserve_long_comments_and_log_excerpts` | `message_test.go` | Reassembled parts retain complete long comments and supplemental log excerpts. |
| `notifications_describe_errors_recovery_and_terminal_states` | `message_test.go` | Error, recovery, and terminal messages remain distinct. |
| `existing_json_message_manifest_is_not_rewritten` | `state_test.go`, `message_test.go` | Existing JSON prompts and parts are reused byte-for-byte. |
| `cancel_during_a_github_call_prevents_further_requests` | `github_test.go` | Cancellation after one GitHub call prevents subsequent requests. |
| `collector_paginates_unresolved_threads_and_nested_comments` | `github_test.go` | Both pagination layers run, resolved threads are filtered, and head stays fixed. |
| `terminal_state_is_reported_even_if_comment_access_is_unavailable` | `github_test.go` | Terminal metadata is reported without requesting comments. |
| `cli_waits_quietly_and_exits_on_merge_or_cancellation` | `cli_test.go` | The built binary waits without unchanged notifications and exits on merge or cancellation. |

Go-specific checks must also cover IDs greater than 2^53, `[]rune` message
splitting, cross-version `flock`, execution without Python or Go installed,
independent action flags, and exit codes. The new quiet-window behavior,
recovery from `collecting`, and both premature-final paths require their own
acceptance checks and cannot be inferred from the original 20 cases.
