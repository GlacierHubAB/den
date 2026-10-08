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
	Orchestrator string // peer name the agent is told to report to
}

// Result says what happened.
type Result struct {
	Dir        string
	Name       string // peer name of the agent ("" for a plain terminal)
	Critter    string // the agent's critter name once its session registered
	SessionID  string
	Registered bool   // the session file appeared within the wait
	Window     string // Hyprland address of the new terminal, "" when unknown
	Workspace  string // where the window is, "" when unknown
	Moved      bool   // it landed elsewhere and was moved to the active workspace
}

// Timeouts, overridable in tests.
var (
	WindowWait  = 8 * time.Second
	SessionWait = 45 * time.Second
	pollEvery   = 300 * time.Millisecond
)

// Launch starts the terminal process; tests replace it. dir and title are
// for the terminal, command is what runs inside it ("" for a shell).
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
		sessions, _ := discover.Scan()
		res.Name = PeerName(req.Name, dir, sessions)
		command = []string{"claude", "--name", res.Name, Prompt(req.Brief, req.Orchestrator)}
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
	if err := Launch(dir, title, command); err != nil {
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
	if !r.Registered {
		return fmt.Sprintf("Opened a terminal in %s%s and started Claude Code there as peer %q with the brief as its first prompt. The session has not registered yet; it will appear in the roster shortly, and SendMessage to %q reaches it once it has.", where, ws, r.Name, r.Name)
	}
	return fmt.Sprintf("Started %s (peer %q) in %s%s. It has the brief as its first prompt and is working on it now; it appears in the roster as %s, and SendMessage to %q reaches it.", r.Critter, r.Name, where, ws, r.Critter, r.Name)
}

// Prompt is the agent's first prompt: the brief, introduced so the agent
// knows who sent it and where to report.
func Prompt(brief, orchestrator string) string {
	if orchestrator == "" {
		orchestrator = "den-orchestrator"
	}
	return fmt.Sprintf("Brief from the Den orchestrator (peer %q), dispatched for your human, who follows your work from Den. Permission prompts and questions reach them there. When you are done, or blocked, report back in a few lines with SendMessage to that peer.\n\n%s", orchestrator, strings.TrimSpace(brief))
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
func launch(dir, title string, command []string) error {
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
		if !strings.HasPrefix(kv, "DEN_ORCHESTRATOR=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
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
