// Package grants is the permission manager's model: what a console may do
// on its own, per class of act, as levels 0 to 5, and the read of it a
// console gets (grants.get and the grants event). Level 0, the empty bar,
// is "Only in Den"; the five segments are levels 1 to 5. Den sets the
// levels; a console only lowers them (from a later PR on).
//
// Nothing here is enforced yet: Enforced is false until the answer check
// reads the levels. The words are Den's own, fixed here; no string an agent
// or a console wrote reaches the read.
package grants

// The classes, in the order the read lists them; a closed set.
const (
	Routine     = "routine"
	Destructive = "destructive"
	Code        = "code"
	Desktop     = "desktop"
	Outward     = "outward"
)

// Steps is how many segments every class's bar has: levels 1 to Steps.
const Steps = 5

// step is one segment's words: a short name (2 to 4 words) and the
// sentence it means.
type step struct{ name, label string }

// class is one class's fixed words and its default level.
type class struct {
	id, label, about string
	def              int
	steps            []step // levels 1 up; the rest have no step
}

// table is every class, in order. Its words are the designer's, from the
// approved permission manager design; a level past a class's steps is not
// available.
var table = []class{
	{Routine, "Everyday answers", "Everyday answers to an agent: a rename, a listed option, a read-only tool, a reply in your words.", 4, []step{
		{"Rename and move", "Rename an agent or move a mission."},
		{"Pick an option", "Also pick one of an agent's listed options."},
		{"Allow a read once", "Also allow a read-only tool once, nothing private."},
		{"Answer in your words", "Also answer an agent in your own words, shown whole in Drey's confirm window; never to an agent holding desktop control."},
	}},
	{Destructive, "Stop and close", "Close and reopen agents, remove worktrees. Den keeps an undo where it can.", 1, []step{
		{"What Den can undo", "Close an agent; you can reopen it."},
		{"Every stop and close", "Also stops that lose work, discards, deleting a branch, a cleanup that deletes ignored files, each with the whole target shown."},
	}},
	{Code, "Run commands", "Commands and edits an agent asks to run.", 1, []step{
		{"One command, shown whole", "Run one command from Drey's confirm window, once, never 'always'."},
	}},
	{Desktop, "Desktop control", "An agent that works your desktop: the mouse, the keyboard, your windows.", 0, []step{
		{"Start a desktop task", "Start a desktop task your console asked for."},
		{"Answer a paused agent", "Also answer and resume an agent that has the desktop, after a quiet moment, never while a password field is focused."},
	}},
	{Outward, "Send out", "What reaches other agents or leaves this machine: messages, merges, pushes, web access.", 0, []step{
		{"Start and message agents", "Start an agent in the default mode, or send one a message."},
		{"Merge a branch", "Also merge a branch."},
		{"Push and web", "Also approve a push or web access."},
	}},
}

// NoStep is the label of a level a class has no step for.
const NoStep = "No further step"

// DenOnlyItem is one thing only the human does, in Den's window.
type DenOnlyItem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// denOnly is the fixed list of what never comes from a console.
var denOnly = []DenOnlyItem{
	{"god_on", "Turn on God mode"},
	{"pairing", "Pair or re-pair a console, add a console"},
	{"api_on", "Turn the API on or unlock it"},
	{"raise", "Raise any level here"},
	{"checks", "Turn off or loosen a check (rate limits, read-first, input idle, desktop pause, the audit)"},
	{"remote", "Anything about a remote host"},
	{"weakening", "Widen permissions: \"always\", raising the mode, installs, allowing a refused click, reading private files"},
	{"spend", "Keep going over a budget, resume or set the weekly usage limit"},
	{"desktop_hard", "Den's own window, ending your session, reloading Hyprland, the panic key"},
}

// Step is a segment as the read carries it.
type Step struct {
	Level     int    `json:"level"`
	Name      string `json:"name"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
}

// Session is a console's lowering of a class for this Den session.
type Session struct {
	Level   int    `json:"level"`
	At      string `json:"at"`
	Console string `json:"console"`
}

// Class is one class as the read carries it.
type Class struct {
	Class    string   `json:"class"`
	Label    string   `json:"label"`
	About    string   `json:"about"`
	DenLevel int      `json:"den_level"`
	Session  *Session `json:"session"`
	Level    int      `json:"level"`
	Default  int      `json:"default"`
	Steps    []Step   `json:"steps"`
	SetInDen *string  `json:"set_in_den"`
}

// Kept is a kind kept from before the permission manager (upgrade only).
type Kept struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Class  string `json:"class"`
	Step   *int   `json:"step"`
	Active bool   `json:"active"`
}

// Grants is the read: grants.get's answer and the grants event's data.
type Grants struct {
	Seq      uint64 `json:"seq"`
	Enforced bool   `json:"enforced"`
	// Powers: the console paired now has its powers. With none, or no
	// console, every level is 0.
	Powers bool `json:"powers"`
	Locked bool `json:"locked"`
	God    bool `json:"god"`
	// StartConfirmed: the human confirmed in Den's window since Den
	// started. Until a later PR builds the start rule, it stands in as
	// Powers: the powers card is the start confirm.
	StartConfirmed bool          `json:"start_confirmed"`
	Classes        []Class       `json:"classes"`
	Kept           []Kept        `json:"kept"`
	DenOnly        []DenOnlyItem `json:"den_only"`
}

// State is what the read is worked out from: Den's own record only.
type State struct {
	Powers bool // the console paired now has its powers
	Locked bool // the human's /api lock
	God    bool // God mode is on
	// Sessions are the classes a console lowered for this Den session,
	// by class.
	Sessions map[string]Session
}

// The refusals of a lowering.
const (
	BadRequest = "bad_request"
	NotLower   = "not_lower"
)

// cap is the class's cap now: its Den level, lowered by a session.
func (st State) cap(c class) int {
	if s, ok := st.Sessions[c.id]; ok && s.Level < c.def {
		return s.Level
	}
	return c.def
}

// Valid reports whether id is a class and level one of its levels, 0 to
// Steps: the closed sets a request is checked against first.
func Valid(id string, level int) bool {
	return Label(id) != "" && level >= 0 && level <= Steps
}

// Label is class id's label, "" for no class.
func Label(id string) string {
	for _, c := range table {
		if c.id == id {
			return c.label
		}
	}
	return ""
}

// Lower checks a console's lowering of class id to level against st, and
// returns the class's new session, or the refusal: bad_request for a class
// or a level outside the closed sets, not_lower for a level that is not
// strictly below the class's cap (min of its Den level and its session),
// a raise or the same level. The caller gives at and console, Den's own
// clock and label.
func Lower(st State, id string, level int, at, console string) (Session, string) {
	for _, c := range table {
		if c.id != id {
			continue
		}
		if level < 0 || level > Steps {
			return Session{}, BadRequest
		}
		if level >= st.cap(c) {
			return Session{}, NotLower
		}
		return Session{Level: level, At: at, Console: console}, ""
	}
	return Session{}, BadRequest
}

// godZero are the classes God mode shows at 0: every desktop kind, and
// Merge, Budget and Usage, answer only in Den's window while it is on.
var godZero = map[string]bool{Desktop: true, Outward: true}

// Read is the read for st, with seq 0: every class at its Den level (its
// default: none is set in Den yet), lowered by its session, and the
// effective level 0 while locked, without powers, or, for the desktop and
// outward classes, in God mode.
func Read(st State) Grants {
	g := Grants{
		Powers:         st.Powers,
		Locked:         st.Locked,
		God:            st.God,
		StartConfirmed: st.Powers, // stand-in until (4): the powers card is the start confirm
		Classes:        make([]Class, 0, len(table)),
		Kept:           []Kept{},
		DenOnly:        append([]DenOnlyItem(nil), denOnly...),
	}
	for _, c := range table {
		out := Class{Class: c.id, Label: c.label, About: c.about, DenLevel: c.def, Default: c.def, Level: st.cap(c), Steps: make([]Step, Steps)}
		if s, ok := st.Sessions[c.id]; ok {
			s := s
			out.Session = &s
		}
		for i := range out.Steps {
			s := Step{Level: i + 1, Label: NoStep}
			if i < len(c.steps) {
				s = Step{Level: i + 1, Name: c.steps[i].name, Label: c.steps[i].label, Available: true}
			}
			out.Steps[i] = s
		}
		if st.Locked || !st.Powers || st.God && godZero[c.id] {
			out.Level = 0
		}
		g.Classes = append(g.Classes, out)
	}
	return g
}
