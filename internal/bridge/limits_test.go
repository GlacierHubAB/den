package bridge

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// A line over MaxLine is never read whole and gets no decision; a request
// past MaxHeld waiting at once gets none either. Both fail open.
func TestSocketLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den.sock")
	srv, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	huge, _ := json.Marshal(map[string]string{"session_id": "s1", "hook_event_name": "PermissionRequest", "tool_input": string(bytes.Repeat([]byte("x"), MaxLine))})
	start := time.Now()
	if _, ok := Ask(path, Envelope{Event: "PermissionRequest", SessionID: "s1", Payload: huge}, 3*time.Second); ok {
		t.Error("an oversized line got a decision")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("an oversized line was held")
	}
	select {
	case r := <-srv.Requests():
		t.Fatalf("an oversized line was delivered: %s", r.Event)
	default:
	}

	old := MaxHeld
	MaxHeld = 2
	t.Cleanup(func() { MaxHeld = old })
	small, _ := json.Marshal(map[string]string{"session_id": "s1", "hook_event_name": "PermissionRequest"})
	got := make(chan bool, 3)
	for range 2 {
		go func() {
			_, ok := Ask(path, Envelope{Event: "PermissionRequest", SessionID: "s1", Payload: small}, 5*time.Second)
			got <- ok
		}()
	}
	held := []*Request{<-srv.Requests(), <-srv.Requests()}
	start = time.Now()
	if _, ok := Ask(path, Envelope{Event: "PermissionRequest", SessionID: "s1", Payload: small}, 3*time.Second); ok || time.Since(start) > 2*time.Second {
		t.Errorf("a request past MaxHeld: ok %v after %v", ok, time.Since(start))
	}
	for _, r := range held {
		r.Reply(Decision{Behavior: "allow"})
	}
	for range 2 {
		if !<-got {
			t.Error("a held request lost its decision")
		}
	}
	// Room again once they are answered.
	go func() { r := <-srv.Requests(); r.Reply(Decision{Behavior: "deny"}) }()
	if d, ok := Ask(path, Envelope{Event: "PermissionRequest", SessionID: "s1", Payload: small}, 3*time.Second); !ok || d.Behavior != "deny" {
		t.Errorf("after the others were answered: %+v %v", d, ok)
	}
}

// Every question Den answers itself, not only a hook's, holds its
// connection for Den's decision: an event missing from the held list
// gets {"ok":true} at once and its caller never hears Den's answer.
func TestDenAnsweredEventsAreHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "den.sock")
	srv, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	for _, ev := range []string{"PermissionRequest", CloseEvent, SkillsEvent, InstallEvent, SendEvent, InboundEvent, MissionEvent, UsageEvent, OrchSendEvent} {
		go func() {
			r := <-srv.Requests()
			r.Reply(Decision{Behavior: "deny", Message: "from Den: " + r.Event})
		}()
		d, ok := Ask(path, Envelope{Event: ev, SessionID: "s1"}, 3*time.Second)
		if !ok || d.Behavior != "deny" || d.Message != "from Den: "+ev {
			t.Errorf("%s: %+v %v", ev, d, ok)
		}
	}
}
