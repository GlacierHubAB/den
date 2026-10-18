package orch

import (
	"strings"
	"testing"
)

// A desktop request from the human goes to desktop_mission with a task by
// default; plan and apps only for configured apps or the restricted mode.
// The control prompt, made from the same rules, says it too.
func TestPromptsPreferADesktopTask(t *testing.T) {
	for name, p := range map[string]string{"plain": Prompt(""), "control": PromptFor("", true)} {
		i := strings.Index(p, "- Desktop: ")
		if i < 0 {
			t.Fatalf("%s: no desktop rule", name)
		}
		rule := p[i:]
		rule = rule[:strings.Index(rule, "\n")]
		for _, want := range []string{"use desktop_mission with task", "one plain sentence", "Do not pass plan or apps with a task", "only when the human names apps from their [desktop.apps] config or asks for the restricted mode", "Never put credentials in a task"} {
			if !strings.Contains(rule, want) {
				t.Errorf("%s: the desktop rule lacks %q", name, want)
			}
		}
	}
}
