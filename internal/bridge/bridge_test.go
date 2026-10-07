package bridge

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestSendAndAskRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den.sock")
	srv, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	if _, err := Listen(path); err != ErrAlreadyRunning {
		t.Errorf("second listen = %v, want ErrAlreadyRunning", err)
	}

	payload, _ := json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "Stop", "last_assistant_message": "done"})
	if !Send(path, Envelope{Seq: 1, Event: "Stop", SessionID: "s1", Payload: payload}) {
		t.Fatal("send failed")
	}
	req := <-srv.Requests()
	if req.Event != "Stop" || req.Input.LastAssistant != "done" || req.WantsReply() {
		t.Errorf("got %+v", req.Envelope)
	}

	go func() {
		r := <-srv.Requests()
		if r.Input.ToolName != "Bash" || !r.WantsReply() {
			t.Errorf("permission request = %+v", r.Input)
		}
		r.Reply(Decision{Behavior: "allow"})
		r.Reply(Decision{Behavior: "deny"}) // ignored
	}()
	payload, _ = json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]string{"command": "ls"}})
	d, ok := Ask(path, Envelope{Seq: 2, Event: "PermissionRequest", SessionID: "s1", Payload: payload}, 2*time.Second)
	if !ok || d.Behavior != "allow" {
		t.Errorf("ask = %+v %v", d, ok)
	}

	if Send(filepath.Join(t.TempDir(), "missing.sock"), Envelope{}) {
		t.Error("send to missing socket should fail open")
	}
}
