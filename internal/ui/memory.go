package ui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/inbox"
	"github.com/GlacierHubAB/den/internal/memory"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/spawn"
	"github.com/GlacierHubAB/den/internal/tuning"
)

// A project's memory: agents keep notes (data, whoever reads them), the
// orchestrator proposes rules from them, and only the human, on a card in
// Den's own window, makes any of them rules, which then reach prompts as
// the human's words. Who wrote a note, Den reads from the asking process.

// memState is each project's memory record, by repo id, and the cards.
type memState struct {
	metas   map[string]*memory.Meta
	broken  map[string]bool         // memory.json there did not parse: Den writes nothing
	rules   map[string]memory.State // rules.md against Den's record
	started bool
	picks   map[string]*rulesPick // rules cards, by inbox item id
	edited  map[string]string     // rules-edited cards, by item id: the repo
}

// rulesPick is a rules card: the proposals it shows and the ones picked.
type rulesPick struct {
	repo   string
	ids    []string
	picked map[int]bool
}

// memStateDir is Den's state folder; tests point it elsewhere.
var memStateDir = tuning.StateDir

func (m *Model) memDir(repo string) string { return memory.Dir(m.memRoot(), repo) }

func (m *Model) memRoot() string { return memStateDir() }

// memMeta is the project's record, read the first time Den needs it.
func (m *Model) memMeta(repo string) *memory.Meta {
	if m.mem.metas == nil {
		m.mem.metas, m.mem.broken = map[string]*memory.Meta{}, map[string]bool{}
	}
	if mt := m.mem.metas[repo]; mt != nil {
		return mt
	}
	mt, err := memory.LoadMeta(memory.MetaPath(m.memRoot(), repo))
	if err != nil {
		m.mem.broken[repo] = true
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: time.Now(), text: "Den: the memory record of " + sanitize.Label(repo, 60) + " was not read (" + sanitize.Label(err.Error(), 80) + "); Den writes nothing there until it is fixed or removed."}))
	}
	m.mem.metas[repo] = &mt
	return &mt
}

func (m *Model) saveMeta(repo string) error {
	if m.mem.broken[repo] {
		return errString("the project's memory record did not parse; Den writes nothing there")
	}
	return memory.SaveMeta(memory.MetaPath(m.memRoot(), repo), *m.memMeta(repo))
}

type errString string

func (e errString) Error() string { return string(e) }

// memStartup checks, once a run, each project's rules.md against Den's
// record: one changed outside Den stops counting and goes to the human.
func (m *Model) memStartup() {
	if m.mem.started {
		return
	}
	m.mem.started = true
	paths, _ := filepath.Glob(filepath.Join(m.memRoot(), "projects", "*", "memory.json"))
	for _, p := range paths {
		repo := filepath.Base(filepath.Dir(p))
		m.checkRules(repo)
	}
}

// checkRules reads rules.md against the record and, when it changed
// outside Den, puts the human the card.
func (m *Model) checkRules(repo string) {
	if m.mem.rules == nil {
		m.mem.rules = map[string]memory.State{}
	}
	mt := m.memMeta(repo)
	st := memory.ReadRules(m.memDir(repo), *mt)
	m.mem.rules[repo] = st
	if !st.Edited {
		return
	}
	for _, r := range m.mem.edited {
		if r == repo {
			return // already asked
		}
	}
	name := strconv.Quote(sanitize.Label(firstNonEmptyS(mt.Name, repo), 60))
	body := []string{"rules.md of " + name + " was changed outside Den, so Den no longer uses it as your rules; agents read its text only as a note.",
		"Den cannot tell who changed it. ^Y makes the file as it is now your rules again; ^N leaves it a note."}
	for _, r := range st.Added {
		body = append(body, "added outside Den: "+memory.QuoteRule(r))
	}
	for _, r := range st.Removed {
		body = append(body, "removed outside Den: "+memory.QuoteRule(r))
	}
	switch {
	case st.TooLong:
		body = append(body, "The file is too long to show whole here; trim it to "+strconv.Itoa(memory.MaxRulesBytes)+" bytes, and Den asks again on its next start.")
	case st.Text != "":
		body = append(body, "──── rules.md now, whole ────")
		for _, l := range strings.Split(strings.TrimRight(st.Text, "\n"), "\n") {
			body = append(body, sanitize.Line(l))
		}
		body = append(body, "──── end ────")
	default:
		body = append(body, "rules.md was removed.")
	}
	it := m.store.AddRulesEdited(memorySessionPrefix+repo, "Rules of "+name+" changed outside Den", body)
	if m.mem.edited == nil {
		m.mem.edited = map[string]string{}
	}
	m.mem.edited[it.ID] = repo
	m.refresh()
}

type memMsg struct {
	r          *bridge.Request
	in         bridge.MemoryInput
	who        string // the agent's critter name, or "the orchestrator"
	session    string // the agent's session, "" for the orchestrator
	orch       bool
	repo, name string
	err        error
}

// handleMemory places the caller by its process, finds its project off the
// loop, then applies.
func (m *Model) handleMemory(r *bridge.Request) tea.Cmd {
	var in bridge.MemoryInput
	if json.Unmarshal(r.Envelope.Payload, &in) != nil {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "bad input"})
		return nil
	}
	// teamCaller names the orchestrator with no session, an agent with its.
	who, session, ok := m.teamCaller(r.PeerPID)
	isOrch := ok && session == ""
	if !ok {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "Den cannot tell which agent asks; only agents Den sees and the orchestrator keep notes"})
		return nil
	}
	if r.Event == bridge.RuleEvent && !isOrch {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "only the orchestrator proposes rules"})
		return nil
	}
	dir := ""
	if isOrch {
		who = "the orchestrator"
		d, err := spawn.ExpandDir(in.Dir)
		if err != nil {
			r.Reply(bridge.Decision{Behavior: "deny", Message: "give dir, the project's directory"})
			return nil
		}
		dir = d
	} else if a, found := m.agentFor(session); found {
		dir = a.Session.CWD
	}
	if dir == "" {
		r.Reply(bridge.Decision{Behavior: "deny", Message: "Den does not know your checkout"})
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), teamScanTimeout)
		defer cancel()
		repo, name, err := teamRepo(ctx, dir)
		return memMsg{r: r, in: in, who: who, session: session, orch: isOrch, repo: repo, name: name, err: err}
	}
}

// applyMemory carries out note, notes or propose_rule.
func (m *Model) applyMemory(msg memMsg, now time.Time) {
	r := msg.r
	deny := func(why string) { r.Reply(bridge.Decision{Behavior: "deny", Message: why}) }
	if msg.err != nil || msg.repo == "" {
		deny("the project is not a git repository")
		return
	}
	m.memStartup()
	mt := m.memMeta(msg.repo)
	if _, ok := m.mem.rules[msg.repo]; !ok {
		m.checkRules(msg.repo)
	}
	switch r.Event {
	case bridge.NoteEvent:
		title, err := memory.Title(msg.in.Title)
		if err == nil {
			err = mt.Allowed(msg.who, now)
		}
		body, berr := memory.Body(msg.in.Text)
		if err == nil {
			err = berr
		}
		if err != nil {
			deny(err.Error())
			return
		}
		rec := memory.Rec{Title: title, Author: msg.who, At: now}
		if msg.session != "" && m.missions != nil {
			if ms := m.missions.Get(m.missionOf(msg.session)); ms != nil {
				rec.Mission = sanitize.Label(ms.Name, 80)
			}
		}
		if m.mem.broken[msg.repo] {
			deny("the project's memory record did not parse; Den writes nothing there")
			return
		}
		id := memory.NewID("n", now)
		mt.Name = msg.name
		if err := memory.WriteNote(m.memDir(msg.repo), mt, id, rec, body); err != nil {
			deny("Den could not keep it: " + sanitize.Label(err.Error(), 120))
			return
		}
		if err := m.saveMeta(msg.repo); err != nil {
			deny(err.Error())
			return
		}
		r.Reply(bridge.Decision{Behavior: "allow", Message: "kept as " + id + " for " + strconv.Quote(sanitize.Label(msg.name, 40)) + "; agents on the project can read it with notes"})
	case bridge.NotesEvent:
		found := memory.Search(m.memDir(msg.repo), *mt, msg.in.Query)
		if st := m.mem.rules[msg.repo]; st.Edited && st.Text != "" {
			found = append([]memory.Found{{ID: "rules.md", Rec: memory.Rec{Title: "rules.md, changed outside Den: not the human's rules until approved again", Author: "unknown", At: now}, Body: st.Text}}, found...)
		}
		r.Reply(bridge.Decision{Behavior: "allow", Message: memory.Fenced(found, memory.ReadBudget)})
	case bridge.RuleEvent:
		text, err := memory.Rule(msg.in.Text)
		if err != nil {
			deny(err.Error())
			return
		}
		var from []string
		for _, id := range msg.in.From {
			if _, ok := mt.Notes[id]; !ok || !memory.ValidID(id) {
				deny("no note " + strconv.Quote(sanitize.Label(id, 40)) + " in this project")
				return
			}
			from = append(from, id)
		}
		if len(mt.Pending) >= memory.MaxPending {
			deny("already " + strconv.Itoa(memory.MaxPending) + " rules wait for the human")
			return
		}
		if slices.ContainsFunc(mt.Pending, func(p memory.Proposal) bool { return strings.EqualFold(p.Text, text) }) || slices.ContainsFunc(m.mem.rules[msg.repo].Rules, func(x string) bool { return strings.EqualFold(x, text) }) {
			deny("that rule is already proposed or in force")
			return
		}
		mt.Name = msg.name
		mt.Pending = append(mt.Pending, memory.Proposal{ID: memory.NewID("r", now), Text: text, From: from, At: now})
		if err := m.saveMeta(msg.repo); err != nil {
			deny(err.Error())
			return
		}
		m.rulesCard(msg.repo)
		r.Reply(bridge.Decision{Behavior: "allow", Message: "proposed; the human picks which proposals become rules, in Den"})
	}
}

// rulesCard puts the project's waiting proposals, at most memory.CardRules,
// on one card, replacing the one before.
func (m *Model) rulesCard(repo string) {
	for id, p := range m.mem.picks {
		if p.repo != repo {
			continue
		}
		for _, it := range m.store.Items() {
			if it.ID == id && it.State != inbox.Resolved {
				m.store.Settle(it, false, "replaced by a newer card")
			}
		}
		delete(m.mem.picks, id)
	}
	mt := m.memMeta(repo)
	if len(mt.Pending) == 0 {
		return
	}
	p := &rulesPick{repo: repo, picked: map[int]bool{}}
	for i, pr := range mt.Pending {
		if i == memory.CardRules {
			break
		}
		p.ids = append(p.ids, pr.ID)
	}
	name := strconv.Quote(sanitize.Label(firstNonEmptyS(mt.Name, repo), 60))
	it := m.store.AddRulesPick(memorySessionPrefix+repo, "Rules proposed for "+name, m.rulesBody(p))
	if m.mem.picks == nil {
		m.mem.picks = map[string]*rulesPick{}
	}
	m.mem.picks[it.ID] = p
	m.refresh()
}

// rulesBody is a rules card's text: each proposal whole, numbered, picked
// or not, and where it came from in Den's words.
// memorySessionPrefix keys a project's memory cards (proposed rules,
// rules edited outside Den) by its repository id.
const memorySessionPrefix = "memory:"

// A rules card fences each note it shows between these lines; a console's
// copy of the card leaves the fenced notes out (withoutNotes).
const (
	noteFenceTop    = "    ┌"
	noteFenceBottom = "    └"
)

// rulesKeys is the card's keys line, in plain words.
func rulesKeys(n int) string {
	if n == 1 {
		return "1 selects it · ^Y keeps it, once selected · ^N drops it"
	}
	return "1–" + strconv.Itoa(n) + " select · ^Y keep the selected rules · ^N drop all"
}

// rulesBody is the card the human judges proposed rules on, each in place:
// what approving does, the project's rules now, then each rule with the
// notes it was drawn from, whole and fenced as the agents' own words (a
// note is at most memory.MaxNote, so none is cut).
func (m *Model) rulesBody(p *rulesPick) []string {
	mt := m.memMeta(p.repo)
	name := sanitize.Label(firstNonEmptyS(mt.Name, p.repo), 60)
	body := []string{
		"Approved rules are added to " + name + "'s rules and reach the orchestrator and every agent Den starts on " + name + " as your own instruction.",
		rulesKeys(len(p.ids)),
	}
	var picked []string
	for i := range p.ids {
		if p.picked[i] {
			picked = append(picked, strconv.Itoa(i+1))
		}
	}
	if len(picked) == 0 {
		body = append(body, "None selected yet.")
	} else {
		body = append(body, "Selected: "+strings.Join(picked, ", ")+".")
	}
	if len(mt.Pending) > len(p.ids) {
		body = append(body, strconv.Itoa(len(mt.Pending)-len(p.ids))+" more wait for the next card.")
	}
	body = append(body, "", name+"'s rules now:")
	switch st := memory.ReadRules(m.memDir(p.repo), *mt); {
	case st.Edited:
		body = append(body, "  rules.md was changed outside Den: approve it in Den first (its own card)")
	case len(st.Rules) == 0:
		body = append(body, "  no rules yet")
	default:
		for _, r := range st.Rules {
			body = append(body, "  - "+memory.QuoteRule(r))
		}
	}
	for i, id := range p.ids {
		pr, ok := proposal(*mt, id)
		if !ok {
			continue
		}
		box := "[ ]"
		if p.picked[i] {
			box = "[x]"
		}
		body = append(body, "", box+" "+strconv.Itoa(i+1)+". "+memory.QuoteRule(pr.Text))
		if len(pr.From) == 0 {
			body = append(body, "    proposed by the orchestrator, from no note")
			continue
		}
		body = append(body, "    proposed by the orchestrator, from "+notesWord(len(pr.From))+":")
		for _, n := range pr.From {
			rec, known := mt.Notes[n]
			text, read := memory.ReadNote(m.memDir(p.repo), n)
			if !known || !read {
				body = append(body, "    - note "+sanitize.Label(n, 40)+": Den has no such note")
				continue
			}
			head := noteFenceTop + " the agent's note, its own words: " + sanitize.Label(rec.Author, 60)
			if rec.Mission != "" {
				head += " · mission " + sanitize.Label(rec.Mission, 60)
			}
			head += " · " + rec.At.Local().Format("Jan 2 15:04")
			body = append(body, head, "    │ "+sanitize.Label(rec.Title, 120))
			for _, l := range strings.Split(text, "\n") {
				body = append(body, "    │ "+sanitize.Line(l))
			}
			body = append(body, noteFenceBottom)
		}
	}
	return body
}

// notesWord is "1 note" or "N notes".
func notesWord(n int) string {
	if n == 1 {
		return "1 note"
	}
	return strconv.Itoa(n) + " notes"
}

func proposal(mt memory.Meta, id string) (memory.Proposal, bool) {
	for _, p := range mt.Pending {
		if p.ID == id {
			return p, true
		}
	}
	return memory.Proposal{}, false
}

// toggleRule is a digit pressed while a rules card is selected in the
// Inbox and nothing is typed: it picks or unpicks that proposal.
func (m *Model) toggleRule(it *inbox.Item, n int) bool {
	p, ok := m.mem.picks[it.ID]
	if !ok || n < 1 || n > len(p.ids) || !it.Answerable() {
		return false
	}
	p.picked[n-1] = !p.picked[n-1]
	it.Body = m.rulesBody(p)
	m.refresh()
	return true
}

// decideRules is ^Y (the picked proposals become rules) or ^N (every one
// shown is dropped) on a rules card, in Den's own window only.
func (m Model) decideRules(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	p, ok := m.mem.picks[it.ID]
	if !ok {
		m.store.Settle(it, false, "stale")
		m.refresh()
		return m.say("That card is from before Den restarted; the proposals wait for the next one.", true)
	}
	mt := m.memMeta(p.repo)
	if verb == "allow" {
		if len(p.picked) == 0 || !slices.Contains(mapValues(p.picked), true) {
			return m.say("Pick rules first: press their numbers while the card is selected.", true)
		}
		if st := m.mem.rules[p.repo]; st.Edited {
			return m.say("rules.md was changed outside Den: answer that card first.", true)
		}
		rules := slices.Clone(mt.Rules)
		for i, id := range p.ids {
			if pr, ok := proposal(*mt, id); ok && p.picked[i] {
				rules = append(rules, pr.Text)
			}
		}
		if len(memory.RulesText(rules)) > memory.MaxRulesBytes {
			return m.say("Those rules would make rules.md longer than Den shows whole; pick fewer.", true)
		}
		if m.mem.broken[p.repo] {
			return m.say("The project's memory record did not parse; Den writes nothing there.", true)
		}
		if err := memory.WriteRules(m.memDir(p.repo), mt, rules); err != nil {
			return m.say("Den could not write the rules: "+err.Error(), true)
		}
		m.mem.rules[p.repo] = memory.State{Rules: mt.Rules}
	}
	mt.Pending = slices.DeleteFunc(mt.Pending, func(pr memory.Proposal) bool { return slices.Contains(p.ids, pr.ID) })
	if err := m.saveMeta(p.repo); err != nil {
		return m.say(err.Error(), true)
	}
	delete(m.mem.picks, it.ID)
	m.store.Settle(it, verb == "allow", map[bool]string{true: "rules written", false: "dropped"}[verb == "allow"])
	m.rulesCard(p.repo) // the next ones, if any
	m.refresh()
	return m, nil
}

func mapValues(mp map[int]bool) []bool {
	var out []bool
	for _, v := range mp {
		out = append(out, v)
	}
	return out
}

// decideRulesEdited is ^Y (rules.md as it is becomes the rules) or ^N (it
// stays a note) on the card for a rules.md changed outside Den.
func (m Model) decideRulesEdited(it *inbox.Item, verb string) (tea.Model, tea.Cmd) {
	repo, ok := m.mem.edited[it.ID]
	if !ok {
		m.store.Settle(it, false, "stale")
		m.refresh()
		return m, nil
	}
	if verb == "allow" {
		mt := m.memMeta(repo)
		if m.mem.broken[repo] {
			return m.say("The project's memory record did not parse; Den writes nothing there.", true)
		}
		if err := memory.Reapprove(m.memDir(repo), mt); err != nil {
			return m.say("Not approved: "+err.Error()+".", true)
		}
		if err := m.saveMeta(repo); err != nil {
			return m.say(err.Error(), true)
		}
		m.mem.rules[repo] = memory.ReadRules(m.memDir(repo), *mt)
	}
	delete(m.mem.edited, it.ID)
	m.store.Settle(it, verb == "allow", map[bool]string{true: "approved as the rules", false: "left a note"}[verb == "allow"])
	m.refresh()
	return m, nil
}

// memoryCommand is /memory (the current project's notes, listed in Den's
// chat), /memory rm <id> (delete a note) and /memory rules (the rules in
// force and the proposals waiting, whose card it puts up again).
func (m *Model) memoryCommand(args []string) (string, bool) {
	p, ok := m.curProject()
	if !ok {
		return "Pick a project first: /project <name>.", true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	repo, name, err := teamRepo(ctx, p.Root)
	cancel()
	if err != nil || repo == "" {
		return "The project is not a git repository: it has no memory.", true
	}
	m.memStartup()
	if _, ok := m.mem.rules[repo]; !ok {
		m.checkRules(repo)
	}
	mt := m.memMeta(repo)
	label := strconv.Quote(sanitize.Label(name, 40))
	switch {
	case len(args) == 0:
		found := memory.Search(m.memDir(repo), *mt, "")
		lines := []string{"Memory of " + label + ": " + strconv.Itoa(len(found)) + " notes, " + strconv.Itoa(len(m.mem.rules[repo].Rules)) + " rules in force, " + strconv.Itoa(len(mt.Pending)) + " proposed. Folder: " + m.memDir(repo)}
		for i, f := range found {
			if i == 20 {
				lines = append(lines, "… and "+strconv.Itoa(len(found)-i)+" older")
				break
			}
			lines = append(lines, f.ID+" · "+f.At.Local().Format("2006-01-02")+" · "+sanitize.Label(f.Author, 40)+" · "+strconv.Quote(sanitize.Label(f.Title, 80)))
		}
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: time.Now(), text: strings.Join(lines, "\n")}))
		return "Listed in the chat: " + strconv.Itoa(len(found)) + " notes.", false
	case args[0] == "rm" && len(args) == 2:
		if m.mem.broken[repo] {
			return "The project's memory record did not parse; Den writes nothing there.", true
		}
		if err := memory.RemoveNote(m.memDir(repo), mt, args[1]); err != nil {
			return "Not deleted: " + err.Error() + ".", true
		}
		if err := m.saveMeta(repo); err != nil {
			return err.Error(), true
		}
		return "Deleted note " + args[1] + ".", false
	case args[0] == "rules" && len(args) == 1:
		st := m.mem.rules[repo]
		lines := []string{"Rules of " + label + ":"}
		if st.Edited {
			lines = append(lines, "rules.md was changed outside Den: none count until you approve it in the Inbox.")
		}
		for _, r := range st.Rules {
			lines = append(lines, "- "+memory.QuoteRule(r))
		}
		if len(st.Rules) == 0 && !st.Edited {
			lines = append(lines, "(none yet)")
		}
		m.logEntry(m.addUnthreaded(chatEntry{role: "note", at: time.Now(), text: strings.Join(lines, "\n")}))
		if len(mt.Pending) > 0 {
			m.rulesCard(repo)
			return strconv.Itoa(len(mt.Pending)) + " proposed rules wait in the Inbox.", false
		}
		return "Listed in the chat.", false
	}
	return "Use /memory, /memory rm <id> or /memory rules.", true
}

// rulesInfos are the rules in force, by project, for the orchestrator.
func (m Model) rulesInfos() []orch.RulesInfo {
	var out []orch.RulesInfo
	for repo, st := range m.mem.rules {
		if st.Edited || len(st.Rules) == 0 {
			continue
		}
		name := repo
		if mt := m.mem.metas[repo]; mt != nil && mt.Name != "" {
			name = mt.Name
		}
		out = append(out, orch.RulesInfo{Project: name, Rules: st.Rules})
	}
	slices.SortFunc(out, func(a, b orch.RulesInfo) int { return strings.Compare(a.Project, b.Project) })
	return out
}
