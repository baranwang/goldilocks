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
