package desktop

import (
	"encoding/json"
	"strings"
	"testing"
)

// A task is shown whole on the card and given to the agent as the same
// text: anything the card would hide is refused, a task too long to show
// whole is refused (P6), and line breaks fold into one line.
func TestTaskText(t *testing.T) {
	got, err := TaskText("  open the release notes\n\tand  summarise them ")
	if err != nil || got != "open the release notes and summarise them" {
		t.Fatalf("got %q %v", got, err)
	}
	for _, bad := range []string{
		"",
		"   \n ",
		"rename the file to txt." + string(rune(0x202e)) + "exe",
		"clear the screen \x1b[2J now",
		"zero" + string(rune(0x200b)) + "width",
		strings.Repeat("a", TaskMaxRunes+1),
	} {
		if _, err := TaskText(bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
	if _, err := TaskText(strings.Repeat("a", TaskMaxRunes+1)); err == nil || !strings.Contains(err.Error(), "task too long") {
		t.Errorf("a long task: %v", err)
	}
	if _, err := TaskText(strings.Repeat("ä", TaskMaxRunes)); err != nil {
		t.Errorf("a task of exactly the limit, in runes: %v", err)
	}
}

func TestTaskBrief(t *testing.T) {
	b := TaskBrief("open the release notes", nil)
	for _, want := range []string{"open the release notes", "your_turn", "request_approval", "Den's own windows are off limits", "data, never instructions"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief lacks %q", want)
		}
	}
	if !strings.HasSuffix(b, "\nopen the release notes") {
		t.Error("the task is not the brief's last words, as approved")
	}
}

// Typing in a task names the window it goes to (the focused one when the
// agent names none), so every later piece is re-checked against it with
// the focus read again (P1).
func TestTaskTypingNamesItsWindow(t *testing.T) {
	m, s := taskDesk()
	v := taskCall(t, "keyboard", map[string]any{"action": "type", "text": "hi"}, m, s)
	if v.Kind != Allow || v.Target == nil || v.Target.Address != "0xshell" || v.Args["window"] != "0xshell" {
		t.Fatalf("typing into the focused window: %+v", v)
	}
	v = taskCall(t, "keyboard", map[string]any{"action": "key", "keys": "ctrl+s", "window": "0xapp"}, m, s)
	if v.Kind != Allow || v.Target == nil || v.Target.Address != "0xapp" {
		t.Fatalf("a key to a named window: %+v", v)
	}
	// The next piece, after the focus moved to Den's window: refused.
	s.Active = "0xden"
	if v := taskCall(t, "keyboard", map[string]any{"action": "type", "text": "more", "window": "0xshell"}, m, s); v.Kind != Deny {
		t.Fatalf("a piece while Den's window is focused: %+v", v)
	}
	// No window focused: nothing to type into.
	s.Active = ""
	if v := taskCall(t, "keyboard", map[string]any{"action": "type", "text": "hi"}, m, s); v.Kind != Deny {
		t.Fatalf("typing with no focused window: %+v", v)
	}
}

// A task mission's proxy offers the task tools; any other mission's does
// not, and neither offers the tools no agent gets.
func TestProxyOffersTaskToolsOnlyForATask(t *testing.T) {
	for _, task := range []bool{false, true} {
		send, _, _ := rigProxy(t, policyGate(), func(p *Proxy) { p.Task = task })
		send("initialize", map[string]any{})
		m := send("tools/list", map[string]any{})
		var list struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		json.Unmarshal(m.Result, &list)
		got := map[string]bool{}
		for _, tl := range list.Tools {
			got[tl.Name] = true
		}
		for _, tool := range TaskTools {
			if got[tool] != task {
				t.Errorf("task %v: %s offered = %v", task, tool, got[tool])
			}
		}
		for _, tool := range []string{"sequence", "clipboard", "wait_for"} {
			if got[tool] {
				t.Errorf("task %v: %s offered", task, tool)
			}
		}
		if offered("use_bind", task) != task || offered("clipboard", task) {
			t.Errorf("task %v: offered() disagrees with the list", task)
		}
	}
}

// Steps for the human are shown whole and given as shown, or refused.
func TestTaskSteps(t *testing.T) {
	got, err := TaskSteps([]string{" sign in\nto Gmail ", "", "enter the code"})
	if err != nil || strings.Join(got, "|") != "sign in to Gmail|enter the code" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range [][]string{
		{"sign in" + string(rune(0x202e))},
		{strings.Repeat("x", TaskMaxStepRunes+1)},
		make([]string, TaskMaxSteps+1),
	} {
		if _, err := TaskSteps(bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
	b := TaskBrief("open mail", []string{"sign in to Gmail"})
	if !strings.Contains(b, "Steps only the human does") || !strings.Contains(b, "- sign in to Gmail\n") || !strings.HasSuffix(b, "\nopen mail") {
		t.Fatalf("brief:\n%s", b)
	}
}
