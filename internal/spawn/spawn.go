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
	// Spawner is the Den binary; when set, the agent gets Den's MCP server
	// and so the spawn_agent tool, to start teammates of its own.
	Spawner string
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
	Window     string // Hyprland address of the new terminal, "" when unknown
	Workspace  string // where the window is, "" when unknown
	Moved      bool   // it landed elsewhere and was moved to the active workspace
	// Untrusted means Claude Code has never been told to trust this folder,
	// so it is asking the human in the new terminal before starting.
	Untrusted bool
}

// Timeouts, overridable in tests.
var (
	WindowWait  = 8 * time.Second
	SessionWait = 45 * time.Second
	pollEvery   = 300 * time.Millisecond
)

// Launch starts the terminal process; tests replace it. dir and title are
// for the terminal, command is what runs inside it (empty for a shell) and
// env is added to the process environment.
var Launch = launch

// Run opens the terminal and, for an agent, waits for its session to
// register so the caller can name it.
func Run(ctx context.Context, req Request) (Result, error) {
	var res Result
	dir, err := ExpandDir(req.Dir)
	if err != nil {
		return res, err
	}
	res.Dir = dir
	var command []string
	if req.Agent {
		if strings.TrimSpace(req.Brief) == "" {
			return res, errors.New("an agent needs a brief")
		}
		if req.Model != "" && !ValidModel(req.Model) {
			return res, fmt.Errorf("unknown model %q: pick one of %s", req.Model, strings.Join(ModelNames(), ", "))
		}
		sessions, _ := discover.Scan()
		res.Name = PeerName(req.Name, dir, sessions)
		res.Model = req.Model
		command = Command(req, res.Name)
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
		title = "den · " + res.Name
	}
	started := time.Now()
	if err := Launch(dir, title, command, env); err != nil {
		return res, err
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
	if res.Untrusted = !Trusted(dir); res.Untrusted {
		// The trust dialog blocks the session until the human answers it;
		// waiting here would only stall the orchestrator.
		return res, nil
	}

	deadline := time.Now().Add(SessionWait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		sessions, _ := discover.Scan()
		for _, s := range sessions {
			if s.Alive && s.Name == res.Name && !s.Started().Before(started.Add(-time.Second)) {
				res.SessionID = s.SessionID
				res.Critter = badge.Name(s.SessionID)
				res.Registered = true
				return res, nil
			}
		}
		sleep(ctx, pollEvery)
	}
	return res, nil
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
		return fmt.Sprintf("Opened a terminal in %s%s and started Claude Code there as peer %q with the brief as its first prompt. Claude Code has never worked in that folder, so it is first asking the human, in that terminal, whether to trust it; nothing happens until they answer. Tell the human. Once they have, the agent appears in the roster and SendMessage to %q reaches it.", where, ws, r.Name, r.Name)
	}
	if !r.Registered {
		return fmt.Sprintf("Opened a terminal in %s%s and started Claude Code there as peer %q with the brief as its first prompt. The session has not registered yet; it will appear in the roster shortly, and SendMessage to %q reaches it once it has.", where, ws, r.Name, r.Name)
	}
	model := ""
	if r.Model != "" {
		model = " on " + r.Model
	}
	return fmt.Sprintf("Started %s (peer %q) in %s%s%s. It has the brief as its first prompt and is working on it now; it appears in the roster as %s, and SendMessage to %q reaches it.", r.Critter, r.Name, where, ws, model, r.Critter, r.Name)
}

// Command is the claude command line for an agent named name.
func Command(req Request, name string) []string {
	command := []string{"claude", "--name", name}
	if req.Model != "" {
		command = append(command, "--model", req.Model)
	}
	if req.Spawner != "" {
		// Den's server beside the user's own: the agent keeps its usual
		// tools and gains spawn_agent, pre-approved so a lead can staff its
		// work without a permission prompt per teammate.
		command = append(command, "--mcp-config", MCPConfig(req.Spawner), "--allowedTools", strings.Join(SpawnTools, ","))
	}
	return append(command, Prompt(req.Brief, req.Orchestrator, req.Lead, req.Spawner != ""))
}

// The MCP server Den offers and the tools on it, as Claude Code names them.
const MCPServer = "den"

var SpawnTools = []string{"mcp__den__spawn_agent", "mcp__den__open_terminal"}

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
		b.WriteString("). Agents you start report to you; wait for all of them before you report back yourself.")
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
// terminal chosen by xdg-terminal-exec.
func launch(dir, title string, command, extra []string) error {
	var argv []string
	if p, err := exec.LookPath("uwsm-app"); err == nil {
		argv = append(argv, p, "--")
	}
	if p, err := exec.LookPath("xdg-terminal-exec"); err == nil {
		argv = append(argv, p, "--dir="+dir, "--title="+title)
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
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DEN_ORCHESTRATOR=") && !strings.HasPrefix(kv, "DEN_PEER=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, extra...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch terminal: %w", err)
	}
	go cmd.Wait() // reap; the terminal outlives us either way
	return nil
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
