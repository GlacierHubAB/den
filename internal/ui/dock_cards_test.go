package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
)

// permissionCard puts an agent's permission request in the inbox.
func permissionCard(t *testing.T, m *Model) *inbox.Item {
	t.Helper()
	sid := m.agents[0].Session.SessionID
	p, _ := json.Marshal(map[string]any{"session_id": sid, "hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]any{"command": "make test"}})
	r := bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: sid, Payload: p})
	m.store.Apply(r)
	m.refresh()
	for _, it := range m.store.Items() {
		if it.Kind == inbox.Permission && it.State != inbox.Resolved {
			return it
		}
	}
	t.Fatal("no permission card")
	return nil
}

func dockAnswer(m *Model, it *inbox.Item, hash, verb string) api.CardResult {
	a := &api.CardAnswer{ID: it.ID, Hash: hash, Verb: verb, Console: "drey-host", Reply: make(chan api.CardResult, 1)}
	m.answerFromDock(a)
	return <-a.Reply
}

// A console's answer goes the way the human's ^Y does, and Den's chat says
// who answered; a changed card, a card Den has open, or a kind the console
// may not answer is refused.
func TestAnswerFromDock(t *testing.T) {
	m := testModel(140, 40)
	m.base = append(m.base, m.agents...)
	it := permissionCard(t, &m)
	hash := api.ProjectCard(m.dockCard(it)).Hash
	if r := dockAnswer(&m, it, strings.Repeat("0", 64), "allow"); r.Code != api.CodeCardChanged {
		t.Errorf("a stale hash: %+v", r)
	}
	m.tab, m.inboxSel = tabInbox, it.ID
	if r := dockAnswer(&m, it, hash, "allow"); r.Code != api.CodeUnavailable {
		t.Errorf("a card open in Den: %+v", r)
	}
	m.tab, m.inboxSel = tabFleet, ""
	r := dockAnswer(&m, it, hash, "allow")
	if r.Code != "" || r.Result != "allowed" || it.State != inbox.Resolved {
		t.Fatalf("answer %+v, state %v", r, it.State)
	}
	found := false
	for _, e := range m.orch.chat {
		found = found || strings.HasPrefix(e.text, "Allowed from drey-host: ")
	}
	if !found {
		t.Error("no chat note naming the console")
	}
	if r := dockAnswer(&m, it, hash, "allow"); r.Code != api.CodeNotFound {
		t.Errorf("an answered card: %+v", r)
	}
	// Kinds a console never answers.
	for _, k := range []inbox.Kind{inbox.Console, inbox.Pair, inbox.APIOn, inbox.Guidance, inbox.Install, inbox.Question, inbox.Grant, inbox.Skills, inbox.Message} {
		if dockMayAnswer(&inbox.Item{Kind: k, SessionID: "x"}) {
			t.Errorf("%v may be answered from the dock", k)
		}
	}
	// Den's own cards about a host carry it in their session id.
	if dockMayAnswer(&inbox.Item{Kind: inbox.MissionUpdate, SessionID: inbox.HostPrefix + "devbox"}) {
		t.Error("a card about a host may be answered from the dock")
	}
}

// A host's request, made the way the relay makes one (a held request with
// its host), is never the console's: refused, and its card says so.
func TestDockRefusesAHostsRequest(t *testing.T) {
	m := testModel(140, 40)
	p, _ := json.Marshal(map[string]any{"session_id": "remote-1", "hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	r := bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: "remote-1", Payload: p})
	r.Host = "devbox"
	it := m.store.Apply(r)
	if it == nil || it.Host != "devbox" {
		t.Fatalf("relayed item %+v", it)
	}
	if dockMayAnswer(it) || m.dockCard(it).DockAnswers {
		t.Error("a host's request may be answered from the dock")
	}
	if it.Answerable() {
		hash := api.ProjectCard(m.dockCard(it)).Hash
		if r := dockAnswer(&m, it, hash, "allow"); r.Code != api.CodeRefusedFromDock {
			t.Errorf("a host's request answered from the dock: %+v", r)
		}
	}
}

// The card carries what Den shows when deciding: the tool, the intent above
// the body, and a twin's warning.
func TestDockCardCarriesWhatDenShows(t *testing.T) {
	m := testModel(140, 40)
	m.base = append(m.base, m.agents...)
	it := permissionCard(t, &m)
	it.Intent = "build and test"
	c := m.dockCard(it)
	if c.Tool != "Bash" || !strings.HasPrefix(c.Body, "build and test\n") {
		t.Errorf("card %+v", c)
	}
	h1 := api.ProjectCard(c).Hash
	it.Tool = "Write"
	if api.ProjectCard(m.dockCard(it)).Hash == h1 {
		t.Error("the tool is not in the hash")
	}
}
