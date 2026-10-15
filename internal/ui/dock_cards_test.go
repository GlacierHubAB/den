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
	for _, k := range []inbox.Kind{inbox.Console, inbox.Pair, inbox.APIOn, inbox.Guidance, inbox.Install, inbox.Question} {
		if dockMayAnswer(&inbox.Item{Kind: k, SessionID: "x"}) {
			t.Errorf("%v may be answered from the dock", k)
		}
	}
	if dockMayAnswer(&inbox.Item{Kind: inbox.Permission, SessionID: inbox.HostPrefix + "devbox"}) {
		t.Error("a host's request may be answered from the dock")
	}
}
