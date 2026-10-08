package orch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeClaude speaks just enough stream-json to exercise the parser.
const fakeClaude = `#!/bin/sh
echo '{"type":"system","subtype":"init","session_id":"abc123","model":"claude-fable-5-1"}'
while IFS= read -r line; do
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Telling "}}}'
  echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Mossy."}}}'
  echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Telling Mossy."},{"type":"tool_use","id":"t1","name":"SendMessage","input":{"to":"grvlle-45","message":"Rebase on main first.","summary":"rebase"}}]}}'
  echo '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"Sent to grvlle-45."}]}}'
  echo '{"type":"result","subtype":"success","is_error":false,"result":"Sent. Mossy Lemur will rebase first.","total_cost_usd":0.0123}'
done
`

func TestRoundTripWithFakeClaude(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	if err := os.WriteFile(script, []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o, err := Start(ctx, Options{Command: []string{script}, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()

	next := func(k Kind) Event {
		t.Helper()
		for {
			select {
			case e, ok := <-o.Events():
				if !ok {
					t.Fatalf("events closed while waiting for %v", k)
				}
				if e.Kind == k {
					return e
				}
			case <-ctx.Done():
				t.Fatalf("timeout waiting for %v", k)
			}
		}
	}
	if e := next(Started); e.SessionID != "abc123" || e.Model != "claude-fable-5-1" {
		t.Errorf("started = %+v", e)
	}
	if err := o.Send("tell mossy to rebase"); err != nil {
		t.Fatal(err)
	}
	if e := next(Delta); e.Text != "Telling " {
		t.Errorf("delta = %+v", e)
	}
	if e := next(Text); e.Text != "Telling Mossy." {
		t.Errorf("text = %+v", e)
	}
	tu := next(ToolUse)
	in, ok := ParseSendMessage(tu.Input)
	if !ok || tu.Tool != "SendMessage" || in.To != "grvlle-45" || in.Message != "Rebase on main first." {
		t.Errorf("tool_use = %+v %+v", tu, in)
	}
	if e := next(ToolResult); e.ToolUseID != "t1" || e.Text != "Sent to grvlle-45." {
		t.Errorf("tool_result = %+v", e)
	}
	if e := next(Result); e.CostUSD != 0.0123 || e.Text == "" {
		t.Errorf("result = %+v", e)
	}
	o.Close()
	next(Exited)
}
