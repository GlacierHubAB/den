package orch

import (
	"strings"
	"testing"
)

// How the orchestrator staffs work is in its prompt, and in the control
// variant of it: small teams, one change at a time, short-lived branches,
// proof on the human's real setup, the cost of every safeguard, closing the
// loop, and how a lead owns an outcome (its parts, their owners, straight
// hand-offs, quiet updates). Each with its reason, so the model can apply it
// and not recite it.
func TestPromptsStaffWorkInSmallProvenSteps(t *testing.T) {
	principles := map[string][]string{
		"small teams":           {"Small teams.", "one engineer by default", "at most one reviewer (security and code together) per product", "and say why"},
		"one change at a time":  {"One change at a time in a repository area.", "branches that edit the same files", "so nothing is rebased over moving code"},
		"short-lived branches":  {"Short-lived branches.", "as soon as its checks pass", "queueing branches"},
		"proof on the real run": {"Prove it on the real setup.", "the human's own scenario has been shown working on their real setup", "installed and run", "tests against fakes", "pass while the real machine fails"},
		"safeguard cost":        {"Weigh the cost of every safeguard.", "goes to the human as a choice with its trade-off", "a few sturdy safeguards"},
		"close the loop":        {"Close the loop.", "what is finished, what is not, and which agents are no longer needed"},
		"own the outcome":       {"Own the outcome.", "end to end and across every product or repository it touches", "a checklist of the parts (a mission's sub-goals, each with an owner)", "its interface written down and agreed before either side builds"},
		"check the parts":       {"Check the parts.", "chases any part that is not moving", "nothing waits silently on another team"},
		"straight to review":    {"Straight to review.", "hand finished work directly to review and CI", "only when something is blocked or needs the human"},
		"quiet updates":         {"Quiet updates.", "milestones (\"works, try it\")", "not about every merge"},
		"the lead's own people": {"the lead starts them itself with its own spawn_agent tool", "Do not start the lead's people yourself"},
	}
	for name, prompt := range map[string]string{"orchestrator": SystemPrompt, "control": ControlSystemPrompt} {
		for what, phrases := range principles {
			for _, p := range phrases {
				if !strings.Contains(prompt, p) {
					t.Errorf("the %s prompt lost %s: no %q", name, what, p)
				}
			}
		}
		if !strings.Contains(prompt, "put these principles in any lead's brief.") {
			t.Errorf("the %s prompt does not tell the orchestrator to pass the principles to a lead", name)
		}
	}
	// What the staffing rules say must not name a tool the control variant
	// swaps out.
	start := strings.Index(SystemPrompt, "- Staffing:")
	end := strings.Index(SystemPrompt, "- When the human wants agents closed")
	if start < 0 || end < start {
		t.Fatal("the staffing rules moved")
	}
	if block := SystemPrompt[start:end]; strings.Contains(block, "SendMessage") {
		t.Error("the staffing rules name SendMessage, which the control variant rewrites")
	}
}
