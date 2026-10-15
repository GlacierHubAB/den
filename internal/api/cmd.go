package api

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Den's commands from the dock: a console lists them (cmd.list), completes
// what it is typing (cmd.complete) and runs one (cmd.run). Den keeps the
// list and decides every line: den-api.sock only checks the shape and the
// limits, and hands the line to Den (Act.Command), which maps it onto one
// command of a closed list or refuses it.

// The command limits.
const (
	cmdTextMax      = 512
	cmdRunsMax      = 30 // a minute, per console
	cmdCompleteMax  = 10 // a second, per connection
	cmdCompleteList = 64
	cmdEntryMax     = 120
)

// The dock flags of a command (CommandInfo.Dock).
const (
	DockOK        = "ok"               // runs from the dock
	DockCard      = "card"             // runs, and asks the human on a card
	DockRefused   = "refused"          // never from the dock
	DockDenScreen = "needs_den_screen" // acts on Den's own screen
)

// CommandInfo is one command as the dock may list it.
type CommandInfo struct {
	Name  string `json:"name"`
	Usage string `json:"usage"`
	Help  string `json:"help"`
	Dock  string `json:"dock"`
}

// CommandRun is a console's command for Den to decide and run; Den replies
// on Reply.
type CommandRun struct {
	Text    string
	Console string
	Reply   chan CommandResult
}

// CommandResult is Den's outcome: Code "" for done, with Den's words in
// Note; else a refusal code and why.
type CommandResult struct {
	Code string
	Note string
}

// cmdLimits are a console's command limits.
type cmdLimits struct {
	mu   sync.Mutex
	runs []time.Time
}

func (l *cmdLimits) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	var kept []time.Time
	for _, t := range l.runs {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	l.runs = kept
	if len(kept) >= cmdRunsMax {
		return false
	}
	l.runs = append(l.runs, now)
	return true
}

// projectCommands cleans Den's command list: one line each, capped, and
// only the four flags.
func projectCommands(in []CommandInfo) []CommandInfo {
	out := []CommandInfo{}
	for _, c := range in {
		switch c.Dock {
		case DockOK, DockCard, DockRefused, DockDenScreen:
		default:
			continue
		}
		out = append(out, CommandInfo{
			Name:  sanitize.Label(c.Name, 40),
			Usage: sanitize.Label(c.Usage, cmdEntryMax),
			Help:  sanitize.Label(c.Help, cmdEntryMax),
			Dock:  c.Dock,
		})
	}
	return out
}

func (s *Server) commands() []CommandInfo {
	if v := s.view.Load(); v != nil {
		return v.Commands
	}
	return []CommandInfo{}
}

// cmdList is cmd.list: every command with its dock flag.
func (s *Server) cmdList(c *conn, req Request) {
	if s.consoleOf(c) == "" {
		c.fail(req.ID, CodeForbidden, "prove first")
		return
	}
	c.reply(req.ID, s.commands())
}

// cmdComplete is cmd.complete: the commands that start with what the
// console has typed, refused ones left out; data that only fills the input.
func (s *Server) cmdComplete(c *conn, req Request) {
	if s.consoleOf(c) == "" {
		c.fail(req.ID, CodeForbidden, "prove first")
		return
	}
	now := time.Now()
	c.mu.Lock()
	var kept []time.Time
	for _, t := range c.completes {
		if now.Sub(t) < time.Second {
			kept = append(kept, t)
		}
	}
	if len(kept) >= cmdCompleteMax {
		c.completes = kept
		c.mu.Unlock()
		c.fail(req.ID, CodeUnavailable, "too many completions")
		return
	}
	c.completes = append(kept, now)
	c.mu.Unlock()
	var p struct {
		Prefix string `json:"prefix"`
	}
	json.Unmarshal(req.Params, &p)
	prefix := strings.ToLower(norm.NFKC.String(strings.TrimSpace(p.Prefix)))
	if len(prefix) > cmdTextMax {
		c.fail(req.ID, CodeBadRequest, "prefix too long")
		return
	}
	out := []CommandInfo{}
	for _, cmd := range s.commands() {
		if cmd.Dock == DockRefused || !strings.HasPrefix(cmd.Name, prefix) {
			continue
		}
		out = append(out, cmd)
		if len(out) == cmdCompleteList {
			break
		}
	}
	c.reply(req.ID, out)
}

// cmdRun is cmd.run: one line, for Den to decide.
func (s *Server) cmdRun(c *conn, req Request) {
	if s.consoleOf(c) == "" {
		c.fail(req.ID, CodeForbidden, "prove first")
		return
	}
	if !s.consolePowers(c) {
		c.fail(req.ID, CodePowersLocked, PowersLocked)
		return
	}
	var p struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(req.Params, &p) != nil || strings.TrimSpace(p.Text) == "" || len(p.Text) > cmdTextMax {
		c.fail(req.ID, CodeBadRequest, "text: one command of at most 512 bytes")
		return
	}
	// Den's chat logs the line: a code in it is out, before it is kept.
	if s.leaked(p.Text) {
		c.fail(req.ID, CodeBadRequest, "the line holds a pairing or console code; Den cancelled it")
		return
	}
	if !s.cmdLimits.allow(time.Now()) {
		c.fail(req.ID, CodeUnavailable, "too many commands")
		return
	}
	r := &CommandRun{Text: p.Text, Console: ConsoleLabel, Reply: make(chan CommandResult, 1)}
	if !s.act(Act{Command: r}) {
		c.fail(req.ID, CodeUnavailable, ClaimBusy)
		return
	}
	go func() {
		select {
		case res := <-r.Reply:
			if res.Code != "" {
				c.fail(req.ID, res.Code, res.Note)
				return
			}
			c.reply(req.ID, map[string]any{"ok": true, "note": res.Note})
		case <-time.After(10 * time.Second):
			c.fail(req.ID, CodeUnavailable, "Den did not answer in time")
		case <-c.done:
		}
	}()
}
