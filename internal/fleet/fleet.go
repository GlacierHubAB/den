// Package fleet joins everything Den knows about one agent: the Claude Code
// session, its agent-badge identity, the git checkout it works in, the
// transcript title and model, and the terminal window it lives in.
package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/gitinfo"
	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/procs"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// Status is Den's state vocabulary. NeedsYou only ever comes from an explicit
// signal (the hook bridge, milestone 2); nothing is guessed. Unknown is shown
// as "?" and never as idle.
type Status int

const (
	Unknown Status = iota
	NeedsYou
	Working
	Idle
	Gone
)

func (s Status) String() string {
	switch s {
	case NeedsYou:
		return "needs you"
	case Working:
		return "working"
	case Idle:
		return "idle"
	case Gone:
		return "gone"
	}
	return "unknown"
}

// Key is the status as a stable identifier for scripts and widgets:
// "unknown", "needs_you", "working", "idle" or "gone".
func (s Status) Key() string {
	switch s {
	case NeedsYou:
		return "needs_you"
	case Working:
		return "working"
	case Idle:
		return "idle"
	case Gone:
		return "gone"
	}
	return "unknown"
}

// Agent is one coding agent as Den sees it.
type Agent struct {
	Session discover.Session
	Name    string
	Labels  badge.Labels
	Git     gitinfo.Info
	HasGit  bool
	Title   string
	Model   string
	Status  Status
	InHerdr bool
	Window  *hypr.Match

	Usage          transcript.Usage // context consumed by the last turn
	HasUsage       bool
	UsageFromBadge bool        // the figures came from agent-badge, which gets them from Claude Code itself
	Limits         badge.Usage // rate limits and spend as agent-badge recorded them
	HasLimit       bool
	Tail           []transcript.Entry // recent prompts, replies and tool calls
	Path           string             // transcript path, "" when none
}

// MarshalJSON adds StatusName, the status as a string, next to the integer
// Status, which stays for compatibility.
func (a Agent) MarshalJSON() ([]byte, error) {
	type fields Agent
	return json.Marshal(struct {
		fields
		StatusName string
	}{fields(a), a.Status.Key()})
}

// Task is the current task label, falling back to the transcript title and
// then Claude Code's own session name.
func (a Agent) Task() string {
	switch {
	case a.Labels.Task != "":
		return a.Labels.Task
	case a.Title != "":
		return a.Title
	}
	return a.Session.Name
}

// Where renders "repo · branch" or the short cwd.
func (a Agent) Where() string {
	if a.HasGit && a.Git.Branch != "" {
		return a.Git.Repo + " · " + a.Git.Branch
	}
	if a.HasGit {
		return a.Git.Repo
	}
	return a.Session.ShortCWD()
}

// Since is how long the agent has been in its current status.
func (a Agent) Since() time.Duration {
	return time.Since(a.Session.StatusChanged())
}

// Place says which terminal window the agent is in, if known.
func (a Agent) Place() string {
	if a.Window == nil {
		if a.InHerdr {
			return "herdr"
		}
		return ""
	}
	class := a.Window.Client.Class
	if i := strings.LastIndex(class, "."); i >= 0 {
		class = class[i+1:]
	}
	s := fmt.Sprintf("%s · ws %s", class, a.Window.Client.Workspace.Name)
	if a.InHerdr {
		s = "herdr in " + s
	}
	return s
}

// mergeUsage prefers the context figures agent-badge recorded, which Claude
// Code reports to it directly, over the transcript estimate, whose window is
// a guess (a 1M-window model that is still under 200k reads as nearly full).
func mergeUsage(u transcript.Usage, has bool, l badge.Usage, hasL bool) (transcript.Usage, bool, bool) {
	if !hasL || l.Window == nil || *l.Window <= 0 {
		return u, has, false
	}
	u.Window = int(*l.Window)
	switch {
	case l.Filled != nil:
		u.Tokens = int(*l.Filled)
	case l.Context != nil:
		u.Tokens = int(*l.Context * *l.Window / 100)
	default:
		return u, has, false
	}
	return u, true, true
}

// Snapshot gathers every agent. Window lookup is skipped when Hyprland is not
// available, and a failing lookup never fails the snapshot.
// Recent is how many activity entries Snapshot reads per agent; main sets it
// from the config file.
var Recent = 20

func Snapshot() ([]Agent, error) {
	sessions, err := discover.Scan()
	if err != nil {
		return nil, err
	}
	labels, _ := badge.Load()
	limits := badge.LoadUsage()
	var clients []hypr.Client
	if hypr.Available() {
		clients, _ = hypr.Clients()
	}
	host, _ := os.Hostname()

	agents := make([]Agent, 0, len(sessions))
	for _, s := range sessions {
		a := Agent{Session: s, Name: badge.Name(s.SessionID), Labels: labels[s.SessionID]}
		if u, ok := limits[s.SessionID]; ok {
			a.Limits, a.HasLimit = u, true
		}
		if s.CWD != "" {
			a.Git, a.HasGit = gitinfo.Lookup(s.CWD)
		}
		a.Path = transcript.Path(s.SessionID)
		facts := transcript.Read(a.Path)
		a.Title, a.Model = facts.Title, facts.Model
		if a.Path != "" {
			a.Usage, a.HasUsage = transcript.ReadUsage(a.Path)
			a.Tail = transcript.Tail(a.Path, Recent)
		}
		a.Usage, a.HasUsage, a.UsageFromBadge = mergeUsage(a.Usage, a.HasUsage, a.Limits, a.HasLimit)
		switch {
		case !s.Alive:
			a.Status = Gone
		case s.Status == "busy":
			a.Status = Working
		case s.Status == "idle":
			a.Status = Idle
		default:
			a.Status = Unknown
		}
		if s.Alive {
			a.InHerdr = procs.HasAncestor(s.PID, "herdr")
		}
		if len(clients) > 0 {
			prefix := ""
			if a.InHerdr {
				prefix = host + ": "
			}
			if m, ok := hypr.Find(clients, a.Title, s.ShortCWD(), prefix); ok {
				a.Window = &m
			}
		}
		agents = append(agents, a)
	}
	sort.SliceStable(agents, func(i, j int) bool {
		if agents[i].Status != agents[j].Status {
			return agents[i].Status < agents[j].Status
		}
		return agents[i].Session.StartedAt < agents[j].Session.StartedAt
	})
	return agents, nil
}

// Focus brings the agent's terminal to the front. Inside herdr it also asks
// herdr to focus the pane. It returns a short description of what it did.
func Focus(a Agent) (string, error) {
	if a.Status == Gone {
		return "", errors.New("that agent is gone")
	}
	var notes []string
	if a.InHerdr {
		if pane, err := herdrPane(a.Session.SessionID); err == nil && pane != "" {
			if err := exec.Command("herdr", "agent", "focus", pane).Run(); err == nil {
				notes = append(notes, "herdr pane "+pane)
			}
		}
	}
	if a.Window == nil {
		if len(notes) > 0 {
			return strings.Join(notes, ", "), nil
		}
		return "", fmt.Errorf("no terminal window found for %s", a.Name)
	}
	if err := hypr.Focus(a.Window.Client.Address); err != nil {
		return "", err
	}
	notes = append(notes, fmt.Sprintf("window on workspace %s (matched by %s)", a.Window.Client.Workspace.Name, a.Window.How))
	return strings.Join(notes, ", "), nil
}

// herdrPane maps a Claude session id to the herdr pane hosting it.
func herdrPane(sessionID string) (string, error) {
	out, err := exec.Command("herdr", "agent", "list").Output()
	if err != nil {
		return "", err
	}
	var resp struct {
		Result struct {
			Agents []struct {
				PaneID  string `json:"pane_id"`
				Session struct {
					Value string `json:"value"`
				} `json:"agent_session"`
			} `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", err
	}
	for _, ag := range resp.Result.Agents {
		if ag.Session.Value == sessionID {
			return ag.PaneID, nil
		}
	}
	return "", nil
}

// Explain says how Den arrived at what it shows for an agent.
func Explain(a Agent) []string {
	var out []string
	add := func(f string, args ...any) { out = append(out, fmt.Sprintf(f, args...)) }
	add("name      %s, generated from the session id %s the way agent-badge does", a.Name, a.Session.SessionID)
	if a.Labels.Role != "" || a.Labels.Task != "" {
		add("labels    role %q and task %q from the agent-badge plugin store", a.Labels.Role, a.Labels.Task)
	} else {
		add("labels    none: agent-badge has not labelled this session (task falls back to the transcript title, then the session name)")
	}
	switch a.Status {
	case Gone:
		add("status    gone: pid %d is not running, the sessions file is stale", a.Session.PID)
	case NeedsYou:
		add("status    needs you: the hook bridge reported something waiting on the human")
	case Working, Idle:
		add("status    %s: %s/%d.json says %q, updated %s ago", a.Status, discover.Dir(), a.Session.PID, a.Session.Status, humanAge(a.Since()))
	default:
		add("status    unknown: the sessions file has no busy/idle status")
	}
	if a.HasGit {
		add("where     repo %q on branch %q from .git under %s", a.Git.Repo, a.Git.Branch, a.Session.CWD)
	} else {
		add("where     %s (no git checkout found above it)", a.Session.CWD)
	}
	if a.Path != "" {
		add("transcript %s", a.Path)
		if a.Title != "" {
			add("title     %q from the last ai-title record; Claude Code shows it as the terminal title", a.Title)
		}
		if a.HasUsage {
			src := "from the last assistant message's usage (window guessed)"
			if a.UsageFromBadge {
				src = "as agent-badge recorded them from Claude Code"
			}
			add("context   %d of %d tokens (%d%%) %s", a.Usage.Tokens, a.Usage.Window, a.Usage.Percent(), src)
		}
	} else {
		add("transcript none found under %s", transcript.ProjectsDir())
	}
	if a.InHerdr {
		add("herdr     a herdr process is among pid %d's ancestors, so Enter also asks herdr to focus the pane", a.Session.PID)
	}
	switch {
	case a.Window != nil:
		add("window    %s on workspace %s, matched by %s", a.Window.Client.Class, a.Window.Client.Workspace.Name, a.Window.How)
	case !hypr.Available():
		add("window    not looked up: Hyprland is not available here")
	default:
		add("window    no terminal window matched the title or the cwd")
	}
	if a.Session.Socket != "" {
		add("messages  delivered to %s", a.Session.Socket)
	} else {
		add("messages  no inbox socket; this session cannot receive messages")
	}
	return out
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

// Find returns the agent whose name, session id or pid matches query. Names
// match case-insensitively on a prefix ("mossy", "Mossy Lemur").
func Find(agents []Agent, query string) (Agent, bool) {
	q := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, "@")))
	if q == "" {
		return Agent{}, false
	}
	for _, a := range agents {
		if strings.HasPrefix(a.Session.SessionID, q) || fmt.Sprint(a.Session.PID) == q {
			return a, true
		}
	}
	for _, a := range agents {
		if strings.HasPrefix(strings.ToLower(a.Name), q) {
			return a, true
		}
	}
	return Agent{}, false
}

// TouchedFolders lists where the agent's recent file changes landed, as
// project folders (two levels under home, or the parent otherwise), most
// recent first, so a reader can tell what an agent sitting in ~ works on.
func TouchedFolders(a Agent) []string {
	home, _ := os.UserHomeDir()
	seen := map[string]bool{}
	var out []string
	for i := len(a.Tail) - 1; i >= 0; i-- {
		for _, c := range a.Tail[i].Changes {
			dir := filepath.Dir(c.Path)
			if home != "" && strings.HasPrefix(dir, home+"/") {
				parts := strings.Split(strings.TrimPrefix(dir, home+"/"), "/")
				if len(parts) > 2 {
					parts = parts[:2]
				}
				dir = "~/" + strings.Join(parts, "/")
			}
			if !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
		}
		if len(out) == 4 {
			break
		}
	}
	return out
}

// RecentActivity is the agent's latest words and latest tool call, one line
// each, newest first.
func RecentActivity(a Agent) []string {
	var out []string
	gotText, gotTool := false, false
	for i := len(a.Tail) - 1; i >= 0 && !(gotText && gotTool); i-- {
		e := a.Tail[i]
		switch {
		case e.Kind == "text" && !gotText:
			out = append(out, "said: "+clip(firstLine(e.Text), 140))
			gotText = true
		case e.Kind == "tool" && !gotTool:
			call := e.Tool
			if e.Text != "" {
				call += " " + firstLine(e.Text)
			} else if e.Code != "" {
				call += " " + firstLine(e.Code)
			}
			out = append(out, "ran: "+clip(call, 140))
			gotTool = true
		}
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return s
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
