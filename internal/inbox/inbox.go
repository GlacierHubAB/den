// Package inbox turns hook events into the list of things waiting on the
// human, plus the "done" and "idle" notices that are nice to see once.
package inbox

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
)

// Kind is what an item asks of you.
type Kind int

const (
	Permission Kind = iota // a tool wants to run; y/n/a answer it
	Plan                   // ExitPlanMode: approve or ask for changes
	Question               // AskUserQuestion: answer in the terminal
	Done                   // a turn finished while you were elsewhere
	Idle                   // Claude Code's idle notice
	Sent                   // a message you sent, with its delivery status
)

func (k Kind) String() string {
	switch k {
	case Permission:
		return "permission"
	case Plan:
		return "plan ready"
	case Question:
		return "question"
	case Done:
		return "done"
	case Idle:
		return "idle"
	case Sent:
		return "sent"
	}
	return "?"
}

// State is where an item is in its life.
type State int

const (
	Waiting    State = iota // Den can answer it
	InTerminal              // only the terminal can answer it now
	Resolved                // answered or superseded; kept briefly for context
)

// Item is one inbox entry.
type Item struct {
	ID        string
	SessionID string
	Kind      Kind
	State     State
	At        time.Time
	Tool      string
	Title     string   // one line, what is being asked
	Intent    string   // the agent's own words for why, when the tool call carries them
	Body      []string // the thing being decided, in full: the command, the diff, the arguments
	Options   []string // AskUserQuestion choices, display only
	Resolved  string   // how it was resolved, for the dim trailer
	Role      string   // the agent's role when this happened
	Task      string   // what the agent was working on when this happened
	Seen      bool     // shown on the Inbox view at least once
	req       *bridge.Request
}

// Answerable reports whether y/n/a act on this item.
func (it *Item) Answerable() bool {
	return it.State == Waiting && (it.Kind == Permission || it.Kind == Plan) && it.req != nil && it.req.WantsReply()
}

// Store holds items and the per-session "always allow" rules.
type Store struct {
	history int // see HistoryLimit
	items   []*Item
	always  map[string]map[string]bool // sessionID -> allow key
	seq     int
}

// New returns an empty store.
func New() *Store { return &Store{always: map[string]map[string]bool{}} }

// Items returns items newest-last, waiting ones first.
func (s *Store) Items() []*Item {
	out := append([]*Item(nil), s.items...)
	sort.SliceStable(out, func(i, j int) bool {
		wi, wj := out[i].State != Resolved && out[i].Kind <= Question, out[j].State != Resolved && out[j].Kind <= Question
		if wi != wj {
			return wi
		}
		if wi {
			return out[i].At.Before(out[j].At) // answer in the order they asked
		}
		return out[i].At.After(out[j].At) // newest notice on top
	})
	return out
}

// Waiting counts items a human still has to deal with.
func (s *Store) Waiting() int {
	n := 0
	for _, it := range s.items {
		if it.State != Resolved && it.Kind <= Question {
			n++
		}
	}
	return n
}

// SessionNeedsYou reports whether a session has anything waiting.
func (s *Store) SessionNeedsYou(sessionID string) bool {
	for _, it := range s.items {
		if it.SessionID == sessionID && it.State != Resolved && it.Kind <= Question {
			return true
		}
	}
	return false
}

// Apply folds a hook request into the store. It returns the item it created,
// if any. Permission requests matching an "always" rule are answered here.
func (s *Store) Apply(r *bridge.Request) *Item {
	in := r.Input
	switch r.Event {
	case "PermissionRequest":
		if key := allowKey(in.ToolName, in.ToolInput); key != "" && s.always[in.SessionID][key] {
			r.Reply(bridge.Decision{Behavior: "allow", Message: "den: allowed by your earlier always-allow"})
			return nil
		}
		it := s.add(&Item{SessionID: in.SessionID, Tool: in.ToolName, req: r})
		if in.ToolName == "ExitPlanMode" {
			it.Kind = Plan
			it.Title = "Plan ready for review"
			it.Body = planExcerpt(in.ToolInput)
		} else {
			it.Kind = Permission
			it.Title, it.Intent, it.Body = describeTool(in.ToolName, in.ToolInput)
		}
		// Drop the soft "permission_prompt" notice for the same session.
		for _, o := range s.items {
			if o != it && o.SessionID == in.SessionID && o.Kind == Permission && o.req == nil && o.State != Resolved {
				o.State, o.Resolved = Resolved, "superseded"
			}
		}
		return it
	case "PreToolUse":
		if in.ToolName != "AskUserQuestion" {
			return nil
		}
		it := s.add(&Item{SessionID: in.SessionID, Kind: Question, State: InTerminal, Tool: in.ToolName})
		it.Title, it.Options = describeQuestion(in.ToolInput)
		return it
	case "PostToolUse":
		s.resolveSession(in.SessionID, "answered in terminal", func(it *Item) bool {
			return it.Kind == Question || (it.Kind <= Plan && it.State == InTerminal)
		})
		return nil
	case "Notification":
		switch in.NotificationType {
		case "permission_prompt":
			if s.hasOpen(in.SessionID, Permission, Plan) {
				return nil
			}
			it := s.add(&Item{SessionID: in.SessionID, Kind: Permission, State: InTerminal, Title: firstNonEmpty(in.Message, "Waiting for your permission")})
			return it
		case "idle_prompt":
			if s.hasOpen(in.SessionID, Idle) {
				return nil
			}
			return s.add(&Item{SessionID: in.SessionID, Kind: Idle, Title: firstNonEmpty(in.Message, "Waiting for your input")})
		case "agent_needs_input", "elicitation_dialog", "elicitation_url_dialog":
			if s.hasOpen(in.SessionID, Question) {
				return nil
			}
			return s.add(&Item{SessionID: in.SessionID, Kind: Question, State: InTerminal, Title: firstNonEmpty(in.Message, "Needs your input")})
		}
	case "UserPromptSubmit":
		s.resolveSession(in.SessionID, "you replied", func(it *Item) bool { return it.Kind != Done })
	case "Stop":
		s.resolveSession(in.SessionID, "turn finished", func(it *Item) bool { return it.Kind != Done })
		return s.add(&Item{SessionID: in.SessionID, Kind: Done, State: Resolved, Title: Summarize(in.LastAssistant)})
	case "SessionEnd":
		s.resolveSession(in.SessionID, "session ended", func(it *Item) bool { return true })
	}
	return nil
}

// Sent records an outgoing message. status is shown as the trailer and can
// be updated through SetStatus.
func (s *Store) Sent(sessionID, text, status string) *Item {
	return s.add(&Item{SessionID: sessionID, Kind: Sent, State: InTerminal, Title: "→ " + firstLine(text), Resolved: status})
}

// SetStatus updates a notice's trailer, marking it resolved when final.
func (s *Store) SetStatus(it *Item, status string, final bool) {
	it.Resolved = status
	if final {
		it.State = Resolved
		it.At = time.Now()
	}
}

// Answer resolves an answerable item. verb is "allow", "deny" or "always".
func (s *Store) Answer(it *Item, verb string) bool {
	if !it.Answerable() {
		return false
	}
	switch verb {
	case "allow":
		it.req.Reply(bridge.Decision{Behavior: "allow"})
		it.State, it.Resolved = Resolved, "allowed in den"
	case "always":
		if key := allowKey(it.Tool, it.req.Input.ToolInput); key != "" {
			if s.always[it.SessionID] == nil {
				s.always[it.SessionID] = map[string]bool{}
			}
			s.always[it.SessionID][key] = true
		}
		it.req.Reply(bridge.Decision{Behavior: "allow"})
		it.State, it.Resolved = Resolved, "allowed, and again this session"
	case "deny":
		it.req.Reply(bridge.Decision{Behavior: "deny", Message: "Denied from den by the user."})
		it.State, it.Resolved = Resolved, "denied in den"
	default:
		return false
	}
	return true
}

// Release hands a waiting permission back to the terminal dialog.
func (s *Store) Release(it *Item, why string) {
	if it.req != nil {
		it.req.Reply(bridge.Decision{})
	}
	if it.State == Waiting {
		it.State = InTerminal
		it.Resolved = why
	}
}

// Dismiss removes an item that needs no answer, or a resolved one.
func (s *Store) Dismiss(it *Item) {
	if it.Answerable() {
		return
	}
	for i, o := range s.items {
		if o == it {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return
		}
	}
}

// MaxHistory is how many finished turns the inbox keeps as history unless
// the store says otherwise.
const MaxHistory = 100

// HistoryLimit overrides MaxHistory for this store when positive.
func (s *Store) HistoryLimit(n int) { s.history = n }

func (s *Store) historyLimit() int {
	if s.history > 0 {
		return s.history
	}
	return MaxHistory
}

// Prune drops resolved items older than keep and dismissible notices beyond
// max. Finished turns are history: they stay, newest MaxHistory of them.
func (s *Store) Prune(keep time.Duration, maxNotices int) {
	var out []*Item
	notices, history := 0, 0
	for i := len(s.items) - 1; i >= 0; i-- {
		it := s.items[i]
		if it.Kind == Done {
			history++
			if history > s.historyLimit() {
				continue
			}
			out = append(out, it)
			continue
		}
		if it.State == Resolved && time.Since(it.At) > keep {
			continue
		}
		if it.Kind > Done {
			notices++
			if notices > maxNotices {
				continue
			}
		}
		out = append(out, it)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	s.items = out
}

// Unseen counts items that have not been shown on the Inbox view yet, plus
// anything still waiting for an answer.
func (s *Store) Unseen() int {
	n := 0
	for _, it := range s.items {
		if !it.Seen || it.Answerable() {
			n++
		}
	}
	return n
}

// MarkSeen records that the Inbox view showed everything it holds.
func (s *Store) MarkSeen() {
	for _, it := range s.items {
		it.Seen = true
	}
}

// Restore puts history loaded from disk in front of everything else, already
// seen.
func (s *Store) Restore(items []*Item) {
	for _, it := range items {
		s.seq++
		it.ID = fmt.Sprintf("h%d", s.seq)
		it.Seen = true
		if it.Kind == 0 {
			it.Kind = Done
		}
		it.State = Resolved
	}
	s.items = append(items, s.items...)
}

func (s *Store) add(it *Item) *Item {
	s.seq++
	it.ID = fmt.Sprintf("i%d", s.seq)
	if it.At.IsZero() {
		it.At = time.Now()
	}
	s.items = append(s.items, it)
	return it
}

func (s *Store) hasOpen(sessionID string, kinds ...Kind) bool {
	for _, it := range s.items {
		if it.SessionID != sessionID || it.State == Resolved {
			continue
		}
		for _, k := range kinds {
			if it.Kind == k {
				return true
			}
		}
	}
	return false
}

func (s *Store) resolveSession(sessionID, why string, match func(*Item) bool) {
	for _, it := range s.items {
		if it.SessionID == sessionID && it.State != Resolved && match(it) {
			if it.req != nil {
				it.req.Reply(bridge.Decision{})
			}
			it.State, it.Resolved = Resolved, why
		}
	}
}

// allowKey is the "always" rule key: tool plus the first two words of a
// Bash command, or the tool name alone for everything else.
func allowKey(tool string, input json.RawMessage) string {
	if tool == "" {
		return ""
	}
	if tool == "Bash" {
		var in struct {
			Command string `json:"command"`
		}
		json.Unmarshal(input, &in)
		f := strings.Fields(in.Command)
		if len(f) == 0 {
			return "Bash"
		}
		if len(f) > 2 {
			f = f[:2]
		}
		return "Bash:" + strings.Join(f, " ")
	}
	return tool
}

// bodyLimit caps how much of a diff, a file or a payload an item carries.
// Enough to decide on; the terminal has the rest.
const bodyLimit = 40

// describeTool says what a permission request is for: a short title, the
// agent's stated intent when the call carries one, and the thing being
// decided in full, line by line, so the human can approve or reject
// knowing exactly what runs.
func describeTool(tool string, input json.RawMessage) (title, intent string, body []string) {
	var in map[string]any
	json.Unmarshal(input, &in)
	str := func(k string) string {
		if v, ok := in[k].(string); ok {
			return v
		}
		return ""
	}
	switch tool {
	case "Bash":
		return "Wants to run", str("description"), commandLines(str("command"))
	case "Edit", "MultiEdit":
		return "Wants to edit " + str("file_path"), "", diffLines(str("old_string"), str("new_string"))
	case "Write":
		return "Wants to write " + str("file_path"), "", limitLines(str("content"), bodyLimit)
	case "NotebookEdit":
		return "Wants to edit notebook " + str("notebook_path"), "", limitLines(str("new_source"), bodyLimit)
	case "Read":
		return "Wants to read " + str("file_path"), "", nil
	case "WebFetch":
		return "Wants to fetch " + str("url"), str("prompt"), nil
	case "WebSearch":
		return "Wants to search the web", "", []string{str("query")}
	case "Agent", "Task":
		return "Wants to start an agent", str("description"), limitLines(str("prompt"), 12)
	case "SendMessage":
		return "Wants to message " + str("to"), "", limitLines(str("message"), 12)
	}
	if server, name, ok := mcpTool(tool); ok {
		return "Wants to call " + name + " on " + server, "", jsonLines(in)
	}
	return "Wants to use " + tool, "", jsonLines(in)
}

// mcpTool splits an MCP tool name, mcp__server__tool, into its parts.
func mcpTool(tool string) (server, name string, ok bool) {
	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return "", "", false
	}
	server, name, ok = strings.Cut(rest, "__")
	if !ok || server == "" || name == "" {
		return "", "", false
	}
	return server, name, true
}

// commandLines is a shell command as the terminal would show it: the
// first line after a prompt, the rest indented under it.
func commandLines(cmd string) []string {
	lines := strings.Split(strings.TrimSpace(cmd), "\n")
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if i == 0 {
			out = append(out, "$ "+l)
		} else {
			out = append(out, "  "+l)
		}
	}
	return out
}

// jsonLines is a tool's arguments as indented JSON, within the body limit.
func jsonLines(in map[string]any) []string {
	if len(in) == 0 {
		return nil
	}
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return nil
	}
	return limitLines(string(b), bodyLimit)
}

// limitLines keeps the first n lines of a text and says how many follow.
func limitLines(s string, n int) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return lines
	}
	return append(lines[:n], fmt.Sprintf("… %d more lines", len(lines)-n))
}

func describeQuestion(input json.RawMessage) (string, []string) {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	json.Unmarshal(input, &in)
	if len(in.Questions) == 0 {
		return "Has a question for you", nil
	}
	q := in.Questions[0]
	var opts []string
	for _, o := range q.Options {
		opts = append(opts, o.Label)
	}
	title := q.Question
	if len(in.Questions) > 1 {
		title += fmt.Sprintf(" (+%d more)", len(in.Questions)-1)
	}
	return title, opts
}

func planExcerpt(input json.RawMessage) []string {
	var in struct {
		Plan string `json:"plan"`
	}
	json.Unmarshal(input, &in)
	return limitLines(in.Plan, bodyLimit)
}

// diffLines shows an edit as removed lines then added lines, the whole
// change up to the body limit split between the two halves.
func diffLines(oldS, newS string) []string {
	half := bodyLimit / 2
	var out []string
	for _, l := range limitLines(oldS, half) {
		out = append(out, "- "+l)
	}
	for _, l := range limitLines(newS, half) {
		out = append(out, "+ "+l)
	}
	return out
}

// Summarize turns an assistant reply into one plain sentence for a notice:
// no code blocks, no markdown furniture, cut at the first sentence end.
func Summarize(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "Turn finished."
	}
	// Drop fenced code blocks wholesale.
	var kept []string
	inFence := false
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(t, "#") {
			continue // headings name sections, they do not say what was done
		}
		t = strings.TrimLeft(t, ">-*• ")
		t = strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "`", "").Replace(t))
		if t != "" {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		return "Turn finished."
	}
	flat := strings.Join(strings.Fields(strings.Join(kept, " ")), " ")
	// First sentence: a terminator followed by a space, or the whole thing.
	for i := 0; i+1 < len(flat); i++ {
		if (flat[i] == '.' || flat[i] == '!' || flat[i] == '?') && flat[i+1] == ' ' && i >= 12 {
			flat = flat[:i+1]
			break
		}
	}
	if r := []rune(flat); len(r) > 160 {
		flat = strings.TrimSpace(string(r[:159])) + "…"
	}
	return flat
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
