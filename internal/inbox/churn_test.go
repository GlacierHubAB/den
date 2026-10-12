package inbox

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// A host's held requests that it lets go are capped as they arrive (per
// host, a minute) and pruned like resolved items: a host churning held and
// gone pairs cannot keep thousands of items in the inbox.
func TestLetGoRemoteItemsAreCappedAndPruned(t *testing.T) {
	s := New()
	sid := "11111111-2222-4333-8444-555555555555"
	refused := 0
	for i := range 5000 {
		payload, _ := json.Marshal(map[string]any{"session_id": sid, "hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]string{"command": "ls"}})
		r := bridge.NewRemoteRequest("evil", fmt.Sprintf("%032x", i), bridge.Envelope{Event: "PermissionRequest", SessionID: sid, Payload: payload}, func(bridge.Decision) {})
		if s.Apply(r) == nil {
			if d, ok := r.Decided(); !ok || d.Behavior != "" {
				t.Fatalf("a held request over the cap was answered %+v %v, want back to its terminal", d, ok)
			}
			refused++
		}
		r.LetGo("no longer waiting on evil")
		s.LetGo(0)
	}
	if n := len(s.Items()); n > MaxRemoteHeldNew || refused < 5000-MaxRemoteHeldNew {
		t.Errorf("%d items kept, %d refused, from 5000 held requests in a minute", n, refused)
	}
	s.Prune(time.Nanosecond, 10)
	if n := len(s.Items()); n != 0 {
		t.Errorf("%d let-go remote items kept after Prune", n)
	}
	// A local item in its terminal stays: only its terminal answers it here.
	local := bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: sid, Payload: []byte(`{"session_id":"` + sid + `","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`)})
	it := s.Apply(local)
	s.Release(it, "you chose the terminal")
	s.Prune(time.Nanosecond, 10)
	if len(s.Items()) != 1 {
		t.Error("a local item in its terminal was pruned")
	}
}
