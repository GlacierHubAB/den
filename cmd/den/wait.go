package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
)

// Exit codes of den wait and den send --wait.
const (
	exitReached = 0 // the state was reached
	exitError   = 1 // bad usage, or no agent matches
	exitTimeout = 2 // --timeout passed first
	exitGone    = 3 // the agent's session ended first
)

// waitCmd is `den wait <who> --until idle|needs-you|done [--timeout d]`.
// It needs no running Den: idle comes from Claude Code's sessions file,
// needs-you and done from the event stream den hook writes.
func waitCmd(args []string) {
	fs := flag.NewFlagSet("wait", flag.ExitOnError)
	until := fs.String("until", "idle", "idle, needs-you or done")
	timeout := fs.Duration("timeout", 0, "give up after this long (exit 2); 0 waits for ever")
	who := ""
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		who, args = args[0], args[1:]
	}
	fs.Parse(args)
	if who == "" && fs.NArg() > 0 {
		who = fs.Arg(0)
	}
	if who == "" {
		fail("usage: den wait <name|session-id|pid> --until idle|needs-you|done [--timeout 10m]")
	}
	switch *until {
	case "idle", "needs-you", "done":
	default:
		fail("den: --until is idle, needs-you or done, not %q", *until)
	}
	a := findAgent(who)
	if a.Status == fleet.Gone {
		fmt.Fprintf(os.Stderr, "den: %s is gone\n", a.Name)
		os.Exit(exitGone)
	}
	code := waitFor(a.Session.SessionID, *until, *timeout, events.Open(events.Path()))
	os.Exit(code)
}

func findAgent(who string) fleet.Agent {
	agents, err := fleet.Snapshot()
	if err != nil {
		fail("den: %v", err)
	}
	a, err := fleet.Lookup(agents, who)
	if err != nil {
		fail("den: %v", err)
	}
	return a
}

// waitFor blocks until the session reaches the state and returns the exit
// code. idle is reached at once when the session is idle already;
// needs-you and done are the next such event after rd was opened.
func waitFor(sessionID, until string, timeout time.Duration, rd *events.Reader) int {
	stop := make(chan struct{})
	defer close(stop)
	hit := make(chan int, 1)
	reach := func(code int) {
		select {
		case hit <- code:
		default:
		}
	}
	if until != "idle" {
		kind := events.Done
		if until == "needs-you" {
			kind = events.NeedsYou
		}
		go rd.Run(stop, func(_ []byte, ev events.Event) {
			switch {
			case ev.Session != sessionID:
			case ev.Kind == kind:
				reach(exitReached)
			case ev.Kind == events.SessionEnd:
				reach(exitGone)
			}
		})
	}
	var deadline <-chan time.Time
	if timeout > 0 {
		deadline = time.After(timeout)
	}
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	for {
		switch status := sessionStatus(sessionID); {
		case status == "gone":
			reach(exitGone)
		case until == "idle" && status == "idle":
			reach(exitReached)
		}
		select {
		case code := <-hit:
			return code
		case <-deadline:
			return exitTimeout
		case <-poll.C:
		}
	}
}

// sessionStatus is Claude Code's own word for the session: "busy", "idle",
// or "gone" when its file or its process is no longer there.
func sessionStatus(sessionID string) string {
	sessions, _ := discover.Scan()
	for _, s := range sessions {
		if s.SessionID == sessionID {
			if !s.Alive {
				return "gone"
			}
			return s.Status
		}
	}
	return "gone"
}
