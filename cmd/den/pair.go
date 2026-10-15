package main

import (
	"fmt"
	"os"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// pairCmd is `den pair`: ask the running Den to show a console code. The
// confirm is a card in Den's own window, never this terminal: a terminal
// proves no human. Den refuses when this process runs under Claude Code,
// one of its agents, `den mcp` or `den hook`.
func pairCmd() {
	d, err := bridge.Call(bridge.SocketPath(), bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.ConsolePairEvent}, 10*time.Second)
	if err != nil {
		fail("den pair: %v: start Den first", err)
	}
	if d.Behavior != "allow" {
		fail("den pair: %s", d.Message)
	}
	fmt.Println(d.Message)
}

// unpairCmd is `den unpair [all]`: end the console, and with all every
// app pairing too. It only lowers, so it is never refused for who asks.
func unpairCmd(args []string) {
	all := len(args) > 0 && args[0] == "all"
	if len(args) > 0 && !all {
		fail("usage: den unpair [all]")
	}
	payload := []byte(`{"all":false}`)
	if all {
		payload = []byte(`{"all":true}`)
	}
	d, err := bridge.Call(bridge.SocketPath(), bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.ConsoleUnpairEvent, Payload: payload}, 10*time.Second)
	if err != nil {
		fail("den unpair: %v", err)
	}
	if d.Behavior != "allow" {
		fail("den unpair: %s", d.Message)
	}
	fmt.Fprintln(os.Stdout, d.Message)
}
