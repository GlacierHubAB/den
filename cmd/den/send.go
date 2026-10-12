package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/peer"
	"github.com/GlacierHubAB/den/internal/sanitize"
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
	if host, ok := remoteHostOf(args[0]); ok {
		if *wait {
			fail("den: --wait does not work for an agent on %s yet", host)
		}
		out, err := sendViaDen(bridge.SocketPath(), args[0], strings.Join(args[1:], " "))
		if err != nil {
			fail("%v", err)
		}
		fmt.Println(out)
		return
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

// remoteHostOf reports the host of a "name@host" query; "local" and a bare
// name are this machine.
func remoteHostOf(who string) (string, bool) {
	i := strings.LastIndexByte(who, '@')
	if i <= 0 || i == len(who)-1 {
		return "", false
	}
	host := strings.ToLower(who[i+1:])
	return host, host != "local"
}

// sendViaDen asks the running Den to message an agent on a remote host:
// only Den holds the ssh session to it. The text travels over den.sock, and
// from there down that session, never in an argument list. Without a Den
// nothing is sent.
func sendViaDen(sock, who, text string) (string, error) {
	host, _ := remoteHostOf(who)
	cwd, _ := os.Getwd()
	payload, _ := json.Marshal(bridge.SendInput{Who: who, Text: text, AskedBy: fmt.Sprintf("den send, pid %d in %s", os.Getpid(), cwd)})
	env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.SendEvent, Payload: payload}
	d, err := bridge.Call(sock, env, 30*time.Second)
	switch {
	case errors.Is(err, bridge.ErrNoDen):
		return "", fmt.Errorf("den: Den is not attached to %s; nothing was sent (start Den, or run den send on %s itself)", host, host)
	case err != nil:
		return "", fmt.Errorf("den: no answer from Den (%v); whether it was sent is not known", err)
	case d.Behavior != "allow":
		return "", fmt.Errorf("den: %s", sanitize.Line(d.Message))
	}
	return sanitize.Line(d.Message), nil
}
