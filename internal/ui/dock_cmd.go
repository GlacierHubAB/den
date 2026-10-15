package ui

import (
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/text/unicode/norm"

	"github.com/GlacierHubAB/den/internal/api"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Den's commands from the dock (internal/api's cmd.go). A console's line
// is checked here, in Den: one command, mapped onto one verb of a closed
// list with its aliases, each verb with what of it the dock may run.
// Anything else is refused, in any form it may come in (case, aliases,
// fullwidth or other NFKC forms). What may run goes through command(), the
// same path as a line typed in Den; whatever asks the human there asks on
// its card.

// dockVerb is one command as the dock sees it.
type dockVerb struct {
	name    string
	aliases []string
	usage   string
	help    string
	// verdict is what the dock may do with these arguments: a dock flag,
	// and why when it is not ok or card.
	verdict func(args []string) (flag, why string)
}

func always(flag, why string) func([]string) (string, string) {
	return func([]string) (string, string) { return flag, why }
}

const (
	whyScreen  = "it acts on Den's own screen: do it in Den"
	whyCard    = "answer the card with card.answer"
	whyNever   = "the console may not do this; do it in Den"
	whyPropose = "not from the dock yet: propose missions in Den, or ask through chat"
)

// dockVerbs are every command Den has, in the order /help lists them.
var dockVerbs = []dockVerb{
	{"/allow", []string{"/y", "/yes"}, "/allow", "allow the selected request", always(api.DockRefused, whyCard)},
	{"/deny", []string{"/n", "/no"}, "/deny", "deny the selected request", always(api.DockRefused, whyCard)},
	{"/always", []string{"/a"}, "/always", "allow, and from now on", always(api.DockRefused, whyCard)},
	{"/answer", nil, "/answer <text>", "answer a question held in Den", always(api.DockRefused, whyNever)},
	{"/api", nil, "/api [off|disconnect|unpair|disallow <n>|all|control off]", "den-api.sock", func(a []string) (string, string) {
		switch {
		case len(a) == 0, a[0] == "status", a[0] == "list":
			return api.DockOK, ""
		case a[0] == "off", a[0] == "disconnect", a[0] == "unpair", a[0] == "disallow":
			return api.DockOK, ""
		case a[0] == "control" && len(a) > 1 && a[1] == "off":
			return api.DockOK, ""
		}
		return api.DockRefused, "pairing, consoles and turning the API or control on are for Den only"
	}},
	{"/remote", []string{"/remotes", "/hosts"}, "/remote [retry <host>]", "remote hosts", func(a []string) (string, string) {
		if len(a) == 0 || a[0] == "retry" {
			return api.DockOK, ""
		}
		return api.DockRefused, "connecting with a changed policy is for Den only"
	}},
	{"/open", []string{"/o", "/focus"}, "/open", "focus the agent's terminal", always(api.DockDenScreen, whyScreen)},
	{"/copy", nil, "/copy", "copy the last code block", always(api.DockDenScreen, whyScreen)},
	{"/close", []string{"/kill"}, "/close <name>[, name…] | idle [dur] [--force]", "close agents, on a card", func(a []string) (string, string) {
		if len(a) == 0 {
			return api.DockRefused, "name the agents: the dock has no selection"
		}
		return api.DockCard, ""
	}},
	{"/reopen", nil, "/reopen [--fork] <name>", "reopen an agent, on a card", func(a []string) (string, string) {
		if len(a) == 0 {
			return api.DockRefused, "name the agent"
		}
		return api.DockCard, ""
	}},
	{"/protect", nil, "/protect [name]", "protect an agent from closing", always(api.DockOK, "")},
	{"/unprotect", nil, "/unprotect <name>", "unprotect an agent", always(api.DockRefused, "lowering close-safety is for Den only for now")},
	{"/flag", nil, "/flag <name>[: note]", "flag an agent", always(api.DockOK, "")},
	{"/unflag", nil, "/unflag <name>", "unflag an agent", always(api.DockOK, "")},
	{"/dismiss", []string{"/x"}, "/dismiss", "dismiss the selected item", always(api.DockDenScreen, whyScreen)},
	{"/unread", []string{"/u"}, "/unread", "keep the selected item unread", always(api.DockDenScreen, whyScreen)},
	{"/rescan", []string{"/r"}, "/rescan", "look at the fleet again", always(api.DockDenScreen, whyScreen)},
	{"/map", nil, "/map", "the fleet's map", always(api.DockDenScreen, whyScreen)},
	{"/fleet", nil, "/fleet [project|flat]", "group the fleet", func(a []string) (string, string) {
		if len(a) > 0 && (a[0] == "project" || a[0] == "projects" || a[0] == "flat") {
			return api.DockOK, ""
		}
		return api.DockDenScreen, whyScreen
	}},
	{"/inbox", nil, "/inbox", "the inbox tab", always(api.DockDenScreen, whyScreen)},
	{"/delete", []string{"/del"}, "/delete", "delete the comment at the cursor", always(api.DockDenScreen, whyScreen)},
	{"/send", nil, "/send", "send review drafts", always(api.DockRefused, "sending drafts to agents is for Den only for now")},
	{"/merge", nil, "/merge <agent>", "merge an agent's worktree, on a card", func(a []string) (string, string) {
		if len(a) == 0 {
			return api.DockRefused, "name the agent"
		}
		return api.DockCard, ""
	}},
	{"/cleanup", nil, "/cleanup <agent>", "remove an agent's worktree, on a card", func(a []string) (string, string) {
		if len(a) == 0 {
			return api.DockRefused, "name the agent"
		}
		return api.DockCard, ""
	}},
	{"/review", nil, "/review", "the review tab", always(api.DockDenScreen, whyScreen)},
	{"/overview", []string{"/missions"}, "/overview", "the overview tab", always(api.DockDenScreen, whyScreen)},
	{"/project", nil, "/project <name> | add <dir> | drop", "pick or pin a project", func(a []string) (string, string) {
		if len(a) == 0 {
			return api.DockDenScreen, whyScreen
		}
		return api.DockOK, ""
	}},
	{"/mission", nil, "/mission move <project>", "move a mission, on a card", func(a []string) (string, string) {
		if len(a) > 0 && a[0] == "move" {
			return api.DockCard, ""
		}
		// A proposal is a turn of the orchestrator's with the human's
		// authority: the console's words would reach it unfenced.
		return api.DockRefused, whyPropose
	}},
	{"/template", []string{"/templates"}, "/template save <name>", "save the open mission as a template", func(a []string) (string, string) {
		if len(a) > 0 && a[0] == "save" {
			return api.DockOK, ""
		}
		if len(a) == 0 {
			return api.DockDenScreen, whyScreen
		}
		return api.DockRefused, whyPropose
	}},
	{"/view", nil, "/view [rescue]", "the agent and user views", func(a []string) (string, string) {
		// Never runs from the dock: a console switches views with
		// view.show and view.hide, and rescue is the human's alone.
		if len(a) > 0 && a[0] == "rescue" {
			return api.DockRefused, "rescuing the windows is for Den only"
		}
		return api.DockDenScreen, "switch views with view.show and view.hide"
	}},
	{"/pause", nil, "/pause", "pause the open mission", always(api.DockDenScreen, whyScreen)},
	{"/resume", nil, "/resume", "resume the open mission", always(api.DockDenScreen, whyScreen)},
	{"/stop", nil, "/stop", "stop the open mission", always(api.DockDenScreen, whyScreen)},
	{"/usage", nil, "/usage [set <pct>|clear|stop|resume]", "the weekly limit", func(a []string) (string, string) {
		switch {
		case len(a) == 0, a[0] == "stop":
			return api.DockOK, ""
		case a[0] == "resume", a[0] == "set", a[0] == "clear":
			// set and clear change the figure the stop is measured
			// against: Den asks on a card.
			return api.DockCard, ""
		}
		return api.DockRefused, "no such /usage command"
	}},
	{"/budget", nil, "/budget", "the open mission's budget", always(api.DockDenScreen, whyScreen)},
	{"/orchestrator", []string{"/orch"}, "/orchestrator [new|restart]", "the orchestrator", func(a []string) (string, string) {
		if len(a) > 0 && (a[0] == "new" || a[0] == "restart") {
			return api.DockOK, ""
		}
		return api.DockDenScreen, whyScreen
	}},
	{"/help", []string{"/?"}, "/help", "Den's help", always(api.DockDenScreen, whyScreen)},
	{"/quit", []string{"/q", "/exit"}, "/quit", "quit Den", always(api.DockRefused, whyNever)},
}

// dockJudged is how many leading words each verb's verdict judges: they
// run in the form judged (lowercased), so what runs is what was checked;
// the words after (names, notes, paths) run as typed.
var dockJudged = map[string]int{"/api": 1, "/remote": 1, "/fleet": 1, "/mission": 1, "/template": 1, "/usage": 1, "/orchestrator": 1}

// dockVerbOf is the verb a command word names, by its name or an alias.
func dockVerbOf(word string) (dockVerb, bool) {
	for _, v := range dockVerbs {
		if v.name == word {
			return v, true
		}
		for _, a := range v.aliases {
			if a == word {
				return v, true
			}
		}
	}
	return dockVerb{}, false
}

// dockCommands is the command list for cmd.list: each verb with what the
// dock may do with it as it stands (with no arguments).
func dockCommands() []api.CommandInfo {
	out := make([]api.CommandInfo, 0, len(dockVerbs))
	for _, v := range dockVerbs {
		flag, _ := v.verdict(nil)
		if flag != api.DockOK && flag != api.DockCard {
			// A verb that runs with some arguments is listed as it may run.
			for _, try := range [][]string{{"x"}, {"status"}, {"retry"}, {"project"}, {"new"}, {"set"}} {
				if f, _ := v.verdict(try); f == api.DockOK || f == api.DockCard {
					flag = f
					break
				}
			}
		}
		out = append(out, api.CommandInfo{Name: v.name, Usage: v.usage, Help: v.help, Dock: flag})
	}
	return out
}

// dockLine checks a console's line and maps it onto a verb: the words to
// run, or why not. One command; no second command word, no shell or line
// separators, no control or format characters; NFKC first, so a
// fullwidth or otherwise folded form is the verb it looks like.
func dockLine(text string) ([]string, dockVerb, string) {
	t := strings.TrimSpace(norm.NFKC.String(text))
	for _, r := range t {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return nil, dockVerb{}, "one line of plain text"
		}
	}
	for _, bad := range []string{";", "&&", "||", "`", "$("} {
		if strings.Contains(t, bad) {
			return nil, dockVerb{}, "one command, with no " + bad
		}
	}
	fields := strings.Fields(t)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return nil, dockVerb{}, "a command starts with /"
	}
	fields[0] = strings.ToLower(fields[0])
	v, ok := dockVerbOf(fields[0])
	if !ok {
		return nil, dockVerb{}, "no such command"
	}
	for _, f := range fields[1:] {
		if _, again := dockVerbOf(strings.ToLower(f)); again {
			return nil, dockVerb{}, "one command at a time"
		}
	}
	args := fields[1:]
	lower := make([]string, len(args))
	for i, a := range args {
		lower[i] = strings.ToLower(a)
	}
	flag, why := v.verdict(lower)
	if flag != api.DockOK && flag != api.DockCard {
		return nil, v, why
	}
	n := dockJudged[v.name]
	if v.name == "/api" && len(lower) > 0 && lower[0] == "control" {
		n = 2
	}
	for i := 0; i < n && i < len(args); i++ {
		args[i] = lower[i]
	}
	return append([]string{v.name}, args...), v, ""
}

// runFromDock runs a console's command line, or says why not.
func (m *Model) runFromDock(r *api.CommandRun) tea.Cmd {
	words, _, why := dockLine(r.Text)
	if why != "" {
		r.Reply <- api.CommandResult{Code: api.CodeRefusedFromDock, Note: why}
		return nil
	}
	line := strings.Join(words, " ")
	m.flash, m.flashErr = "", false
	m.dockFrom = sanitize.Label(r.Console, 40)
	mm, cmd := m.command(line)
	*m = mm.(Model)
	m.dockFrom = ""
	m.logEntry(m.addUnthreaded(chatEntry{role: "note", text: "from " + sanitize.Label(r.Console, 40) + ": " + sanitize.Label(line, 200), at: time.Now()}))
	note := sanitize.Label(m.flash, 200)
	if m.flashErr {
		r.Reply <- api.CommandResult{Code: api.CodeUnavailable, Note: note}
		return cmd
	}
	r.Reply <- api.CommandResult{Note: note}
	return cmd
}
