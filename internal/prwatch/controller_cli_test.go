package prwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestControllerCLIRequiresRuntimeIdentity(t *testing.T) {
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = c.RunCLI(context.Background(), []string{"start", "--pr", specPR}, "", t.TempDir(), &out)
	if err == nil || out.Len() != 0 {
		t.Fatal("missing runtime ID accepted")
	}
}

func TestControllerCLIStartDoesNotClaimRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = c.RunCLI(context.Background(), []string{"start", "--pr", specPR}, specParent, t.TempDir(), &out)
	if err != nil {
		t.Fatal(err)
	}
	var r StartResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if !r.Spawn || r.TicketFile == "" {
		t.Fatal(r)
	}
	out.Reset()
	err = c.RunCLI(context.Background(), []string{"status", "--pr", specPR}, specParent, t.TempDir(), &out)
	if err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Ready || status.Stage != Starting {
		t.Fatal(status)
	}
}

func TestControllerCLIRetiresLegacyActions(t *testing.T) {
	for _, action := range []string{"register", "checkpoint", "finish", "fail"} {
		t.Run(action, func(t *testing.T) {
			c, err := NewController(t.TempDir(), func() time.Time { return specTime })
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = c.RunCLI(context.Background(), []string{action}, specParent, t.TempDir(), &out)
			if err == nil || err.Error() != legacyWatcherMessage || out.Len() != 0 {
				t.Fatalf("legacy action was not retired: %v %q", err, out.String())
			}
		})
	}
}

func TestControllerCLIImportIsExplicitlyUnsupportedUntilMigration(t *testing.T) {
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = c.RunCLI(context.Background(), []string{"import", "--pr", specPR, "--state-file", "/tmp/legacy.json"}, specParent, t.TempDir(), &out)
	if !errors.Is(err, ErrUnsupportedAction) || out.Len() != 0 {
		t.Fatalf("import did not expose the migration boundary: %v %q", err, out.String())
	}
}

func TestControllerCLICancelledContextStopsProtocolAction(t *testing.T) {
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	err = c.RunCLI(ctx, []string{"start", "--pr", specPR}, specParent, t.TempDir(), &out)
	if !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("cancelled action was not stopped: %v %q", err, out.String())
	}
}

func TestControllerCLIRejectsInvalidActionFlags(t *testing.T) {
	for _, args := range [][]string{
		{"status", "--pr", specPR, "positional"},
		{"status", "--pr", specPR, "--interval", "1"},
		{"start", "--pr", specPR, "--interval", "0"},
		{"start", "--pr", specPR, "--quiet-seconds", "-1"},
		{"start", "--pr", specPR, "--quiet-seconds", "nan"},
		{"start", "--pr", specPR, "--quiet-seconds", "inf"},
		{"received", "--pr", specPR, "--event-id", "event"},
		{"received", "--pr", specPR, "--event-id", "event", "--part", "0"},
		{"advance", "--ticket-file", filepath.Join(t.TempDir(), "ticket"), "--inspect", "false"},
		{"start", "--pr", specPR, "--session-id", specParent},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			c, err := NewController(t.TempDir(), func() time.Time { return specTime })
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := c.RunCLI(context.Background(), args, specParent, t.TempDir(), &out); err == nil || out.Len() != 0 {
				t.Fatalf("invalid action accepted: %v %q", err, out.String())
			}
		})
	}
}

func TestControllerCLIResumePreservesAndReplacesBinding(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	start, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ObserveStart(specParent, specChild, specWatch); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Bind(start.TicketFile, specChild); err != nil {
		t.Fatal(err)
	}
	beforeTicket, err := os.ReadFile(start.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := c.RunCLI(context.Background(), []string{"resume", "--pr", specPR}, specParent, t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	var retained StartResult
	if err := json.Unmarshal(out.Bytes(), &retained); err != nil {
		t.Fatal(err)
	}
	if retained.Spawn || retained.AgentID != specChild {
		t.Fatalf("resume unexpectedly replaced child: %+v", retained)
	}
	afterTicket, err := os.ReadFile(start.TicketFile)
	if err != nil || !bytes.Equal(beforeTicket, afterTicket) {
		t.Fatal("ordinary resume changed the verified ticket", err)
	}
	out.Reset()
	if err := c.RunCLI(context.Background(), []string{"resume", "--pr", specPR, "--replace"}, specParent, t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	var replaced StartResult
	if err := json.Unmarshal(out.Bytes(), &replaced); err != nil {
		t.Fatal(err)
	}
	if !replaced.Spawn || replaced.AgentID != "" {
		t.Fatalf("replacement did not clear child binding: %+v", replaced)
	}
	newTicket, err := os.ReadFile(start.TicketFile)
	if err != nil || bytes.Equal(beforeTicket, newTicket) {
		t.Fatal("replacement retained the old ticket", err)
	}
	if err := os.WriteFile(start.TicketFile, beforeTicket, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Bind(start.TicketFile, specChild); err == nil {
		t.Fatal("old child rebound a replaced ticket without a new observation")
	}
}

func TestControllerCLIInspectDoesNotOfferOrAdvance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	c, start, store := boundFixture(t)
	state, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := c.RunCLI(context.Background(), []string{"advance", "--ticket-file", start.TicketFile, "--inspect"}, specChild, t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	var action Action
	if err := json.Unmarshal(out.Bytes(), &action); err != nil {
		t.Fatal(err)
	}
	if action.Action != "wait" || action.Reason != "worker_released" || action.Prompt != "" || action.ThreadID != "" {
		t.Fatalf("unexpected inspect result: %+v", action)
	}
	after, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Control.Progress != state.Control.Progress {
		t.Fatal("inspect advanced durable progress")
	}
}

func TestControllerCLIChildCannotUseParentCommandTicketFlags(t *testing.T) {
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = c.RunCLI(context.Background(), []string{"status", "--ticket-file", filepath.Join(t.TempDir(), "ticket")}, specChild, t.TempDir(), &out)
	if err == nil || out.Len() != 0 {
		t.Fatal("child ticket flag was accepted as a parent command")
	}
}

func TestControllerCLIChildRejectsParentPRFlag(t *testing.T) {
	if _, err := parseControllerCLI([]string{"advance", "--ticket-file", "/tmp/ticket", "--pr", specPR}); err == nil {
		t.Fatal("child advance accepted parent --pr flag")
	}
}

func TestControllerAdvancePropagatesCancellation(t *testing.T) {
	c, start, _ := boundFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Advance(ctx, start.TicketFile, specChild, Dependencies{
		Read: func(ctx context.Context, _ PR, _ func() bool) (Snapshot, error) {
			return nil, ctx.Err()
		},
		Now:  func() time.Time { return specTime },
		Wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was converted to a watcher fault: %v", err)
	}
}
