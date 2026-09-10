package prwatch

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultInterval = time.Minute
	defaultQuiet    = 30 * time.Second
	bindLifetime    = 10 * time.Minute
	startLifetime   = 24 * time.Hour
)

type Controller struct {
	Root string
	Now  func() time.Time
}

type StartOptions struct {
	Interval time.Duration
	Quiet    time.Duration
	Reopened bool
}

type StartResult struct {
	WatchID    string `json:"watch_id"`
	TicketFile string `json:"ticket_file,omitempty"`
	StateFile  string `json:"state_file"`
	Spawn      bool   `json:"spawn"`
	AgentID    string `json:"agent_id,omitempty"`
}

type Ticket struct {
	WatchID  string `json:"watch_id"`
	ParentID string `json:"parent_id"`
	PRURL    string `json:"pr_url"`
	Nonce    string `json:"nonce"`
}

type Membership struct {
	ParentID   string    `json:"parent_id"`
	AgentID    string    `json:"agent_id"`
	TurnID     string    `json:"turn_id"`
	ObservedAt time.Time `json:"observed_at"`
}

func NewController(root string, now func() time.Time) (*Controller, error) {
	if runtime.GOOS == "windows" {
		return nil, ErrUnsupportedPlatform
	}
	if root == "" {
		return nil, errors.New("controller root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(abs, 0700); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Controller{Root: resolved, Now: now}, nil
}

func DefaultControllerRoot() (string, error) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(userHome, ".codex")
	}
	return filepath.Join(home, "goldilocks", "pr-watch"), nil
}

func (c *Controller) Open(parentID, prURL string) (*Store, error) {
	if !validUUID(parentID) {
		return nil, errors.New("parent_id must be a UUID")
	}
	pr, err := ParsePR(prURL)
	if err != nil {
		return nil, err
	}
	parentDir := c.parentDir(parentID)
	if err := c.ensureManagedDir(parentDir); err != nil {
		return nil, err
	}
	statePath := filepath.Join(parentDir, pr.Key+".json")
	if err := c.rejectSymlinkedFile(statePath); err != nil {
		return nil, err
	}
	return NewStore(parentDir, pr.URL)
}

func (c *Controller) Start(parentID, cwd, prURL string, opts StartOptions) (StartResult, error) {
	if !validUUID(parentID) {
		return StartResult{}, errors.New("parent_id must be a UUID")
	}
	if !filepath.IsAbs(cwd) {
		return StartResult{}, errors.New("cwd must be absolute")
	}
	interval, quiet, err := resolveStartOptions(opts)
	if err != nil {
		return StartResult{}, err
	}
	store, err := c.Open(parentID, prURL)
	if err != nil {
		return StartResult{}, err
	}
	if err := c.withParentLock(parentID, func() error { return c.cleanupStarts(parentID) }); err != nil {
		return StartResult{}, err
	}

	release, err := waitFlock(stateLockPath(store.Path))
	if err != nil {
		return StartResult{}, err
	}
	defer release()

	state, readErr := ReadState(store.Path)
	ticketFile := ticketPath(store.Path)
	if readErr == nil {
		store.Data = state
		return c.resumeIntent(store, ticketFile, parentID, opts, interval, quiet)
	}
	if !errors.Is(readErr, os.ErrNotExist) {
		return StartResult{}, readErr
	}

	watchID, err := newUUID()
	if err != nil {
		return StartResult{}, err
	}
	ticket, err := newTicket(watchID, parentID, store.PR.URL)
	if err != nil {
		return StartResult{}, err
	}
	if err := writeJSON0600(ticketFile, ticket); err != nil {
		return StartResult{}, err
	}
	store.Data = State{
		Version: 4,
		PRURL:   store.PR.URL,
		Control: newControl(parentID, watchID, cwd, digest(ticket.Nonce), c.Now().UTC(), Options{Interval: interval, Quiet: quiet}),
	}
	if err := store.Save(); err != nil {
		return StartResult{}, err
	}
	return startResult(store, ticketFile, true), nil
}

func (c *Controller) ObserveStart(parentID, agentID, turnID string) error {
	if !validUUID(parentID) || !validUUID(agentID) || !validUUID(turnID) {
		return errors.New("membership identities must be UUIDs")
	}
	parentDir := c.parentDir(parentID)
	if _, err := os.Stat(parentDir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return c.withParentLock(parentID, func() error {
		unbound, err := c.hasUnboundIntent(parentID)
		if err != nil || !unbound {
			return err
		}
		startDir := filepath.Join(parentDir, "starts")
		if err := c.ensureManagedDir(startDir); err != nil {
			return err
		}
		return writeJSON0600(filepath.Join(startDir, agentID+".json"), Membership{
			ParentID: parentID, AgentID: agentID, TurnID: turnID, ObservedAt: c.Now().UTC(),
		})
	})
}

func (c *Controller) Bind(ticketFile, agentID string) (*Store, error) {
	if !validUUID(agentID) {
		return nil, errors.New("agent_id must be a UUID")
	}
	ticket, stateFile, err := c.readManagedTicket(ticketFile)
	if err != nil {
		return nil, err
	}
	var bound *Store
	err = c.withParentLock(ticket.ParentID, func() error {
		current, currentState, err := c.readManagedTicket(ticketFile)
		if err != nil {
			return err
		}
		if current != ticket || currentState != stateFile {
			return errors.New("ticket changed while binding")
		}
		if err := c.rejectChildConflict(ticket.ParentID, ticket.WatchID, agentID); err != nil {
			return err
		}
		store, err := c.Open(ticket.ParentID, ticket.PRURL)
		if err != nil {
			return err
		}
		if store.Path != stateFile {
			return errors.New("ticket state path mismatch")
		}
		for {
			err = store.Update(func(tx *Store) error {
				control := tx.Data.Control
				if !ticketMatches(tx.Data, ticket) {
					return errors.New("ticket does not match managed state")
				}
				if control.AgentID != "" {
					if control.AgentID != agentID {
						return errors.New("watch is bound to a different child")
					}
					return nil
				}
				if c.Now().UTC().After(control.BindAfter.Add(bindLifetime)) {
					return errors.New("unbound watch intent expired")
				}
				membership, err := c.readMembership(ticket.ParentID, agentID)
				if err != nil {
					return err
				}
				if !bindingAllowed(tx.Data, ticket, agentID, membership) {
					return errors.New("ticket is not authorized for this child")
				}
				control.AgentID = agentID
				control.Stage = Initializing
				control.Progress++
				return nil
			})
			if !errors.Is(err, ErrLocked) {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		if err != nil {
			return err
		}
		bound = store
		return nil
	})
	return bound, err
}

func bindingAllowed(s State, ticket Ticket, child string, m Membership) bool {
	c := s.Control
	sum := sha256.Sum256([]byte(ticket.Nonce))
	return c != nil && ticket.WatchID == c.WatchID && ticket.PRURL == s.PRURL &&
		ticket.ParentID == c.ParentID && hex.EncodeToString(sum[:]) == c.TicketSHA256 &&
		m.ParentID == c.ParentID && m.AgentID == child &&
		!m.ObservedAt.Before(c.BindAfter) && (c.AgentID == "" || c.AgentID == child)
}

func (c *Controller) resumeIntent(store *Store, ticketFile, parentID string, opts StartOptions, interval, quiet time.Duration) (StartResult, error) {
	control := store.Data.Control
	if control == nil || control.ParentID != parentID {
		return StartResult{}, errors.New("existing state is not a managed watch")
	}
	if control.AgentID == "" && control.Stage == Starting && c.Now().UTC().After(control.BindAfter.Add(bindLifetime)) {
		return StartResult{}, errors.New("unbound watch intent expired")
	}
	if opts.Interval > 0 && interval != control.Interval || opts.Quiet > 0 && quiet != control.Quiet {
		return StartResult{}, errors.New("duplicate start options conflict with existing intent")
	}
	ticket, stateFile, err := c.readManagedTicket(ticketFile)
	if err == nil && stateFile == store.Path && ticketMatches(store.Data, ticket) {
		return startResult(store, ticketFile, control.AgentID == ""), nil
	}
	if !opts.Reopened {
		return StartResult{}, errors.New("managed watch ticket is missing or mismatched; explicit resume required")
	}
	ticket, err = newTicket(control.WatchID, control.ParentID, store.PR.URL)
	if err != nil {
		return StartResult{}, err
	}
	if err := writeJSON0600(ticketFile, ticket); err != nil {
		return StartResult{}, err
	}
	control.TicketSHA256 = digest(ticket.Nonce)
	if control.AgentID == "" {
		control.BindAfter = c.Now().UTC()
	}
	if err := store.Save(); err != nil {
		return StartResult{}, err
	}
	return startResult(store, ticketFile, control.AgentID == ""), nil
}

func resolveStartOptions(opts StartOptions) (time.Duration, time.Duration, error) {
	if opts.Interval < 0 || opts.Quiet < 0 {
		return 0, 0, errors.New("interval and quiet must not be negative")
	}
	interval, quiet := opts.Interval, opts.Quiet
	if interval == 0 {
		interval = defaultInterval
	}
	if quiet == 0 {
		quiet = defaultQuiet
	}
	return interval, quiet, nil
}

func newTicket(watchID, parentID, prURL string) (Ticket, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Ticket{}, err
	}
	return Ticket{WatchID: watchID, ParentID: parentID, PRURL: prURL, Nonce: hex.EncodeToString(nonce[:])}, nil
}

func readTicket(path string) (Ticket, error) {
	var ticket Ticket
	if err := readStrictJSON(path, &ticket); err != nil {
		return Ticket{}, err
	}
	pr, err := ParsePR(ticket.PRURL)
	nonce, nonceErr := hex.DecodeString(ticket.Nonce)
	if !validUUID(ticket.WatchID) || !validUUID(ticket.ParentID) || err != nil || pr.URL != ticket.PRURL || nonceErr != nil || len(nonce) != 32 {
		return Ticket{}, errors.New("ticket identity is invalid")
	}
	return ticket, nil
}

func (c *Controller) readManagedTicket(path string) (Ticket, string, error) {
	if !filepath.IsAbs(path) {
		return Ticket{}, "", errors.New("ticket path must be absolute")
	}
	clean := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return Ticket{}, "", err
	}
	if resolved != clean || !pathWithin(c.Root, resolved) {
		return Ticket{}, "", errors.New("ticket path is outside the controller root or uses a symlink")
	}
	ticket, err := readTicket(resolved)
	if err != nil {
		return Ticket{}, "", err
	}
	pr, _ := ParsePR(ticket.PRURL)
	parentDir := c.parentDir(ticket.ParentID)
	stateFile := filepath.Join(parentDir, pr.Key+".json")
	if resolved != ticketPath(stateFile) {
		return Ticket{}, "", errors.New("ticket path does not match its identity")
	}
	if err := c.rejectSymlinkedFile(stateFile); err != nil {
		return Ticket{}, "", err
	}
	return ticket, stateFile, nil
}

func ticketMatches(state State, ticket Ticket) bool {
	control := state.Control
	return control != nil && ticket.WatchID == control.WatchID && ticket.ParentID == control.ParentID &&
		ticket.PRURL == state.PRURL && digest(ticket.Nonce) == control.TicketSHA256
}

func startResult(store *Store, ticketFile string, spawn bool) StartResult {
	return StartResult{
		WatchID: store.Data.Control.WatchID, TicketFile: ticketFile, StateFile: store.Path,
		Spawn: spawn, AgentID: store.Data.Control.AgentID,
	}
}

func (c *Controller) readMembership(parentID, agentID string) (Membership, error) {
	startDir := filepath.Join(c.parentDir(parentID), "starts")
	if err := c.validateManagedDir(startDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Membership{}, errors.New("child start was not observed")
		}
		return Membership{}, err
	}
	path := filepath.Join(startDir, agentID+".json")
	if err := c.rejectSymlinkedFile(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Membership{}, errors.New("child start was not observed")
		}
		return Membership{}, err
	}
	var membership Membership
	if err := readStrictJSON(path, &membership); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Membership{}, errors.New("child start was not observed")
		}
		return Membership{}, err
	}
	if !validUUID(membership.ParentID) || !validUUID(membership.AgentID) || !validUUID(membership.TurnID) || membership.ObservedAt.IsZero() {
		return Membership{}, errors.New("membership identity is invalid")
	}
	return membership, nil
}

func (c *Controller) rejectChildConflict(parentID, watchID, agentID string) error {
	states, err := c.parentStates(parentID)
	if err != nil {
		return err
	}
	for _, state := range states {
		if state.Control != nil && state.Control.WatchID != watchID && state.Control.AgentID == agentID && !state.Finished {
			return errors.New("child is already bound to another active watch")
		}
	}
	return nil
}

func (c *Controller) hasUnboundIntent(parentID string) (bool, error) {
	states, err := c.parentStates(parentID)
	if err != nil {
		return false, err
	}
	for _, state := range states {
		if state.Control != nil && state.Control.AgentID == "" && state.Control.Stage == Starting {
			return true, nil
		}
	}
	return false, nil
}

func (c *Controller) parentStates(parentID string) ([]State, error) {
	entries, err := os.ReadDir(c.parentDir(parentID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var states []State
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(c.parentDir(parentID), entry.Name())
		if err := c.rejectSymlinkedFile(path); err != nil {
			return nil, err
		}
		state, err := ReadState(path)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}

func (c *Controller) cleanupStarts(parentID string) error {
	startDir := filepath.Join(c.parentDir(parentID), "starts")
	if err := c.validateManagedDir(startDir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	entries, err := os.ReadDir(startDir)
	if err != nil {
		return err
	}
	states, err := c.parentStates(parentID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(startDir, entry.Name())
		if err := c.rejectSymlinkedFile(path); err != nil {
			return err
		}
		var membership Membership
		if err := readStrictJSON(path, &membership); err != nil {
			return err
		}
		now := c.Now().UTC()
		if !membership.ObservedAt.Add(startLifetime).Before(now) || membershipNeeded(states, membership, now) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func membershipNeeded(states []State, membership Membership, now time.Time) bool {
	for _, state := range states {
		control := state.Control
		if control != nil && control.AgentID == "" && !now.After(control.BindAfter.Add(bindLifetime)) &&
			!membership.ObservedAt.Before(control.BindAfter) {
			return true
		}
	}
	return false
}

func (c *Controller) withParentLock(parentID string, fn func() error) error {
	parentDir := c.parentDir(parentID)
	if err := c.ensureManagedDir(parentDir); err != nil {
		return err
	}
	release, err := waitFlock(filepath.Join(parentDir, "bindings.lock"))
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

func waitFlock(path string) (func() error, error) {
	deadline := time.Now().Add(time.Second)
	for {
		release, err := flock(path)
		if !errors.Is(err, ErrLocked) || time.Now().After(deadline) {
			return release, err
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (c *Controller) ensureManagedDir(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("managed directory is not a real directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	if err := c.validateManagedDir(path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return err
	}
	return nil
}

func (c *Controller) validateManagedDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("managed directory is not a real directory")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != filepath.Clean(path) || !pathWithin(c.Root, resolved) {
		return errors.New("managed directory is outside the controller root or uses a symlink")
	}
	return nil
}

func (c *Controller) rejectSymlinkedFile(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if resolved != filepath.Clean(path) || !pathWithin(c.Root, resolved) {
		return errors.New("managed state target is outside the controller root or uses a symlink")
	}
	return nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (c *Controller) parentDir(parentID string) string {
	return filepath.Join(c.Root, "parents", parentID)
}

func stateLockPath(stateFile string) string {
	return strings.TrimSuffix(stateFile, filepath.Ext(stateFile)) + ".state.lock"
}

func ticketPath(stateFile string) string {
	return strings.TrimSuffix(stateFile, filepath.Ext(stateFile)) + ".ticket"
}

func readStrictJSON(path string, destination any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func writeJSON0600(path string, value any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".controller-*.tmp")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
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
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("file commit is uncertain: %w", err)
	}
	if err := directory.Sync(); err != nil {
		directory.Close()
		return fmt.Errorf("file commit is uncertain: %w", err)
	}
	return directory.Close()
}
