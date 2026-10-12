package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/theme"
)

// evil would write the clipboard (OSC 52), clear the screen and home the
// cursor, retitle the window (OSC 0), plant a hyperlink (OSC 8), overwrite
// the line and flip the reading order, if it reached the terminal.
const evil = "\x1b]52;c;SGVsbG8=\x07\x1b[2J\x1b[H\x1b]0;pwned\x07\x1b]8;;http://example.invalid\x1b\\link\x1b]8;;\x1b\\\r\u202e\u2066"

// leaked names what of evil reached a frame. The frame has Den's own SGR
// styling, so it looks for the injected sequences and payloads only.
func leaked(frame string) []string {
	var out []string
	for _, s := range []string{"]52;", "SGVsbG8", "\x1b[2J", "\x1b[H", "]0;", "pwned", "]8;", "example.invalid", "\x07", "\r", "\u202e", "\u2066"} {
		if strings.Contains(frame, s) {
			out = append(out, s)
		}
	}
	return out
}

func injectedModel(w, h int) Model {
	m := testModel(w, h)
	// A role and task as a less trusted source could send them, without
	// the sanitising fleet.Snapshot does: the frame must still be clean.
	m.agents[0].Labels.Role = "coding agent" + evil
	m.agents[0].Labels.Task = "task" + evil
	m.agents[0].Session.Name = "peer" + evil
	m.agents[0].Session.CWD = "/home/user/repo" + evil
	s1 := m.agents[0].Session.SessionID
	m.store.Apply(req("PermissionRequest", s1, map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "echo " + evil, "description": "say" + evil}}))
	m.store.Apply(req("PreToolUse", s1, map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []map[string]any{{"question": "Which?" + evil, "options": []map[string]string{{"label": "one" + evil}}}}}}))
	m.store.Apply(req("Notification", s1, map[string]any{"notification_type": "idle_prompt", "message": "idle" + evil}))
	m.store.Apply(req("Stop", s1, map[string]any{"last_assistant_message": "Done" + evil}))
	return m
}

// TestHookInjectionNeverReachesTheFrame feeds hook payloads carrying escape
// sequences through the inbox and renders every view.
func TestHookInjectionNeverReachesTheFrame(t *testing.T) {
	for _, tb := range []tab{tabFleet, tabInbox, tabOverview, tabOrch} {
		m := injectedModel(140, 40)
		m.tab = tb
		frame := m.View().Content
		if l := leaked(frame); len(l) > 0 {
			t.Errorf("tab %d: the frame carries %q", tb, l)
		}
	}
	// The inbox item itself was sanitised on the way in, not only the frame.
	for _, it := range injectedModel(140, 40).store.Items() {
		for _, s := range append([]string{it.Title, it.Intent}, append(it.Body, it.Options...)...) {
			if l := leaked(s); len(l) > 0 || strings.Contains(s, "\x1b") {
				t.Errorf("item %q carries %q", s, l)
			}
		}
	}
}

// TestSpike8RendererOutput is spike 8 of the remote design: a role with an
// OSC 52 clipboard write and a screen clear, rendered through bubbletea's
// real renderer into a buffer, must not reach the terminal.
func TestSpike8RendererOutput(t *testing.T) {
	m := injectedModel(120, 30)
	m.tab = tabInbox
	var out bytes.Buffer
	p := tea.NewProgram(frameOnce{m}, tea.WithOutput(&out), tea.WithInput(nil), tea.WithWindowSize(120, 30),
		tea.WithoutSignals(), tea.WithColorProfile(colorprofile.TrueColor), tea.WithEnvironment([]string{"TERM=xterm-256color"}))
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		p.Kill()
		t.Fatal("the program did not finish")
	}
	if !strings.Contains(out.String(), "Mossy Lemur") {
		t.Fatalf("nothing rendered: %q", out.String())
	}
	// The renderer itself clears the screen, so only the payloads are
	// looked for here.
	for _, s := range []string{"]52;", "SGVsbG8", "pwned", "]0;", "example.invalid", "\u202e", "\u2066"} {
		if strings.Contains(out.String(), s) {
			t.Errorf("the terminal received %q", s)
		}
	}
}

// frameOnce renders the model once, then quits.
type frameOnce struct{ m Model }

type quitMsg struct{}

func (f frameOnce) Init() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return quitMsg{} })
}

func (f frameOnce) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(quitMsg); ok {
		return f, tea.Quit
	}
	return f, nil
}

func (f frameOnce) View() tea.View { return f.m.View() }

// TestLongCommandRenders: a one-megabyte single-line command is taken into
// the inbox and drawn quickly, on every view that may show it.
func TestLongCommandRenders(t *testing.T) {
	m := Model{st: newStyles(theme.Deepharness), agents: sample(), store: inbox.New(), width: 140, height: 40}
	m.input = newInput(m.st)
	cmd := strings.Repeat("x", 1<<20) + evil
	start := time.Now()
	m.store.Apply(req("PermissionRequest", m.agents[0].Session.SessionID, map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}))
	for _, tb := range []tab{tabFleet, tabInbox, tabOverview} {
		m.tab = tb
		if l := leaked(m.View().Content); len(l) > 0 {
			t.Errorf("tab %d: the frame carries %q", tb, l)
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
}
