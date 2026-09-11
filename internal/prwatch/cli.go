package prwatch

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
	"unicode/utf8"
)

type cliOptions struct {
	pr, stateDir, bodyFile, eventID string
	part                            int
	interval, quiet                 float64
}

func RunCLI(ctx context.Context, args []string, output io.Writer) error {
	options, err := parseCLI(args)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return ErrUnsupportedPlatform
	}
	options.stateDir, err = filepath.Abs(options.stateDir)
	if err != nil {
		return err
	}
	store, err := NewStore(options.stateDir, options.pr)
	if err != nil {
		return err
	}
	if store.Data.Control != nil && args[0] != "status" {
		return errors.New("managed state: use watcher advance/status/stop")
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)

	switch args[0] {
	case "stop":
		file, err := os.OpenFile(store.StopPath, os.O_WRONLY|os.O_CREATE, 0600)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		return encoder.Encode(struct {
			Type      string `json:"type"`
			WatcherID string `json:"watcher_id"`
		}{"stop_requested", store.PR.Key})
	case "status":
		pollerRunning := false
		release, err := store.Lock(false)
		if errors.Is(err, ErrLocked) {
			pollerRunning = true
		} else if err != nil {
			return err
		} else if err := release(); err != nil {
			return err
		}
		var pending json.RawMessage
		if hasPending(store) {
			pending, err = store.PendingEvent()
			if err != nil {
				return err
			}
		}
		return encoder.Encode(struct {
			WatcherID     string          `json:"watcher_id"`
			StateFile     string          `json:"state_file"`
			PollerRunning bool            `json:"poller_running"`
			Finished      bool            `json:"finished"`
			PendingEvent  json.RawMessage `json:"pending_event"`
		}{store.PR.Key, store.Path, pollerRunning, store.Data.Finished, pending})
	}

	release, err := store.Lock(true)
	if err != nil {
		return err
	}
	defer release()
	if store.Data.Control != nil && args[0] != "status" {
		return errors.New("managed state: use watcher advance/status/stop")
	}

	switch args[0] {
	case "watch":
		if _, err := os.Stat(store.Path); errors.Is(err, os.ErrNotExist) {
			if err := store.Save(); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		interval, err := seconds(options.interval)
		if err != nil {
			return err
		}
		quiet, err := seconds(options.quiet)
		if err != nil {
			return err
		}
		result, err := Watch(ctx, store, Options{Interval: interval, Quiet: quiet}, LiveDependencies())
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "prepare":
		extra := ""
		if options.bodyFile != "" {
			body, err := os.ReadFile(options.bodyFile)
			if err != nil {
				return err
			}
			if !utf8.Valid(body) {
				return errors.New("body file must contain valid UTF-8")
			}
			extra = string(body)
		}
		parts, err := store.Prepare(extra)
		if err != nil {
			return err
		}
		return encoder.Encode(struct {
			Type  string `json:"type"`
			Parts int    `json:"parts"`
		}{"prepared", parts})
	case "message":
		message, err := store.Message(options.part)
		if err != nil {
			return err
		}
		return encoder.Encode(message)
	case "ack":
		if err := store.Ack(options.eventID); err != nil {
			return err
		}
		return encoder.Encode(struct {
			Type    string `json:"type"`
			EventID string `json:"event_id"`
		}{"acknowledged", options.eventID})
	}
	return errors.New("unknown PR watch action")
}

func parseCLI(args []string) (cliOptions, error) {
	if len(args) == 0 {
		return cliOptions{}, errors.New("PR watch action is required")
	}
	options := cliOptions{stateDir: filepath.Join("work", "pr-watch"), interval: 60, quiet: 30}
	set := flag.NewFlagSet(args[0], flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&options.pr, "pr", "", "canonical HTTPS pull request URL")
	set.StringVar(&options.stateDir, "state-dir", options.stateDir, "watch state directory")
	switch args[0] {
	case "watch":
		set.Float64Var(&options.interval, "interval", options.interval, "seconds between unchanged checks")
		set.Float64Var(&options.quiet, "quiet-seconds", options.quiet, "seconds without changes before delivery")
	case "prepare":
		set.StringVar(&options.bodyFile, "body-file", "", "supplemental UTF-8 evidence file")
	case "message":
		set.IntVar(&options.part, "part", 0, "1-based message part")
	case "ack":
		set.StringVar(&options.eventID, "event-id", "", "pending event ID")
	case "stop", "status":
	default:
		return cliOptions{}, fmt.Errorf("unknown PR watch action %q", args[0])
	}
	if err := set.Parse(args[1:]); err != nil {
		return cliOptions{}, err
	}
	if set.NArg() != 0 {
		return cliOptions{}, errors.New("positional arguments are not supported")
	}
	if options.pr == "" {
		return cliOptions{}, errors.New("--pr is required")
	}
	if args[0] == "message" && options.part <= 0 {
		return cliOptions{}, errors.New("--part must be positive")
	}
	if args[0] == "ack" && options.eventID == "" {
		return cliOptions{}, errors.New("--event-id is required")
	}
	if args[0] == "watch" {
		if _, err := seconds(options.interval); err != nil {
			return cliOptions{}, fmt.Errorf("interval: %w", err)
		}
		if _, err := seconds(options.quiet); err != nil {
			return cliOptions{}, fmt.Errorf("quiet-seconds: %w", err)
		}
	}
	return options, nil
}

func seconds(value float64) (time.Duration, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0, errors.New("seconds must be finite and positive")
	}
	duration, err := time.ParseDuration(strconv.FormatFloat(value, 'f', -1, 64) + "s")
	if err != nil || duration <= 0 {
		return 0, errors.New("seconds out of range")
	}
	return duration, nil
}

func LiveDependencies() Dependencies {
	return Dependencies{
		Read: func(ctx context.Context, pr PR, stopped func() bool) (Snapshot, error) {
			return Collect(ctx, pr, ReadGH, stopped)
		},
		Now: time.Now,
		Wait: func(ctx context.Context, duration time.Duration) error {
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
}
