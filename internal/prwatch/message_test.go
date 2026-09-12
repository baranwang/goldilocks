package prwatch_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	pw "github.com/baranwang/goldilocks/internal/prwatch"
)

func TestMessagePartsPreserveUnicodeAndRestart(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	release, err := s.Lock(true)
	check(t, err)
	body := strings.Repeat("评论 😀 引号 \" 反斜杠 \\\n", 1000)
	snapshot := emptySnapshot()
	delta := pw.Changes{"comments": map[string]any{"1": map[string]any{"body": body, "author": "reviewer", "url": testPR}}}
	_, err = s.Stage("update", snapshot, delta, nil, []pw.Observation{{
		Type: "update", ObservedAt: "2026-09-09T00:00:00Z", HeadSHA: "abc123", Changes: delta,
	}}, time.Unix(0, 0))
	check(t, err)
	count, err := s.Prepare("CI log\nsource: " + testPR)
	check(t, err)
	if count < 2 {
		t.Fatal("not split")
	}
	parts := make([]pw.Message, count)
	var restored strings.Builder
	for i := range parts {
		parts[i], err = s.Message(i + 1)
		check(t, err)
		if !utf8.ValidString(parts[i].Prompt) {
			t.Fatal("invalid UTF-8")
		}
		_, chunk, ok := strings.Cut(parts[i].Prompt, "\n\n")
		if !ok {
			t.Fatal("missing header")
		}
		at := strings.LastIndex(chunk, "\n\nWatch:")
		if at < 0 {
			t.Fatal("missing footer")
		}
		restored.WriteString(chunk[:at])
	}
	plain := strings.ReplaceAll(restored.String(), "\n> ", "\n")
	if !strings.Contains(plain, body) || !strings.Contains(plain, "CI log\nsource: "+testPR) {
		t.Fatal("comment body or supplemental evidence lost")
	}
	check(t, release())

	s = mustStore(t, dir)
	release, err = s.Lock(true)
	check(t, err)
	defer func() { check(t, release()) }()
	again, err := s.Prepare("must not replace old text")
	check(t, err)
	if again != count {
		t.Fatal("manifest split changed")
	}
	for i, part := range parts {
		got, err := s.Message(i + 1)
		check(t, err)
		if got != part {
			t.Fatal("retry prompt changed")
		}
	}
	if _, err := s.Message(0); err == nil {
		t.Fatal("zero part accepted")
	}
	if _, err := s.Message(count + 1); err == nil {
		t.Fatal("out-of-range part accepted")
	}
}

func TestNotificationKeepsObservationAndRemovalEvidence(t *testing.T) {
	removed := map[string]any{
		"head_sha": "old-head",
		"comments": map[string]any{"comment-id": map[string]any{
			"body": "old comment body", "author": "alice", "url": testPR + "#issuecomment-1",
		}},
		"reviews": map[string]any{"review-id": map[string]any{
			"body": "old review body", "author": "bob", "state": "CHANGES_REQUESTED", "commit_id": "reviewed-head",
		}},
		"thread_comments": map[string]any{"live-thread-id": []any{map[string]any{
			"body": "removed reply body", "author": "carol", "path": "src/auth.go", "originalLine": json.Number("27"),
		}}},
	}
	first := pw.Changes{
		"checks": map[string]any{
			"before": []any{
				map[string]any{"name": "unchanged", "status": "COMPLETED", "conclusion": "SUCCESS", "url": testPR + "#unchanged"},
				map[string]any{"name": "removed", "status": "COMPLETED", "conclusion": "SUCCESS"},
			},
			"after": []any{
				map[string]any{"name": "unchanged", "status": "COMPLETED", "conclusion": "SUCCESS", "url": testPR + "#unchanged"},
				map[string]any{"name": "tests", "status": "COMPLETED", "conclusion": "FAILURE", "url": testPR + "#tests"},
			},
		},
		"comments_removed": []any{"comment-id"},
		"reviews_removed":  []any{"review-id"},
		"threads_removed":  []any{"resolved-thread-id"},
		"removed_evidence": removed,
	}
	second := pw.Changes{"comments": map[string]any{"new-id": map[string]any{
		"body": "new comment", "author": "dora", "url": testPR + "#issuecomment-2",
	}}}
	event := pw.Event{
		Source: "pr-watch", WatcherID: "watch", EventID: "event", Type: "merged", PRURL: testPR,
		ObservedAt: "2026-09-09T00:02:00Z", Changes: second,
		Observations: []pw.Observation{
			{Type: "update", ObservedAt: "2026-09-09T00:00:00Z", HeadSHA: "old-head", Changes: first},
			{Type: "update", ObservedAt: "2026-09-09T00:01:00Z", HeadSHA: "new-head", Changes: second},
		},
	}
	body, err := pw.NotificationBody(event)
	check(t, err)
	for _, evidence := range []string{
		"PR merged. Monitoring stopped.", "2026-09-09T00:00:00Z", "old-head", "2026-09-09T00:01:00Z", "new-head",
		"tests", "failure", testPR + "#tests", "removed: no longer reported", "old comment body", "old review body",
		"removed reply body", "last observed", "reviewed-head", "src/auth.go", "original line 27", "no longer unresolved", "new comment",
	} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(evidence)) {
			t.Fatalf("missing %q in:\n%s", evidence, body)
		}
	}
	for _, noise := range []string{"unchanged", "resolved-thread-id", "live-thread-id", `"changes"`, `"watcher_id"`} {
		if strings.Contains(body, noise) {
			t.Fatalf("unexpected %q in:\n%s", noise, body)
		}
	}
	if strings.Index(body, "PR merged. Monitoring stopped.") > strings.Index(body, "old comment body") {
		t.Fatal("terminal summary must precede accumulated evidence")
	}
}

func TestNotificationDescribesLegacyErrorRecoveryAndTerminalStates(t *testing.T) {
	failure := "GitHub access failed"
	for _, tc := range []struct {
		event pw.Event
		want  []string
	}{
		{pw.Event{Type: "error", Error: &failure, Changes: pw.Changes{}}, []string{"GitHub reads failed", failure}},
		{pw.Event{Type: "recovered", Changes: pw.Changes{}}, []string{"recovered", "monitoring continues"}},
		{pw.Event{Type: "closed", Changes: pw.Changes{}}, []string{"PR closed", "Monitoring stopped"}},
		{pw.Event{Type: "stopped", Changes: pw.Changes{}}, []string{"Monitoring stopped"}},
	} {
		body, err := pw.NotificationBody(tc.event)
		check(t, err)
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing %q", body, want)
			}
		}
		if strings.Contains(body, `"changes"`) {
			t.Fatal("raw event JSON leaked")
		}
	}
}

func TestNotificationRendersLineAddressableReviewCommentAsCodeComment(t *testing.T) {
	event := pw.Event{Type: "update", Changes: pw.Changes{
		"threads": map[string]any{
			"thread": map[string]any{"comments": []any{map[string]any{
				"path": "internal/auth.go", "line": 42, "author": "alice",
				"body": "**<sub><sub>![P1 Badge](https://img.shields.io/badge/P1-orange?style=flat)</sub></sub>  Validate authorization**\nFix auth \"quote\"\nSecond line",
			}}},
		}},
	}
	body, err := pw.NotificationBody(event)
	check(t, err)
	directives := []string{}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "::code-comment{") {
			directives = append(directives, line)
		}
	}
	if len(directives) != 1 {
		t.Fatalf("expected one standalone directive line, got %d:\n%s", len(directives), body)
	}
	directive := directives[0]
	want := `::code-comment{title="Validate authorization" body="**<sub><sub>![P1 Badge](https://img.shields.io/badge/P1-orange?style=flat)</sub></sub>  Validate authorization**\nFix auth \"quote\"\nSecond line" file="internal/auth.go" start=42 end=42 priority=1}`
	if directive != want {
		t.Fatalf("directive mismatch:\n got: %s\nwant: %s", directive, want)
	}
	if strings.Contains(body, "> Fix auth") || strings.Contains(body, "\n> Second line") {
		t.Fatalf("targeted comment was rendered as blockquote:\n%s", body)
	}
}

func TestNotificationFallsBackToMarkdownForUntargetableReviewComment(t *testing.T) {
	for _, comment := range []map[string]any{
		{"author": "alice", "body": "needs review\nwith detail"},
		{"author": "alice", "path": "internal/auth.go", "line": "not-a-line", "body": "needs review\nwith detail"},
		{"author": "alice", "path": "internal/auth.go", "line": 0, "body": "needs review\nwith detail"},
		{"author": "alice", "path": "internal/auth.go", "line": -1, "body": "needs review\nwith detail"},
	} {
		body, err := pw.NotificationBody(pw.Event{Type: "update", Changes: pw.Changes{"threads": map[string]any{
			"thread": map[string]any{"comments": []any{comment}},
		}}})
		check(t, err)
		if !strings.Contains(body, "**Review comment — @alice**") || !strings.Contains(body, "> needs review\n> with detail") {
			t.Fatalf("markdown fallback missing:\n%s", body)
		}
		if strings.Contains(body, "::code-comment") {
			t.Fatalf("untargetable comment emitted directive:\n%s", body)
		}
	}
}

func TestNotificationCodeCommentEscapesAndPrioritizes(t *testing.T) {
	for _, tc := range []struct {
		name, badge string
		priority    string
	}{
		{"p1", "P1", " priority=1"},
		{"p2", "P2", " priority=2"},
		{"p3", "P3", " priority=3"},
		{"none", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "**heading**\nquote \\\" and slash \\\\ and newline\nsecond"
			if tc.badge != "" {
				body = "**<sub><sub>![" + tc.badge + " Badge](https://img.shields.io/badge/" + tc.badge + "-orange)</sub></sub>  Heading**\n" + body
			}
			comment := map[string]any{"path": "x.go", "line": 7, "originalLine": 99, "author": "a", "body": body}
			out, err := pw.NotificationBody(pw.Event{Type: "update", Changes: pw.Changes{"threads": map[string]any{"t": map[string]any{"comments": []any{comment}}}}})
			check(t, err)
			if !strings.Contains(out, `file="x.go" start=7 end=7`+tc.priority+`}`) {
				t.Fatalf("missing location/priority: %s", out)
			}
			if !strings.Contains(out, `\"`) || !strings.Contains(out, `\\`) || !strings.Contains(out, `\n`) {
				t.Fatalf("body was not JSON escaped: %s", out)
			}
		})
	}
}

func TestNotificationOutdatedOriginalLineFallsBackToMarkdown(t *testing.T) {
	comment := map[string]any{"path": "x.go", "originalLine": 9, "author": "a", "body": "old"}
	out, err := pw.NotificationBody(pw.Event{Type: "update", Changes: pw.Changes{"threads": map[string]any{"t": map[string]any{"outdated": true, "comments": []any{comment}}}}})
	check(t, err)
	if strings.Contains(out, "::code-comment") || !strings.Contains(out, "original line 9") {
		t.Fatalf("outdated original line should use markdown: %s", out)
	}
}

func TestNotificationRendersEachTerminalObservationOnceWithEvidence(t *testing.T) {
	for _, tc := range []struct {
		kind, summary string
	}{
		{"merged", "PR merged. Monitoring stopped."},
		{"closed", "PR closed. Monitoring stopped."},
		{"stopped", "Monitoring stopped."},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			terminalChanges := pw.Changes{"comments": map[string]any{"terminal": map[string]any{
				"body": "terminal evidence", "author": "reviewer", "url": testPR,
			}}}
			if tc.kind != "stopped" {
				terminalChanges["state"] = map[string]any{"before": "OPEN", "after": strings.ToUpper(tc.kind)}
			}
			event := pw.Event{
				Type: tc.kind,
				Observations: []pw.Observation{
					{Type: "update", ObservedAt: "2026-09-09T00:00:00Z", HeadSHA: "old-head", Changes: pw.Changes{
						"comments": map[string]any{"first": map[string]any{"body": "preceding evidence", "author": "alice"}},
					}},
					{Type: tc.kind, ObservedAt: "2026-09-09T00:01:00Z", HeadSHA: "terminal-head", Changes: terminalChanges},
				},
			}
			body, err := pw.NotificationBody(event)
			check(t, err)
			if strings.Count(body, tc.summary) != 1 {
				t.Fatalf("terminal summary count: %q\n%s", tc.summary, body)
			}
			for _, want := range []string{
				"2026-09-09T00:00:00Z", "old-head", "preceding evidence",
				"2026-09-09T00:01:00Z", "terminal-head", "terminal evidence",
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("missing %q:\n%s", want, body)
				}
			}
			if tc.kind != "stopped" && !strings.Contains(body, "PR state: "+tc.kind) {
				t.Fatalf("terminal delta missing:\n%s", body)
			}
		})
	}
}

func TestPrepareKeepsExistingV2MessageManifest(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	fixture, err := os.ReadFile(filepath.Join("testdata", "v2-pending.json"))
	check(t, err)
	check(t, os.WriteFile(s.Path, fixture, 0600))
	s = mustStore(t, dir)
	release, err := s.Lock(true)
	check(t, err)
	defer func() { check(t, release()) }()
	count, err := s.Prepare("new evidence must be ignored")
	check(t, err)
	message, err := s.Message(1)
	check(t, err)
	if count != 1 || message.Prompt != "legacy JSON\n原文 \\ and quotes \"" {
		t.Fatalf("legacy manifest changed: %#v", message)
	}
}
