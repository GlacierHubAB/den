package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/closer"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// publish is the snapshot Den hands the API now.
func publish(t *testing.T, m *Model) api.Snapshot {
	t.Helper()
	var snap api.Snapshot
	publishedHook = func(s api.Snapshot) { snap = s }
	defer func() { publishedHook = nil }()
	m.publishAPI()
	return snap
}

// cardIn is the card of id in snap, if listed.
func cardIn(snap api.Snapshot, id string) (api.Card, bool) {
	for _, c := range snap.Cards {
		if c.ID == id {
			return c, true
		}
	}
	return api.Card{}, false
}

// settledIn is how snap says id settled, if it does.
func settledIn(snap api.Snapshot, id string) (api.Settled, bool) {
	for _, s := range snap.Settled {
		if s.ID == id {
			return s, true
		}
	}
	return api.Settled{}, false
}

// mirrored records what Den hands the mirror.
func mirrored(t *testing.T) *[]api.Converse {
	t.Helper()
	var got []api.Converse
	old := mirrorSink
	mirrorSink = func(_ *api.Server, c api.Converse) { got = append(got, c) }
	t.Cleanup(func() { mirrorSink = old })
	return &got
}

// startedClose is a close of Maple a console answered: the model, the
// item, the close's run, its card as listed before the answer, and the
// mirror's record.
func startedClose(t *testing.T) (Model, *inbox.Item, []closeDoneMsg, api.Card, *[]api.Converse) {
	t.Helper()
	ops := &fakeCloseOps{}
	closeOps = ops
	t.Cleanup(func() { closeOps = closer.System{} })
	mirror := mirrored(t)
	m := closeModel(t)
	mm, _ := m.closeCommand([]string{"maple"})
	m = mm.(Model)
	it := closeItems(m)[0]
	m.tab, m.inboxSel = tabFleet, ""
	listed, ok := cardIn(publish(t, &m), it.ID)
	if !ok || listed.Started {
		t.Fatalf("before the answer: %+v listed %v", listed, ok)
	}
	r, run := dockAnswerCmd(&m, it, "allow")
	if r.Result != "allowed" || !it.Started {
		t.Fatalf("the allow: %+v", r)
	}
	return m, it, closeDones(run), listed, mirror
}

// A console's allow on a close: the card stays listed, started, by the
// console, with no verbs and its hash unchanged, and takes no answer;
// when the close ends it leaves the list, settled once with its outcome,
// and Den says so in its chat and in the mirror, by name only.
func TestStartedCloseCardIsListedThenSettledDone(t *testing.T) {
	m, it, dones, listed, mirror := startedClose(t)
	snap := publish(t, &m)
	c, ok := cardIn(snap, it.ID)
	if !ok || !c.Started || c.StartedBy != "console" || c.DockAnswers || len(c.Verbs) != 0 {
		t.Fatalf("while it runs: %+v listed %v", c, ok)
	}
	if api.ProjectCard(c).Hash != api.ProjectCard(listed).Hash {
		t.Errorf("the started card's hash changed")
	}
	if _, ok := settledIn(snap, it.ID); ok {
		t.Error("settled while it runs")
	}
	if r, _ := dockAnswerCmd(&m, it, "allow"); r.Code != api.CodeNotFound {
		t.Errorf("a second allow: %+v", r)
	}
	if len(dones) != 1 {
		t.Fatalf("runs %d", len(dones))
	}
	mm, _ := m.Update(dones[0])
	m = mm.(Model)
	snap = publish(t, &m)
	if _, ok := cardIn(snap, it.ID); ok {
		t.Error("still listed after the close")
	}
	if st, _ := settledIn(snap, it.ID); st != (api.Settled{ID: it.ID, Verb: "allow", By: "console", Outcome: "done"}) {
		t.Errorf("settled %+v", st)
	}
	if len(m.api.started) != 0 {
		t.Errorf("started cards kept: %v", m.api.started)
	}
	if len(*mirror) != 1 {
		t.Fatalf("mirror %+v", *mirror)
	}
	e := (*mirror)[0]
	if e.Origin != api.OriginCard || e.Card != it.ID || e.End == nil || e.End.Action != "close" || strings.Join(e.End.Done, ",") != "Maple Owl" || len(e.End.Failed) != 0 || e.End.Lost {
		t.Errorf("mirror entry %+v %+v", e, e.End)
	}
	if n := chatEnds(m); len(n) != 1 || n[0].status != "" {
		t.Errorf("chat end lines %+v", n)
	}
}

// chatEnds are Den's chat lines for a console-answered action's end.
func chatEnds(m Model) []chatEntry {
	var out []chatEntry
	for _, e := range m.orch.chat {
		if e.role == "note" && strings.Contains(e.text, " the console answered ended: ") {
			out = append(out, e)
		}
	}
	return out
}

// A close that leaves an agent there settles failed: Den's chat line and
// flash show it as a failure, and the mirror names who was not closed.
func TestStartedCloseThatFailsSettlesFailed(t *testing.T) {
	m, it, dones, _, mirror := startedClose(t)
	publish(t, &m)
	done := dones[0]
	done.results = append([]closer.Result(nil), done.results...)
	done.results[0].Ended, done.results[0].Err = false, errors.New("operation not permitted")
	mm, _ := m.Update(done)
	m = mm.(Model)
	snap := publish(t, &m)
	if st, _ := settledIn(snap, it.ID); st.Outcome != "failed" || st.By != "console" {
		t.Errorf("settled %+v", st)
	}
	if !m.flashErr {
		t.Errorf("the flash %q is not an error", m.flash)
	}
	if n := chatEnds(m); len(n) != 1 || !strings.HasPrefix(n[0].status, "failed") || !strings.Contains(n[0].text, "operation not permitted") {
		t.Errorf("chat end lines %+v", n)
	}
	if len(*mirror) != 1 || len((*mirror)[0].End.Failed) != 1 || len((*mirror)[0].End.Done) != 0 {
		t.Fatalf("mirror %+v", *mirror)
	}
}

// A close that never reports settles lost, once: its plan and its frozen
// card go with it, and the close reporting late changes nothing.
func TestStartedCloseLostThenReportingLate(t *testing.T) {
	m, it, dones, _, mirror := startedClose(t)
	publish(t, &m)
	it.StartedUntil = time.Now().Add(-time.Second)
	m.loseStarted()
	snap := publish(t, &m)
	if st, _ := settledIn(snap, it.ID); st != (api.Settled{ID: it.ID, Verb: "allow", By: "console", Outcome: "lost"}) {
		t.Errorf("settled %+v", st)
	}
	if len(m.api.started) != 0 || m.closing[it.ID].Targets != nil {
		t.Errorf("kept after lost: started %v, plan %+v", m.api.started, m.closing[it.ID])
	}
	if len(*mirror) != 1 || !(*mirror)[0].End.Lost || strings.Join((*mirror)[0].End.Done, ",") != "Maple Owl" {
		t.Fatalf("mirror %+v", *mirror)
	}
	resolved := it.Resolved
	mm, _ := m.Update(dones[0])
	m = mm.(Model)
	snap = publish(t, &m)
	if st, _ := settledIn(snap, it.ID); st.Outcome != "lost" || it.Resolved != resolved {
		t.Errorf("after a late report: %+v %q", st, it.Resolved)
	}
	if len(*mirror) != 1 || len(chatEnds(m)) != 0 {
		t.Errorf("a late report told again: mirror %d, chat %+v", len(*mirror), chatEnds(m))
	}
}

// Den's own ^Y on a close is started by den and settled by den; nothing
// goes to the mirror or the chat about a console's answer.
func TestDenStartedCloseIsByDen(t *testing.T) {
	ops := &fakeCloseOps{}
	closeOps = ops
	t.Cleanup(func() { closeOps = closer.System{} })
	mirror := mirrored(t)
	m := closeModel(t)
	mm, _ := m.closeCommand([]string{"maple"})
	m = mm.(Model)
	it := closeItems(m)[0]
	mm, cmd := m.answer("allow")
	m = mm.(Model)
	if c, ok := cardIn(publish(t, &m), it.ID); !ok || !c.Started || c.StartedBy != "den" {
		t.Fatalf("while it runs: %+v %v", c, ok)
	}
	mm, _ = m.Update(closeDones(cmd)[0])
	m = mm.(Model)
	if st, _ := settledIn(publish(t, &m), it.ID); st != (api.Settled{ID: it.ID, Verb: "allow", By: "den", Outcome: "done"}) {
		t.Errorf("settled %+v", st)
	}
	if len(*mirror) != 0 || len(chatEnds(m)) != 0 {
		t.Errorf("told as a console's: mirror %+v chat %+v", *mirror, chatEnds(m))
	}
}

// A started card is as it was when it started: renaming its agent
// meanwhile changes neither its text nor its hash.
func TestStartedCardIsFrozen(t *testing.T) {
	m, it, _, _, _ := startedClose(t)
	before, _ := cardIn(publish(t, &m), it.ID)
	for i := range m.base {
		if strings.HasPrefix(m.base[i].Name, "Maple") {
			m.base[i].Name = "Ember Fox"
		}
	}
	m.refresh()
	after, ok := cardIn(publish(t, &m), it.ID)
	if !ok || after.Agent != before.Agent || api.ProjectCard(after).Hash != api.ProjectCard(before).Hash {
		t.Errorf("before %+v\nafter %+v", before, after)
	}
}

// Only a card a console may answer becomes a started card: a host's close
// and a kind only Den answers leave the list when they start, settled by
// their answer, never listed as started.
func TestOnlyDockCardsAreListedStarted(t *testing.T) {
	m := closeModel(t)
	host := m.store.AddCloseOn("devbox", "s-remote", "Close Mossy Lemur@devbox?", []string{"Mossy Lemur@devbox"}, nil)
	install := m.store.AddInstall("devbox", "Install Den's hooks on devbox?", nil, nil)
	for _, it := range []*inbox.Item{host, install} {
		it.Answer, it.AnsweredBy = "allow", "den"
		m.store.Start(it, "working…", time.Minute)
	}
	snap := publish(t, &m)
	for _, it := range []*inbox.Item{host, install} {
		if c, ok := cardIn(snap, it.ID); ok {
			t.Errorf("%s listed: %+v", it.Title, c)
		}
		if st, ok := settledIn(snap, it.ID); !ok || st != (api.Settled{ID: it.ID, Verb: "allow", By: "den"}) {
			t.Errorf("%s settled %+v %v", it.Title, st, ok)
		}
	}
	if len(m.api.started) != 0 {
		t.Errorf("started cards: %v", m.api.started)
	}
}
