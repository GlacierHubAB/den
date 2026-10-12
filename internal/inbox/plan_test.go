package inbox

import (
	"encoding/json"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// A plan the hook cannot approve (no plan in the call) is not held: Den
// says so and leaves it to the terminal, and ^Y never claims an approval.
func TestPlanDenCannotApprove(t *testing.T) {
	store := New()
	payload, _ := json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "PermissionRequest", "tool_name": "ExitPlanMode", "tool_input": map[string]any{"planFilePath": "/work/p.md"}})
	r := bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: "s1", Payload: payload})
	it := store.Apply(r)
	if it == nil || it.Kind != Plan || it.State != InTerminal || it.Resolved != CannotApprovePlan || it.Answerable() {
		t.Fatalf("plan = %+v", it)
	}
	if d, ok := r.Decided(); !ok || d.Behavior != "" {
		t.Errorf("hook still held or answered: %+v %v", d, ok)
	}
	if store.Answer(it, "allow") {
		t.Error("allowed a plan Den cannot approve")
	}

	// An always-allow for plans in this session does not approve it either.
	ok := fixture(t, "permission-exitplanmode")
	if it := store.Apply(ok); it == nil || !store.Answer(it, "always") {
		t.Fatalf("plan with a plan = %+v", it)
	}
	r = bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: "s1", Payload: payload})
	if it := store.Apply(r); it == nil || it.State != InTerminal {
		t.Errorf("always-allowed a plan Den cannot approve: %+v", it)
	}
}

// The approval of a real plan goes to the hook as an allow; deny keeps its
// message for the agent.
func TestRealPlanDenied(t *testing.T) {
	store := New()
	r := fixture(t, "permission-exitplanmode")
	it := store.Apply(r)
	if !store.Answer(it, "deny") {
		t.Fatal("deny failed")
	}
	if d, _ := r.Decided(); d.Behavior != "deny" || d.Message == "" || it.Resolved != "denied in den" {
		t.Errorf("decision = %+v, item %q", d, it.Resolved)
	}
}
