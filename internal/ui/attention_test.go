package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/notify"
	"github.com/GlacierHubAB/den/internal/orch"
)

// attentionModel is the tree model with notifications recorded instead of
// shown: the orchestrator directs Snug Capybara, who leads Mossy Lemur
// (needs you) and Fuzzy Sprite (idle).
func attentionModel(t *testing.T) (*Model, *[]string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := treeModel()
	m.cfg = config.Defaults()
	m.att = newAttention()
	var sent []string
	m.att.send = func(n notify.Note) bool {
		sent = append(sent, n.Summary+"|"+n.Body)
		return false
	}
	m.att.looking = func(string) bool { return false }
	return &m, &sent
}

// runAll executes a command and everything it batches, as bubbletea would.
func runAll(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if b, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range b {
			runAll(c)
		}
	}
}

func TestNeedsYouNotifiesOncePerWait(t *testing.T) {
	m, sent := attentionModel(t)
	perm := req("PermissionRequest", "fuzzy-sprite", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "cat ~/.ssh/id_ed25519", "description": "Read the deploy key"}})
	runAll(m.handleRequest(perm))
	if len(*sent) != 1 || (*sent)[0] != "Fuzzy Sprite needs your permission|" {
		t.Fatalf("sent = %q", *sent)
	}
	runAll(m.handleRequest(req("PreToolUse", "fuzzy-sprite", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []map[string]any{{"question": "Which key?"}}}})))
	if len(*sent) != 1 {
		t.Errorf("a second request while one is out stays quiet: %q", *sent)
	}
	runAll(m.handleRequest(req("UserPromptSubmit", "fuzzy-sprite", map[string]any{"prompt": "go on"})))
	runAll(m.handleRequest(req("PermissionRequest", "fuzzy-sprite", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": "Delete the staging database"}})))
	if len(*sent) != 2 || (*sent)[1] != "Fuzzy Sprite has a plan for you to review|" {
		t.Errorf("a new wait notifies again: %q", *sent)
	}
	for _, s := range *sent {
		for _, leak := range []string{"ssh", "deploy", "staging", "Which key"} {
			if strings.Contains(s, leak) {
				t.Errorf("notification leaks %q: %s", leak, s)
			}
		}
	}
}

func TestNoNotificationWhileLookingOrForTheOrchestrator(t *testing.T) {
	m, sent := attentionModel(t)
	m.att.looking = func(sid string) bool { return sid == "fuzzy-sprite" }
	runAll(m.handleRequest(req("PermissionRequest", "fuzzy-sprite", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})))
	m.orch.proc = &orch.Orchestrator{SessionID: "orch-session"}
	runAll(m.handleRequest(req("PreToolUse", "orch-session", map[string]any{"tool_name": "AskUserQuestion"})))
	m.cfg.Notify.NeedsYou = false
	runAll(m.handleRequest(req("PreToolUse", "maple-owl", map[string]any{"tool_name": "AskUserQuestion"})))
	if len(*sent) != 0 {
		t.Errorf("sent = %q", *sent)
	}
}

func TestDoneNotifiesOnlyTheTopOfABranch(t *testing.T) {
	m, sent := attentionModel(t)
	stop := func(sid string) {
		runAll(m.handleRequest(req("Stop", sid, map[string]any{"last_assistant_message": "All done."})))
	}
	stop("fuzzy-sprite") // off by default
	m.cfg.Notify.Done = true
	stop("fuzzy-sprite")  // a worker reporting to its lead
	stop("snug-capybara") // a lead whose worker still needs the human
	if len(*sent) != 0 {
		t.Fatalf("sent = %q", *sent)
	}
	m.base[0].Status = fleet.Idle // Mossy Lemur is done too
	m.refresh()
	stop("snug-capybara")
	stop("maple-owl") // directed by nobody: reports to the human
	if strings.Join(*sent, ",") != "Snug Capybara finished|,Maple Owl finished|" {
		t.Errorf("sent = %q", *sent)
	}
}

func TestFailuresNotifyOnlyWhenAskedFor(t *testing.T) {
	m, sent := attentionModel(t)
	fail := req("StopFailure", "maple-owl", map[string]any{"error": "rate_limit"})
	runAll(m.handleRequest(fail))
	m.cfg.Notify.Failed = true
	runAll(m.handleRequest(req("StopFailure", "fuzzy-sprite", map[string]any{"error": "overloaded"})))
	if strings.Join(*sent, ",") != "Fuzzy Sprite stopped on an error|" {
		t.Errorf("sent = %q", *sent)
	}
	if m.store.SessionNeedsYou("fuzzy-sprite") {
		t.Error("a failure is never needs you")
	}
}

func TestRemindOnceAfterTheWait(t *testing.T) {
	m, sent := attentionModel(t)
	runAll(m.handleRequest(req("PermissionRequest", "fuzzy-sprite", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})))
	*sent = nil
	now := time.Now()
	runAll(m.checkStuck(now.Add(9 * time.Minute)))
	if len(*sent) != 0 {
		t.Fatalf("too early: %q", *sent)
	}
	runAll(m.checkStuck(now.Add(11 * time.Minute)))
	runAll(m.checkStuck(now.Add(20 * time.Minute)))
	if strings.Join(*sent, ",") != "Fuzzy Sprite still needs your permission|" {
		t.Errorf("sent = %q", *sent)
	}
}

func TestStalledAfterQuietWork(t *testing.T) {
	m, sent := attentionModel(t)
	m.hooksReady = true
	now := time.Now()
	m.att.since = now.Add(-time.Hour)
	m.att.lastHook["snug-capybara"] = now.Add(-5 * time.Minute)
	runAll(m.checkStuck(now))
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Stalled {
			t.Fatalf("stalled too early: %+v", it)
		}
	}
	runAll(m.checkStuck(now.Add(20 * time.Minute)))
	var stalled *inbox.Item
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Stalled {
			stalled = it
		}
	}
	if stalled == nil || stalled.SessionID != "snug-capybara" || m.store.SessionNeedsYou("snug-capybara") {
		t.Fatalf("stalled = %+v", stalled)
	}
	if len(*sent) != 0 {
		t.Errorf("stalls do not notify by default: %q", *sent)
	}
	runAll(m.handleRequest(req("PostToolUse", "snug-capybara", map[string]any{"tool_name": "Read"})))
	if stalled.State != inbox.Resolved {
		t.Error("a hook event ends the stall")
	}
	m.cfg.Notify.Stalled = true
	runAll(m.checkStuck(now.Add(time.Hour)))
	if strings.Join(*sent, ",") != "Snug Capybara has gone quiet|" {
		t.Errorf("sent = %q", *sent)
	}
	m.hooksReady = false
	m.store = inbox.New()
	runAll(m.checkStuck(now.Add(2 * time.Hour)))
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Stalled {
			t.Error("without hooks nothing counts as stalled")
		}
	}
}

func TestUnreadCommand(t *testing.T) {
	m, _ := attentionModel(t)
	runAll(m.handleRequest(req("Stop", "maple-owl", map[string]any{"last_assistant_message": "Finished."})))
	m.tab = tabInbox
	m.store.MarkSeen()
	m.inboxCur = len(m.store.Items()) - 1
	next, _ := m.command("/unread")
	mm := next.(Model)
	if mm.store.Unseen() != 1 || !strings.Contains(mm.flash, "Kept unread") {
		t.Fatalf("unseen = %d, flash %q", mm.store.Unseen(), mm.flash)
	}
	if !strings.Contains(mm.View().Content, "Inbox") {
		t.Error("view renders")
	}
	next, _ = mm.command("/unread")
	if mm := next.(Model); mm.store.Unseen() != 0 {
		t.Error("/unread again marks it read")
	}
}

func TestOnCommandsAndDensOwnEvents(t *testing.T) {
	m, _ := attentionModel(t)
	out := filepath.Join(t.TempDir(), "on")
	m.cfg.On.NeedsYou = `echo "$DEN_EVENT $DEN_AGENT $DEN_REQUEST" >> ` + out
	m.cfg.On.Stalled = `cat >> ` + out
	runAll(m.handleRequest(req("PermissionRequest", "fuzzy-sprite", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf build"}})))
	runAll(m.handleRequest(req("Stop", "fuzzy-sprite", map[string]any{"last_assistant_message": "ok"})))
	m.orch.proc = &orch.Orchestrator{SessionID: "orch-session"}
	runAll(m.handleRequest(req("PreToolUse", "orch-session", map[string]any{"tool_name": "AskUserQuestion"})))
	m.hooksReady = true
	m.att.since = time.Now().Add(-time.Hour)
	runAll(m.checkStuck(time.Now()))
	raw, _ := os.ReadFile(out)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || lines[0] != "needs_you Fuzzy Sprite permission" || !strings.Contains(lines[1], `"kind":"stalled"`) || !strings.Contains(lines[1], `"agent":"Snug Capybara"`) {
		t.Errorf("on commands ran with %q", lines)
	}
	if strings.Contains(string(raw), "rm -rf") {
		t.Error("the command text leaked")
	}
	stream, _ := os.ReadFile(events.Path())
	if strings.Count(string(stream), "\n") != 1 || !strings.Contains(string(stream), `"kind":"stalled","session":"snug-capybara"`) {
		t.Errorf("Den writes only what den hook cannot know: %q", stream)
	}
}
