# Task 2 report: deterministic `::code-comment` rendering

## Implementation

- Added focused notification tests covering JSON-safe escaping, current `line` precedence over `originalLine`, P1/P2/P3 priority extraction, no-priority output, and outdated original-line Markdown fallback.
- Added a private formatter for active, non-outdated review-thread comments with a valid positive line target.
- Titles come from the first bold body heading, stripping the nested badge markup; otherwise the title falls back to `Review comment — @author`.
- String directive attributes are encoded with the standard-library JSON encoder (HTML escaping disabled to preserve review Markdown); `start` and `end` are emitted as numeric line values.
- Untargetable/outdated comments continue through the existing Markdown formatter. Existing CI, links, commit evidence, merge state, and footer behavior remain unchanged.

## Verification

Commands run from this worktree:

```text
go test ./internal/prwatch -run 'TestNotification(CodeCommentEscapesAndPrioritizes|OutdatedOriginalLineFallsBackToMarkdown|RendersLineAddressableReviewCommentAsCodeComment|FallsBackToMarkdownForUntargetableReviewComment)' -count=1
ok   github.com/baranwang/goldilocks/internal/prwatch  0.414s

go test ./internal/prwatch -count=1
ok   github.com/baranwang/goldilocks/internal/prwatch  14.078s

go test ./... -count=1
ok   github.com/baranwang/goldilocks/cmd/goldilocks  0.530s
ok   github.com/baranwang/goldilocks/internal/prwatch  15.545s
ok   github.com/baranwang/goldilocks/scripts  2.618s
```

## Concerns

The formatter intentionally targets only current review-thread comments; historical/removed evidence stays Markdown so `withObservedHead` and evidence context remain intact. Badge priority detection follows the existing `![P1 Badge]`/`![P2 Badge]`/`![P3 Badge]` markup convention.

## Review follow-up

Targeted directives now retain the original state heading, reviewed commit, and GitHub link as adjacent Markdown evidence. Comments within each thread are copied and sorted by path, line, author, and body before rendering, ensuring stable directive order without mutating source data.

Additional verification:

```text
go test ./internal/prwatch -count=1
ok   github.com/baranwang/goldilocks/internal/prwatch  14.162s

go test ./... -count=1
ok   github.com/baranwang/goldilocks/cmd/goldilocks  0.395s
ok   github.com/baranwang/goldilocks/internal/prwatch  14.097s
ok   github.com/baranwang/goldilocks/scripts  2.725s
```

## Review follow-up 2

Added regression coverage for numeric line ordering (2 before 10) across reversed input permutations and for same path/line/author/body comments distinguished by stable IDs. The deterministic sort key now includes the comment ID as its final tie-breaker.

Fresh verification:

```text
go test ./internal/prwatch -count=1
ok   github.com/baranwang/goldilocks/internal/prwatch  13.930s

go test ./... -count=1
ok   github.com/baranwang/goldilocks/cmd/goldilocks  0.475s
ok   github.com/baranwang/goldilocks/internal/prwatch  12.507s
ok   github.com/baranwang/goldilocks/scripts  2.509s
```
