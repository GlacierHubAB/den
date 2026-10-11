package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/peer"
)

// send is `den send [--wait] [--timeout d] <who> <text…>`: deliver a
// message to one agent, and with --wait block until its next turn is done
// (exit codes as den wait).
func send(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	wait := fs.Bool("wait", false, "wait until the agent's turn is done")
	timeout := fs.Duration("timeout", 0, "with --wait, give up after this long (exit 2)")
	fs.Parse(args)
	args = fs.Args()
	if len(args) < 2 {
		fail("usage: den send [--wait] [--timeout 10m] <name|session-id|pid> <message>")
	}
	a := findAgent(args[0])
	if a.Status == fleet.Gone {
		fail("den: %s is gone", a.Name)
	}
	// Start following before the message goes out, so a quick turn's end
	// cannot slip past.
	var rd *events.Reader
	if *wait {
		rd = events.Open(events.Path())
	}
	if err := peer.Send(a.Session.Socket, strings.Join(args[1:], " ")); err != nil {
		fail("den: %s: %v", a.Name, err)
	}
	fmt.Printf("delivered to %s\n", a.Name)
	if *wait {
		os.Exit(waitFor(a.Session.SessionID, "done", *timeout, rd))
	}
}
