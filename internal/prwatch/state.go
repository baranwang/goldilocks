package prwatch

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
)

const prURLHelp = "use an HTTPS PR URL: https://HOST/OWNER/REPO/pull/NUMBER"

func ParsePR(raw string) (PR, error) {
	parsed, err := url.Parse(raw)
	if err != nil || escapedIdentityPath(raw) || parsed.Scheme != "https" ||
		parsed.Hostname() == "" || parsed.User != nil || hasExplicitPort(parsed.Host) {
		return PR{}, errors.New(prURLHelp)
	}
	path := parsed.Path
	if strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	parts := strings.Split(path, "/")
	if len(parts) != 5 || parts[0] != "" || parts[3] != "pull" ||
		!validName(parts[1]) || !validName(parts[2]) || !validNumber(parts[4]) {
		return PR{}, errors.New(prURLHelp)
	}
	host := strings.ToLower(parsed.Hostname())
	urlHost := host
	if strings.Contains(host, ":") {
		urlHost = "[" + host + "]"
	}
	canonical := "https://" + urlHost + "/" + parts[1] + "/" + parts[2] + "/pull/" + parts[4]
	sum := sha256.Sum256([]byte(canonical))
	return PR{
		URL: canonical, Host: host, Owner: parts[1], Repo: parts[2], Number: parts[4],
		Key: fmt.Sprintf("%x", sum)[:16],
	}, nil
}

func escapedIdentityPath(raw string) bool {
	if end := strings.IndexAny(raw, "?#"); end >= 0 {
		raw = raw[:end]
	}
	authority := strings.Index(raw, "://")
	if authority < 0 {
		return false
	}
	path := strings.IndexByte(raw[authority+3:], '/')
	return path >= 0 && strings.Contains(raw[authority+3+path:], "%")
}

func hasExplicitPort(host string) bool {
	if strings.HasPrefix(host, "[") {
		end := strings.IndexByte(host, ']')
		return end < 0 || end+1 != len(host)
	}
	return strings.Contains(host, ":")
}

func validName(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if r != '_' && r != '.' && r != '-' && !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			return false
		}
	}
	return true
}

func validNumber(value string) bool {
	if value == "" || value[0] < '1' || value[0] > '9' {
		return false
	}
	for _, r := range value[1:] {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func ReadState(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	return decodeState(raw)
}

func decodeState(raw []byte) (State, error) {
	fields, err := objectFields(raw)
	if err != nil {
		return State{}, err
	}
	if err := require(fields, "version", "pr_url", "snapshot", "pending", "last_ack", "error", "finished"); err != nil {
		return State{}, err
	}
	var state State
	if err := decodeJSON(raw, &state); err != nil {
		return State{}, err
	}
	switch state.Version {
	case 2:
		if _, exists := fields["control"]; exists {
			return State{}, errors.New("control requires state version 4")
		}
	case 3:
		if err := require(fields, "collecting"); err != nil {
			return State{}, err
		}
		if _, exists := fields["control"]; exists {
			return State{}, errors.New("control requires state version 4")
		}
	case 4:
		if err := require(fields, "collecting", "control"); err != nil {
			return State{}, err
		}
		if isNull(fields["control"]) {
			return State{}, errors.New("state version 4 requires non-null control")
		}
	default:
		return State{}, fmt.Errorf("unsupported state version %d", state.Version)
	}
	pr, err := ParsePR(state.PRURL)
	if err != nil || pr.URL != state.PRURL {
		return State{}, errors.New("state pr_url is not canonical")
	}
	if err := validateSnapshotRaw(fields["snapshot"], true); err != nil {
		return State{}, fmt.Errorf("snapshot: %w", err)
	}
	if err := validateNullableString(fields["last_ack"]); err != nil {
		return State{}, fmt.Errorf("last_ack: %w", err)
	}
	if err := validateNullableString(fields["error"]); err != nil {
		return State{}, fmt.Errorf("error: %w", err)
	}
	if isNull(fields["finished"]) {
		return State{}, errors.New("finished must be a bool")
	}
	var finished bool
	if err := decodeJSON(fields["finished"], &finished); err != nil {
		return State{}, fmt.Errorf("finished: %w", err)
	}
	if !isNull(fields["pending"]) {
		if err := validatePending(fields["pending"], pr); err != nil {
			return State{}, fmt.Errorf("pending: %w", err)
		}
	}
	if state.Version >= 3 && !isNull(fields["collecting"]) {
		if err := validateCollecting(fields["collecting"]); err != nil {
			return State{}, fmt.Errorf("collecting: %w", err)
		}
	}
	if err := validateControl(state); err != nil {
		return State{}, err
	}
	return state, nil
}

func decodeJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func objectFields(raw []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := decodeJSON(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("expected JSON object")
	}
	return fields, nil
}

func require(fields map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing %s", name)
		}
	}
	return nil
}

func isNull(raw []byte) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func validateNullableString(raw []byte) error {
	if isNull(raw) {
		return nil
	}
	var value string
	return decodeJSON(raw, &value)
}

func validateSnapshotRaw(raw []byte, nullable bool) error {
	if isNull(raw) {
		if nullable {
			return nil
		}
		return errors.New("must be an object")
	}
	var snapshot Snapshot
	if err := decodeJSON(raw, &snapshot); err != nil {
		return err
	}
	return validateSnapshot(snapshot)
}

func validateSnapshot(snapshot Snapshot) error {
	if snapshot == nil {
		return errors.New("must be an object")
	}
	for _, field := range []string{"head_sha", "state", "mergeable", "merge_state"} {
		if _, ok := snapshot[field].(string); !ok {
			return fmt.Errorf("%s must be a string", field)
		}
	}
	if _, ok := snapshot["draft"].(bool); !ok {
		return errors.New("draft must be a bool")
	}
	checks, ok := snapshot["checks"].([]any)
	if !ok {
		return errors.New("checks must be an array")
	}
	for _, raw := range checks {
		check, ok := raw.(map[string]any)
		if !ok {
			return errors.New("each check must be an object")
		}
		for _, field := range []string{"name", "status", "conclusion", "url"} {
			if err := optionalString(check, field); err != nil {
				return fmt.Errorf("check %s: %w", field, err)
			}
		}
	}
	for _, field := range []string{"comments", "reviews"} {
		items, ok := snapshot[field].(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", field)
		}
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("each %s value must be an object", field)
			}
			if err := validateEvidence(item); err != nil {
				return fmt.Errorf("%s: %w", field, err)
			}
		}
	}
	threads, ok := snapshot["threads"].(map[string]any)
	if !ok {
		return errors.New("threads must be an object")
	}
	for _, raw := range threads {
		thread, ok := raw.(map[string]any)
		if !ok {
			return errors.New("each thread must be an object")
		}
		if outdated, exists := thread["outdated"]; exists {
			if _, ok := outdated.(bool); !ok {
				return errors.New("thread outdated must be a bool")
			}
		}
		replies, ok := thread["comments"].([]any)
		if !ok {
			return errors.New("thread comments must be an array")
		}
		for _, raw := range replies {
			reply, ok := raw.(map[string]any)
			if !ok {
				return errors.New("each thread comment must be an object")
			}
			if err := validateEvidence(reply); err != nil {
				return fmt.Errorf("thread comment: %w", err)
			}
		}
	}
	return nil
}

func validateEvidence(value map[string]any) error {
	for _, field := range []string{"body", "url", "author", "path", "state", "commit_id", "submitted_at", "diffSide"} {
		if err := optionalString(value, field); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
	}
	if id, ok := value["id"]; ok && id != nil {
		if _, stringID := id.(string); !stringID {
			if _, numberID := id.(json.Number); !numberID {
				return errors.New("id must be a string or number")
			}
		}
	}
	for _, field := range []string{"line", "originalLine"} {
		if number, ok := value[field]; ok && number != nil {
			if _, ok := number.(json.Number); !ok {
				return fmt.Errorf("%s must be a number", field)
			}
		}
	}
	return nil
}

func optionalString(value map[string]any, field string) error {
	raw, ok := value[field]
	if !ok || raw == nil {
		return nil
	}
	if _, ok := raw.(string); !ok {
		return errors.New("must be a string")
	}
	return nil
}

func validatePending(raw []byte, pr PR) error {
	fields, err := objectFields(raw)
	if err != nil {
		return err
	}
	if err := require(fields, "event", "snapshot", "error"); err != nil {
		return err
	}
	eventFields, err := objectFields(fields["event"])
	if err != nil {
		return fmt.Errorf("event: %w", err)
	}
	if err := require(eventFields, "source", "watcher_id", "event_id", "type", "pr_url", "observed_at", "head_sha", "changes"); err != nil {
		return fmt.Errorf("event: %w", err)
	}
	var event Event
	if err := decodeJSON(fields["event"], &event); err != nil {
		return fmt.Errorf("event: %w", err)
	}
	if event.Source != "pr-watch" || event.PRURL != pr.URL || event.WatcherID != pr.Key || event.EventID == "" || event.Type == "" {
		return errors.New("event identity is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.ObservedAt); err != nil {
		return errors.New("event observed_at is invalid")
	}
	if event.Changes == nil {
		return errors.New("event changes must be an object")
	}
	if err := validateSnapshotRaw(fields["snapshot"], true); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if err := validateNullableString(fields["error"]); err != nil {
		return fmt.Errorf("error: %w", err)
	}
	if messages, ok := fields["messages"]; ok {
		var values []map[string]json.RawMessage
		if err := decodeJSON(messages, &values); err != nil || len(values) == 0 {
			return errors.New("messages must be a non-empty array")
		}
		for _, value := range values {
			prompt, ok := value["prompt"]
			if !ok || isNull(prompt) {
				return errors.New("message prompt must be a string")
			}
			var text string
			if err := decodeJSON(prompt, &text); err != nil {
				return errors.New("message prompt must be a string")
			}
		}
	}
	if observations, ok := eventFields["observations"]; ok {
		var values []Observation
		if err := decodeJSON(observations, &values); err != nil || len(values) == 0 {
			return errors.New("event observations must be a non-empty array")
		}
		for _, observation := range values {
			if err := validateObservation(observation); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCollecting(raw []byte) error {
	fields, err := objectFields(raw)
	if err != nil {
		return err
	}
	if err := require(fields, "snapshot", "kind", "observations", "recovered_error"); err != nil {
		return err
	}
	if err := validateSnapshotRaw(fields["snapshot"], false); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	var batch Batch
	if err := decodeJSON(raw, &batch); err != nil {
		return err
	}
	if batch.Kind == "" || len(batch.Observations) == 0 {
		return errors.New("kind and observations must be non-empty")
	}
	if err := validateNullableString(fields["recovered_error"]); err != nil {
		return fmt.Errorf("recovered_error: %w", err)
	}
	for _, observation := range batch.Observations {
		if err := validateObservation(observation); err != nil {
			return err
		}
	}
	return nil
}

func validateObservation(observation Observation) error {
	if observation.Type == "" || observation.ObservedAt == "" || observation.HeadSHA == "" || observation.Changes == nil {
		return errors.New("observation type, time, head, and changes are required")
	}
	if _, err := time.Parse(time.RFC3339Nano, observation.ObservedAt); err != nil {
		return errors.New("observation observed_at is invalid")
	}
	return nil
}

func NewStore(dir, prURL string) (*Store, error) {
	pr, err := ParsePR(prURL)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0700); err != nil {
			return nil, err
		}
	}
	store := &Store{
		PR:       pr,
		Path:     filepath.Join(dir, pr.Key+".json"),
		StopPath: filepath.Join(dir, pr.Key+".stop"),
		Data:     newState(pr.URL),
	}
	state, err := ReadState(store.Path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if state.PRURL != pr.URL {
		return nil, errors.New("state identity does not match this watcher")
	}
	store.Data = state
	return store, nil
}

func newState(prURL string) State { return State{Version: 3, PRURL: prURL} }

func (s *Store) Lock(migrate bool) (func() error, error) {
	release, err := flock(strings.TrimSuffix(s.Path, filepath.Ext(s.Path)) + ".lock")
	if err != nil {
		return nil, err
	}
	state, err := ReadState(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		state = newState(s.PR.URL)
	} else if err != nil {
		release()
		return nil, err
	}
	if state.PRURL != s.PR.URL {
		release()
		return nil, errors.New("state identity does not match this watcher")
	}
	s.Data = state
	if migrate && s.Data.Version == 2 {
		s.Data.Version = 3
		s.Data.Collecting = nil
		if err := s.Save(); err != nil {
			release()
			return nil, err
		}
	}
	return release, nil
}

func (s *Store) Save() error {
	if s.deferSave {
		return nil
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(s.Data); err != nil {
		return err
	}
	if _, err := decodeState(buffer.Bytes()); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.Path), ".watch-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(buffer.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), s.Path); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(s.Path))
	if err != nil {
		return fmt.Errorf("state commit is uncertain: %w", err)
	}
	if err := dir.Sync(); err != nil {
		dir.Close()
		return fmt.Errorf("state commit is uncertain: %w", err)
	}
	if err := dir.Close(); err != nil {
		return fmt.Errorf("state commit is uncertain: %w", err)
	}
	return nil
}

func (s *Store) Update(fn func(*Store) error) error {
	path := strings.TrimSuffix(s.Path, filepath.Ext(s.Path)) + ".state.lock"
	release, err := flock(path)
	if err != nil {
		return err
	}
	defer release()
	state, err := ReadState(s.Path)
	if err != nil {
		return err
	}
	if state.PRURL != s.PR.URL || state.Control == nil {
		return errors.New("managed state identity mismatch")
	}
	tx := *s
	tx.Data, tx.deferSave = state, true
	if err := fn(&tx); err != nil {
		return err
	}
	if err := validateControl(tx.Data); err != nil {
		return err
	}
	tx.deferSave = false
	if err := tx.Save(); err != nil {
		return err
	}
	s.Data = tx.Data
	if tx.removeStopMarker && tx.Data.Finished {
		if err := os.Remove(tx.StopPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Store) PendingEvent() (json.RawMessage, error) {
	if len(s.Data.Pending) == 0 || isNull(s.Data.Pending) {
		return nil, errors.New("there is no pending event")
	}
	fields, err := objectFields(s.Data.Pending)
	if err != nil {
		return nil, err
	}
	raw, ok := fields["event"]
	if !ok {
		return nil, errors.New("pending event is missing")
	}
	return append(json.RawMessage(nil), raw...), nil
}

func (s *Store) Stage(kind string, snapshot Snapshot, changes Changes, failure *string, observations []Observation, at time.Time) (json.RawMessage, error) {
	if len(s.Data.Pending) != 0 && !isNull(s.Data.Pending) {
		return s.PendingEvent()
	}
	if kind == "" {
		return nil, errors.New("event type is required")
	}
	if observations != nil && len(observations) == 0 {
		return nil, errors.New("observations must be non-empty")
	}
	if changes == nil {
		changes = Changes{}
	}
	pendingSnapshot := snapshot
	if kind == "error" {
		if observations != nil {
			return nil, errors.New("error events cannot finalize observations")
		}
		pendingSnapshot = s.Data.Snapshot
	}
	if pendingSnapshot != nil {
		if err := validateSnapshot(pendingSnapshot); err != nil {
			return nil, fmt.Errorf("snapshot: %w", err)
		}
	}
	observedAt := at.UTC().Format(time.RFC3339Nano)
	if observations != nil {
		for _, observation := range observations {
			if err := validateObservation(observation); err != nil {
				return nil, err
			}
		}
		observedAt = observations[len(observations)-1].ObservedAt
	}
	eventID, err := newUUID()
	if err != nil {
		return nil, err
	}
	var headSHA *string
	if pendingSnapshot != nil {
		head := pendingSnapshot["head_sha"].(string)
		headSHA = &head
	}
	event := Event{
		Source: "pr-watch", WatcherID: s.PR.Key, EventID: eventID, Type: kind,
		PRURL: s.PR.URL, ObservedAt: observedAt, HeadSHA: headSHA, Changes: changes,
		Error: failure, Observations: observations,
	}
	eventRaw, err := encodeJSON(event)
	if err != nil {
		return nil, err
	}
	pendingRaw, err := encodeJSON(struct {
		Event    json.RawMessage `json:"event"`
		Snapshot Snapshot        `json:"snapshot"`
		Error    *string         `json:"error"`
	}{eventRaw, pendingSnapshot, failure})
	if err != nil {
		return nil, err
	}
	oldPending, oldCollecting := s.Data.Pending, s.Data.Collecting
	s.Data.Pending = pendingRaw
	if observations != nil {
		s.Data.Collecting = nil
	}
	if err := s.Save(); err != nil {
		s.Data.Pending, s.Data.Collecting = oldPending, oldCollecting
		return nil, err
	}
	return eventRaw, nil
}

func encodeJSON(value any) (json.RawMessage, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

func (s *Store) Ack(eventID string) error {
	if len(s.Data.Pending) == 0 || isNull(s.Data.Pending) {
		if s.Data.LastAck != nil && *s.Data.LastAck == eventID {
			if s.Data.Finished {
				if s.deferSave {
					s.removeStopMarker = true
					return nil
				}
				if err := os.Remove(s.StopPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			return nil
		}
		return errors.New("ack must match the pending event_id")
	}
	fields, err := objectFields(s.Data.Pending)
	if err != nil {
		return err
	}
	var event Event
	if err := decodeJSON(fields["event"], &event); err != nil {
		return err
	}
	if event.EventID != eventID {
		return errors.New("ack must match the pending event_id")
	}
	var snapshot Snapshot
	if !isNull(fields["snapshot"]) {
		if err := decodeJSON(fields["snapshot"], &snapshot); err != nil {
			return err
		}
	}
	var failure *string
	if err := decodeJSON(fields["error"], &failure); err != nil {
		return err
	}
	oldSnapshot, oldPending, oldLastAck, oldError, oldFinished := s.Data.Snapshot, s.Data.Pending, s.Data.LastAck, s.Data.Error, s.Data.Finished
	s.Data.Snapshot, s.Data.Pending, s.Data.Error = snapshot, nil, failure
	s.Data.LastAck = &eventID
	s.Data.Finished = event.Type == "merged" || event.Type == "closed" || event.Type == "stopped"
	if err := s.Save(); err != nil {
		s.Data.Snapshot, s.Data.Pending, s.Data.LastAck, s.Data.Error, s.Data.Finished = oldSnapshot, oldPending, oldLastAck, oldError, oldFinished
		return err
	}
	if s.Data.Finished {
		if s.deferSave {
			s.removeStopMarker = true
			return nil
		}
		if err := os.Remove(s.StopPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
