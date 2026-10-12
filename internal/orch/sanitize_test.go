package orch

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamTextIsSanitised(t *testing.T) {
	const evil = `\u001b]52;c;SGVsbG8=\u0007\u001b[2J\u001b]0;pwned\u0007\u202e\r`
	stream := strings.Join([]string{
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"part` + evil + `"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"whole` + evil + `"},{"type":"tool_use","id":"t1","name":"Send` + evil + `","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"result` + evil + `"}]}}`,
		`{"type":"result","result":"done` + evil + `"}`,
		`{"type":"error","error":"bad` + evil + `"}`,
	}, "\n")
	o := &Orchestrator{events: make(chan Event, 16)}
	o.readStdout(strings.NewReader(stream))
	close(o.events)
	n := 0
	for e := range o.events {
		n++
		if strings.ContainsAny(e.Text+e.Tool, "\x1b\x07\r\u202e") || strings.Contains(e.Text, "pwned") {
			t.Errorf("%v event keeps an injected sequence: %q %q", e.Kind, e.Text, e.Tool)
		}
	}
	if n < 5 {
		t.Errorf("only %d events", n)
	}
	in, _ := json.Marshal(map[string]any{"peers": []string{"peer\x1b]0;pwned\x07"}, "reason": "idle\u202e"})
	if d := Describe(CloseAgentTool, in); strings.ContainsAny(d, "\x1b\x07\u202e") || strings.Contains(d, "pwned") {
		t.Errorf("Describe = %q", d)
	}
}
