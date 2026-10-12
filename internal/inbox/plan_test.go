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

}

// Plans are approved one at a time: /always does not answer one, and no
// always-rule of the session, however it came about, approves a later plan.
func TestPlanNeverAlwaysAllowed(t *testing.T) {
	store := New()
	r := fixture(t, "permission-exitplanmode")
	it := store.Apply(r)
	if store.Answer(it, "always") || !it.Answerable() {
		t.Fatalf("/always answered a plan: %+v", it)
	}
	if _, ok := r.Decided(); ok {
		t.Error("the hook got a decision")
	}
	if allowKey(bridge.PlanTool, r.Input.ToolInput) != "" {
		t.Error("a plan has an always-rule key")
	}
	store.always[r.Input.SessionID] = map[string]bool{bridge.PlanTool: true, "": true}
	r2 := fixture(t, "permission-exitplanmode")
	if it := store.Apply(r2); it == nil || !it.Answerable() {
		t.Errorf("a later plan was not held for the human: %+v", it)
	}
	if _, ok := r2.Decided(); ok {
		t.Error("a later plan was answered by a rule")
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
