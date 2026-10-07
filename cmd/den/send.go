package main

import (
	"fmt"
	"strings"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/peer"
)

// send is `den send <who> <text…>`: deliver a message to one agent.
func send(args []string) {
	if len(args) < 2 {
		fail("usage: den send <name|session-id|pid> <message>")
	}
	agents, err := fleet.Snapshot()
	if err != nil {
		fail("den: %v", err)
	}
	a, ok := fleet.Find(agents, args[0])
	if !ok {
		fail("den: no agent matches %q", args[0])
	}
	if a.Status == fleet.Gone {
		fail("den: %s is gone", a.Name)
	}
	if err := peer.Send(a.Session.Socket, strings.Join(args[1:], " ")); err != nil {
		fail("den: %s: %v", a.Name, err)
	}
	fmt.Printf("delivered to %s\n", a.Name)
}
