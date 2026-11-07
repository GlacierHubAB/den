package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// dockAnswerCmd is a console's card.answer on it with its v1 hash: the
// reply and the command the answer started.
func dockAnswerCmd(m *Model, it *inbox.Item, verb string) (api.CardResult, tea.Cmd) {
	hash := api.ProjectCard(m.dockCard(it)).Hash
	a := &api.CardAnswer{ID: it.ID, Hash: hash, Verb: verb, Console: "app-host", Reply: make(chan api.CardResult, 1)}
	cmd := m.answerFromDock(a)
	return <-a.Reply, cmd
}

// settledBy is who the settled event credits for it, "" when none.
func settledBy(m Model, it *inbox.Item) string {
	for _, s := range m.dockSettled() {
		if s.ID == it.ID {
			return s.Verb + " by " + s.By
		}
	}
	return ""
}

// A console's allow on a Close starts the close and says so; the card
// takes no second answer while it runs, from the console or from Den, so
// the close runs once; its end settles it crediting the console.
func TestDockCloseStartsOnceAndCreditsTheConsole(t *testing.T) {
	ops := &fakeCloseOps{}
	closeOps = ops
	t.Cleanup(func() { closeOps = closer.System{} })
	m := closeModel(t)
	mm, _ := m.closeCommand([]string{"maple"})
	m = mm.(Model)
	it := closeItems(m)[0]
	m.tab, m.inboxSel = tabFleet, ""

	r, run := dockAnswerCmd(&m, it, "allow")
	if r.Code != "" || r.Result != "allowed" || !strings.Contains(r.Note, "started; Den settles it when it ends") || !it.Started || it.Answerable() {
		t.Fatalf("first allow: %+v, started %v", r, it.Started)
	}
	for _, verb := range []string{"allow", "deny"} {
		if r, _ := dockAnswerCmd(&m, it, verb); r.Code != api.CodeNotFound {
			t.Errorf("a console's %s while closing: %+v", verb, r)
		}
	}
	m.tab = tabInbox
	m.selectItem(it)
	mm, _ = m.answer("allow")
	m = mm.(Model)
	if !strings.Contains(m.flash, "answered and is closing…") {
		t.Errorf("Den's ^Y while closing: flash %q", m.flash)
	}
	dones := closeDones(run)
	if len(dones) != 1 || strings.Join(ops.signals, ",") != "terminated" {
		t.Fatalf("runs %d, signals %v", len(dones), ops.signals)
	}
	mm, _ = m.Update(dones[0])
	m = mm.(Model)
	if it.State != inbox.Resolved || settledBy(m, it) != "allow by console" {
		t.Errorf("after the close: %v, settled %q", it.State, settledBy(m, it))
	}
}

// Den's own ^Y on a Close is credited to Den once the close ends.
func TestDenCloseIsCreditedWhenItEnds(t *testing.T) {
	ops := &fakeCloseOps{}
	closeOps = ops
	t.Cleanup(func() { closeOps = closer.System{} })
	m := closeModel(t)
	mm, _ := m.closeCommand([]string{"maple"})
	m = mm.(Model)
	it := closeItems(m)[0]
	mm, cmd := m.answer("allow")
	m = mm.(Model)
	if !it.Started || it.Answer != "allow" || it.AnsweredBy != "den" {
		t.Fatalf("while closing: started %v, answer %q by %q", it.Started, it.Answer, it.AnsweredBy)
	}
	dones := closeDones(cmd)
	if len(dones) != 1 {
		t.Fatalf("runs %d", len(dones))
	}
	mm, _ = m.Update(dones[0])
	if got := settledBy(mm.(Model), it); got != "allow by den" {
		t.Errorf("settled %q", got)
	}
}

// A console's allow on a Restore starts the reopen and says so; a second
// answer is refused and the agent is spawned once; the end credits the
// console.
func TestDockRestoreStartsOnceAndCreditsTheConsole(t *testing.T) {
	m, fake := restoreModel(t)
	mm, _ := m.reopenCommand([]string{"Comet", "Newt"})
	m = mm.(Model)
	it := restoreItems(m)[0]
	m.tab, m.inboxSel = tabFleet, ""

	r, run := dockAnswerCmd(&m, it, "allow")
	if r.Code != "" || r.Result != "allowed" || !strings.Contains(r.Note, "started") || !it.Started {
		t.Fatalf("first allow: %+v", r)
	}
	if r, _ := dockAnswerCmd(&m, it, "allow"); r.Code != api.CodeNotFound {
		t.Errorf("a second allow while reopening: %+v", r)
	}
	var done *restoreDoneMsg
	for _, msg := range runCmds(run) {
		if d, ok := msg.(restoreDoneMsg); ok {
			done = &d
		}
	}
	if done == nil || len(fake.reqs) != 1 {
		t.Fatalf("spawns %d", len(fake.reqs))
	}
	mm, _ = m.Update(*done)
	m = mm.(Model)
	if it.State != inbox.Resolved || settledBy(m, it) != "allow by console" {
		t.Errorf("after the reopen: %v, settled %q", it.State, settledBy(m, it))
	}
}

// A started action that never reports is settled lost with a chat note;
// a lost send's note says it may still have gone out.
func TestLostStartedItemsSayWhatToCheck(t *testing.T) {
	m := closeModel(t)
	send := m.store.AddMessageOn("devbox", "s-r", "Send to Comet Newt?", []string{"hello"}, nil)
	m.store.Start(send, "sending…", -time.Hour)
	mm, _ := m.closeCommand([]string{"maple"})
	m = mm.(Model)
	cl := closeItems(m)[0]
	m.store.Start(cl, "closing…", -time.Hour)
	m.loseStarted()
	var notes []string
	for _, e := range m.orch.chat {
		if e.role == "note" && strings.HasPrefix(e.text, "Den lost track of ") {
			notes = append(notes, e.text)
		}
	}
	want := []string{
		"Den lost track of Send to Comet Newt?: it may still have been sent, so check before sending again.",
		"Den lost track of " + cl.Title + ": check the agents.",
	}
	slices.Sort(notes)
	slices.Sort(want)
	if !slices.Equal(notes, want) {
		t.Errorf("notes %q, want %q", notes, want)
	}
	if send.State != inbox.Resolved || cl.State != inbox.Resolved {
		t.Errorf("states %v %v", send.State, cl.State)
	}
}

// closeDones are the close runs a command carries out.
func closeDones(c tea.Cmd) []closeDoneMsg {
	var out []closeDoneMsg
	for _, msg := range runCmds(c) {
		if d, ok := msg.(closeDoneMsg); ok {
			out = append(out, d)
		}
	}
	return out
}
