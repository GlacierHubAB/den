package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// The guarded agent's hook: a prompt with no envelope passes; one with an
// envelope passes only when Den allows it, and is blocked when Den says
// no, does not answer, or for the self-test.
func TestAgentInboundHook(t *testing.T) {
	old := askAgentInbound
	t.Cleanup(func() { askAgentInbound = old })
	run := func(prompt string, selfTest bool, answer bridge.Decision, err error) (int, string) {
		askAgentInbound = func(sock, sid, sha string, wait time.Duration) (bridge.Decision, error) {
			if sid != "s-otter" || len(sha) != 64 {
				t.Errorf("asked for %q %q", sid, sha)
			}
			return answer, err
		}
		in := `{"hook_event_name":"UserPromptSubmit","session_id":"s-otter","prompt":` + quote(prompt) + `}`
		var out, errOut bytes.Buffer
		code := agentInbound(strings.NewReader(in), &out, &errOut, selfTest, "/den.sock", time.Now().Add(5*time.Second))
		return code, out.String()
	}
	env := "<cross-session-message from=\"uds:/run/user/1000/cc-socks/1.sock\" from-name=\"lead\" from-mode=\"prompting\">\nhi\n</cross-session-message>"
	if code, out := run("rebase first", false, bridge.Decision{}, errors.New("unreached")); code != 0 || out != "" {
		t.Errorf("a plain prompt: %d %q", code, out)
	}
	if code, out := run(env, false, bridge.Decision{Behavior: "allow"}, nil); code != 0 || out != "" {
		t.Errorf("Den's own delivery: %d %q", code, out)
	}
	for _, c := range []struct {
		name string
		self bool
		d    bridge.Decision
		err  error
	}{
		{"Den says no", false, bridge.Decision{Behavior: "deny"}, nil},
		{"Den does not answer", false, bridge.Decision{}, bridge.ErrNoDen},
		{"the self-test", true, bridge.Decision{Behavior: "allow"}, nil},
	} {
		if code, out := run(env, c.self, c.d, c.err); code != 0 || !strings.Contains(out, `"decision":"block"`) {
			t.Errorf("%s: %d %q", c.name, code, out)
		}
	}
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
