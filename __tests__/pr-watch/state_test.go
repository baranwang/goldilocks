package prwatch_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	pw "github.com/baranwang/goldilocks/internal/prwatch"
)

const testPR = "https://github.com/example/project/pull/7"

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustStore(t *testing.T, dir string) *pw.Store {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX PR watch locking")
	}
	s, err := pw.NewStore(dir, testPR)
	check(t, err)
	return s
}

func emptySnapshot() pw.Snapshot {
	return pw.Snapshot{
		"head_sha": "abc123", "state": "OPEN", "draft": false,
		"mergeable": "MERGEABLE", "merge_state": "CLEAN", "checks": []any{},
		"comments": map[string]any{}, "reviews": map[string]any{}, "threads": map[string]any{},
	}
}

func decodeAny(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	check(t, decoder.Decode(&value))
	return value
}

func TestV2PendingAndManifestSurviveUpgrade(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	legacy, err := os.ReadFile(filepath.Join("testdata", "v2-pending.json"))
	check(t, err)
	check(t, os.WriteFile(s.Path, legacy, 0600))

	s = mustStore(t, dir)
	release, err := s.Lock(false)
	check(t, err)
	check(t, release())
	before, err := os.ReadFile(s.Path)
	check(t, err)
	if !bytes.Equal(before, legacy) {
		t.Fatal("status-style read rewrote v2")
	}

	release, err = s.Lock(true)
	check(t, err)
	expected := decodeAny(t, legacy).(map[string]any)["pending"]
	actual := decodeAny(t, s.Data.Pending)
	if s.Data.Version != 3 || !reflect.DeepEqual(actual, expected) {
		t.Fatal("pending changed")
	}
	check(t, release())

	s = mustStore(t, dir)
	raw, err := s.PendingEvent()
	check(t, err)
	var event pw.Event
	check(t, json.Unmarshal(raw, &event))
	if event.EventID != "legacy-event" {
		t.Fatal(event)
	}
}

func TestParsePRCanonicalIdentityAndRejectsUnsafeURLs(t *testing.T) {
	pr, err := pw.ParsePR("https://GitHub.COM/例子/项目._-/pull/7/?tab=checks#discussion")
	check(t, err)
	if pr.URL != "https://github.com/例子/项目._-/pull/7" || pr.Host != "github.com" ||
		pr.Owner != "例子" || pr.Repo != "项目._-" || pr.Number != "7" || pr.Key != "7014281cd1fd5716" {
		t.Fatalf("unexpected canonical PR: %#v", pr)
	}
	again, err := pw.ParsePR(testPR + "/?tab=checks#discussion")
	check(t, err)
	plain, err := pw.ParsePR(testPR)
	check(t, err)
	if again != plain {
		t.Fatalf("equivalent URLs differ: %#v %#v", again, plain)
	}
	encodedNoise, err := pw.ParsePR(testPR + "?tab=%E6%B5%8B#discussion%20thread")
	check(t, err)
	if encodedNoise != plain {
		t.Fatalf("encoded query/fragment affected identity: %#v %#v", encodedNoise, plain)
	}
	for _, otherURL := range []string{
		"https://github.com/example/other-project/pull/7",
		"https://github.example.com/example/project/pull/7",
		"https://github.com/example/project/pull/8",
	} {
		other, err := pw.ParsePR(otherURL)
		check(t, err)
		if other.Key == plain.Key {
			t.Fatalf("different PR reused identity: %s", otherURL)
		}
	}

	for _, raw := range []string{
		"http://github.com/example/project/pull/7",
		"https://user@github.com/example/project/pull/7",
		"https://:secret@github.com/example/project/pull/7",
		"https://github.com:443/example/project/pull/7",
		"https://github.com/%65xample/project/pull/7",
		"https://github.com/%E4%BE%8B%E5%AD%90/project/pull/7",
		"https://github.com/example/project/pull/0",
		"https://github.com/./project/pull/7",
		"https://github.com/example/../pull/7",
		"https://github.com/example/project/pull/7//",
		"https://github.com/example/project/pull/7;touch",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := pw.ParsePR(raw); err == nil {
				t.Fatal("accepted invalid PR URL")
			}
		})
	}
}

func TestReadStateValidatesShapeAndKeepsLargeNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := `{"version":3,"pr_url":"https://github.com/example/project/pull/7","snapshot":{"head_sha":"abc123","state":"OPEN","draft":false,"mergeable":"MERGEABLE","merge_state":"CLEAN","checks":[{"name":"tests"}],"comments":{"9007199254740993":{"id":9007199254740993,"body":"fix"}},"reviews":{},"threads":{"T1":{"comments":[{"body":"reply"}]}}},"pending":null,"last_ack":null,"error":null,"finished":false,"collecting":null}`
	check(t, os.WriteFile(path, []byte(raw), 0600))
	state, err := pw.ReadState(path)
	check(t, err)
	comment := state.Snapshot["comments"].(map[string]any)["9007199254740993"].(map[string]any)
	if id, ok := comment["id"].(json.Number); !ok || id.String() != "9007199254740993" {
		t.Fatalf("numeric identity lost: %#v", comment["id"])
	}

	validSnapshot := `{"head_sha":"abc123","state":"OPEN","draft":false,"mergeable":"MERGEABLE","merge_state":"CLEAN","checks":[],"comments":{},"reviews":{},"threads":{}}`
	for name, invalid := range map[string]string{
		"trailing JSON":      raw + `{}`,
		"unknown version":    strings.Replace(raw, `"version":3`, `"version":999`, 1),
		"missing collecting": strings.Replace(raw, `,"collecting":null`, "", 1),
		"noncanonical URL":   strings.Replace(raw, testPR, testPR+"/", 1),
		"missing snapshot":   strings.Replace(raw, `"head_sha":"abc123",`, "", 1),
		"bad comment":        fmt.Sprintf(`{"version":3,"pr_url":%q,"snapshot":%s,"pending":null,"last_ack":null,"error":null,"finished":false,"collecting":null}`, testPR, strings.Replace(validSnapshot, `"comments":{}`, `"comments":{"1":"bad"}`, 1)),
		"bad replies":        fmt.Sprintf(`{"version":3,"pr_url":%q,"snapshot":%s,"pending":null,"last_ack":null,"error":null,"finished":false,"collecting":null}`, testPR, strings.Replace(validSnapshot, `"threads":{}`, `"threads":{"T":{"comments":{}}}`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			check(t, os.WriteFile(path, []byte(invalid), 0600))
			if _, err := pw.ReadState(path); err == nil {
				t.Fatal("accepted invalid state")
			}
			got, err := os.ReadFile(path)
			check(t, err)
			if string(got) != invalid {
				t.Fatal("invalid state was modified")
			}
		})
	}
}

func TestReadStateRejectsInvalidPendingAndCollecting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	snapshot, err := json.Marshal(emptySnapshot())
	check(t, err)
	base := fmt.Sprintf(`{"version":3,"pr_url":%q,"snapshot":null,"pending":null,"last_ack":null,"error":null,"finished":false,"collecting":null}`, testPR)
	observation := fmt.Sprintf(`{"type":"update","observed_at":"2026-09-09T00:00:00Z","head_sha":"abc123","changes":{}}`)
	event := fmt.Sprintf(`{"source":"pr-watch","watcher_id":"db205ddecb4ad216","event_id":"event-1","type":"initial","pr_url":%q,"observed_at":"2026-09-09T00:00:00Z","head_sha":"abc123","changes":{}}`, testPR)
	for name, replacement := range map[string]string{
		"wrong watcher":  fmt.Sprintf(`{"event":%s,"snapshot":null,"error":null}`, strings.Replace(event, "db205ddecb4ad216", "wrong", 1)),
		"empty event":    fmt.Sprintf(`{"event":%s,"snapshot":null,"error":null}`, strings.Replace(event, `"event_id":"event-1"`, `"event_id":""`, 1)),
		"empty messages": fmt.Sprintf(`{"event":%s,"snapshot":null,"error":null,"messages":[]}`, event),
		"bad prompt":     fmt.Sprintf(`{"event":%s,"snapshot":null,"error":null,"messages":[{"prompt":7}]}`, event),
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(base, `"pending":null`, `"pending":`+replacement, 1)
			check(t, os.WriteFile(path, []byte(raw), 0600))
			if _, err := pw.ReadState(path); err == nil {
				t.Fatal("accepted invalid pending")
			}
		})
	}
	for name, collecting := range map[string]string{
		"no observations": fmt.Sprintf(`{"snapshot":%s,"kind":"update","observations":[],"recovered_error":null}`, snapshot),
		"bad time":        fmt.Sprintf(`{"snapshot":%s,"kind":"update","observations":[%s],"recovered_error":null}`, snapshot, strings.Replace(observation, "2026-09-09T00:00:00Z", "later", 1)),
		"bad changes":     fmt.Sprintf(`{"snapshot":%s,"kind":"update","observations":[%s],"recovered_error":null}`, snapshot, strings.Replace(observation, `"changes":{}`, `"changes":[]`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.Replace(base, `"collecting":null`, `"collecting":`+collecting, 1)
			check(t, os.WriteFile(path, []byte(raw), 0600))
			if _, err := pw.ReadState(path); err == nil {
				t.Fatal("accepted invalid collecting batch")
			}
		})
	}
}

func TestLockRevalidatesAndRemainsExclusive(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	_, err = mustStore(t, filepath.Dir(s.Path)).Lock(true)
	if !errors.Is(err, pw.ErrLocked) {
		t.Fatal("second owner acquired lock", err)
	}
	check(t, release())
	check(t, os.WriteFile(s.Path, []byte(`{"version":999}`), 0600))
	if release, err = s.Lock(true); err == nil {
		release()
		t.Fatal("stale constructor bypassed validation")
	}
	raw, err := os.ReadFile(s.Path)
	check(t, err)
	if string(raw) != `{"version":999}` {
		t.Fatal("corrupt state was replaced")
	}
}

func TestPendingSurvivesRestartUntilMatchingAck(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	release, err := s.Lock(true)
	check(t, err)
	snapshot := emptySnapshot()
	snapshot["comments"] = map[string]any{"9007199254740993": map[string]any{"id": json.Number("9007199254740993")}}
	first, err := s.Stage("initial", snapshot, pw.Changes{}, nil, nil, time.Date(2026, 9, 9, 0, 0, 0, 0, time.FixedZone("offset", 8*60*60)))
	check(t, err)
	check(t, release())

	s = mustStore(t, dir)
	release, err = s.Lock(true)
	check(t, err)
	again, err := s.PendingEvent()
	check(t, err)
	if !bytes.Equal(again, first) {
		t.Fatalf("pending event changed across restart:\n%s\n%s", first, again)
	}
	var event pw.Event
	check(t, json.Unmarshal(again, &event))
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(event.EventID) || event.ObservedAt != "2026-09-08T16:00:00Z" {
		t.Fatalf("bad event identity/time: %#v", event)
	}
	if err := s.Ack("wrong-event"); err == nil {
		t.Fatal("accepted wrong ack")
	}
	still, err := s.PendingEvent()
	check(t, err)
	if !bytes.Equal(still, first) {
		t.Fatal("wrong ack changed pending event")
	}
	check(t, s.Ack(event.EventID))
	check(t, s.Ack(event.EventID))
	if s.Data.Pending != nil || s.Data.LastAck == nil || *s.Data.LastAck != event.EventID || s.Data.Snapshot["head_sha"] != "abc123" {
		t.Fatalf("ack did not advance state: %#v", s.Data)
	}
	id := s.Data.Snapshot["comments"].(map[string]any)["9007199254740993"].(map[string]any)["id"]
	if number, ok := id.(json.Number); !ok || number.String() != "9007199254740993" {
		t.Fatalf("ack lost numeric identity: %#v", id)
	}
	check(t, release())
}

func TestStagePreservesExistingPreparedPending(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	_, err = s.Stage("initial", emptySnapshot(), pw.Changes{}, nil, nil, time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))
	check(t, err)
	envelope := decodeAny(t, s.Data.Pending).(map[string]any)
	envelope["messages"] = []any{map[string]any{"prompt": "prepared legacy JSON\n原文 \\ and quotes \""}}
	s.Data.Pending, err = json.Marshal(envelope)
	check(t, err)
	collecting := &pw.Batch{
		Snapshot: emptySnapshot(), Kind: "update",
		Observations: []pw.Observation{{Type: "update", ObservedAt: "2026-09-09T01:00:00Z", HeadSHA: "abc123", Changes: pw.Changes{}}},
	}
	s.Data.Collecting = collecting
	check(t, s.Save())
	frozenEvent, err := s.PendingEvent()
	check(t, err)
	frozenPending := append(json.RawMessage(nil), s.Data.Pending...)
	frozenFile, err := os.ReadFile(s.Path)
	check(t, err)

	next := emptySnapshot()
	next["head_sha"] = "new-head"
	returned, err := s.Stage("update", next, pw.Changes{"head_sha": map[string]any{"before": "abc123", "after": "new-head"}}, nil, collecting.Observations, time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC))
	check(t, err)
	afterFile, err := os.ReadFile(s.Path)
	check(t, err)
	if !bytes.Equal(returned, frozenEvent) || !bytes.Equal(s.Data.Pending, frozenPending) ||
		!bytes.Equal(afterFile, frozenFile) || !reflect.DeepEqual(s.Data.Collecting, collecting) {
		t.Fatal("later stage rewrote frozen pending state")
	}
	check(t, release())
}

func TestTerminalAckFinishesAndRemovesStopMarker(t *testing.T) {
	for _, kind := range []string{"merged", "closed", "stopped"} {
		t.Run(kind, func(t *testing.T) {
			s := mustStore(t, t.TempDir())
			release, err := s.Lock(true)
			check(t, err)
			check(t, os.WriteFile(s.StopPath, []byte("stop\n"), 0600))
			raw, err := s.Stage(kind, emptySnapshot(), pw.Changes{}, nil, nil, time.Now())
			check(t, err)
			if s.Data.Finished {
				t.Fatal("terminal event finished before ack")
			}
			var event pw.Event
			check(t, json.Unmarshal(raw, &event))
			check(t, s.Ack(event.EventID))
			if !s.Data.Finished {
				t.Fatal("terminal ack did not finish")
			}
			if _, err := os.Stat(s.StopPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("terminal ack did not remove stop marker", err)
			}
			check(t, release())
		})
	}
}

func TestErrorAckKeepsCollectingAndBaseline(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	baseline := emptySnapshot()
	observed := emptySnapshot()
	observed["head_sha"] = "new-head"
	observation := pw.Observation{Type: "update", ObservedAt: "2026-09-09T01:00:00Z", HeadSHA: "new-head", Changes: pw.Changes{"head_sha": map[string]any{"before": "abc123", "after": "new-head"}}}
	s.Data.Snapshot = baseline
	s.Data.Collecting = &pw.Batch{Snapshot: observed, Kind: "update", Observations: []pw.Observation{observation}}
	check(t, s.Save())
	failure := "GitHub access failed"
	raw, err := s.Stage("error", observed, pw.Changes{}, &failure, nil, time.Date(2026, 9, 9, 2, 0, 0, 0, time.UTC))
	check(t, err)
	var event pw.Event
	check(t, json.Unmarshal(raw, &event))
	check(t, s.Ack(event.EventID))
	if s.Data.Collecting == nil || s.Data.Snapshot["head_sha"] != "abc123" || s.Data.Error == nil || *s.Data.Error != failure {
		t.Fatalf("error ack lost batching state or advanced baseline: %#v", s.Data)
	}
	check(t, release())
}

func TestObservationBatchUsesLastTimeAndClearsCollecting(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	observed := emptySnapshot()
	observations := []pw.Observation{
		{Type: "update", ObservedAt: "2026-09-09T01:00:00Z", HeadSHA: "abc123", Changes: pw.Changes{}},
		{Type: "update", ObservedAt: "2026-09-09T02:00:00.123Z", HeadSHA: "abc123", Changes: pw.Changes{}},
	}
	s.Data.Collecting = &pw.Batch{Snapshot: observed, Kind: "update", Observations: observations}
	check(t, s.Save())
	raw, err := s.Stage("update", observed, pw.Changes{}, nil, observations, time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC))
	check(t, err)
	var event pw.Event
	check(t, json.Unmarshal(raw, &event))
	if event.ObservedAt != observations[1].ObservedAt || !reflect.DeepEqual(event.Observations, observations) || s.Data.Collecting != nil {
		t.Fatalf("batch was not finalized atomically: %#v %#v", event, s.Data.Collecting)
	}
	check(t, release())
	state, err := pw.ReadState(s.Path)
	check(t, err)
	if state.Collecting != nil {
		t.Fatal("saved collecting batch was not cleared")
	}
}

func TestAtomicSaveFailurePreservesOriginal(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	check(t, s.Save())
	before, err := os.ReadFile(s.Path)
	check(t, err)
	s.Data.Pending = json.RawMessage(`{`)
	if err := s.Save(); err == nil {
		t.Fatal("invalid pending JSON unexpectedly saved")
	}
	after, err := os.ReadFile(s.Path)
	check(t, err)
	if !bytes.Equal(after, before) {
		t.Fatal("failed save changed original state")
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(s.Path), ".watch-*.tmp"))
	check(t, err)
	if len(temps) != 0 {
		t.Fatalf("failed save left temporary files: %v", temps)
	}
	check(t, release())
}
