// Package spawn opens a terminal on the workspace the human is looking at,
// optionally with a Claude Code session in it that starts on a brief. It is
// what the orchestrator's spawn_agent and open_terminal tools do.
//
// The terminal is launched the way Omarchy's own keybinding does it:
// `setsid uwsm-app -- xdg-terminal-exec --dir=<dir> -- <command>`, so it is
// the user's default terminal, detached from Den, and Hyprland places it on
// the active workspace. Den records that workspace before launching and
// moves the window there if it landed anywhere else.
package spawn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/discover"
	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/permmode"
	"github.com/GlacierHubAB/den/internal/view"
	"github.com/GlacierHubAB/den/internal/winmark"
	"github.com/GlacierHubAB/den/internal/worktree"
)

// Request says what to open.
type Request struct {
	Dir          string // working directory; "~" is expanded; must exist
	Agent        bool   // start Claude Code in the terminal
	Brief        string // the agent's first prompt (Agent only)
	Name         string // the agent's peer name; generated when empty
	Orchestrator string // peer name of Den's orchestrator
	// Lead is the peer name of the agent that is starting this one, when it
	// is not the orchestrator: the new agent reports to its lead.
	Lead string
	// Model is the claude --model value for the agent ("" for the default).
	// Models says which ones the tool offers and when.
	Model string
	// AddDirs are given to claude with --add-dir: the agent's private
	// skills folder, when the human approved skills for it.
	AddDirs []string
	// Worktree starts the agent in a git worktree of its own, made by
	// Claude Code (claude --worktree) under <repo>/.claude/worktrees: "" for
	// none, WorktreeAuto for one named after the agent, else the name.
	Worktree Worktree
	// Spawner is the Den binary; when set, the agent gets Den's MCP server
	// and so the spawn_agent tool, to start teammates of its own.
	Spawner string
	// Resume is the session id of a conversation to reopen with claude
	// --resume instead of starting on a brief; Fork gives the reopened
	// conversation a new session id (--fork-session).
	Resume string
	Fork   bool
	// PermissionMode is the claude --permission-mode the agent starts in;
	// "" is default, never Claude Code's own default (auto). Den gives its
	// global mode.
	PermissionMode string
	// Custom, when set, is the whole command for the agent named name, in
	// place of Command's: a desktop agent is launched like the
	// orchestrator, with its own tools, settings and prompt.
	Custom func(name string) []string
	// NoSessionWait returns once the window is up, without waiting for a
	// session to register: a print-mode agent may never write one.
	NoSessionWait bool

	// SessionID is the session id a new agent starts with (claude
	// --session-id), "" for Claude Code's own; never with Resume. Settings
	// is a --settings file Den wrote for it (an absolute path), "" for
	// none. God mode gives both to an agent it guards.
	SessionID string
	Settings  string
}

// uuidRe is a session id as Claude Code takes one.
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Worktree is spawn_agent's worktree input: true (a worktree named after
// the agent), false, or the name to give it.
type Worktree string

// WorktreeAuto asks for a worktree named after the agent.
const WorktreeAuto Worktree = "\x00auto"

// UnmarshalJSON reads true, false, null or a string.
func (w *Worktree) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case bool:
		*w = ""
		if x {
			*w = WorktreeAuto
		}
	case string:
		*w = Worktree(strings.TrimSpace(x))
		if strings.EqualFold(string(*w), "true") {
			*w = WorktreeAuto
		} else if strings.EqualFold(string(*w), "false") {
			*w = ""
		}
	default:
		*w = ""
	}
	return nil
}

// Models the spawn tool offers, cheapest first, with when to pick each.
var Models = []struct{ Name, When string }{
	{"haiku", "small, mechanical, well-specified tasks: a sitemap, a lint pass, a file move"},
	{"sonnet", "ordinary building, writing and fixing with a clear brief"},
	{"opus", "work that needs judgment: design, architecture, product decisions, reviewing, leading other agents"},
}

// ModelNames lists the names in Models.
func ModelNames() []string {
	var out []string
	for _, m := range Models {
		out = append(out, m.Name)
	}
	return out
}

// ValidModel reports whether the tool offers the model.
func ValidModel(name string) bool {
	for _, m := range Models {
		if m.Name == name {
			return true
		}
	}
	return false
}

// Result says what happened.
type Result struct {
	Dir        string
	Name       string // peer name of the agent ("" for a plain terminal)
	Critter    string // the agent's critter name once its session registered
	SessionID  string
	Registered bool   // the session file appeared within the wait
	Model      string // the model it was started on, "" for the default
	// Worktree is the name of the git worktree the agent works in ("" when
	// it shares the folder), WorktreePath where Claude Code puts it and
	// WorktreeBranch the branch it is on.
	Worktree       string
	WorktreePath   string
	WorktreeBranch string
	Window         string // Hyprland address of the new terminal, "" when unknown
	Workspace      string // where the window is, "" when unknown
	Moved          bool   // it landed elsewhere and was moved to the active workspace
	// Untrusted means Claude Code has never been told to trust this folder,
	// so it is asking the human in the new terminal before starting.
	Untrusted bool
}

// Timeouts, overridable in tests.
var (
	WindowWait  = 8 * time.Second
	SessionWait = 45 * time.Second
	// HoldWait is how long, once a session with the agent's name shows,
	// Den waits for one that runs in its marked window.
	HoldWait  = 5 * time.Second
	pollEvery = 300 * time.Millisecond
)

// Launch starts the terminal process; tests replace it. dir and title are
// for the terminal, marker is the window class for an agent's terminal
// ("" for none, see hypr.Marker), command is what runs inside it (empty
// for a shell) and env is added to the process environment.
var Launch = launch

// Run opens the terminal and, for an agent, waits for its session to
// register so the caller can name it.
func Run(ctx context.Context, req Request) (Result, error) {
	var res Result
	var sessions []discover.Session
	dir, err := ExpandDir(req.Dir)
	if err != nil {
		return res, err
	}
	res.Dir = dir
	var command []string
	if req.Agent {
		if strings.TrimSpace(req.Brief) == "" && req.Resume == "" {
			return res, errors.New("an agent needs a brief")
		}
		// A resumed agent keeps the exact model id its transcript names.
		if req.Model != "" && req.Resume == "" && !ValidModel(req.Model) {
			return res, fmt.Errorf("unknown model %q: pick one of %s", req.Model, strings.Join(ModelNames(), ", "))
		}
		if req.PermissionMode != "" && !permmode.Valid(req.PermissionMode) && req.PermissionMode != permmode.Bypass {
			return res, fmt.Errorf("unknown permission mode %q", req.PermissionMode)
		}
		if req.Settings != "" && !filepath.IsAbs(req.Settings) {
			return res, fmt.Errorf("the settings file must be an absolute path")
		}
		if req.SessionID != "" && (req.Resume != "" || !uuidRe.MatchString(req.SessionID)) {
			return res, fmt.Errorf("a session id is a UUID, for a new agent only")
		}
		sessions, _ = discover.Scan()
		res.Name = PeerName(req.Name, dir, sessions)
		res.Model = req.Model
		if req.Worktree != "" {
			name, path, err := planWorktree(dir, req.Worktree, res.Name)
			if err != nil {
				return res, err
			}
			req.Worktree = Worktree(name)
			res.Worktree, res.WorktreePath, res.WorktreeBranch = name, path, WorktreeBranch(name)
		}
		command = Command(req, res.Name)
		if req.Custom != nil {
			command = req.Custom(res.Name)
		}
	}
	// The agent's own spawns name it as their lead through this variable;
	// the orchestrator's marker must not leak into agents it starts.
	var env []string
	if res.Name != "" {
		env = append(env, "DEN_PEER="+res.Name)
	}

	// Where the human is right now is where the window must end up.
	var want hypr.Workspace
	var before []hypr.Client
	hyprOK := hypr.Available()
	if hyprOK {
		var ok bool
		if want, ok = hypr.ActiveWorkspace(); ok {
			res.Workspace = strconv.Itoa(want.ID)
		}
		before, _ = hypr.Clients()
	}
	title := "den"
	if res.Name != "" {
		// PeerName keeps names to safe characters; sanitised again since a
		// title is shown by the terminal and the window manager.
		title = hypr.AgentTitle(res.Name)
	}
	// The agent's terminal carries a window class of its own, the one way
	// Den may later tell its window from others with the same title or
	// directory. A class already on a live window is not reused: Den
	// could not tell the two apart.
	marker := ""
	if req.Agent {
		marker = freeMarker(res.Name, before, sessions)
	}
	started := time.Now()
	if err := Launch(dir, title, marker, command, env); err != nil {
		return res, err
	}
	// Closing trusts only a marker recorded here and bound to the session
	// below; without the record, Den never closes the window.
	if marker != "" {
		_ = winmark.Record(res.Name, marker)
	}

	if hyprOK {
		deadline := time.Now().Add(WindowWait)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			after, err := hypr.Clients()
			if err == nil {
				if c, ok := hypr.NewTerminal(before, after); ok {
					res.Window = c.Address
					res.Workspace = c.Workspace.Name
					if want.ID != 0 && c.Workspace.ID != want.ID {
						if hypr.MoveToWorkspace(c.Address, strconv.Itoa(want.ID)) == nil {
							res.Moved = true
							res.Workspace = want.Name
						}
					}
					break
				}
			}
			sleep(ctx, pollEvery)
		}
	}
	if !req.Agent {
		return res, nil
	}
	if req.NoSessionWait {
		return res, nil
	}
	if res.Untrusted = !Trusted(dir); res.Untrusted {
		// The trust dialog blocks the session until the human answers it;
		// waiting here would only stall the orchestrator.
		return res, nil
	}

	deadline := time.Now().Add(SessionWait)
	var seen time.Time // when a session with the name first showed
	for time.Now().Before(deadline) && ctx.Err() == nil {
		sessions, _ := discover.Scan()
		// Another session may have taken the name too. The one that runs
		// in the marked window is the agent, and only it gets the marker.
		// Den waits a little (HoldWait) for it, since a rival can register
		// first; then, when it still cannot tell, the first is the agent,
		// as before, and the marker stays unbound, so closing never
		// touches the window.
		var first *discover.Session
		holds := false
		for i, s := range sessions {
			if !s.Alive || s.Name != res.Name || s.Started().Before(started.Add(-time.Second)) {
				continue
			}
			if first == nil {
				first = &sessions[i]
			}
			if marker == "" || !hyprOK {
				break
			}
			pid := s.PID
			if ok, _ := winmark.Bind(res.Name, s.SessionID, func(mk string) bool { return hypr.MarkerHolds(mk, pid) }); ok {
				first, holds = &sessions[i], true
				break
			}
		}
		if first != nil && seen.IsZero() {
			seen = time.Now()
		}
		if first != nil && !holds && marker != "" && hyprOK && time.Since(seen) < HoldWait && time.Now().Add(pollEvery).Before(deadline) {
			sleep(ctx, pollEvery)
			continue
		}
		if first != nil {
			if holds {
				// Only a window bound to the session just now: in User view
				// it comes up out of sight.
				settleView(ctx, marker, first.PID)
			}
			res.SessionID = first.SessionID
			res.Critter = badge.Name(first.SessionID)
			res.Registered = true
			return res, nil
		}
		sleep(ctx, pollEvery)
	}
	return res, nil
}

// viewSwitcher is what hides a new agent's window in User view; tests
// replace it.
var viewSwitcher = view.New

// settleView hides the window the marker names when Den is in User view
// and the session's process runs in it (internal/view). It never delays the
// spawn by more than a few seconds and never fails it.
func settleView(ctx context.Context, marker string, pid int) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, _ = viewSwitcher().Settle(ctx, marker, pid)
}

// WorktreeBranch is the branch Claude Code gives a worktree called name.
func WorktreeBranch(name string) string { return "worktree-" + name }

// worktreeNote is added to the brief of an agent that has a worktree.
func worktreeNote(name string) string {
	return "You work in your own git worktree, on branch " + WorktreeBranch(name) + ". Commit your work there as you go. Do not merge, rebase onto or push to the main branch and do not switch branches in the main checkout: the human reviews your branch in Den and merges it."
}

// planWorktree decides the worktree's name and where Claude Code will put
// it, and checks the folder is a repository with a commit to branch from.
// A name asked for may reopen a worktree that exists (Claude Code does
// that); a name made from the agent's never does.
func planWorktree(dir string, want Worktree, agent string) (name, path string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := worktree.MainRoot(ctx, dir)
	if root == "" {
		return "", "", fmt.Errorf("worktree: %s is not inside a git repository", dir)
	}
	if err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}").Run(); err != nil {
		return "", "", fmt.Errorf("worktree: %s has no commit yet to branch from", root)
	}
	base := agent
	if want != WorktreeAuto {
		base = string(want)
	}
	base = strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(base), "-"), "-")
	if base == "" {
		base = "agent"
	}
	name = base
	for i := 2; want == WorktreeAuto; i++ {
		if _, err := os.Stat(filepath.Join(root, ".claude", "worktrees", name)); err != nil {
			break
		}
		name = base + "-" + strconv.Itoa(i)
	}
	return name, filepath.Join(root, ".claude", "worktrees", name), nil
}

// Describe is the tool result the orchestrator reads.
func (r Result) Describe() string {
	where := shortDir(r.Dir)
	ws := ""
	if r.Workspace != "" {
		ws = " on workspace " + r.Workspace
	}
	if r.Name == "" {
		return fmt.Sprintf("Opened a terminal in %s%s.", where, ws)
	}
	if r.Untrusted {
		return fmt.Sprintf("Opened a terminal in %s%s and started Claude Code there as peer %q with the brief as its first prompt. Claude Code has never worked in that folder, so it is first asking the human, in that terminal, whether to trust it; nothing happens until they answer. Tell the human. Once they have, the agent appears in the roster and your messages to %q reach it.", where, ws, r.Name, r.Name)
	}
	if !r.Registered {
		return fmt.Sprintf("Opened a terminal in %s%s and started Claude Code there as peer %q with the brief as its first prompt. The session has not registered yet; it will appear in the roster shortly, and your messages to %q reach it once it has.", where, ws, r.Name, r.Name)
	}
	model := ""
	if r.Model != "" {
		model = " on " + r.Model
	}
	tree := ""
	if r.Worktree != "" {
		tree = fmt.Sprintf(" It works in its own git worktree %s on branch %s; the human merges that branch in Den's Review.", shortDir(r.WorktreePath), r.WorktreeBranch)
	}
	return fmt.Sprintf("Started %s (peer %q) in %s%s%s. It has the brief as its first prompt and is working on it now; it appears in the roster as %s, and your messages to %q reach it.%s", r.Critter, r.Name, where, ws, model, r.Critter, r.Name, tree)
}

// Command is the claude command line for an agent named name.
func Command(req Request, name string) []string {
	command := []string{"claude", "--name", name}
	if req.Resume != "" {
		command = []string{"claude", "--resume", req.Resume}
		if req.Fork {
			command = append(command, "--fork-session")
		}
		command = append(command, "--name", name)
	} else if req.Worktree != "" && req.Worktree != WorktreeAuto {
		command = append(command, "--worktree", string(req.Worktree))
	}
	for _, d := range req.AddDirs {
		command = append(command, "--add-dir", d)
	}
	if req.Model != "" {
		command = append(command, "--model", req.Model)
	}
	// Always a mode, on a resume too: with none Claude Code starts in auto,
	// and a reopened agent starts in the mode in force now, not the one it
	// last ran in.
	mode := req.PermissionMode
	if mode == "" {
		mode = permmode.Default
	}
	command = append(command, "--permission-mode", mode)
	if req.Settings != "" {
		command = append(command, "--settings", req.Settings)
	}
	if req.SessionID != "" && req.Resume == "" {
		command = append(command, "--session-id", req.SessionID)
	}
	if req.Spawner != "" {
		// Den's server beside the user's own: the agent keeps its usual
		// tools and gains spawn_agent, pre-approved so a lead can staff its
		// work without a permission prompt per teammate.
		command = append(command, "--mcp-config", MCPConfig(req.Spawner), "--allowedTools", strings.Join(SpawnTools, ","))
	}
	if req.Resume != "" {
		return command // the conversation carries on; there is no new prompt
	}
	// --allowedTools takes a list, so the prompt after it would be read as
	// one more tool; "--" ends the options.
	prompt := Prompt(req.Brief, req.Orchestrator, req.Lead, req.Spawner != "")
	if req.Worktree != "" && req.Worktree != WorktreeAuto {
		prompt += "\n\n" + worktreeNote(string(req.Worktree))
	}
	return append(command, "--", prompt)
}

// The MCP server Den offers and the tools on it, as Claude Code names them.
const MCPServer = "den"

var SpawnTools = []string{"mcp__den__spawn_agent", "mcp__den__open_terminal", "mcp__den__desktop_mission"}

// MCPConfig is the --mcp-config JSON that registers `<exe> mcp` as the
// server named MCPServer.
func MCPConfig(exe string) string {
	b, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			MCPServer: map[string]any{"command": exe, "args": []string{"mcp"}},
		},
	})
	return string(b)
}

// Prompt is the agent's first prompt: the brief, introduced so the agent
// knows who sent it, where to report, and whether it may start teammates.
func Prompt(brief, orchestrator, lead string, canSpawn bool) string {
	if orchestrator == "" {
		orchestrator = "den-orchestrator"
	}
	var b strings.Builder
	if lead != "" {
		fmt.Fprintf(&b, "Brief from your lead (peer %q), who works for the Den orchestrator (peer %q) and for your human, who follows your work from Den. Permission prompts and questions reach them there. When you are done, or blocked, report back in a few lines with SendMessage to your lead, %q.", lead, orchestrator, lead)
	} else {
		fmt.Fprintf(&b, "Brief from the Den orchestrator (peer %q), dispatched for your human, who follows your work from Den. Permission prompts and questions reach them there. When you are done, or blocked, report back in a few lines with SendMessage to that peer.", orchestrator)
	}
	if canSpawn {
		b.WriteString(" You have Den's spawn_agent tool: when a piece of the work deserves an agent of its own, start one in its own terminal with a complete brief, and pick its model by how hard its task is (")
		var when []string
		for _, m := range Models {
			when = append(when, m.Name+" for "+m.When)
		}
		b.WriteString(strings.Join(when, "; "))
		b.WriteString("). Give it a role in a few words (\"coding agent\", \"ui designer\"): Den then offers the human the vetted skills that fit, for that agent only. Agents you start report to you; wait for all of them before you report back yourself.")
	}
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(brief))
	return b.String()
}

// Trusted reports whether Claude Code has recorded the human's answer to
// its "do you trust this folder?" dialog for dir or a folder above it. It
// reads the projects map in .claude.json ($CLAUDE_CONFIG_DIR, else $HOME);
// an unreadable file counts as untrusted.
func Trusted(dir string) bool {
	path := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".claude.json")
	if os.Getenv("CLAUDE_CONFIG_DIR") == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".claude.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cfg struct {
		Projects map[string]struct {
			Trusted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return false
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if p, ok := cfg.Projects[d]; ok && p.Trusted {
			return true
		}
		if d == filepath.Dir(d) {
			return false
		}
	}
}

// ExpandDir resolves ~ and relative paths and checks the directory exists.
func ExpandDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", errors.New("dir is required")
	}
	home, _ := os.UserHomeDir()
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(home, dir)
	}
	dir = filepath.Clean(dir)
	st, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("no such directory: %s", dir)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("not a directory: %s", dir)
	}
	return dir, nil
}

var unsafeName = regexp.MustCompile(`[^a-z0-9]+`)

// PeerName returns a session name no running session uses: the requested
// one (cleaned) with a suffix when taken, else "<folder>-<4 hex>".
func PeerName(want, dir string, sessions []discover.Session) string {
	taken := map[string]bool{}
	for _, s := range sessions {
		if s.Alive {
			taken[s.Name] = true
		}
	}
	base := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(want), "-"), "-")
	if base != "" {
		if !taken[base] {
			return base
		}
		for i := 2; ; i++ {
			if n := base + "-" + strconv.Itoa(i); !taken[n] {
				return n
			}
		}
	}
	base = strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-")
	if base == "" {
		base = "agent"
	}
	for {
		b := make([]byte, 2)
		rand.Read(b)
		if n := base + "-" + hex.EncodeToString(b); !taken[n] {
			return n
		}
	}
}

// launch runs the terminal detached, the way Omarchy's terminal keybinding
// does: through uwsm-app when present, as its own session, with the default
// terminal chosen by xdg-terminal-exec. When that is Ghostty and the window
// has a marker, Ghostty is started directly, as a process of its own, so
// the window takes its class and its teardown cannot take other windows
// with it.
func launch(dir, title, marker string, command, extra []string) error {
	var argv []string
	if p, err := exec.LookPath("uwsm-app"); err == nil {
		argv = append(argv, p, "--")
	}
	if g, ok := ghosttyChosen(); ok && marker != "" {
		argv = append(argv, ghosttyArgs(g, dir, title, marker, command)...)
	} else if p, err := exec.LookPath("xdg-terminal-exec"); err == nil {
		argv = append(argv, p, "--dir="+dir, "--title="+title)
		if marker != "" {
			argv = append(argv, "--app-id="+marker)
		}
		if len(command) > 0 {
			argv = append(argv, "--")
			argv = append(argv, command...)
		}
	} else if t := os.Getenv("TERMINAL"); t != "" && t != "xdg-terminal-exec" {
		argv = append(argv, t)
		if len(command) > 0 {
			argv = append(argv, "-e")
			argv = append(argv, command...)
		}
	} else {
		return errors.New("no terminal launcher: install xdg-terminal-exec or set $TERMINAL")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = childEnv(os.Environ(), extra)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch terminal: %w", err)
	}
	go cmd.Wait() // reap; the terminal outlives us either way
	return nil
}

// freeMarker is the window class for the agent named name, or "" when a
// live window already has it or a live session Den spawned holds it.
func freeMarker(name string, live []hypr.Client, sessions []discover.Session) string {
	marker := hypr.Marker(name)
	for _, c := range live {
		if c.InitialClass == marker {
			return ""
		}
	}
	alive := func(id string) bool {
		for _, s := range sessions {
			if s.SessionID == id && s.Alive {
				return true
			}
		}
		return false
	}
	if winmark.Held(marker, alive) {
		return ""
	}
	return marker
}

// ghosttyArgs is the Ghostty command line for an agent's window: its own
// instance, the marker as its class, then the command as xdg-terminal-exec
// would have given it.
func ghosttyArgs(ghostty, dir, title, marker string, command []string) []string {
	argv := []string{ghostty, "--gtk-single-instance=false", "--class=" + marker, "--working-directory=" + dir, "--title=" + title}
	if len(command) > 0 {
		argv = append(argv, "-e")
		argv = append(argv, command...)
	}
	return argv
}

// ghosttyChosen reports whether xdg-terminal-exec would open Ghostty, and
// where Ghostty is; tests replace it.
var ghosttyChosen = func() (string, bool) {
	xte, err := exec.LookPath("xdg-terminal-exec")
	if err != nil {
		return "", false
	}
	g, err := exec.LookPath("ghostty")
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, xte, "--print-id").Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), "com.mitchellh.ghostty") {
		return "", false
	}
	return g, true
}

// inherited are the variables a Claude Code session sets for the processes
// it runs: its id, its messaging socket, its child-session flag. The spawn
// tool runs under a session, and a new session that inherits them takes
// itself for a child of it and stops registering and saving transcripts.
// Den's own markers go too: the orchestrator's must not leak, and the
// agent gets its own peer name.
var inherited = []string{
	"CLAUDECODE=", "CLAUDE_PID=", "CLAUDE_EFFORT=",
	"CLAUDE_CODE_SESSION_", "CLAUDE_CODE_MESSAGING_", "CLAUDE_CODE_CHILD_SESSION=",
	"CLAUDE_CODE_ENTRYPOINT=", "CLAUDE_CODE_EXECPATH=",
	"DEN_ORCHESTRATOR=", "DEN_PEER=",
}

// childEnv is the environment for the new terminal: the current one
// without what a parent session set, plus extra.
func childEnv(environ, extra []string) []string {
	var env []string
next:
	for _, kv := range environ {
		for _, p := range inherited {
			if strings.HasPrefix(kv, p) {
				continue next
			}
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

func shortDir(dir string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(dir, home) {
		return "~" + strings.TrimPrefix(dir, home)
	}
	return dir
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
