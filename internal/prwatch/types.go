package prwatch

import (
	"encoding/json"
	"errors"
)

var ErrLocked = errors.New("another command holds this watch lock")
var ErrUnsupportedPlatform = errors.New("PR monitoring currently supports macOS/Linux")
var ErrStopped = errors.New("watch stop requested")

type PR struct {
	URL    string
	Host   string
	Owner  string
	Repo   string
	Number string
	Key    string
}

type Snapshot map[string]any
type Changes map[string]any

type Observation struct {
	Type       string  `json:"type"`
	ObservedAt string  `json:"observed_at"`
	HeadSHA    string  `json:"head_sha"`
	Changes    Changes `json:"changes"`
}

type Batch struct {
	Snapshot       Snapshot      `json:"snapshot"`
	Kind           string        `json:"kind"`
	Observations   []Observation `json:"observations"`
	RecoveredError *string       `json:"recovered_error"`
}

type State struct {
	Version    int             `json:"version"`
	PRURL      string          `json:"pr_url"`
	Snapshot   Snapshot        `json:"snapshot"`
	Pending    json.RawMessage `json:"pending"`
	LastAck    *string         `json:"last_ack"`
	Error      *string         `json:"error"`
	Finished   bool            `json:"finished"`
	Collecting *Batch          `json:"collecting"`
	Control    *Control        `json:"control,omitempty"`
}

type Store struct {
	PR        PR
	Path      string
	StopPath  string
	Data      State
	deferSave bool
}

type Event struct {
	Source       string        `json:"source"`
	WatcherID    string        `json:"watcher_id"`
	EventID      string        `json:"event_id"`
	Type         string        `json:"type"`
	PRURL        string        `json:"pr_url"`
	ObservedAt   string        `json:"observed_at"`
	HeadSHA      *string       `json:"head_sha"`
	Changes      Changes       `json:"changes"`
	Error        *string       `json:"error,omitempty"`
	Observations []Observation `json:"observations,omitempty"`
}
