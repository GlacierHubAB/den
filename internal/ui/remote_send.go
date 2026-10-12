package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/remote"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// `den send name@host …` asks the running Den (bridge.SendEvent), because
// only Den holds the ssh session to the host. It is only ever read from the
// local den.sock, where any local process may write, so an agent here could
// use it to steer agents on a host the human keeps to themselves: Den asks
// the human first, one inbox question with the host, the agent and the full
// text, and sends only on ^Y. It is plain text and approves nothing.

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
	if m.sendAsks(a.Host) {
		m.askSend(r, a, in)
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
	var exact, prefix, there []fleet.Agent
	for _, a := range agents {
		if a.Host != host {
			continue
		}
		there = append(there, a)
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
		return fleet.Agent{}, noMatchOnHost(who, host, there)
	case 1:
		return hits[0], nil
	}
	var names []string
	for _, h := range hits {
		names = append(names, h.Name)
	}
	return fleet.Agent{}, fmt.Errorf("%q fits more than one agent on %s: %s; use the full name", q, host, strings.Join(names, ", "))
}

// noMatchOnHost says no agent on host fits who, and names the agents that
// are there, so the next try can use one of their names.
func noMatchOnHost(who, host string, there []fleet.Agent) error {
	where := host
	if where == "" {
		where = "this machine"
	}
	if len(there) == 0 {
		return fmt.Errorf("no agent matches %q: Den knows no agents on %s", who, where)
	}
	const max = 8
	var names []string
	for _, a := range there {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	if len(names) > max {
		names = append(names[:max], fmt.Sprintf("and %d more", len(names)-max))
	}
	return fmt.Errorf("no agent matches %q; on %s: %s", who, where, strings.Join(names, ", "))
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

// sendAsks reports whether a den send to an agent on host needs the
// human's ^Y. A host that lets this machine's agents act there (its
// orchestrator = true in remotes.toml) takes them directly, as an agent on
// this machine does; until hosts can say so, every host asks.
func (m Model) sendAsks(host string) bool { return true }

// askedSend is a den send waiting on the human.
type askedSend struct {
	req       *bridge.Request
	host, sid string // the host and the session id it knows
	name      string
	text      string
}

// askedSentMsg is the end of a send the human allowed.
type askedSentMsg struct {
	itemID string
	name   string
	host   string
	err    error
}

// askSend puts a den send in the inbox as a question.
func (m *Model) askSend(r *bridge.Request, a fleet.Agent, in bridge.SendInput) {
	title := "Send a message to " + a.Name + " on " + a.Host + "?"
	body := []string{"on " + a.Host + " · " + a.Name + " (" + a.Session.Name + ")", "asked by " + sanitize.Label(in.AskedBy, 120), ""}
	body = append(body, strings.Split(sanitize.Text(in.Text), "\n")...)
	it := m.store.AddMessageOn(a.Host, a.Session.SessionID, title, body, r)
	if m.sends == nil {
		m.sends = map[string]askedSend{}
	}
	m.sends[it.ID] = askedSend{req: r, host: a.Host, sid: a.Session.SessionID, name: a.Name, text: in.Text}
	m.flash, m.flashErr = title+" ^Y sends it, ^N does not.", false
	m.refresh()
}

// decideSend is ^Y or ^N on a den send question; /always does not apply.
func (m Model) decideSend(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	s, ok := m.sends[it.ID]
	if !ok {
		m.store.Settle(it, false, "the message was lost; nothing was sent")
		return m.say("That message is stale; nothing was sent.", true)
	}
	if verb == "always" {
		return m.say("Each message is asked for once: ^Y sends this one, ^N does not.", true)
	}
	delete(m.sends, it.ID)
	if verb != "allow" {
		s.req.Reply(bridge.Decision{Behavior: bridge.SendDeclined, Message: "declined in Den; nothing was sent"})
		m.store.Settle(it, false, "declined in Den; nothing was sent")
		m.refresh()
		return m.say("Not sent.", false)
	}
	if s.req.HungUp() {
		m.store.Settle(it, false, "the den send that asked gave up; nothing was sent")
		m.refresh()
		return m.say("The den send that asked gave up; nothing was sent.", true)
	}
	if err := m.remoteCan(s.host); err != nil {
		m.store.Settle(it, false, err.Error())
		m.refresh()
		return m.say(err.Error(), true)
	}
	it.Resolved = "sending…"
	feed, id := m.remote.feed, it.ID
	cmd := func() tea.Msg {
		return askedSentMsg{itemID: id, name: s.name, host: s.host, err: feed.Send(s.host, s.sid, s.text)}
	}
	model, say := m.say("Sending to "+s.name+" on "+s.host+"…", false)
	return model, tea.Batch(say, cmd)
}

// finishAskedSend settles the question with what happened, which also
// answers the den send that asked.
func (m *Model) finishAskedSend(msg askedSentMsg) tea.Cmd {
	outcome := "delivered to " + msg.name + " on " + msg.host
	if msg.err != nil {
		outcome = "approved, but not sent: " + sanitize.Line(msg.err.Error())
	}
	for _, it := range m.store.Items() {
		if it.ID == msg.itemID {
			m.store.Settle(it, msg.err == nil, outcome)
		}
	}
	m.flash, m.flashErr = strings.ToUpper(outcome[:1])+outcome[1:], msg.err != nil
	m.refresh()
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} })
}
