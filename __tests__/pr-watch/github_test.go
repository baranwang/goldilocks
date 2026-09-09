package prwatch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	pw "github.com/baranwang/goldilocks/internal/prwatch"
)

func githubFixture(t *testing.T, name string) any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "github", name+".json"))
	check(t, err)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	check(t, decoder.Decode(&value))
	return value
}

func TestTerminalMetadataSkipsAllFeedbackRequests(t *testing.T) {
	pr, err := pw.ParsePR(testPR)
	check(t, err)
	calls := 0
	read := func(ctx context.Context, args ...string) (any, error) {
		calls++
		if calls != 1 || args[0] != "pr" || args[1] != "view" {
			t.Fatal(args)
		}
		return map[string]any{"headRefOid": "abc123", "state": "MERGED", "isDraft": false,
			"mergeable": "UNKNOWN", "mergeStateStatus": "UNKNOWN"}, nil
	}
	snap, err := pw.Collect(context.Background(), pr, read, func() bool { return false })
	check(t, err)
	if calls != 1 || snap["state"] != "MERGED" {
		t.Fatal(calls, snap)
	}
	stopped := false
	calls = 0
	stopRead := func(ctx context.Context, args ...string) (any, error) {
		stopped = true
		return read(ctx, args...)
	}
	_, err = pw.Collect(context.Background(), pr, stopRead, func() bool { return stopped })
	if !errors.Is(err, pw.ErrStopped) || calls != 1 {
		t.Fatal("cancel after read ignored", err, calls)
	}
}

func TestCollectorPaginatesRESTThreadsAndRepliesAtObservedHead(t *testing.T) {
	pr, err := pw.ParsePR(testPR)
	check(t, err)
	responses := []struct {
		name string
		args []string
	}{
		{"metadata", []string{"pr", "view", testPR, "--json", "url,number,state,isDraft,mergeable,mergeStateStatus,headRefOid"}},
		{"comments", []string{"api", "--hostname", "github.com", "--paginate", "--slurp", "repos/example/project/issues/7/comments?per_page=100"}},
		{"reviews", []string{"api", "--hostname", "github.com", "--paginate", "--slurp", "repos/example/project/pulls/7/reviews?per_page=100"}},
		{"threads-1", nil},
		{"replies-2", nil},
		{"threads-2", nil},
		{"checks", []string{"api", "--hostname", "github.com", "--paginate", "--slurp", "repos/example/project/commits/abc123/check-runs?filter=latest&per_page=100"}},
		{"statuses", []string{"api", "--hostname", "github.com", "--paginate", "--slurp", "repos/example/project/commits/abc123/status?per_page=100"}},
	}
	call := 0
	read := func(ctx context.Context, args ...string) (any, error) {
		if call >= len(responses) {
			t.Fatalf("unexpected request: %q", args)
		}
		want := responses[call]
		call++
		if want.args != nil && !reflect.DeepEqual(args, want.args) {
			t.Fatalf("request %d:\n got %q\nwant %q", call, args, want.args)
		}
		joined := strings.Join(args, "\n")
		switch want.name {
		case "threads-1":
			if !strings.Contains(joined, "reviewThreads(first:100, after:$cursor)") ||
				!strings.Contains(joined, "id body url path line originalLine author { login }") ||
				strings.Count(joined, "pageInfo { hasNextPage endCursor }") != 2 || strings.Contains(joined, "cursor=") {
				t.Fatalf("bad first thread request: %q", args)
			}
		case "replies-2":
			if !containsArgs(args, "threadId=T1", "cursor=next-comment") ||
				!strings.Contains(joined, "id body url path line originalLine author { login }") ||
				strings.Count(joined, "pageInfo { hasNextPage endCursor }") != 1 {
				t.Fatalf("bad reply cursor: %q", args)
			}
		case "threads-2":
			if !containsArgs(args, "owner=example", "repo=project", "number=7", "cursor=next-thread") {
				t.Fatalf("bad thread cursor: %q", args)
			}
		}
		return githubFixture(t, want.name), nil
	}

	snapshot, err := pw.Collect(context.Background(), pr, read, func() bool { return false })
	check(t, err)
	if call != len(responses) {
		t.Fatalf("made %d requests, want %d", call, len(responses))
	}
	comments := snapshot["comments"].(map[string]any)
	reviews := snapshot["reviews"].(map[string]any)
	threads := snapshot["threads"].(map[string]any)
	checks := snapshot["checks"].([]any)
	if len(comments) != 2 || comments["9007199254740993"].(map[string]any)["body"] != "page 2" {
		t.Fatalf("REST pages or numeric IDs lost: %#v", comments)
	}
	if reviews["3"].(map[string]any)["state"] != "CHANGES_REQUESTED" {
		t.Fatalf("review fields lost: %#v", reviews)
	}
	if len(threads) != 2 || threads["RESOLVED"] != nil {
		t.Fatalf("thread pages or resolved filtering wrong: %#v", threads)
	}
	t1 := threads["T1"].(map[string]any)["comments"].([]any)
	if len(t1) != 2 || t1[1].(map[string]any)["body"] != "nested page" {
		t.Fatalf("reply pagination wrong: %#v", t1)
	}
	if len(checks) != 3 {
		t.Fatalf("CI pages not flattened: %#v", checks)
	}
	for _, raw := range checks {
		check := raw.(map[string]any)
		if check["status"] != strings.ToUpper(check["status"].(string)) {
			t.Fatalf("check status not normalized: %#v", check)
		}
	}
}

func containsArgs(args []string, values ...string) bool {
	for _, value := range values {
		found := false
		for _, arg := range args {
			if arg == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func TestInitialBaselineAndEditedCommentDelta(t *testing.T) {
	before := emptySnapshot()
	before["comments"] = map[string]any{"9007199254740993": map[string]any{"id": "9007199254740993", "body": "old", "url": testPR}}
	before["threads"] = map[string]any{"T1": map[string]any{"outdated": false, "comments": []any{
		map[string]any{"id": "a", "body": "first"}, map[string]any{"id": "same", "body": "unchanged"},
	}}}
	initial, err := pw.ChangesBetween(nil, before)
	check(t, err)
	if _, exists := initial["comments"]; exists {
		t.Fatal("old top-level comments replayed")
	}
	after, err := pw.CloneSnapshot(before)
	check(t, err)
	after["comments"].(map[string]any)["9007199254740993"].(map[string]any)["body"] = "edited"
	afterReplies := after["threads"].(map[string]any)["T1"].(map[string]any)["comments"].([]any)
	afterReplies[0].(map[string]any)["body"] = "edited reply"
	after["threads"].(map[string]any)["T1"].(map[string]any)["comments"] = append(afterReplies, map[string]any{"id": "b", "body": "new reply"})
	delta, err := pw.ChangesBetween(before, after)
	check(t, err)
	raw, err := json.Marshal(delta)
	check(t, err)
	if !strings.Contains(string(raw), "9007199254740993") || !strings.Contains(string(raw), "edited") {
		t.Fatal(string(raw))
	}
	replies := delta["threads"].(map[string]any)["T1"].(map[string]any)["comments"].([]any)
	if len(replies) != 2 || replies[0].(map[string]any)["body"] != "edited reply" || replies[1].(map[string]any)["body"] != "new reply" {
		t.Fatalf("thread delta replayed unchanged replies or lost edits: %#v", replies)
	}
	same, err := pw.ChangesBetween(after, after)
	check(t, err)
	if len(same) != 0 {
		t.Fatal("duplicate observation", same)
	}
}

func TestInitialReviewsKeepLatestRequestedDecisionPerAuthor(t *testing.T) {
	snapshot := emptySnapshot()
	snapshot["reviews"] = map[string]any{
		"1": map[string]any{"id": "1", "author": "alice", "state": "CHANGES_REQUESTED", "submitted_at": "2026-09-09T00:00:00Z"},
		"2": map[string]any{"id": "2", "author": "alice", "state": "COMMENTED", "submitted_at": "2026-09-09T01:00:00Z"},
		"3": map[string]any{"id": "3", "author": "bob", "state": "CHANGES_REQUESTED", "submitted_at": "2026-09-09T00:00:00Z"},
		"4": map[string]any{"id": "4", "author": "bob", "state": "APPROVED", "submitted_at": "2026-09-09T02:00:00Z"},
	}
	changes, err := pw.ChangesBetween(nil, snapshot)
	check(t, err)
	reviews := changes["reviews"].(map[string]any)
	if len(reviews) != 1 || reviews["1"] == nil {
		t.Fatalf("latest decision filtering wrong: %#v", reviews)
	}
}

func TestRemovedEvidenceKeepsOldHeadAndOnlyRemovedRepliesFromLiveThreads(t *testing.T) {
	before := emptySnapshot()
	before["head_sha"] = "old-head"
	before["comments"] = map[string]any{"2": map[string]any{"id": "2", "body": "gone"}}
	before["reviews"] = map[string]any{"3": map[string]any{"id": "3", "body": "gone review"}}
	before["threads"] = map[string]any{
		"LIVE": map[string]any{"outdated": false, "comments": []any{
			map[string]any{"id": "b", "body": "removed second"}, map[string]any{"id": "a", "body": "removed first"}, map[string]any{"id": "c", "body": "still here"},
		}},
		"RESOLVED": map[string]any{"outdated": false, "comments": []any{map[string]any{"id": "z", "body": "do not infer deletion"}}},
	}
	after := emptySnapshot()
	after["head_sha"] = "new-head"
	after["threads"] = map[string]any{
		"LIVE": map[string]any{"outdated": true, "comments": []any{map[string]any{"id": "c", "body": "still here"}}},
	}

	changes, err := pw.ChangesBetween(before, after)
	check(t, err)
	if !reflect.DeepEqual(changes["comments_removed"], []string{"2"}) ||
		!reflect.DeepEqual(changes["reviews_removed"], []string{"3"}) ||
		!reflect.DeepEqual(changes["threads_removed"], []string{"RESOLVED"}) {
		t.Fatalf("removed IDs wrong: %#v", changes)
	}
	evidence := changes["removed_evidence"].(map[string]any)
	if evidence["head_sha"] != "old-head" || evidence["comments"].(map[string]any)["2"] == nil || evidence["reviews"].(map[string]any)["3"] == nil {
		t.Fatalf("top-level removed evidence lost: %#v", evidence)
	}
	replies := evidence["thread_comments"].(map[string]any)["LIVE"].([]any)
	if len(replies) != 2 || replies[0].(map[string]any)["id"] != "a" || replies[1].(map[string]any)["id"] != "b" {
		t.Fatalf("removed replies not deterministic: %#v", replies)
	}
	if strings.Contains(string(mustJSON(t, evidence)), "do not infer deletion") {
		t.Fatal("resolved thread replies were misreported as deleted")
	}
	threadDelta := changes["threads"].(map[string]any)["LIVE"].(map[string]any)
	if threadDelta["outdated"] != true || len(threadDelta["comments"].([]any)) != 0 {
		t.Fatalf("thread status delta wrong: %#v", threadDelta)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	check(t, err)
	return raw
}

func TestCloneSnapshotKeepsJSONNumbersAndValidatesInputs(t *testing.T) {
	snapshot := emptySnapshot()
	snapshot["comments"] = map[string]any{"9007199254740993": map[string]any{"id": json.Number("9007199254740993")}}
	clone, err := pw.CloneSnapshot(snapshot)
	check(t, err)
	id := clone["comments"].(map[string]any)["9007199254740993"].(map[string]any)["id"]
	if number, ok := id.(json.Number); !ok || number.String() != "9007199254740993" {
		t.Fatalf("numeric identity lost: %#v", id)
	}
	clone["head_sha"] = "changed"
	if snapshot["head_sha"] != "abc123" {
		t.Fatal("clone shares storage")
	}
	if _, err := pw.ChangesBetween(snapshot, pw.Snapshot{"state": "OPEN"}); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
}

func TestChangesIgnoreCheckOrderingFromOlderSnapshots(t *testing.T) {
	before := emptySnapshot()
	after := emptySnapshot()
	alpha := map[string]any{"name": "alpha", "status": "COMPLETED", "conclusion": "SUCCESS", "url": "https://example.com/路径?a=<b>"}
	beta := map[string]any{"name": "beta", "status": "COMPLETED", "conclusion": "SUCCESS", "url": nil}
	before["checks"] = []any{beta, alpha}
	after["checks"] = []any{alpha, beta}
	changes, err := pw.ChangesBetween(before, after)
	check(t, err)
	if _, exists := changes["checks"]; exists {
		t.Fatalf("check order created a false change: %#v", changes)
	}
}

func TestCollectorClassifiesReadAndResponseFailures(t *testing.T) {
	pr, err := pw.ParsePR(testPR)
	check(t, err)
	boom := errors.New("temporary")
	_, err = pw.Collect(context.Background(), pr, func(context.Context, ...string) (any, error) { return nil, boom }, func() bool { return false })
	var readErr *pw.ReadError
	if !errors.As(err, &readErr) || !errors.Is(err, boom) {
		t.Fatalf("retryable read was not wrapped: %v", err)
	}
	_, err = pw.Collect(context.Background(), pr, func(context.Context, ...string) (any, error) { return map[string]any{"state": "OPEN"}, nil }, func() bool { return false })
	if !errors.As(err, &readErr) {
		t.Fatalf("malformed response was not retryable: %v", err)
	}
	calls := 0
	_, err = pw.Collect(context.Background(), pr, func(context.Context, ...string) (any, error) {
		calls++
		if calls == 1 {
			return githubFixture(t, "metadata"), nil
		}
		return []any{[]any{map[string]any{"id": json.Number("1"), "html_url": testPR, "url": true}}}, nil
	}, func() bool { return false })
	if !errors.As(err, &readErr) || calls != 2 {
		t.Fatalf("malformed optional feedback field was accepted: %v", err)
	}
	_, err = pw.Collect(context.Background(), pr, func(context.Context, ...string) (any, error) {
		return nil, context.DeadlineExceeded
	}, func() bool { return false })
	if !errors.As(err, &readErr) {
		t.Fatalf("reader timeout was confused with caller cancellation: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = pw.Collect(cancelled, pr, func(context.Context, ...string) (any, error) { t.Fatal("read after cancellation"); return nil, nil }, func() bool { return false })
	if !errors.Is(err, context.Canceled) || errors.As(err, &readErr) {
		t.Fatalf("caller cancellation was classified as read failure: %v", err)
	}
}

func TestReadGHUsesNumbersAndClassifiesProcessFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PR watcher network support is macOS/Linux")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	script := strings.Replace(`#!/bin/sh
case "$1" in
  ok) printf '{"id":9007199254740993}' ;;
  graphql) printf '{"errors":[{"message":"denied"}]}' ;;
  trailing) printf '{}{}' ;;
  fail) printf 'github unavailable' >&2; exit 1 ;;
  longfail) printf '__LONG_DIAGNOSTIC__' >&2; exit 1 ;;
esac
`, "__LONG_DIAGNOSTIC__", strings.Repeat("界", 1205), 1)
	check(t, os.WriteFile(path, []byte(script), 0700))
	t.Setenv("PATH", dir)
	value, err := pw.ReadGH(context.Background(), "ok")
	check(t, err)
	if value.(map[string]any)["id"].(json.Number).String() != "9007199254740993" {
		t.Fatal(value)
	}
	for _, arg := range []string{"graphql", "trailing", "fail"} {
		_, err := pw.ReadGH(context.Background(), arg)
		var readErr *pw.ReadError
		if !errors.As(err, &readErr) {
			t.Fatalf("%s not classified as retryable: %v", arg, err)
		}
	}
	_, err = pw.ReadGH(context.Background(), "longfail")
	var longErr *pw.ReadError
	if !errors.As(err, &longErr) || len([]rune(strings.TrimPrefix(err.Error(), "GitHub read failed: "))) != 1200 {
		t.Fatalf("diagnostic was not capped by rune: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	_, err = pw.ReadGH(context.Background(), "ok")
	var readErr *pw.ReadError
	if err == nil || errors.As(err, &readErr) {
		t.Fatalf("missing gh was wrapped as retryable: %v", err)
	}
}
