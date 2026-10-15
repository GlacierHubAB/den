package ui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/transcript"
	"github.com/GlacierHubAB/den/internal/usage"
)

// The usage guard (package usage): the share of the weekly limit used, in
// the header; warnings at 80% and 90%; and, only with usage.stop_percent
// set, a stop: "USAGE STOP" to every agent and no new agents or mission
// runs until the human lets the fleet go on.

// usageText is what Den sends each agent when it stops the fleet.
const usageText = "USAGE STOP"

// usageGuard is the guard's state in the Model.
type usageGuard struct {
	path    string        // usage.json, "" in tests: nothing is written
	st      usage.State   // what is kept between runs
	reading usage.Reading // the reading Den goes by
	ok      bool          // false: usage unknown
	// asked is the figure a console sent, on the card asking for it: its
	// item id, and the share (nil to clear the figure).
	askedID string
	asked   *float64
}

// save writes the state when it has a file.
func (g *usageGuard) save() {
	if g.path != "" {
		_ = usage.Save(g.path, g.st) // best effort; the next change tries again
	}
}

// usageSamples are the weekly figures this machine's sessions reported.
// Agents on other hosts may be on other accounts, so they do not count.
func (m Model) usageSamples() []usage.Sample {
	var out []usage.Sample
	for _, a := range m.base {
		if a.Host != "" || !a.HasLimit || a.Limits.SevenDay == nil {
			continue
		}
		out = append(out, usage.Sample{Percent: a.Limits.SevenDay, Resets: a.Limits.SevenDayResets, At: a.Limits.Now})
	}
	return out
}

// weekSpent is Den's own cost estimate for the week so far: every session
// it knows of on this machine, since the last reset a week back.
func (m Model) weekSpent(now time.Time) float64 {
	from := now.Add(-7 * 24 * time.Hour)
	if r := m.use.st.LastReset; !r.IsZero() && r.Add(-7*24*time.Hour).Before(now) {
		from = r.Add(-7 * 24 * time.Hour)
	}
	byModel := map[string]transcript.Tokens{}
	for _, a := range m.base {
		if a.Host != "" || a.Path == "" {
			continue
		}
		for model, tk := range transcript.Shared.Window(a.Path, from, now) {
			byModel[model] = byModel[model].Add(tk)
		}
	}
	return fleet.Prices.OfWindow(byModel).USD
}

// checkUsage runs on every tick: it settles the reading, warns once per
// threshold and weekly reset, and stops the fleet when the human set a stop
// share and it is reached.
func (m *Model) checkUsage(now time.Time) tea.Cmd {
	g := &m.use
	before, _ := json.Marshal(g.st)
	fresh, haveFresh := usage.Newest(m.usageSamples(), now)
	spent := 0.0
	if !haveFresh && m.cfg.Usage.WeeklyUSD > 0 {
		spent = m.weekSpent(now)
	}
	g.reading, g.ok = g.st.Current(fresh, haveFresh, spent, m.cfg.Usage.WeeklyUSD, now)
	var cmds []tea.Cmd
	if g.ok {
		for _, w := range g.st.Due(g.reading, now) {
			title := fmt.Sprintf("The weekly limit is at %.0f%%: past %d%%", g.reading.Percent, w)
			body := []string{usage.Label(g.reading, true, usage.State{}, now)}
			if t := untilText(g.reading.Resets.Format(time.RFC3339)); !g.reading.Resets.IsZero() && t != "" {
				body = append(body, "it "+t)
			}
			if p := m.cfg.Usage.StopPercent; p > 0 {
				body = append(body, fmt.Sprintf("Den stops the fleet at %d%% (usage.stop_percent).", p))
			} else {
				body = append(body, "Nothing stops on its own; /usage stop stops the fleet now.")
			}
			m.store.AddUsage(title, body)
			cmds = append(cmds, m.notifyCmd(inbox.UsageSession, fmt.Sprintf("says the weekly limit is past %d%%", w)))
		}
		if !g.st.Stopped && usage.ShouldStop(g.reading, m.cfg.Usage.StopPercent) && g.st.Armed(m.cfg.Usage.StopPercent, now) {
			cmds = append(cmds, m.stopFleet(fmt.Sprintf("the weekly limit reached %.0f%%, your stop is %d%%", g.reading.Percent, m.cfg.Usage.StopPercent)))
		}
	}
	if after, _ := json.Marshal(g.st); string(after) != string(before) {
		g.save()
	}
	return tea.Batch(cmds...)
}

// stopFleet stops the fleet: "USAGE STOP" to every agent Den can reach,
// through the same path as a message the human sends, and no new agents or
// mission runs until the human lets the fleet go on.
func (m *Model) stopFleet(why string) tea.Cmd {
	now := time.Now()
	g := &m.use
	g.st.Stopped, g.st.StoppedAt, g.st.StopWhy = true, now, why
	g.save()
	var cmds []tea.Cmd
	var sent, unreachable []string
	send := m.sender()
	for _, a := range m.agents {
		if a.Status == fleet.Gone || a.Stale || !a.Session.Alive && a.Host == "" {
			continue
		}
		if a.Host != "" {
			// Only where the host's policy lets Den act on its agents.
			if err := m.remoteCan(a.Host); err != nil {
				unreachable = append(unreachable, a.Name+"@"+a.Host)
				continue
			}
		}
		it := m.sentItem(a, usageText, "sending…")
		cmds = append(cmds, deliver(send, a, usageText, it))
		sent = append(sent, a.Name)
	}
	body := []string{"why: " + why}
	if len(sent) > 0 {
		body = append(body, "sent \""+usageText+"\" to "+strings.Join(sent, ", "))
	} else {
		body = append(body, "no agent to send it to")
	}
	if len(unreachable) > 0 {
		body = append(body, "not reachable for stop: "+strings.Join(unreachable, ", "))
	}
	body = append(body, "No new agents or mission runs until you let the fleet go on: /usage resume.")
	m.store.AddUsage("Den stopped the fleet for usage", body)
	m.flash, m.flashErr = "Usage stop: \""+usageText+"\" sent to "+strconv.Itoa(len(sent))+" agent(s).", true
	m.refresh()
	cmds = append(cmds, m.notifyCmd(inbox.UsageSession, "stopped the fleet for usage"))
	return tea.Batch(cmds...)
}

// usageStopped is the refusal while the fleet is stopped, "" when it is not.
func (m Model) usageStopped() string {
	if !m.use.st.Stopped {
		return ""
	}
	return "The fleet is stopped for usage (" + m.use.st.StopWhy + "): nothing new starts until /usage resume."
}

// usageCommand is /usage: show, set N, stop, resume, clear.
func (m Model) usageCommand(args []string) (tea.Model, tea.Cmd) {
	now := time.Now()
	if len(args) == 0 {
		return m.say(usage.Label(m.use.reading, m.use.ok, m.use.st, now)+" · /usage set <percent> · /usage stop · /usage resume", false)
	}
	switch strings.ToLower(args[0]) {
	case "set":
		if len(args) < 2 {
			return m.say("/usage set <percent>, e.g. /usage set 87", true)
		}
		p, err := strconv.ParseFloat(strings.TrimSuffix(args[1], "%"), 64)
		if err != nil || p < 0 || p > 100 {
			return m.say("A share of the weekly limit, 0 to 100.", true)
		}
		if m.dockFrom != "" {
			return m.askUsageFigure(&p)
		}
		return m.setUsageFigure(&p)
	case "clear":
		if m.dockFrom != "" {
			return m.askUsageFigure(nil)
		}
		return m.setUsageFigure(nil)
	case "stop":
		if m.use.st.Stopped {
			return m.say(m.usageStopped(), true)
		}
		m.use.st.ResumedWhen = time.Time{} // a stop by hand arms the automatic one again
		cmd := m.stopFleet("stopped by you")
		return m, cmd
	case "resume":
		if !m.use.st.Stopped {
			return m.say("The fleet is not stopped.", false)
		}
		it := m.store.AddUsageResume("Let the fleet go on after the usage stop?", []string{
			"stopped " + m.use.st.StoppedAt.Local().Format("Mon 15:04") + ": " + m.use.st.StopWhy,
			usage.Label(m.use.reading, m.use.ok, usage.State{}, now),
			"^Y lets agents start and mission runs go on again; the agents themselves decide what to do with \"" + usageText + "\".",
		})
		m.refresh()
		if it == nil {
			return m.say("The question is already in the inbox: ^Y there lets the fleet go on.", false)
		}
		return m.say("Answer it in the inbox: ^Y lets the fleet go on.", false)
	}
	return m.say("/usage, /usage set <percent>, /usage clear, /usage stop, /usage resume", true)
}

// setUsageFigure takes the human's figure, or clears it (p nil).
func (m Model) setUsageFigure(p *float64) (tea.Model, tea.Cmd) {
	now := time.Now()
	if p == nil {
		m.use.st.SetPercent = nil
		m.use.save()
		return m.say("Your usage figure is cleared.", false)
	}
	m.use.st.Set(*p, now)
	m.use.save()
	cmd := m.checkUsage(now)
	mm, say := m.say(fmt.Sprintf("Usage set to %.0f%% (an estimate) until %s.", *p, m.use.st.SetUntil.Local().Format("Mon 15:04")), false)
	return mm, tea.Batch(cmd, say)
}

// usageFigureLabel is a figure as the card shows it.
func usageFigureLabel(p *float64) string {
	if p == nil {
		return "none: Den goes by what the sessions report"
	}
	return fmt.Sprintf("%.0f%% (an estimate)", *p)
}

// askUsageFigure puts a console's /usage set or clear on a card: the
// figure is what the usage stop is measured against, so it is the
// human's to take.
func (m Model) askUsageFigure(p *float64) (tea.Model, tea.Cmd) {
	now := time.Now()
	var cur *float64
	if m.use.st.SetPercent != nil && now.Before(m.use.st.SetUntil) {
		cur = m.use.st.SetPercent
	}
	title := "Take the usage figure " + m.dockFrom + " sent?"
	it := m.store.AddUsageSet(title, []string{
		"your figure now: " + usageFigureLabel(cur),
		"the figure sent: " + usageFigureLabel(p),
		usage.Label(m.use.reading, m.use.ok, m.use.st, now),
		"A lower figure can hold off your usage stop.",
	})
	if it == nil {
		return m.say("A usage figure is already waiting on a card in the inbox.", true)
	}
	if p != nil {
		v := *p
		p = &v
	}
	m.use.askedID, m.use.asked = it.ID, p
	m.refresh()
	return m.say("The usage figure waits on a card in the inbox.", false)
}

// decideUsageSet is ^Y or ^N on the console's figure.
func (m Model) decideUsageSet(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	p, ok := m.use.asked, m.use.askedID == it.ID
	m.use.askedID, m.use.asked = "", nil
	switch {
	case !ok:
		m.store.Settle(it, false, "the figure is gone")
		m.refresh()
		return m.say("That usage figure is gone.", true)
	case verb != "allow":
		m.store.Settle(it, false, "kept your figure")
		m.refresh()
		return m.say("Your usage figure stays as it is.", false)
	}
	m.store.Settle(it, true, "took the figure")
	m.refresh()
	return m.setUsageFigure(p)
}

// decideUsageResume is ^Y or ^N on the question.
func (m Model) decideUsageResume(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	if verb != "allow" {
		m.store.Settle(it, false, "stays stopped")
		m.refresh()
		return m.say("The fleet stays stopped.", false)
	}
	// The automatic stop holds off for the rest of this week at this stop
	// share; /usage stop, a new week or another share arms it again.
	m.use.st.Resume(m.cfg.Usage.StopPercent, time.Now())
	m.use.save()
	m.store.Settle(it, true, "the fleet goes on")
	m.refresh()
	return m.say("The fleet may go on: new agents and mission runs are allowed again.", false)
}
