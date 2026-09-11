# PR Watcher Controller Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make startup, delivery, and recovery of a PR watcher depend on durable controller state and observed runtime evidence.

**Architecture:** Extend the existing PR state store with a managed lifecycle and a transactional outbox. Reuse the collector and quiet-window algorithm, move lifecycle decisions out of the model, and use Codex hooks for child membership, delivery receipts, and bounded continuation. Parent and child instructions become small clients of the controller.

**Tech Stack:** Go standard library, existing gh-based GitHub reader, Codex Desktop hooks and App tools, shell/PowerShell launchers, GoReleaser v2.18.1.

**Spec:** [2026-09-10-pr-watch-controller-design.md](../specs/2026-09-10-pr-watch-controller-design.md)

## Global Constraints

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

---

## Execution context and dependency order

Read the complete spec first. The inspected baseline is commit `835b53c`; PR #6
was already merged. At execution time use the git-worktrees skill to create an
isolated `codex/` branch from the current default branch, check for repository
instructions, and re-resolve line locations by the symbols below. Do not add
implementation commits to the old PR branch. This plan does not authorize
publishing releases or sending messages to unrelated tasks.

Execute Tasks 1–9 in order. Tasks share a state format and are deliberately one
plan. Each task has a testable boundary and a reviewable commit. Tests below use
`package prwatch` for new controller tests; existing external `prwatch_test`
suites remain unchanged except where explicitly listed. New tests on Windows
skip POSIX managed operations, while decoder/format/routing checks still run.

The implementation sketches specify the important algorithms and exact
interfaces. Follow the listed validation rules and tests as well as the code;
do not replace production logic with a hard-coded result from a sample test.
All shell examples assume repository root and use the required `rtk` prefix.

## File map

| Path | Responsibility |
| --- | --- |
| `internal/prwatch/control.go` (new) | Managed schema, actions, constants, validation and initial control constructor |
| `internal/prwatch/controller.go` (new) | Parent start/status/resume/stop/import, tickets, observed membership and path resolver |
| `internal/prwatch/advance.go` (new) | Child action executor, worker ownership, heartbeat, yield, managed poll loop |
| `internal/prwatch/delivery.go` (new) | Exact-part offers, receipt matching, ack transaction, reception/deduplication |
| `internal/prwatch/lifecycle.go` (new) | Progress accounting, child/parent stop decisions and diagnostics |
| `internal/prwatch/runtime.go` (new) | Runtime payload DTOs, App result decoder, hook-to-controller dispatch |
| `internal/prwatch/types.go` | Add optional Control and private transaction flag to existing State/Store |
| `internal/prwatch/state.go` | v4 validation, short state transactions, fsync-backed commit; preserve old standalone semantics |
| `internal/prwatch/watch.go` | Extract shared single poll cycle; retain existing Watch public signature |
| `internal/prwatch/cli.go` | Reject direct mutations of managed state; retain standalone CLI |
| `internal/prwatch/controller_cli.go` (new) | Versioned watcher CLI parsing, runtime identity and action encoding |
| `cmd/goldilocks/hook.go` | Decode payload once; combine routing context with controller hook decisions |
| `cmd/goldilocks/watcher.go` | Replace old registry/checkpoint implementation with thin controller CLI adapter |
| `cmd/goldilocks/main.go` | Context-aware watcher dispatch and exit-code mapping |
| `hooks/hooks.json` | Add App PostToolUse and parent Stop handlers |
| `internal/prwatch/*_test.go`, `cmd/goldilocks/*_test.go` | Colocated domain, integration and adapter tests |
| `skills/pr-watch/SKILL.md`, `skills/pr-watch/references/watcher.md` | Parent protocol and child execute-loop instructions |
| `README.md`, `README.zh-hans.md`, `README.zh-hant.md` | User-visible startup, recovery, trust/restart and limits |
| `scripts/verify.go`, `scripts/verify_test.go` | Packaging smoke for the complete hook set |
| `.codex-plugin/plugin.json`, `scripts/SHA256SUMS` | Candidate 0.3.0 version and regenerated pinned asset hashes |
| `docs/verification/pr-watch-controller.md` (new at implementation) | Installed-candidate evidence and explicitly open release gates |

No launcher rewrite, new dependency, generic framework or runtime log scraper.
Existing lock functions are reused; `.state.lock` is another POSIX flock, not a
new locking implementation.

### Task 1: Managed schema and atomic transactions

**Files:**
- Create: `internal/prwatch/control.go`, `internal/prwatch/controller_test.go`
- Modify: `internal/prwatch/types.go`, `internal/prwatch/state.go` (`ReadState`, `Save`)
- Test: `internal/prwatch/state_test.go`, `internal/prwatch/controller_test.go`

**Interfaces:**
- Consumes: existing `State`, `Store`, `ParsePR`, `newUUID`, `flock`, `ReadState`, `Store.Save`.
- Produces: types below; `newControl(parent, watchID, cwd, ticketHash string, now time.Time, options Options) *Control`; `validateControl(State) error`; `(*Store).Update(func(*Store) error) error`.
- Existing `Store.Lock(bool)` remains the worker/legacy lock; Update uses `.state.lock`.

- [ ] **Step 1: Add the schema and failing transaction tests.**

Add `Control *Control` with JSON tag `control,omitempty` to State and private
`deferSave bool` to Store. Define these concrete types in `control.go`:

```go
type Stage string
const (
    Starting Stage = "starting"
    Initializing Stage = "initializing"
    Running Stage = "running"
    Stopping Stage = "stopping"
    NeedsAttention Stage = "needs_attention"
    Finished Stage = "finished"
)
type PollCycle struct {
    QuietDeadline time.Time `json:"quiet_deadline"`
    Failures int `json:"failures"`
    ReadFailed bool `json:"read_failed"`
}
type Execution struct {
    ID string `json:"id"`
    HeartbeatAt time.Time `json:"heartbeat_at"`
    HostHandle string `json:"host_handle,omitempty"`
    Ended bool `json:"ended"`
}
type Receipt struct {
    CallID string `json:"call_id"`
    AgentID string `json:"agent_id"`
    ParentID string `json:"parent_id"`
    EventID string `json:"event_id"`
    Part int `json:"part"`
    SHA256 string `json:"sha256"`
    AcceptedAt time.Time `json:"accepted_at"`
}
type DeliveryPart struct {
    Prompt string `json:"prompt,omitempty"`
    SHA256 string `json:"sha256"`
    Offers int `json:"offers"`
    OfferedAt time.Time `json:"offered_at"`
    Receipt *Receipt `json:"receipt,omitempty"`
    Received bool `json:"received"`
}
type Delivery struct {
    EventID string `json:"event_id"`
    Kind string `json:"kind"`
    Parts []DeliveryPart `json:"parts"`
}
type Control struct {
    WatchID string `json:"watch_id"`
    ParentID string `json:"parent_id"`
    AgentID string `json:"agent_id"`
    Cwd string `json:"cwd"`
    TicketSHA256 string `json:"ticket_sha256"`
    CreatedAt time.Time `json:"created_at"`
    BindAfter time.Time `json:"bind_after"`
    Stage Stage `json:"stage"`
    Interval time.Duration `json:"interval"`
    Quiet time.Duration `json:"quiet"`
    InitialEventID string `json:"initial_event_id"`
    InitialAccepted bool `json:"initial_accepted"`
    RefreshRequired bool `json:"refresh_required"`
    Ready bool `json:"ready"`
    Worker Execution `json:"worker"`
    Cycle PollCycle `json:"cycle"`
    Outbox *Delivery `json:"outbox,omitempty"`
    History map[string]Delivery `json:"history"`
    Progress uint64 `json:"progress"`
    LastStopProgress uint64 `json:"last_stop_progress"`
    NoProgressStops int `json:"no_progress_stops"`
    FailureCode string `json:"failure_code"`
    FailureDetail string `json:"failure_detail"`
    FaultSeen bool `json:"fault_seen"`
    ImportPath string `json:"import_path,omitempty"`
    ImportSHA256 string `json:"import_sha256,omitempty"`
}
type Action struct {
    SchemaVersion int `json:"schema_version"`
    Action string `json:"action"`
    WatchID string `json:"watch_id"`
    EventID string `json:"event_id,omitempty"`
    Part int `json:"part,omitempty"`
    Parts int `json:"parts,omitempty"`
    ThreadID string `json:"thread_id,omitempty"`
    Prompt string `json:"prompt,omitempty"`
    RetryAfterSeconds int `json:"retry_after_seconds"`
    Reason string `json:"reason"`
}
```

Create internal helpers in `controller_test.go`; later tasks reuse these exact
names. Import `bytes`, `errors`, `os`, `runtime`, `strings`, `testing`, `time` as
used below. Production files import their own used stdlib packages.

```go
const specParent = "00000000-0000-4000-8000-000000000001"
const specChild = "00000000-0000-4000-8000-000000000002"
const specWatch = "00000000-0000-4000-8000-000000000003"
const specPR = "https://github.com/example/project/pull/7"
var specTime = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
func managedFixture(t *testing.T) *Store {
    t.Helper()
    if runtime.GOOS == "windows" { t.Skip("managed POSIX state") }
    s, err := NewStore(t.TempDir(), specPR)
    if err != nil { t.Fatal(err) }
    s.Data.Version = 4
    s.Data.Control = newControl(specParent, specWatch, t.TempDir(),
        strings.Repeat("a", 64), specTime, Options{time.Minute, 30*time.Second})
    if err := s.Save(); err != nil { t.Fatal(err) }
    return s
}
func TestManagedTransactionRollsBack(t *testing.T) {
    s := managedFixture(t)
    before, err := os.ReadFile(s.Path)
    if err != nil { t.Fatal(err) }
    sentinel := errors.New("abort")
    err = s.Update(func(tx *Store) error {
        tx.Data.Control.Progress++
        if err := tx.Save(); err != nil { return err }
        return sentinel
    })
    if !errors.Is(err, sentinel) { t.Fatal(err) }
    after, err := os.ReadFile(s.Path)
    if err != nil { t.Fatal(err) }
    if !bytes.Equal(before, after) { t.Fatal("aborted transaction persisted") }
}
func TestManagedTransactionDoesNotNeedWorkerLock(t *testing.T) {
    s := managedFixture(t)
    release, err := s.Lock(false)
    if err != nil { t.Fatal(err) }
    defer release()
    if err := s.Update(func(tx *Store) error {
        tx.Data.Control.Progress++
        return nil
    }); err != nil { t.Fatal(err) }
    if s.Data.Control.Progress != 1 { t.Fatal(s.Data.Control.Progress) }
}
```

- [ ] **Step 2: Run the new tests red.**

Run: `rtk go test ./internal/prwatch -run 'TestManagedTransaction' -count=1`
Expected: missing `newControl` or `Update`, not unrelated syntax errors.

- [ ] **Step 3: Implement transactions and managed validation.**

`newControl` sets Starting, UTC timestamps, options, empty history, and zero
progress. Validate canonical PR, UUIDs, absolute cwd, 64 hex ticket digest,
positive durations, known stage, nonnegative counters, receipt identity, and
outbox matching pending. `Ready` requires InitialAccepted; Finished requires
business finished and no pending/collecting/outbox. Reject Control on v2/v3,
null/missing Control on v4, unknown versions and malformed state without writing.

```go
func (s *Store) Update(fn func(*Store) error) error {
    path := strings.TrimSuffix(s.Path, filepath.Ext(s.Path)) + ".state.lock"
    release, err := flock(path)
    if err != nil { return err }
    defer release()
    state, err := ReadState(s.Path)
    if err != nil { return err }
    if state.PRURL != s.PR.URL || state.Control == nil {
        return errors.New("managed state identity mismatch")
    }
    tx := *s
    tx.Data, tx.deferSave = state, true
    if err := fn(&tx); err != nil { return err }
    if err := validateControl(tx.Data); err != nil { return err }
    tx.deferSave = false
    if err := tx.Save(); err != nil { return err }
    s.Data = tx.Data
    return nil
}
```

Save starts with `if s.deferSave { return nil }`. The outer Save encodes, syncs,
closes, renames, and syncs the directory. After directory-sync failure report an
uncertain commit and reread before retry; never assume rename did not happen.
Preserve `UseNumber` and existing v2->v3 migration tests. Add cases for null
Control, invalid stage, mismatched receipt, unknown version and failed Save.

- [ ] **Step 4: Run domain and race tests.**

Run: `rtk go test ./internal/prwatch -count=1`
Run: `rtk go test -race ./internal/prwatch -run 'TestManagedTransaction|TestLock' -count=1`
Expected: PASS; held worker lock does not block a state transaction.

- [ ] **Step 5: Commit the state boundary.**

```sh
rtk git add internal/prwatch/control.go internal/prwatch/types.go internal/prwatch/state.go internal/prwatch/controller_test.go internal/prwatch/state_test.go
rtk git commit -m "feat: add transactional managed watch state" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 2: Parent intents and ticket-based child binding

**Files:**
- Create: `internal/prwatch/controller.go`
- Modify/Test: `internal/prwatch/controller_test.go`

**Interfaces:**
- Consumes: Task 1 schema and transactions, `newUUID`, `ParsePR`.
- Produces the concrete types below and these exact signatures:
  - `NewController(root string, now func() time.Time) (*Controller, error)`
  - `DefaultControllerRoot() (string, error)`
  - `(*Controller).Open(parentID, prURL string) (*Store, error)`
  - `(*Controller).Start(parentID, cwd, prURL string, opts StartOptions) (StartResult, error)`
  - `(*Controller).ObserveStart(parentID, agentID, turnID string) error`
  - `(*Controller).Bind(ticketFile, agentID string) (*Store, error)`
  - `bindingAllowed(State, Ticket, string, Membership) bool`

```go
type Controller struct { Root string; Now func() time.Time }
type StartOptions struct { Interval, Quiet time.Duration; Reopened bool }
type StartResult struct {
    WatchID string `json:"watch_id"`
    TicketFile string `json:"ticket_file,omitempty"`
    StateFile string `json:"state_file"`
    Spawn bool `json:"spawn"`
    AgentID string `json:"agent_id,omitempty"`
}
type Ticket struct {
    WatchID string `json:"watch_id"`
    ParentID string `json:"parent_id"`
    PRURL string `json:"pr_url"`
    Nonce string `json:"nonce"`
}
type Membership struct {
    ParentID string `json:"parent_id"`
    AgentID string `json:"agent_id"`
    TurnID string `json:"turn_id"`
    ObservedAt time.Time `json:"observed_at"`
}
```

- [ ] **Step 1: Add a handshake fixture and failing identity tests.**

```go
func startedFixture(t *testing.T) (*Controller, StartResult) {
    t.Helper()
    if runtime.GOOS == "windows" { t.Skip("managed POSIX state") }
    c, err := NewController(t.TempDir(), func() time.Time { return specTime })
    if err != nil { t.Fatal(err) }
    r, err := c.Start(specParent, t.TempDir(), specPR,
        StartOptions{Interval: time.Minute, Quiet: 30*time.Second})
    if err != nil { t.Fatal(err) }
    return c, r
}
func TestTicketRequiresObservedChildAndIsSingleOwner(t *testing.T) {
    c, r := startedFixture(t)
    if _, err := c.Bind(r.TicketFile, specChild); err == nil { t.Fatal("unobserved bind") }
    if err := c.ObserveStart(specParent, specChild, specWatch); err != nil { t.Fatal(err) }
    s, err := c.Bind(r.TicketFile, specChild)
    if err != nil { t.Fatal(err) }
    if s.Data.Control.Stage != Initializing || s.Data.Control.Ready {
        t.Fatal("binding fabricated readiness")
    }
    if _, err := c.Bind(r.TicketFile, specChild); err != nil { t.Fatal(err) }
    other := "00000000-0000-4000-8000-000000000004"
    if _, err := c.Bind(r.TicketFile, other); err == nil { t.Fatal("different owner") }
}
func TestDuplicateStartKeepsIntent(t *testing.T) {
    c, first := startedFixture(t)
    next, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{})
    if err != nil { t.Fatal(err) }
    if next.WatchID != first.WatchID || next.TicketFile != first.TicketFile {
        t.Fatal("duplicate intent")
    }
}
```

Add a table with two parent UUIDs, PRs 7/8 and children 2/4. Observe starts in
reverse order, bind each ticket, assert exact triples. Concurrent Start calls
for the same key must return one WatchID. Include a separate-process lock case
using the existing built-CLI test technique when Task 7 wires CLI commands.

- [ ] **Step 2: Run handshake tests red.**

Run: `rtk go test ./internal/prwatch -run 'TestTicket|TestDuplicateStart' -count=1`
Expected: missing Controller methods before implementation.

- [ ] **Step 3: Implement durable intent creation and membership binding.**

Root uses CODEX_HOME or UserHomeDir/.codex, then goldilocks/pr-watch. Open uses
`NewStore(filepath.Join(c.Root,"parents",parentID),prURL)`. Validate UUID before
using it in a path. Create under the per-PR state lock and revalidate any existing
file. Zero options mean defaults, negative values fail; conflicting explicit
options on duplicate start fail rather than silently reconfigure.

Generate 32 random bytes for the nonce; write the ticket 0600 before state.
A failed state creation can leave an orphan ticket; a later locked Start may
replace it only if no state exists. If state exists, a missing/mismatched ticket
requires explicit resume. Bound duplicates return AgentID and Spawn=false.
Use a byte-identical ticket for an existing unbound intent.

```go
func bindingAllowed(s State, ticket Ticket, child string, m Membership) bool {
    c := s.Control
    sum := sha256.Sum256([]byte(ticket.Nonce))
    return c != nil && ticket.WatchID == c.WatchID && ticket.PRURL == s.PRURL &&
        ticket.ParentID == c.ParentID && hex.EncodeToString(sum[:]) == c.TicketSHA256 &&
        m.ParentID == c.ParentID && m.AgentID == child &&
        !m.ObservedAt.Before(c.BindAfter) && (c.AgentID == "" || c.AgentID == child)
}
```

Reject an unbound intent after ten minutes. Repeat Bind by an already bound
owner skips membership age/expiry but still checks ticket and owner. First bind
sets AgentID/Initializing and increments Progress once. Serialize Bind under
`ROOT/parents/PARENT_UUID/bindings.lock`, then acquire the target state lock.
Under that parent lock reject a child UUID already bound to another nonfinished
watch by inspecting the atomically saved parent records. No code takes these
locks in reverse order. Add a concurrent two-ticket/same-child rejection test. ObserveStart writes only
membership, only if the parent has an unbound intent; it never assigns a watch.
Unknown parents are ignored. Bound state itself retains verified membership,
so later deletion of the temporary start record cannot invalidate receipts.

Use EvalSymlinks and filepath.Rel to reject relative/outside-root ticket paths,
symlinked state targets, malformed JSON, wrong nonce, PR, watch or parent. Remove
unused start records older than 24 hours during Start and later Status; do not
remove records needed by an unbound intent. Path checks are accident isolation,
not a security boundary against an actor with unrestricted local shell access.

- [ ] **Step 4: Run identity, concurrency and path validation tests.**

Run: `rtk go test -race ./internal/prwatch -run 'TestTicket|TestDuplicateStart|TestManaged' -count=1`
Expected: PASS including wrong-parent, expired-ticket and outside-root cases.

- [ ] **Step 5: Commit the startup protocol.**

```sh
rtk git add internal/prwatch/controller.go internal/prwatch/controller_test.go
rtk git commit -m "feat: bind watch intents to observed child identities" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 3: Share the polling algorithm and track real worker execution

**Files:**
- Create: `internal/prwatch/advance.go`, `internal/prwatch/advance_test.go`
- Modify: `internal/prwatch/watch.go`, `internal/prwatch/watch_test.go`
- Test: existing `internal/prwatch/cli_test.go` long-poll cases

**Interfaces:**
- Consumes: `Dependencies`, `Options`, Store domain methods, Task 1 transactions and PollCycle.
- Produces:
  - `(*Store).ApplyPoll(snapshot Snapshot, readErr error, started, observed time.Time, options Options, cycle PollCycle) (PollOutcome, error)`
  - `(*Controller).PollManaged(ctx context.Context, s *Store, deps Dependencies) (json.RawMessage, error)`
  - `(*Controller).Yield(ticketFile, agentID string) error`
  - `(*Controller).WorkerStatus(s *Store) (WorkerStatus, error)`
  - `ErrYielded` and `ErrBusy` sentinel errors.

```go
type PollOutcome struct {
    Cycle PollCycle
    Event json.RawMessage
    Delay time.Duration
}
type WorkerStatus struct { Locked, Fresh bool; ExecutionID string }
var ErrYielded = errors.New("current execution yielded; state preserved")
var ErrBusy = errors.New("watch execution already running")
```

- [ ] **Step 1: Add failing shared-cycle regressions.**

In advance_test.go define this snapshot helper; later tests reuse it:

```go
func managedSnapshot() Snapshot {
    return Snapshot{"head_sha":"abc123", "state":"OPEN", "draft":false,
        "mergeable":"MERGEABLE", "merge_state":"CLEAN", "checks":[]any{},
        "comments":map[string]any{}, "reviews":map[string]any{}, "threads":map[string]any{}}
}
func TestManagedQuietRequiresSuccessfulObservation(t *testing.T) {
    s := managedFixture(t)
    s.Data.Snapshot = managedSnapshot()
    changed := managedSnapshot()
    changed["head_sha"] = "def456"
    opts := Options{time.Minute, 30*time.Second}
    first, err := s.ApplyPoll(changed, nil, specTime, specTime, opts, PollCycle{})
    if err != nil { t.Fatal(err) }
    if len(first.Event) != 0 { t.Fatal("froze before quiet") }
    early, err := s.ApplyPoll(changed, nil, specTime.Add(20*time.Second),
        specTime.Add(35*time.Second), opts, first.Cycle)
    if err != nil { t.Fatal(err) }
    if len(early.Event) != 0 { t.Fatal("read began before quiet deadline") }
    last, err := s.ApplyPoll(changed, nil, specTime.Add(36*time.Second),
        specTime.Add(37*time.Second), opts, early.Cycle)
    if err != nil { t.Fatal(err) }
    if len(last.Event) == 0 { t.Fatal("never froze observed quiet") }
}
```

Add a bound helper for this and subsequent tasks:

```go
func boundFixture(t *testing.T) (*Controller, StartResult, *Store) {
    t.Helper()
    c, r := startedFixture(t)
    if err := c.ObserveStart(specParent, specChild, specWatch); err != nil { t.Fatal(err) }
    s, err := c.Bind(r.TicketFile, specChild)
    if err != nil { t.Fatal(err) }
    return c, r, s
}
func TestManagedWorkerExcludesDuplicateButAllowsStateWrites(t *testing.T) {
    c, _, s := boundFixture(t)
    release, err := s.Lock(false)
    if err != nil { t.Fatal(err) }
    defer release()
    _, err = c.PollManaged(context.Background(), s, Dependencies{})
    if !errors.Is(err, ErrBusy) { t.Fatal(err) }
    if err := s.Update(func(tx *Store) error {
        tx.Data.Control.FaultSeen = true
        return nil
    }); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Run new cycle/worker tests red.**

Run: `rtk go test ./internal/prwatch -run 'TestManagedQuiet|TestManagedWorker' -count=1`
Expected: missing ApplyPoll/PollManaged.

- [ ] **Step 3: Extract the existing Watch read-result logic without changing its semantics.**

ApplyPoll handles exactly one read; it does not acquire locks or sleep. The
caller supplies observation timestamps and wraps it in a transaction when
managed. Move the existing error/Observe/Freeze/backoff branches into it:

```go
func (s *Store) ApplyPoll(snapshot Snapshot, readErr error, started, observed time.Time,
    options Options, cycle PollCycle) (PollOutcome, error) {
    out := PollOutcome{Cycle: cycle}
    if errors.Is(readErr, ErrStopped) {
        out.Event, readErr = s.Stop(observed)
        return out, readErr
    }
    if readErr != nil {
        var failure *ReadError
        if !errors.As(readErr, &failure) { return out, readErr }
        out.Cycle.Failures++
        out.Cycle.ReadFailed = true
        if out.Cycle.Failures >= 3 {
            var err error
            out.Event, err = s.Failure(readErr.Error(), observed)
            if err != nil || out.Event != nil { return out, err }
        }
    } else {
        out.Cycle.Failures = 0
        kind, err := s.Observe(snapshot, observed)
        if err != nil { return out, err }
        if kind == "initial" || kind == "merged" || kind == "closed" ||
            kind == "recovered" && s.Data.Snapshot == nil {
            out.Event, err = s.Freeze("", observed)
            return out, err
        }
        if kind != "" || cycle.ReadFailed && s.Data.Collecting != nil {
            out.Cycle.QuietDeadline = observed.Add(options.Quiet)
        }
        if s.Data.Collecting != nil && kind == "" && !cycle.ReadFailed &&
            !out.Cycle.QuietDeadline.IsZero() && !started.Before(out.Cycle.QuietDeadline) {
            out.Event, err = s.Freeze("", observed)
            return out, err
        }
        out.Cycle.ReadFailed = false
    }
    out.Delay = min(options.Interval, 300*time.Second)
    for range min(out.Cycle.Failures, 3) { out.Delay = min(2*out.Delay, 300*time.Second) }
    if !out.Cycle.ReadFailed && !out.Cycle.QuietDeadline.IsZero() {
        out.Delay = min(out.Delay, max(time.Duration(0), out.Cycle.QuietDeadline.Sub(observed)))
    }
    return out, nil
}
```

Rewrite existing Watch to retain its pending/finished/stop guards, call Read,
ApplyPoll, and its existing one-second cancellation-aware wait. Preserve its
signature and all standalone tests. On loop restart with Collecting, reset the
quiet deadline to now+Quiet, not the previously expired wall-clock deadline.

- [ ] **Step 4: Implement the managed worker around the shared cycle.**

PollManaged acquires `.lock` nonblocking before using Dependencies; lock conflict
returns ErrBusy. Generate an execution UUID; save worker ID/heartbeat and mark
Ready/Running only if InitialAccepted and no requested stop/fault. Start a
heartbeat goroutine using `deps.Wait(heartbeatCtx, 5*time.Second)` that updates heartbeat under `.state.lock` only
while the worker ID still matches. Join the goroutine on exit; it uses a separate
Store value to avoid sharing mutable `s.Data` across goroutines. Heartbeat does
not increment Progress. Every successful completed read increments Progress once.

The core ordering is:

```text
acquire worker lock
transaction: set execution ID; reset quiet to now+Quiet if collecting, otherwise zero; enter polling
start heartbeat
loop:
  transaction: reread, prefer pending/terminal/stop/yield; read options/cycle
  read GitHub without state lock, checking stop/yield between API calls
  transaction: reread; recheck stop/yield and execution ID; apply read result
  if pending: return event
  wait in one-second slices, outside state lock
defer: stop and join heartbeat; release worker lock; transactionally mark matching execution ended
```

A yield observed during a read discards that incomplete read, preserves saved
collecting/pending, and returns ErrYielded. A stop observed during/after the read
stages Stop before any new reads. Retain ReadGH's existing 45-second timeout.
WorkerStatus probes the worker lock, rereads execution state and reports Fresh
only when heartbeat is at most 90 seconds old; stale heartbeat never authorizes
replacement while the lock is held. The optional host handle is populated only
from a separately verified runtime adapter; empty is supported and not invented.

- [ ] **Step 5: Verify stop, heartbeat, restart and original regressions.**

Run: `rtk go test -race ./internal/prwatch -count=1`
Add channel-coordinated tests: stop during blocked fake Read; heartbeat while Read
is blocked; yield preserving exact pending bytes; cancelled context releasing
worker lock; failed heartbeat persistence returning an observable error; resumed
Collecting receives a full new quiet interval; a new execution with no Collecting
clears any previous quiet deadline so a past deadline cannot create a busy loop. Use fake clocks/channels rather
than real 90-second sleeps. The existing built CLI waiting tests must still pass.

- [ ] **Step 6: Commit the polling/ownership boundary.**

```sh
rtk git add internal/prwatch/advance.go internal/prwatch/advance_test.go internal/prwatch/watch.go internal/prwatch/watch_test.go
rtk git commit -m "refactor: share poll cycles and track managed executions" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 4: Receipt-driven outbox and the child advance loop

**Files:**
- Create: `internal/prwatch/delivery.go`, `internal/prwatch/delivery_test.go`
- Modify: `internal/prwatch/advance.go`, `internal/prwatch/message.go` (managed footer before initial freezing)

**Interfaces:**
- Consumes: Tasks 1–3, existing `Prepare`, `Message`, `Ack`, `PendingEvent`.
- Produces:
  - `(*Controller).Advance(ctx context.Context, ticketFile, agentID string, deps Dependencies) (Action, error)`
  - `(*Controller).AcceptSend(send ObservedSend) error`
  - `(*Controller).Receive(parentID, prURL, eventID string, part int) (string, error)`
  - `(*Store).Offer(now time.Time) (Action, error)`
  - `(*Store).CommitAccepted() error`

```go
type ObservedSend struct {
    AgentID, ParentID, CallID, Prompt string
    Accepted bool
    ObservedAt time.Time
}
```

- [ ] **Step 1: Add a pending delivery helper and failing receipt tests.**

```go
func deliveryFixture(t *testing.T) (*Controller, StartResult, *Store, Action) {
    t.Helper()
    c, r, s := boundFixture(t)
    err := s.Update(func(tx *Store) error {
        _, err := tx.Stage("initial", managedSnapshot(), Changes{}, nil, nil, specTime)
        return err
    })
    if err != nil { t.Fatal(err) }
    a, err := c.Advance(context.Background(), r.TicketFile, specChild, Dependencies{})
    if err != nil { t.Fatal(err) }
    if a.Action != "send" { t.Fatal(a) }
    return c, r, s, a
}
func TestSendAcceptanceRequiresExactDestinationAndBody(t *testing.T) {
    c, _, s, a := deliveryFixture(t)
    for _, bad := range []ObservedSend{
        {specChild, specWatch, "bad-target", a.Prompt, true, specTime},
        {specChild, specParent, "bad-body", a.Prompt+"changed", true, specTime},
        {specWatch, specParent, "bad-agent", a.Prompt, true, specTime},
        {specChild, specParent, "failed", a.Prompt, false, specTime},
    } {
        _ = c.AcceptSend(bad)
    }
    state, err := ReadState(s.Path)
    if err != nil { t.Fatal(err) }
    if state.Control.Outbox.Parts[0].Receipt != nil { t.Fatal("false receipt") }
    good := ObservedSend{specChild, specParent, "call-1", a.Prompt, true, specTime}
    if err := c.AcceptSend(good); err != nil { t.Fatal(err) }
    if err := c.AcceptSend(good); err != nil { t.Fatal(err) }
    state, err = ReadState(s.Path)
    if err != nil { t.Fatal(err) }
    if state.LastAck != nil { t.Fatal("receipt alone acknowledged business event") }
}
func TestAcceptedEventCommitsAtomicallyAfterRestart(t *testing.T) {
    c, _, s, a := deliveryFixture(t)
    err := c.AcceptSend(ObservedSend{specChild, specParent, "call-1", a.Prompt, true, specTime})
    if err != nil { t.Fatal(err) }
    again, err := c.Open(specParent, specPR)
    if err != nil { t.Fatal(err) }
    if err := again.Update(func(tx *Store) error { return tx.CommitAccepted() }); err != nil { t.Fatal(err) }
    state, err := ReadState(s.Path)
    if err != nil { t.Fatal(err) }
    if state.LastAck == nil || *state.LastAck != a.EventID || state.Control.Outbox != nil {
        t.Fatal("acceptance did not commit baseline and history together")
    }
}
```

- [ ] **Step 2: Run the receipt tests red.**

Run: `rtk go test ./internal/prwatch -run 'TestSendAcceptance|TestAcceptedEvent' -count=1`
Expected: missing Advance/AcceptSend/CommitAccepted.

- [ ] **Step 3: Implement immutable offers and receipt matching.**

Offer runs only inside Update. If no outbox, call Prepare("") and obtain every
Message; construct parts with SHA-256 of exact prompts. Existing prepared prompts
are never rewritten. For new managed manifests append `Run: WATCH_UUID` alongside
the existing Watch/Event/Part footer before Prepare freezes them. Capture the
initial successful event ID only when RefreshRequired=false; imported pending
initial events cannot stand in for the required fresh baseline. Error events
never satisfy initial readiness.

For the first unaccepted part: if fewer than five seconds since the prior offer,
return wait with remaining positive seconds. Otherwise, if Offers<3 increment
Offers and OfferedAt and return exact send. At three unresolved offers enter
NeedsAttention with `delivery_receipt_missing`; preserve pending/outbox. Offers
are not progress. A normal fast successful receipt moves directly to the next
part, without waiting five seconds.

AcceptSend locates only watches bound to AgentID under ParentID. Match current
outbox prompt exactly and its hash, require Accepted and a nonempty CallID, and
persist Receipt plus one Progress increment. Repeating an identical call ID is
idempotent; a call ID already attached to different content fails. Once persisted, an
accepted receipt remains valid after an authorized child replacement: validate
the current sender at ingestion, not by retroactively comparing old receipt
AgentID values to the new Control.AgentID during state reload. A repeated
matching send under a new call ID does not increment accepted progress again.
Ignore unrelated agents/tools without recording prompt bodies.

CommitAccepted runs inside Update. With incomplete outbox return without ack.
With all receipts, clone the delivery into History, clear historical prompt
bodies, set InitialAccepted only for InitialEventID, call existing Ack(EventID),
clear Outbox and increment Progress. The transaction saves once; no partial
state can expose a new Snapshot without its acceptance history. For a terminal
event leave final lifecycle transition to lock-free cleanup in Advance.

The transaction body for acknowledgment is concrete:

```go
func (s *Store) CommitAccepted() error {
    control := s.Data.Control
    if control == nil || control.Outbox == nil { return nil }
    current := control.Outbox
    for _, part := range current.Parts {
        if part.Receipt == nil { return nil }
    }
    history := *current
    history.Parts = append([]DeliveryPart(nil), current.Parts...)
    for i := range history.Parts { history.Parts[i].Prompt = "" }
    if err := s.Ack(current.EventID); err != nil { return err }
    control.History[current.EventID] = history
    if current.EventID == control.InitialEventID { control.InitialAccepted = true }
    control.Outbox = nil
    control.Progress++
    return nil
}
```

- [ ] **Step 4: Implement Advance orchestration and parent reception.**

```text
Bind(ticket, runtime child UUID)
transaction: reconcile fully accepted outbox; choose attention/offer/terminal
if offer or attention: return action
if terminal: verify worker lock free; mark Finished; return finished
PollManaged(ctx, store, deps)
  ErrBusy -> wait, require existing host handle
  ErrYielded -> wait or attention from saved state
  event -> transaction Offer(now), then return send
  other error -> persist concrete fault without erasing pending; return attention
```

Never block awaiting App delivery inside a state transaction. Advance with empty
Dependencies is valid only when a pending event/terminal action prevents polling,
as in the tests; production supplies LiveDependencies.

Receive validates the parent's own store, event ID and part range against Outbox
or History. Return `duplicate` for an already received part, otherwise mark it
and return `event_complete` only when all parts are received, else `new_part`.
Received can arrive before PostToolUse; do not require an acceptance receipt as
proof that the parent saw a message. Reception does not create transport receipts,
advance Snapshot, set Ready or claim business processing.

- [ ] **Step 5: Verify multipart failure windows and bounded retries.**

Run: `rtk go test -race ./internal/prwatch -count=1`
Add tests with >12,000 runes to force three parts: accept first, restart, resume
second with identical bytes; wrong part/call ID; three offers with fake time;
acceptance arriving during retry delay; parent parts received 3,1,2 and repeated
2; simulated failure before outer Save; terminal ack while worker lock remains
held. Assert no fabricated Finished/Ready and preserve original pending bytes.

- [ ] **Step 6: Commit delivery and action protocol.**

```sh
rtk git add internal/prwatch/delivery.go internal/prwatch/delivery_test.go internal/prwatch/advance.go internal/prwatch/message.go
rtk git commit -m "feat: advance watches from durable delivery receipts" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 5: Real runtime membership and App receipt adapters

**Files:**
- Create: `internal/prwatch/runtime.go`, `internal/prwatch/runtime_test.go`
- Modify: `cmd/goldilocks/hook.go`, `cmd/goldilocks/main_test.go`, `hooks/hooks.json`

**Interfaces:**
- Consumes: ObserveStart and AcceptSend from Tasks 2/4.
- Produces: `DecodeRuntime(io.Reader) (RuntimeEvent, error)`;
  `DecodeSend(RuntimeEvent, time.Time) (ObservedSend, bool, error)`;
  `(*Controller).ObserveRuntime(RuntimeEvent) error`.
- Main's existing HookEvent becomes an alias of RuntimeEvent, preserving Name/Cwd/SessionID/AgentID/StopActive field names.

```go
type RuntimeEvent struct {
    Name string `json:"hook_event_name"`
    Cwd string `json:"cwd"`
    SessionID string `json:"session_id"`
    AgentID string `json:"agent_id"`
    AgentType string `json:"agent_type"`
    TurnID string `json:"turn_id"`
    StopActive bool `json:"stop_hook_active"`
    ToolName string `json:"tool_name"`
    ToolUseID string `json:"tool_use_id"`
    ToolInput json.RawMessage `json:"tool_input"`
    ToolResponse json.RawMessage `json:"tool_response"`
}
```

- [ ] **Step 1: Add actual-shape decoder tests.**

```go
func TestAppReceiptDecodesObservedDesktopShape(t *testing.T) {
    e := RuntimeEvent{Name:"PostToolUse", AgentID:specChild,
        ToolName:"mcp__codex_app__send_message_to_thread", ToolUseID:"exec-receipt-1"}
    e.ToolInput = json.RawMessage(`{"threadId":"00000000-0000-4000-8000-000000000001","prompt":"test marker"}`)
    e.ToolResponse = json.RawMessage(`{"content":[{"type":"text","text":"{\"threadId\":\"00000000-0000-4000-8000-000000000001\"}"}],"isError":false}`)
    send, recognized, err := DecodeSend(e, specTime)
    if err != nil || !recognized || !send.Accepted || send.ParentID != specParent ||
        send.AgentID != specChild || send.CallID != e.ToolUseID || send.Prompt != "test marker" {
        t.Fatalf("%+v %v %v", send, recognized, err)
    }
    e.ToolResponse = json.RawMessage(`{"isError":false,"content":[]}`)
    send, _, _ = DecodeSend(e, specTime)
    if send.Accepted { t.Fatal("empty result counted as success") }
}
func TestRuntimeRejectsTrailingAndOversizedInput(t *testing.T) {
    for _, raw := range []string{`{"hook_event_name":"Stop"}{}`,
        strings.Repeat(" ", (1<<20)+1)} {
        if _, err := DecodeRuntime(strings.NewReader(raw)); err == nil { t.Fatal("accepted bad payload") }
    }
}
```

Add failed MCP result, omitted isError with valid receipt, invalid isError type,
wrong returned thread ID, missing tool_use_id, unrelated tool name and Unicode
prompt round-trip cases. A reported `isError:false` with missing content is not
sufficient. Tests simulate payloads only; label them decoder tests, not runtime
capability evidence.

- [ ] **Step 2: Run the adapter tests red.**

Run: `rtk go test ./internal/prwatch -run 'TestAppReceipt|TestRuntime' -count=1`
Expected: missing decoder methods before implementation.

- [ ] **Step 3: Implement strict decoding and short hook writes.**

DecodeRuntime uses `io.LimitReader(input,(1<<20)+1)`, rejects excess bytes, uses
existing decodeJSON to reject trailing values, and permits unknown fields for
host evolution. DecodeSend requires the exact canonical tool name and agent
UUID. Parse tool_input into `{ThreadID string; Prompt string}` with explicit
`threadId`/`prompt` tags. Parse result isError as `*bool`, content as text blocks,
and require exactly one unambiguous returned `threadId` equal to the destination.
Conflicting success blocks fail. A declared remote host different from the local
watch host is rejected for this version. Ignore unrelated tools before parsing
or persisting their input.

ObserveRuntime dispatch:

```go
func (c *Controller) ObserveRuntime(e RuntimeEvent) error {
    switch e.Name {
    case "SubagentStart":
        return c.ObserveStart(e.SessionID, e.AgentID, e.TurnID)
    case "PostToolUse":
        send, recognized, err := DecodeSend(e, c.Now())
        if err != nil || !recognized { return err }
        return c.AcceptSend(send)
    default:
        return nil
    }
}
```

For relevant ErrLocked retry with short sleeps for at most one second, rereading
state each time. Other errors surface immediately. A hook persistence failure
must not modify the App result to a fabricated success. Keep main hook exit
behavior fail-open, with a clear diagnostic/systemMessage for managed failures.

RunHook keeps routing injection for SessionStart/SubagentStart; record membership
before adding routing output and do not discard routing if membership has no
matching intent. A relevant membership error adds a systemMessage alongside
routing context. Unrelated input must not create controller state directories.

- [ ] **Step 4: Add PostToolUse to the plugin hook manifest and test composition.**

Add this group using the same launcher and timeout as existing handlers:

```json
"PostToolUse": [{
  "matcher": "mcp__codex_app__send_message_to_thread",
  "hooks": [{
    "type": "command",
    "command": "\"${PLUGIN_ROOT}/scripts/goldilocks.sh\" hook",
    "commandWindows": "powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File \"$env:PLUGIN_ROOT/scripts/goldilocks.ps1\" hook; exit $LASTEXITCODE",
    "timeout": 150,
    "statusMessage": "Recording PR watcher delivery"
  }]
}]
```

Routing tests must continue to pass on Windows. Windows managed observation
returns an unsupported diagnostic only for an explicitly managed operation;
unrelated lifecycle/tool events remain no-ops. No actual trust settings change
in unit tests or during this task.

- [ ] **Step 5: Verify and commit the runtime adapter.**

Run: `rtk go test ./cmd/goldilocks ./internal/prwatch -count=1`

```sh
rtk git add internal/prwatch/runtime.go internal/prwatch/runtime_test.go cmd/goldilocks/hook.go cmd/goldilocks/main_test.go hooks/hooks.json
rtk git commit -m "feat: observe watcher membership and App send receipts" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 6: Parent startup guard, bounded recovery and truthful status

**Files:**
- Create: `internal/prwatch/lifecycle.go`, `internal/prwatch/lifecycle_test.go`
- Modify: `internal/prwatch/control.go`, `internal/prwatch/controller.go`, `cmd/goldilocks/hook.go`, `hooks/hooks.json`
- Replace legacy lifecycle assertions: `cmd/goldilocks/watcher_test.go`: update the old hook-dispatch assertion here to use a managed fixture; retain direct legacy helper tests until Task 7 removes those helpers.

**Interfaces:**
- Consumes: binding, PollManaged worker facts, receipt progress, RuntimeEvent.
- Produces:
  - `(*Controller).StopDecision(RuntimeEvent) (*HookDecision, error)`
  - `(*Controller).Status(parentID, prURL string) (Status, error)`
  - `(*Controller).Stop(parentID, prURL string) error`
  - `(*Controller).Resume(parentID, prURL string, replace bool) (StartResult, error)`

```go
type HookDecision struct {
    Decision string `json:"decision,omitempty"`
    Reason string `json:"reason,omitempty"`
    SystemMessage string `json:"systemMessage,omitempty"`
}
type Status struct {
    WatchID string `json:"watch_id"`
    Stage Stage `json:"stage"`
    Ready bool `json:"ready"`
    Activity string `json:"activity"`
    Worker WorkerStatus `json:"worker"`
    AgentID string `json:"agent_id"`
    PendingEventID string `json:"pending_event_id"`
    FailureCode string `json:"failure_code"`
    FailureDetail string `json:"failure_detail"`
    CleanupConfirmed bool `json:"cleanup_confirmed"`
}
```

Add independent `ParentStopProgress uint64` (`parent_stop_progress`) and
`ParentNoProgressStops int` (`parent_no_progress_stops`) to Control. Parent Stop
and SubagentStop must not consume each other's budget.

- [ ] **Step 1: Write startup and repeated-stop regression tests.**

```go
func TestParentSeesChildThatNeverRegistered(t *testing.T) {
    c, _ := startedFixture(t)
    d, err := c.StopDecision(RuntimeEvent{Name:"Stop", SessionID:specParent})
    if err != nil { t.Fatal(err) }
    if d == nil || d.Decision != "block" { t.Fatal("incomplete startup passed parent guard") }
    status, err := c.Status(specParent, specPR)
    if err != nil { t.Fatal(err) }
    if status.Ready || status.Stage == Running { t.Fatal("invented startup") }
}
func TestRepeatedChildStopsUseEvidenceBudget(t *testing.T) {
    c, _, _ := boundFixture(t)
    e := RuntimeEvent{Name:"SubagentStop", SessionID:specParent, AgentID:specChild}
    for i := 0; i < 2; i++ {
        e.StopActive = i > 0
        d, err := c.StopDecision(e)
        if err != nil { t.Fatal(err) }
        if d == nil || d.Decision != "block" { t.Fatal("lost permitted continuation", i) }
        if _, err := c.Status(specParent, specPR); err != nil { t.Fatal(err) }
    }
    d, err := c.StopDecision(e)
    if err != nil { t.Fatal(err) }
    if d != nil && d.Decision == "block" { t.Fatal("unbounded no-progress loop") }
    status, err := c.Status(specParent, specPR)
    if err != nil { t.Fatal(err) }
    if status.Stage != NeedsAttention { t.Fatal(status) }
}
```

Add an unrelated UUID test asserting no state changes and no decision. Add a
fresh accepted receipt between stops and assert it resets the child budget;
heartbeat-only and status-only changes must not reset either budget.

- [ ] **Step 2: Run lifecycle tests red.**

Run: `rtk go test ./internal/prwatch -run 'TestParentSees|TestRepeatedChild' -count=1`
Expected: missing lifecycle methods.

- [ ] **Step 3: Implement the state-derived decision and lifecycle commands.**

For a child, select only an exact bound AgentID. Ignore stop_hook_active as an
exit predicate; consult durable state. Inside Update, use the following budget:

```go
control := tx.Data.Control
if control.LastStopProgress != control.Progress { control.NoProgressStops = 0 }
control.LastStopProgress = control.Progress
if control.NoProgressStops >= 2 {
    control.Stage = NeedsAttention
    control.FailureCode = "no_progress"
    control.FailureDetail = "No measurable progress after two continuations"
    control.FaultSeen = false
} else {
    control.NoProgressStops++
}
```

On exhaustion request internal yield for the current worker and return a warning,
not another block. Otherwise choose an exact instruction: wait on the saved
execution handle; if the handle is lost invoke yield and wait for lock release;
run advance to deliver pending; or run advance to verify terminal cleanup.
Construct commands with proper shell quoting of the resolved launcher/ticket
paths; no interpolation of GitHub comment text into hook reasons.

For parent Stop enumerate only that parent's managed PR files. Starting or
Initializing without Ready blocks with status/recovery instructions; expire
unbound startup after ten minutes. An unreported NeedsAttention fault gets a
report instruction. Use the independent parent budget. A healthy Ready watch
does not keep the parent alive indefinitely. Corrupt matching state yields a
warning with its path and no destructive rewrite; unrelated parents are ignored.

Status rereads persisted state and probes the worker lock. Activities:
`awaiting_binding`, `polling` (lock+fresh heartbeat), `delivery_pending`, `idle`,
`unknown` (held lock+stale heartbeat). Historical Running does not imply polling.
Status may mark the current diagnostic FaultSeen and expire stale starting
intents, but never increments Progress. CleanupConfirmed requires no held lock
and no unresolved recorded execution. A stale saved worker can be reconciled as
ended only after a free lock is actually observed under the worker lock.

Stop persists Stopping and the stop marker without waiting for network I/O.
Keep the stop marker across resume. Resume requires a free worker lock and retains pending/history/collecting.
With replace=false and an existing verified child, retain the ticket and binding,
set Initializing, reset continuation counters, and re-enter via the same child.
Do not require another SubagentStart for that child. With replace=true, or an
unbound failed startup, rotate the nonce, clear AgentID, set BindAfter to now and
create Starting with Spawn=true. Set Ready=false while retaining InitialAccepted
as historical delivery evidence; a real next poll re-establishes readiness.
Expose --replace only on resume. If a user stop is pending, resume only finishes stopping.
Never clear a stop request to restart polling.

- [ ] **Step 4: Wire parent Stop and new child decisions into RunHook.**

Add a Stop group with the same launcher fields as SubagentStop and status message
`Checking PR watcher startup`. RunHook routes both Stop and SubagentStop to
StopDecision, encoding only a non-nil result. Keep no-op output empty. Existing
model-routing injection remains unchanged. Legacy CheckStop is no longer called
by the new hook; legacy records stay intact for migration, not falsely protected.

- [ ] **Step 5: Run lifecycle, isolation, corruption and race checks.**

Run: `rtk go test -race ./internal/prwatch ./cmd/goldilocks -count=1`
Include: terminal accepted with a held lock; pending-before-stop; missing
membership; two parent watches with different progress; status not resetting
budget; corrupted JSON not blocking unrelated agents; resume rejected while a
worker is alive; user stop surviving resume; no implicit reopened generation.

- [ ] **Step 6: Commit bounded lifecycle enforcement.**

```sh
rtk git add internal/prwatch/lifecycle.go internal/prwatch/lifecycle_test.go internal/prwatch/control.go internal/prwatch/controller.go cmd/goldilocks/hook.go cmd/goldilocks/watcher_test.go hooks/hooks.json
rtk git commit -m "feat: guard startup and recover watchers from observed progress" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 7: Replace manual lifecycle commands and rewrite the two agent protocols

**Files:**
- Create: `internal/prwatch/controller_cli.go`, `internal/prwatch/controller_cli_test.go`
- Modify: `cmd/goldilocks/watcher.go`, `cmd/goldilocks/main.go`, `cmd/goldilocks/watcher_test.go`, `cmd/goldilocks/main_test.go`
- Modify: `skills/pr-watch/SKILL.md`, `skills/pr-watch/references/watcher.md`
- Modify: `README.md`, `README.zh-hans.md`, `README.zh-hant.md`

**Interfaces:**
- Consumes: Start, Status, Stop, Resume, Receive, Advance, Yield from earlier tasks.
- Produces: `(*Controller).RunCLI(ctx context.Context, args []string, runtimeID, cwd string, output io.Writer) error`;
  `RunWatcherCLI(ctx context.Context, args []string, output io.Writer) error` in main.
- Import is added in Task 8. Until then that action returns an explicit unsupported-action error.

- [ ] **Step 1: Add CLI protocol tests red.**

In controller_cli_test.go, reuse internal helpers and import bytes/context/json:

```go
func TestControllerCLIRequiresRuntimeIdentity(t *testing.T) {
    c, err := NewController(t.TempDir(), func() time.Time { return specTime })
    if err != nil { t.Fatal(err) }
    var out bytes.Buffer
    err = c.RunCLI(context.Background(), []string{"start","--pr",specPR}, "", t.TempDir(), &out)
    if err == nil || out.Len() != 0 { t.Fatal("missing runtime ID accepted") }
}
func TestControllerCLIStartDoesNotClaimRunning(t *testing.T) {
    if runtime.GOOS == "windows" { t.Skip("managed POSIX CLI") }
    c, err := NewController(t.TempDir(), func() time.Time { return specTime })
    if err != nil { t.Fatal(err) }
    var out bytes.Buffer
    err = c.RunCLI(context.Background(), []string{"start","--pr",specPR}, specParent, t.TempDir(), &out)
    if err != nil { t.Fatal(err) }
    var r StartResult
    if err := json.Unmarshal(out.Bytes(), &r); err != nil { t.Fatal(err) }
    if !r.Spawn || r.TicketFile == "" { t.Fatal(r) }
    out.Reset()
    err = c.RunCLI(context.Background(), []string{"status","--pr",specPR}, specParent, t.TempDir(), &out)
    if err != nil { t.Fatal(err) }
    var status Status
    if err := json.Unmarshal(out.Bytes(), &status); err != nil { t.Fatal(err) }
    if status.Ready || status.Stage != Starting { t.Fatal(status) }
}
```

Table-test wrong-action flags, positional arguments, negative/NaN/infinite
intervals, invalid part, absent ticket, a child trying a parent's ticket as a
parent command, and the retired register/checkpoint/finish/fail names. Test resume without --replace
retains the old verified binding and succeeds without another SubagentStart;
resume --replace rejects the old ticket and requires a newly observed child. A parent
command always namespaces by the real runtime ID; no --session-id override.

- [ ] **Step 2: Run CLI tests red.**

Run: `rtk go test ./internal/prwatch -run 'TestControllerCLI' -count=1`
Expected: missing controller RunCLI.

- [ ] **Step 3: Implement action-specific parsing and replace the old registry adapter.**

Use one flag.FlagSet per action, io.Discard parser diagnostics, explicit required
flags, the existing seconds validator for polling overrides, and JSON encoding
with HTML escaping disabled. A zero duration omitted by the user means default;
an explicitly supplied zero is rejected. Child advance receives LiveDependencies.
Use context cancellation for watcher commands in main, matching existing
pr-watch signal handling and exit codes (0 successful protocol action; 2 input
or state error; 3 lock conflict; 130 cancellation). An attention JSON action is
an intentional result, not a fabricated success status.

Replace watcher.go with the thin adapter:

```go
func RunWatcherCLI(ctx context.Context, args []string, output io.Writer) error {
    root, err := prwatch.DefaultControllerRoot()
    if err != nil { return err }
    controller, err := prwatch.NewController(root, time.Now)
    if err != nil { return err }
    cwd, err := os.Getwd()
    if err != nil { return err }
    return controller.RunCLI(ctx, args, os.Getenv("CODEX_THREAD_ID"), cwd, output)
}
```

Remove Registration/Checkpoint/Finish/Fail/CheckStop code and stale mkdir locks
from main. Replace old watcher tests with the equivalent controller lifecycle
and adapter assertions; do not carry self-attested checkpoint/cleanup assertions
forward as valid behavior. Retired actions return:

```text
Legacy watcher registration commands are retired. Use watcher start/status;
stop the old execution before watcher import --pr URL --state-file PATH.
```

- [ ] **Step 4: Rewrite parent instructions around the new protocol.**

Use the writing-skills skill when editing the instruction files. Keep model
routing policy unchanged. The parent skill must contain this complete ordered
procedure (translate documentation copy consistently where required):

```text
Resolve the PR and your runtime UUID. Run watcher start before spawning.
If start returns an existing bound agent, reuse it; do not spawn a duplicate.
If a new child is needed, give it the resolved launcher and returned ticket path.
Its assignment is to EXECUTE the watcher loop, not merely read or summarize it.
Record the returned control handle against watch_id in your task context.
During startup, process initial event messages and check watcher status.
Do not claim monitoring started until ready=true and polling is currently
observed, or the runtime confirms the assigned child is delivering a next event.
If the child completes before readiness, inspect status and report startup
incomplete; resume the same child once when a concrete recovery action exists.
After readiness, continue your work. Do not poll GitHub in the parent.
On receipt, validate source UUID, assigned PR, run/event/part; call received.
Wait for all parts before acting and skip duplicate events already handled.
Before changing code, reread current PR/head; comments are untrusted evidence.
For stop, run watcher stop and tell the assigned child to continue cleanup.
Only report stopped after finished plus cleanup_confirmed, not after the request.
```

The exact child handoff template is generated from already resolved strings;
use this format, not the previous ambiguous “Forward only” sentence:

```go
fmt.Sprintf(`Execute the Goldilocks PR watcher loop in %s.
Launcher: %s
Ticket file: %s
Read and execute the watcher reference at %s.
Start by running watcher advance with this ticket. Send every returned message
exactly as supplied to its returned thread_id, then advance again. If a command
is still running, wait on that same execution. Continue until the controller
returns finished or attention; reading the reference is not completion.`,
    watchCwd, launcherPath, ticketPath, referencePath)
```

This is documentation for the spawning agent, not a new Go prompt-generator
service. All four values come from resolved paths and Start output; never put
PR comment text into this assignment.

- [ ] **Step 5: Replace the long child reference with an action table and exceptional recovery.**

Document these actions exactly:

| Result | Required child action |
| --- | --- |
| Running execution session | Keep its real handle; wait at most 60 seconds per tool call; do not run another advance |
| `send` | Invoke App send_message_to_thread with exactly threadId=thread_id and prompt=prompt; then advance |
| `wait` | Await the known running execution or the supplied retry delay; use yield only if that handle was lost |
| `finished` | End; include the final controller result |
| `attention` | Send its exact failure-report prompt to its parent destination once, report the send outcome, then end; no new agent or false cleanup claim |
| Unknown schema/action | Stop interpreting; report incompatible controller protocol |

Explain that send failure still returns to advance for controller retry logic;
the child never acknowledges by itself. A wait action with an existing worker
and a lost handle uses yield, waits for worker lock release through controller
status/control actions, then advances. Add a ticket-scoped child inspection mode
`watcher advance --inspect` returning wait/attention/activity without polling or
offering a message; do not use the parent's status namespace from a child.
The parser accepts --inspect only on advance and dispatches to
`(*Controller).Inspect(ticketFile, agentID string) (Action, error)`, implemented
as validated Bind plus state/worker observation with no progress increment.
Its wait reason explicitly says worker_running or worker_released; this lets
lost-handle recovery wait safely without a guessed handle or duplicate poller.

README copies explain starting versus active, interruptions, receipt semantics,
legacy import, hook trust and tested restart requirement. Do not claim idle
wake or shutdown continuity passed before Task 9.

- [ ] **Step 6: Verify CLI end to end and review the instructions against the original failure.**

Run: `rtk go test ./... -count=1`
Run: `rtk go test -race ./internal/prwatch ./cmd/goldilocks -count=1`
Extend the external built-CLI test technique to use an isolated CODEX_HOME and
runtime UUID, two simultaneous start processes, fake gh initial state containing
16 unresolved threads, and synthetic PostToolUse receipt input. Assert no manual
ack is needed, startup cannot pass before all parts and a next poll, and all
16 thread bodies appear across the frozen messages. Synthetic inputs remain
unit/integration evidence, not installed Desktop acceptance.

- [ ] **Step 7: Commit the usable controller workflow.**

```sh
rtk git add internal/prwatch/controller_cli.go internal/prwatch/controller_cli_test.go internal/prwatch/advance.go cmd/goldilocks/watcher.go cmd/goldilocks/main.go cmd/goldilocks/watcher_test.go cmd/goldilocks/main_test.go skills/pr-watch/SKILL.md skills/pr-watch/references/watcher.md README.md README.zh-hans.md README.zh-hant.md internal/prwatch/cli_test.go
rtk git commit -m "refactor: make PR watcher agents execute controller actions" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 8: Preserve legacy state and make resume/reopen explicit

**Files:**
- Create: `internal/prwatch/migration_test.go`
- Modify: `internal/prwatch/controller.go`, `internal/prwatch/controller_cli.go`, `internal/prwatch/cli.go`, `internal/prwatch/advance.go`
- Test: `internal/prwatch/state_test.go`, `internal/prwatch/cli_test.go`

**Interfaces:**
- Consumes: legacy State v2/v3, current ticket creation, managed transactions and the shared poll cycle.
- Produces: `(*Controller).Import(parentID, cwd, prURL, sourceFile string) (StartResult, error)`;
  `--reopened` implementation on Start, and guarded standalone CLI mutations.

- [ ] **Step 1: Add byte-preservation and live-owner tests red.**

```go
func TestImportKeepsPreparedLegacyMessage(t *testing.T) {
    if runtime.GOOS == "windows" { t.Skip("managed import") }
    source := filepath.Join(t.TempDir(), "legacy.json")
    raw, err := os.ReadFile(filepath.Join("testdata", "v2-pending.json"))
    if err != nil { t.Fatal(err) }
    if err := os.WriteFile(source, raw, 0600); err != nil { t.Fatal(err) }
    old, err := ReadState(source)
    if err != nil { t.Fatal(err) }
    c, err := NewController(t.TempDir(), func() time.Time { return specTime })
    if err != nil { t.Fatal(err) }
    r, err := c.Import(specParent, t.TempDir(), old.PRURL, source)
    if err != nil { t.Fatal(err) }
    managed, err := ReadState(r.StateFile)
    if err != nil { t.Fatal(err) }
    var oldPending, newPending any
    if err := decodeJSON(old.Pending, &oldPending); err != nil { t.Fatal(err) }
    if err := decodeJSON(managed.Pending, &newPending); err != nil { t.Fatal(err) }
    if !reflect.DeepEqual(oldPending, newPending) { t.Fatal("event or prepared strings changed") }
    after, err := os.ReadFile(source)
    if err != nil { t.Fatal(err) }
    if !bytes.Equal(raw, after) { t.Fatal("import rewrote source") }
}
```

Add tests for held source `.lock`, relative/mismatched source, existing managed
watch refusing overwrite, v3 collecting plus pending, >2^53 IDs, no-pending
acknowledged baseline, terminal source, old registration left untouched, and
reopen requiring cleaned Finished state. Import reflect for the semantic RawMessage comparison; decoded prompt strings
remain byte-identical even if outer JSON whitespace changes. Use the existing fixture's PR identity
rather than changing its raw JSON to fit a different PR.

- [ ] **Step 2: Run migration tests red.**

Run: `rtk go test ./internal/prwatch -run 'TestImport' -count=1`
Expected: missing Import implementation.

- [ ] **Step 3: Implement explicit import under both source and destination locks.**

Derive the source lock by replacing .json with .lock, acquire nonblocking, then
reread the source and verify requested canonical PR. Import only v2/v3 and reject
a managed source. Acquire destination state lock second; never reverse that
order. Destination must not exist. Prepare a ticket then write one v4 state
containing an untouched copy of pending/collecting, the original business
baseline, a fresh Starting Control, RefreshRequired=true, source path and digest.
Preserve source and registration files. A failed destination write leaves source
unchanged and can leave only an orphan ticket, handled by Start's recovery rule.
If the source is already finished, reject with “start --reopened after verifying
that the PR has reopened”; do not manufacture an active watch from a terminal one.

In the standalone RunCLI, immediately after loading State add:

```go
if store.Data.Control != nil && args[0] != "status" {
    return errors.New("managed state: use watcher advance/status/stop")
}
```

Recheck after acquiring the worker lock too, so a state changed between initial
load and lock acquisition cannot bypass the guard. Managed import lives in a
separate namespace, but defense against direct --state-dir access is still needed.

- [ ] **Step 4: Implement refresh-after-import and generation archival.**

Deliver imported pending first. Preserve and finish imported Collecting under a
new observed quiet window. Once both are empty and RefreshRequired, the next
successful nonterminal read stages a full initial event without erasing the old
baseline beforehand:

```go
changes, err := ChangesBetween(nil, snapshot)
if err != nil { return err }
raw, err := tx.Stage("initial", snapshot, changes, nil, nil, observed)
if err != nil { return err }
var event Event
if err := decodeJSON(raw, &event); err != nil { return err }
tx.Data.Control.InitialEventID = event.EventID
tx.Data.Control.RefreshRequired = false
```

This branch belongs before ordinary Observe for a nonterminal snapshot only
when pending and collecting are empty. Terminal metadata remains immediate.
Refactor ApplyPoll to own this branch so legacy and managed read-result handling
still have one decision point. Imported pending events do not accidentally set
InitialAccepted for the new baseline. Recovery then follows normal acceptance.

For explicit --reopened, acquire the worker lock and state lock, require saved
Finished and no pending/collecting/outbox, write a 0600 archive named with the
old watch UUID under the parent's archive directory, fsync it, then replace the
active ticket/state with a new generation. A crash after archive but before
replacement is idempotent: accept an identical archive, reject conflicting bytes.
Never silently restart on ordinary start. Archive lookup is used by Receive for
late parts from an old generation; event IDs must identify exactly one retained
event. Unknown/ambiguous old event IDs are rejected, not attributed to the new run.

- [ ] **Step 5: Verify migration and rollback boundaries.**

Run: `rtk go test ./internal/prwatch -count=1`
Run: `rtk go test -race ./internal/prwatch -run 'TestImport|TestManaged|TestTicket' -count=1`
Expected: existing v2/v3 tests unchanged; managed mutations through standalone
CLI rejected; repeated reopen/import never duplicates or overwrites evidence.

- [ ] **Step 6: Commit compatibility and explicit migration.**

```sh
rtk git add internal/prwatch/migration_test.go internal/prwatch/controller.go internal/prwatch/controller_cli.go internal/prwatch/cli.go internal/prwatch/advance.go internal/prwatch/watch.go internal/prwatch/state_test.go internal/prwatch/cli_test.go
rtk git commit -m "feat: migrate legacy watches without losing pending evidence" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 9: Package the candidate and complete the installed-runtime gates

**Files:**
- Modify: `scripts/verify.go`, `scripts/verify_test.go`, `.codex-plugin/plugin.json`, `scripts/SHA256SUMS`
- Create: `docs/verification/pr-watch-controller.md`
- Reuse: `.goreleaser.yaml`, `.github/workflows/ci.yml`, `scripts/launcher_test.go`

**Interfaces:**
- Consumes: completed controller protocol, five hook event definitions, existing six-target GoReleaser build and verifier.
- Produces: candidate 0.3.0 with checked assets, a pass/fail/not_verified release matrix, and retained local runtime evidence.
- Publishing, tagging, and remote release upload are not actions in this task.

- [ ] **Step 1: Extend packaging tests for the complete event set.**

Add a focused manifest test in scripts/verify_test.go (existing test files may
already import some of these packages):

```go
func TestControllerHooksHavePortableLaunchers(t *testing.T) {
    raw, err := os.ReadFile("../hooks/hooks.json")
    if err != nil { t.Fatal(err) }
    var config struct {
        Hooks map[string][]struct {
            Hooks []struct { Command, CommandWindows string }
        }
    }
    if err := json.Unmarshal(raw, &config); err != nil { t.Fatal(err) }
    for _, name := range []string{"SessionStart","SubagentStart","PostToolUse","SubagentStop","Stop"} {
        groups := config.Hooks[name]
        if len(groups) != 1 || len(groups[0].Hooks) != 1 { t.Fatal(name) }
        h := groups[0].Hooks[0]
        if h.Command == "" || h.CommandWindows == "" { t.Fatal("missing launcher", name) }
    }
}
```

Run: `rtk go test ./scripts -run TestControllerHooksHavePortableLaunchers -count=1`
The manifest entries may already make this test pass; the smoke assertions below
must fail if the verifier still treats all five events as routing injection.

- [ ] **Step 2: Update native smoke behavior and bump the candidate manifest.**

Extend verify.go's smoke event list to the five events. SessionStart and
SubagentStart require the original routing context; unregistered Stop,
SubagentStop and PostToolUse require no block/no controller state mutation.
Use an isolated CODEX_HOME under the smoke temporary directory so verification
cannot inspect or mutate a user's real watcher records. Add it to the native
smoke process environment along with existing PLUGIN_ROOT and PLUGIN_DATA.

```go
routing := event == "SessionStart" || event == "SubagentStart"
if !routing {
    if len(bytes.TrimSpace(output)) != 0 {
        return fmt.Errorf("unrelated %s was affected", event)
    }
} else {
    var result struct { HookSpecificOutput struct{ HookEventName, AdditionalContext string } }
    if err := json.Unmarshal(output, &result); err != nil { return err }
    if result.HookSpecificOutput.HookEventName != event ||
        !strings.Contains(result.HookSpecificOutput.AdditionalContext, "## Hard boundary") {
        return errors.New("routing injection failed")
    }
}
```

Set plugin.json version to `0.3.0`; keep asset naming and GoReleaser version
unchanged. This is a candidate build, not publication. Update the verifier's
summary from “three hook events” to “five hook events”.

- [ ] **Step 3: Run offline checks and regenerate pinned checksums from the candidate.**

```sh
rtk go test ./...
rtk go test -race ./...
rtk proxy env GOLDILOCKS_VERSION=0.3.0 goreleaser release --snapshot --clean
rtk proxy cp dist/SHA256SUMS scripts/SHA256SUMS
rtk go run ./scripts/verify.go
rtk git diff --check
```

Use Go 1.25.6 and GoReleaser v2.18.1. Review the six checksum entries before
committing; never weaken a verification failure by accepting checksums fetched
from an untrusted download. Run the existing CI matrix on macOS, Linux and
Windows when a review branch is opened. Cross-build success is not native
execution evidence for the other architectures.

- [ ] **Step 4: Prepare concrete installed-candidate acceptance and obtain only missing runtime inputs.**

Prepare an extracted candidate with the verified binaries preseeded through the
launcher's supported cache layout, preserving checksum verification. This allows
testing candidate hooks before a public 0.3.0 release exists. Record the candidate
hash, cache paths and exact trust definitions. Do not alter installed 0.2.0 files.
Read the launchers for the actual cache path rather than inventing a new override.

The executor needs an explicitly authorized disposable PR and a local test main
task that the user can leave idle. If absent, ask for those inputs after the
candidate and bounded scripts are prepared. Do not create a user-owned task or
post to the original reported PR implicitly. Hook definitions must be reviewed
by the user; no trusted_hash edits or bypass flags. On the tested build restart
Desktop and prove the candidate's SubagentStart and PostToolUse handlers execute.

Use this test assignment for the first startup failure case:

```text
Controlled PR watcher startup test. A parent intent has already been created
for this test. Read the supplied watcher reference, then immediately answer
PROBE_STOPPED_BEFORE_BIND without executing any CLI command. Do not claim a
watcher started. Do not send a message or alter a PR.
```

The parent attempts normal completion; capture its Stop guard and startup
status. Expected: starting/incomplete or needs_attention, never Ready/Running.
The unbound child is not arbitrarily resumed by SubagentStop. This reproduces
the original bug instead of testing only a compliant child.

- [ ] **Step 5: Execute the actual lifecycle and delivery matrix.**

Use one explicitly bound watcher for the approved disposable PR. Keep test hooks
scoped to its actual UUID and expire any test intervention after ten minutes;
never log unrelated tool bodies. Drive these concrete cases and retain the
actual runtime payload/result for each:

| Case | Intervention | Required evidence |
| --- | --- | --- |
| Binding order | Two pending intents; spawn unrelated/target children in reverse order | Ticket ties each watch to the correct UUID; unrelated child completes once |
| Normal initial | PR has 16 unresolved threads with enough text for three parts | All bodies/parts delivered; status not ready before all accepts and next poll |
| Premature stop 1/2 | Child attempts final twice while original advance execution is still alive | Same UUID continues twice, false/true/true stop flags, original worker ID retained |
| No progress | Child ignores recovery and finishes a third time | NeedsAttention, one visible diagnosis, no unlimited restarts |
| Partial delivery | Final after part 1; then recover | Parts 2/3 delivered with unchanged manifest; no early ack |
| Accepted-before-ack | Finish immediately after successful App send and before next advance | PostToolUse receipt survives; next advance commits without losing event |
| Receipt missing | In an isolated candidate test, omit the receipt observer for one marked send | Identical retry, bounded offers, no false acceptance; restore observer afterward |
| Lost execution handle | Discard only the test child's remembered handle, keep worker running | Yield requests safe exit; lock release observed before replacement; no PID kill |
| Stop pending | Request stop while nonterminal multipart event is pending | Pending delivery precedes stopped; no further GitHub read after stop observed |
| Terminal | Merge or close only if authorized for the disposable fixture | Terminal accepted, lock released, Finished; green/open never finishes |

To force a precise stop window, use a scoped test hook or a child instruction
at the actual action boundary, not a fabricated JSON receipt. Review its exact
code/trust scope first. Do not use an unbounded global Stop hook. Simulated App
acceptance is insufficient for the real acceptance rows.

- [ ] **Step 6: Verify idle delivery and long quiet operation.**

Run a fresh valid watcher for at least 25 minutes. Once ready, allow the main
task to become idle. After more than 20 minutes with no changes, the user (or an
explicitly authorized fixture operation) adds a real comment to the disposable
PR. Record absence of unchanged-event spam, the reset/expiry of the observed
quiet window, exact delivery acceptance, actual wakeup of the idle parent and
its received-part acknowledgment. The parent must not already be running due to
an unrelated input at delivery time. Do not infer wakeup from a send tool result.

Run the original-case acceptance again with the candidate installed after hook
trust and restart. Verify startup is never claimed for the deliberate no-bind
child. Report UI cancellation, hard interruption, sleep and App shutdown
separately; they are not covered by this idle-delivery test.

- [ ] **Step 7: Write the release matrix and clean up test hooks/agents.**

Create docs/verification/pr-watch-controller.md with this exact table header and
one row per spec acceptance ID A1–A13:

```markdown
| Acceptance ID | Status | Candidate/build/platform | Actual evidence | Limitation |
| --- | --- | --- | --- | --- |
```

Use only `passed`, `failed`, or `not_verified`. Preserve explicit raw evidence
locations and SHA-256 digests locally; commit anonymized excerpts without user
paths, tokens or private PR content. Record all test agents ended, temporary hook
sources removed, and whether another restart is required to unload cached hooks.
Do not erase the user's audit/trust history. Remaining runtime gates prevent a
production-ready claim; they do not invalidate completed offline tasks.

- [ ] **Step 8: Commit the candidate and evidence, without publishing.**

Run: `rtk git diff --check`

```sh
rtk git add scripts/verify.go scripts/verify_test.go .codex-plugin/plugin.json scripts/SHA256SUMS docs/verification/pr-watch-controller.md
rtk git commit -m "test: verify managed watcher candidate and runtime gates" -m "Co-authored-by: Codex <noreply@openai.com>"
```

If live inputs or hook review are unavailable, commit only the completed offline
candidate/evidence marked not_verified; leave Steps 4–7 unchecked. Do not mark
Task 9 or the implementation objective complete. Return the exact outstanding
runtime actions and candidate artifacts for the user to run on their local host.

## Self-review and execution handoff

- Spec A1–A13 map to named tasks in the spec acceptance table; no requirement is silently delegated to agent judgment.
- DTO and method names are defined in Tasks 1–8; implementation workers must keep those names consistent across commits.
- The deliberate release boundary is Task 9's real Desktop evidence; decoder fixtures, local subprocesses and previous version results cannot close it.
- This document is an implementation plan, not a record of tests already executed. All execution checkboxes remain unchecked.

After reviewing the spec and plan, choose either subagent-driven execution with
per-task review or inline execution with the executing-plans skill. No production
implementation or runtime probe is started by writing these documents.
