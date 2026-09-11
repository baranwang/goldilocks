package prwatch

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"time"
)

const legacyWatcherMessage = "Legacy watcher registration commands are retired. Use watcher start/status; stop the old execution before watcher import --pr URL --state-file PATH."

// ErrUnsupportedAction is retained for source compatibility with callers that
// compiled against the pre-migration CLI; import is implemented now.
var ErrUnsupportedAction = errors.New("watcher import is unsupported until migration support is implemented")

type controllerCLIOptions struct {
	action      string
	pr          string
	ticketFile  string
	stateFile   string
	eventID     string
	part        int
	interval    float64
	quiet       float64
	replace     bool
	reopened    bool
	inspect     bool
	intervalSet bool
	quietSet    bool
}

// RunCLI executes one managed watcher protocol action. Parent actions use
// runtimeID as their namespace; child actions use it as the child identity.
func (c *Controller) RunCLI(ctx context.Context, args []string, runtimeID, cwd string, output io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	options, err := parseControllerCLI(args)
	if err != nil {
		return err
	}
	if output == nil {
		return errors.New("watcher output is required")
	}
	if !validUUID(runtimeID) {
		return errors.New("runtime ID must be a UUID")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Managed polling and its durable state protocol depend on POSIX locking.
	// Keep construction and argument validation available on Windows so routing
	// hooks and protocol tests remain usable, but reject execution here before
	// any managed action can reach a poll loop.
	if runtime.GOOS == "windows" {
		return ErrUnsupportedPlatform
	}
	encode := func(value any) error {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(value)
	}

	switch options.action {
	case "start":
		if !filepath.IsAbs(cwd) {
			return errors.New("watcher cwd must be absolute")
		}
		interval, quiet, err := options.durations()
		if err != nil {
			return err
		}
		result, err := c.Start(runtimeID, cwd, options.pr, StartOptions{
			Interval: interval, Quiet: quiet, Reopened: options.reopened,
		})
		if err != nil {
			return err
		}
		return encode(result)
	case "status":
		result, err := c.Status(runtimeID, options.pr)
		if err != nil {
			return err
		}
		return encode(result)
	case "stop":
		if err := c.Stop(runtimeID, options.pr); err != nil {
			return err
		}
		store, err := c.Open(runtimeID, options.pr)
		if err != nil {
			return err
		}
		state, err := ReadState(store.Path)
		if err != nil {
			return err
		}
		return encode(struct {
			Type    string `json:"type"`
			Action  string `json:"action"`
			WatchID string `json:"watch_id"`
		}{Type: "stop_requested", Action: "stop_requested", WatchID: state.Control.WatchID})
	case "resume":
		result, err := c.Resume(runtimeID, options.pr, options.replace)
		if err != nil {
			return err
		}
		return encode(result)
	case "received", "receive":
		result, err := c.Receive(runtimeID, options.pr, options.eventID, options.part)
		if err != nil {
			return err
		}
		return encode(struct {
			Type    string `json:"type"`
			Action  string `json:"action"`
			EventID string `json:"event_id"`
			Part    int    `json:"part"`
			Result  string `json:"result"`
		}{Type: "received", Action: "received", EventID: options.eventID, Part: options.part, Result: result})
	case "advance":
		var action Action
		if options.inspect {
			action, err = c.Inspect(options.ticketFile, runtimeID)
		} else {
			action, err = c.Advance(ctx, options.ticketFile, runtimeID, LiveDependencies())
		}
		if err != nil {
			return err
		}
		return encode(action)
	case "yield":
		if err := c.Yield(options.ticketFile, runtimeID); err != nil {
			return err
		}
		return encode(struct {
			Type   string `json:"type"`
			Action string `json:"action"`
		}{Type: "yielded", Action: "yielded"})
	case "import":
		if !filepath.IsAbs(cwd) {
			return errors.New("watcher cwd must be absolute")
		}
		result, err := c.Import(runtimeID, cwd, options.pr, options.stateFile)
		if err != nil {
			return err
		}
		return encode(result)
	default:
		return errors.New("unknown watcher action")
	}
}

func (o controllerCLIOptions) durations() (interval, quiet time.Duration, err error) {
	if o.intervalSet {
		interval, err = seconds(o.interval)
		if err != nil {
			return 0, 0, fmt.Errorf("interval: %w", err)
		}
	}
	if o.quietSet {
		quiet, err = seconds(o.quiet)
		if err != nil {
			return 0, 0, fmt.Errorf("quiet-seconds: %w", err)
		}
	}
	return interval, quiet, nil
}

func parseControllerCLI(args []string) (controllerCLIOptions, error) {
	if len(args) == 0 {
		return controllerCLIOptions{}, errors.New("watcher action is required")
	}
	action := args[0]
	if action == "register" || action == "checkpoint" || action == "finish" || action == "fail" {
		return controllerCLIOptions{}, errors.New(legacyWatcherMessage)
	}
	options := controllerCLIOptions{action: action}
	set := flag.NewFlagSet(action, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	switch action {
	case "start":
		set.StringVar(&options.pr, "pr", "", "canonical HTTPS pull request URL")
		set.Float64Var(&options.interval, "interval", 0, "seconds between unchanged checks")
		set.Float64Var(&options.quiet, "quiet-seconds", 0, "seconds without changes before delivery")
		set.BoolVar(&options.reopened, "reopened", false, "start a new generation after a verified reopen")
	case "status", "stop":
		set.StringVar(&options.pr, "pr", "", "canonical HTTPS pull request URL")
	case "resume":
		set.StringVar(&options.pr, "pr", "", "canonical HTTPS pull request URL")
		set.BoolVar(&options.replace, "replace", false, "replace the saved child binding")
	case "received", "receive":
		set.StringVar(&options.pr, "pr", "", "canonical HTTPS pull request URL")
		set.StringVar(&options.eventID, "event-id", "", "pending event ID")
		set.IntVar(&options.part, "part", 0, "1-based event part")
	case "advance":
		setTicketFlags(set, &options.ticketFile)
		set.BoolVar(&options.inspect, "inspect", false, "inspect worker state without polling")
	case "yield":
		setTicketFlags(set, &options.ticketFile)
	case "import":
		set.StringVar(&options.pr, "pr", "", "canonical HTTPS pull request URL")
		set.StringVar(&options.stateFile, "state-file", "", "legacy state file")
	default:
		return controllerCLIOptions{}, fmt.Errorf("unknown watcher action %q", action)
	}
	if err := set.Parse(args[1:]); err != nil {
		return controllerCLIOptions{}, err
	}
	if set.NArg() != 0 {
		return controllerCLIOptions{}, errors.New("positional arguments are not supported")
	}
	if action == "import" {
		if options.pr == "" {
			return controllerCLIOptions{}, errors.New("--pr is required")
		}
		if options.stateFile == "" {
			return controllerCLIOptions{}, errors.New("--state-file is required")
		}
		return options, nil
	}
	if action == "advance" || action == "yield" {
		if options.ticketFile == "" {
			return controllerCLIOptions{}, errors.New("--ticket-file is required")
		}
	} else if options.pr == "" {
		return controllerCLIOptions{}, errors.New("--pr is required")
	}
	if action == "received" || action == "receive" {
		if options.eventID == "" {
			return controllerCLIOptions{}, errors.New("--event-id is required")
		}
		if options.part <= 0 {
			return controllerCLIOptions{}, errors.New("--part must be positive")
		}
	}
	options.intervalSet = false
	options.quietSet = false
	set.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "interval":
			options.intervalSet = true
		case "quiet-seconds":
			options.quietSet = true
		}
	})
	if action != "start" && (options.intervalSet || options.quietSet) {
		return controllerCLIOptions{}, errors.New("interval flags are only valid for start")
	}
	return options, nil
}

func setTicketFlags(set *flag.FlagSet, target *string) {
	set.StringVar(target, "ticket-file", "", "absolute managed ticket file")
	set.StringVar(target, "ticket", "", "absolute managed ticket file")
}
