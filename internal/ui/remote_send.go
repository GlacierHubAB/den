package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// `den send name@host …` asks the running Den (bridge.SendEvent), because
// only Den holds the ssh session to the host. It is only ever read from the
// local den.sock; the message shows in the inbox as a sent notice, with who
// ran the command. It is plain text and approves nothing.

// cliSentMsg is the end of a send asked for over den.sock.
type cliSentMsg struct {
	req  *bridge.Request
	item *inbox.Item
	name string
	host string
	err  error
}

// handleSendRequest resolves who in this Den's own fleet and sends.
func (m *Model) handleSendRequest(r *bridge.Request) tea.Cmd {
	deny := func(msg string) { r.Reply(bridge.Decision{Behavior: "deny", Message: msg}) }
	if r.Host != "" {
		deny("a send can only be asked for from this machine")
		return nil
	}
	var in bridge.SendInput
	_ = json.Unmarshal(r.Payload, &in)
	if !strings.Contains(in.Who, "@") || strings.HasSuffix(strings.TrimSpace(in.Who), "@") {
		deny("name an agent on a host: name@host")
		return nil
	}
	// The remote agents with the bare session ids their hosts know, so
	// fleet.Lookup resolves "name@host" and "session-id@host" alike.
	var bare []fleet.Agent
	for _, a := range m.remote.agents {
		a.Session.SessionID, _ = splitKey(a.Session.SessionID)
		bare = append(bare, a)
	}
	a, err := onHostAgent(bare, in.Who)
	switch {
	case err != nil:
		deny(err.Error())
		return nil
	case a.Host == "":
		deny("den send name@local goes straight to the agent; run it without the Den round trip")
		return nil
	case a.Status == fleet.Gone:
		deny(a.Name + " is gone")
		return nil
	case strings.TrimSpace(in.Text) == "" || len(in.Text) > remote.MaxSendText:
		deny(fmt.Sprintf("the message must be 1 byte to %d KB", remote.MaxSendText>>10))
		return nil
	}
	if err := m.remoteCan(a.Host); err != nil {
		deny(err.Error())
		return nil
	}
	it := m.store.SentOn(a.Host, a.Session.SessionID, in.Text, "sending… (asked by "+sanitize.Label(in.AskedBy, 80)+")")
	m.refresh()
	feed, name, host, sid, text := m.remote.feed, a.Name, a.Host, a.Session.SessionID, in.Text
	return func() tea.Msg {
		return cliSentMsg{req: r, item: it, name: name, host: host, err: feed.Send(host, sid, text)}
	}
}

// onHostAgent resolves "name@host" among a host's agents: an exact name,
// peer name or session id, else a prefix of one that fits a single agent.
// Several fits are listed, never guessed.
func onHostAgent(agents []fleet.Agent, who string) (fleet.Agent, error) {
	i := strings.LastIndexByte(who, '@')
	q, host := strings.ToLower(strings.TrimSpace(who[:i])), strings.ToLower(strings.TrimSpace(who[i+1:]))
	q = strings.TrimPrefix(q, "@")
	if q == "" || host == "" {
		return fleet.Agent{}, fmt.Errorf("no agent matches %q", who)
	}
	if host == "local" {
		host = ""
	}
	var exact, prefix []fleet.Agent
	for _, a := range agents {
		if a.Host != host {
			continue
		}
		keys := []string{strings.ToLower(a.Name), strings.ToLower(a.Session.Name), strings.ToLower(a.Session.SessionID)}
		for _, k := range keys {
			if k == q {
				exact = append(exact, a)
				break
			}
		}
		for _, k := range keys {
			if k != "" && strings.HasPrefix(k, q) {
				prefix = append(prefix, a)
				break
			}
		}
	}
	hits := exact
	if len(hits) == 0 {
		hits = prefix
	}
	switch len(hits) {
	case 0:
		return fleet.Agent{}, fmt.Errorf("no agent matches %q", who)
	case 1:
		return hits[0], nil
	}
	var names []string
	for _, h := range hits {
		names = append(names, h.Name)
	}
	return fleet.Agent{}, fmt.Errorf("%q fits more than one agent on %s: %s; use the full name", q, host, strings.Join(names, ", "))
}

// applyCLISent records the outcome and answers the command.
func (m *Model) applyCLISent(msg cliSentMsg) {
	if msg.err != nil {
		m.store.SetStatus(msg.item, "failed: "+msg.err.Error(), true)
		msg.req.Reply(bridge.Decision{Behavior: "deny", Message: sanitize.Line(msg.err.Error())})
		return
	}
	m.store.SetStatus(msg.item, "delivered", true)
	msg.req.Reply(bridge.Decision{Behavior: "allow", Message: "delivered to " + msg.name + " on " + msg.host})
}
