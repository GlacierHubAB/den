package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/orch"
)

// Orchestrator guidance (orchestrator.guidance) reaches the orchestrator's
// prompt only as text whose SHA-256 the human approved with ^Y on Den's
// inbox question, which shows all of it. Den records that one hash in
// guidance.json and nothing else; only that ^Y writes it. The tick reads
// the key again, twice when it changed, so a half-written file changes
// nothing: turning guidance off, or text that fails its checks, unloads it
// once the orchestrator is idle; turning it on or changing it asks first.

// guidanceState is what Den knows of orchestrator guidance.
type guidanceState struct {
	read    bool   // the setting has been read at least once
	setting string // orchestrator.guidance as last read whole
	sha     string // SHA-256 of the text the setting names now, "" when off or refused
	refused string // why the setting's text was refused, said once
	// active is the approved text the orchestrator gets, activeSHA its hash.
	active, activeSHA string
	// restart: the running orchestrator has other guidance than active,
	// and is restarted (resuming its session) once it is idle.
	restart bool
	// The open question: its item, and the guidance it asks about.
	askItem *inbox.Item
	ask     orch.Guidance
	// declined is the hash the human said no to; it is not asked again
	// until the text changes or Den restarts.
	declined string
	reading  bool // a read is in flight
	// load reads the setting and its text; tests replace it.
	load func(lastSetting, lastSHA string) guidanceReadMsg
}

// guidanceReadMsg is one read of orchestrator.guidance and its text.
type guidanceReadMsg struct {
	ok      bool // the config read; when false nothing changes
	setting string
	g       orch.Guidance
	err     error // the text was refused
}

// guidanceWait is the pause before a changed setting is read again.
var guidanceWait = func() { time.Sleep(100 * time.Millisecond) }

// readGuidance reads the setting and its text, and once more after
// guidanceWait when either differs from what Den last read: only two
// reads that agree count.
func readGuidance(lastSetting, lastSHA string) guidanceReadMsg {
	once := func() guidanceReadMsg {
		setting, err := config.GuidanceSetting()
		if err != nil {
			return guidanceReadMsg{}
		}
		msg := guidanceReadMsg{ok: true, setting: setting}
		if setting != "" {
			msg.g, msg.err = orch.LoadGuidance(setting)
		}
		return msg
	}
	first := once()
	if !first.ok || (first.setting == lastSetting && first.g.SHA256 == lastSHA) {
		return first
	}
	guidanceWait()
	again := once()
	if !again.ok || again.setting != first.setting || again.g.SHA256 != first.g.SHA256 || errText(again.err) != errText(first.err) {
		return guidanceReadMsg{}
	}
	return first
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// guidancePath is where the approved hash is kept.
func guidancePath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "guidance.json")
}

type approvedGuidance struct {
	SHA256 string `json:"approved_sha256"`
}

// approvedSHA is the hash the human last approved, "" for none.
func approvedSHA() string {
	raw, err := os.ReadFile(guidancePath())
	if err != nil {
		return ""
	}
	var a approvedGuidance
	if json.Unmarshal(raw, &a) != nil || len(a.SHA256) != 64 {
		return ""
	}
	return a.SHA256
}

// saveApproved records the hash the human approved. Only decideGuidance,
// on the human's ^Y, calls it.
func saveApproved(sha string) error {
	p := guidancePath()
	if p == "" {
		return errors.New("no state folder")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw, _ := json.Marshal(approvedGuidance{SHA256: sha})
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// startGuidance takes the setting Den started with, so the orchestrator's
// first start already has approved guidance; the tick reads it again.
func (m *Model) startGuidance(setting string) {
	m.guide.load = readGuidance
	msg := guidanceReadMsg{ok: true, setting: setting}
	if setting != "" {
		msg.g, msg.err = orch.LoadGuidance(setting)
	}
	m.applyGuidance(msg)
}

// pollGuidance is the tick's read of the setting, one at a time.
func (m *Model) pollGuidance() tea.Cmd {
	if m.guide.reading || m.guide.load == nil {
		return nil
	}
	m.guide.reading = true
	load, setting, sha := m.guide.load, m.guide.setting, m.guide.sha
	return func() tea.Msg { return load(setting, sha) }
}

// applyGuidance acts on a read of the setting.
func (m *Model) applyGuidance(msg guidanceReadMsg) tea.Cmd {
	m.guide.reading = false
	if !msg.ok {
		return nil // the config did not read: what is in use stays
	}
	first := !m.guide.read
	m.guide.read = true
	m.guide.setting, m.guide.sha = msg.setting, msg.g.SHA256
	if msg.setting == "" {
		m.guide.refused = ""
		m.dropGuidanceQuestion("orchestrator.guidance is off now")
		m.setGuidance("", "", "orchestrator guidance is off")
		return nil
	}
	if msg.err != nil {
		m.dropGuidanceQuestion("its text was refused")
		m.setGuidance("", "", "")
		if why := msg.err.Error(); why != m.guide.refused {
			m.guide.refused = why
			m.guidanceNote("orchestrator guidance not loaded: "+why, true)
		}
		return nil
	}
	m.guide.refused = ""
	g := msg.g
	if g.SHA256 == m.guide.activeSHA {
		return nil
	}
	if approved := approvedSHA(); g.SHA256 == approved {
		m.dropGuidanceQuestion("")
		m.setGuidance(g.Text, g.SHA256, "orchestrator guidance loaded: "+guidanceName(g)+" · sha256 "+g.SHA256[:12])
		return nil
	} else if approved != "" && m.guide.ask.SHA256 != g.SHA256 && m.guide.declined != g.SHA256 {
		m.guidanceNote("orchestrator guidance not loaded: its text (sha256 "+g.SHA256[:12]+") is not the text you approved ("+approved[:12]+"); approve it in the inbox to load it", true)
	}
	m.setGuidance("", "", "")
	if m.guide.ask.SHA256 == g.SHA256 || m.guide.declined == g.SHA256 {
		return nil
	}
	m.dropGuidanceQuestion("the text changed")
	return m.askGuidance(g, first)
}

func guidanceName(g orch.Guidance) string {
	if g.Setting == orch.ADHDName {
		return orch.ADHDName
	}
	return filepath.Base(g.Setting)
}

// guidanceNote tells the human in the orchestrator chat; why guidance was
// not loaded goes on the flash line too, and stays there.
func (m *Model) guidanceNote(text string, notLoaded bool) {
	m.logEntry(m.addChat(chatEntry{role: "note", text: text, at: time.Now()}))
	if notLoaded {
		m.flash, m.flashErr = text, true
	}
}

// setGuidance makes text the guidance the orchestrator runs with, from
// its next start; a running one restarts once idle. note, when the
// guidance in use changes, says what happened.
func (m *Model) setGuidance(text, sha, note string) {
	if sha == m.guide.activeSHA {
		return
	}
	m.guide.active, m.guide.activeSHA = text, sha
	if note != "" {
		m.guidanceNote(note, false)
	}
	m.guide.restart = m.orch.proc != nil || m.orch.starting
	m.restartForGuidance()
}

// restartForGuidance ends an idle orchestrator whose guidance is out of
// date; the next message resumes its session with the guidance in use. A
// turn in progress is never cut short: the tick and the turn's result try
// again.
func (m *Model) restartForGuidance() {
	if !m.guide.restart {
		return
	}
	if m.orch.proc == nil && !m.orch.starting {
		m.guide.restart = false
		return
	}
	if m.orch.proc == nil || m.orch.thinking || len(m.orch.unanswered) > 0 || len(m.orch.pending) > 0 {
		return
	}
	proc := m.orch.proc
	m.orch.proc = nil
	go proc.Close()
	m.orch.started = false
	m.orch.status = ""
	m.guide.restart = false
	m.logEntry(m.addChat(chatEntry{role: "note", text: "the orchestrator resumes its session with the new guidance setting at your next message", at: time.Now()}))
}

// askGuidance puts Den's question in the inbox: the whole text, where it
// comes from and its SHA-256.
func (m *Model) askGuidance(g orch.Guidance, first bool) tea.Cmd {
	title := "Load orchestrator guidance " + guidanceName(g) + "?"
	if !first || approvedSHA() != "" {
		title = "Load changed orchestrator guidance " + guidanceName(g) + "?"
	}
	body := []string{
		"from: " + g.Source,
		"sha256: " + g.SHA256,
		fmt.Sprintf("Den adds the %d bytes below to the orchestrator's prompt, after its fixed rules, which this text cannot relax or override. ^Y loads it; ^N leaves the orchestrator without it.", len(g.Text)),
		"──── guidance text ────",
	}
	body = append(body, strings.Split(strings.TrimSuffix(g.Text, "\n"), "\n")...)
	body = append(body, "──── end of guidance text ────")
	it := m.store.AddGuidance(title, body)
	m.guide.askItem, m.guide.ask = it, g
	m.refresh()
	if m.cfg.Notify.NeedsYou {
		return m.notifyCmd(inbox.GuidanceSession, "asks you to approve orchestrator guidance")
	}
	return nil
}

// dropGuidanceQuestion resolves an open guidance question that no longer
// stands.
func (m *Model) dropGuidanceQuestion(why string) {
	if m.guide.askItem == nil {
		return
	}
	if it := m.guide.askItem; it.State != inbox.Resolved {
		if why == "" {
			why = "no longer asked"
		}
		m.store.Settle(it, false, why)
	}
	m.guide.askItem, m.guide.ask = nil, orch.Guidance{}
}

// decideGuidance is the human's ^Y or ^N on the question, after the whole
// text was shown (answer's readGate). ^Y records the hash and loads the
// text it asked about, if the setting still names that same text.
func (m Model) decideGuidance(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	g := m.guide.ask
	if it != m.guide.askItem || g.SHA256 == "" {
		m.store.Settle(it, false, "stale; nothing loaded")
		m.refresh()
		return m.say("That guidance question is stale; nothing was loaded.", true)
	}
	m.guide.askItem, m.guide.ask = nil, orch.Guidance{}
	if verb != "allow" {
		m.guide.declined = g.SHA256
		m.store.Settle(it, false, "declined; the orchestrator runs without it")
		m.refresh()
		return m.say("Orchestrator guidance left off.", false)
	}
	if m.guide.sha != g.SHA256 {
		m.store.Settle(it, false, "the text changed since; nothing loaded")
		m.refresh()
		return m.say("The guidance text changed since that question; nothing was loaded.", true)
	}
	if err := saveApproved(g.SHA256); err != nil {
		m.store.Settle(it, false, "could not record the approval; nothing loaded")
		m.refresh()
		return m.say("Could not record the approval: "+err.Error(), true)
	}
	m.store.Settle(it, true, "approved · sha256 "+g.SHA256[:12])
	m.setGuidance(g.Text, g.SHA256, "orchestrator guidance approved and loaded: "+guidanceName(g)+" · sha256 "+g.SHA256[:12])
	m.refresh()
	return m.say("Orchestrator guidance approved.", false)
}
