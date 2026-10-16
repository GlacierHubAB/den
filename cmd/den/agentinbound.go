package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/peermsg"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// runAgentInbound is `den hook agent-inbound`, the UserPromptSubmit hook in
// the --settings file Den gives a guarded agent (one that runs without
// asking). A prompt with no peer envelope passes: the human typed it in
// the agent's terminal, or Den wrote it. A prompt that holds anything like
// an envelope passes only when Den says it delivered exactly those bytes
// to this session; anything else is blocked, Den not answering included,
// and anything going wrong inside exits 2, which Claude Code also takes as
// a block. --self-test asks Den nothing and blocks every envelope: Den
// runs it at launch on a made-up one and starts the agent only when it is
// blocked.
func runAgentInbound(args []string) {
	selfTest, sock := agentInboundArgs(args)
	os.Exit(agentInbound(os.Stdin, os.Stdout, os.Stderr, selfTest, sock, time.Now().Add(inboundDeadline)))
}

// agentInboundArgs reads the hook's arguments: --self-test, and --socket,
// the den.sock Den named in the settings file. Only that path is asked,
// whatever the agent's environment says; "" when it is missing or not
// absolute, and then an envelope is blocked.
func agentInboundArgs(args []string) (selfTest bool, sock string) {
	for i, a := range args {
		switch {
		case a == "--self-test":
			selfTest = true
		case a == "--socket" && i+1 < len(args):
			sock = args[i+1]
		}
	}
	if !filepath.IsAbs(sock) {
		sock = ""
	}
	return selfTest, sock
}

// askAgentInbound reaches Den; tests replace it.
var askAgentInbound = func(sock, sessionID, sha, from string, wait time.Duration) (bridge.Decision, error) {
	payload, err := json.Marshal(map[string]string{"sha256": sha, "from": from})
	if err != nil {
		return bridge.Decision{}, err
	}
	env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.AgentInboundEvent, SessionID: sessionID, Payload: payload}
	return bridge.Call(sock, env, wait)
}

func agentInbound(stdin io.Reader, stdout, stderr io.Writer, selfTest bool, sock string, deadline time.Time) (code int) {
	defer func() {
		if recover() != nil {
			fmt.Fprintln(stderr, "den: the agent's inbound hook failed")
			code = 2
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(stdin, 8<<20))
	if err != nil {
		fmt.Fprintln(stderr, "den: reading the hook input:", err)
		return 2
	}
	var in struct {
		Event     string `json:"hook_event_name"`
		SessionID string `json:"session_id"`
		Prompt    string `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || in.Event != "UserPromptSubmit" {
		fmt.Fprintln(stderr, "den: not a UserPromptSubmit hook input")
		return 2
	}
	if !peermsg.HasEnvelope(in.Prompt) {
		return 0
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
	switch {
	case selfTest:
		return block("Den's self-test: peer messages Den did not deliver are blocked")
	case in.SessionID == "":
		return block("Den blocked a peer message: no session to check it for")
	case sock == "":
		return block("Den blocked a peer message: its hook has no den.sock to ask")
	case wait <= 0:
		return block("Den did not answer in time; the peer message was not delivered")
	}
	from := ""
	if e, err := peermsg.Parse(in.Prompt); err == nil {
		from = e.From
	}
	d, err := askAgentInbound(sock, in.SessionID, peermsg.Hash(in.Prompt), from, wait)
	if err != nil || d.Behavior != "allow" {
		return block("Den did not deliver this peer message, so it was blocked")
	}
	return 0
}
