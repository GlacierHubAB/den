package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/peermsg"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// runInbound is `den hook orchestrator-inbound`, the UserPromptSubmit hook
// Den gives its orchestrator (orch.Argv). Unlike `den hook` it fails
// closed: a prompt reaches the orchestrator only when Den wrote it.
//
//   - A prompt that holds anything like a peer envelope is always blocked
//     (exit 0, {"decision":"block"}). A well-formed one is handed to Den,
//     which may take its body as a report and send it on as its own turn.
//   - Any other prompt passes (exit 0, nothing printed) only when Den says
//     it wrote exactly those bytes in the last 30 s; otherwise blocked.
//   - Den not answering blocks. Anything going wrong inside exits 2, which
//     Claude Code also takes as a block.
//
// It is done within 2 s. --self-test reads the same input but asks Den
// nothing: Den runs it at every launch on a made-up envelope and starts the
// orchestrator only when it is blocked.
func runInbound(args []string) {
	selfTest := false
	for _, a := range args {
		selfTest = selfTest || a == "--self-test"
	}
	os.Exit(inbound(os.Stdin, os.Stdout, os.Stderr, selfTest, bridge.SocketPath(), time.Now().Add(inboundDeadline)))
}

// inboundDeadline is how long the hook may take; Claude Code's own timeout
// for it is longer (orch.HookTimeout).
const inboundDeadline = 2 * time.Second

// askInbound reaches Den; tests replace it.
var askInbound = func(sock string, in bridge.InboundInput, wait time.Duration) (bridge.Decision, error) {
	payload, err := json.Marshal(in)
	if err != nil {
		return bridge.Decision{}, err
	}
	env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.InboundEvent, Payload: payload}
	return bridge.Call(sock, env, wait)
}

func inbound(stdin io.Reader, stdout, stderr io.Writer, selfTest bool, sock string, deadline time.Time) (code int) {
	defer func() {
		if recover() != nil {
			fmt.Fprintln(stderr, "den: the orchestrator's inbound hook failed")
			code = 2
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(stdin, 8<<20))
	if err != nil {
		fmt.Fprintln(stderr, "den: reading the hook input:", err)
		return 2
	}
	var in struct {
		Event  string `json:"hook_event_name"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.Event != "UserPromptSubmit" {
		fmt.Fprintln(stderr, "den: not a UserPromptSubmit hook input")
		return 2
	}
	block := func(reason string) int {
		out, err := json.Marshal(map[string]string{"decision": "block", "reason": sanitize.Label(reason, 300)})
		if err != nil {
			return 2
		}
		if _, err := stdout.Write(append(out, '\n')); err != nil {
			return 2
		}
		return 0
	}
	wait := time.Until(deadline) - 200*time.Millisecond
	if peermsg.HasEnvelope(in.Prompt) {
		e, err := peermsg.Parse(in.Prompt)
		switch {
		case err != nil:
			if !selfTest && wait > 0 {
				// Den counts every envelope, read or not, to tell a peer's
				// blocked turn from its own; its answer changes nothing.
				askInbound(sock, bridge.InboundInput{Kind: "refused", Body: err.Error()}, wait)
			}
			return block("Den refused a peer message it could not read: " + err.Error())
		case selfTest:
			return block("Den's self-test: peer messages are blocked")
		case wait <= 0:
			return block("Den did not answer in time; the peer message was not delivered")
		}
		d, err := askInbound(sock, bridge.InboundInput{Kind: "envelope", From: e.From, FromName: e.FromName, FromMode: e.FromMode, PID: e.PID, Body: e.Body}, wait)
		if err != nil {
			if errors.Is(err, bridge.ErrNoDen) {
				return block("Den is not running; the peer message was not delivered")
			}
			return block("Den did not answer; the peer message was not delivered")
		}
		return block(d.Message)
	}
	if selfTest {
		return block("Den's self-test expects a peer message")
	}
	if wait <= 0 {
		return block("Den did not answer in time")
	}
	d, err := askInbound(sock, bridge.InboundInput{Kind: "prompt", SHA256: peermsg.Hash(in.Prompt)}, wait)
	if err != nil || d.Behavior != "allow" {
		return block("the orchestrator takes turns only from Den, and Den did not write this one")
	}
	return 0
}
